package admin

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	settlementpkg "github.com/ChargePilot2026/charge-pilot/internal/central/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Withdraws 分页返回提现单，支持按状态与关键字（单号或参与方编码）过滤，按 ID 倒序。
func (s ResourceStore) Withdraws(ctx context.Context, page PageQuery) (Page[settlementpkg.WithdrawRow], error) {
	out := Page[settlementpkg.WithdrawRow]{Items: []settlementpkg.WithdrawRow{}, Page: page.Page, PageSize: page.PageSize}
	query := s.BillingDB.WithContext(ctx).Table("withdraw_request")
	if page.Status != "" {
		query = query.Where("status = ?", page.Status)
	}
	if page.Keyword != "" {
		query = query.Where("withdraw_no LIKE ? OR party_code LIKE ?", likePattern(page.Keyword), likePattern(page.Keyword))
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		return out, err
	}
	if err := query.Order("id DESC").Offset((page.Page - 1) * page.PageSize).Limit(page.PageSize).
		Find(&out.Items).Error; err != nil {
		return out, err
	}
	return out, nil
}

// createWithdraw 新建提现申请：余额重算、参与方与模板有效性校验归属 settlement 家族，
// 相同请求号重放直接返回原单据，不重复占用余额。本 handler 只做输入校验与审计编排。
func (a ResourceAPI) createWithdraw(c *gin.Context) {
	var in struct {
		RequestID   string  `json:"request_id"`   // 客户端请求号,必须是 UUID,用于幂等
		PartyID     uint64  `json:"party_id"`     // 分账参与方 ID,必须存在且其分账模板仍为 active
		AmountCents int64   `json:"amount_cents"` // 申请金额,单位分,必须大于 0 且不超过可提现余额
		Note        *string `json:"note"`         // 备注(可空),最长 255 字符
	}
	if !decodeResource(c, &in) {
		return
	}
	if in.RequestID == "" || !validUUID(in.RequestID) || in.PartyID == 0 || in.AmountCents <= 0 || (in.Note != nil && utf8Count(*in.Note) > 255) {
		httpapi.BadRequest(c, "请填写 UUID 请求号、有效参与方、正数金额和备注")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	var no string
	err := a.Store.BillingDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		no, err = (settlementpkg.WithdrawStore{}).Create(c.Request.Context(), tx, settlementpkg.WithdrawInput{
			RequestID: in.RequestID, PartyID: in.PartyID, AmountCents: in.AmountCents, Note: in.Note,
		})
		if err != nil {
			return err
		}
		return resourceAudit(tx, profile, "create", "withdraw", in.PartyID, nil,
			gin.H{"withdraw_no": no, "party_id": in.PartyID, "amount_cents": in.AmountCents}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"withdraw_no": no, "status": "pending"})
}

// decideWithdraw 按 approve 值审核提现，驳回须填写理由；状态机与余额校验归属 settlement 家族。
func (a ResourceAPI) decideWithdraw(c *gin.Context) {
	no := strings.TrimSpace(c.Param("withdraw_no"))
	var in struct {
		Approve *bool   `json:"approve"` // 是否通过(可空);为 true 表示通过,为 false 时必须同时给 reason
		Reason  *string `json:"reason"`  // 审核意见(可空),驳回时必填且不能是空白,最长 255 字符
	}
	if !decodeResource(c, &in) {
		return
	}
	if no == "" || len(no) > 64 || (in.Approve == nil && in.Reason == nil) {
		httpapi.BadRequest(c, "请提供提现单号和审核决定")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.BillingDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		action, rowID, err := (settlementpkg.WithdrawStore{}).Decide(c.Request.Context(), tx, no, profile.ID, settlementpkg.WithdrawDecision{Approve: in.Approve, Reason: in.Reason})
		if err != nil {
			return err
		}
		return resourceAudit(tx, profile, action, "withdraw", rowID, nil, gin.H{"withdraw_no": no}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"withdraw_no": no})
}

