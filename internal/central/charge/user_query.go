package charge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	orderpkg "github.com/ChargePilot2026/charge-pilot/internal/central/order"
	settlementpkg "github.com/ChargePilot2026/charge-pilot/internal/central/settlement"
	"net/url"
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

// UserQueryAPI 提供用户实时状态、遥测曲线及订单历史。
// 遥测由 gateway_db 管理，通过带服务令牌的网关接口读取。
type UserQueryAPI struct {
	Auth           identity.SessionAuthenticator
	DB             *gorm.DB
	GatewayURL     string
	ServiceToken   string
	Gateway        serviceclient.Client
	AllowedWindows []string
}

// CurvePoint 是一次充电的一个遥测采样点。
// 这些值可为空，因为设备在某一时刻可能只上报其中一部分指标。
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
	r.GET("/api/v1/user/charge/:order_no/curve", a.historyCurve)
	r.GET("/api/v1/user/charge/:order_no", a.detail)
}

// resolveOrder 读取订单并校验用户归属；越权与不存在返回相同错误。
func (a UserQueryAPI) resolveOrder(c *gin.Context, orderNo string) (orderpkg.ChargeOrderRecord, bool) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return orderpkg.ChargeOrderRecord{}, false
	}
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" || len(orderNo) > 64 {
		httpapi.BadRequest(c, "订单号无效")
		return orderpkg.ChargeOrderRecord{}, false
	}
	var order orderpkg.ChargeOrderRecord
	err := a.DB.WithContext(c.Request.Context()).Where("order_no = ? AND user_id = ? AND deleted_at IS NULL", orderNo, userID).Take(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "充电订单不存在", nil)
		return orderpkg.ChargeOrderRecord{}, false
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "订单暂时无法读取", nil)
		return orderpkg.ChargeOrderRecord{}, false
	}
	return order, true
}

func (a UserQueryAPI) ongoing(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	var order orderpkg.ChargeOrderRecord
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
	// 端口状态只是补充信息：网关抖一下不该让整个快照失败。
	payload["poll_continue"] = order.Status == "paid" || order.Status == "charging"
	payload["next_poll_after_ms"] = 5000
	if err := a.schemeView(ctx, order, payload); err != nil {
		httpapi.Write(c, 503, 5003, "订单方案及确认状态暂不可读取", nil)
		return
	}
	payload["telemetry_available"] = false
	if order.Status == "charging" && order.StartedAt.Valid {
		now := time.Now().UTC()
		payload["elapsed_seconds"] = max(int64(0), int64(now.Sub(order.StartedAt.Time).Seconds()))
		from := now.Add(-2 * time.Minute)
		if order.StartedAt.Time.After(from) {
			from = order.StartedAt.Time
		}
		var telemetry gatewayTelemetry
		path := fmt.Sprintf("/api/v1/internal/devices/%s/telemetry?port_no=%d&from=%s&to=%s", url.PathEscape(order.DeviceID), order.PortNo, url.QueryEscape(from.Format(time.RFC3339Nano)), url.QueryEscape(now.Format(time.RFC3339Nano)))
		if err := a.Gateway.GetJSON(ctx, a.GatewayURL, a.ServiceToken, path, &telemetry); err == nil {
			for _, point := range telemetry.Data.Series {
				ts, err := time.Parse(time.RFC3339Nano, point.TS)
				if err != nil || ts.Before(from) || ts.After(now) {
					continue
				}
				if point.PowerW != nil || point.MeterKWh != nil {
					payload["telemetry_available"] = true
				}
				payload["telemetry_at"] = ts
				if point.PowerW != nil {
					payload["current_power_w"] = *point.PowerW
				}
				if point.VoltageV != nil {
					payload["voltage_v"] = *point.VoltageV
				}
				if point.TemperatureC != nil {
					payload["temperature_c"] = *point.TemperatureC
				}
				if point.MeterKWh != nil {
					payload["charged_kwh"] = *point.MeterKWh
				}
			}
		}
	}
	httpapi.OK(c, payload)
}

// curveWindow 限制曲线请求最多能回看多远，
// 避免一次调用把某台设备的整段遥测历史都拉出来。
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

	// 网关遥测查询失败时返回错误，不将故障转换为空曲线。
	var telemetry gatewayTelemetry
	if err := a.Gateway.GetJSON(ctx, a.GatewayURL, a.ServiceToken,
		fmt.Sprintf("/api/v1/internal/devices/%s/telemetry?port_no=%d&from=%s&to=%s",
			url.PathEscape(order.DeviceID), order.PortNo, window.From.UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)),
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

// 逐字段转换 sql.Null*，保留 NULL 语义，避免驱动内部结构进入 JSON。
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

