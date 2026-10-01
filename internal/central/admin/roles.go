package admin

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 本文件实现角色管理与权限字典查询。
// 写操作仅允许授予操作者已持有的权限，防止通过角色编辑或账号分配提升权限。

// roleCodePattern 约束角色编码：小写字母开头，后接小写字母、数字或下划线，共 3–64 位。
var roleCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

// RoleRow 是角色列表投影，包含权限编码、绑定账号数量及内置角色标记。
type RoleRow struct {
	ID          uint64   `json:"id"`                   // 角色主键
	Code        string   `json:"code"`                 // 角色编码，小写字母开头 3–64 位；账号记录的是 id，这里用于展示与自检
	Name        string   `json:"name"`                 // 角色名称，必填，最多 128 字符
	Description *string  `json:"description"`          // 角色说明，可空，最多 255 字符
	IsBuiltin   bool     `json:"is_builtin"`           // 是否内置角色：内置角色的权限集合锁定，不可改也不可删
	ActiveCode  *string  `json:"active_code"`          // 未删除时的角色编码（由 code 派生的生成列），软删除后为 NULL
	Permissions []string `json:"permissions" gorm:"-"` // 该角色持有的权限编码，按字典序；无权限时返回 [] 而不是 null
	UserCount   int64    `json:"user_count"`           // 仍挂在这个角色上的未删除账号数，大于 0 时不允许删除该角色
}

// PermissionRow 是权限字典的一行，角色编辑器勾选权限时读的就是这张表。
type PermissionRow struct {
	ID          uint64 `json:"id"`          // 权限主键
	Code        string `json:"code"`        // 权限编码，路由 Auth.Require(perm) 用的就是它
	Name        string `json:"name"`        // 权限中文名，前端按模块分组展示
	Module      string `json:"module"`      // 所属模块，如 alert、pricing、webhook
	Description string `json:"description"` // 权限说明，库中为 NULL 时由 SQL 补空串
}

// registerRoles 挂载角色与权限字典的接口：角色增删改查，权限字典只读。
func (a ResourceAPI) registerRoles(r *gin.Engine) {
	// 角色列表使用 admin_user.read，供后台账号表单和角色管理界面读取。
	r.GET("/api/v1/admin/roles", a.Auth.Require("admin_user.read"), a.listRoles)
	// 权限字典使用 admin_user.read，仅返回可选权限，不授予权限。
	r.GET("/api/v1/admin/permissions", a.Auth.Require("admin_user.read"), a.listPermissions)
	r.POST("/api/v1/admin/roles", a.Auth.Require("role.create"), a.createRole)
	r.PUT("/api/v1/admin/roles/:id", a.Auth.Require("role.update"), a.updateRole)
	r.DELETE("/api/v1/admin/roles/:id", a.Auth.Require("role.delete"), a.deleteRole)
}

// permissionCodesFor 取出给定
// 的这些角色合起来持有的权限编码（去重）。被软
// 删除的角色不参与：删掉的角色不该继续给人授权。
func (a ResourceAPI) permissionCodesFor(ctx context.Context, codes ...string) ([]string, error) {
	var result []string
	err := a.Store.AdminDB.WithContext(ctx).Table("permission AS p").
		Joins("JOIN role_permission rp ON rp.permission_id = p.id").
		Joins("JOIN role r ON r.id = rp.role_id AND r.deleted_at IS NULL").
		Where("r.code IN ?", codes).Distinct("p.code").Pluck("p.code", &result).Error
	return withoutRetiredPermissions(result), err
}

// withoutRetiredPermissions 让未重建开发库中的已移除功能权限不再参与展示或授权。
// 角色及其余权限保持原样，不修改已有权限绑定数据。
func withoutRetiredPermissions(codes []string) []string {
	result := make([]string, 0, len(codes))
	for _, code := range codes {
		if !retiredPermission(code) {
			result = append(result, code)
		}
	}
	return result
}

