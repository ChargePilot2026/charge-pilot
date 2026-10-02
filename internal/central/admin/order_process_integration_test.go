package admin

import (
	"encoding/json"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/central/order"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/google/uuid"
)

func TestOrderProcessLoadsCanonicalOrderAndRejectsDeletedOrder(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" {
		t.Skip("MySQL with current user schema required")
	}
	tx := openFinanceDB(t, "TEST_USER_DATABASE_URL").Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("rollback process fixtures: %v", err)
		}
	})
	tag := uuid.NewString()
	user := struct {
		ID     uint64
		Openid string
	}{Openid: "process-proxy-" + tag}
	if err := tx.Table("user").Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	order := order.ChargeOrderRecord{OrderNo: "process-" + tag, UserID: user.ID, DeviceID: "process-board-" + tag, PortNo: 2, Status: "completed", CreatedMonth: month}
	if err := tx.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var fail atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if r.URL.Path != "/api/v1/internal/charge-orders/"+order.OrderNo+"/process" || q.Get("charge_order_id") != fmt.Sprint(order.ID) || q.Get("device_id") != order.DeviceID || q.Get("port_no") != "2" || q.Get("after_id") != "0" || q.Get("limit") != "1000" || len(q) != 5 || r.Header.Get("X-Service-Token") != "process-service" {
			t.Errorf("client changed canonical order identity: %s", r.URL)
		}
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[],"next_after_id":null}}`))
	}))
	defer gateway.Close()
	api := ResourceAPI{Store: ResourceStore{UserDB: tx}, GatewayURL: gateway.URL, ServiceToken: "process-service"}
	router := httpapi.NewRouter()
	router.GET("/orders/:id/process", api.orderProcess)
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		return response
	}
	path := fmt.Sprintf("/orders/%d/process?charge_order_id=999&order_no=other&device_id=other&port_no=9", order.ID)
	response := request(path)
	if response.Code != 200 {
		t.Fatalf("process request: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Data OrderProcessPage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Data.Items == nil || result.Data.NextAfterID != nil {
		t.Fatalf("process response: %s %v", response.Body.String(), err)
	}
	fail.Store(true)
	if response = request(path); response.Code != 503 {
		t.Fatalf("gateway outage became empty successful chart: %d %s", response.Code, response.Body.String())
	}
	if err := tx.Table("charge_order").Where("id=?", order.ID).Update("deleted_at", now).Error; err != nil {
		t.Fatal(err)
	}
	if response = request(path); response.Code != 404 || calls.Load() != 2 {
		t.Fatalf("deleted order reached gateway: status=%d calls=%d", response.Code, calls.Load())
	}
}
