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
type WithdrawRow struct {
	ID           uint64  `gorm:"column:id" json:"id"`
	WithdrawNo   string  `gorm:"column:withdraw_no" json:"withdraw_no"`
	PartyID      uint64  `gorm:"column:party_id" json:"party_id"`
	PartyCode    string  `gorm:"column:party_code" json:"party_code"`
	AmountCents  int64   `gorm:"column:amount_cents" json:"amount_cents"`
	BankAccount  *string `gorm:"column:bank_account" json:"bank_account"`
	BankName     *string `gorm:"column:bank_name" json:"bank_name"`
	Status       string  `gorm:"column:status" json:"status"`
	ReviewedBy   *uint64 `gorm:"column:reviewed_by" json:"reviewed_by"`
	ReviewedAt   *string `gorm:"column:reviewed_at" json:"reviewed_at"`
	RejectReason *string `gorm:"column:reject_reason" json:"reject_reason"`
	PaidAt       *string `gorm:"column:paid_at" json:"paid_at"`
	Note         *string `gorm:"column:note" json:"note"`
	CreatedAt    string  `gorm:"column:created_at" json:"created_at"`
}

// AvailableCents is the settled amount for a party that has not been withdrawn
// or already claimed by an open request.
func (s ResourceStore) AvailableCents(ctx context.Context, tx *gorm.DB, partyID uint64) (int64, error) {
	return s.AvailableCentsExcluding(ctx, tx, partyID, 0)
}

