package charge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/google/uuid"
)

func TestStartAuthorizationRequiresMatchingPaidRecords(t *testing.T) {
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
	openid := "go-test-start-auth-" + uuid.NewString()
	orderNo := "ORD-" + uuid.NewString()
	month := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	user, err := db.ExecContext(ctx, "INSERT INTO user (openid) VALUES (?)", openid)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	defer db.ExecContext(ctx, "DELETE FROM user WHERE id = ?", userID)
	order, err := db.ExecContext(ctx, "INSERT INTO charge_order (order_no,user_id,device_id,port_no,created_month) VALUES (?,?,?,?,?)", orderNo, userID, "BOARD-TEST", 1, month)
	if err != nil {
		t.Fatal(err)
	}
	orderID, _ := order.LastInsertId()
	defer db.ExecContext(ctx, "DELETE FROM charge_order WHERE id = ? AND created_month = ?", orderID, month)
	payment, err := db.ExecContext(ctx, "INSERT INTO payment_order (order_no,biz_type,biz_id,user_id,pay_method,total_cents,created_month) VALUES (?,'charge',?,?,'wechat',100,?)", "PAY-"+uuid.NewString(), orderID, userID, month)
	if err != nil {
		t.Fatal(err)
	}
	paymentID, _ := payment.LastInsertId()
	defer db.ExecContext(ctx, "DELETE FROM payment_order WHERE id = ? AND created_month = ?", paymentID, month)
	if _, err := db.ExecContext(ctx, "UPDATE charge_order SET payment_order_id = ? WHERE id = ? AND created_month = ?", paymentID, orderID, month); err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter()
	StartAuthorization{DB: db, ServiceToken: "test-service-token"}.Register(router)
	request := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/charge-orders/"+orderNo+"/start-authorization", nil)
		req.Header.Set("X-Service-Token", token)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		return resp
	}
	if got := request(""); got.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth: %d", got.Code)
	}
	if got := request("test-service-token"); got.Code != http.StatusConflict {
		t.Fatalf("unpaid authorized: %d", got.Code)
	}
	if _, err := db.ExecContext(ctx, "UPDATE charge_order SET status = 'paid' WHERE id = ? AND created_month = ?", orderID, month); err != nil {
		t.Fatal(err)
	}
	if got := request("test-service-token"); got.Code != http.StatusConflict {
		t.Fatalf("unpaid payment authorized: %d", got.Code)
	}
	if _, err := db.ExecContext(ctx, "UPDATE payment_order SET status = 'paid', paid_cents = 99 WHERE id = ? AND created_month = ?", paymentID, month); err != nil {
		t.Fatal(err)
	}
	if got := request("test-service-token"); got.Code != http.StatusConflict {
		t.Fatalf("underpaid authorized: %d", got.Code)
	}
	if _, err := db.ExecContext(ctx, "UPDATE payment_order SET paid_cents = 100 WHERE id = ? AND created_month = ?", paymentID, month); err != nil {
		t.Fatal(err)
	}
	if got := request("test-service-token"); got.Code != http.StatusConflict {
		t.Fatalf("paid order without command limit authorized: %d", got.Code)
	}
	if _, err := db.ExecContext(ctx, "UPDATE charge_order SET charge_mode = 4, charge_quantity = 600 WHERE id = ? AND created_month = ?", orderID, month); err != nil {
		t.Fatal(err)
	}
	if got := request("test-service-token"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), orderNo) {
		t.Fatalf("paid order rejected: %d %s", got.Code, got.Body.String())
	}
}
