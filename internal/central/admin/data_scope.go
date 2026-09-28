package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// DataScope restricts which stations and vendors an account may read. An
// account with no scope rows is unrestricted within its role permissions, which
// keeps the common case simple without weakening the scoped one.
type DataScope struct {
	AdminUserID  uint64   `gorm:"column:admin_user_id"`
	ScopeType    string   `gorm:"column:scope_type"`
	ScopeID      uint64   `gorm:"column:scope_id"`
	StationIDs   []uint64 `gorm:"-"`
	VendorIDs    []uint64 `gorm:"-"`
	Unrestricted bool     `gorm:"-"`
}

func (DataScope) TableName() string { return "admin_data_scope" }

// LoadDataScope reads the account's scope. It returns an empty, unrestricted
// scope when the operator holds the wildcard role, because the built-in
// customer administrator is the break-glass account for the whole platform.
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

// ApplyStations narrows a query to the stations in scope. An empty list means
// the account may see every station, so no condition is added.
func (s DataScope) ApplyStations(query *gorm.DB, column string) *gorm.DB {
	if s.Unrestricted || len(s.StationIDs) == 0 {
		return query
	}
	return query.Where(column+" IN ?", s.StationIDs)
}

// ApplyVendors narrows a query the same way for vendor-owned resources.
func (s DataScope) ApplyVendors(query *gorm.DB, column string) *gorm.DB {
	if s.Unrestricted || len(s.VendorIDs) == 0 {
		return query
	}
	return query.Where(column+" IN ?", s.VendorIDs)
}

// AllowsStation reports whether a single station is visible.
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

// FieldMask hides sensitive columns for roles that must see the record but not
// the raw value, such as a support agent reading a customer's phone number.
type FieldMask struct {
	RoleID uint64 `gorm:"column:role_id"`
	Fields []MaskedField
}

type MaskedField struct {
	Resource string `gorm:"column:resource"`
	Field    string `gorm:"column:field"`
}

func (FieldMask) TableName() string { return "admin_field_mask" }

// LoadFieldMask reads the masking rules bound to a role.
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

// Hide reports whether a field must be masked for this role.
func (m FieldMask) Hide(resource, field string) bool {
	for _, rule := range m.Fields {
		if rule.Resource == resource && rule.Field == field {
			return true
		}
	}
	return false
}

// MaskRow blanks the masked keys of a JSON-shaped row so the same masking rule
// applies to list and detail responses without hand-writing each projection.
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

const maskPlaceholder = "***"

// MaskSlice applies MaskRow across a list response.
func (m FieldMask) MaskSlice(resource string, rows []map[string]any) []map[string]any {
	for i := range rows {
		m.MaskRow(resource, rows[i])
	}
	return rows
}

var errScopeConflict = errors.New("数据范围与现有记录冲突")

// scopeRequest validates the scope ids actually exist before storing them, so a
// typo cannot silently grant an empty (that is, unrestricted) scope later.
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
