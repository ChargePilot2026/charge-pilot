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

// registerWalletRisks 挂载钱包退款风控的后台路由：风控工单列表、审核、解除冻结。
// 审核（finance.wallet_risk.review）允许客服和财务两个角色，解除冻结（finance.wallet_risk.release）只给财务，
// 因为解冻会直接恢复用户的钱包可用状态，风险高于出一张复核单。
func (a ResourceAPI) registerWalletRisks(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/wallet-risks", a.Auth.Require("finance.wallet_risk.review"), a.walletRisks)
	r.POST("/api/v1/admin/billing/wallet-risks/:request_id/review", a.Auth.Require("finance.wallet_risk.review"), a.reviewWalletRisk)
	r.POST("/api/v1/admin/billing/wallet-risks/:request_id/release", a.Auth.Require("finance.wallet_risk.release"), a.releaseWalletRisk)
}

// walletRisks 分页返回钱包退款风控工单，只统计已经关联到冻结记录（wallet_risk_freeze_link）的请求，
// 因为没有冻结的请求并不构成需要人工处理的风险。
// pending 视图是"还没审过"的请求，reviewed 视图是"已审过"的请求，reviewed 下再按是否已解冻区分。
// 每行附带 can_review、can_release 两个布尔位供前端控制按钮：解冻还要求审核已完成、尚未解冻、冻结仍生效，且触发规则确为钱包退款频次。
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

// riskRequest 是风控侧对 wallet_refund_request 一行所需字段的最小投影，只查审核流程真正用得到的列。
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

// reviewWalletRisk 审核一笔钱包退款风控请求。审核人必须是财务或客服角色（写入前再判一次，不能只依赖路由权限）。
// 通过时先按原路把申请金额拆成若干笔微信充值退款并冻结等额钱包余额；不通过则只留审核结论，不动钱。
// 幂等靠 wallet_risk_review：同一请求号重放时，若审核人、结论、留言都一致就返回首次的 response，否则判冲突。
// 另外要求该请求恰好关联一条触发规则为 wallet_refund_frequency 的冻结记录，否则拒绝——风控单必须能追溯到它冻结的原因。
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
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
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
	a.flushAudit(c, auditPending)
	httpapi.OK(c, response)
}

// reserveWalletRefund 按申请金额为风控通过的请求预留退款额：把用户的微信充值支付单按 id 顺序逐笔拆成退款记录，
// 每笔写成 automatic 策略的 pending 退款（执行器会自己跑），并冻结等额的钱包余额，防止这笔钱在到账前被再次花掉。
// 只能挑全额实付、且有微信交易号的充值单；每笔还要扣掉已有 pending/processing 退款和已退金额。
// 任一环节凑不齐申请金额就整笔回滚（errConflict），不留下半截退款。
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

// releaseWalletRisk 解除一笔风控请求带来的冻结，由客户财务执行。
// 前置条件：该请求已审过、冻结仍为 frozen、触发规则确为 wallet_refund_frequency、且冻结记录属于同一用户。
// 解冻后重算该用户是否还剩别的冻结、账号是否仍 active；都不占用才把钱包置回 active，否则保持冻结。
// 幂等靠 wallet_risk_release：同人同留言重放返回首次 response，不同内容判冲突。
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
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
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
	a.flushAudit(c, auditPending)
	httpapi.OK(c, response)
}
