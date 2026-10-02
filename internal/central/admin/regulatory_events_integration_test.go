package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/delivery"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// 平移原 internal/regulatory 的入队语义：六类对象、event_id 幂等、
// 同 ID 不同内容拒绝；状态查询端点要求服务令牌。
func TestRegulatoryEventsEnqueueIdempotent(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable central database required")
	}
	ctx := context.Background()
	conn, err := dbconn.Open(ctx, os.Getenv("TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	queue := RegulatoryQueue{AdminDB: conn}
	objects := []struct {
		kind string
		key  string
		data string
	}{
		{"operator", "vendor-1", `{"vendor_id":"vendor-1","name":"测试运营商","contact":"13800000000"}`},
		{"station", "station-1", `{"station_id":"station-1","name":"站点","address":"上海","longitude":121.5,"latitude":31.2,"service_type":"charge"}`},
		{"device", "device-1", `{"device_id":"device-1","model":"AC-2","protocol":"dc589","firmware_version":"1.0"}`},
		{"order", "order-1", `{"order_id":"order-1","started_at":"2026-09-29T12:00:00Z","ended_at":"2026-09-29T12:30:00Z","energy_kwh":"1.50","amount_cents":250}`},
		{"alert", "alert-1", `{"alert_id":"alert-1","type":"temperature","severity":"critical","time":"2026-09-29T12:00:00Z"}`},
		{"battery", "battery-1", `{"battery_code":"battery-1","soc":80.5}`},
	}
	ids := make([]string, 0, len(objects))
	for _, object := range objects {
		event := delivery.Event{EventID: uuid.NewString(), ObjectType: object.kind, ObjectKey: object.key, Data: json.RawMessage(object.data)}
		ids = append(ids, event.EventID)
		created, err := queue.Enqueue(ctx, event)
		if err != nil || !created {
			t.Fatalf("enqueue %s created=%v err=%v", object.kind, created, err)
		}
		created, err = queue.Enqueue(ctx, event)
		if err != nil || created {
			t.Fatalf("duplicate %s created=%v err=%v", object.kind, created, err)
		}
		changed := event
		var revised map[string]any
		if err := json.Unmarshal(event.Data, &revised); err != nil {
			t.Fatal(err)
		}
		revised["revision"] = 2
		changed.Data, _ = json.Marshal(revised)
		if _, err := queue.Enqueue(ctx, changed); !errors.Is(err, errRegulatoryConflict) {
			t.Fatalf("changed duplicate error=%v for %s", err, object.kind)
		}
	}
	defer func() {
		for _, id := range ids {
			_, _ = conn.ExecContext(ctx, "DELETE FROM regulatory_report WHERE event_id = ?", id)
		}
	}()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegulatoryEventsAPI{Queue: queue, ServiceToken: "reg-secret"}.Register(router)
	path := "/api/v1/internal/regulatory/events/" + ids[0]
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
	if response.Code != 401 {
		t.Fatalf("status without token returned %d", response.Code)
	}
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("X-Service-Token", "reg-secret")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"status":"queued"`) {
		t.Fatalf("queued status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest("GET", path, nil)
	request.Header.Set("X-Service-Token", "reg-secret")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"attempts":0`) {
		t.Fatalf("queued attempts status=%d body=%s", response.Code, response.Body.String())
	}
}
