package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
)

// mockFaultGateway 在内存里模拟 gateway 的故障扫描、处理标记与心跳查询端点。
type mockFaultGateway struct {
	mu        sync.Mutex
	events    []mockStoredEvent
	nextID    uint64
	markedOps []uint64
}

type mockStoredEvent struct {
	id         uint64
	eventKey   string
	deviceID   string
	receivedAt time.Time
	event      protocol.Event
	processed  bool
}

func newMockFaultGateway() *mockFaultGateway {
	return &mockFaultGateway{nextID: 1}
}

func (m *mockFaultGateway) add(event protocol.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	m.events = append(m.events, mockStoredEvent{
		id: m.nextID, eventKey: "key-" + uuid.NewString(), deviceID: event.DeviceID,
		receivedAt: event.ReceivedAt, event: event,
	})
}

func (m *mockFaultGateway) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		const devicePrefix = "/api/v1/internal/devices/"
		if r.URL.Path != "/api/v1/internal/device-faults" && r.URL.Path != "/api/v1/internal/device-faults/mark-processed" && len(r.URL.Path) > len(devicePrefix) && r.URL.Path[:len(devicePrefix)] != devicePrefix {
			t.Errorf("unexpected gateway call: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/api/v1/internal/device-faults":
			reports := []map[string]any{}
			for _, e := range m.events {
				if e.processed || e.event.Type != protocol.Fault {
					continue
				}
				payload, _ := json.Marshal(e.event)
				reports = append(reports, map[string]any{"id": e.id, "event_key": e.eventKey, "received_at": e.receivedAt, "payload": string(payload)})
			}
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"reports": reports}})
		case "/api/v1/internal/device-faults/mark-processed":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				IDs []uint64 `json:"ids"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("mark-processed body: %v", err)
			}
			marked := int64(0)
			for i := range m.events {
				for _, id := range req.IDs {
					if m.events[i].id == id && !m.events[i].processed {
						m.events[i].processed = true
						marked++
					}
				}
			}
			m.markedOps = append(m.markedOps, req.IDs...)
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"marked": marked}})
		default:
			rest := strings.TrimPrefix(r.URL.Path, devicePrefix)
			device, foundSuffix := strings.CutSuffix(rest, "/latest-heartbeat")
			if !foundSuffix {
				t.Errorf("unexpected gateway call: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var after time.Time
			if raw := r.URL.Query().Get("after"); raw != "" {
				parsed, err := time.Parse(time.RFC3339Nano, raw)
				if err != nil {
					t.Errorf("after=%q: %v", raw, err)
				}
				after = parsed
			}
			if key := r.URL.Query().Get("after_key"); key != "" {
				matched := false
				for _, e := range m.events {
					if e.deviceID == device && e.eventKey == key {
						after = e.receivedAt
						matched = true
						break
					}
				}
				if !matched {
					writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"found": false}})
					return
				}
			}
			var latest *mockStoredEvent
			for i := range m.events {
				e := &m.events[i]
				if e.deviceID == device && e.event.Type == protocol.Heartbeat && e.receivedAt.After(after) {
					if latest == nil || e.receivedAt.After(latest.receivedAt) {
						latest = e
					}
				}
			}
			if latest == nil {
				writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"found": false}})
				return
			}
			payload, _ := json.Marshal(latest.event)
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"found": true, "received_at": latest.receivedAt, "payload": string(payload)}})
		}
	}))
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

// 平移原 worker/alerts 场景：烟雾告警上报、持续故障只保留一条、
// 陈旧/无效心跳不能恢复、安全心跳自动恢复；处理标记只推进一次。
func TestDeviceSmokeAlertRecoversAndRaisesAgain(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable central database required")
	}
	ctx := context.Background()
	conn, err := dbconn.Open(ctx, os.Getenv("TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db, err := dbconn.WrapGORM(conn)
	if err != nil {
		t.Fatal(err)
	}

	device := "smoke-" + uuid.NewString()
	t.Cleanup(func() {
		conn.ExecContext(ctx, "DELETE FROM alert_event WHERE device_id = ?", device)
	})
	mock := newMockFaultGateway()
	gateway := mock.server(t)
	defer gateway.Close()
	syncer := DeviceAlertSync{AdminDB: db, Gateway: serviceclient.Client{Timeout: 5 * time.Second}, GatewayURL: gateway.URL, ServiceToken: "service"}

	at := time.Now().UTC().Truncate(time.Millisecond)
	fault := protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.Fault, Port: 255, FaultCode: 0xbb, ReceivedAt: at}
	run := func() {
		t.Helper()
		if _, err := syncer.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	check := func(count int64, status string) {
		t.Helper()
		var n int64
		if err := db.Table("alert_event").Where("device_id = ? AND metric = 'smoke' AND rule_id IS NULL", device).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		var row deviceAlert
		if err := db.Table("alert_event").Where("device_id = ?", device).Order("id DESC").Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		if n != count || row.Status != status {
			t.Fatalf("alerts=%d status=%s, want %d/%s", n, row.Status, count, status)
		}
	}

	mock.add(fault)
	run()
	check(1, "active")
	markedAfterFirst := len(mock.markedOps)
	if markedAfterFirst != 1 {
		t.Fatalf("marked calls=%d want 1", markedAfterFirst)
	}

	db.Table("alert_event").Where("device_id = ?", device).Update("status", "acknowledged")
	fault.ReceivedAt = at.Add(time.Second)
	mock.add(fault)
	run()
	check(1, "acknowledged")

	// 早于最新故障的正常心跳不能关闭告警。
	heartbeat := protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, ReceivedAt: at.Add(500 * time.Millisecond), PortStates: []uint8{0, 0}}
	mock.add(heartbeat)
	run()
	check(1, "acknowledged")
	// 无安全状态的信号心跳也不能当作恢复。
	heartbeat.ReceivedAt, heartbeat.PortStates = at.Add(2*time.Second), nil
	mock.add(heartbeat)
	run()
	check(1, "acknowledged")
	heartbeat.ReceivedAt, heartbeat.PortStates, heartbeat.DeviceStatus = at.Add(3*time.Second), []uint8{0, 0}, 2
	mock.add(heartbeat)
	run()
	check(1, "acknowledged")
	// 故障之后且设备状态安全的心跳触发自动恢复。
	heartbeat.ReceivedAt, heartbeat.DeviceStatus = at.Add(4*time.Second), 0
	mock.add(heartbeat)
	run()
	check(1, "auto_resolved")

	// 恢复后的新故障重新建一条告警，且处理标记不重复推进旧事件。
	fault.ReceivedAt = at.Add(5 * time.Second)
	mock.add(fault)
	run()
	check(2, "active")
	if len(mock.markedOps) != 3 {
		t.Fatalf("total marked calls=%d want 3", len(mock.markedOps))
	}
}

// 同步端点要求服务令牌。
func TestDeviceAlertSyncAPIRequiresServiceToken(t *testing.T) {
	router := httpapi.NewRouter()
	(DeviceAlertSyncAPI{ServiceToken: "service"}).Register(router)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/internal/device-alerts/sync", nil)
	router.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("status=%d want=401", w.Code)
	}
}
