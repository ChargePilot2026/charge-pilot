package admin

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (a ResourceAPI) registerMeterReviews(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/meter-reviews", a.Auth.Require("finance.read"), a.meterReviews)
	r.POST("/api/v1/admin/billing/meter-reviews/:id/propose", a.Auth.Require("billing.meter.review"), a.proposeMeter)
	r.POST("/api/v1/admin/billing/meter-reviews/:id/decide", a.Auth.Require("billing.meter.review"), a.decideMeter)
}
func (a ResourceAPI) meterReviews(c *gin.Context) {
	q, ok := parsePage(c, "pending resolved")
	if !ok {
		return
	}
	type entry struct {
		ChargeOrderID uint64               `json:"charge_order_id"`
		OrderNo       string               `json:"order_no"`
		Reason        string               `json:"reason"`
		Status        string               `json:"status"`
		SourceJSON    json.RawMessage      `json:"source"`
		Reviews       []charge.MeterReview `json:"reviews" gorm:"-"`
	}
	rows := []entry{}
	db := a.Store.BillingDB.WithContext(c.Request.Context()).Table("manual_fee_review")
	if q.Status != "" {
		db = db.Where("status=?", q.Status)
	}
	if q.Keyword != "" {
		db = db.Where("order_no LIKE ? ESCAPE '!'", likePattern(q.Keyword))
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := db.Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	for i := range rows {
		rows[i].Reviews = []charge.MeterReview{}
		if err := a.Store.UserDB.WithContext(c.Request.Context()).Where("charge_order_id=?", rows[i].ChargeOrderID).Order("id DESC").Find(&rows[i].Reviews).Error; err != nil {
			resourceFailure(c, err)
			return
		}
	}
	httpapi.OK(c, Page[entry]{Items: rows, Total: total, Page: q.Page, PageSize: q.PageSize})
}
func meterFailure(c *gin.Context, err error) {
	if errors.Is(err, billing.ErrConflict) {
		httpapi.Write(c, 409, 2009, "计量核实状态已变化、原始数据不一致或审核人重复，请刷新后重试", nil)
		return
	}
	if errors.Is(err, pricing.ErrMeterReview) || errors.Is(err, pricing.ErrInvalidPricing) {
		httpapi.BadRequest(c, "计量分段须连续覆盖原始时段、总电量一致，且各段不得跨不同费率；价格快照必须有效")
		return
	}
	resourceFailure(c, err)
}
func (a ResourceAPI) proposeMeter(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	p, ok := a.financeActor(c, "billing.meter.review")
	if !ok {
		return
	}
	var in struct {
		RequestID string                 `json:"request_id"`
		Reason    string                 `json:"reason"`
		Segments  []pricing.MeterSegment `json:"segments"`
	}
	if !decodeResource(c, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if _, err := uuid.Parse(in.RequestID); err != nil || !validText(in.Reason, 500) || len(in.Segments) == 0 || len(in.Segments) > 10080 {
		httpapi.BadRequest(c, "请填写 UUID、核实依据及有效计量分段")
		return
	}
	row, err := (charge.BillingOrders{DB: a.Store.UserDB}).ProposeMeter(c.Request.Context(), id, p.ID, in.RequestID, in.Reason, in.Segments)
	if err != nil {
		meterFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}
func (a ResourceAPI) decideMeter(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	p, ok := a.financeActor(c, "billing.meter.review")
	if !ok {
		return
	}
	var in struct {
		ReviewID uint64 `json:"review_id"`
		Approve  *bool  `json:"approve"`
		Reason   string `json:"reason"`
	}
	if !decodeResource(c, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.ReviewID == 0 || in.Approve == nil || (!*in.Approve && !validText(in.Reason, 500)) {
		httpapi.BadRequest(c, "请提供审核记录、决定和拒绝依据")
		return
	}
	var review charge.MeterReview
	if err := a.Store.UserDB.WithContext(c.Request.Context()).Where("id=? AND charge_order_id=?", in.ReviewID, id).Take(&review).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if *in.Approve {
		first, err := a.Auth.Store.Profile(c.Request.Context(), review.FirstReviewerID)
		if err != nil || first.Role != "customer_finance" || !hasPermission(first, "billing.meter.review") {
			httpapi.Write(c, 409, 2009, "首次核实人的账号或权限已失效，请拒绝后重新提交", nil)
			return
		}
	}
	if err := (charge.BillingOrders{DB: a.Store.UserDB}).ReviewMeter(c.Request.Context(), id, in.ReviewID, p.ID, *in.Approve, in.Reason); err != nil {
		meterFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"approved": *in.Approve})
}
