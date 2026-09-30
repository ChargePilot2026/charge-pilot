package regulatory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type failingSender struct{}

func (failingSender) Mode() string                      { return "http" }
func (failingSender) Send(context.Context, Event) error { return errors.New("receiver offline") }

func TestSixRegulatoryObjectsAndOfflineRetry(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable admin MySQL required")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, os.Getenv("TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queue := Queue{DB: db}
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
		event := Event{EventID: uuid.NewString(), ObjectType: object.kind, ObjectKey: object.key, Data: json.RawMessage(object.data)}
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
		if _, err := queue.Enqueue(ctx, changed); !errors.Is(err, ErrEventConflict) {
			t.Fatalf("changed duplicate error=%v for %s", err, object.kind)
		}
	}
	defer func() {
		for _, id := range ids {
			_, _ = db.ExecContext(ctx, "DELETE FROM regulatory_report WHERE event_id = ?", id)
		}
	}()
	if _, err := ValidateEvent(Event{EventID: uuid.NewString(), ObjectType: "battery", ObjectKey: "bat", Data: json.RawMessage(`{"battery_code":"bat","soc":101}`)}); err == nil {
		t.Fatal("invalid battery SOC accepted")
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	API{Queue: queue, ServiceToken: "reg-secret"}.Register(router)
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
	// 第一次尝试失败，
	// 但已落库的事件仍留在队列里等待重试。
	count, err := (Deliverer{DB: db, Sender: failingSender{}}).RunBatch(ctx)
	if count != 0 || err == nil {
		t.Fatalf("offline delivery count=%d err=%v", count, err)
	}
	var state string
	var attempts int
	if err := db.QueryRowContext(ctx, "SELECT status,attempts FROM regulatory_report WHERE event_id = ?", ids[0]).Scan(&state, &attempts); err != nil || state != "queued" || attempts != 1 {
		t.Fatalf("retry state=%s attempts=%d err=%v", state, attempts, err)
	}
	for _, id := range ids {
		if _, err := db.ExecContext(ctx, "UPDATE regulatory_report SET next_attempt_at = ? WHERE event_id = ?", time.Now().UTC().Add(-time.Second), id); err != nil {
			t.Fatal(err)
		}
	}
	count, err = (Deliverer{DB: db, Sender: SimulationSender{}}).RunBatch(ctx)
	if err != nil || count != len(objects) {
		t.Fatalf("simulation count=%d err=%v", count, err)
	}
	request = httptest.NewRequest("GET", path, nil)
	request.Header.Set("X-Service-Token", "reg-secret")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"delivered_mode":"simulation"`) {
		t.Fatalf("simulation status=%d body=%s", response.Code, response.Body.String())
	}
}