// gatewayTelemetry 对应网关返回的内部 API 信封；
// 载荷在 data 下面，不在顶层。
type gatewayTelemetry struct {
	Data struct {
		Granularity         string         `json:"granularity"`
		BoundaryApproximate bool           `json:"boundary_approximate"`
		Summary             map[string]any `json:"summary"`
		Series              []struct {
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

func (a UserQueryAPI) historyCurve(c *gin.Context) {
	order, ok := a.resolveOrder(c, c.Param("order_no"))
	if !ok {
		return
	}
	if !order.StartedAt.Valid {
		httpapi.Write(c, 409, 2009, "订单尚未开始充电", nil)
		return
	}
	granularity := c.DefaultQuery("granularity", "15min")
	if granularity != "15min" && granularity != "hourly" {
		httpapi.BadRequest(c, "granularity 须为 15min 或 hourly")
		return
	}
	started := order.StartedAt.Time.UTC()
	ended := time.Now().UTC()
	if order.EndedAt.Valid {
		ended = order.EndedAt.Time.UTC()
	}
	if !started.Before(ended) {
		httpapi.Write(c, 409, 2009, "订单时间窗无效", nil)
		return
	}
	if started.Before(time.Now().UTC().AddDate(-3, 0, 0)) {
		httpapi.Write(c, 422, 2018, "历史遥测已超过三年保留期", nil)
		return
	}
	bucket := 15 * time.Minute
	if granularity == "hourly" {
		bucket = time.Hour
	}
	if int(ended.Truncate(bucket).Sub(started.Truncate(bucket))/bucket)+1 > 2000 {
		httpapi.BadRequest(c, "订单时间窗超过当前粒度的最大点数，请使用更粗粒度")
		return
	}
	query := url.Values{}
	query.Set("order_id", order.OrderNo)
	query.Set("port_no", fmt.Sprint(order.PortNo))
	query.Set("started_at", started.Format(time.RFC3339Nano))
	query.Set("ended_at", ended.Format(time.RFC3339Nano))
	query.Set("granularity", granularity)
	var telemetry gatewayTelemetry
	path := "/api/v1/internal/devices/" + url.PathEscape(order.DeviceID) + "/historical-curve?" + query.Encode()
	if err := a.Gateway.GetJSON(c.Request.Context(), a.GatewayURL, a.ServiceToken, path, &telemetry); err != nil {
		httpapi.Write(c, 503, 5003, "历史充电曲线暂时无法读取", nil)
		return
	}
	summary := telemetry.Data.Summary
	if summary == nil {
		summary = map[string]any{}
	}
	if order.ChargedKWh.Valid {
		summary["total_kwh"] = order.ChargedKWh.String
	}
	httpapi.OK(c, gin.H{
		"order_id": order.ID, "order_no": order.OrderNo, "granularity": granularity,
		"series": telemetry.Data.Series, "summary": summary,
		"boundary_approximate": telemetry.Data.BoundaryApproximate,
	})
}

// fee 从已确认的计费回执读取订单费用及未结金额。
func (a UserQueryAPI) fee(ctx context.Context, orderID uint64) (electric, service, total, shortfall int64, ok bool) {
	var receipt settlementpkg.ChargeFeeRecord
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
	ElectricCents  *int64     `json:"electric_cents" gorm:"column:electric_cents"`
	ServiceCents   *int64     `json:"service_cents" gorm:"column:service_cents"`
	TotalCents     *int64     `json:"total_cents" gorm:"column:total_cents"`
	DiscountCents  int64      `json:"discount_cents" gorm:"column:discount_cents"`
}

// history 按最新优先分页返回客户自己的订单，
// 归属规则与其他所有订单读取完全一致。
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
	// 费用保存在 charge_fee_receipt.result_json 中，分页读取订单后批量加载对应回执并解析金额。
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
		receipts := []settlementpkg.ChargeFeeRecord{}
		if err := a.DB.WithContext(c.Request.Context()).Where("charge_order_id IN ?", ids).Find(&receipts).Error; err != nil {
			httpapi.Write(c, 503, 5003, "费用暂时无法读取", nil)
			return
		}
		byOrder := map[uint64]settlementpkg.ChargeFeeRecord{}
		for _, receipt := range receipts {
			byOrder[receipt.ChargeOrderID] = receipt
		}
		for i := range rows {
			if receipt, present := byOrder[rows[i].OrderID]; present {
				if electric, service, total, ok := receipt.Fees(); ok {
					rows[i].ElectricCents = &electric
					rows[i].ServiceCents = &service
					rows[i].TotalCents = &total
				}
			}
		}
	}
	httpapi.OK(c, gin.H{"items": rows, "total": total, "page": page, "page_size": pageSize})
}

// detail 返回一笔订单及其费用回执和退款汇总，
// 让客户能看到自己到底被收了多少、退了多少。
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
		// 显式映射数据库列名，避免 GORM 名称推导产生零值。
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
	if err := a.schemeView(ctx, order, payload); err != nil {
		httpapi.Write(c, 503, 5003, "订单方案及确认状态暂不可读取", nil)
		return
	}
	httpapi.OK(c, payload)
}
