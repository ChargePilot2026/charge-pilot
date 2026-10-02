package order

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/central/eventoutbox"
	"github.com/ChargePilot2026/charge-pilot/internal/central/wallet"
	"math"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrStartResultConflict = errors.New("charge start result conflicts with order state")

type StartResult struct {
	CommandID     string    `json:"command_id" binding:"required,uuid"`
	ChargeOrderID uint64    `json:"charge_order_id" binding:"required"`
	OrderNo       string    `json:"order_no" binding:"required,max=64"`
	DeviceID      string    `json:"device_id" binding:"required,max=64"`
	PortNo        uint8     `json:"port_no" binding:"required"`
	PortID        uint64    `json:"port_id" binding:"required"`
	Success       bool      `json:"success"`
	ResultCode    uint8     `json:"result_code"`
	OccurredAt    time.Time `json:"occurred_at" binding:"required"`
}

type StartResultStore struct{ DB *gorm.DB }

func (s StartResultStore) Apply(ctx context.Context, result StartResult) (bool, error) {
	if s.DB == nil || result.CommandID == "" || result.ChargeOrderID == 0 || result.OrderNo == "" || result.DeviceID == "" || result.PortNo == 0 || result.PortID == 0 || result.PortID > math.MaxInt64 || result.OccurredAt.IsZero() || result.Success != (result.ResultCode == 0) || result.ResultCode > 3 {
		return false, ErrStartResultConflict
	}
	result.OccurredAt = result.OccurredAt.UTC().Truncate(time.Millisecond)
	replayed := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var walletRow wallet.Row
		var walletPayment struct {
			PayMethod string
			UserID    uint64
		}
		if err := tx.Table("payment_order p").Select("p.pay_method,p.user_id").Joins("JOIN charge_order c ON c.payment_order_id=p.id").Where("c.id=?", result.ChargeOrderID).Take(&walletPayment).Error; err != nil {
			return err
		}
		if walletPayment.PayMethod == "balance" {
			var err error
			walletRow, err = wallet.Lock(tx, walletPayment.UserID)
			if err != nil {
				return err
			}
		}
		// Match the card transaction lock order: session before order.
		// 跨家族锁：card_charge 归属 card 家族，仅取行锁参与加锁顺序，不读字段。
		var cardLocked struct{ ID uint64 }
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("card_charge").Where("charge_order_id=?", result.ChargeOrderID).Find(&cardLocked).Error; err != nil {
			return err
		}
		var order ChargeOrderRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND order_no = ? AND deleted_at IS NULL", result.ChargeOrderID, result.OrderNo).Take(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrStartResultConflict
		}
		if err != nil {
			return err
		}
		if order.DeviceID != result.DeviceID || order.PortNo != result.PortNo {
			return ErrStartResultConflict
		}
		var existing StartReceiptRecord
		err = tx.Where("command_id = ?", result.CommandID).Take(&existing).Error
		if err == nil {
			if existing.ChargeOrderID != result.ChargeOrderID || existing.OrderNo.String != result.OrderNo || existing.DeviceID.String != result.DeviceID || existing.PortNo.Int16 != int16(result.PortNo) || existing.PortID.Int64 != int64(result.PortID) || existing.Success != result.Success || existing.ResultCode.Int16 != int16(result.ResultCode) || !existing.OccurredAt.Time.Equal(result.OccurredAt.UTC()) {
				return ErrStartResultConflict
			}
			replayed = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if order.Status != "paid" {
			return ErrStartResultConflict
		}
		receipt := StartReceiptRecord{CommandID: result.CommandID, ChargeOrderID: result.ChargeOrderID,
			OrderNo: sql.NullString{String: result.OrderNo, Valid: true}, DeviceID: sql.NullString{String: result.DeviceID, Valid: true},
			PortNo: sql.NullInt16{Int16: int16(result.PortNo), Valid: true}, PortID: sql.NullInt64{Int64: int64(result.PortID), Valid: true},
			Success: result.Success, ResultCode: sql.NullInt16{Int16: int16(result.ResultCode), Valid: true}, OccurredAt: sql.NullTime{Time: result.OccurredAt.UTC(), Valid: true}}
		if err := tx.Create(&receipt).Error; err != nil {
			if dbutil.IsMySQLDuplicate(err) {
				return ErrStartResultConflict
			}
			return err
		}
		stream, eventName := "charge_started_stream", "start_acked"
		if result.Success {
			if err := tx.Table("card_operation").Where("charge_order_id=? AND kind='start' AND status='confirming'", result.ChargeOrderID).Updates(map[string]any{"status": "confirmed", "confirmed_at": result.OccurredAt}).Error; err != nil {
				return err
			}
			if err := tx.Create(&ActivePortChargeRecord{PortID: result.PortID, DeviceID: result.DeviceID, PortNo: result.PortNo,
				ChargeOrderID: result.ChargeOrderID, UserID: order.UserID, StartedAt: result.OccurredAt.UTC()}).Error; err != nil {
				if dbutil.IsMySQLDuplicate(err) {
					return ErrStartResultConflict
				}
				return err
			}
			updated := tx.Model(&ChargeOrderRecord{}).Where("id = ? AND order_no = ? AND status = 'paid' AND deleted_at IS NULL", result.ChargeOrderID, result.OrderNo).
				Updates(map[string]any{"status": "charging", "started_at": result.OccurredAt.UTC()})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrStartResultConflict
			}
		} else {
			stream, eventName = "charge_start_rejected_stream", "start_rejected"
			failureReason := fmt.Sprintf("device START rejected: %d", result.ResultCode)
			updated := tx.Model(&ChargeOrderRecord{}).Where("id = ? AND order_no = ? AND status = 'paid' AND deleted_at IS NULL", result.ChargeOrderID, result.OrderNo).
				Updates(map[string]any{"failure_reason": failureReason, "status": "refunding"})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrStartResultConflict
			}
			if walletPayment.PayMethod == "balance" {
				var payment struct {
					PaidCents     int64
					RefundedCents int64
				}
				if err := tx.Table("payment_order").Select("paid_cents,refunded_cents").Where("id=?", order.PaymentOrderID.Int64).Take(&payment).Error; err != nil {
					return err
				}
				if err := refundBalanceStartFailure(tx, order, &walletRow, payment.PaidCents-payment.RefundedCents, "CSF"+result.CommandID, failureReason); err != nil {
					return err
				}
				if err := tx.Model(&ChargeOrderRecord{}).Where("id=?", order.ID).Update("status", "refunded").Error; err != nil {
					return err
				}
				if err := tx.Table("card_operation").Where("charge_order_id=? AND kind='start'", order.ID).Updates(map[string]any{"status": "failed", "failure_reason": failureReason, "confirmed_at": result.OccurredAt}).Error; err != nil {
					return err
				}
				if err := tx.Table("card_charge").Where("charge_order_id=?", order.ID).Update("active_port", nil).Error; err != nil {
					return err
				}
			} else {
				var payment startRefundPaymentRow
				lookup := tx.Table("charge_order AS c").Select("p.id, p.user_id, p.total_cents, p.paid_cents, p.refunded_cents, p.wechat_transaction_id, p.status").
					Joins("JOIN payment_order AS p ON p.id = c.payment_order_id AND p.biz_type = 'charge' AND p.pay_method = 'wechat' AND p.biz_id = c.id AND p.user_id = c.user_id").
					Clauses(clause.Locking{Strength: "UPDATE"}).Where("c.id = ? AND p.status = 'paid' AND p.deleted_at IS NULL", result.ChargeOrderID).Take(&payment)
				if errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
					return ErrStartResultConflict
				}
				if lookup.Error != nil {
					return lookup.Error
				}
				if payment.UserID != order.UserID || !payment.WechatTransactionID.Valid || payment.WechatTransactionID.String == "" ||
					payment.TotalCents <= 0 || payment.PaidCents <= 0 || payment.RefundedCents < 0 || payment.PaidCents > payment.TotalCents-payment.RefundedCents {
					return ErrStartResultConflict
				}
				digest := sha256.Sum256([]byte("start-failure\x00" + result.CommandID))
				refundNo := "RF" + hex.EncodeToString(digest[:16])
				refundEventID := "R" + hex.EncodeToString(digest[:16])
				refundCents := payment.PaidCents - payment.RefundedCents
				if refundCents <= 0 {
					return ErrStartResultConflict
				}
				month := dbutil.MonthStart()
				// 跨家族写：refund_record 归属 refund 家族，经裸表名插入，避免 order 反向依赖 refund。
				if err := tx.Table("refund_record").Create(map[string]any{
					"execution_policy": "automatic", "refund_no": refundNo, "payment_order_id": payment.ID, "user_id": payment.UserID,
					"biz_type": "charge", "biz_id": result.ChargeOrderID, "refund_cents": refundCents,
					"reason": failureReason, "status": "pending", "created_month": month,
				}).Error; err != nil {
					return err
				}
				refundEnvelope, err := json.Marshal(map[string]any{
					"event_id": refundEventID, "refund_no": refundNo, "payment_order_id": payment.ID,
					"charge_order_id": result.ChargeOrderID, "order_no": result.OrderNo, "amount_cents": refundCents,
				})
				if err != nil {
					return err
				}
				if err := tx.Create(&eventoutbox.EventOutboxRecord{EventID: refundEventID, Stream: "refund_required_stream", EnvelopeJSON: refundEnvelope}).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Create(&ChargeEventLogRecord{ChargeOrderID: result.ChargeOrderID, EventID: result.CommandID,
			Event: eventName, Actor: "gateway", Detail: fmt.Sprintf("device=%s port=%d result=%d", result.DeviceID, result.PortNo, result.ResultCode),
			OccurredAt: result.OccurredAt.UTC()}).Error; err != nil {
			return err
		}
		envelope, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err := tx.Create(&eventoutbox.EventOutboxRecord{EventID: result.CommandID, Stream: stream, EnvelopeJSON: envelope}).Error; err != nil {
			return err
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return replayed, err
}

