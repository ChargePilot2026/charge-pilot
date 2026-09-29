package admin

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"
	"unicode"

	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a ResourceAPI) registerWalletRisks(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/wallet-risks", a.Auth.Require("finance.wallet_risk.review"), a.walletRisks)
	r.POST("/api/v1/admin/billing/wallet-risks/:request_id/review", a.Auth.Require("finance.wallet_risk.review"), a.reviewWalletRisk)
	r.POST("/api/v1/admin/billing/wallet-risks/:request_id/release", a.Auth.Require("finance.wallet_risk.release"), a.releaseWalletRisk)
}
func (a ResourceAPI) walletRisks(c *gin.Context) {
	q, ok := parsePage(c, "pending reviewed")
	if !ok {
		return
	}
	db := a.Store.UserDB.WithContext(c.Request.Context()).Table("wallet_refund_request r").Joins("LEFT JOIN wallet_risk_review v ON v.request_id=r.request_id LEFT JOIN wallet_risk_freeze_link l ON l.request_id=r.request_id LEFT JOIN risk_freeze_log f ON f.id=l.freeze_id LEFT JOIN wallet_risk_release x ON x.request_id=r.request_id").Where("l.freeze_id IS NOT NULL")
	if q.Status == "reviewed" {
		db = db.Where("v.request_id IS NOT NULL")
	} else {
		db = db.Where("v.request_id IS NULL")
	}
	out := Page[map[string]any]{Items: []map[string]any{}, Page: q.Page, PageSize: q.PageSize}
	if e := db.Count(&out.Total).Error; e != nil {
		resourceFailure(c, e)
		return
	}
	if e := db.Select("r.request_id,r.user_id,r.amount_cents,r.reason,r.created_at,v.response_json AS review_json,v.created_at AS review_created_at,x.response_json AS release_json,x.created_at AS release_created_at,f.status AS freeze_status,f.trigger_rule,l.freeze_id").Order("r.created_at DESC,r.request_id").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error; e != nil {
		resourceFailure(c, e)
		return
	}
	normalizeRows(out.Items)
	stringIDs(out.Items, "user_id")
	p := c.MustGet("admin_profile").(Profile)
	for _, row := range out.Items {
		row["can_review"] = (p.Role == "customer_finance" || p.Role == "customer_cs") && hasPermission(p, "finance.wallet_risk.review")
		row["review"] = row["review_json"]
		row["release"] = row["release_json"]
		row["freeze_linked"] = row["freeze_id"] != nil
		row["can_release"] = p.Role == "customer_finance" && hasPermission(p, "finance.wallet_risk.release") && row["review"] != nil && row["release"] == nil && row["freeze_status"] == "frozen" && row["trigger_rule"] == "wallet_refund_frequency"
		delete(row, "review_json")
		delete(row, "release_json")
		delete(row, "trigger_rule")
		delete(row, "freeze_id")
	}
	httpapi.OK(c, out)
}

type riskRequest struct {
	RequestID   string
	UserID      uint64
	AmountCents int64
	Reason      *string
}
type riskWallet struct {
	ID, UserID, Version       uint64
	BalanceCents, FrozenCents int64
	Status                    string
}

