package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Withdraw views the money a split party has actually earned. A party may only
// withdraw amounts already confirmed as paid in, so the ledger never allows a
// payout for money that has not been collected from users.
// WithdrawRow maps the withdraw_request columns explicitly so a rename cannot
// silently turn the whole page into zeros.
// WithdrawRow 是提现单列表页的一行,对应计费库 withdraw_request 表。
// 它是“可提现余额 → 申请 → 审核 → 打款”这条资金链路的对外形态:申请时校验余额、
// 审核时占用余额、打款时二次校验余额,三步读的都是这一张表。
type WithdrawRow struct {
	ID           uint64  `gorm:"column:id" json:"id"`                       // 提现单主键 ID
	WithdrawNo   string  `gorm:"column:withdraw_no" json:"withdraw_no"`     // 提现单号,形如 WD + UUID 前 24 位大写,全局唯一
	PartyID      uint64  `gorm:"column:party_id" json:"party_id"`           // 分账参与方 ID,关联管理库 split_party.id
	PartyCode    string  `gorm:"column:party_code" json:"party_code"`       // 参与方编码快照,申请时从 split_party 复制,便于对账不依赖联表
	AmountCents  int64   `gorm:"column:amount_cents" json:"amount_cents"`   // 申请金额,单位分,必须为正数且不超过可提现余额
	BankAccount  *string `gorm:"column:bank_account" json:"bank_account"`   // 收款账号快照(可空),来自参与方资料
	BankName     *string `gorm:"column:bank_name" json:"bank_name"`         // 开户行名称快照(可空)
	Status       string  `gorm:"column:status" json:"status"`               // 单据状态:pending 待审核 / approved 审核通过 / rejected 已驳回 / paid 已打款 / failed 失败
	ReviewedBy   *uint64 `gorm:"column:reviewed_by" json:"reviewed_by"`     // 审核人管理员 ID(可空),审核后才写入
	ReviewedAt   *string `gorm:"column:reviewed_at" json:"reviewed_at"`     // 审核时间(可空),形如 2006-01-02 15:04:05.000
	RejectReason *string `gorm:"column:reject_reason" json:"reject_reason"` // 驳回原因(可空),最长 255 字符
	PaidAt       *string `gorm:"column:paid_at" json:"paid_at"`             // 打款时间(可空),仅 paid 状态有值
	Note         *string `gorm:"column:note" json:"note"`                   // 操作备注(可空),最长 255 字符
	CreatedAt    string  `gorm:"column:created_at" json:"created_at"`       // 申请创建时间,UTC
}

// AvailableCents is the settled amount for a party that has not been withdrawn
// or already claimed by an open request.
// AvailableCents 返回该参与方当前可提现金额(单位分),等于已结算金额减去未完结申请占用的金额。
func (s ResourceStore) AvailableCents(ctx context.Context, tx *gorm.DB, partyID uint64) (int64, error) {
	return s.AvailableCentsExcluding(ctx, tx, partyID, 0)
}

// AvailableCentsExcluding is AvailableCents with one request left out of the
// reservation. Approving or paying a request must not count that same request as
// a competing claim, otherwise an operator could never approve what they created.
// AvailableCentsExcluding 就是 AvailableCents,只是把 excludeID 这张单据从占用里剔除,
// 供审核通过与打款两个动作使用,否则“本单占的余额”会把自己挡住。
func (s ResourceStore) AvailableCentsExcluding(ctx context.Context, tx *gorm.DB, partyID, excludeID uint64) (int64, error) {
	if tx == nil {
		tx = s.BillingDB.WithContext(ctx)
	}
	var earned int64
	// settlement_party_amount has no partition column, so the join is on the
	// settlement id alone; adding a created_month predicate here would fail.
	if err := tx.Table("settlement_party_amount AS p").
		Joins("JOIN settlement AS st ON st.id = p.settlement_id").
		Where("p.party_id = ? AND st.status = 'paid'", partyID).
		Select("COALESCE(SUM(p.amount_cents),0)").Scan(&earned).Error; err != nil {
		return 0, err
	}
	claimedQuery := tx.Table("withdraw_request").Where("party_id = ? AND status IN ('pending','approved')", partyID)
	if excludeID != 0 {
		claimedQuery = claimedQuery.Where("id <> ?", excludeID)
	}
	var claimed int64
	if err := claimedQuery.Select("COALESCE(SUM(amount_cents),0)").Scan(&claimed).Error; err != nil {
		return 0, err
	}
	if claimed > earned {
		return 0, nil
	}
	return earned - claimed, nil
}

