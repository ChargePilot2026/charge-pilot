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

	paymentdb "github.com/ChargePilot2026/charge-pilot/internal/central/charge/paymentgenerated"
	"github.com/google/uuid"
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
	DB                 *sql.DB
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
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return PaymentCallbackResult{}, err
	}
	defer tx.Rollback()
	q := paymentdb.New(tx)
	intent, err := q.LockIntentForCallback(ctx, payment.MerchantOrderNo)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentCallbackResult{}, ErrPaymentCallbackConflict
	}
	if err != nil {
		return PaymentCallbackResult{}, err
	}
	order, err := q.LockPaymentForCallback(ctx, paymentdb.LockPaymentForCallbackParams{ID: intent.PaymentOrderID, OrderNo: intent.MerchantOrderNo})
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentCallbackResult{}, ErrPaymentCallbackConflict
	}
	if err != nil {
		return PaymentCallbackResult{}, err
	}
	if order.UserID != intent.UserID || intent.Openid != payment.OpenID || order.TotalCents != intent.TotalCents || order.TotalCents != payment.PaidCents ||
		intent.ChargeQuantity == 0 || intent.PortNo == 0 || order.ID > math.MaxInt64 {
		return PaymentCallbackResult{}, ErrPaymentCallbackConflict
	}
	previousDigest, err := q.CallbackDigestByTransaction(ctx, payment.TransactionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PaymentCallbackResult{}, err
	}
	if err == nil && (!previousDigest.Valid || previousDigest.String != digest) {
		return PaymentCallbackResult{}, ErrPaymentCallbackConflict
	}
	if order.Status == paymentdb.PaymentOrderStatusPaid {
		if err != nil || !order.WechatTransactionID.Valid || order.WechatTransactionID.String != payment.TransactionID ||
			order.PaidCents != payment.PaidCents {
			return PaymentCallbackResult{}, ErrPaymentCallbackConflict
		}
		if intent.Status == paymentdb.ChargePaymentIntentStatusRefundRequired && !intent.ChargeOrderID.Valid {
			return PaymentCallbackResult{RefundRequired: true, Replayed: true}, tx.Commit()
		}
		if intent.Status != paymentdb.ChargePaymentIntentStatusPaid || !intent.ChargeOrderID.Valid ||
			intent.ChargeOrderID.Int64 <= 0 || order.BizID != uint64(intent.ChargeOrderID.Int64) {
			return PaymentCallbackResult{}, ErrPaymentCallbackConflict
		}
		var chargeNo string
		if err := tx.QueryRowContext(ctx, "SELECT order_no FROM charge_order WHERE id = ? AND payment_order_id = ? LIMIT 1", order.BizID, order.ID).Scan(&chargeNo); err != nil {
			return PaymentCallbackResult{}, err
		}
		return PaymentCallbackResult{ChargeOrderID: order.BizID, ChargeOrderNo: chargeNo, Replayed: true}, tx.Commit()
	}
	if order.Status != paymentdb.PaymentOrderStatusInitiated || order.BizID != 0 || err == nil {
		return PaymentCallbackResult{}, ErrPaymentCallbackConflict
	}
	if err := q.InsertCallbackDigest(ctx, paymentdb.InsertCallbackDigestParams{WechatTransactionID: payment.TransactionID, RequestDigest: sql.NullString{String: digest, Valid: true}}); err != nil {
		return PaymentCallbackResult{}, err
	}
	changed, err := q.MarkPaymentPaidByCallback(ctx, paymentdb.MarkPaymentPaidByCallbackParams{PaidCents: payment.PaidCents,
		WechatTransactionID: sql.NullString{String: payment.TransactionID, Valid: true},
		PaidAt:              sql.NullTime{Time: paidAt, Valid: true}, ID: order.ID, OrderNo: order.OrderNo, TotalCents: payment.PaidCents})
	if err := requireOne(changed, err); err != nil {
		return PaymentCallbackResult{}, err
	}
	if intent.Status != paymentdb.ChargePaymentIntentStatusInitiated || time.Now().After(intent.ExpiresAt) || paidAt.After(intent.ExpiresAt) {
		return s.queueLateRefund(ctx, tx, q, intent, order, payment, digest, paidAt)
	}
	chargeNo := "CH" + strings.ReplaceAll(uuid.NewString(), "-", "")
	created, err := q.CreateChargeOrderFromPayment(ctx, paymentdb.CreateChargeOrderFromPaymentParams{
		OrderNo: chargeNo, UserID: intent.UserID, DeviceID: intent.DeviceID, PortNo: intent.PortNo,
		PortCode:       sql.NullString{String: intent.PortCode, Valid: true},
		PaymentOrderID: sql.NullInt64{Int64: int64(order.ID), Valid: true},
		ChargeMode:     sql.NullInt16{Int16: int16(intent.ChargeMode), Valid: true},
		ChargeQuantity: sql.NullInt16{Int16: int16(intent.ChargeQuantity), Valid: true},
	})
	if err != nil {
		return PaymentCallbackResult{}, err
	}
	chargeID, err := created.LastInsertId()
	if err != nil || chargeID <= 0 || chargeID > math.MaxInt64 {
		return PaymentCallbackResult{}, ErrPaymentCallbackConflict
	}
	if err := q.InsertChargePricingSnapshot(ctx, paymentdb.InsertChargePricingSnapshotParams{ChargeOrderID: uint64(chargeID), PaymentIntentID: intent.IntentID,
		UserID: intent.UserID, PortCode: intent.PortCode, PricingSnapshot: intent.PricingSnapshot}); err != nil {
		return PaymentCallbackResult{}, err
	}
	if changed, err := q.BindPaymentToCharge(ctx, paymentdb.BindPaymentToChargeParams{BizID: uint64(chargeID), ID: order.ID}); err != nil {
		return PaymentCallbackResult{}, err
	} else if err := requireOne(changed, nil); err != nil {
		return PaymentCallbackResult{}, err
	}
	if changed, err := q.MarkIntentPaid(ctx, paymentdb.MarkIntentPaidParams{PaidAt: sql.NullTime{Time: paidAt, Valid: true}, ChargeOrderID: sql.NullInt64{Int64: chargeID, Valid: true}, IntentID: intent.IntentID}); err != nil {
		return PaymentCallbackResult{}, err
	} else if err := requireOne(changed, nil); err != nil {
		return PaymentCallbackResult{}, err
	}
	eventID := "P" + digest[:32]
	if err := q.InsertPaymentChargeEvent(ctx, paymentdb.InsertPaymentChargeEventParams{ChargeOrderID: uint64(chargeID), EventID: eventID,
		Event: "payment_confirmed", Detail: "verified payment callback", OccurredAt: paidAt}); err != nil {
		return PaymentCallbackResult{}, err
	}
	envelope, _ := json.Marshal(map[string]any{"event_id": eventID, "charge_order_id": chargeID, "order_no": chargeNo, "payment_order_id": order.ID})
	if err := q.InsertPaymentOutbox(ctx, paymentdb.InsertPaymentOutboxParams{EventID: eventID, Stream: "charge_payment_confirmed_stream", EnvelopeJson: envelope}); err != nil {
		return PaymentCallbackResult{}, err
	}
	return PaymentCallbackResult{ChargeOrderID: uint64(chargeID), ChargeOrderNo: chargeNo}, tx.Commit()
}

