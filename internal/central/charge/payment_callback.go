package charge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrPaymentCallbackConflict = errors.New("verified payment does not match intent")

// VerifiedPayment must only be constructed after the configured payment SDK
// has verified/decrypted a success notification or queried the same trade.
type VerifiedPayment struct {
	Provider        string
	MerchantID      string
	AppID           string
	MerchantOrderNo string
	TransactionID   string
	OpenID          string
	PaidCents       int64
	PaidAt          time.Time
}

type PaymentCallbackResult struct {
	ChargeOrderID  uint64 `json:"charge_order_id,omitempty"`
	ChargeOrderNo  string `json:"charge_order_no,omitempty"`
	RefundRequired bool   `json:"refund_required"`
	Replayed       bool   `json:"replayed"`
}

type PaymentCallbackStore struct {
	DB                 *gorm.DB
	ExpectedProvider   string
	ExpectedMerchantID string
	ExpectedAppID      string
}

func (s PaymentCallbackStore) Apply(ctx context.Context, payment VerifiedPayment) (PaymentCallbackResult, error) {
	paidAt := payment.PaidAt.UTC().Truncate(time.Millisecond)
	if s.DB == nil || s.ExpectedProvider == "" || s.ExpectedMerchantID == "" || s.ExpectedAppID == "" ||
		payment.Provider != s.ExpectedProvider || payment.MerchantID != s.ExpectedMerchantID || payment.AppID != s.ExpectedAppID ||
		payment.MerchantOrderNo == "" || len(payment.MerchantOrderNo) > 64 || payment.TransactionID == "" || len(payment.TransactionID) > 64 || payment.OpenID == "" ||
		payment.PaidCents <= 0 || paidAt.IsZero() || paidAt.After(time.Now().Add(5*time.Minute)) {
		return PaymentCallbackResult{}, ErrPaymentCallbackConflict
	}

	digest := callbackDigest(payment)
	var callbackResult PaymentCallbackResult
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var intent PaymentIntentRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("merchant_order_no = ?", payment.MerchantOrderNo).Take(&intent).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPaymentCallbackConflict
		}
		if err != nil {
			return err
		}
		var order PaymentOrderRecord
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND order_no = ?", intent.PaymentOrderID, intent.MerchantOrderNo).Take(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPaymentCallbackConflict
		}
		if err != nil {
			return err
		}
		if order.UserID != intent.UserID || intent.OpenID != payment.OpenID || order.TotalCents != intent.TotalCents || order.TotalCents != payment.PaidCents ||
			intent.ChargeQuantity == 0 || intent.PortNo == 0 || order.ID > math.MaxInt64 {
			return ErrPaymentCallbackConflict
		}

		var previousDigest PaymentCallbackDigestRecord
		digestErr := tx.Where("wechat_transaction_id = ?", payment.TransactionID).Take(&previousDigest).Error
		if digestErr != nil && !errors.Is(digestErr, gorm.ErrRecordNotFound) {
			return digestErr
		}
		if digestErr == nil && previousDigest.RequestDigest != digest {
			return ErrPaymentCallbackConflict
		}

		if order.Status == "paid" {
			if digestErr != nil || !order.WechatTransactionID.Valid || order.WechatTransactionID.String != payment.TransactionID || order.PaidCents != payment.PaidCents {
				return ErrPaymentCallbackConflict
			}
			if intent.Status == "refund_required" && !intent.ChargeOrderID.Valid {
				callbackResult = PaymentCallbackResult{RefundRequired: true, Replayed: true}
				return nil
			}
			if intent.Status != "paid" || !intent.ChargeOrderID.Valid || intent.ChargeOrderID.Int64 <= 0 || order.BizID != uint64(intent.ChargeOrderID.Int64) {
				return ErrPaymentCallbackConflict
			}
			var chargeOrder ChargeOrderRecord
			if err := tx.Select("order_no").Where("id = ? AND payment_order_id = ?", order.BizID, order.ID).Take(&chargeOrder).Error; err != nil {
				return err
			}
			callbackResult = PaymentCallbackResult{ChargeOrderID: order.BizID, ChargeOrderNo: chargeOrder.OrderNo, Replayed: true}
			return nil
		}
		if order.Status != "initiated" || order.BizID != 0 || digestErr == nil {
			return ErrPaymentCallbackConflict
		}
		if err := tx.Create(&PaymentCallbackDigestRecord{WechatTransactionID: payment.TransactionID, RequestDigest: digest}).Error; err != nil {
			if isMySQLDuplicate(err) {
				return ErrPaymentCallbackConflict
			}
			return err
		}

		updated := tx.Model(&PaymentOrderRecord{}).Where("id = ? AND order_no = ? AND status = 'initiated' AND total_cents = ? AND paid_cents = 0", order.ID, order.OrderNo, payment.PaidCents).
			Updates(map[string]any{"status": "paid", "paid_cents": payment.PaidCents, "wechat_transaction_id": payment.TransactionID, "paid_at": paidAt})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrPaymentCallbackConflict
		}

		if intent.Status != "initiated" || time.Now().After(intent.ExpiresAt) || paidAt.After(intent.ExpiresAt) {
			return queueLatePaymentRefund(tx, intent, order, payment, digest, paidAt)
		}

		chargeNo := "CH" + strings.ReplaceAll(uuid.NewString(), "-", "")
		chargeOrder := ChargeOrderRecord{OrderNo: chargeNo, UserID: intent.UserID, DeviceID: intent.DeviceID, PortNo: intent.PortNo,
			PortCode: sql.NullString{String: intent.PortCode, Valid: true}, PaymentOrderID: sql.NullInt64{Int64: int64(order.ID), Valid: true},
			Status: "paid", ChargeMode: intent.ChargeMode, ChargeQuantity: intent.ChargeQuantity, CreatedMonth: utcDate()}
		if err := tx.Create(&chargeOrder).Error; err != nil {
			return err
		}
		if chargeOrder.ID == 0 || chargeOrder.ID > math.MaxInt64 {
			return ErrPaymentCallbackConflict
		}
		if err := tx.Create(&ChargePricingSnapshotRecord{ChargeOrderID: chargeOrder.ID, PaymentIntentID: intent.IntentID,
			UserID: intent.UserID, PortCode: intent.PortCode, PricingSnapshot: intent.PricingSnapshot}).Error; err != nil {
			return err
		}
		bound := tx.Model(&PaymentOrderRecord{}).Where("id = ? AND biz_id = 0 AND status = 'paid'", order.ID).Update("biz_id", chargeOrder.ID)
		if bound.Error != nil {
			return bound.Error
		}
		if bound.RowsAffected != 1 {
			return ErrPaymentCallbackConflict
		}
		paid := tx.Model(&PaymentIntentRecord{}).Where("intent_id = ? AND status = 'initiated' AND charge_order_id IS NULL", intent.IntentID).
			Updates(map[string]any{"status": "paid", "paid_at": paidAt, "charge_order_id": chargeOrder.ID})
		if paid.Error != nil {
			return paid.Error
		}
		if paid.RowsAffected != 1 {
			return ErrPaymentCallbackConflict
		}

		eventID := "P" + digest[:32]
		if err := tx.Create(&ChargeEventLogRecord{ChargeOrderID: chargeOrder.ID, EventID: eventID,
			Event: "payment_confirmed", Actor: "payment_callback", Detail: "verified payment callback", OccurredAt: paidAt}).Error; err != nil {
			return err
		}
		envelope, err := json.Marshal(map[string]any{"event_id": eventID, "charge_order_id": chargeOrder.ID, "order_no": chargeNo, "payment_order_id": order.ID})
		if err != nil {
			return err
		}
		if err := tx.Create(&EventOutboxRecord{EventID: eventID, Stream: "charge_payment_confirmed_stream", EnvelopeJSON: envelope}).Error; err != nil {
			return err
		}
		callbackResult = PaymentCallbackResult{ChargeOrderID: chargeOrder.ID, ChargeOrderNo: chargeNo}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return callbackResult, err
}