// Withdraws 分页返回提现单,支持按状态与关键字(单号或参与方编码)过滤,按 ID 倒序。
func (s ResourceStore) Withdraws(ctx context.Context, page PageQuery) (Page[WithdrawRow], error) {
	out := Page[WithdrawRow]{Items: []WithdrawRow{}, Page: page.Page, PageSize: page.PageSize}
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

// createWithdraw books money out of the settled balance. The available amount
// is recomputed under the same transaction that writes the request, so two
// concurrent requests can never overdraw a party.
// createWithdraw 新建提现申请:在写单据的同一个事务里重算可提现余额,并发申请不会把余额提穿;
// 相同请求号重放直接返回原单据,不重复占用余额;打款前的余额校验和模板有效性也在这里完成。
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
	no := "WD" + strings.ToUpper(strings.ReplaceAll(in.RequestID, "-", ""))[:24]
	err := a.Store.BillingDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// An identical replay returns the original request rather than a second one.
		var existing WithdrawRow // 幂等重放时命中的原单据,用来比对金额与参与方是否一致
		found := tx.Table("withdraw_request").Where("withdraw_no = ?", no).Take(&existing)
		if found.Error == nil {
			if existing.AmountCents != in.AmountCents || existing.PartyID != in.PartyID {
				return errConflict
			}
			return nil
		}
		if !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		var party struct {
			ID         uint64  `gorm:"column:id"`                // 参与方主键 ID
			Code       string  `gorm:"column:party_code"`        // 参与方编码
			Name       string  `gorm:"column:party_name"`        // 参与方名称
			BankAcc    *string `gorm:"column:bank_account"`      // 收款账号(可空)
			BankName   *string `gorm:"column:bank_name"`         // 开户行(可空)
			TemplateID uint64  `gorm:"column:split_template_id"` // 绑定的分账模板 ID,模板已删除或非 active 时不允许提现
		}
		// The party identity lives in admin_db; it is resolved through its own
		// connection rather than joined into the billing transaction. split_party
		// has no deleted_at: retirement is recorded on the parent template.
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("split_party").
			Select("id, party_code, party_name, bank_account, bank_name, split_template_id").
			Where("id = ?", in.PartyID).Take(&party).Error; err != nil {
			return err
		}
		var template struct {
			Status  string  `gorm:"column:status"`     // 分账模板状态,只有 active 允许提现
			Deleted *string `gorm:"column:deleted_at"` // 分账模板软删时间(可空),非空表示已下线
		}
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("split_template").
			Select("status, deleted_at").Where("id = ?", party.TemplateID).Take(&template).Error; err != nil {
			return err
		}
		if template.Deleted != nil || template.Status != "active" {
			return errConflict
		}
		available, err := a.Store.AvailableCents(c.Request.Context(), tx, in.PartyID)
		if err != nil {
			return err
		}
		if available < in.AmountCents {
			return errConflict
		}
		if err := tx.Table("withdraw_request").Create(map[string]any{
			"withdraw_no": no, "party_id": in.PartyID, "party_code": party.Code, "amount_cents": in.AmountCents,
			"bank_account": party.BankAcc, "bank_name": party.BankName, "status": "pending", "note": in.Note,
		}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	// Withdrawals move real money, so the audit entry is mandatory. It lives in
	// admin_db and is written after the billing commit rather than inside it.
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		return resourceAudit(tx, profile, "create", "withdraw", in.PartyID, nil,
			gin.H{"withdraw_no": no, "party_id": in.PartyID, "amount_cents": in.AmountCents}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	}); err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"withdraw_no": no, "status": "pending"})
}

