package control

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

type ChargeProcessPoint struct {
	ID               uint64    `json:"id"`
	TS               time.Time `json:"ts"`
	PowerW           float64   `json:"power_w"`
	ChargedKWh       float64   `json:"charged_kwh" gorm:"column:charged_kwh"`
	RemainingKWh     float64   `json:"remaining_kwh" gorm:"column:remaining_kwh"`
	ChargedSeconds   uint32    `json:"charged_seconds"`
	RemainingSeconds uint32    `json:"remaining_seconds"`
	SignalStrength   uint8     `json:"signal_strength"`
	PortStatus       *uint8    `json:"port_status"`
	VoltageV         *uint16   `json:"voltage_v"`
	TemperatureC     *int16    `json:"temperature_c"`
	DeviceStatus     *uint8    `json:"device_status"`
}

func (a TelemetryAPI) chargeProcess(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	orderNo := c.Param("order_no")
	if strings.TrimSpace(orderNo) == "" || len(orderNo) > 64 {
		httpapi.BadRequest(c, "订单编号无效")
		return
	}
	values := c.Request.URL.Query()
	afterID := uint64(0)
	if values.Has("after_id") {
		var err error
		afterID, err = strconv.ParseUint(values.Get("after_id"), 10, 64)
		if err != nil {
			httpapi.BadRequest(c, "after_id 须为非负整数")
			return
		}
	}
	limit := 1000
	if values.Has("limit") {
		var err error
		limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || limit < 1 || limit > 2000 {
			httpapi.BadRequest(c, "limit 须为1至2000")
			return
		}
	}
	// Validate all identity filters before touching the database.
	var chargeOrderID uint64
	if values.Has("charge_order_id") {
		var err error
		chargeOrderID, err = strconv.ParseUint(values.Get("charge_order_id"), 10, 64)
		if err != nil || chargeOrderID == 0 {
			httpapi.BadRequest(c, "charge_order_id 须为有效订单ID")
			return
		}
	}
	deviceID := values.Get("device_id")
	if values.Has("device_id") && (strings.TrimSpace(deviceID) == "" || len(deviceID) > 64) {
		httpapi.BadRequest(c, "设备编号无效")
		return
	}
	var portNo uint64
	if values.Has("port_no") {
		var err error
		portNo, err = strconv.ParseUint(values.Get("port_no"), 10, 8)
		if err != nil || portNo == 0 {
			httpapi.BadRequest(c, "port_no 须为有效端口号")
			return
		}
	}
	query := a.DB.WithContext(c.Request.Context()).Table("charge_process").
		Select("id,ts,power_deciwatts/10.0 AS power_w,charged_mwh/1000000.0 AS charged_kwh,remaining_mwh/1000000.0 AS remaining_kwh,charged_seconds,remaining_seconds,signal_strength,port_status,voltage_v,temperature_c,device_status").
		Where("order_no=? AND id>?", orderNo, afterID)
	if chargeOrderID > 0 {
		query = query.Where("charge_order_id=?", chargeOrderID)
	}
	if deviceID != "" {
		query = query.Where("device_id=?", deviceID)
	}
	if portNo > 0 {
		query = query.Where("port_no=?", portNo)
	}
	items := []ChargeProcessPoint{}
	if err := query.Order("id ASC").Limit(limit + 1).Scan(&items).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "充电过程暂不可读取", nil)
		return
	}
	var next *uint64
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1].ID
		next = &last
	}
	for i := range items {
		items[i].TS = items[i].TS.UTC()
	}
	httpapi.OK(c, gin.H{"items": items, "next_after_id": next})
}
