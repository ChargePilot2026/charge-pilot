package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 后台登录相关的三类失败原因。对外只区分"凭据错误"和"被锁定"，
// 避免通过报错文案区分"用户不存在"与"密码错误"，给暴力破解留线索。
var ErrCredentials = errors.New("用户名或密码错误")
var ErrLocked = errors.New("账号已锁定，请在 30 分钟后重试")
var ErrMFA = errors.New("此账号已启用双因素认证，当前登录入口暂不支持")

// Account 是一个后台账号在 admin_user_role 表上的行，同时承载登录判定所需的全部字段。
// 敏感字段一律 json："-"，不会随任何接口返回。
type Account struct {
	AuthVersion      uint64         `json:"-"`             // 凭证版本号：改密或强制下线时自增，用于让已签发的令牌立即失效
	ID               uint64         `json:"admin_user_id"` // 账号主键
	Username         string         `json:"username"`      // 登录名
	DisplayName      sql.NullString `json:"-"`             // 显示名；可空，缺失时前端回退到登录名
	PasswordHash     string         `json:"-"`             // bcrypt 口令散列，绝不出接口
	RoleID           uint64         `json:"role_id"`       // 所属角色 ID，权限由此推导
	Status           string         `json:"-"`             // 账号状态：active / disabled / locked
	MFASecret        *string        `json:"-"`             // TOTP 密钥；指针，未绑定时为 null
	MFAEnabled       bool           `json:"-"`             // 是否已启用双因素认证
	FailedLoginCount uint32         `json:"-"`             // 连续登录失败次数，达到 5 次锁定
	LockedUntil      sql.NullTime   `json:"-"`             // 锁定到期时间；可空，未锁定为 null
}

// TableName 指明 Account 映射到 admin_user_role（表名与模型名不一致，必须显式指定）。
func (Account) TableName() string { return "admin_user_role" }

// Profile 是已通过鉴权的操作人身份，挂在 gin 上下文的 admin_profile 上，
// 权限判断和审计记录都取自它。刻意不携带口令散列等任何敏感字段。
type Profile struct {
	AuthVersion uint64   `json:"-"`             // 凭证版本号，与账号一致，用于校验令牌是否已失效
	ID          uint64   `json:"admin_user_id"` // 账号主键
	Username    string   `json:"username"`      // 登录名
	DisplayName string   `json:"display_name"`  // 显示名
	Role        string   `json:"role"`          // 角色编码，如 customer_admin
	RoleName    string   `json:"role_name"`     // 当前角色名称，从 role 表读取，支持自定义角色。
	RoleID      uint64   `json:"role_id"`       // 角色主键
	MFAEnabled  bool     `json:"mfa_enabled"`   // 当前登录账号是否已启用双因素认证。
	Permissions []string `json:"permissions"`   // 权限码列表，按 code 排序；前端用它决定按钮可见性
}

// Store 是后台自身的数据库句柄，只连 admin_db；业务模块的库由 ResourceStore 分别持有。
type Store struct{ DB *gorm.DB }

// Bootstrap 在全新安装时创建第一个后台账号。
// 用户名与密码都为空表示不初始化；用户名与密码只给一半、或密码不在 12–72 字节之间，一律报错。
// 只在库里还没有任何账号时才创建，绝不覆盖已有账号的口令。
func (s Store) Bootstrap(ctx context.Context, username, password string) error {
	if username == "" && password == "" {
		return nil
	}
	if username == "" || len(username) > 64 || len(password) < 12 || len(password) > 72 {
		return errors.New("admin bootstrap requires username and 12–72 byte password")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var role struct{ ID uint64 }
		if err := tx.Table("role").Clauses(clause.Locking{Strength: "UPDATE"}).Where("code = 'customer_admin' AND deleted_at IS NULL").Take(&role).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&Account{}).Where("deleted_at IS NULL").Count(&count).Error; err != nil {
			return err
		}
		// 只初始化一套全新安装，绝不重置已有口令。
		if count > 0 {
			return nil
		}
		account := Account{Username: username, PasswordHash: string(hash), RoleID: role.ID, Status: "active"}
		if err := tx.Create(&account).Error; err != nil {
			return err
		}
		return audit(tx, account, "bootstrap", "")
	})
}

// Login 校验用户名与口令，成功时返回账号并写登录时间与审计。
// 连续 5 次失败即锁定 30 分钟，锁定到期后失败计数自动清零。
// 账号启用双因素认证时，口令正确也只返回账号、不写成功状态，由调用方接着走 MFA 流程。
// 用户名不存在与口令错误返回同一个错误，不给暴力破解区分线索。
func (s Store) Login(ctx context.Context, username, password, ip string) (Account, error) {
	var account Account
	var denied error
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("username = ? AND deleted_at IS NULL", username).Take(&account).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCredentials
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if account.Status == "disabled" {
			return ErrCredentials
		}
		if account.Status == "locked" && (!account.LockedUntil.Valid || account.LockedUntil.Time.After(now)) {
			return ErrLocked
		}
		attempts := account.FailedLoginCount
		if account.LockedUntil.Valid && !account.LockedUntil.Time.After(now) {
			attempts = 0
		}
		if bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)) != nil {
			attempts++
			updates := map[string]any{"failed_login_count": attempts, "status": "active", "locked_until": nil}
			denied = ErrCredentials
			if attempts >= 5 {
				updates["status"] = "locked"
				updates["locked_until"] = now.Add(30 * time.Minute)
				denied = ErrLocked
			}
			if err := tx.Model(&Account{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
				return err
			}
			return audit(tx, account, "login_failed", ip)
		}
		if account.MFAEnabled {
			// 口令是对的，但还差一个二次因素。
			// 这次登录还没走完，所以不发会话，
			// 成功状态也只在验证码校验通过之后才写。
			account.Status = "active"
			return nil
		}
		if err := tx.Model(&Account{}).Where("id = ?", account.ID).Updates(map[string]any{"failed_login_count": 0, "locked_until": nil, "status": "active", "last_login_at": now}).Error; err != nil {
			return err
		}
		account.Status = "active"
		return audit(tx, account, "login", ip)
	})
	if err != nil {
		return Account{}, err
	}
	if denied != nil {
		return Account{}, denied
	}
	return account, nil
}

