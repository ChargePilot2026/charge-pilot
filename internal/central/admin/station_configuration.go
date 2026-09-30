package admin

import (
	"math"
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const effectiveRuleSQL = "status='active' AND deleted_at IS NULL AND (effective_from IS NULL OR effective_from<=NOW(3)) AND (effective_to IS NULL OR effective_to>NOW(3))"
const stationOfferColumns = "o.id,o.station_id,o.device_id,o.package_template_id,o.name,o.mode,o.price_cents,o.duration_minutes,o.min_charge_cents,o.show_remark,o.card_default,o.status,o.version"

func (a ResourceAPI) stationScope(c *gin.Context) (DataScope, bool) {
	scope, err := LoadDataScope(c.Request.Context(), a.Store.AdminDB, c.MustGet("admin_profile").(Profile))
	if err != nil {
		resourceFailure(c, err)
		return scope, false
	}
	return scope, true
}

func (a ResourceAPI) requireStationScope(c *gin.Context, id uint64) bool {
	scope, ok := a.stationScope(c)
	if !ok {
		return false
	}
	if !scope.AllowsStation(id) {
		httpapi.Write(c, 403, 1003, "该站点不在您的数据范围内", nil)
		return false
	}
	return true
}

func canManageStationDefault(scope DataScope) bool {
	return scope.Unrestricted || len(scope.VendorIDs) == 0
}

// Station-wide writes affect every vendor at the station. A vendor-scoped
// operator may only write a concrete device belonging to their vendor range.
func (a ResourceAPI) requirePricingTargetScope(c *gin.Context, stationID uint64, deviceID string) bool {
	scope, ok := a.stationScope(c)
	if !ok {
		return false
	}
	if !scope.AllowsStation(stationID) {
		httpapi.Write(c, 403, 1003, "该站点不在您的数据范围内", nil)
		return false
	}
	if canManageStationDefault(scope) {
		return true
	}
	if deviceID == "" {
		httpapi.Write(c, 403, 1003, "厂商数据范围受限，不能修改整站默认配置", nil)
		return false
	}
	var device struct{ VendorID uint64 }
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("device_meta").Select("vendor_id").
		Where("station_id=? AND device_id=? AND deleted_at IS NULL", stationID, deviceID).Take(&device).Error; err != nil {
		resourceFailure(c, err)
		return false
	}
	if !scope.AllowsVendor(device.VendorID) {
		httpapi.Write(c, 403, 1003, "该设备厂商不在您的数据范围内", nil)
		return false
	}
	return true
}

func scopedOffers(query *gorm.DB, scope DataScope) *gorm.DB {
	query = scope.ApplyStations(query, "o.station_id")
	if canManageStationDefault(scope) {
		return query
	}
	return query.Joins("LEFT JOIN device_meta offer_device ON offer_device.device_id=o.device_id AND offer_device.station_id=o.station_id AND offer_device.deleted_at IS NULL").
		Where("(o.device_id IS NULL OR offer_device.vendor_id IN ?)", scope.VendorIDs)
}

// queryStationID distinguishes an omitted filter from an invalid empty value.
func queryStationID(c *gin.Context, required bool) (uint64, bool) {
	values, exists := c.Request.URL.Query()["station_id"]
	if !exists && !required {
		return 0, true
	}
	if len(values) != 1 {
		httpapi.BadRequest(c, "station_id 必须为正整数")
		return 0, false
	}
	id, err := strconv.ParseUint(values[0], 10, 64)
	if err != nil || id == 0 || id > math.MaxInt64 {
		httpapi.BadRequest(c, "station_id 必须为正整数")
		return 0, false
	}
	return id, true
}

func queryBool(c *gin.Context, name string) (bool, bool) {
	values, exists := c.Request.URL.Query()[name]
	if !exists {
		return false, true
	}
	if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
		httpapi.BadRequest(c, name+" 必须为 true 或 false")
		return false, false
	}
	return values[0] == "true", true
}

func (a ResourceAPI) stationConfiguration(c *gin.Context) {
	id, ok := pathID(c)
	if !ok || !a.requireStationScope(c, id) {
		return
	}
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	var station Station
	db := a.Store.AdminDB.WithContext(c.Request.Context())
	if err := db.Where("id=? AND deleted_at IS NULL", id).Take(&station).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	// Read one snapshot so the default, its latest version and the offer list agree.
	var rule map[string]any
	var latest struct{ Version uint32 }
	offers := []map[string]any{}
	err := db.Transaction(func(tx *gorm.DB) error {
		rows := []map[string]any{}
		q := tx.Table("pricing_rule").Select("id,name,station_id,device_id,template_id,spec_json,channel,version,status,effective_from,effective_to").
			Where("station_id=? AND device_id IS NULL", id).Where(effectiveRuleSQL)
		if err := q.Order("version DESC,id DESC").Limit(1).Find(&rows).Error; err != nil {
			return err
		}
		normalizeRows(rows)
		if len(rows) > 0 {
			rule = rows[0]
		}
		if err := tx.Table("pricing_rule").Select("version").Where("station_id=? AND device_id IS NULL AND deleted_at IS NULL", id).
			Order("version DESC,id DESC").Limit(1).Find(&latest).Error; err != nil {
			return err
		}
		return scopedOffers(tx.Table("charge_offer o").Select(stationOfferColumns), scope).Where("o.station_id=? AND o.deleted_at IS NULL", id).
			Order("o.device_id,o.mode,o.price_cents,o.id").Find(&offers).Error
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(offers)
	httpapi.OK(c, gin.H{"station_id": id, "default_rule": rule, "station_latest_version": latest.Version,
		"offers": offers, "can_manage_default": canManageStationDefault(scope), "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}
