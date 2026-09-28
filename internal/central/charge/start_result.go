package charge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

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
	if s.DB == nil || result.CommandID == "" || result.ChargeOrderID == 0 || result.OrderNo == "" || result.DeviceID == "" || result.PortNo == 0 || result.PortID == 0 || result.PortID > math.MaxInt64 || result.OccurredAt.IsZero() || result.Success != (result.ResultCode == 0) {
		return false, ErrStartResultConflict
	}
	result.OccurredAt = result.OccurredAt.UTC().Truncate(time.Millisecond)
	replayed := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
			if isMySQLDuplicate(err) {
				return ErrStartResultConflict
			}
			return err
		}
		stream, eventName := "charge_started_stream", "start_acked"
		if result.Success {
			if err := tx.Create(&ActivePortChargeRecord{PortID: result.PortID, DeviceID: result.DeviceID, PortNo: result.PortNo,
				ChargeOrderID: result.ChargeOrderID, UserID: order.UserID, StartedAt: result.OccurredAt.UTC()}).Error; err != nil {
				if isMySQLDuplicate(err) {
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
			month := utcDate()
			refund := RefundRecord{ExecutionPolicy: "automatic", RefundNo: refundNo, PaymentOrderID: payment.ID, UserID: payment.UserID,
				BizType: "charge", BizID: result.ChargeOrderID, RefundCents: refundCents,
				Reason: sql.NullString{String: failureReason, Valid: true}, Status: "pending", CreatedMonth: month}
			if err := tx.Create(&refund).Error; err != nil {
				return err
			}
			refundEnvelope, err := json.Marshal(map[string]any{
				"event_id": refundEventID, "refund_no": refundNo, "payment_order_id": payment.ID,
				"charge_order_id": result.ChargeOrderID, "order_no": result.OrderNo, "amount_cents": refundCents,
			})
			if err != nil {
				return err
			}
			if err := tx.Create(&EventOutboxRecord{EventID: refundEventID, Stream: "refund_required_stream", EnvelopeJSON: refundEnvelope}).Error; err != nil {
				return err
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
		if err := tx.Create(&EventOutboxRecord{EventID: result.CommandID, Stream: stream, EnvelopeJSON: envelope}).Error; err != nil {
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

func utcDate() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
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
