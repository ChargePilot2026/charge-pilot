package admin

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registerRefunds 挂载退款模块的全部后台路由：人工发起退款、退款单列表、双人复核（通过/驳回）、自动退款重试。
// 四类动作各用一套权限：发起（order.refund.create）、只读（finance.refund.read）、
// 复核（order.refund.review）、重投（finance.refund.retry）——重投等于把资金动作放出去，权限高于复核。
func (a ResourceAPI) registerRefunds(r *gin.Engine) {
	r.POST("/api/v1/admin/orders/:id/refunds", a.Auth.Require("order.refund.create"), a.manualRefund)
	r.GET("/api/v1/admin/billing/refunds", a.Auth.Require("finance.refund.read"), a.refunds)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/approve", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/reject", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/retry", a.Auth.Require("finance.refund.retry"), a.retryRefund)
}

// manualRefund 由客服/运营对指定充电订单发起人工退款：校验请求体后在同一事务里锁住支付单，
// 确认订单与支付单可退、累计退款不超额，再插入一条待复核的退款记录。
// 幂等靠 request_id：同号同内容重放返回原退款单号，不同内容则判冲突，避免重复出款。
// 生成的单据走 manual_review 策略，必须等双人复核通过后才会真正执行退款。
func (a ResourceAPI) manualRefund(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID   string `json:"request_id"`   // 客户端生成的幂等请求号，格式为 UUID；重放同一请求号不会重复建单
		AmountCents int64  `json:"amount_cents"` // 本次退款金额，单位分，必须为正整数
		Reason      string `json:"reason"`       // 退款原因，必填，最多 255 字，用于工单与审计追溯
	}
	if !decodeResource(c, &in) {
		return
	}
	if !requestIDPattern.MatchString(in.RequestID) || in.AmountCents <= 0 || !validText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写有效请求编号、正整数退款分值和退款原因")
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	payload, _ := json.Marshal(gin.H{"actor_id": p.ID, "order_id": id, "amount_cents": in.AmountCents, "reason": in.Reason})
	sum := sha256.Sum256([]byte(in.RequestID))
	refundNo := "MR" + hex.EncodeToString(sum[:16])
	created := false
	var auditPending []auditEntry
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var order charge.ChargeOrderRecord
		if err := tx.Where("id=? AND deleted_at IS NULL", id).Take(&order).Error; err != nil {
			return err
		}
		var pay charge.PaymentOrderRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", order.PaymentOrderID).Take(&pay).Error; err != nil {
			return err
		}
		var receipt struct {
			PayloadJSON string // 同一 request_id 首次提交时的请求体快照，重放时用于逐字段比对内容是否一致
			RefundNo    string // 首次生成的退款单号，重放时直接返回它，保证同号始终对应同一笔退款
		}
		e := tx.Table("manual_refund_request").Where("request_id=?", in.RequestID).Take(&receipt).Error
		if e == nil {
			var x, y any
			_ = json.Unmarshal([]byte(receipt.PayloadJSON), &x)
			_ = json.Unmarshal(payload, &y)
			xb, _ := json.Marshal(x)
			yb, _ := json.Marshal(y)
			if string(xb) != string(yb) {
				return errConflict
			}
			refundNo = receipt.RefundNo
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&order).Error; err != nil {
			return err
		}
		if !oneOf(order.Status, "completed cancelled failed refunded") || order.Status == "refunded" || pay.UserID != order.UserID || pay.PayMethod != "wechat" || !oneOf(pay.Status, "paid partial_refunded") || in.AmountCents > pay.PaidCents-pay.RefundedCents {
			return errConflict
		}
		var pending int64
		if err := tx.Table("refund_record").Select("COALESCE(SUM(refund_cents),0)").Where("payment_order_id=? AND status IN ('pending','processing') AND deleted_at IS NULL", pay.ID).Scan(&pending).Error; err != nil {
			return err
		}
		if pending > pay.PaidCents-pay.RefundedCents-in.AmountCents {
			return errConflict
		}
		now := time.Now().UTC()
		record := charge.RefundRecord{RefundNo: refundNo, PaymentOrderID: pay.ID, UserID: pay.UserID, BizType: "charge", BizID: id, RefundCents: in.AmountCents, Reason: sql.NullString{String: in.Reason, Valid: true}, Status: "pending", ExecutionPolicy: "manual_review", NextAttemptAt: now, CreatedMonth: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		if err := tx.Table("manual_refund_request").Create(map[string]any{"request_id": in.RequestID, "payload_json": string(payload), "refund_no": refundNo}).Error; err != nil {
			return err
		}
		created = true
		auditPending = []auditEntry{{"request", "refund", record.ID, nil, in, in.RequestID}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, gin.H{"created": created, "refund_no": refundNo, "request_id": in.RequestID})
}