// payWithdraw 登记打款结果：状态推进与余额校验归属 settlement 家族；
// 已打款的单据重复调用只回报状态，不会重复出款。
func (a ResourceAPI) payWithdraw(c *gin.Context) {
	no := strings.TrimSpace(c.Param("withdraw_no"))
	var in struct {
		Note *string `json:"note"` // 打款备注(可空),最长 255 字符
	}
	if !decodeResource(c, &in) {
		return
	}
	if no == "" || len(no) > 64 {
		httpapi.BadRequest(c, "提现单号无效")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	alreadyPaid := false
	err := a.Store.BillingDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		rowID, paid, err := (settlementpkg.WithdrawStore{}).Pay(c.Request.Context(), tx, no, in.Note)
		if err != nil {
			return err
		}
		if paid {
			alreadyPaid = true
			return nil
		}
		return resourceAudit(tx, profile, "pay", "withdraw", rowID, nil, gin.H{"withdraw_no": no}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"withdraw_no": no, "status": "paid", "already_paid": alreadyPaid})
}

// ReconcileRow 表示 finance_reconcile_log 中单日对账结果。
type ReconcileRow struct {
	ID            uint64 `gorm:"column:id" json:"id"`                         // 对账记录主键 ID
	ReconcileType string `gorm:"column:reconcile_type" json:"reconcile_type"` // 对账类型:wechat_refund 微信退款 / wechat_pay 微信支付 / split 分账 / withdraw 提现
	ReconcileDate string `gorm:"column:reconcile_date" json:"reconcile_date"` // 对账日期,格式 YYYY-MM-DD;同一天同类型只保留一条,重跑覆盖
	InternalCount int    `gorm:"column:internal_count" json:"internal_count"` // 内部台账笔数
	ExternalCount int    `gorm:"column:wechat_count" json:"wechat_count"`     // 渠道上报笔数(库字段 wechat_count)
	DiffCount     int    `gorm:"column:diff_count" json:"diff_count"`         // 对不上的笔数
	InternalCents int64  `gorm:"column:internal_cents" json:"internal_cents"` // 内部台账金额合计,单位分
	ExternalCents int64  `gorm:"column:wechat_cents" json:"wechat_cents"`     // 渠道上报金额合计,单位分(库字段 wechat_cents)
	DiffCents     int64  `gorm:"column:diff_cents" json:"diff_cents"`         // 差额合计,单位分,等于渠道减内部
	Resolved      bool   `gorm:"column:resolved" json:"resolved"`             // 运营是否已标记为处理完毕
	CreatedAt     string `gorm:"column:created_at" json:"created_at"`         // 记录写入时间,UTC
	// Diffs 在查询后由 JSON 文本解码，避免直接将 JSON 列扫描到 any 字段。
	Diffs []ReconcileDiff `json:"diffs" gorm:"-"` // 逐条差异明细,查询后从 diffs_json 解析回填
}

// ReconcileDiff 是一条对不上的流水：同一个参照号在内部台账和渠道侧的金额以及差异原因。
type ReconcileDiff struct {
	Ref           string `json:"ref"`            // 对账参照号(内部单号或渠道流水号),按它逐笔比对
	InternalCents int64  `json:"internal_cents"` // 内部台账金额,单位分
	ChannelCents  int64  `json:"channel_cents"`  // 渠道上报金额,单位分
	Reason        string `json:"reason"`         // 差异原因:金额不一致 / 内部无对应记录
}

// runReconcile 触发一次对账：校验对账类型、日期和上报条数，把渠道数据按参照号聚合，
// 再与内部台账逐笔比对，差异原样落库交给运营判断，不做自动抹平。
func (a ResourceAPI) runReconcile(c *gin.Context) {
	var in struct {
		ReconcileType string            `json:"reconcile_type"`  // 对账类型:wechat_refund / wechat_pay / split / withdraw
		Date          string            `json:"date"`            // 对账日期,格式 YYYY-MM-DD
		Channel       []ReconcileAmount `json:"channel_amounts"` // 渠道上报明细,1-2000 条,按参照号累加后与内部比对
	}
	if !decodeResource(c, &in) {
		return
	}
	if !oneOf(in.ReconcileType, "wechat_refund wechat_pay split withdraw") {
		httpapi.BadRequest(c, "对账类型无效")
		return
	}
	date, err := time.Parse("2006-01-02", in.Date)
	if err != nil {
		httpapi.BadRequest(c, "对账日期须为 YYYY-MM-DD")
		return
	}
	if len(in.Channel) == 0 || len(in.Channel) > 2000 {
		httpapi.BadRequest(c, "请提供 1–2000 条渠道对账数据")
		return
	}
	channel := map[string]int64{}
	for _, row := range in.Channel {
		if !validText(row.Ref, 64) {
			httpapi.BadRequest(c, "渠道流水号无效")
			return
		}
		channel[row.Ref] += row.AmountCents
	}
	created, err := a.reconcileType(c, in.ReconcileType, date, channel)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, created)
}