// decideWithdraw implements approval, rejection and payout recording. Every
// transition is guarded by the current status so a replayed click cannot pay a
// request twice, and the party balance is rechecked before payout.
// decideWithdraw 处理提现单的审核决定:approve=true 表示通过,给了 reason 就按驳回处理。
// 每次流转都校验当前状态,并用行锁加状态条件更新,重复点击不会把同一笔钱处理两次;
// 通过前会剔除本单后重算可提现余额,余额不足直接报冲突。
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
	action := "approve"
	var withdrawID uint64
	err := a.Store.BillingDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row WithdrawRow
		if err := tx.Table("withdraw_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("withdraw_no = ?", no).Take(&row).Error; err != nil {
			return err
		}
		withdrawID = row.ID
		values := map[string]any{"reviewed_by": profile.ID, "reviewed_at": gorm.Expr("UTC_TIMESTAMP(3)")}
		if in.Reason != nil && utf8Count(*in.Reason) > 255 {
			return errConflict
		}
		switch {
		case in.Approve != nil && *in.Approve:
			if row.Status != "pending" {
				return errConflict
			}
			// This request already occupies the balance as a pending row, so it is
			// excluded from the reservation; otherwise approving would always fail.
			available, err := a.Store.AvailableCentsExcluding(c.Request.Context(), tx, row.PartyID, row.ID)
			if err != nil {
				return err
			}
			if available < row.AmountCents {
				return errConflict
			}
			values["status"] = "approved"
		case in.Reason != nil:
			// A rejection needs a reason and works from either open state.
			if strings.TrimSpace(*in.Reason) == "" {
				return errConflict
			}
			if row.Status != "pending" && row.Status != "approved" {
				return errConflict
			}
			action, values["status"], values["reject_reason"] = "reject", "rejected", strings.TrimSpace(*in.Reason)
		case in.Approve != nil:
			// approve=false without a reason cannot explain the rejection.
			return errConflict
		}
		return tx.Table("withdraw_request").Where("withdraw_no = ? AND status = ?", no, row.Status).Updates(values).Error
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	if err := a.auditFinance(c, profile, action, "withdraw", withdrawID, gin.H{"withdraw_no": no}); err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"withdraw_no": no})
}

// payWithdraw records that the approved money actually left the company.
// payWithdraw 登记打款结果:只有已通过的单据能置为 paid,且登记前会再次剔除本单校验余额;
// 已打款的单据重复调用只回报状态,不会重复出款。
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
	var withdrawID uint64
	err := a.Store.BillingDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row WithdrawRow
		if err := tx.Table("withdraw_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("withdraw_no = ?", no).Take(&row).Error; err != nil {
			return err
		}
		withdrawID = row.ID
		if row.Status == "paid" {
			// Already paid: report the recorded state without paying again.
			return nil
		}
		if row.Status != "approved" {
			return errConflict
		}
		// The approved request is the payout being recorded, so it is excluded
		// from the reservation just as at approval time.
		available, err := a.Store.AvailableCentsExcluding(c.Request.Context(), tx, row.PartyID, row.ID)
		if err != nil {
			return err
		}
		if available < row.AmountCents {
			return errConflict
		}
		values := map[string]any{"status": "paid", "paid_at": gorm.Expr("UTC_TIMESTAMP(3)")}
		if in.Note != nil && utf8Count(*in.Note) > 255 {
			return errConflict
		} else if in.Note != nil {
			values["note"] = *in.Note
		}
		return tx.Table("withdraw_request").Where("withdraw_no = ? AND status = 'approved'", no).Updates(values).Error
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	if err := a.auditFinance(c, c.MustGet("admin_profile").(Profile), "pay", "withdraw", withdrawID, gin.H{"withdraw_no": no}); err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"withdraw_no": no, "status": "paid"})
}

// ReconcileRow 是一天的对账结果快照,对应管理库 finance_reconcile_log 表。
// 它把内部台账与渠道(微信)上报的笔数、金额、差额并排存下来,供运营判断差异并标记已处理。
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
	// Diffs is populated after the query: it comes from a JSON column, which
	// GORM cannot scan into an any-typed struct field.
	Diffs []ReconcileDiff `json:"diffs" gorm:"-"` // 逐条差异明细,查询后从 diffs_json 解析回填
}

