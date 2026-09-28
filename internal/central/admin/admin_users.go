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

type AdminUserRow struct {
	ID            uint64  `json:"id"`
	Username      string  `json:"username"`
	DisplayName   *string `json:"display_name"`
	RoleID        *uint64 `json:"role_id"`
	RoleCode      *string `json:"role_code"`
	Phone         *string `json:"phone"`
	Email         *string `json:"email"`
	Status        string  `json:"status"`
	MFAEnabled    bool    `json:"mfa_enabled"`
	LastLoginAt   *string `json:"last_login_at"`
	LockedUntil   *string `json:"locked_until"`
	FailedLogins  uint32  `json:"failed_login_count"`
	DeletePending bool    `json:"-"`
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

func (a ResourceAPI) registerAdminUsers(r *gin.Engine) {
	r.GET("/api/v1/admin/admin-users", a.Auth.Require("admin_user.read"), a.listAdminUsers)
	r.PUT("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.update"), a.updateAdminUser)
	r.DELETE("/api/v1/admin/admin-users/:id", a.Auth.Require("admin_user.delete"), a.deleteAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/unlock", a.Auth.Require("admin_user.update"), a.unlockAdminUser)
	r.POST("/api/v1/admin/admin-users/:id/reset-password", a.Auth.Require("admin_user.reset_password"), a.resetAdminPassword)
	r.POST("/api/v1/admin/admin-users/:id/mfa", a.Auth.Require("admin_user.update"), a.adminUserMFA)
}

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

// assignableRole confirms the target role exists and that the acting operator
// does not grant powers beyond their own, so privilege cannot be escalated by
// creating or editing an account.
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

var errInsufficient = errors.New("不能授予自己未拥有的权限")

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
		// Changing a role must invalidate existing sessions, otherwise the old
		// permissions stay live until the token expires.
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
			// An operator must not be able to demote or disable themselves and
			// lock the last administrator out of the console.
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

func (a ResourceAPI) roleFailure(c *gin.Context, err error) {
	if errors.Is(err, errInsufficient) {
		httpapi.Write(c, http.StatusForbidden, 1003, "不能授予自己未拥有的权限", nil)
		return
	}
	resourceFailure(c, err)
}

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
	// Bumping auth_version already invalidates every issued session.
	httpapi.OK(c, gin.H{"id": id, "sessions_revoked": true})
}

// adminUserMFA enrols, confirms or removes the second factor. A secret is only
// persisted after the operator proves the authenticator works.
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
	// The enrolment label must name the account being protected, not the
	// operator, so the authenticator app cannot be bound to the wrong identity.
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
		// The secret is stored disabled; it only becomes active after confirm.
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
