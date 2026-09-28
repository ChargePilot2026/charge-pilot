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

var ErrCredentials = errors.New("用户名或密码错误")
var ErrLocked = errors.New("账号已锁定，请在 30 分钟后重试")
var ErrMFA = errors.New("此账号已启用双因素认证，当前登录入口暂不支持")

type Account struct {
	AuthVersion      uint64         `json:"-"`
	ID               uint64         `json:"admin_user_id"`
	Username         string         `json:"username"`
	DisplayName      sql.NullString `json:"-"`
	PasswordHash     string         `json:"-"`
	RoleID           uint64         `json:"role_id"`
	Status           string         `json:"-"`
	MFASecret        *string        `json:"-"`
	MFAEnabled       bool           `json:"-"`
	FailedLoginCount uint32         `json:"-"`
	LockedUntil      sql.NullTime   `json:"-"`
}

func (Account) TableName() string { return "admin_user_role" }

type Profile struct {
	AuthVersion uint64   `json:"-"`
	ID          uint64   `json:"admin_user_id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Role        string   `json:"role"`
	RoleID      uint64   `json:"role_id"`
	Permissions []string `json:"permissions"`
}

type Store struct{ DB *gorm.DB }

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
		// Only initialize a fresh installation. Never reset an existing password.
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
			// The password is correct but a second factor is required. The login
			// is not complete yet, so no session is issued and the success state
			// is only written once the code verifies.
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

// CompleteMFA verifies the second factor and only then marks the login
// successful. A wrong code counts toward the lockout so codes cannot be brute
// forced independently of the password.
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

func deniedMFA(tx *gorm.DB, err error, accountID uint64, denied *error, ip string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		*denied = ErrCredentials
		return nil
	}
	return err
}

func (s Store) Profile(ctx context.Context, id uint64) (Profile, error) {
	var row struct {
		Account
		Role string
	}
	err := s.DB.WithContext(ctx).Table("admin_user_role AS a").Select("a.*, r.code AS role").Joins("JOIN role AS r ON r.id=a.role_id AND r.deleted_at IS NULL").Where("a.id = ? AND a.status='active' AND a.deleted_at IS NULL", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Profile{}, ErrCredentials
	}
	if err != nil {
		return Profile{}, err
	}
	p := Profile{AuthVersion: row.AuthVersion, ID: row.ID, Username: row.Username, DisplayName: row.DisplayName.String, RoleID: row.RoleID, Role: row.Role, Permissions: []string{}}
	err = s.DB.WithContext(ctx).Table("permission AS p").Joins("JOIN role_permission AS rp ON rp.permission_id=p.id").Where("rp.role_id = ?", p.RoleID).Order("p.code").Pluck("p.code", &p.Permissions).Error
	return p, err
}

func audit(tx *gorm.DB, a Account, action, ip string) error {
	now := time.Now().UTC()
	return tx.Table("audit_log").Create(map[string]any{"actor_id": a.ID, "actor_name": a.Username, "module": "auth", "action": action, "target_type": "admin_user", "target_id": fmt.Sprint(a.ID), "client_ip": ip, "created_month": time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}).Error
}

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