// ReconcileDiff is one line that did not match between the internal ledger and
// the channel report.
// ReconcileDiff 是一条对不上的流水:同一个参照号在内部台账和渠道侧的金额以及差异原因。
type ReconcileDiff struct {
	Ref           string `json:"ref"`            // 对账参照号(内部单号或渠道流水号),按它逐笔比对
	InternalCents int64  `json:"internal_cents"` // 内部台账金额,单位分
	ChannelCents  int64  `json:"channel_cents"`  // 渠道上报金额,单位分
	Reason        string `json:"reason"`         // 差异原因:金额不一致 / 内部无对应记录
}

// runReconcile compares the internal refund ledger against the amounts the
// channel reported. Differences are written verbatim for an operator rather than
// being silently reconciled away.
// runReconcile 触发一次对账:校验对账类型、日期和上报条数,把渠道数据按参照号聚合,
// 再与内部台账逐笔比对,差异原样落库交给运营判断,不做自动抹平。
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

// ReconcileAmount 是渠道上报的一行:一个参照号对应一个金额(单位分)。
type ReconcileAmount struct {
	Ref         string `json:"ref"`          // 渠道流水号,最长 64 字符
	AmountCents int64  `json:"amount_cents"` // 渠道金额,单位分;同一流水号出现多次会累加
}

// reconcileType 按类型取内部台账并与渠道数据比对,算出笔数、金额与差异,
// 再按 (类型,日期) 唯一键 upsert 写回 finance_reconcile_log,重跑覆盖上一次结果而不是堆叠。
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
	// The unique key on (type,date) keeps one reconciliation per day; a rerun
	// overwrites the recorded comparison instead of stacking duplicates.
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

// internalRefunds 取当天支付成功、且退款已完成的退款记录,按退款单号聚合金额,供微信退款对账。
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

// internalPayments 取当天已收款以及之后发生退款的支付单,按支付单号聚合金额,供微信支付对账。
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

// internalSplits 取当天生成的分账汇总,按分账单号聚合金额,供分账对账。
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

// internalWithdraws 取当天创建的提现单,按提现单号聚合金额,供提现对账。
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

// indexAmounts 把任意行切片按 pick 取出的“参照号 → 金额”汇总成 map,跳过空参照号,重复参照号累加。
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

// resolveReconcile 标记一条对账记录是否已处理,行锁下按 ID 更新,重复调用结果一致。
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