type startRefundPaymentRow struct {
	ID                  uint64
	UserID              uint64
	TotalCents          int64
	PaidCents           int64
	RefundedCents       int64
	WechatTransactionID sql.NullString
	Status              string
}

// refundBalanceStartFailure 复现卡族 RefundInTx 的余额退款事务体（钱包退回 +
// payment_order 改写 + 支付状态同步 + refund_record 记录）。card→order 与 refund→order
// 依赖已单向占用，order 不得反向 import 三者，跨家族行一律经裸表名访问；
// 支付行校验冲突返回 ErrStartResultConflict（与原 card.ErrCardOperation 同为服务端错误）。
func refundBalanceStartFailure(tx *gorm.DB, order ChargeOrderRecord, w *wallet.Row, cents int64, refundNo, reason string) error {
	if cents <= 0 {
		return nil
	}
	if err := wallet.Move(tx, order.UserID, w, cents, refundNo, reason); err != nil {
		return err
	}
	var payment struct {
		ID            uint64
		PaidCents     int64
		RefundedCents int64
	}
	if err := tx.Table("payment_order").Select("id,paid_cents,refunded_cents").Where("id=? AND pay_method='balance'", order.PaymentOrderID.Int64).Take(&payment).Error; err != nil {
		return err
	}
	if payment.PaidCents-payment.RefundedCents < cents {
		return ErrStartResultConflict
	}
	status := "partial_refunded"
	if payment.RefundedCents+cents == payment.PaidCents {
		status = "refunded"
	}
	if err := tx.Table("payment_order").Where("id=?", payment.ID).Updates(map[string]any{"refunded_cents": gorm.Expr("refunded_cents+?", cents), "status": status}).Error; err != nil {
		return err
	}
	if err := SyncOrderPaymentStatus(tx, payment.ID); err != nil {
		return err
	}
	return tx.Table("refund_record").Create(map[string]any{
		"refund_no": refundNo, "payment_order_id": payment.ID, "user_id": order.UserID,
		"biz_type": "charge", "biz_id": order.ID, "refund_cents": cents, "status": "success",
		"execution_policy": "automatic", "reason": reason, "created_month": dbutil.MonthStart(),
	}).Error
}

type StartResultAPI struct {
	Store        StartResultStore
	ServiceToken string
}

func (a StartResultAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/charge-orders/:order_no/start-result", a.handle)
}

func (a StartResultAPI) handle(c *gin.Context) {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return
	}
	var request StartResult
	if err := c.ShouldBindJSON(&request); err != nil || c.Param("order_no") != request.OrderNo {
		httpapi.BadRequest(c, "invalid start result")
		return
	}
	replayed, err := a.Store.Apply(c.Request.Context(), request)
	if errors.Is(err, ErrStartResultConflict) {
		httpapi.Write(c, http.StatusConflict, 2000, "start result conflicts with order", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "start result persistence failed", nil)
		return
	}
	httpapi.OK(c, gin.H{"accepted": true, "replayed": replayed})
}
