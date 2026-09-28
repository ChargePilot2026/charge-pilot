package charge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	ErrOrderNotFound = errors.New("充电订单不存在")
	ErrNotOwner      = errors.New("无权查看该订单")
)

// UserQueryAPI serves the read-only views a customer needs while charging: the
// live snapshot, the telemetry curve behind it, and their own order history.
// Telemetry lives in gateway_db, so the curve is read through the gateway's
// service-token API rather than by opening a second connection here.
type UserQueryAPI struct {
	Auth           identity.SessionAuthenticator
	DB             *gorm.DB
	GatewayURL     string
	ServiceToken   string
	Gateway        serviceclient.Client
	AllowedWindows []string
}

// CurvePoint is one telemetry sample for a charge. Values are nullable because
// a device may report only some of these metrics at a given moment.
type CurvePoint struct {
	TS           time.Time `json:"ts"`
	PowerW       *float64  `json:"power_w"`
	CurrentA     *float64  `json:"current_a"`
	VoltageV     *float64  `json:"voltage_v"`
	TemperatureC *float64  `json:"temperature_c"`
	BatterySOC   *float64  `json:"battery_soc"`
	ChargedKWh   *float64  `json:"meter_kwh"`
}

func (a UserQueryAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/charge/ongoing", a.ongoing)
	r.GET("/api/v1/user/charge/ongoing/snapshot", a.snapshot)
	r.GET("/api/v1/user/charge/ongoing/curve", a.curve)
	r.GET("/api/v1/user/charge/history", a.history)
	r.GET("/api/v1/user/charge/:order_no", a.detail)
}

// resolveOrder loads an order and proves the caller owns it. Ownership is checked
// before anything is read, so another customer's order is indistinguishable from
// a missing one.
func (a UserQueryAPI) resolveOrder(c *gin.Context, orderNo string) (ChargeOrderRecord, bool) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return ChargeOrderRecord{}, false
	}
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" || len(orderNo) > 64 {
		httpapi.BadRequest(c, "订单号无效")
		return ChargeOrderRecord{}, false
	}
	var order ChargeOrderRecord
	err := a.DB.WithContext(c.Request.Context()).Where("order_no = ? AND user_id = ? AND deleted_at IS NULL", orderNo, userID).Take(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "充电订单不存在", nil)
		return ChargeOrderRecord{}, false
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "订单暂时无法读取", nil)
		return ChargeOrderRecord{}, false
	}
	return order, true
}

func (a UserQueryAPI) ongoing(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	var order ChargeOrderRecord
	err := a.DB.WithContext(c.Request.Context()).
		Where("user_id = ? AND deleted_at IS NULL AND status IN ('paid','charging')", userID).
		Order("id DESC").Take(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.OK(c, gin.H{"order_no": "", "status": ""})
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "订单暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{
		"order_no": order.OrderNo, "status": order.Status, "device_id": order.DeviceID,
		"port_no": order.PortNo, "started_at": nullTime(order.StartedAt), "ended_at": nullTime(order.EndedAt),
	})
}

func (a UserQueryAPI) snapshot(c *gin.Context) {
	orderNo := strings.TrimSpace(c.Query("order_id"))
	if orderNo == "" {
		orderNo = strings.TrimSpace(c.Query("order_no"))
	}
	order, ok := a.resolveOrder(c, orderNo)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	payload := gin.H{
		"order_no": order.OrderNo, "status": order.Status, "device_id": order.DeviceID,
		"port_no": order.PortNo, "started_at": nullTime(order.StartedAt), "ended_at": nullTime(order.EndedAt),
		"charged_kwh": nullString(order.ChargedKWh), "charged_seconds": nullInt(order.ChargedSeconds),
		"discount_cents": order.DiscountCents,
	}
	if electric, service, total, shortfall, ok := a.fee(ctx, order.ID); ok {
		payload["electric_cents"] = electric
		payload["service_cents"] = service
		payload["total_cents"] = total
		payload["shortfall_cents"] = shortfall
	}
	// Port state is supplementary: a gateway hiccup must not fail the snapshot.
	var port struct {
		Data any `json:"data"`
	}
	if err := a.Gateway.GetJSON(c.Request.Context(), a.GatewayURL, a.ServiceToken,
		"/api/v1/internal/devices/"+order.DeviceID+"/ports/"+fmt.Sprint(order.PortNo), &port); err == nil {
		payload["port"] = port.Data
	}
	httpapi.OK(c, payload)
}

// curveWindow bounds how far back a curve request may look, so a single call
// cannot pull the whole telemetry history of a device.
type curveWindow struct {
	From  time.Time
	Label string
}

