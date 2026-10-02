package delivery_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/admin"
	"github.com/ChargePilot2026/charge-pilot/internal/delivery"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/google/uuid"
)

type failingSender struct{}

func (failingSender) Mode() string { return "http" }
func (failingSender) Send(context.Context, delivery.Event) error {
	return errors.New("receiver offline")
}

// 平移原 internal/regulatory 集成场景：worker 侧 delivery.RegulatoryDeliverer
// 经 HTTP 领取、发送、回执状态；失败保留重试，成功后置 delivered。
// 入队与校验语义由 central/admin 的 RegulatoryQueue 承担并复用。
func TestRegulatoryDeliveryRoundTrip(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable central database required")
	}
	ctx := context.Background()
	conn, err := dbconn.Open(ctx, os.Getenv("TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	queue := admin.RegulatoryQueue{AdminDB: conn}
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
	}
	defer func() {
		for _, id := range ids {
			_, _ = conn.ExecContext(ctx, "DELETE FROM regulatory_report WHERE event_id = ?", id)
		}
	}()

	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "service" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/internal/regulatory-reports/claim":
			rows, err := conn.QueryContext(r.Context(), `SELECT id,event_id,object_type,object_key,CAST(payload_json AS CHAR) FROM regulatory_report
				WHERE status IN ('queued','processing') AND next_attempt_at <= ? AND (lease_until IS NULL OR lease_until < ?) ORDER BY id LIMIT 20`,
				time.Now().UTC(), time.Now().UTC())
			if err != nil {
				t.Errorf("claim select: %v", err)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			items := []map[string]any{}
			for rows.Next() {
				var id uint64
				var eventID, objectType, objectKey, payload string
				if err := rows.Scan(&id, &eventID, &objectType, &objectKey, &payload); err != nil {
					t.Errorf("claim scan: %v", err)
					continue
				}
				token := uuid.NewString()
				if _, err := conn.ExecContext(r.Context(), `UPDATE regulatory_report SET status='processing',lease_token=?,lease_until=? WHERE id=?`,
					token, time.Now().UTC().Add(2*time.Minute), id); err != nil {
					t.Errorf("claim lease: %v", err)
					continue
				}
				var raw json.RawMessage
				_ = json.Unmarshal([]byte(payload), &raw)
				items = append(items, map[string]any{"id": id, "event_id": eventID, "object_type": objectType, "object_key": objectKey, "data": raw, "lease_token": token})
			}
			rows.Close()
			writeReply(t, w, map[string]any{"code": 0, "data": map[string]any{"items": items}})
		case "/api/v1/internal/regulatory-reports/finish":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Items []struct {
					ID         uint64 `json:"id"`
					LeaseToken string `json:"lease_token"`
					Delivered  bool   `json:"delivered"`
					Mode       string `json:"mode"`
					Error      string `json:"error"`
				} `json:"items"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("finish body: %v", err)
			}
			finished := 0
			for _, item := range req.Items {
				var result sql.Result
				var err error
				if item.Delivered {
					result, err = conn.ExecContext(r.Context(), `UPDATE regulatory_report SET status='delivered',attempts=attempts+1,delivered_at=?,delivered_mode=?,last_error=NULL,lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=?`,
						time.Now().UTC(), item.Mode, item.ID, item.LeaseToken)
				} else {
					result, err = conn.ExecContext(r.Context(), `UPDATE regulatory_report SET status='queued',attempts=attempts+1,next_attempt_at=?,last_error=?,lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=?`,
						time.Now().UTC().Add(2*time.Second), item.Error, item.ID, item.LeaseToken)
				}
				if err != nil {
					t.Errorf("finish update: %v", err)
					continue
				}
				if affected, _ := result.RowsAffected(); affected == 1 {
					finished++
				}
			}
			writeReply(t, w, map[string]any{"code": 0, "data": map[string]any{"finished": finished}})
		default:
			t.Errorf("unexpected central call: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer central.Close()

	deliverer := func(sender delivery.Sender) delivery.RegulatoryDeliverer {
		return delivery.RegulatoryDeliverer{Central: serviceclient.Client{Timeout: 5 * time.Second}, CentralURL: central.URL, ServiceToken: "service", Sender: sender}
	}
	// 第一次尝试失败，事件仍留在队列里等待重试。
	count, err := deliverer(failingSender{}).RunBatch(ctx)
	if count != 0 || err == nil {
		t.Fatalf("offline delivery count=%d err=%v", count, err)
	}
	var state string
	var attempts int
	if err := conn.QueryRowContext(ctx, "SELECT status,attempts FROM regulatory_report WHERE event_id = ?", ids[0]).Scan(&state, &attempts); err != nil || state != "queued" || attempts != 1 {
		t.Fatalf("retry state=%s attempts=%d err=%v", state, attempts, err)
	}
	for _, id := range ids {
		if _, err := conn.ExecContext(ctx, "UPDATE regulatory_report SET next_attempt_at = ? WHERE event_id = ?", time.Now().UTC().Add(-time.Second), id); err != nil {
			t.Fatal(err)
		}
	}
	count, err = deliverer(delivery.SimulationSender{}).RunBatch(ctx)
	if err != nil || count != len(objects) {
		t.Fatalf("simulation count=%d err=%v", count, err)
	}
	var deliveredMode string
	if err := conn.QueryRowContext(ctx, "SELECT delivered_mode FROM regulatory_report WHERE event_id = ?", ids[0]).Scan(&deliveredMode); err != nil || deliveredMode != "simulation" {
		t.Fatalf("delivered_mode=%q err=%v", deliveredMode, err)
	}
}
