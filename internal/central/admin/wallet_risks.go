package admin

import (
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

// registerWalletRisks 注册风控列表、审核和解冻路由。
// 审核要求 finance.wallet_risk.review，允许客服与财务；解冻要求 finance.wallet_risk.release，仅允许财务。
func (a ResourceAPI) registerWalletRisks(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/wallet-risks", a.Auth.Require("finance.wallet_risk.review"), a.walletRisks)
	r.POST("/api/v1/admin/billing/wallet-risks/:request_id/review", a.Auth.Require("finance.wallet_risk.review"), a.reviewWalletRisk)
	r.POST("/api/v1/admin/billing/wallet-risks/:request_id/release", a.Auth.Require("finance.wallet_risk.release"), a.releaseWalletRisk)
}

// walletRisks 分页查询关联冻结记录的退款风控请求。
// pending、reviewed 分别表示未审核和已审核；已审核记录可按解冻状态过滤。
// can_review、can_release 按权限及审核、冻结状态计算，解冻还要求 wallet_refund_frequency 规则。
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

// riskRequest 是钱包退款风控审核所需的最小字段投影。
type riskRequest struct {
	RequestID   string  // 请求号，业务主键，审核、解冻、幂等都按它定位
	UserID      uint64  // 申请退款的用户 ID，冻结与退款记录都挂在该用户上
	AmountCents int64   // 申请退款金额，单位分
	Reason      *string // 申请原因，可空
}

// riskWallet 是 wallet_account 一行的最小投影，用于校验可用余额与冻结额、比对乐观锁版本。
type riskWallet struct {
	ID, UserID, Version       uint64
	BalanceCents, FrozenCents int64  // 余额与已冻结金额（均为分）；可退额度 = 余额 - 冻结额
	Status                    string // 钱包状态，如 active / frozen
}

// validComment 校验风控留言：先按 validText 限长 1–255 字，再拒绝控制字符。
// 控制字符会污染工单展示和后续导出，因此即使长度合法也判为无效。
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

// reviewWalletRisk 要求财务或客服角色，并验证请求恰好关联一条 wallet_refund_frequency 冻结记录。
// 通过时按原渠道拆分充值退款并冻结等额余额；拒绝时仅保存审核结论。
// 相同请求、审核人、结论和留言返回首次回执；内容不一致时返回冲突。
func (a ResourceAPI) reviewWalletRisk(c *gin.Context) {
	p := c.MustGet("admin_profile").(Profile)
	if p.Role != "customer_finance" && p.Role != "customer_cs" {
		httpapi.Write(c, 403, 1003, "须由财务或客服风控审核人员操作", nil)
		return
	}
	var in struct {
		Approved *bool  `json:"approved"` // 审核结论，true 通过 / false 驳回；必须显式给出，指针为 nil 视为未填
		Comment  string `json:"comment"`  // 审核依据，1–255 字且不含控制字符
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
	err := a.auditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		var req riskRequest
		if err := tx.Table("wallet_refund_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id=?", c.Param("request_id")).Take(&req).Error; err != nil {
			return err
		}
		var prior struct {
			ActorID      uint64 // 首次审核人 ID，重放时必须仍是同一个人
			Approved     bool   // 首次审核结论
			Comment      string // 首次审核依据
			ResponseJSON string // 首次审核的返回体，重放时原样回给调用方
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
	httpapi.OK(c, response)
}

// reserveWalletRefund 按申请金额拆分微信充值支付单，创建 automatic/pending 退款并冻结钱包余额。
// 仅使用全额实付且有微信交易号的充值，扣除已有退款占用与已退金额；资金不足时整笔事务回滚。
func reserveWalletRefund(tx *gorm.DB, req riskRequest) error {
	err := charge.ReserveWalletRefund(tx, charge.WalletRefundReservation{RequestID: req.RequestID, UserID: req.UserID, AmountCents: req.AmountCents, Reason: "wallet risk approved"})
	if errors.Is(err, charge.ErrRefundConflict) {
		return errConflict
	}
	return err
}

// releaseWalletRisk 由客户财务解除已审核、同用户且仍生效的 wallet_refund_frequency 冻结。
// 解冻后仅在用户 active 且无其他冻结时恢复钱包为 active。
// 相同审核人和留言重放返回首次回执；内容不一致时返回冲突。
func (a ResourceAPI) releaseWalletRisk(c *gin.Context) {
	p, ok := a.financeActor(c, "finance.wallet_risk.release")
	if !ok {
		return
	}
	var in struct {
		Comment string `json:"comment"` // 解冻依据，1–255 字且不含控制字符
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
	err := a.auditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		var req riskRequest
		if err := tx.Table("wallet_refund_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id=?", c.Param("request_id")).Take(&req).Error; err != nil {
			return err
		}
		var prior struct {
			ActorID               uint64 // 首次解冻人 ID
			Comment, ResponseJSON string // 首次解冻依据与返回体，重放时原样返回
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
			ID                  uint64 // 冻结记录主键，解冻时按它更新 risk_freeze_log
			UserID              uint64 // 被冻结用户，必须与风控请求的申请人对得上
			Status, TriggerRule string // 冻结状态（须为 frozen）与触发规则（须为 wallet_refund_frequency）
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
	httpapi.OK(c, response)
}
