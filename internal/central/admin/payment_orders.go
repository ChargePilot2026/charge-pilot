package admin

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PaymentOrderView 包含实际支付、余额扣费与充值记录；充电生命周期由关联充电单展示。
type PaymentOrderView struct {
	PaymentOrderID uint64     `json:"payment_order_id"`
	OrderNo        string     `json:"order_no"`
	UserID         uint64     `json:"user_id,string"`
	BizType        string     `json:"biz_type"`       // charge 充电支付，wallet_recharge 余额充值。
	PayMethod      string     `json:"pay_method"`     // wechat 微信支付，balance 余额支付。
	Status         string     `json:"status"`         // 支付单内部流程状态，保留关闭和失败等原因。
	PaymentStatus  string     `json:"payment_status"` // 金额确认后的 pending/paid/refunded/partial_refunded。
	TotalCents     int64      `json:"total_cents"`
	PaidCents      int64      `json:"paid_cents"`
	RefundedCents  int64      `json:"refunded_cents"` // 累计成功退款金额，单位分。
	PaidAt         *time.Time `json:"paid_at"`
	CreatedAt      time.Time  `json:"created_at"`
	ChargeOrderID  *uint64    `json:"charge_order_id"`
	ChargeOrderNo  *string    `json:"charge_order_no"`
}

type PaymentOrderQuery struct {
	PageQuery
	OrderNo, BizType, PayMethod, PaymentStatus string
	From, To                                   *time.Time
}

// PaymentOrders 直接读取支付单，充值不会被误列为充电订单。
func (s ResourceStore) PaymentOrders(ctx context.Context, q PaymentOrderQuery) (Page[PaymentOrderView], error) {
	out := Page[PaymentOrderView]{Items: []PaymentOrderView{}, Page: q.Page, PageSize: q.PageSize}
	query := s.UserDB.WithContext(ctx).Table("payment_order AS p").Where("p.deleted_at IS NULL")
	if q.OrderNo != "" {
		query = query.Where("p.order_no=?", q.OrderNo)
	}
	if q.BizType != "" {
		query = query.Where("p.biz_type=?", q.BizType)
	}
	if q.PayMethod != "" {
		query = query.Where("p.pay_method=?", q.PayMethod)
	}
	if q.PaymentStatus != "" {
		query = query.Where("("+charge.PaymentStatusSQL+")=?", q.PaymentStatus)
	}
	if q.From != nil {
		query = query.Where("p.created_at >= ?", q.From.UTC())
	}
	if q.To != nil {
		query = query.Where("p.created_at <= ?", q.To.UTC())
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	// 标量子查询限制为一条，历史异常关联也不会放大分页总数。充值单始终不关联充电单。
	linked := " FROM charge_order c WHERE p.biz_type='charge' AND c.payment_order_id=p.id AND c.user_id=p.user_id AND c.deleted_at IS NULL ORDER BY c.created_at DESC,c.id DESC LIMIT 1)"
	columns := "p.id AS payment_order_id,p.order_no,p.user_id,p.biz_type,p.pay_method,p.status," + charge.PaymentStatusSQL + " AS payment_status," +
		"p.total_cents,p.paid_cents,p.refunded_cents,p.paid_at,p.created_at," +
		"(SELECT c.id" + linked + " AS charge_order_id,(SELECT c.order_no" + linked + " AS charge_order_no"
	err := query.Select(columns).Order("p.created_at DESC,p.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Scan(&out.Items).Error
	return out, err
}

func (a ResourceAPI) paymentOrders(c *gin.Context) {
	page, ok := parsePage(c, "")
	if !ok {
		return
	}
	q := PaymentOrderQuery{PageQuery: page, OrderNo: c.Query("order_no"), BizType: c.Query("biz_type"),
		PayMethod: c.Query("pay_method"), PaymentStatus: c.Query("payment_status")}
	if utf8.RuneCountInString(q.OrderNo) > 64 || !oneOf(q.BizType, "charge wallet_recharge") ||
		!oneOf(q.PayMethod, "wechat balance") || !oneOf(q.PaymentStatus, "pending paid refunded partial_refunded") {
		httpapi.BadRequest(c, "支付单号、用途、支付方式或支付状态无效")
		return
	}
	var err error
	q.From, q.To, err = paymentTimeRange(c.Query("created_from"), c.Query("created_to"))
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	out, err := a.Store.PaymentOrders(c.Request.Context(), q)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

func paymentTimeRange(fromRaw, toRaw string) (*time.Time, *time.Time, error) {
	var from, to *time.Time
	for _, bound := range []struct {
		raw    string
		target **time.Time
	}{{fromRaw, &from}, {toRaw, &to}} {
		raw, target := bound.raw, bound.target
		if raw == "" {
			continue
		}
		value, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil || value.UTC().Year() < 1000 || value.UTC().Year() > 9999 {
			return nil, nil, errors.New("时间格式须为 ISO 8601，且在有效日期范围内")
		}
		*target = &value
	}
	if from != nil && to != nil && from.After(*to) {
		return nil, nil, errors.New("开始时间不能晚于结束时间")
	}
	return from, to, nil
}
