package admin

import (
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"math"
	"strconv"
)

const effectiveRuleSQL = "status='active' AND deleted_at IS NULL AND (effective_from IS NULL OR effective_from<=NOW(3)) AND (effective_to IS NULL OR effective_to>NOW(3))"

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