// ReconcileAmount 是渠道上报的一行：一个参照号对应一个金额（单位分）。
type ReconcileAmount struct {
	Ref         string `json:"ref"`          // 渠道流水号,最长 64 字符
	AmountCents int64  `json:"amount_cents"` // 渠道金额,单位分;同一流水号出现多次会累加
}

// reconcileType 比对内部台账与渠道数据，计算笔数、金额及差异。
// 按类型和日期更新 finance_reconcile_log，同日重跑覆盖原结果。
func (a ResourceAPI) reconcileType(c *gin.Context, kind string, date time.Time, channel map[string]int64) (ReconcileRow, error) {
	out := ReconcileRow{ReconcileType: kind, ReconcileDate: date.Format("2006-01-02"), Diffs: []ReconcileDiff{}}
	var internal map[string]int64
	var err error
	switch kind {
	case "wechat_refund":
		internal, err = a.internalRefunds(c, date)
	case "wechat_pay":
		internal, err = a.internalPayments(c, date)
	case "split":
		internal, err = a.internalSplits(c, date)
	default:
		internal, err = a.internalWithdraws(c, date)
	}
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for ref, cents := range internal {
		seen[ref] = true
		if channel[ref] != cents {
			out.Diffs = append(out.Diffs, ReconcileDiff{Ref: ref, InternalCents: cents, ChannelCents: channel[ref], Reason: "金额不一致"})
			out.DiffCents += channel[ref] - cents
			out.DiffCount++
		}
	}
	for ref, cents := range channel {
		if !seen[ref] {
			out.Diffs = append(out.Diffs, ReconcileDiff{Ref: ref, InternalCents: 0, ChannelCents: cents, Reason: "内部无对应记录"})
			out.DiffCents += cents
			out.DiffCount++
		}
	}
	out.InternalCount, out.ExternalCount = len(internal), len(channel)
	for _, cents := range internal {
		out.InternalCents += cents
	}
	for _, cents := range channel {
		out.ExternalCents += cents
	}
	diffs, _ := json.Marshal(out.Diffs)
	// 按 type 和 date 唯一键更新对账结果，重复执行不新增记录。
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Exec(
		`INSERT INTO finance_reconcile_log(reconcile_type,reconcile_date,internal_count,wechat_count,diff_count,internal_cents,wechat_cents,diff_cents,diffs_json,resolved)
		 VALUES(?,?,?,?,?,?,?,?,?,0)
		 ON DUPLICATE KEY UPDATE internal_count=VALUES(internal_count),wechat_count=VALUES(wechat_count),diff_count=VALUES(diff_count),
		   internal_cents=VALUES(internal_cents),wechat_cents=VALUES(wechat_cents),diff_cents=VALUES(diff_cents),diffs_json=VALUES(diffs_json),resolved=0`,
		kind, date, out.InternalCount, out.ExternalCount, out.DiffCount, out.InternalCents, out.ExternalCents, out.DiffCents, string(diffs)).Error; err != nil {
		return out, err
	}
	return out, nil
}

// internalRefunds 取当天支付成功、且退款已完成的退款记录，按退款单号聚合金额，供微信退款对账。
func (a ResourceAPI) internalRefunds(c *gin.Context, date time.Time) (map[string]int64, error) {
	rows := []struct {
		RefundNo    string
		RefundCents int64
	}{}
	if err := a.Store.UserDB.WithContext(c.Request.Context()).Table("refund_record AS r").
		Joins("JOIN payment_order AS p ON p.id = r.payment_order_id").
		Where("DATE(p.paid_at) = ? AND r.status = 'succeeded' AND r.deleted_at IS NULL", date.Format("2006-01-02")).
		Select("r.refund_no, r.refund_cents").Find(&rows).Error; err != nil {
		return nil, err
	}
	return indexAmounts(rows, func(i int) (string, int64) { return rows[i].RefundNo, rows[i].RefundCents }), nil
}

