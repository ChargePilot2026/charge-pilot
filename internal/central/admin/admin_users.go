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

// AdminUserRow 是 PC 后台管理员账号（admin_user 表）的列表行映射。
// 账号归属 central_db，与终端用户 central_db.user 分开存放；一个账号一个主角色，权限由 role 展开。
type AdminUserRow struct {
	ID            uint64  `json:"id"`                 // 账号主键
	Username      string  `json:"username"`           // 登录用户名，全局唯一；本接口只读，不提供新建
	DisplayName   *string `json:"display_name"`       // 显示名称；nil 表示未设置
	RoleID        *uint64 `json:"role_id"`            // 主角色 ID；nil 表示尚未分配角色
	RoleCode      *string `json:"role_code"`          // 主角色编码（如 customer_admin），由 role 表联查得出；角色已删时为 nil
	RoleName      *string `json:"role_name"`          // 实际角色名称，支持自定义角色；角色已删时为空。
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

// registerAdminUsers 注册管理员账号及安全操作接口。
// 资料、角色、解锁和 MFA 使用 admin_user.update；重置密码与删除分别要求独立权限。
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
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user AS u").Where("u.deleted_at IS NULL")
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
	if err := query.Select("u.id,u.username,u.display_name,u.role_id,r.code AS role_code,r.name AS role_name,u.phone,u.email,u.status,u.mfa_enabled,u.last_login_at,u.locked_until,u.failed_login_count").
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

// assignableRole 要求角色存在、权限非空，且其全部权限属于操作者已持有的权限。
func assignableRole(ctx context.Context, db *gorm.DB, p Profile, roleID uint64) error {
	var codes []string
	if err := db.WithContext(ctx).Table("role AS r").
		Joins("JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id").
		Where("r.id=? AND r.deleted_at IS NULL", roleID).Pluck("p.code", &codes).Error; err != nil {
		return err
	}
	codes = withoutRetiredPermissions(codes)
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

// updateAdminUser 更新已提交的资料字段、角色和状态；指针字段区分缺省值与显式更新。
// 状态仅允许 active 或 disabled；禁止操作者停用或降级自己。角色变更递增 auth_version，撤销已有会话。
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
	values := map[string]any{}
	for key, value := range map[string]*string{"display_name": in.DisplayName, "phone": in.Phone, "email": in.Email, "status": in.Status} {
		if value != nil {
			values[key] = *value
		}
	}
	if len(values) == 0 && in.RoleID == nil {
		httpapi.BadRequest(c, "没有可更新的字段")
		return
	}
	var before AdminUserRow
	roleChanged := false
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		// 表单会回传原角色；用有类型的快照比较，避免 uint64 与驱动 int64 被误判为不同。
		roleChanged = in.RoleID != nil && (before.RoleID == nil || *in.RoleID != *before.RoleID)
		if id == profile.ID {
			// 禁止操作者停用或降级自己，避免失去管理入口。
			if in.Status != nil && *in.Status == "disabled" {
				return errConflict
			}
			if roleChanged {
				return errConflict
			}
		}
		if roleChanged {
			if err := assignableRole(c.Request.Context(), tx, profile, *in.RoleID); err != nil {
				return err
			}
			values["role_id"] = *in.RoleID
			// 仅角色变化时递增凭证版本；普通资料更新保留现有会话。
			values["auth_version"] = gorm.Expr("auth_version + 1")
		}
		if err := tx.Table("admin_user").Where("id = ?", id).Updates(values).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "update", "admin_user", id, before, in, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		a.roleFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "sessions_revoked": roleChanged})
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
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user AS u").
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
		if err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("admin_user").Where("id = ?", id).
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
		if err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("admin_user").Where("id = ?", id).
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
		if err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("admin_user").Where("id = ?", id).
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

// adminUserMFA 按 action 管理目标账号的 TOTP：enrol 登记未启用密钥，
// confirm 验证口令后启用，disable 清除密钥。成功操作递增 auth_version。
// 本人 MFA 必须通过个人安全设置操作，禁止在此接口修改自己的验证器。
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
	// 验证器标签使用目标账号身份，确保密钥绑定到被管理的账号。
	var target struct {
		Username string `gorm:"column:username"`
	}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user").
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
		// 登记阶段保存未启用密钥；confirm 验证成功后启用。
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user").
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
			if err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
				return err
			}
			if err := tx.Table("admin_user").Where("id = ?", id).
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
			if err := tx.Table("admin_user").Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
				return err
			}
			if err := tx.Table("admin_user").Where("id = ?", id).
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