func validComment(s string) bool {
	if !validText(s, 255) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (a ResourceAPI) reviewWalletRisk(c *gin.Context) {
	p := c.MustGet("admin_profile").(Profile)
	if p.Role != "customer_finance" && p.Role != "customer_cs" {
		httpapi.Write(c, 403, 1003, "须由财务或客服风控审核人员操作", nil)
		return
	}
	var in struct {
		Approved *bool  `json:"approved"`
		Comment  string `json:"comment"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if in.Approved == nil || !validComment(in.Comment) {
		httpapi.BadRequest(c, "请填写审核结论及有效依据")
		return
	}
	var response map[string]any
	var auditPending []auditEntry
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var req riskRequest
		if err := tx.Table("wallet_refund_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id=?", c.Param("request_id")).Take(&req).Error; err != nil {
			return err
		}
		var prior struct {
			ActorID      uint64
			Approved     bool
			Comment      string
			ResponseJSON string
		}
		err := tx.Table("wallet_risk_review").Where("request_id=?", req.RequestID).Take(&prior).Error
		if err == nil {
			if prior.ActorID != p.ID || prior.Approved != *in.Approved || prior.Comment != in.Comment {
				return errConflict
			}
			return json.Unmarshal([]byte(prior.ResponseJSON), &response)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var linked int64
		if err := tx.Table("wallet_risk_freeze_link l").Joins("JOIN risk_freeze_log f ON f.id=l.freeze_id").Where("l.request_id=? AND f.user_id=? AND f.trigger_rule='wallet_refund_frequency'", req.RequestID, req.UserID).Count(&linked).Error; err != nil {
			return err
		}
		if linked != 1 {
			return errConflict
		}
		if *in.Approved {
			if err := reserveWalletRefund(tx, req); err != nil {
				return err
			}
		}
		response = map[string]any{"actor_id": strconv.FormatUint(p.ID, 10), "approved": *in.Approved, "comment": in.Comment}
		b, _ := json.Marshal(response)
		if err := tx.Table("wallet_risk_review").Create(map[string]any{"request_id": req.RequestID, "actor_id": p.ID, "approved": *in.Approved, "comment": in.Comment, "response_json": string(b)}).Error; err != nil {
			return err
		}
		auditPending = []auditEntry{{"review", "wallet_risk", req.UserID, nil, gin.H{"request_id": req.RequestID, "review": response}, req.RequestID}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, response)
}
func reserveWalletRefund(tx *gorm.DB, req riskRequest) error {
	var w riskWallet
	if err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND deleted_at IS NULL", req.UserID).Take(&w).Error; err != nil {
		return err
	}
	if req.AmountCents <= 0 || w.BalanceCents < w.FrozenCents || req.AmountCents > w.BalanceCents-w.FrozenCents {
		return errConflict
	}
	var existing int64
	if err := tx.Table("wallet_refund_part").Where("request_id=?", req.RequestID).Count(&existing).Error; err != nil {
		return err
	}
	if existing != 0 {
		return errConflict
	}
	var payments []charge.PaymentOrderRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND biz_type='wallet_recharge' AND pay_method='wechat' AND status IN ('paid','partial_refunded') AND deleted_at IS NULL", req.UserID).Order("id").Find(&payments).Error; err != nil {
		return err
	}
	left := req.AmountCents
	now := time.Now().UTC()
	for _, pay := range payments {
		if !pay.WechatTransactionID.Valid || pay.PaidCents != pay.TotalCents {
			continue
		}
		var reserved int64
		if err := tx.Table("refund_record").Select("COALESCE(SUM(refund_cents),0)").Where("payment_order_id=? AND status IN ('pending','processing') AND deleted_at IS NULL", pay.ID).Scan(&reserved).Error; err != nil {
			return err
		}
		amount := min(left, pay.PaidCents-pay.RefundedCents-reserved)
		if amount <= 0 {
			continue
		}
		sum := sha256.Sum256([]byte(req.RequestID + ":" + strconv.FormatUint(pay.ID, 10)))
		r := charge.RefundRecord{RefundNo: "WR" + hex.EncodeToString(sum[:16]), PaymentOrderID: pay.ID, UserID: req.UserID, BizType: "wallet_recharge", BizID: pay.BizID, RefundCents: amount, Reason: sql.NullString{String: "wallet risk approved", Valid: true}, Status: "pending", ExecutionPolicy: "automatic", NextAttemptAt: now, CreatedMonth: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}
		if err := tx.Create(&r).Error; err != nil {
			return err
		}
		if err := tx.Table("wallet_refund_part").Create(map[string]any{"refund_record_id": r.ID, "request_id": req.RequestID, "wallet_account_id": w.ID, "amount_cents": amount, "settled": false}).Error; err != nil {
			return err
		}
		left -= amount
		if left == 0 {
			break
		}
	}
	if left != 0 {
		return errConflict
	}
	if err := tx.Table("wallet_account").Where("id=?", w.ID).Updates(map[string]any{"frozen_cents": gorm.Expr("frozen_cents+?", req.AmountCents), "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	return nil
}
func (a ResourceAPI) releaseWalletRisk(c *gin.Context) {
	p, ok := a.financeActor(c, "finance.wallet_risk.release")
	if !ok {
		return
	}
	var in struct {
		Comment string `json:"comment"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validComment(in.Comment) {
		httpapi.BadRequest(c, "请填写有效解冻依据")
		return
	}
	var response map[string]any
	var auditPending []auditEntry
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var req riskRequest
		if err := tx.Table("wallet_refund_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id=?", c.Param("request_id")).Take(&req).Error; err != nil {
			return err
		}
		var prior struct {
			ActorID               uint64
			Comment, ResponseJSON string
		}
		err := tx.Table("wallet_risk_release").Where("request_id=?", req.RequestID).Take(&prior).Error
		if err == nil {
			if prior.ActorID != p.ID || prior.Comment != in.Comment {
				return errConflict
			}
			return json.Unmarshal([]byte(prior.ResponseJSON), &response)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var count int64
		if err := tx.Table("wallet_risk_review").Where("request_id=?", req.RequestID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return errConflict
		}
		var w riskWallet
		if err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND deleted_at IS NULL", req.UserID).Take(&w).Error; err != nil {
			return err
		}
		var freeze struct {
			ID                  uint64
			UserID              uint64
			Status, TriggerRule string
		}
		if err := tx.Table("risk_freeze_log f").Select("f.id,f.user_id,f.status,f.trigger_rule").Joins("JOIN wallet_risk_freeze_link l ON l.freeze_id=f.id").Where("l.request_id=?", req.RequestID).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&freeze).Error; err != nil {
			return err
		}
		if freeze.UserID != req.UserID || freeze.TriggerRule != "wallet_refund_frequency" || freeze.Status != "frozen" {
			return errConflict
		}
		if err := tx.Table("risk_freeze_log").Where("id=?", freeze.ID).Updates(map[string]any{"status": "unfrozen", "unfreeze_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		if err := tx.Table("risk_freeze_log").Where("user_id=? AND status='frozen'", req.UserID).Count(&count).Error; err != nil {
			return err
		}
		var user struct{ Status string }
		if err := tx.Table("user").Where("id=? AND deleted_at IS NULL", req.UserID).Take(&user).Error; err != nil {
			return err
		}
		active := count == 0 && user.Status == "active"
		if active {
			if err := tx.Table("wallet_account").Where("id=?", w.ID).Updates(map[string]any{"status": "active", "version": gorm.Expr("version+1")}).Error; err != nil {
				return err
			}
		}
		response = map[string]any{"actor_id": strconv.FormatUint(p.ID, 10), "comment": in.Comment, "wallet_active": active}
		b, _ := json.Marshal(response)
		if err := tx.Table("wallet_risk_release").Create(map[string]any{"request_id": req.RequestID, "actor_id": p.ID, "comment": in.Comment, "response_json": string(b)}).Error; err != nil {
			return err
		}
		auditPending = []auditEntry{{"release", "wallet_risk", req.UserID, nil, gin.H{"request_id": req.RequestID, "release": response}, req.RequestID}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, response)
}