func queueLatePaymentRefund(tx *gorm.DB, intent PaymentIntentRecord, order PaymentOrderRecord, payment VerifiedPayment, digest string, paidAt time.Time) error {
	changed := tx.Model(&PaymentIntentRecord{}).Where("intent_id = ? AND status IN ('initiated','expired')", intent.IntentID).
		Updates(map[string]any{"status": "refund_required", "paid_at": paidAt})
	if changed.Error != nil {
		return changed.Error
	}
	if changed.RowsAffected != 1 {
		return ErrPaymentCallbackConflict
	}
	refundNo := "REF" + digest[:32]
	if err := tx.Create(&RefundRecord{RefundNo: refundNo, PaymentOrderID: order.ID, UserID: intent.UserID, BizType: "charge",
		BizID: 0, RefundCents: payment.PaidCents, Reason: sql.NullString{String: "payment arrived after intent expired", Valid: true},
		Status: "pending", CreatedMonth: utcDate()}).Error; err != nil {
		return err
	}
	eventID := "R" + digest[:32]
	envelope, err := json.Marshal(map[string]any{"event_id": eventID, "refund_no": refundNo, "payment_order_id": order.ID, "amount_cents": payment.PaidCents})
	if err != nil {
		return err
	}
	if err := tx.Create(&EventOutboxRecord{EventID: eventID, Stream: "refund_required_stream", EnvelopeJSON: envelope}).Error; err != nil {
		return err
	}
	return nil
}

func callbackDigest(payment VerifiedPayment) string {
	value := payment.Provider + "\x00" + payment.MerchantID + "\x00" + payment.AppID + "\x00" + payment.OpenID + "\x00" + payment.MerchantOrderNo + "\x00" + payment.TransactionID + "\x00" +
		decimalAmount(payment.PaidCents)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func decimalAmount(amount int64) string { return strconv.FormatInt(amount, 10) }
