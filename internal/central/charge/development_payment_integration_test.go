package charge

import (
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/google/uuid"
	"os"
	"testing"
)

// Exercise the same authenticated checkout used by H5, including receipt
// replay, cross-account access, wallet splitting, and terminal settlement.
func TestDevelopmentWalletCheckoutAndRefund(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	db := openAccountDB(t, "TEST_USER_DATABASE_URL")
	admin := openAccountDB(t, "TEST_ADMIN_DATABASE_URL")
	first, second := createTwoUsers(t, db)
	users := []uint64{first, second}
	t.Cleanup(func() {
		db.Exec("DELETE l FROM wallet_risk_freeze_link l JOIN wallet_refund_request r ON r.request_id=l.request_id WHERE r.user_id IN ?", users)
		db.Exec("DELETE FROM risk_freeze_log WHERE user_id IN ?", users)
		db.Exec("DELETE x FROM wallet_refund_part x JOIN refund_record r ON r.id=x.refund_record_id WHERE r.user_id IN ?", users)
		db.Exec("DELETE FROM refund_record WHERE user_id IN ?", users)
		db.Exec("DELETE FROM wallet_refund_request WHERE user_id IN ?", users)
		db.Exec("DELETE FROM wallet_txn WHERE user_id IN ?", users)
		db.Exec("DELETE c FROM payment_callback_idempotent c JOIN payment_order p ON p.wechat_transaction_id=c.wechat_transaction_id WHERE p.user_id IN ?", users)
		db.Exec("DELETE c FROM charge_prepay c JOIN payment_order p ON p.id=c.payment_order_id WHERE p.user_id IN ?", users)
		db.Exec("DELETE FROM wallet_recharge_request WHERE user_id IN ?", users)
		db.Exec("DELETE FROM payment_order WHERE user_id IN ?", users)
		db.Exec("DELETE FROM wallet_account WHERE user_id IN ?", users)
		db.Exec("DELETE FROM user WHERE id IN ?", users)
	})
	one, two := accountRouter(t, db, admin, first), accountRouter(t, db, admin, second)
	request := map[string]any{"request_id": uuid.NewString(), "amount_cents": 1000}
	code, body := callJSON(t, one, "POST", "/api/v1/user/wallet/recharge", request)
	if code != 200 {
		t.Fatalf("recharge %d: %v", code, body)
	}
	result := body["data"].(map[string]any)
	if result["can_pay"] != true {
		t.Fatal(result)
	}
	order := result["merchant_order_no"].(string)
	code, replay := callJSON(t, one, "POST", "/api/v1/user/wallet/recharge", request)
	if code != 200 || replay["data"].(map[string]any)["payment_order_id"] != result["payment_order_id"] {
		t.Fatalf("replay %d %v", code, replay)
	}
	if code, body = callJSON(t, two, "POST", "/api/v1/user/wallet/recharge", request); code != 409 {
		t.Fatalf("foreign replay %d %v", code, body)
	}
	confirm := map[string]any{"merchant_order_no": order}
	if code, body = callJSON(t, two, "POST", "/api/v1/user/development/payments/confirm", confirm); code != 404 {
		t.Fatalf("foreign confirm %d %v", code, body)
	}
	for range 2 {
		if code, body = callJSON(t, one, "POST", "/api/v1/user/development/payments/confirm", confirm); code != 200 {
			t.Fatalf("confirm %d %v", code, body)
		}
	}
	if b := readBalance(t, one); b["balance_cents"] != float64(1000) {
		t.Fatalf("double credit %v", b)
	}
	if code, body = callJSON(t, one, "POST", "/api/v1/user/wallet/recharge", request); code != 200 || body["data"].(map[string]any)["can_pay"] != false {
		t.Fatalf("paid replay %d %v", code, body)
	}
	// Refund amounts are reserved once, then released by the actual executor.
	refund := map[string]any{"request_id": uuid.NewString(), "amount_cents": 300, "reason": nil}
	for range 2 {
		if code, body = callJSON(t, one, "POST", "/api/v1/user/wallet/refund", refund); code != 200 || body["data"].(map[string]any)["status"] != "accepted" {
			t.Fatalf("refund %d %v", code, body)
		}
	}
	if b := readBalance(t, one); b["frozen_cents"] != float64(300) {
		t.Fatal(b)
	}
	refund["amount_cents"] = 301
	if code, _ = callJSON(t, one, "POST", "/api/v1/user/wallet/refund", refund); code != 409 {
		t.Fatal("changed replay accepted")
	}
	refund["amount_cents"] = 300
	if code, _ = callJSON(t, two, "POST", "/api/v1/user/wallet/refund", refund); code != 409 && code != 404 {
		t.Fatalf("foreign refund %d", code)
	}
	exec := RefundExecutor{DB: db, Provider: payment.Simulator{}, ProviderName: "simulation"}
	if _, err := exec.Batch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b := readBalance(t, one); b["balance_cents"] != float64(700) || b["frozen_cents"] != float64(0) {
		t.Fatal(b)
	}
	code, body = callJSON(t, one, "GET", "/api/v1/user/wallet/refunds", nil)
	if code != 200 {
		t.Fatal(body)
	}
	data := body["data"].(map[string]any)
	items := data["items"].([]any)
	item := items[0].(map[string]any)
	if item["status"] != "success" || item["refunded_cents"] != float64(300) || data["user_id"] == nil {
		t.Fatal(data)
	}
	// Third distinct valid claim in five minutes creates a linked risk ticket.
	for i := range 2 {
		req := map[string]any{"request_id": uuid.NewString(), "amount_cents": 100}
		code, body = callJSON(t, one, "POST", "/api/v1/user/wallet/refund", req)
		if code != 200 {
			t.Fatal(body)
		}
		want := "accepted"
		if i == 1 {
			want = "manual_review"
		}
		if body["data"].(map[string]any)["status"] != want {
			t.Fatal(body)
		}
	}
	if b := readBalance(t, one); b["status"] != "frozen" || b["frozen_cents"] != float64(100) {
		t.Fatal(b)
	}
}
