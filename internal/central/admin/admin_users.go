package admin

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AdminUserRow 是 PC 后台管理员账号（admin_user_role 表）的列表行映射。
// 账号归属 admin_db，与终端用户 user_db.user 分开存放；一个账号一个主角色，权限由 role 展开。
type AdminUserRow struct {
	ID            uint64  `json:"id"`                 // 账号主键
	Username      string  `json:"username"`           // 登录用户名，全局唯一；本接口只读，不提供新建
	DisplayName   *string `json:"display_name"`       // 显示名称；nil 表示未设置
	RoleID        *uint64 `json:"role_id"`            // 主角色 ID；nil 表示尚未分配角色
	RoleCode      *string `json:"role_code"`          // 主角色编码（如 customer_admin），由 role 表联查得出；角色已删时为 nil
	Phone         *string `json:"phone"`              // 备用手机号；nil 表示未登记
	Email         *string `json:"email"`              // 邮箱；nil 表示未登记
	Status        string  `json:"status"`             // 账号状态：active 正常 / disabled 停用 / locked 因连续登录失败临时锁定
	MFAEnabled    bool    `json:"mfa_enabled"`        // 是否已启用双因素认证
	LastLoginAt   *string `json:"last_login_at"`      // 最近一次登录时间；nil 表示从未登录
	LockedUntil   *string `json:"locked_until"`       // 锁定到期时间；未锁定时为 nil
	FailedLogins  uint32  `json:"failed_login_count"` // 连续登录失败次数，成功登录或被解锁后归零
	DeletePending bool    `json:"-"`                  // 是否有待处理的下线流程，本列表不使用、不输出
}

// usernamePattern 是登录用户名的格式约束：字母、数字、下划线、点、短横，3–64 位。
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

// registerAdminUsers 注册管理员账号的六个后台接口，覆盖账号管理页的读写与安全操作。
// 改角色、解锁、改双因素用 admin_user.update，重置密码另用 admin_user.reset_password，
// 删除账号用 admin_user.delete——权限拆开是为了让"能改人"不等于"能删人"。
func (a ResourceAPI) registerAdminUsers(r *gin.Engine) {
	r.GET("/api/v1/admin/admin-users", a.Auth.Require("admin_user.read"), a.listAdminUsers)
	r.PUT("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.update"), a.updateAdminUser)
	r.DELETE("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.delete"), a.deleteAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/unlock", a.Auth.Require("admin_user.update"), a.unlockAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/reset-password", a.Auth.Require("admin_user.reset_password"), a.resetAdminPassword)
	r.POST("/api/v1/admin/admin-users/:id/mfa", a.Auth.Require("admin_user.update"), a.adminUserMFA)
}