// refundReview 是人工退款的双人复核记录（user_db.refund_review 表），一条退款单至多一条。
// 复核采用"两人两签"：第一审核人只能把单子推进到 awaiting_second，第二位不同的审核人签字后才转 approved 并放行执行；
// snapshot_json 冻结第一签时的退款要素，第二签必须对得上，避免两次签之间金额或对象被改动。
type refundReview struct {
	RefundRecordID uint64  // 关联的退款记录主键 refund_record.id，一对一
	SnapshotJSON   string  // 第一签时冻结的退款要素 JSON，第二签据此比对，内容不一致即冲突
	FirstSigner    uint64  // 第一审核人 ID
	FirstComment   string  // 第一审核人的审核意见
	SecondSigner   *uint64 // 第二审核人 ID，nil 表示第二签还没做
	SecondComment  *string // 第二审核人的审核意见，nil 表示第二签还没做
}

// refunds 分页返回退款单列表，附带每单当前登录人能否审批/驳回/重试的布尔位（can_approve、can_reject、can_retry）。
// 只有人工复核（execution_policy=manual_review）且处于 pending 的单子才进入复核流程，
// 并且第一审核人不能审自己签过的单子（can_approve 会因其已签字而为 false）；自动策略的单子则把执行进度放进 task 字段。
func (a ResourceAPI) refunds(c *gin.Context) {
	q, ok := parsePage(c, "pending processing success failed rejected")
	if !ok {
		return
	}
	if len(c.Query("refund_no")) > 64 {
		httpapi.BadRequest(c, "退款单号过长")
		return
	}
	db := a.Store.UserDB.WithContext(c.Request.Context()).Table("refund_record r").Joins("LEFT JOIN refund_review v ON v.refund_record_id=r.id").Where("r.deleted_at IS NULL")
	if q.Status != "" {
		db = db.Where("r.status=?", q.Status)
	}
	if c.Query("refund_no") != "" {
		db = db.Where("r.refund_no=?", c.Query("refund_no"))
	}
	out := Page[map[string]any]{Items: []map[string]any{}, Page: q.Page, PageSize: q.PageSize}
	if err := db.Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := db.Select("r.id,r.refund_no,r.user_id,r.payment_order_id,r.refund_cents,r.status,r.reason,r.failure_reason,r.created_at,r.completed_at,r.execution_policy,r.retry_count,r.next_attempt_at,v.first_signer,v.second_signer,v.first_comment,v.second_comment").Order("r.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(out.Items)
	stringIDs(out.Items, "id", "user_id", "payment_order_id", "first_signer", "second_signer")
	p := c.MustGet("admin_profile").(Profile)
	for _, row := range out.Items {
		status := row["status"]
		manual := row["execution_policy"] == "manual_review"
		reviewable := status == "pending" && manual && p.Role == "customer_finance" && hasPermission(p, "order.refund.review")
		row["can_approve"] = reviewable && row["first_signer"] != strconv.FormatUint(p.ID, 10)
		row["can_reject"] = reviewable
		row["can_retry"] = row["execution_policy"] == "automatic" && (status == "pending" || status == "processing") && hasPermission(p, "finance.refund.retry")
		row["task"] = nil
		if !manual {
			row["task"] = gin.H{"stage": status, "attempts": row["retry_count"], "last_error": row["failure_reason"], "scheduled_at": row["next_attempt_at"]}
		}
		row["review"] = nil
		if status == "rejected" {
			row["review"] = gin.H{"status": "rejected", "first_signer": "", "second_signer": nil, "first_comment": "", "second_comment": nil}
		}
		if row["first_signer"] != nil {
			state := "awaiting_second"
			if row["second_signer"] != nil {
				state = "approved"
			}
			if status == "rejected" {
				state = "rejected"
			}
			row["review"] = gin.H{"status": state, "first_signer": row["first_signer"], "second_signer": row["second_signer"], "first_comment": row["first_comment"], "second_comment": row["second_comment"]}
		}
		for _, key := range []string{"first_signer", "second_signer", "first_comment", "second_comment", "execution_policy", "retry_count", "next_attempt_at"} {
			delete(row, key)
		}
	}
	httpapi.OK(c, out)
}

// refundSnapshot 把退款记录的六个关键要素序列化成规范化 JSON，作为双人复核的冻结快照。
// 刻意不包含状态、重试次数和时间戳：这些字段在复核过程中本来就会变，纳入比对会让第二签永远对不上。
func refundSnapshot(r charge.RefundRecord) string {
	b, _ := json.Marshal(gin.H{"refund_no": r.RefundNo, "payment_order_id": r.PaymentOrderID, "user_id": r.UserID, "biz_type": r.BizType, "biz_id": r.BizID, "refund_cents": r.RefundCents})
	return string(b)
}

// reviewRefund 人工退款的复核入口，approve 与 reject 两条路由共用此处理函数，靠路由后缀区分动作。
// 通过：第一次调用写入 refund_review 进入 awaiting_second；第二次由不同的人调用才转 approved，
// 同时把 execution_policy 改为 automatic 并把 next_attempt_at 置为当前，从而被退款执行器接走。
// 驳回：单人即可，直接写 refund_rejection 并把退款单置为 rejected，终结本次退款。
// 两种动作都只在 pending + manual_review 的单子上有效，其余状态一律冲突。
func (a ResourceAPI) reviewRefund(c *gin.Context) {
	p, ok := a.financeActor(c, "order.refund.review")
	if !ok {
		return
	}
	var in struct {
		ApproveComment string `json:"approve_comment"` // 通过时必填的审核意见，1–255 字
		Reason         string `json:"reason"`          // 驳回时必填的驳回理由，1–255 字
	}
	if !decodeResource(c, &in) {
		return
	}
	approve := strings.HasSuffix(c.FullPath(), "/approve")
	if approve && !validText(in.ApproveComment, 255) || !approve && !validText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写 1–255 字审核意见")
		return
	}
	status := "rejected"
	var auditPending []auditEntry
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var r charge.RefundRecord
		if err := tx.Where("refund_no=? AND deleted_at IS NULL", c.Param("refund_no")).Take(&r).Error; err != nil {
			return err
		}
		var pay charge.PaymentOrderRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", r.PaymentOrderID).Take(&pay).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", r.ID).Take(&r).Error; err != nil {
			return err
		}
		if r.Status != "pending" || r.ExecutionPolicy != "manual_review" {
			return errConflict
		}
		var v refundReview
		e := tx.Table("refund_review").Where("refund_record_id=?", r.ID).Take(&v).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if !approve {
			if err := tx.Table("refund_rejection").Create(map[string]any{"refund_record_id": r.ID, "actor_id": p.ID, "reason": in.Reason}).Error; err != nil {
				return err
			}
			if err := tx.Table("refund_record").Where("id=?", r.ID).Updates(map[string]any{"status": "rejected", "failure_reason": in.Reason}).Error; err != nil {
				return err
			}
		} else {
			if pay.PayMethod != "wechat" || pay.UserID != r.UserID || pay.BizID != r.BizID || pay.BizType != r.BizType || r.RefundCents <= 0 || r.RefundCents > pay.PaidCents-pay.RefundedCents {
				return errConflict
			}
			snapshot := refundSnapshot(r)
			if errors.Is(e, gorm.ErrRecordNotFound) {
				status = "awaiting_second"
				if err := tx.Table("refund_review").Create(map[string]any{"refund_record_id": r.ID, "snapshot_json": snapshot, "first_signer": p.ID, "first_comment": in.ApproveComment}).Error; err != nil {
					return err
				}
			} else {
				var stored any
				_ = json.Unmarshal([]byte(v.SnapshotJSON), &stored)
				b, _ := json.Marshal(stored)
				if v.FirstSigner == p.ID || v.SecondSigner != nil || string(b) != snapshot {
					return errConflict
				}
				first, err := a.Auth.Store.Profile(c.Request.Context(), v.FirstSigner)
				if err != nil || first.Role != "customer_finance" || !hasPermission(first, "order.refund.review") {
					return errConflict
				}
				status = "approved"
				if err := tx.Table("refund_review").Where("refund_record_id=?", r.ID).Updates(map[string]any{"second_signer": p.ID, "second_comment": in.ApproveComment, "approved_at": time.Now().UTC()}).Error; err != nil {
					return err
				}
				if err := tx.Table("refund_record").Where("id=?", r.ID).Updates(map[string]any{"execution_policy": "automatic", "next_attempt_at": time.Now().UTC()}).Error; err != nil {
					return err
				}
			}
		}
		auditPending = []auditEntry{{status, "refund", r.ID, nil, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, gin.H{"review_status": status})
}

// retryRefund 手动重投一笔自动执行失败的退款：只把 next_attempt_at 拨到当前时间让执行器再跑一次，
// 不改任何金额或状态。仅限 execution_policy 为 automatic 且仍处于 pending/processing 的单子，
// 人工复核中的单子不能走这条路，否则等于绕过双人复核直接出款。
func (a ResourceAPI) retryRefund(c *gin.Context) {
	var in struct {
		Reason string `json:"reason"` // 重投原因，必填，1–255 字，落审计用
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写重试原因")
		return
	}
	var auditPending []auditEntry
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var r charge.RefundRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("refund_no=? AND deleted_at IS NULL", c.Param("refund_no")).Take(&r).Error; err != nil {
			return err
		}
		if r.ExecutionPolicy != "automatic" || (r.Status != "pending" && r.Status != "processing") {
			return errConflict
		}
		if err := tx.Table("refund_record").Where("id=?", r.ID).Update("next_attempt_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		auditPending = []auditEntry{{"retry", "refund", r.ID, nil, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, gin.H{"scheduled": true})
}