func retiredPermission(code string) bool {
	return strings.HasPrefix(code, "customer_service.") || strings.HasPrefix(code, "ota.") ||
		strings.HasPrefix(code, "alert.rule.") || strings.HasPrefix(code, "alert.subscription.") || code == "settings.ota.update" || code == "membership.create" || code == "alert.risk_config.update" || code == "pricing.template.create"
}

// listRoles 按 ID 升序返回未删除角色，附带权限编码及关联账号数。
func (a ResourceAPI) listRoles(c *gin.Context) {
	var rows []RoleRow
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("role AS r").
		Where("r.deleted_at IS NULL").Order("r.id").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	for i := range rows {
		var codes []string
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("permission AS p").
			Joins("JOIN role_permission rp ON rp.permission_id = p.id").
			Where("rp.role_id = ?", rows[i].ID).Order("p.code").Pluck("p.code", &codes).Error; err != nil {
			resourceFailure(c, err)
			return
		}
		rows[i].Permissions = withoutRetiredPermissions(codes)
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user_role").
			Where("role_id = ? AND deleted_at IS NULL", rows[i].ID).Count(&rows[i].UserCount).Error; err != nil {
			resourceFailure(c, err)
			return
		}
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// listPermissions 按模块、编码排序返回权限列表，分组由前端处理。
func (a ResourceAPI) listPermissions(c *gin.Context) {
	rows := []PermissionRow{}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("permission").
		Select("id, code, name, CASE WHEN code LIKE 'feedback.%' THEN 'device' ELSE module END AS module, COALESCE(description, '') AS description").
		Order("module, code").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	visible := make([]PermissionRow, 0, len(rows))
	for _, row := range rows {
		if !retiredPermission(row.Code) {
			visible = append(visible, row)
		}
	}
	httpapi.OK(c, gin.H{"items": visible})
}

// roleInput 是角色新增与更新共用的请求体，字段与 RoleRow 对应。
type roleInput struct {
	Code        string   `json:"code"`        // 角色编码，小写字母开头的 3–64 位
	Name        string   `json:"name"`        // 角色名称，必填，最多 128 字符
	Description *string  `json:"description"` // 角色说明，可空，最多 255 字符
	Permissions []string `json:"permissions"` // 要授予的权限编码，必须非空、不重复，且只能来自调用者自己已持有的权限
}

// validate 要求角色权限非空、不重复，且全部属于调用者已持有的权限。
// 内置角色更新时仅允许修改名称和说明，权限集合保持固定。
func (in roleInput) validate(codes []string) error {
	if !roleCodePattern.MatchString(in.Code) || in.Name == "" || len([]rune(in.Name)) > 128 {
		return errRoleInput
	}
	if in.Description != nil && len([]rune(*in.Description)) > 255 {
		return errRoleInput
	}
	if len(in.Permissions) == 0 {
		return errRoleInput
	}
	known := make(map[string]bool, len(codes))
	for _, code := range codes {
		known[code] = true
	}
	seen := make(map[string]bool, len(in.Permissions))
	for _, code := range in.Permissions {
		code = strings.TrimSpace(code)
		if !known[code] || seen[code] {
			return errRoleInput
		}
		seen[code] = true
	}
	return nil
}

// errRoleInput 表示角色参数不合法（编码格式、名称长度、权限为空或超出自己持有的范围）。
var errRoleInput = errors.New("invalid role")

// createRole 新建自定义角色，初始版本号固定为 1，权限按请求整体替换。
// 权限集合在事务提交后单独写审计，避免审计写失败把角色回滚成"建了但没权限"的中间态。
func (a ResourceAPI) createRole(c *gin.Context) {
	var in roleInput
	if !decodeResource(c, &in) {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	held, err := a.permissionCodesFor(c.Request.Context(), profile.Role)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	if err := in.validate(held); err != nil {
		httpapi.BadRequest(c, "角色无效：编码须为小写字母开头的 3–64 位，名称必填，权限须非空且只能从自己已持有的权限中选择")
		return
	}
	var id uint64
	var auditPending []auditEntry
	err = a.auditedTransaction(c, a.Store.AdminDB, &auditPending, func(tx *gorm.DB) error {
		if err := tx.Table("role").Create(map[string]any{
			"code": in.Code, "name": in.Name, "description": in.Description, "is_builtin": false,
		}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		if err := replaceRolePermissions(tx, id, in.Permissions); err != nil {
			return err
		}
		auditPending = []auditEntry{{"create", "role", id, nil, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// updateRole 更新角色。内置角色只改名称和说明，权限集合锁定；自定义角色按请求整体替换权限。
// 响应里的 permissions_locked 告诉前端权限这一栏这次是不是只读的。
func (a ResourceAPI) updateRole(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in roleInput
	if !decodeResource(c, &in) {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	held, err := a.permissionCodesFor(c.Request.Context(), profile.Role)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	if err := in.validate(held); err != nil {
		httpapi.BadRequest(c, "角色无效：只能从自己已持有的权限中选择")
		return
	}
	var before RoleRow
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("role").
		Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.Write(c, http.StatusNotFound, 1004, "角色不存在", nil)
			return
		}
		resourceFailure(c, err)
		return
	}
	// 内置角色仅允许修改名称和说明，其权限集合固定，避免破坏基础管理权限。
	var auditPending []auditEntry
	err = a.auditedTransaction(c, a.Store.AdminDB, &auditPending, func(tx *gorm.DB) error {
		if before.IsBuiltin {
			if err := tx.Table("role").Where("id = ?", id).
				Updates(map[string]any{"name": in.Name, "description": in.Description}).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Table("role").Where("id = ?", id).Updates(map[string]any{
				"name": in.Name, "description": in.Description,
			}).Error; err != nil {
				return err
			}
			if err := replaceRolePermissions(tx, id, in.Permissions); err != nil {
				return err
			}
		}
		auditPending = []auditEntry{{"update", "role", id, before, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "permissions_locked": before.IsBuiltin})
}

// replaceRolePermissions 整体替换某个角色的权限绑定：先清空旧绑定，再按编码逐个
// 查 permission 的 id 插入。只在调用方已校验过编码的前提下调用。
func replaceRolePermissions(tx *gorm.DB, roleID uint64, codes []string) error {
	if err := tx.Exec("DELETE FROM role_permission WHERE role_id = ?", roleID).Error; err != nil {
		return err
	}
	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if err := tx.Exec("INSERT INTO role_permission (role_id, permission_id) SELECT ?, id FROM permission WHERE code = ?", roleID, code).Error; err != nil {
			return err
		}
	}
	return nil
}

// deleteRole 软删除角色，保留历史审计引用。
// 内置角色、仍有关联账号的角色及调用者当前角色均不可删除。
func (a ResourceAPI) deleteRole(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	var role RoleRow
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("role").
		Where("id = ? AND deleted_at IS NULL", id).Take(&role).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.Write(c, http.StatusNotFound, 1004, "角色不存在", nil)
			return
		}
		resourceFailure(c, err)
		return
	}
	if role.IsBuiltin {
		httpapi.Write(c, http.StatusConflict, 2009, "内置角色不可删除：它承载运营自身的访问权限", nil)
		return
	}
	if role.Code == profile.Role {
		httpapi.Write(c, http.StatusConflict, 2009, "不能删除自己正在使用的角色", nil)
		return
	}
	var held int64
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user_role").
		Where("role_id = ? AND deleted_at IS NULL", id).Count(&held).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if held > 0 {
		httpapi.Write(c, http.StatusConflict, 2009,
			"该角色下仍有账号，请先改派后再删除", nil)
		return
	}
	var auditPending []auditEntry
	err := a.auditedTransaction(c, a.Store.AdminDB, &auditPending, func(tx *gorm.DB) error {
		changed := tx.Table("role").Where("id = ? AND deleted_at IS NULL", id).
			Updates(map[string]any{"deleted_at": time.Now().UTC()})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Exec("DELETE FROM role_permission WHERE role_id = ?", id).Error; err != nil {
			return err
		}
		auditPending = []auditEntry{{"delete", "role", id, role, nil, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}
