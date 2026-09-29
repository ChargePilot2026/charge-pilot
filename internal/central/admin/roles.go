package admin

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

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

var roleCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

type RoleRow struct {
	ID          uint64   `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description *string  `json:"description"`
	IsBuiltin   bool     `json:"is_builtin"`
	ActiveCode  *string  `json:"active_code"`
	Permissions []string `json:"permissions"`
	UserCount   int64    `json:"user_count"`
}

type PermissionRow struct {
	ID          uint64 `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Module      string `json:"module"`
	Description string `json:"description"`
}

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
}

// permissionCodesFor returns the permission codes held by the given roles.
func (a ResourceAPI) permissionCodesFor(ctx context.Context, codes ...string) ([]string, error) {
	var result []string
	err := a.Store.AdminDB.WithContext(ctx).Table("permission AS p").
		Joins("JOIN role_permission rp ON rp.permission_id = p.id").
		Joins("JOIN role r ON r.id = rp.role_id AND r.deleted_at IS NULL").
		Where("r.code IN ?", codes).Distinct("p.code").Pluck("p.code", &result).Error
	return result, err
}

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

type roleInput struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description *string  `json:"description"`
	Permissions []string `json:"permissions"`
}

// validate rejects a role that would grant something the caller does not hold.
// This is the whole point of the endpoint: a role is a bundle of privileges, and
// letting one account assemble a bundle outside its own reach would make the
// permission model advisory rather than enforced.
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

var errRoleInput = errors.New("invalid role")

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