// internalPayments 取当天已收款以及之后发生退款的支付单，按支付单号聚合金额，供微信支付对账。
func (a ResourceAPI) internalPayments(c *gin.Context, date time.Time) (map[string]int64, error) {
	rows := []struct {
		OrderNo   string
		PaidCents int64
	}{}
	if err := a.Store.UserDB.WithContext(c.Request.Context()).Table("payment_order").
		Where("DATE(paid_at) = ? AND status IN ('paid','partial_refunded','refunded') AND deleted_at IS NULL", date.Format("2006-01-02")).
		Select("order_no, paid_cents").Find(&rows).Error; err != nil {
		return nil, err
	}
	return indexAmounts(rows, func(i int) (string, int64) { return rows[i].OrderNo, rows[i].PaidCents }), nil
}

// internalSplits 取当天生成的分账汇总，按分账单号聚合金额，供分账对账。
func (a ResourceAPI) internalSplits(c *gin.Context, date time.Time) (map[string]int64, error) {
	rows := []struct {
		SettlementNo string
		TotalCents   int64
	}{}
	if err := a.Store.BillingDB.WithContext(c.Request.Context()).Table("settlement").
		Where("DATE(created_at) = ?", date.Format("2006-01-02")).Select("settlement_no, total_cents").Find(&rows).Error; err != nil {
		return nil, err
	}
	return indexAmounts(rows, func(i int) (string, int64) { return rows[i].SettlementNo, rows[i].TotalCents }), nil
}

// internalWithdraws 取当天创建的提现单，按提现单号聚合金额，供提现对账。
func (a ResourceAPI) internalWithdraws(c *gin.Context, date time.Time) (map[string]int64, error) {
	rows := []struct {
		WithdrawNo  string
		AmountCents int64
	}{}
	if err := a.Store.BillingDB.WithContext(c.Request.Context()).Table("withdraw_request").
		Where("DATE(created_at) = ?", date.Format("2006-01-02")).Select("withdraw_no, amount_cents").Find(&rows).Error; err != nil {
		return nil, err
	}
	return indexAmounts(rows, func(i int) (string, int64) { return rows[i].WithdrawNo, rows[i].AmountCents }), nil
}

// indexAmounts 把任意行切片按 pick 取出的“参照号 → 金额”汇总成 map，跳过空参照号，重复参照号累加。
func indexAmounts[T any](rows []T, pick func(int) (string, int64)) map[string]int64 {
	out := map[string]int64{}
	for i := range rows {
		ref, cents := pick(i)
		if ref != "" {
			out[ref] += cents
		}
	}
	return out
}

// resolveReconcile 标记一条对账记录是否已处理，行锁下按 ID 更新，重复调用结果一致。
func (a ResourceAPI) resolveReconcile(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Resolved bool   `json:"resolved"` // 是否标记为已处理
		Note     string `json:"note"`     // 处理备注,最长 255 字符
	}
	if !decodeResource(c, &in) {
		return
	}
	if utf8Count(in.Note) > 255 {
		httpapi.BadRequest(c, "备注过长")
		return
	}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var existing struct{ ID uint64 }
		if err := tx.Table("finance_reconcile_log").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&existing).Error; err != nil {
			return err
		}
		return tx.Table("finance_reconcile_log").Where("id = ?", id).Updates(map[string]any{"resolved": in.Resolved}).Error
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "resolved": in.Resolved})
}

// validUUID 判断字符串是不是合法 UUID，用于提现、导出等接口的请求号幂等校验。
func validUUID(v string) bool {
	if _, err := uuid.Parse(v); err != nil {
		return false
	}
	return true
}

// utf8Count 按 Unicode 字符而不是字节统计长度，用于中文字段的 255 之类上限校验。
func utf8Count(s string) int { return len([]rune(s)) }

