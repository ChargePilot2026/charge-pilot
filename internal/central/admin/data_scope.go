package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// DataScope 限制一个账号能读哪些站点和厂商。
// 没有任何范围行的账号在其角色权限内不受限，
// 这样既让最常见的情况保持简单，又不会把配了范围的那个账号放宽。
//
// 一个账号在 admin_data_scope 表上的数据范围，限制它能看到哪些站点和厂商。
// 库表里一行是"一个范围项"，下面的 StationIDs / VendorIDs 是读出来后按类型归并的内存视图。
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

// LoadDataScope 读出账号的数据范围。运营持有通配角色时返回空的不受限范围，
// 因为内置的客户管理员本来就是全平台的破窗账号
// （出问题时用来应急的那个）。
//
// LoadDataScope 读出账号的数据范围，并按范围类型归并到 StationIDs / VendorIDs。
// 一行记录都没有即视为不受限（Unrestricted=true）：这是最常见的情况，
// 也不会让确实配置了范围的账号被放宽。
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

// ApplyStations 把查询收窄到范围内的站点。列表为空表示这个账号可以看到所有站点，
// 所以不追加任何条件。
//
// ApplyStations 给查询加上站点范围过滤。column 是调用方的站点列名（如 t.station_id），
// 不受限或范围内没有站点时原样返回，不加任何条件。
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

// AllowsStation 报告单个站点是否可见。
//
// AllowsStation 判断单个站点是否可见，供详情接口在取出记录后做归属校验。
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

// FieldMask 为"必须看到这条记录、但不该看到原始值"的角色遮蔽敏感列，
// 比如客服去读用户的手机号这个场景。
//
// FieldMask 是按角色配置的字段遮蔽规则：角色能看到这条记录，但不该看到其中的原始值
// （客服能查用户、但看不到完整手机号）。规则行来自 admin_field_mask 表。
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

// MaskRow 把一行 JSON 结构数据里命中遮蔽规则的键置空，
// 于是同一条遮蔽规则不用逐个手写投影，就能同时管住列表和详情响应。
//
// MaskRow 就地把一行 map 里命中遮蔽规则的字段替换成占位符。
// 只处理行里已存在的键，缺失的键不补，因此不会凭空多出字段。
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

// maskPlaceholder 是被遮蔽字段的固定占位值，让前端能看出"这里有值但你看不到"。
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

// validateScope 在存库之前校验范围 ID 确实存在，
// 这样打错一个 ID 也不会事后悄悄变成空范围（也就是不受限）。
//
// validateScope 校验待保存的范围 ID 真实存在，数量须在 1–500 之间。
// 这一步不能省：范围记录存空等于"不受限"，一个打错的 ID 会悄悄把账号变成全平台可见。
// scopeType 只接受 station 与 vendor，其余一律按冲突返回。
func (a ResourceAPI) validateScope(ctx context.Context, scopeType string, ids []uint64) error {
	if len(ids) == 0 || len(ids) > 500 {
		return errScopeConflict
	}
	table := "station"
	if scopeType == "vendor" {
		table = "vendor"
	} else if scopeType != "station" {
		return errScopeConflict
	}
	var found int64
	if err := a.Store.AdminDB.WithContext(ctx).Table(table).Where("id IN ? AND deleted_at IS NULL", ids).Count(&found).Error; err != nil {
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