// CompleteMFA 校验 TOTP 动态口令，通过后才把这次登录标记为成功并写登录时间。
// 动态口令错误同样计入失败次数，因此无法脱离口令单独暴力破解验证码。
//
// CompleteMFA 校验二次因素，通过之后才把这次登录标记为成功。
// 验证码填错同样计入锁定计数，
// 所以没法绕开口令单独暴力破解验证码。
func (s Store) CompleteMFA(ctx context.Context, accountID uint64, code, ip string) (Account, error) {
	var account Account
	var denied error
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", accountID).Take(&account).Error; err != nil {
			return deniedMFA(tx, err, accountID, &denied, ip)
		}
		now := time.Now().UTC()
		if account.Status == "disabled" || !account.MFAEnabled || account.MFASecret == nil || *account.MFASecret == "" {
			return ErrCredentials
		}
		if err := VerifyTOTP(*account.MFASecret, code, now); err != nil {
			attempts := account.FailedLoginCount + 1
			updates := map[string]any{"failed_login_count": attempts, "status": "active", "locked_until": nil}
			denied = ErrInvalidTOTP
			if attempts >= 5 {
				updates["status"] = "locked"
				updates["locked_until"] = now.Add(30 * time.Minute)
				denied = ErrLocked
			}
			if err := tx.Model(&Account{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
				return err
			}
			return audit(tx, account, "mfa_failed", ip)
		}
		if err := tx.Model(&Account{}).Where("id = ?", account.ID).Updates(map[string]any{"failed_login_count": 0, "locked_until": nil, "status": "active", "last_login_at": now}).Error; err != nil {
			return err
		}
		account.Status = "active"
		return audit(tx, account, "login_mfa", ip)
	})
	if err != nil {
		return Account{}, err
	}
	if denied != nil {
		return Account{}, denied
	}
	return account, nil
}

// deniedMFA 处理 MFA 阶段"账号查不到"的情况：记为凭据错误并让事务正常结束，
// 真正的错误照常向上返回。
func deniedMFA(tx *gorm.DB, err error, accountID uint64, denied *error, ip string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		*denied = ErrCredentials
		return nil
	}
	return err
}

// Profile 读取一个账号的登录身份与权限清单：先联表取角色编码，再按角色展开权限码。
// 只接受 active 且未删除的账号；查不到与被停用都返回凭据错误，避免泄漏账号是否存在。
func (s Store) Profile(ctx context.Context, id uint64) (Profile, error) {
	var row struct {
		Account
		Role     string
		RoleName string
	}
	err := s.DB.WithContext(ctx).Table("admin_user_role AS a").Select("a.*, r.code AS role,r.name AS role_name").Joins("JOIN role AS r ON r.id=a.role_id AND r.deleted_at IS NULL").Where("a.id = ? AND a.status='active' AND a.deleted_at IS NULL", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Profile{}, ErrCredentials
	}
	if err != nil {
		return Profile{}, err
	}
	p := Profile{AuthVersion: row.AuthVersion, ID: row.ID, Username: row.Username, DisplayName: row.DisplayName.String, RoleID: row.RoleID, Role: row.Role, RoleName: row.RoleName, MFAEnabled: row.MFAEnabled, Permissions: []string{}}
	err = s.DB.WithContext(ctx).Table("permission AS p").Joins("JOIN role_permission AS rp ON rp.permission_id=p.id").Where("rp.role_id = ?", p.RoleID).Order("p.code").Pluck("p.code", &p.Permissions).Error
	p.Permissions = withoutRetiredPermissions(p.Permissions)
	return p, err
}

// audit 写一条认证类审计（登录成功、失败、MFA、改密、初始化），
// 固定记在 auth 模块和 admin_user 对象上。created_month 是该表按月分区的键，必须一并写入。
func audit(tx *gorm.DB, a Account, action, ip string) error {
	now := time.Now().UTC()
	return tx.Table("audit_log").Create(map[string]any{"actor_id": a.ID, "actor_name": a.Username, "module": "auth", "action": action, "target_type": "admin_user", "target_id": fmt.Sprint(a.ID), "client_ip": ip, "created_month": time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}).Error
}

// ChangePassword 校验旧口令后改新口令，并把 auth_version 自增一，
// 使该账号已签发的令牌立即失效。旧口令错误返回凭据错误；
// 新口令必须与旧口令不同且在 12–72 字节之间（bcrypt 的上限）。
func (s Store) ChangePassword(ctx context.Context, id uint64, oldPassword, newPassword, ip string) error {
	if len(newPassword) < 12 || len(newPassword) > 72 || newPassword == oldPassword {
		return errors.New("new password must differ and contain 12–72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var a Account
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status='active' AND deleted_at IS NULL", id).Take(&a).Error; err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(oldPassword)) != nil {
			return ErrCredentials
		}
		if err := tx.Model(&Account{}).Where("id = ?", id).Updates(map[string]any{"password_hash": string(hash), "auth_version": gorm.Expr("auth_version+1")}).Error; err != nil {
			return err
		}
		return audit(tx, a, "change_password", ip)
	})
}
