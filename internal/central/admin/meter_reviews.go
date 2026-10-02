package admin

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// registerMeterReviews 挂载计量核实的三个接口。列表只读，要求 finance.read；
// 提交核实和裁决核实都要求 billing.meter.review，权限在路由层就先拦一道。
func (a ResourceAPI) registerMeterReviews(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/meter-reviews", a.Auth.Require("finance.read"), a.meterReviews)
	r.POST("/api/v1/admin/billing/meter-reviews/:id/propose", a.Auth.Require("billing.meter.review"), a.proposeMeter)
	r.POST("/api/v1/admin/billing/meter-reviews/:id/decide", a.Auth.Require("billing.meter.review"), a.decideMeter)
	r.POST("/api/v1/admin/billing/meter-reviews/:id/resolve-amount", a.Auth.Require("billing.meter.review"), a.resolveMeterAmount)
}

func (a ResourceAPI) resolveMeterAmount(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	p, ok := a.financeActor(c, "billing.meter.review")
	if !ok {
		return
	}
	var in settlement.ManualSettlement
	if !decodeResource(c, &in) {
		return
	}
	in.ChargeOrderID = id
	in.ActorID = p.ID
	in.Reason = strings.TrimSpace(in.Reason)
	if err := (settlement.BillingOrders{DB: a.Store.UserDB}).ResolveAmount(c.Request.Context(), in); err != nil {
		meterFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"queued": true})
}

// meterReviews 按 pending、resolved 状态及订单号分页查询人工定价工单，逐单补充计量核实记录。
func (a ResourceAPI) meterReviews(c *gin.Context) {
	q, ok := parsePage(c, "pending resolved")
	if !ok {
		return
	}
	// entry 合并人工定价工单与计量核实记录，供列表一次返回。
	type entry struct {
		ChargeOrderID uint64                   `json:"charge_order_id"`  // 计费订单主键（central_db），核实记录靠它关联。
		OrderNo       string                   `json:"order_no"`         // 订单号，列表关键词搜索的就是它。
		Reason        string                   `json:"reason"`           // 兜底原因：自动计费为什么没能算出费用，供人工核实参考。
		Status        string                   `json:"status"`           // 兜底单状态：pending 待核实、resolved 已处置。
		SourceJSON    json.RawMessage          `json:"source"`           // 设备上报的原始计量数据（source_json），为空表示设备根本没上报可用数据。
		Reviews       []settlement.MeterReview `json:"reviews" gorm:"-"` // 该订单下的核实记录，按时间倒序；gorm:"-" 表示不由本行查询直接装载。
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
		publicSource, err := publicBillingSource(rows[i].SourceJSON)
		if err != nil {
			resourceFailure(c, err)
			return
		}
		rows[i].SourceJSON = publicSource
		rows[i].Reviews = []settlement.MeterReview{}
		if err := a.Store.UserDB.WithContext(c.Request.Context()).Where("charge_order_id=?", rows[i].ChargeOrderID).Order("id DESC").Find(&rows[i].Reviews).Error; err != nil {
			resourceFailure(c, err)
			return
		}
	}
	httpapi.OK(c, Page[entry]{Items: rows, Total: total, Page: q.Page, PageSize: q.PageSize})
}

// Preserve the exact persisted billing payload; only its public copy uses a
// string user ID. Internal receipts still decode historical numeric IDs.
func publicBillingSource(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, nil
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, err
	}
	if value, ok := source["user_id"]; ok {
		var id uint64
		if err := json.Unmarshal(value, &id); err != nil {
			return nil, err
		}
		source["user_id"], _ = json.Marshal(strconv.FormatUint(id, 10))
	}
	return json.Marshal(source)
}

// meterFailure 把核实相关的领域错误翻成管理端提示：状态被并发改动、原始数据对不上
// 或审核人重复归 409 并提示刷新；计量分段和价格快照的校验失败归 400；其余交给通用的
// resourceFailure 处理。
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

// proposeMeter 提交一次计量核实申请，path 上的 id 是兜底单的 charge_order_id。
// 分段必须连续覆盖原始时段、总电量一致、且各段不跨不同费率，上限 10080 段
// （七天按分钟切分）；request_id 必须是 UUID，用来保证重放同一次提交不会产生多条核实记录。
func (a ResourceAPI) proposeMeter(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	p, ok := a.financeActor(c, "billing.meter.review")
	if !ok {
		return
	}
	// 请求体只在此使用，不入库：核实记录由 settlement.BillingOrders 按这里的输入落库。
	var in struct {
		RequestID string                 `json:"request_id"` // 客户端生成的 UUID 幂等键。
		Reason    string                 `json:"reason"`     // 核实依据，去空白后最长 500 字符。
		Segments  []pricing.MeterSegment `json:"segments"`   // 人工修正后的计量分段，须连续覆盖原始时段且总电量与原始一致。
	}
	if !decodeResource(c, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if _, err := uuid.Parse(in.RequestID); err != nil || !validText(in.Reason, 500) || len(in.Segments) == 0 || len(in.Segments) > 10080 {
		httpapi.BadRequest(c, "请填写 UUID、核实依据及有效计量分段")
		return
	}
	row, err := (settlement.BillingOrders{DB: a.Store.UserDB}).ProposeMeter(c.Request.Context(), id, p.ID, in.RequestID, in.Reason, in.Segments)
	if err != nil {
		meterFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

// decideMeter 校验核实记录所属订单，并用 Approve 指针区分缺失字段与拒绝。
// 拒绝必须填写原因；通过前复查首次核实人的账号和权限是否仍有效。
func (a ResourceAPI) decideMeter(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	p, ok := a.financeActor(c, "billing.meter.review")
	if !ok {
		return
	}
	// review_id 指定待裁决的 charge_meter_review 记录。
	var in struct {
		ReviewID uint64 `json:"review_id"` // 待裁决的核实记录主键，必须属于本订单。
		Approve  *bool  `json:"approve"`   // 是否通过；指针是为了把"没传"和 false 区分开，拒绝时必须另填原因。
		Reason   string `json:"reason"`    // 拒绝原因，去空白后最长 500 字符；通过时可留空。
	}
	if !decodeResource(c, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.ReviewID == 0 || in.Approve == nil || (!*in.Approve && !validText(in.Reason, 500)) {
		httpapi.BadRequest(c, "请提供审核记录、决定和拒绝依据")
		return
	}
	var review settlement.MeterReview
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
	if err := (settlement.BillingOrders{DB: a.Store.UserDB}).ReviewMeter(c.Request.Context(), id, in.ReviewID, p.ID, *in.Approve, in.Reason); err != nil {
		meterFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"approved": *in.Approve})
}
