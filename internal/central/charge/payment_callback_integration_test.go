package charge

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestVerifiedPaymentCreatesOneChargeOrderAfterCallback(t *testing.T) {
	url := os.Getenv("TEST_USER_DATABASE_URL")
	if url == "" {
		t.Skip("set a disposable user database URL")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.ExecContext(ctx, "INSERT INTO user (openid) VALUES (?)", "callback-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	transactionID := "wx-" + uuid.NewString()
	defer func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM event_outbox WHERE event_id IN (SELECT event_id FROM charge_event_log WHERE charge_order_id IN (SELECT id FROM charge_order WHERE user_id = ?))", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_event_log WHERE charge_order_id IN (SELECT id FROM charge_order WHERE user_id = ?)", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_order_pricing WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_order WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_payment_intent WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM payment_order WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM payment_callback_idempotent WHERE wechat_transaction_id = ?", transactionID)
		_, _ = db.ExecContext(ctx, "DELETE FROM user WHERE id = ?", userID)
	}()
	input := IntentInput{UserID: uint64(userID), ClientRequestID: uuid.NewString(),
		Port: ScanResult{Kind: "port", DeviceID: "paid-device", StationID: 9,
			Port: &ScanPort{PortID: "paid-device:1", DeviceID: "paid-device", PortNo: 1, Online: true, Available: true}},
		Energy: "1", Minutes: 60,
		Rule: pricing.Rule{ID: 3, StationID: 9, Version: 1, Spec: pricing.Spec{Basis: pricing.BasisEnergy, Windows: []pricing.Window{{Start: "00:00", End: "24:00", CentsPerKWh: 100}}, Service: pricing.ServiceFee{Mode: pricing.ServiceEnergy, CentsPerKWh: 40}}}}
	intent, err := (PaymentIntentStore{DB: testGORMDB(t, db)}).Reserve(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_order WHERE user_id = ?", userID).Scan(&before); err != nil || before != 0 {
		t.Fatalf("before callback orders=%d err=%v", before, err)
	}
	verified := VerifiedPayment{Provider: "simulation", MerchantID: "test-merchant", AppID: "test-app", MerchantOrderNo: intent.MerchantOrderNo,
		TransactionID: transactionID, OpenID: intent.OpenID, PaidCents: intent.Estimate.TotalCents, PaidAt: time.Now().UTC()}
	store := PaymentCallbackStore{DB: testGORMDB(t, db), ExpectedProvider: "simulation", ExpectedMerchantID: "test-merchant", ExpectedAppID: "test-app"}
	first, err := store.Apply(ctx, verified)
	if err != nil || first.ChargeOrderID == 0 || first.RefundRequired {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	replay, err := store.Apply(ctx, verified)
	if err != nil || !replay.Replayed || replay.ChargeOrderID != first.ChargeOrderID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	verified.PaidCents++
	if _, err := store.Apply(ctx, verified); !errors.Is(err, ErrPaymentCallbackConflict) {
		t.Fatalf("amount changed: %v", err)
	}
	var orders, receipts, events int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_order WHERE user_id = ? AND status = 'paid'", userID).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_order_pricing WHERE charge_order_id = ?", first.ChargeOrderID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM event_outbox WHERE stream = 'charge_payment_confirmed_stream' AND event_id IN (SELECT event_id FROM charge_event_log WHERE charge_order_id = ?)", first.ChargeOrderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if orders != 1 || receipts != 1 || events != 1 {
		t.Fatalf("orders=%d pricing=%d outbox=%d", orders, receipts, events)
	}
}

func TestLateVerifiedPaymentQueuesRefundWithoutChargeOrder(t *testing.T) {
	url := os.Getenv("TEST_USER_DATABASE_URL")
	if url == "" {
		t.Skip("set a disposable user database URL")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.ExecContext(ctx, "INSERT INTO user (openid) VALUES (?)", "late-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	transactionID := "wx-" + uuid.NewString()
	defer func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json, '$.payment_order_id')) IN (SELECT CAST(id AS CHAR) FROM payment_order WHERE user_id = ?)", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM refund_record WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_payment_intent WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM payment_order WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM payment_callback_idempotent WHERE wechat_transaction_id = ?", transactionID)
		_, _ = db.ExecContext(ctx, "DELETE FROM user WHERE id = ?", userID)
	}()
	input := IntentInput{UserID: uint64(userID), ClientRequestID: uuid.NewString(),
		Port: ScanResult{Kind: "port", DeviceID: "late-device", StationID: 9,
			Port: &ScanPort{PortID: "late-device:1", DeviceID: "late-device", PortNo: 1, Online: true, Available: true}},
		Energy: "1", Minutes: 60,
		Rule: pricing.Rule{ID: 3, StationID: 9, Version: 1, Spec: pricing.Spec{Basis: pricing.BasisEnergy, Windows: []pricing.Window{{Start: "00:00", End: "24:00", CentsPerKWh: 100}}, Service: pricing.ServiceFee{Mode: pricing.ServiceEnergy, CentsPerKWh: 40}}}}
	intent, err := (PaymentIntentStore{DB: testGORMDB(t, db)}).Reserve(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE charge_payment_intent SET expires_at = DATE_SUB(NOW(3), INTERVAL 1 MINUTE), status = 'expired' WHERE intent_id = ?", intent.IntentID); err != nil {
		t.Fatal(err)
	}
	verified := VerifiedPayment{Provider: "simulation", MerchantID: "test-merchant", AppID: "test-app", MerchantOrderNo: intent.MerchantOrderNo,
		TransactionID: transactionID, OpenID: intent.OpenID, PaidCents: intent.Estimate.TotalCents, PaidAt: time.Now().UTC()}
	store := PaymentCallbackStore{DB: testGORMDB(t, db), ExpectedProvider: "simulation", ExpectedMerchantID: "test-merchant", ExpectedAppID: "test-app"}
	result, err := store.Apply(ctx, verified)
	if err != nil || !result.RefundRequired || result.ChargeOrderID != 0 {
		t.Fatalf("late result=%+v err=%v", result, err)
	}
	replay, err := store.Apply(ctx, verified)
	if err != nil || !replay.RefundRequired || !replay.Replayed {
		t.Fatalf("late replay=%+v err=%v", replay, err)
	}
	var charges, refunds int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_order WHERE user_id = ?", userID).Scan(&charges); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM refund_record WHERE user_id = ? AND status = 'pending'", userID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if charges != 0 || refunds != 1 {
		t.Fatalf("late charges=%d refunds=%d", charges, refunds)
	}
}