// listAdminUsers 分页返回管理员账号列表，支持按状态（active / disabled / locked）和用户名/显示名关键词过滤，
// 并联查角色编码。本接口只读，不暴露密码哈希、MFA 密钥等敏感字段。
func (a ResourceAPI) listAdminUsers(c *gin.Context) {
	page, ok := parsePage(c, "active disabled locked")
	if !ok {
		return
	}
	out := Page[AdminUserRow]{Items: []AdminUserRow{}, Page: page.Page, PageSize: page.PageSize}
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user_role AS u").Where("u.deleted_at IS NULL")
	if page.Status != "" {
		query = query.Where("u.status = ?", page.Status)
	}
	if page.Keyword != "" {
		query = query.Where("u.username LIKE ? OR u.display_name LIKE ?", likePattern(page.Keyword), likePattern(page.Keyword))
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := query.Select("u.id,u.username,u.display_name,u.role_id,r.code AS role_code,u.phone,u.email,u.status,u.mfa_enabled,u.last_login_at,u.locked_until,u.failed_login_count").
		Joins("LEFT JOIN role AS r ON r.id = u.role_id AND r.deleted_at IS NULL").
		Order("u.id DESC").Offset((page.Page - 1) * page.PageSize).Limit(page.PageSize).Find(&out.Items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	for i := range out.Items {
		out.Items[i].MFAEnabled = normalizeBool(out.Items[i].MFAEnabled)
	}
	httpapi.OK(c, out)
}

// assignableRole 确认目标角色存在，
// 并且操作者没有授予超出自己范围的权限，
// 这样就无法通过新建或编辑账号来给自己提权。

// assignableRole 校验目标角色可以授予：角色必须存在且至少带一项权限，
// 并且每一项权限操作者自己都有——否则通过新建或编辑账号就能自我提权。
func (a ResourceAPI) assignableRole(p Profile, roleID uint64) error {
	var codes []string
	if err := a.Store.AdminDB.WithContext(context.Background()).Table("role AS r").
		Joins("JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id").
		Where("r.id=? AND r.deleted_at IS NULL", roleID).Pluck("p.code", &codes).Error; err != nil {
		return err
	}
	if len(codes) == 0 {
		return errConflict
	}
	for _, code := range codes {
		if !hasPermission(p, code) {
			return errInsufficient
		}
	}
	return nil
}

// errInsufficient 表示要授予的权限超出了操作者自己拥有的范围，由 assignableRole 判定。
var errInsufficient = errors.New("不能授予自己未拥有的权限")

// updateAdminUser 修改管理员账号的显示名、手机号、邮箱、角色和状态。
// 字段用指针表示"传了才改"；状态只允许 active / disabled。
// 两条硬约束：不能把自己停用或降级（避免把最后一个管理员锁在控制台外）；
// 改角色会顺带把 auth_version + 1，令该账号已签发的会话全部失效。
func (a ResourceAPI) updateAdminUser(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		DisplayName *string `json:"display_name"`
		Phone       *string `json:"phone"`
		Email       *string `json:"email"`
		RoleID      *uint64 `json:"role_id"`
		Status      *string `json:"status"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if in.DisplayName != nil && utf8.RuneCountInString(*in.DisplayName) > 128 {
		httpapi.BadRequest(c, "显示名称过长")
		return
	}
	if in.Phone != nil && len(*in.Phone) > 32 {
		httpapi.BadRequest(c, "手机号过长")
		return
	}
	if in.Email != nil && (len(*in.Email) > 128 || (*in.Email != "" && !strings.Contains(*in.Email, "@"))) {
		httpapi.BadRequest(c, "邮箱格式无效")
		return
	}
	if in.Status != nil && !oneOf(*in.Status, "active disabled") {
		httpapi.BadRequest(c, "账号状态无效")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	if in.RoleID != nil {
		if err := a.assignableRole(profile, *in.RoleID); err != nil {
			a.roleFailure(c, err)
			return
		}
	}
	values := map[string]any{}
	for key, value := range map[string]*string{"display_name": in.DisplayName, "phone": in.Phone, "email": in.Email, "status": in.Status} {
		if value != nil {
			values[key] = *value
		}
	}
	if in.RoleID != nil {
		values["role_id"] = *in.RoleID
		// 换角色必须让已存在的会话失效，
		// 否则旧权限会一直有效，直到令牌自然过期。
		values["auth_version"] = gorm.Expr("auth_version + 1")
	}
	if len(values) == 0 {
		httpapi.BadRequest(c, "没有可更新的字段")
		return
	}
	var before map[string]any
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("admin_user_role").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if id == profile.ID {
			// 不能让操作者把自己降级或停用，
			// 那会把最后一个管理员锁在控制台外面。
			if in.Status != nil && *in.Status == "disabled" {
				return errConflict
			}
			if in.RoleID != nil && *in.RoleID != before["role_id"] {
				return errConflict
			}
		}
		if err := tx.Table("admin_user_role").Where("id = ?", id).Updates(values).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "update", "admin_user", id, before, in, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// roleFailure 角色相关失败的出口：越权授予返回 403，其余走通用处理。
func (a ResourceAPI) roleFailure(c *gin.Context, err error) {
	if errors.Is(err, errInsufficient) {
		httpapi.Write(c, http.StatusForbidden, 1003, "不能授予自己未拥有的权限", nil)
		return
	}
	resourceFailure(c, err)
}

// deleteAdminUser 软删除管理员账号（离职场景，审计要留痕，不做物理删除）。
// 两条硬约束：不能删自己；删完之后系统里必须还剩至少一个 active 的客户管理员账号。
// 删除同时把状态置为 disabled 并让 auth_version + 1，已签发的会话随即失效。
func (a ResourceAPI) deleteAdminUser(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	if id == profile.ID {
		httpapi.Write(c, http.StatusConflict, 2009, "不能删除当前登录账号", nil)
		return
	}
	var remaining int64
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user_role AS u").
		Joins("JOIN role AS r ON r.id = u.role_id").
		Where("u.deleted_at IS NULL AND u.status = 'active' AND r.code = 'customer_admin' AND r.deleted_at IS NULL").
		Where("u.id <> ?", id).Count(&remaining).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if remaining == 0 {
		httpapi.Write(c, http.StatusConflict, 2009, "必须保留至少一个有效的客户管理员账号", nil)
		return
	}
	var before map[string]any
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("admin_user_role").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("admin_user_role").Where("id = ?", id).
			Updates(map[string]any{"deleted_at": time.Now().UTC(), "deleted_by": profile.ID, "status": "disabled", "auth_version": gorm.Expr("auth_version + 1")}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "delete", "admin_user", id, before, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "deleted": true})
}

// unlockAdminUser 解锁一个因连续登录失败被临时锁定的账号：
// 状态恢复 active、清空锁定到期时间、失败计数归零。
func (a ResourceAPI) unlockAdminUser(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before map[string]any
		if err := tx.Table("admin_user_role").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("admin_user_role").Where("id = ?", id).
			Updates(map[string]any{"status": "active", "locked_until": nil, "failed_login_count": 0}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "unlock", "admin_user", id, before, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "unlocked": true})
}

// resetAdminPassword 由管理员为账号重置密码，密码须为 12–72 字节，存 bcrypt 哈希。
// 重置同时让 auth_version + 1，该账号所有已签发会话立即失效，并清空锁定状态与失败计数。
func (a ResourceAPI) resetAdminPassword(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		NewPassword string `json:"new_password"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if len(in.NewPassword) < 12 || len(in.NewPassword) > 72 {
		httpapi.BadRequest(c, "新密码须为 12–72 字节")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err = a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before map[string]any
		if err := tx.Table("admin_user_role").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("admin_user_role").Where("id = ?", id).
			Updates(map[string]any{"password_hash": string(hash), "auth_version": gorm.Expr("auth_version + 1"), "failed_login_count": 0, "locked_until": nil}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "reset_password", "admin_user", id, before, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	// auth_version 加一本身就已经让所有已签发的会话失效了。
	httpapi.OK(c, gin.H{"id": id, "sessions_revoked": true})
}

// adminUserMFA 为某账号登记、确认或移除双因素。
// 密钥只有在操作者证明验证器确实能用之后才落库。

// adminUserMFA 为某账号启用、确认或关闭双因素认证（TOTP），按 action 分三种：
// enrol 生成密钥并以"未启用"状态落库（密钥在 confirm 之前不生效）；
// confirm 校验一次验证码通过后才真正置为启用；disable 清空密钥。
// 不允许对自己操作，必须走本人账号的安全设置，避免把验证器绑到错误的身份上。
// 三种动作成功时都会让 auth_version + 1，已签发会话随之失效。
func (a ResourceAPI) adminUserMFA(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	if id == profile.ID {
		httpapi.Write(c, http.StatusConflict, 2009, "请使用本人账号的安全设置修改双因素认证", nil)
		return
	}
	// 登记用的标签必须写被保护的那个账号，而不是操作者自己，
	// 否则验证器 App 可能被绑到错误的身份上。
	var target struct {
		Username string `gorm:"column:username"`
	}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user_role").
		Select("username").Where("id = ? AND deleted_at IS NULL", id).Take(&target).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	var in struct {
		Action string `json:"action"`
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if !decodeResource(c, &in) {
		return
	}
	now := time.Now().UTC()
	switch in.Action {
	case "enrol":
		secret, err := NewTOTPSecret()
		if err != nil {
			resourceFailure(c, err)
			return
		}
		uri, err := TOTPURI("ChargePilot", target.Username, secret)
		if err != nil {
			resourceFailure(c, err)
			return
		}
		code, err := TOTPCode(secret, now)
		if err != nil {
			resourceFailure(c, err)
			return
		}
		// 密钥以未启用状态存下来，只有 confirm 之后才真正生效。
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user_role").
			Where("id = ? AND deleted_at IS NULL", id).
			Updates(map[string]any{"mfa_secret": secret, "mfa_enabled": false}).Error; err != nil {
			resourceFailure(c, err)
			return
		}
		httpapi.OK(c, gin.H{"secret": secret, "otpauth_uri": uri, "code": code, "enabled": false})
	case "confirm":
		if in.Secret == "" || in.Code == "" {
			httpapi.BadRequest(c, "请提供密钥和验证码")
			return
		}
		if err := VerifyTOTP(in.Secret, in.Code, now); err != nil {
			httpapi.Write(c, http.StatusBadRequest, 1002, "验证码校验失败", nil)
			return
		}
		err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			var before map[string]any
			if err := tx.Table("admin_user_role").Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
				return err
			}
			if err := tx.Table("admin_user_role").Where("id = ?", id).
				Updates(map[string]any{"mfa_secret": in.Secret, "mfa_enabled": true, "auth_version": gorm.Expr("auth_version + 1")}).Error; err != nil {
				return err
			}
			return resourceAudit(tx, profile, "mfa_enable", "admin_user", id, before, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
		})
		if err != nil {
			resourceFailure(c, err)
			return
		}
		httpapi.OK(c, gin.H{"id": id, "mfa_enabled": true})
	case "disable":
		err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			var before map[string]any
			if err := tx.Table("admin_user_role").Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
				return err
			}
			if err := tx.Table("admin_user_role").Where("id = ?", id).
				Updates(map[string]any{"mfa_secret": nil, "mfa_enabled": false, "auth_version": gorm.Expr("auth_version + 1")}).Error; err != nil {
				return err
			}
			return resourceAudit(tx, profile, "mfa_disable", "admin_user", id, before, nil, c.ClientIP(), c.GetHeader("X-Request-ID"))
		})
		if err != nil {
			resourceFailure(c, err)
			return
		}
		httpapi.OK(c, gin.H{"id": id, "mfa_enabled": false})
	default:
		httpapi.BadRequest(c, "请指定 enrol、confirm 或 disable")
	}
}
