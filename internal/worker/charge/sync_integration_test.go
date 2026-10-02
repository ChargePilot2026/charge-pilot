package charge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/google/uuid"
)

// TestStartResultRetriesUntilCentralPersists 验证第 5 批⑤之后的契约：
// worker 经 gateway 内部端点取回执清单，central 失败时不做上报标记，
// central 确认后才批量 mark-reported。
func TestStartResultRetriesUntilCentralPersists(t *testing.T) {
	ctx := context.Background()
	commandID, orderNo := uuid.NewString(), "ORD-"+uuid.NewString()
	ackAt := time.Now().UTC().Truncate(time.Second)

	var mu sync.Mutex
	var marked []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "test-service-token" {
			t.Errorf("missing gateway authentication")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/internal/start-results":
			if r.URL.Query().Get("command_id") != commandID {
				t.Errorf("unexpected command filter: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []map[string]any{{
				"command_id": commandID, "stop_command_id": uuid.NewString(),
				"charge_order_id": 9001, "order_no": orderNo, "device_id": "TEST-BOARD",
				"port_no": 1, "port_id": 55, "status": "acked", "result_code": 0,
				"ack_at": ackAt.Format(time.RFC3339Nano),
			}}}})
		case "/api/v1/internal/start-results/mark-reported":
			var body struct {
				CommandIDs []string `json:"command_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("bad mark body: %v", err)
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			marked = append(marked, body.CommandIDs...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"marked": len(body.CommandIDs)}})
		default:
			t.Errorf("unexpected gateway path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer gateway.Close()

	var centralCalls int
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "test-service-token" || r.URL.Path != "/api/v1/internal/charge-orders/"+orderNo+"/start-result" {
			t.Errorf("bad central request: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var body startResult
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.CommandID != commandID || !body.Success || body.PortID != 55 {
			t.Errorf("bad central body: %+v %v", body, err)
			w.WriteHeader(400)
			return
		}
		centralCalls++
		if centralCalls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer central.Close()

	syncer := Synchronizer{
		Gateway:       serviceclient.Client{HTTP: gateway.Client()},
		GatewayURL:    gateway.URL,
		CentralURL:    central.URL,
		ServiceToken:  "test-service-token",
		CommandFilter: commandID,
	}
	if count, err := syncer.SyncBatch(ctx); count != 0 || err == nil {
		t.Fatalf("failed central call reported: %d %v", count, err)
	}
	mu.Lock()
	if len(marked) != 0 {
		t.Fatalf("failed result marked reported: %v", marked)
	}
	mu.Unlock()
	if count, err := syncer.SyncBatch(ctx); count != 1 || err != nil {
		t.Fatalf("retry did not persist: %d %v", count, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(marked) != 1 || marked[0] != commandID {
		t.Fatalf("successful result not marked reported: %v", marked)
	}
}