func (a UserQueryAPI) window(raw string) curveWindow {
	allowed := a.AllowedWindows
	if len(allowed) == 0 {
		allowed = []string{"last_30min", "last_2h", "last_24h"}
	}
	durations := map[string]time.Duration{"last_30min": 30 * time.Minute, "last_2h": 2 * time.Hour, "last_24h": 24 * time.Hour}
	if raw == "" {
		raw = allowed[0]
	}
	if _, ok := durations[raw]; !ok {
		raw = allowed[0]
	}
	_ = allowed
	return curveWindow{From: time.Now().UTC().Add(-durations[raw]), Label: raw}
}

func (a UserQueryAPI) curve(c *gin.Context) {
	orderNo := strings.TrimSpace(c.Query("order_id"))
	if orderNo == "" {
		orderNo = strings.TrimSpace(c.Query("order_no"))
	}
	order, ok := a.resolveOrder(c, orderNo)
	if !ok {
		return
	}
	window := a.window(strings.TrimSpace(c.Query("window")))
	ctx := c.Request.Context()

	// Telemetry is gateway-owned; a failure there is reported as such rather
	// than being smoothed into an empty series.
	var telemetry gatewayTelemetry
	if err := a.Gateway.GetJSON(ctx, a.GatewayURL, a.ServiceToken,
		fmt.Sprintf("/api/v1/internal/devices/%s/telemetry?from=%s&to=%s",
			order.DeviceID, window.From.UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)),
		&telemetry); err != nil {
		httpapi.Write(c, 503, 5003, "充电曲线暂时无法读取，请稍后重试", nil)
		return
	}
	series := make([]CurvePoint, 0, len(telemetry.Data.Series))
	for _, item := range telemetry.Data.Series {
		ts, err := time.Parse(time.RFC3339, item.TS)
		if err != nil {
			continue
		}
		series = append(series, CurvePoint{
			TS: ts.UTC(), PowerW: item.PowerW, CurrentA: item.CurrentA, VoltageV: item.VoltageV,
			TemperatureC: item.TemperatureC, BatterySOC: item.BatterySOC, ChargedKWh: item.MeterKWh,
		})
	}
	httpapi.OK(c, gin.H{
		"order_no": order.OrderNo, "window": window.Label, "from": window.From.UTC(), "to": time.Now().UTC(),
		"series": series, "count": len(series),
	})
}

// The order model uses sql.Null* columns; serialising them directly would leak
// the driver internals into the JSON, so each is unwrapped to a plain value.
func nullTime(v sql.NullTime) any {
	if !v.Valid {
		return nil
	}
	return v.Time.UTC()
}

func nullString(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

func nullInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// gatewayTelemetry mirrors the internal API envelope the gateway returns; the
// payload sits under data, not at the top level.
type gatewayTelemetry struct {
	Data struct {
		Series []struct {
			TS           string   `json:"ts"`
			PowerW       *float64 `json:"power_w"`
			CurrentA     *float64 `json:"current_a"`
			VoltageV     *float64 `json:"voltage_v"`
			TemperatureC *float64 `json:"temperature_c"`
			BatterySOC   *float64 `json:"battery_soc"`
			MeterKWh     *float64 `json:"meter_kwh"`
		} `json:"series"`
	} `json:"data"`
}

// fee reads the settled amounts for an order. The receipt is the authoritative
// record of what the customer owes once billing has run.
func (a UserQueryAPI) fee(ctx context.Context, orderID uint64) (electric, service, total, shortfall int64, ok bool) {
	var receipt ChargeFeeRecord
	if err := a.DB.WithContext(ctx).Where("charge_order_id = ?", orderID).Take(&receipt).Error; err != nil {
		return 0, 0, 0, 0, false
	}
	electric, service, total, ok = receipt.Fees()
	if !ok {
		return 0, 0, 0, 0, false
	}
	return electric, service, total, receipt.ShortfallCents, true
}

type historyRow struct {
	OrderID        uint64     `json:"order_id" gorm:"column:id"`
	OrderNo        string     `json:"order_no" gorm:"column:order_no"`
	DeviceID       string     `json:"device_id" gorm:"column:device_id"`
	PortNo         uint8      `json:"port_no" gorm:"column:port_no"`
	Status         string     `json:"status" gorm:"column:status"`
	StartedAt      *time.Time `json:"started_at" gorm:"column:started_at"`
	EndedAt        *time.Time `json:"ended_at" gorm:"column:ended_at"`
	ChargedKWh     *string    `json:"charged_kwh" gorm:"column:charged_kwh"`
	ChargedSeconds *uint32    `json:"charged_seconds" gorm:"column:charged_seconds"`
	ElectricCents  int64      `json:"electric_cents" gorm:"column:electric_cents"`
	ServiceCents   int64      `json:"service_cents" gorm:"column:service_cents"`
	TotalCents     int64      `json:"total_cents" gorm:"column:total_cents"`
	DiscountCents  int64      `json:"discount_cents" gorm:"column:discount_cents"`
}

// history pages a customer's own orders, newest first, with the same ownership
// rule as every other order read.
func (a UserQueryAPI) history(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	page, pageSize := 1, 20
	if raw := c.Query("page"); raw != "" {
		if _, err := fmt.Sscan(raw, &page); err != nil || page < 1 || page > 100000 {
			httpapi.BadRequest(c, "分页参数无效")
			return
		}
	}
	if raw := c.Query("page_size"); raw != "" {
		if _, err := fmt.Sscan(raw, &pageSize); err != nil || pageSize < 1 || pageSize > 100 {
			httpapi.BadRequest(c, "分页参数无效：page_size 为 1–100")
			return
		}
	}
	status := strings.TrimSpace(c.Query("status"))
	base := a.DB.WithContext(c.Request.Context()).Table("charge_order").Where("user_id = ? AND deleted_at IS NULL", userID)
	if status != "" {
		if !oneOfStatus(status, "pending_payment paid charging completed cancelled failed refunding refunded") {
			httpapi.BadRequest(c, "订单状态无效")
			return
		}
		base = base.Where("status = ?", status)
	}
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		httpapi.Write(c, 503, 5003, "订单暂时无法读取", nil)
		return
	}
	rows := []historyRow{}
	// The fee amounts live inside charge_fee_receipt.result_json, so they cannot
	// be selected through a join; the page of orders is read first and the
	// receipts for exactly that page are merged in afterwards.
	if err := base.Select("id, order_no, device_id, port_no, status, started_at, ended_at, charged_kwh, charged_seconds, discount_cents").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "订单暂时无法读取", nil)
		return
	}
	if len(rows) > 0 {
		ids := make([]uint64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.OrderID)
		}
		receipts := []ChargeFeeRecord{}
		if err := a.DB.WithContext(c.Request.Context()).Where("charge_order_id IN ?", ids).Find(&receipts).Error; err != nil {
			httpapi.Write(c, 503, 5003, "费用暂时无法读取", nil)
			return
		}
		byOrder := map[uint64]ChargeFeeRecord{}
		for _, receipt := range receipts {
			byOrder[receipt.ChargeOrderID] = receipt
		}
		for i := range rows {
			if receipt, present := byOrder[rows[i].OrderID]; present {
				if electric, service, total, ok := receipt.Fees(); ok {
					rows[i].ElectricCents = electric
					rows[i].ServiceCents = service
					rows[i].TotalCents = total
				}
			}
		}
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

