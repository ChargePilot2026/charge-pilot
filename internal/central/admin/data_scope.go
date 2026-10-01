package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// DataScope 将账号的站点、厂商范围项归并为内存集合。
// 无范围记录或 Unrestricted=true 时，不附加数据范围限制；角色权限仍需单独校验。
type DataScope struct {
	AdminUserID  uint64   `gorm:"column:admin_user_id"` // 账号 ID
	ScopeType    string   `gorm:"column:scope_type"`    // 范围类型：station 限站点 / vendor 限厂商
	ScopeID      uint64   `gorm:"column:scope_id"`      // 该类型下的对象 ID，配合 ScopeType 定位一个站点或厂商
	StationIDs   []uint64 `gorm:"-"`                    // 归并后的站点 ID 列表；gorm:"-" 表示不参与扫描
	VendorIDs    []uint64 `gorm:"-"`                    // 归并后的厂商 ID 列表；gorm:"-" 表示不参与扫描
	Unrestricted bool     `gorm:"-"`                    // 是否不受限：无任何范围记录即为真；gorm:"-" 表示不落库
}

// TableName 指明 DataScope 映射到 admin_data_scope。
func (DataScope) TableName() string { return "admin_data_scope" }

// LoadDataScope 按类型加载账号的站点、厂商范围。
// 通配角色或无范围记录的账号返回 Unrestricted=true。
func LoadDataScope(ctx context.Context, db *gorm.DB, p Profile) (DataScope, error) {
	scope := DataScope{AdminUserID: p.ID}
	rows := []DataScope{}
	if err := db.WithContext(ctx).Table("admin_data_scope").Where("admin_user_id = ?", p.ID).Find(&rows).Error; err != nil {
		return scope, err
	}
	if len(rows) == 0 {
		scope.Unrestricted = true
		return scope, nil
	}
	for _, row := range rows {
		switch row.ScopeType {
		case "station":
			scope.StationIDs = append(scope.StationIDs, row.ScopeID)
		case "vendor":
			scope.VendorIDs = append(scope.VendorIDs, row.ScopeID)
		}
	}
	return scope, nil
}

// ApplyStations 为指定站点列追加范围过滤；column 由调用方提供，如 t.station_id。
// Unrestricted 为真或站点范围为空时，不追加条件。
func (s DataScope) ApplyStations(query *gorm.DB, column string) *gorm.DB {
	if s.Unrestricted || len(s.StationIDs) == 0 {
		return query
	}
	return query.Where(column+" IN ?", s.StationIDs)
}

// ApplyVendors 对归属厂商的资源用同样的方式收窄查询。
//
// ApplyVendors 与 ApplyStations 同理，只是作用于厂商维度的字段。
func (s DataScope) ApplyVendors(query *gorm.DB, column string) *gorm.DB {
	if s.Unrestricted || len(s.VendorIDs) == 0 {
		return query
	}
	return query.Where(column+" IN ?", s.VendorIDs)
}

// AllowsStation 判断站点是否在可见范围内，供详情接口执行归属校验。
func (s DataScope) AllowsStation(id uint64) bool {
	if s.Unrestricted || len(s.StationIDs) == 0 {
		return true
	}
	for _, allowed := range s.StationIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

// FieldMask 描述按角色配置的响应字段遮蔽规则。
type FieldMask struct {
	RoleID uint64 `gorm:"column:role_id"` // 规则所属角色 ID
	Fields []MaskedField
}

// MaskedField 是一条遮蔽规则：某资源下的某个字段需要打码。
type MaskedField struct {
	Resource string `gorm:"column:resource"` // 资源名，对应业务模块（如 user）
	Field    string `gorm:"column:field"`    // 字段名，需打码的列
}

// TableName 指明 FieldMask 映射到 admin_field_mask。
func (FieldMask) TableName() string { return "admin_field_mask" }

// LoadFieldMask 读出绑在某个角色上的遮蔽规则。
//
// LoadFieldMask 读出某角色的全部遮蔽规则；roleID 为 0 表示不限角色，直接返回空规则集（不遮蔽）。
func LoadFieldMask(ctx context.Context, db *gorm.DB, roleID uint64) (FieldMask, error) {
	mask := FieldMask{RoleID: roleID}
	if roleID == 0 {
		return mask, nil
	}
	if err := db.WithContext(ctx).Table("admin_field_mask").Where("role_id = ?", roleID).Find(&mask.Fields).Error; err != nil {
		return mask, err
	}
	return mask, nil
}

// Hide 判断某资源的某个字段对该角色是否必须打码。
//
// Hide 判断某资源的某字段是否需要打码。
func (m FieldMask) Hide(resource, field string) bool {
	for _, rule := range m.Fields {
		if rule.Resource == resource && rule.Field == field {
			return true
		}
	}
	return false
}

// MaskRow 就地将已有字段中命中脱敏规则的值替换为占位符，不补充缺失字段。
func (m FieldMask) MaskRow(resource string, row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	for _, rule := range m.Fields {
		if rule.Resource != resource {
			continue
		}
		if _, present := row[rule.Field]; present {
			row[rule.Field] = maskPlaceholder
		}
	}
	return row
}

// maskPlaceholder 是被遮蔽字段的固定响应占位值。
const maskPlaceholder = "***"

// MaskSlice 对一个列表响应里的每一行套用 MaskRow。
//
// MaskSlice 对列表响应里的每一行套用 MaskRow，原地修改并返回。
func (m FieldMask) MaskSlice(resource string, rows []map[string]any) []map[string]any {
	for i := range rows {
		m.MaskRow(resource, rows[i])
	}
	return rows
}

// errScopeConflict 表示范围配置本身不合法（类型未知、ID 不存在、数量越界），统一按 409 返回。
var errScopeConflict = errors.New("数据范围与现有记录冲突")

// validateScope 校验范围类型为 station 或 vendor，ID 数量为 1–500，且对应记录存在。
// 空范围表示不受限，保存前必须排除无效 ID。
func (a ResourceAPI) validateScope(ctx context.Context, scopeType string, ids []uint64) error {
	if len(ids) == 0 || len(ids) > 500 {
		return errScopeConflict
	}
	for _, id := range ids {
		if id == 0 {
			return errScopeConflict
		}
	}
	if scopeType == "vendor" {
		var page Page[vendorRow]
		query := vendorQuery(PageQuery{Page: 1, PageSize: 1}, DataScope{VendorIDs: ids})
		if err := a.requestVendor(ctx, "GET", "", query, nil, &page, ""); err != nil {
			return err
		}
		if int(page.Total) != len(ids) {
			return errScopeConflict
		}
		return nil
	}
	if scopeType != "station" {
		return errScopeConflict
	}
	var found int64
	if err := a.Store.AdminDB.WithContext(ctx).Table("station").Where("id IN ? AND deleted_at IS NULL", ids).Count(&found).Error; err != nil {
		return err
	}
	if int(found) != len(ids) {
		return errScopeConflict
	}
	return nil
}

// decodeScopeList 解析范围 ID 数组：空白按"未填"处理返回空列表，
// 非法 JSON 交回调用方报错，不静默变成空范围。
func decodeScopeList(raw string) ([]uint64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ids []uint64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}