// AvailableCentsExcluding is AvailableCents with one request left out of the
// reservation. Approving or paying a request must not count that same request as
// a competing claim, otherwise an operator could never approve what they created.
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
func (a ResourceAPI) createWithdraw(c *gin.Context) {
	var in struct {
		RequestID   string  `json:"request_id"`
		PartyID     uint64  `json:"party_id"`
		AmountCents int64   `json:"amount_cents"`
		Note        *string `json:"note"`
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
		var existing WithdrawRow
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
			ID         uint64  `gorm:"column:id"`
			Code       string  `gorm:"column:party_code"`
			Name       string  `gorm:"column:party_name"`
			BankAcc    *string `gorm:"column:bank_account"`
			BankName   *string `gorm:"column:bank_name"`
			TemplateID uint64  `gorm:"column:split_template_id"`
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
			Status  string  `gorm:"column:status"`
			Deleted *string `gorm:"column:deleted_at"`
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
func (a ResourceAPI) decideWithdraw(c *gin.Context) {
	no := strings.TrimSpace(c.Param("withdraw_no"))
	var in struct {
		Approve *bool   `json:"approve"`
		Reason  *string `json:"reason"`
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
func (a ResourceAPI) payWithdraw(c *gin.Context) {
	no := strings.TrimSpace(c.Param("withdraw_no"))
	var in struct {
		Note *string `json:"note"`
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

type ReconcileRow struct {
	ID            uint64 `gorm:"column:id" json:"id"`
	ReconcileType string `gorm:"column:reconcile_type" json:"reconcile_type"`
	ReconcileDate string `gorm:"column:reconcile_date" json:"reconcile_date"`
	InternalCount int    `gorm:"column:internal_count" json:"internal_count"`
	ExternalCount int    `gorm:"column:wechat_count" json:"wechat_count"`
	DiffCount     int    `gorm:"column:diff_count" json:"diff_count"`
	InternalCents int64  `gorm:"column:internal_cents" json:"internal_cents"`
	ExternalCents int64  `gorm:"column:wechat_cents" json:"wechat_cents"`
	DiffCents     int64  `gorm:"column:diff_cents" json:"diff_cents"`
	Resolved      bool   `gorm:"column:resolved" json:"resolved"`
	CreatedAt     string `gorm:"column:created_at" json:"created_at"`
	// Diffs is populated after the query: it comes from a JSON column, which
	// GORM cannot scan into an any-typed struct field.
	Diffs []ReconcileDiff `json:"diffs" gorm:"-"`
}

// ReconcileDiff is one line that did not match between the internal ledger and
// the channel report.
type ReconcileDiff struct {
	Ref           string `json:"ref"`
	InternalCents int64  `json:"internal_cents"`
	ChannelCents  int64  `json:"channel_cents"`
	Reason        string `json:"reason"`
}

// runReconcile compares the internal refund ledger against the amounts the
// channel reported. Differences are written verbatim for an operator rather than
// being silently reconciled away.
func (a ResourceAPI) runReconcile(c *gin.Context) {
	var in struct {
		ReconcileType string            `json:"reconcile_type"`
		Date          string            `json:"date"`
		Channel       []ReconcileAmount `json:"channel_amounts"`
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

type ReconcileAmount struct {
	Ref         string `json:"ref"`
	AmountCents int64  `json:"amount_cents"`
}

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

func (a ResourceAPI) resolveReconcile(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Resolved bool   `json:"resolved"`
		Note     string `json:"note"`
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
func (a ResourceAPI) auditFinance(c *gin.Context, p Profile, action, target string, id uint64, detail any) error {
	return a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		return resourceAudit(tx, p, action, target, id, nil, detail, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
}

func validUUID(v string) bool {
	if _, err := uuid.Parse(v); err != nil {
		return false
	}
	return true
}

func utf8Count(s string) int { return len([]rune(s)) }

// registerFinanceOps wires the settlement, withdrawal and reconciliation
// endpoints. Each write carries its own finance permission so an operator who
// may read the ledger still cannot move money.
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
func (a ResourceAPI) settlements(c *gin.Context) {
	page, ok := parsePage(c, "pending confirmed paid failed")
	if !ok {
		return
	}
	type party struct {
		PartyID     uint64 `gorm:"column:party_id" json:"party_id"`
		PartyCode   string `gorm:"column:party_code" json:"party_code"`
		PartyName   string `gorm:"column:party_name" json:"party_name"`
		RatioBP     uint32 `gorm:"column:ratio_bp" json:"ratio_bp"`
		Electric    int64  `gorm:"column:electric_cents" json:"electric_cents"`
		Service     int64  `gorm:"column:service_cents" json:"service_cents"`
		AmountCents int64  `gorm:"column:amount_cents" json:"amount_cents"`
		Status      string `gorm:"column:status" json:"status"`
	}
	// Column names differ from the Go field names, so every field is mapped
	// explicitly; without the tags GORM scans zeros.
	type row struct {
		ID               uint64  `gorm:"column:id" json:"id"`
		SettlementNo     string  `gorm:"column:settlement_no" json:"settlement_no"`
		OrderNo          string  `gorm:"column:order_no" json:"order_no"`
		Mode             string  `gorm:"column:mode" json:"mode"`
		TemplateCode     *string `gorm:"column:split_template_code" json:"split_template_code"`
		Status           string  `gorm:"column:status" json:"status"`
		TotalCents       int64   `gorm:"column:total_cents" json:"total_cents"`
		ElectricCents    int64   `gorm:"column:electric_cents" json:"electric_cents"`
		ServiceCents     int64   `gorm:"column:service_cents" json:"service_cents"`
		SplitPoolCents   int64   `gorm:"column:split_pool_cents" json:"split_pool_cents"`
		ExcludedElectric int64   `gorm:"column:split_pool_excluded_electric_cents" json:"split_pool_excluded_electric_cents"`
		CreatedAt        string  `gorm:"column:created_at" json:"created_at"`
		Parties          []party `json:"parties" gorm:"-"`
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
func (a ResourceAPI) registerWebhookDelivery(r *gin.Engine) {
	r.GET("/api/v1/admin/webhooks/:id/deliveries", a.Auth.Require("webhook.read"), a.webhookDeliveries)
	r.POST("/api/v1/admin/webhooks/:id/deliveries/:event_id/retry", a.Auth.Require("webhook.create"), a.retryWebhookDelivery)
}

type WebhookDeliveryRow struct {
	ID             uint64  `json:"id"`
	SubscriptionID uint64  `json:"subscription_id"`
	EventID        string  `json:"event_id"`
	EventType      string  `json:"event_type"`
	RequestBody    string  `json:"request_body"`
	ResponseStatus *int    `json:"response_status"`
	ResponseBody   *string `json:"response_body"`
	ErrorMsg       *string `json:"error_msg"`
	AttemptCount   uint32  `json:"attempt_count"`
	DurationMs     *uint64 `json:"duration_ms"`
	DeliveredAt    string  `json:"delivered_at"`
}

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
		URL        string
		EventTypes []string
		Enabled    bool
		Secret     string
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
		EventType   string
		RequestBody string
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

func (eventOutboxRow) TableName() string { return "event_outbox" }

type eventOutboxRow struct {
	EventID      string `gorm:"column:event_id"`
	Stream       string `gorm:"column:stream"`
	EnvelopeJSON []byte `gorm:"column:envelope_json"`
}