// registerFinanceOps 注册分账、提现和对账路由，分别校验读取和操作权限。
func (a ResourceAPI) registerFinanceOps(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/settlements", a.Auth.Require("finance.read"), a.settlements)
	r.GET("/api/v1/admin/billing/withdraws", a.Auth.Require("finance.read"), a.withdraws)
	r.POST("/api/v1/admin/billing/withdraws", a.Auth.Require("finance.withdraw.create"), a.createWithdraw)
	r.POST("/api/v1/admin/billing/withdraws/:withdraw_no/decide", a.Auth.Require("finance.withdraw.review"), a.decideWithdraw)
	r.POST("/api/v1/admin/billing/withdraws/:withdraw_no/pay", a.Auth.Require("finance.withdraw.review"), a.payWithdraw)
	r.GET("/api/v1/admin/billing/reconciles", a.Auth.Require("finance.read"), a.reconciles)
	r.POST("/api/v1/admin/billing/reconciles", a.Auth.Require("finance.read"), a.runReconcile)
	r.POST("/api/v1/admin/billing/reconciles/:id/resolve", a.Auth.Require("finance.read"), a.resolveReconcile)
}

// settlements 分页查询分账汇总及各参与方的金额和比例。
// 查询列显式映射到 Go 字段，保持聚合结果与模型一致。
func (a ResourceAPI) settlements(c *gin.Context) {
	page, ok := parsePage(c, "pending confirmed paid failed")
	if !ok {
		return
	}
	type party struct {
		PartyID     uint64 `gorm:"column:party_id" json:"party_id"`             // 参与方 ID
		PartyCode   string `gorm:"column:party_code" json:"party_code"`         // 参与方编码
		PartyName   string `gorm:"column:party_name" json:"party_name"`         // 参与方名称
		RatioBP     uint32 `gorm:"column:ratio_bp" json:"ratio_bp"`             // 分成比例,单位万分比(bp),10000 表示 100%
		Electric    int64  `gorm:"column:electric_cents" json:"electric_cents"` // 该参与方分到的电费金额,单位分
		Service     int64  `gorm:"column:service_cents" json:"service_cents"`   // 该参与方分到的服务费金额,单位分
		AmountCents int64  `gorm:"column:amount_cents" json:"amount_cents"`     // 该参与方分成合计,单位分
		Status      string `gorm:"column:status" json:"status"`                 // 这笔分成的状态:pending 待付 / paid 已付 / failed 失败
	}
	// 显式映射查询列名，避免字段名称不一致导致扫描结果为零值。
	type row struct {
		ID               uint64  `gorm:"column:id" json:"id"`                                                                 // 分账汇总主键 ID
		SettlementNo     string  `gorm:"column:settlement_no" json:"settlement_no"`                                           // 分账单号
		OrderNo          string  `gorm:"column:order_no" json:"order_no"`                                                     // 来源充电订单号
		Mode             string  `gorm:"column:mode" json:"mode"`                                                             // 分账模式:mode_a 电费与服务费都进分账池 / mode_b 只有服务费进分账池
		TemplateCode     *string `gorm:"column:split_template_code" json:"split_template_code"`                               // 分账模板编码快照(可空),早期分账只记模板 ID
		Status           string  `gorm:"column:status" json:"status"`                                                         // 分账单状态:pending 待确认 / confirmed 已确认 / paid 已付出 / failed 失败
		TotalCents       int64   `gorm:"column:total_cents" json:"total_cents"`                                               // 订单总金额,单位分
		ElectricCents    int64   `gorm:"column:electric_cents" json:"electric_cents"`                                         // 订单电费金额,单位分
		ServiceCents     int64   `gorm:"column:service_cents" json:"service_cents"`                                           // 订单服务费金额,单位分
		SplitPoolCents   int64   `gorm:"column:split_pool_cents" json:"split_pool_cents"`                                     // 进入分账池的金额,单位分
		ExcludedElectric int64   `gorm:"column:split_pool_excluded_electric_cents" json:"split_pool_excluded_electric_cents"` // mode_b 下被排除、不进分账池的电费金额,单位分
		CreatedAt        string  `gorm:"column:created_at" json:"created_at"`                                                 // 分账单创建时间
		Parties          []party `json:"parties" gorm:"-"`                                                                    // 各参与方分成明细,查询后按分账 ID 二次回填
	}
	out := Page[row]{Items: []row{}, Page: page.Page, PageSize: page.PageSize}
	query := a.Store.BillingDB.WithContext(c.Request.Context()).Table("settlement")
	if page.Status != "" {
		query = query.Where("status = ?", page.Status)
	}
	if page.Keyword != "" {
		query = query.Where("settlement_no LIKE ? OR order_no LIKE ?", likePattern(page.Keyword), likePattern(page.Keyword))
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := query.Order("id DESC").Offset((page.Page - 1) * page.PageSize).Limit(page.PageSize).Find(&out.Items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	for i := range out.Items {
		out.Items[i].Parties = []party{}
		if err := a.Store.BillingDB.WithContext(c.Request.Context()).Table("settlement_party_amount").
			Where("settlement_id = ?", out.Items[i].ID).Order("party_code").
			Find(&out.Items[i].Parties).Error; err != nil {
			resourceFailure(c, err)
			return
		}
	}
	httpapi.OK(c, out)
}

// withdraws 分页返回提现单，状态与关键字过滤交给 ResourceStore，避免两个接口各写一套。
func (a ResourceAPI) withdraws(c *gin.Context) {
	page, ok := parsePage(c, "pending approved rejected paid failed")
	if !ok {
		return
	}
	out, err := a.Store.Withdraws(c.Request.Context(), page)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// reconciles 按类型和处理状态分页查询对账记录，将 diffs_json 转为文本后逐条解码。
func (a ResourceAPI) reconciles(c *gin.Context) {
	page, ok := parsePage(c, "wechat_refund wechat_pay split withdraw")
	if !ok {
		return
	}
	out := Page[ReconcileRow]{Items: []ReconcileRow{}, Page: page.Page, PageSize: page.PageSize}
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("finance_reconcile_log")
	if page.Status != "" {
		query = query.Where("reconcile_type = ?", page.Status)
	}
	if raw := c.Query("resolved"); raw == "true" || raw == "false" {
		query = query.Where("resolved = ?", raw == "true")
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := query.Order("reconcile_date DESC, id DESC").Offset((page.Page - 1) * page.PageSize).Limit(page.PageSize).Find(&out.Items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	// 将 diffs_json 转为文本后解码到 any 字段。
	ids := make([]uint64, 0, len(out.Items))
	for _, row := range out.Items {
		ids = append(ids, row.ID)
	}
	diffs := map[uint64][]byte{}
	if len(ids) > 0 {
		var stored []struct {
			ID    uint64 `gorm:"column:id"`
			Diffs []byte `gorm:"column:diffs_json"`
		}
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("finance_reconcile_log").
			Select("id, CAST(diffs_json AS CHAR) AS diffs_json").Where("id IN ?", ids).Find(&stored).Error; err == nil {
			for _, row := range stored {
				diffs[row.ID] = row.Diffs
			}
		}
	}
	for i := range out.Items {
		out.Items[i].Diffs = []ReconcileDiff{}
		if raw, present := diffs[out.Items[i].ID]; present && len(raw) > 0 {
			_ = json.Unmarshal(raw, &out.Items[i].Diffs)
		}
	}
	httpapi.OK(c, out)
}

// registerWebhookDelivery 注册 Webhook 投递日志查询与手动重发路由，让运营能救回一个
// 因外部原因而一直投不出去的订阅，重发复用 webhook.create 权限。
func (a ResourceAPI) registerWebhookDelivery(r *gin.Engine) {
	r.GET("/api/v1/admin/webhooks/:id/deliveries", a.Auth.Require("webhook.read"), a.webhookDeliveries)
	r.POST("/api/v1/admin/webhooks/:id/deliveries/:event_id/retry", a.Auth.Require("webhook.create"), a.retryWebhookDelivery)
}

// WebhookDeliveryRow 表示 webhook_delivery_log 中单次投递结果。
type WebhookDeliveryRow struct {
	ID             uint64  `json:"id"`              // 投递日志主键 ID
	SubscriptionID uint64  `json:"subscription_id"` // 所属 Webhook 订阅 ID
	EventID        string  `json:"event_id"`        // 事件 ID,同一订阅内唯一,重发按它定位原始事件
	EventType      string  `json:"event_type"`      // 事件类型
	RequestBody    string  `json:"request_body"`    // 实际发出的请求体 JSON
	ResponseStatus *int    `json:"response_status"` // 订阅方 HTTP 状态码；未收到响应时为 NULL。
	ResponseBody   *string `json:"response_body"`   // 订阅方返回体(可空)
	ErrorMsg       *string `json:"error_msg"`       // 投递失败原因(可空),成功时为空
	AttemptCount   uint32  `json:"attempt_count"`   // 累计投递尝试次数
	DurationMs     *uint64 `json:"duration_ms"`     // 单次请求耗时毫秒(可空)
	DeliveredAt    string  `json:"delivered_at"`    // 投递时间
}

// webhookDeliveries 分页返回某个订阅的投递日志，支持按事件 ID 或事件类型关键字过滤，按 ID 倒序。
func (a ResourceAPI) webhookDeliveries(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	page, ok := parsePage(c, "")
	if !ok {
		return
	}
	out := Page[WebhookDeliveryRow]{Items: []WebhookDeliveryRow{}, Page: page.Page, PageSize: page.PageSize}
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("webhook_delivery_log").Where("subscription_id = ?", id)
	if page.Keyword != "" {
		query = query.Where("event_id LIKE ? OR event_type LIKE ?", likePattern(page.Keyword), likePattern(page.Keyword))
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := query.Order("id DESC").Offset((page.Page - 1) * page.PageSize).Limit(page.PageSize).Find(&out.Items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

// retryWebhookDelivery 从原始事件体生成新事件，复用正式签名投递链路。
// 使用新的事件 ID；订阅停用时拒绝重发。
func (a ResourceAPI) retryWebhookDelivery(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	eventID := strings.TrimSpace(c.Param("event_id"))
	if eventID == "" || len(eventID) > 64 {
		httpapi.BadRequest(c, "事件 ID 无效")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	var subscription struct {
		URL        string   // 订阅方回调地址
		EventTypes []string // 订阅的事件类型列表
		Enabled    bool     // 订阅是否启用,停用时不允许重发
		Secret     string   // 订阅签名密钥,投递时用它计算签名
	}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("webhook_subscription").
		Where("id = ? AND deleted_at IS NULL", id).Take(&subscription).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if !subscription.Enabled {
		httpapi.Write(c, 409, 2009, "订阅已停用，无法重发", nil)
		return
	}
	var delivery struct {
		EventType   string // 原事件类型
		RequestBody string // 原事件体,重发时作为 data 原样带回
	}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("webhook_delivery_log").
		Where("subscription_id = ? AND event_id = ?", id, eventID).Take(&delivery).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	// 经由事件流重新投递，是为了让签名、SSRF 检查和投递日志都留在同一条投递
	// 链路上。
	envelope := map[string]any{
		"event_id": "retry-" + uuid.NewString(), "event_type": delivery.EventType, "source": "admin_manual_retry",
		"occurred_at": time.Now().UTC(), "data": json.RawMessage(delivery.RequestBody),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&eventOutboxRow{
			EventID: envelope["event_id"].(string), Stream: "charge_events_stream", EnvelopeJSON: payload,
		}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "retry", "webhook", id, nil, gin.H{"event_id": eventID, "subscription_id": id}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	}); err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"event_id": eventID, "requeued": true})
}

// TableName 指定 GORM 写入central_db.admin_event_outbox 表（事件流投递队列）。
func (eventOutboxRow) TableName() string { return "admin_event_outbox" }

// eventOutboxRow 是发往事件流的信封，手动重发 Webhook 时用它把事件重新排队。
type eventOutboxRow struct {
	EventID      string `gorm:"column:event_id"`      // 重发事件 ID,retry- 加新 UUID,避免与原事件去重键冲突
	Stream       string `gorm:"column:stream"`        // 目标事件流名称,固定 charge_events_stream
	EnvelopeJSON []byte `gorm:"column:envelope_json"` // 事件信封 JSON
}
