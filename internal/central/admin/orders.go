package admin

import (
	"context"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type OrderView struct {
	OrderID           uint64        `json:"order_id"`
	OrderNo           string        `json:"order_no"`
	UserID            uint64        `json:"user_id"`
	DeviceID          string        `json:"device_id"`
	PortNo            uint8         `json:"port_no"`
	StationID         *uint64       `json:"station_id"`
	StationName       *string       `json:"station_name" gorm:"-"`
	Status            string        `json:"status"`
	CreatedAt         time.Time     `json:"created_at"`
	StartedAt         *time.Time    `json:"started_at"`
	EndedAt           *time.Time    `json:"ended_at"`
	DurationSeconds   *uint64       `json:"duration_seconds"`
	MeterKWh          *string       `json:"meter_kwh" gorm:"column:meter_kwh"`
	ElectricFeeCents  *int64        `json:"electric_fee_cents"`
	ServiceFeeCents   *int64        `json:"service_fee_cents"`
	TotalFeeCents     *int64        `json:"total_fee_cents"`
	RefundStatus      string        `json:"refund_status"`
	PaymentOrderID    *uint64       `json:"payment_order_id"`
	PaymentOrderNo    *string       `json:"payment_order_no"`
	PaymentStatus     *string       `json:"payment_status"`
	PaidCents         *int64        `json:"paid_cents"`
	RefundedCents     *int64        `json:"refunded_cents"`
	FailureReason     *string       `json:"failure_reason"`
	RefundApplicantID *string       `json:"refund_applicant_id" gorm:"-"`
	Billing           *OrderBilling `json:"billing" gorm:"-"`
}
type OrderBilling struct {
	CalculationNo *string          `json:"calculation_no"`
	Settlements   []SettlementView `json:"settlements"`
}
type SettlementView struct {
	SettlementID   uint64      `json:"settlement_id" gorm:"column:id"`
	SettlementNo   string      `json:"settlement_no" gorm:"column:settlement_no"`
	Mode           string      `json:"mode" gorm:"column:mode"`
	Status         string      `json:"status" gorm:"column:status"`
	SplitPoolCents int64       `json:"split_pool_cents" gorm:"column:split_pool_cents"`
	Parties        []PartyView `json:"parties" gorm:"-"`
}
type PartyView struct {
	PartyID     uint64 `json:"party_id" gorm:"column:party_id"`
	PartyCode   string `json:"party_code" gorm:"column:party_code"`
	PartyName   string `json:"party_name" gorm:"column:party_name"`
	RatioBP     uint32 `json:"ratio_bp" gorm:"column:ratio_bp"`
	AmountCents int64  `json:"amount_cents" gorm:"column:amount_cents"`
	Status      string `json:"status" gorm:"column:status"`
}
type OrderQuery struct {
	PageQuery
	OrderNo, DeviceID string
	StationID         uint64
	From, To          *time.Time
}

const orderColumns = `c.id AS order_id,c.order_no,c.user_id,c.device_id,c.port_no,i.station_id,c.status,c.created_at,c.started_at,c.ended_at,
 c.charged_seconds AS duration_seconds,c.charged_kwh AS meter_kwh,c.electric_cents AS electric_fee_cents,c.service_cents AS service_fee_cents,c.total_cents AS total_fee_cents,
 c.payment_order_id,p.order_no AS payment_order_no,p.status AS payment_status,p.paid_cents,p.refunded_cents,c.failure_reason,
 CASE WHEN p.status='refunded' THEN 'refunded' WHEN p.refunded_cents>0 THEN 'partial_refunded' WHEN c.status='refunding' OR EXISTS (SELECT 1 FROM refund_record r WHERE r.payment_order_id=p.id AND r.status IN ('pending','processing') AND r.deleted_at IS NULL) THEN 'processing' ELSE 'none' END AS refund_status`

func (s ResourceStore) orderQuery(ctx context.Context) *gorm.DB {
	return s.UserDB.WithContext(ctx).Table("charge_order AS c").Joins("LEFT JOIN payment_order AS p ON p.id=c.payment_order_id AND p.user_id=c.user_id AND p.deleted_at IS NULL").Joins("LEFT JOIN charge_payment_intent AS i ON i.charge_order_id=c.id").Where("c.deleted_at IS NULL")
}
func (s ResourceStore) Orders(ctx context.Context, q OrderQuery) (Page[OrderView], error) {
	out := Page[OrderView]{Items: []OrderView{}, Page: q.Page, PageSize: q.PageSize}
	query := s.orderQuery(ctx)
	if q.OrderNo != "" {
		query = query.Where("c.order_no = ?", q.OrderNo)
	}
	if q.DeviceID != "" {
		query = query.Where("c.device_id = ?", q.DeviceID)
	}
	if q.Status != "" {
		query = query.Where("c.status = ?", q.Status)
	}
	if q.StationID != 0 {
		query = query.Where("i.station_id = ?", q.StationID)
	}
	if q.From != nil {
		query = query.Where("COALESCE(c.started_at,c.created_at) >= ?", q.From.UTC())
	}
	if q.To != nil {
		query = query.Where("COALESCE(c.started_at,c.created_at) <= ?", q.To.UTC())
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	if err := query.Select(orderColumns).Order("c.created_at DESC,c.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Scan(&out.Items).Error; err != nil {
		return out, err
	}
	return out, s.orderStations(ctx, out.Items)
}
func (s ResourceStore) orderStations(ctx context.Context, rows []OrderView) error {
	ids := []uint64{}
	for _, r := range rows {
		if r.StationID != nil {
			ids = append(ids, *r.StationID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var stations []Station
	if err := s.AdminDB.WithContext(ctx).Select("id,name").Where("id IN ? AND deleted_at IS NULL", ids).Find(&stations).Error; err != nil {
		return err
	}
	names := map[uint64]string{}
	for _, r := range stations {
		names[r.ID] = r.Name
	}
	for i := range rows {
		if rows[i].StationID != nil {
			if name, ok := names[*rows[i].StationID]; ok {
				rows[i].StationName = &name
			}
		}
	}
	return nil
}
func (a ResourceAPI) orders(c *gin.Context) {
	page, ok := parsePage(c, "pending_payment paid charging completed cancelled failed refunding refunded")
	if !ok {
		return
	}
	q := OrderQuery{PageQuery: page, OrderNo: c.Query("order_no"), DeviceID: c.Query("device_id")}
	if len(q.OrderNo) > 64 || len(q.DeviceID) > 64 {
		httpapi.BadRequest(c, "订单号或设备号过长")
		return
	}
	if raw := c.Query("station_id"); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || id == 0 {
			httpapi.BadRequest(c, "站点 ID 无效")
			return
		}
		q.StationID = id
	}
	for key, target := range map[string]**time.Time{"started_from": &q.From, "started_to": &q.To} {
		if raw := c.Query(key); raw != "" {
			v, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				httpapi.BadRequest(c, "时间格式须为 ISO 8601")
				return
			}
			*target = &v
		}
	}
	if q.From != nil && q.To != nil && q.From.After(*q.To) {
		httpapi.BadRequest(c, "开始时间不能晚于结束时间")
		return
	}
	out, err := a.Store.Orders(c.Request.Context(), q)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}
func (a ResourceAPI) order(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var row OrderView
	if err := a.Store.orderQuery(ctx).Select(orderColumns).Where("c.id = ?", id).Take(&row).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	rows := []OrderView{row}
	if err := a.Store.orderStations(ctx, rows); err != nil {
		resourceFailure(c, err)
		return
	}
	row = rows[0]
	billing := OrderBilling{Settlements: []SettlementView{}}
	var fee struct{ CalculationNo *string }
	if err := a.Store.BillingDB.WithContext(ctx).Table("fee_receipt").Select("calculation_no").Where("charge_order_id = ?", id).Limit(1).Scan(&fee).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	billing.CalculationNo = fee.CalculationNo
	if err := a.Store.BillingDB.WithContext(ctx).Table("settlement").Where("order_no = ?", row.OrderNo).Order("id").Find(&billing.Settlements).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	for i := range billing.Settlements {
		billing.Settlements[i].Parties = []PartyView{}
		if err := a.Store.BillingDB.WithContext(ctx).Table("settlement_party_amount").Where("settlement_id = ?", billing.Settlements[i].SettlementID).Order("id").Find(&billing.Settlements[i].Parties).Error; err != nil {
			resourceFailure(c, err)
			return
		}
	}
	row.Billing = &billing
	for _, p := range c.MustGet("admin_profile").(Profile).Permissions {
		if p == "order.refund.create" {
			actor := strconv.FormatUint(c.MustGet("admin_profile").(Profile).ID, 10)
			row.RefundApplicantID = &actor
		}
	}
	httpapi.OK(c, row)
}
func (a ResourceAPI) timeline(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var order struct{ ID uint64 }
	if err := a.Store.UserDB.WithContext(ctx).Table("charge_order").Select("id").Where("id = ? AND deleted_at IS NULL", id).Take(&order).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	rows := []struct {
		EventID string    `json:"event_id"`
		At      time.Time `json:"at" gorm:"column:occurred_at"`
		Event   string    `json:"event"`
		Actor   string    `json:"actor"`
		Detail  string    `json:"detail"`
	}{}
	if err := a.Store.UserDB.WithContext(ctx).Table("charge_event_log").Where("charge_order_id = ?", id).Order("occurred_at,id").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"order_id": id, "timeline": rows})
}
