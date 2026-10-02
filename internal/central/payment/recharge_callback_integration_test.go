package payment

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/google/uuid"
)

// TestVerifiedRechargeCreditsWalletExactlyOnce 验证充值回调按 biz_type 分流且仅入账一次，无需支付意图。
func TestVerifiedRechargeCreditsWalletExactlyOnce(t *testing.T) {
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

	user, err := db.ExecContext(ctx, "INSERT INTO user (openid) VALUES (?)", "recharge-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	transactionID := "wx-" + uuid.NewString()
	defer func() {
		for _, stmt := range []string{
			"DELETE FROM event_outbox WHERE stream = 'wallet_recharge_settled_stream'",
			"DELETE FROM wallet_txn WHERE user_id = ?",
			"DELETE FROM wallet_recharge_request WHERE user_id = ?",
			"DELETE FROM wallet_account WHERE user_id = ?",
			"DELETE FROM payment_order WHERE user_id = ?",
			"DELETE FROM payment_callback_idempotent WHERE wechat_transaction_id = ?",
			"DELETE FROM user WHERE id = ?",
		} {
			if stmt == "DELETE FROM event_outbox WHERE stream = 'wallet_recharge_settled_stream'" {
				_, _ = db.ExecContext(ctx, stmt)
				continue
			}
			if stmt == "DELETE FROM payment_callback_idempotent WHERE wechat_transaction_id = ?" {
				_, _ = db.ExecContext(ctx, stmt, transactionID)
				continue
			}
			if stmt == "DELETE FROM user WHERE id = ?" {
				_, _ = db.ExecContext(ctx, stmt, userID)
				continue
			}
			_, _ = db.ExecContext(ctx, stmt, userID)
		}
	}()

	if _, err := db.ExecContext(ctx, "INSERT INTO wallet_account (user_id, balance_cents, frozen_cents, status, version) VALUES (?, 0, 0, 'active', 0)", userID); err != nil {
		t.Fatal(err)
	}
	const amount = int64(5000)
	orderNo := "RC" + uuid.NewString()[:12]
	payment, err := db.ExecContext(ctx, `INSERT INTO payment_order
		(order_no, biz_type, biz_id, user_id, pay_method, total_cents, paid_cents, status, created_month)
		VALUES (?, 'wallet_recharge', 0, ?, 'wechat', ?, 0, 'initiated', ?)`, orderNo, userID, amount, dbutil.MonthStart())
	if err != nil {
		t.Fatal(err)
	}
	paymentOrderID, _ := payment.LastInsertId()
	requestID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO wallet_recharge_request
		(request_id, user_id, amount_cents, payment_order_id) VALUES (?, ?, ?, ?)`,
		requestID, userID, amount, paymentOrderID); err != nil {
		t.Fatal(err)
	}

	store := PaymentCallbackStore{DB: testGORMDB(t, db), ExpectedProvider: "simulation", ExpectedMerchantID: "test-merchant", ExpectedAppID: "test-app"}
	verified := VerifiedPayment{Provider: "simulation", MerchantID: "test-merchant", AppID: "test-app",
		MerchantOrderNo: orderNo, TransactionID: transactionID, OpenID: "recharge-openid",
		PaidCents: amount, PaidAt: time.Now().UTC()}

	if _, err := store.Apply(ctx, verified); err != nil {
		t.Fatalf("recharge callback rejected: %v", err)
	}
	var balance int64
	if err := db.QueryRowContext(ctx, "SELECT balance_cents FROM wallet_account WHERE user_id = ?", userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != amount {
		t.Fatalf("balance after recharge = %d, want %d", balance, amount)
	}
	var txns int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wallet_txn WHERE user_id = ? AND biz_type = 'recharge'", userID).Scan(&txns); err != nil {
		t.Fatal(err)
	}
	if txns != 1 {
		t.Fatalf("wallet_txn rows = %d, want 1", txns)
	}

	// 重放的通知不能第二次入账。
	result, err := store.Apply(ctx, verified)
	if err != nil {
		t.Fatalf("replayed callback rejected: %v", err)
	}
	if !result.Replayed {
		t.Fatal("second callback should report a replay")
	}
	if err := db.QueryRowContext(ctx, "SELECT balance_cents FROM wallet_account WHERE user_id = ?", userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != amount {
		t.Fatalf("balance after replay = %d, want %d (double credit)", balance, amount)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_order WHERE user_id = ?", userID).Scan(&txns); err != nil {
		t.Fatal(err)
	}
	if txns != 0 {
		t.Fatalf("recharge created %d charge orders, want 0", txns)
	}
}

// TestVerifiedRechargeRejectsAmountMismatch —— 金额与申请不一致的验签回调必须被拒绝，
// 充值金额与支付订单对不上时也不得结算。
func TestVerifiedRechargeRejectsAmountMismatch(t *testing.T) {
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

	user, err := db.ExecContext(ctx, "INSERT INTO user (openid) VALUES (?)", "recharge-bad-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	defer func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM wallet_recharge_request WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM wallet_account WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM payment_order WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM user WHERE id = ?", userID)
	}()
	if _, err := db.ExecContext(ctx, "INSERT INTO wallet_account (user_id, balance_cents, frozen_cents, status, version) VALUES (?, 0, 0, 'active', 0)", userID); err != nil {
		t.Fatal(err)
	}
	orderNo := "RC" + uuid.NewString()[:12]
	transactionID := "wx-" + uuid.NewString()
	defer func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM payment_callback_idempotent WHERE wechat_transaction_id = ?", transactionID)
	}()
	payment, err := db.ExecContext(ctx, `INSERT INTO payment_order
		(order_no, biz_type, biz_id, user_id, pay_method, total_cents, paid_cents, status, created_month)
		VALUES (?, 'wallet_recharge', 0, ?, 'wechat', 3000, 0, 'initiated', ?)`, orderNo, userID, dbutil.MonthStart())
	if err != nil {
		t.Fatal(err)
	}
	paymentOrderID, _ := payment.LastInsertId()
	// 申请写的是 5000，
	// 而支付订单和验签后的通知都写着 3000。
	if _, err := db.ExecContext(ctx, `INSERT INTO wallet_recharge_request
		(request_id, user_id, amount_cents, payment_order_id) VALUES (?, ?, 5000, ?)`,
		uuid.NewString(), userID, paymentOrderID); err != nil {
		t.Fatal(err)
	}
	store := PaymentCallbackStore{DB: testGORMDB(t, db), ExpectedProvider: "simulation", ExpectedMerchantID: "test-merchant", ExpectedAppID: "test-app"}
	if _, err := store.Apply(ctx, VerifiedPayment{Provider: "simulation", MerchantID: "test-merchant", AppID: "test-app",
		MerchantOrderNo: orderNo, TransactionID: transactionID, OpenID: "recharge-openid",
		PaidCents: 3000, PaidAt: time.Now().UTC()}); err == nil {
		t.Fatal("a recharge whose request amount disagrees with the payment must be refused")
	}
	var balance int64
	if err := db.QueryRowContext(ctx, "SELECT balance_cents FROM wallet_account WHERE user_id = ?", userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 0 {
		t.Fatalf("balance = %d after a refused callback, want 0", balance)
	}
}