func (s PaymentCallbackStore) queueLateRefund(ctx context.Context, tx *sql.Tx, q *paymentdb.Queries, intent paymentdb.LockIntentForCallbackRow, order paymentdb.LockPaymentForCallbackRow, payment VerifiedPayment, digest string, paidAt time.Time) (PaymentCallbackResult, error) {
	changed, err := q.MarkIntentRefundRequired(ctx, paymentdb.MarkIntentRefundRequiredParams{PaidAt: sql.NullTime{Time: paidAt, Valid: true}, IntentID: intent.IntentID})
	if err := requireOne(changed, err); err != nil {
		return PaymentCallbackResult{}, err
	}
	refundNo := "REF" + digest[:32]
	if err := q.InsertLatePaymentRefund(ctx, paymentdb.InsertLatePaymentRefundParams{RefundNo: refundNo, PaymentOrderID: order.ID,
		UserID: intent.UserID, RefundCents: payment.PaidCents}); err != nil {
		return PaymentCallbackResult{}, err
	}
	eventID := "R" + digest[:32]
	envelope, _ := json.Marshal(map[string]any{"event_id": eventID, "refund_no": refundNo, "payment_order_id": order.ID, "amount_cents": payment.PaidCents})
	if err := q.InsertPaymentOutbox(ctx, paymentdb.InsertPaymentOutboxParams{EventID: eventID, Stream: "refund_required_stream", EnvelopeJson: envelope}); err != nil {
		return PaymentCallbackResult{}, err
	}
	return PaymentCallbackResult{RefundRequired: true}, tx.Commit()
}

func callbackDigest(payment VerifiedPayment) string {
	value := payment.Provider + "\x00" + payment.MerchantID + "\x00" + payment.AppID + "\x00" + payment.OpenID + "\x00" + payment.MerchantOrderNo + "\x00" + payment.TransactionID + "\x00" +
		decimalAmount(payment.PaidCents)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func decimalAmount(amount int64) string { return strconv.FormatInt(amount, 10) }

func requireOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return ErrPaymentCallbackConflict
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrPaymentCallbackConflict
	}
	return nil
}