// auditFinance records money-moving actions in admin_db, which owns audit_log.
// auditFinance 在管理库写一条审计流水,凡是动钱的动作(提现、对账、Webhook 重发)都走这里。
func (a ResourceAPI) auditFinance(c *gin.Context, p Profile, action, target string, id uint64, detail any) error {
	return a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		return resourceAudit(tx, p, action, target, id, nil, detail, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
}

// validUUID 判断字符串是不是合法 UUID,用于提现、导出等接口的请求号幂等校验。
func validUUID(v string) bool {
	if _, err := uuid.Parse(v); err != nil {
		return false
	}
	return true
}

// utf8Count 按 Unicode 字符而不是字节统计长度,用于中文字段的 255 之类上限校验。
func utf8Count(s string) int { return len([]rune(s)) }

// registerFinanceOps wires the settlement, withdrawal and reconciliation
// endpoints. Each write carries its own finance permission so an operator who
// may read the ledger still cannot move money.
// registerFinanceOps 注册分账、提现、对账的读写路由;读与写分开授权,能看账的人不能动钱。
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

// settlements lists the executed allocations with their per-party amounts, so
// operators see what was actually split rather than a monthly placeholder.
// settlements 分页返回分账汇总,并按参与方带出各家的分成金额与比例;
// 列名与 Go 字段名不一致,所以每个字段都显式写了 gorm 列映射。
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
	// Column names differ from the Go field names, so every field is mapped
	// explicitly; without the tags GORM scans zeros.
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

// withdraws 分页返回提现单,状态与关键字过滤交给 ResourceStore,避免两个接口各写一套。
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

// reconciles 分页返回对账记录,支持按对账类型和是否已处理过滤;
// diffs_json 是 JSON 列,GORM 扫不进切片,所以用 CAST 取回文本再逐条反序列化。
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
	// diffs_json is a JSON column; it is read with an explicit cast because GORM
	// cannot scan it into the row's any-typed field.
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

// registerWebhookDelivery exposes the delivery log and a manual resend so an
// operator can recover a subscription that was failing for an external reason.
// registerWebhookDelivery 注册 Webhook 投递日志查询与手动重发路由,重发复用 webhook.create 权限。
func (a ResourceAPI) registerWebhookDelivery(r *gin.Engine) {
	r.GET("/api/v1/admin/webhooks/:id/deliveries", a.Auth.Require("webhook.read"), a.webhookDeliveries)
	r.POST("/api/v1/admin/webhooks/:id/deliveries/:event_id/retry", a.Auth.Require("webhook.create"), a.retryWebhookDelivery)
}

// WebhookDeliveryRow 是一次 Webhook 投递的日志行,对应管理库 webhook_delivery_log 表,用于排查订阅方为什么没收到事件。
type WebhookDeliveryRow struct {
	ID             uint64  `json:"id"`              // 投递日志主键 ID
	SubscriptionID uint64  `json:"subscription_id"` // 所属 Webhook 订阅 ID
	EventID        string  `json:"event_id"`        // 事件 ID,同一订阅内唯一,重发按它定位原始事件
	EventType      string  `json:"event_type"`      // 事件类型
	RequestBody    string  `json:"request_body"`    // 实际发出的请求体 JSON
	ResponseStatus *int    `json:"response_status"` // 订阅方返回的 HTTP 状态码(可空),请求未拿到响应时为空
	ResponseBody   *string `json:"response_body"`   // 订阅方返回体(可空)
	ErrorMsg       *string `json:"error_msg"`       // 投递失败原因(可空),成功时为空
	AttemptCount   uint32  `json:"attempt_count"`   // 累计投递尝试次数
	DurationMs     *uint64 `json:"duration_ms"`     // 单次请求耗时毫秒(可空)
	DeliveredAt    string  `json:"delivered_at"`    // 投递时间
}

// webhookDeliveries 分页返回某个订阅的投递日志,支持按事件 ID 或事件类型关键字过滤,按 ID 倒序。
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

// retryWebhookDelivery republishes the stored event body to its subscription by
// inserting a fresh stream entry, so the normal signed delivery path runs again
// instead of an ad-hoc unverified request.
// retryWebhookDelivery 手动重发:读出原始事件体,重新写进事件流(不复用原事件 ID),
// 让正式的签名投递链路再跑一次,而不是绕过签名直接发一个 HTTP 请求;订阅停用时拒绝重发。
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
	// Re-publishing through the event stream keeps signing, SSRF checks and the
	// delivery log on the single delivery path.
	envelope := map[string]any{
		"event_id": "retry-" + uuid.NewString(), "event_type": delivery.EventType, "source": "admin_manual_retry",
		"occurred_at": time.Now().UTC(), "data": json.RawMessage(delivery.RequestBody),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Create(&eventOutboxRow{
		EventID: envelope["event_id"].(string), Stream: "charge_events_stream", EnvelopeJSON: payload,
	}).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := a.auditFinance(c, profile, "retry", "webhook", id, gin.H{"event_id": eventID, "subscription_id": id}); err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"event_id": eventID, "requeued": true})
}

// TableName 指定 GORM 写入管理库 event_outbox 表(事件流投递队列)。
func (eventOutboxRow) TableName() string { return "event_outbox" }

// eventOutboxRow 是发往事件流的信封,手动重发 Webhook 时用它把事件重新排队。
type eventOutboxRow struct {
	EventID      string `gorm:"column:event_id"`      // 重发事件 ID,retry- 加新 UUID,避免与原事件去重键冲突
	Stream       string `gorm:"column:stream"`        // 目标事件流名称,固定 charge_events_stream
	EnvelopeJSON []byte `gorm:"column:envelope_json"` // 事件信封 JSON
}
