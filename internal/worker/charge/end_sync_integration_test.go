package charge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
)

// TestEndKeepsPortOwnedUntilCentralAcceptsMeter 验证第 5 批⑤之后的契约：
// freeze 先于 central 提交；central 未确认时不释放端口；
// 二次重放时冻结回执保持首写内容（即使计量证据已消失）。
func TestEndKeepsPortOwnedUntilCentralAcceptsMeter(t *testing.T) {
	ctx := context.Background()
	const (
		eventID       = uint64(42)
		orderID       = uint64(9001)
		portID        = int64(55)
		heartbeatID   = uint64(7)
		chargedWh     = uint32(125)
		segmentFirst  = uint32(50)
		segmentSecond = uint32(75)
	)
	orderNo := "ORD-" + uuid.NewString()
	deviceID := "board-" + uuid.NewString()
	start := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)

	end := protocol.Event{Protocol: "dc589", DeviceID: deviceID, Port: 1, Type: protocol.ChargeEnd,
		OrderNumber: fmt.Sprintf("%016d", orderID), ConsumerType: 2,
		EnergyMilliKWh: chargedWh, ChargedSeconds: 600,
		StartedAt: start, EndedAt: start.Add(10 * time.Minute), ReceivedAt: start.Add(10 * time.Minute)}
	endJSON, _ := json.Marshal(end)
	heartbeat := protocol.Event{Protocol: "dc589", DeviceID: deviceID, Type: protocol.Heartbeat,
		ReceivedAt:    start.Add(5 * time.Minute),
		ChargingPorts: []protocol.PortTelemetry{{Port: 1, ChargedSeconds: 300, ChargedMWh: 50000}}}
	heartbeatJSON, _ := json.Marshal(heartbeat)

	var mu sync.Mutex
	var order []string
	evidenceGone := false
	releases := 0
	var processed []uint64
	frozenPayload := ""
	frozenKey := ""
	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "test-token" {
			t.Errorf("missing gateway authentication")
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/internal/end-events":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []map[string]any{{"id": eventID, "payload": string(endJSON)}}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/internal/charge-commands/by-order/9001":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"found": true, "command": map[string]any{
				"command_id": uuid.NewString(), "stop_command_id": uuid.NewString(),
				"charge_order_id": orderID, "order_no": orderNo, "device_id": deviceID,
				"port_no": 1, "port_id": portID, "status": "acked", "result_code": 0,
				"ack_at": start.Format(time.RFC3339Nano),
			}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/internal/devices/"+deviceID+"/meter-samples":
			items := []map[string]any{}
			mu.Lock()
			gone := evidenceGone
			mu.Unlock()
			if !gone {
				items = append(items, map[string]any{"id": heartbeatID, "payload": string(heartbeatJSON)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": items}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/internal/charge-end-deliveries/freeze":
			record("freeze")
			var body struct {
				DeviceEventID uint64 `json:"device_event_id"`
				ChargeOrderID uint64 `json:"charge_order_id"`
				Payload       string `json:"payload"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DeviceEventID != eventID || body.ChargeOrderID != orderID {
				t.Errorf("bad freeze body: %+v %v", body, err)
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			key := fmt.Sprintf("%d/%d", body.DeviceEventID, body.ChargeOrderID)
			if frozenKey == "" {
				frozenKey, frozenPayload = key, body.Payload
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"frozen": false, "payload": body.Payload}})
				return
			}
			if key != frozenKey {
				w.WriteHeader(409)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"frozen": true, "payload": frozenPayload}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/internal/ports/release":
			record("release")
			var body struct {
				PortID  uint64 `json:"port_id"`
				OrderNo string `json:"order_no"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PortID != uint64(portID) || body.OrderNo != orderNo {
				t.Errorf("bad release body: %+v %v", body, err)
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			releases++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"released": true}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/internal/device-events/mark-processed":
			record("mark-processed")
			var body struct {
				IDs []uint64 `json:"ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("bad mark body: %v", err)
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			processed = append(processed, body.IDs...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"marked": len(body.IDs)}})
		default:
			t.Errorf("unexpected gateway request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer gateway.Close()

	var centralCalls int
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("central")
		if r.Header.Get("X-Service-Token") != "test-token" || r.URL.Path != "/api/v1/internal/charge-orders/"+orderNo+"/end-result" {
			t.Errorf("bad central request: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var result endResult
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil ||
			result.Meter.ChargedWh != chargedWh || result.PortID != uint64(portID) ||
			len(result.Meter.Segments) != 2 ||
			result.Meter.Segments[0].EnergyWh != segmentFirst ||
			result.Meter.Segments[1].EnergyWh != segmentSecond {
			t.Errorf("bad meter: %+v %v", result.Meter, err)
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

	syncer := EndSynchronizer{
		Gateway:      serviceclient.Client{HTTP: gateway.Client()},
		GatewayURL:   gateway.URL,
		CentralURL:   central.URL,
		ServiceToken: "test-token",
	}
	if count, err := syncer.SyncBatch(ctx); count != 0 || err == nil {
		t.Fatalf("failed end accepted: %d %v", count, err)
	}
	mu.Lock()
	if releases != 0 {
		t.Fatalf("port released before central accepted: %d", releases)
	}
	mu.Unlock()

	// 计量证据消失后重试：冻结回执仍保持首写内容，端口随后释放。
	mu.Lock()
	evidenceGone = true
	mu.Unlock()
	if count, err := syncer.SyncBatch(ctx); count != 1 || err != nil {
		t.Fatalf("retry end: %d %v", count, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if releases != 1 {
		t.Fatalf("port not released exactly once: %d", releases)
	}
	if len(processed) != 1 || processed[0] != eventID {
		t.Fatalf("end event not marked processed: %v", processed)
	}
	want := []string{"freeze", "central", "freeze", "central", "release", "mark-processed"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("unexpected call order: %v", order)
	}
}