// detail returns one order with its fee receipt and refund summary so a customer
// can see exactly what they were charged and refunded.
func (a UserQueryAPI) detail(c *gin.Context) {
	order, ok := a.resolveOrder(c, c.Param("order_no"))
	if !ok {
		return
	}
	ctx := c.Request.Context()
	payload := gin.H{
		"order_no": order.OrderNo, "status": order.Status, "device_id": order.DeviceID, "port_no": order.PortNo,
		"started_at": nullTime(order.StartedAt), "ended_at": nullTime(order.EndedAt),
		"charged_kwh": nullString(order.ChargedKWh), "charged_seconds": nullInt(order.ChargedSeconds),
		"discount_cents": order.DiscountCents,
	}
	if electric, service, total, shortfall, ok := a.fee(ctx, order.ID); ok {
		payload["electric_cents"] = electric
		payload["service_cents"] = service
		payload["total_cents"] = total
		payload["shortfall_cents"] = shortfall
	}
	if order.PaymentOrderID.Valid {
		// Column names are mapped explicitly; GORM cannot infer them from the
		// Go field names and would silently read zeros.
		// Column names are mapped explicitly; GORM cannot infer them from the
		// Go field names and would silently read zeros.
		var payment struct {
			OrderNo       string `gorm:"column:order_no" json:"order_no"`
			TotalCents    int64  `gorm:"column:total_cents" json:"total_cents"`
			PaidCents     int64  `gorm:"column:paid_cents" json:"paid_cents"`
			RefundedCents int64  `gorm:"column:refunded_cents" json:"refunded_cents"`
			Status        string `gorm:"column:status" json:"status"`
		}
		if err := a.DB.WithContext(ctx).Table("payment_order").
			Select("order_no, total_cents, paid_cents, refunded_cents, status").
			Where("id = ? AND deleted_at IS NULL", order.PaymentOrderID.Int64).Take(&payment).Error; err == nil {
			payload["payment"] = payment
		}
	}
	refunds := []map[string]any{}
	if err := a.DB.WithContext(ctx).Table("refund_record").
		Where("biz_type = 'charge' AND biz_id = ? AND deleted_at IS NULL", order.ID).
		Select("refund_no, refund_cents, status, reason, created_at").Order("id DESC").Find(&refunds).Error; err != nil {
		httpapi.Write(c, 503, 5003, "退款记录暂时无法读取", nil)
		return
	}
	payload["refunds"] = refunds
	httpapi.OK(c, payload)
}
