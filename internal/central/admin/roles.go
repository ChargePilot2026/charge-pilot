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

// 角色与权限的管理。
//
// RBAC 从一开始就在每个地方强制执行——每条后台
// 路由都声明自己需要的权限——但角色本身既看不见
// 也改不了。角色来自迁移和引导脚本，于是运营要
// 给一个账号配角色，就只能先知道它的数字 id；
// 新岗位要用的角色更是根本建不出来。接口文档甚
// 至让运营「在角色管理里」授权，而那个界面并不
// 存在。
//
// 这里每一次写入都受同一条规则约束，和 createUser
// 已经在用的那条一样：谁也不能授予自己没有的权限。
// 没有这条，customer_admin 就能造出一个带着任何人类
// 审核者都没批准过的权限的账号，而审计记录还会如实
// 把这次提权记成一次日常操作。

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
	// 角色列表保留它一直以来的 admin_user.read 这道门：
	// 后台账号表单本来就调它来填角色选择器，收窄这道门会让
	// 所有非超管运营都建不了账号。它现在多返回的字段是原来那
	// 三个的超集。
	//
	// role.read / role.create / role.update 早就是种在种子
	// 数据里、无人使用的权限，它们等的就是这个界面。
	r.GET("/api/v1/admin/roles", a.Auth.Require("admin_user.read"), a.listRoles)
	// 权限字典用和角色列表相同的权限把门：它只描述
	// 存在哪些权限，不授予任何东西，而能选角色的人也
	// 需要看见这个角色能做什么。种子数据里没有
	// role.read，在这里另造一个只会把一项能力拆成
	// 两个几乎一样的编码。
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
		strings.HasPrefix(code, "alert.rule.") || strings.HasPrefix(code, "alert.subscription.") || code == "settings.ota.update"
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
		rows[i].Permissions = withoutRetiredPermissions(codes)
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("admin_user_role").
			Where("role_id = ? AND deleted_at IS NULL", rows[i].ID).Count(&rows[i].UserCount).Error; err != nil {
			resourceFailure(c, err)
			return
		}
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// listPermissions 列出全部权
// 限，按模块、编码排序，平铺返回（分组由前端做）
// 它就是角色编辑器勾选权限时对着的那张表；因为分
// 组在前端做，这里只平铺，模块名挂在每一行上。
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

// validate 校验角色输入，其中最关键的一条
// 是权限只能从 codes（调用者已持有的权限）
// 里选，且必须非空、不重复；否则这套权限模型就只是
// 建议性的了：角色是一打包权限，放任一个账号在够
// 不着的地方拼出一包，这套模型就退化成参考意见而不
// 是强制。内置角色走同一个校验，但 update
// Role 会把它的权限集合锁住，只改名称和说明。
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
	// 内置角色承载运营自身的访问权限。放开改它的
	// 权限集合，就等于让最后一个管理员把所有人都锁
	// 在门外，所以名称和说明还能改，授权不能改。
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

// deleteRole 下线一个没人挂
// 着的角色（软删除），但先拒绝三种情况。
//
// 一、内置角色承载运营自身的访
// 问权限，删了可能把最后一个管
// 理员关在自己的后台门外。二、角
// 色下还有账号时不删，那些账号
// 会在这一刻无声地丢掉全部权
// 限，运营必须先自己把账号改派
// 去，而悄悄替他们改派比逼他们
// 自己动手更糟。三、也不能删自己
// 正在使用的角色，这和前两者是
// 同一种自锁，只是换了一条路。
//
// 整行做软删除，是为了让审计记录和
// 历史操作人名称仍能解析到角色。
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
