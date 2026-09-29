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

// Role and permission management.
//
// RBAC was enforced everywhere from the start — every admin route declares the
// permission it needs — but there was no way to see or change the roles
// themselves. Roles came from migrations and the bootstrap, so an operator
// could assign an account to a role only by knowing its numeric id, and could
// not create a role for a new job at all. The API documentation even told
// operators to authorise a new role "in role management", a surface that did
// not exist.
//
// The rule that governs every write here is the same one createUser already
// uses: nobody may grant a permission they do not hold themselves. Without it
// a customer_admin could mint an account with permissions no human reviewer
// ever approved, and the audit trail would faithfully record a privilege
// escalation as a routine action.

// roleCodePattern 约束角色编码：小写字母开头，后接小写字母、数字或下划线，共 3–64 位。
var roleCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

// RoleRow 是角色列表接口返回的一行：一个角色（一份权限的命名打包），
// 加上运营侧做判断要用的附带信息——绑定的权限编码、已挂账号数、内置标记。
type RoleRow struct {
	ID          uint64   `json:"id"`          // 角色主键
	Code        string   `json:"code"`        // 角色编码，小写字母开头 3–64 位；账号记录的是 id，这里用于展示与自检
	Name        string   `json:"name"`        // 角色名称，必填，最多 128 字符
	Description *string  `json:"description"` // 角色说明，可空，最多 255 字符
	IsBuiltin   bool     `json:"is_builtin"`  // 是否内置角色：内置角色的权限集合锁定，不可改也不可删
	ActiveCode  *string  `json:"active_code"` // 未删除时的角色编码（由 code 派生的生成列），软删除后为 NULL
	Permissions []string `json:"permissions"` // 该角色持有的权限编码，按字典序；无权限时返回 [] 而不是 null
	UserCount   int64    `json:"user_count"`  // 仍挂在这个角色上的未删除账号数，大于 0 时不允许删除该角色
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
	// The role list keeps the admin_user.read gate it always had: the admin
	// user form already calls it to populate its role selector, and narrowing it
	// would break account creation for every non-superuser operator. The extra
	// fields it now returns are a superset of the old three.
	//
	// role.read / role.create / role.update already existed as seeded
	// permissions with no consumer; this is the surface they were reserved for.
	r.GET("/api/v1/admin/roles", a.Auth.Require("admin_user.read"), a.listRoles)
	// The dictionary is gated with the same permission as the role list: it
	// describes which privileges exist but grants nothing, and whoever may
	// choose a role also needs to see what that role can do. There is no
	// role.read permission in the seed data, and inventing one here would
	// split a single capability across two near-identical codes.
	r.GET("/api/v1/admin/permissions", a.Auth.Require("admin_user.read"), a.listPermissions)
	r.POST("/api/v1/admin/roles", a.Auth.Require("role.create"), a.createRole)
	r.PUT("/api/v1/admin/roles/:id", a.Auth.Require("role.update"), a.updateRole)
	r.DELETE("/api/v1/admin/roles/:id", a.Auth.Require("role.delete"), a.deleteRole)
}

// permissionCodesFor returns the permission codes held by the given roles.
// permissionCodesFor 取出这些角色合起来持有的权限编码（去重）。
// 被软删除的角色不参与：删掉的角色不该继续给人授权。
func (a ResourceAPI) permissionCodesFor(ctx context.Context, codes ...string) ([]string, error) {
	var result []string
	err := a.Store.AdminDB.WithContext(ctx).Table("permission AS p").
		Joins("JOIN role_permission rp ON rp.permission_id = p.id").
		Joins("JOIN role r ON r.id = rp.role_id AND r.deleted_at IS NULL").
		Where("r.code IN ?", codes).Distinct("p.code").Pluck("p.code", &result).Error
	return result, err
}

// listRoles 列出未删除的角色，按 id 升序。权限编码和账号数逐行另查后填进同一行，
// 一次请求一个 R 返回完整行，避免前端为了画角色列表再发 N 次请求。
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
		if codes == nil {
			codes = []string{}
		}
		rows[i].Permissions = codes
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user_role").
			Where("role_id = ? AND deleted_at IS NULL", rows[i].ID).Count(&rows[i].UserCount).Error; err != nil {
			resourceFailure(c, err)
			return
		}
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// listPermissions is what the role editor binds against. It is grouped by
// module by the frontend, so it is returned flat with the module carried on
// each row.
// listPermissions 列出全部权限，按模块、编码排序，平铺返回（分组由前端做）。
func (a ResourceAPI) listPermissions(c *gin.Context) {
	rows := []PermissionRow{}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("permission").
		Select("id, code, name, module, COALESCE(description, '') AS description").
		Order("module, code").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// roleInput 是角色新增与更新共用的请求体，字段与 RoleRow 对应。
type roleInput struct {
	Code        string   `json:"code"`        // 角色编码，小写字母开头的 3–64 位
	Name        string   `json:"name"`        // 角色名称，必填，最多 128 字符
	Description *string  `json:"description"` // 角色说明，可空，最多 255 字符
	Permissions []string `json:"permissions"` // 要授予的权限编码，必须非空、不重复，且只能来自调用者自己已持有的权限
}

// validate rejects a role that would grant something the caller does not hold.
// This is the whole point of the endpoint: a role is a bundle of privileges, and
// letting one account assemble a bundle outside its own reach would make the
// permission model advisory rather than enforced.
// validate 校验角色输入，其中最关键的一条是权限只能从 codes（调用者已持有的权限）里选，
// 且必须非空、不重复；否则这套权限模型就只是建议性的了。
// 内置角色走同一个校验，但 updateRole 会把它的权限集合锁住，只改名称和说明。
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
	err = a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
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
	a.flushAudit(c, auditPending)
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
	// Built-in roles carry the operator's own access. Allowing their permission
	// set to be edited would let the last admin lock every admin out, so the
	// name and description stay editable but the grants do not.
	var auditPending []auditEntry
	err = a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
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
	a.flushAudit(c, auditPending)
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

// deleteRole retires a role that nobody holds.
//
// Three things are refused rather than guessed at. A built-in role carries the
// operators' own access, so removing it could lock the last administrator out of
// their own console. A role that still has accounts cannot be retired, because
// those accounts would silently lose every permission the moment it went — the
// operator would have to reassign them first, and doing that silently is worse
// than making them do it on purpose. And nobody may retire the role they are
// acting through, which is the same self-lockout by another route.
//
// The row is soft-deleted so the audit trail and historical actor names keep
// resolving.
// deleteRole 下线一个角色（软删除），但先拒绝三种情况：内置角色承载运营自身的访问权限，
// 删了可能把最后一个管理员关在门外；角色下还有账号时不删，那些账号会当场丢掉全部权限，
// 必须运营自己先改派；也不能删自己正在使用的角色，这和前两者是同一种自锁。
// 软删除保留行，是为了让审计记录和历史操作人名称仍能解析到角色。
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
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
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
	a.flushAudit(c, auditPending)
	httpapi.OK(c, gin.H{"id": id})
}
