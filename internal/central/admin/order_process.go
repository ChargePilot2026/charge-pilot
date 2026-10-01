package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

// ChargeProcessPoint 是原始心跳过程读数；可空字段保留未上报状态，不补零或插值。
type ChargeProcessPoint struct {
	ID               uint64    `json:"id"`
	TS               time.Time `json:"ts"`
	PowerW           float64   `json:"power_w"`
	ChargedKWh       float64   `json:"charged_kwh"`
	RemainingKWh     float64   `json:"remaining_kwh"`
	ChargedSeconds   uint32    `json:"charged_seconds"`
	RemainingSeconds uint32    `json:"remaining_seconds"`
	SignalStrength   uint8     `json:"signal_strength"`
	PortStatus       *uint8    `json:"port_status"`
	VoltageV         *uint16   `json:"voltage_v"`
	TemperatureC     *int16    `json:"temperature_c"`
	DeviceStatus     *uint8    `json:"device_status"`
}

// OrderProcessPage 按过程行 ID 游标返回，不截断订单历史；末页游标显式为 null。
type OrderProcessPage struct {
	Items       []ChargeProcessPoint `json:"items"`
	NextAfterID *uint64              `json:"next_after_id"`
}

type orderProcessQuery struct {
	AfterID uint64
	Limit   uint16
}

type orderProcessIdentity struct {
	ID       uint64
	OrderNo  string
	DeviceID string
	PortNo   uint8
}

var errOrderProcess = errors.New("充电过程暂时无法读取，请稍后重试")

// parseOrderProcessQuery 拒绝空值、重复游标和超限分页，避免悄悄回落到第一页。
func parseOrderProcessQuery(c *gin.Context) (orderProcessQuery, bool) {
	q := orderProcessQuery{Limit: 1000}
	values := c.Request.URL.Query()
	for _, name := range []string{"after_id", "limit"} {
		if raw, exists := values[name]; exists {
			if len(raw) != 1 || raw[0] == "" {
				httpapi.BadRequest(c, "过程游标或分页参数无效")
				return q, false
			}
			n, err := strconv.ParseUint(raw[0], 10, 64)
			if err != nil || (name == "limit" && (n == 0 || n > 2000)) {
				httpapi.BadRequest(c, "过程游标必须非负，limit 为 1–2000")
				return q, false
			}
			if name == "after_id" {
				q.AfterID = n
			} else {
				q.Limit = uint16(n)
			}
		}
	}
	return q, true
}

// orderProcess 只从有效订单取规范身份，客户端只能指定分页，不能改变订单归属。
func (a ResourceAPI) orderProcess(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	q, ok := parseOrderProcessQuery(c)
	if !ok {
		return
	}
	if a.Store.UserDB == nil {
		resourceFailure(c, errOrderProcess)
		return
	}
	var order orderProcessIdentity
	if err := a.Store.UserDB.WithContext(c.Request.Context()).Table("charge_order").
		Select("id,order_no,device_id,port_no").Where("id=? AND deleted_at IS NULL", id).Take(&order).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	page, err := a.readOrderProcess(c.Request.Context(), order, q)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, errOrderProcess.Error(), nil)
		return
	}
	httpapi.OK(c, page)
}

func (a ResourceAPI) readOrderProcess(ctx context.Context, order orderProcessIdentity, q orderProcessQuery) (OrderProcessPage, error) {
	if a.GatewayURL == "" || a.ServiceToken == "" || order.ID == 0 || order.OrderNo == "" || order.DeviceID == "" || order.PortNo == 0 {
		return OrderProcessPage{}, errOrderProcess
	}
	query := url.Values{
		"charge_order_id": {strconv.FormatUint(order.ID, 10)},
		"device_id":       {order.DeviceID},
		"port_no":         {strconv.FormatUint(uint64(order.PortNo), 10)},
		"after_id":        {strconv.FormatUint(q.AfterID, 10)},
		"limit":           {strconv.FormatUint(uint64(q.Limit), 10)},
	}
	var result struct {
		Code *int `json:"code"`
		Data *struct {
			Items       []ChargeProcessPoint `json:"items"`
			NextAfterID json.RawMessage      `json:"next_after_id"`
		} `json:"data"`
	}
	client := serviceclient.Client{HTTP: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
	path := "/api/v1/internal/charge-orders/" + url.PathEscape(order.OrderNo) + "/process?" + query.Encode()
	if err := client.GetJSON(ctx, a.GatewayURL, a.ServiceToken, path, &result); err != nil || result.Code == nil || *result.Code != 0 || result.Data == nil || result.Data.Items == nil || len(result.Data.NextAfterID) == 0 {
		return OrderProcessPage{}, errOrderProcess
	}
	page := OrderProcessPage{Items: result.Data.Items}
	if err := json.Unmarshal(result.Data.NextAfterID, &page.NextAfterID); err != nil || len(page.Items) > int(q.Limit) {
		return OrderProcessPage{}, errOrderProcess
	}
	previous := q.AfterID
	for _, item := range page.Items {
		if item.ID <= previous || item.TS.IsZero() {
			return OrderProcessPage{}, errOrderProcess
		}
		previous = item.ID
	}
	if page.NextAfterID != nil && (len(page.Items) == 0 || *page.NextAfterID != previous) {
		return OrderProcessPage{}, errOrderProcess
	}
	return page, nil
}
