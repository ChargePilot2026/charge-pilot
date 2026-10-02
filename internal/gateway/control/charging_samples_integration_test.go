package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
)

// 往 device_event 插入一条心跳事件，返回行 id。
func insertHeartbeatEvent(t *testing.T, ctx context.Context, database *sql.DB, deviceID, eventKey string, receivedAt time.Time, ports ...protocol.PortTelemetry) uint64 {
	t.Helper()
	event := protocol.Event{DeviceID: deviceID, Type: protocol.Heartbeat, ReceivedAt: receivedAt, ChargingPorts: ports}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	result, err := database.ExecContext(ctx,
		"INSERT INTO device_event (event_key,protocol_name,device_id,event_type,port_no,event_json,received_at) VALUES (?,?,?,?,?,?,?)",
		eventKey, "dc589", deviceID, "heartbeat", 0, string(payload), receivedAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return uint64(id)
}

func TestCollectChargingSamplesPagesByCursor(t *testing.T) {
	url := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if url == "" {
		t.Skip("set disposable gateway database URL")
	}
	ctx := context.Background()
	database, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	orm, err := dbconn.WrapGORM(database)
	if err != nil {
		t.Fatal(err)
	}
	api := TelemetryAPI{DB: orm, ServiceToken: "service"}
	unique := uuid.NewString()
	deviceID := "board-" + unique
	base := time.Now().UTC().Add(-time.Hour)
	// 5 条心跳，id 递增；端口 1 功率递增，端口 2 恒零。
	ids := make([]uint64, 0, 5)
	for i := 0; i < 5; i++ {
		ids = append(ids, insertHeartbeatEvent(t, ctx, database, deviceID, fmt.Sprintf("ev-%s-%d", unique, i), base.Add(time.Duration(i)*time.Minute),
			protocol.PortTelemetry{Port: 1, PowerDeciWatts: uint32(10 * (i + 1)), ChargedMWh: uint32(1000 * (i + 1)), ChargedSeconds: uint32(60 * (i + 1))},
			protocol.PortTelemetry{Port: 2, PowerDeciWatts: 0}))
	}
	defer func() {
		for _, id := range ids {
			database.ExecContext(ctx, "DELETE FROM device_event WHERE id = ?", id)
		}
	}()

	// 第 1 页：limit=2，取最旧两条，complete=false 且给出续页游标。
	page, next, complete, err := api.collectChargingSamples(ctx, deviceID, 1, base.Add(-time.Minute), 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if complete || len(page) != 2 {
		t.Fatalf("page1 len=%d complete=%t want len=2 complete=false", len(page), complete)
	}
	if page[0].ChargingPorts[0].PowerDeciWatts != 10 || page[1].ChargingPorts[0].PowerDeciWatts != 20 {
		t.Fatalf("page1 powers=%d,%d want 10,20", page[0].ChargingPorts[0].PowerDeciWatts, page[1].ChargingPorts[0].PowerDeciWatts)
	}
	if next != ids[1] {
		t.Fatalf("nextAfterID=%d want=%d", next, ids[1])
	}

	// 第 2 页：从游标续取，端口过滤只回端口 1。
	page, next, complete, err = api.collectChargingSamples(ctx, deviceID, 1, base.Add(-time.Minute), next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if complete || len(page) != 2 || next != ids[3] {
		t.Fatalf("page2 len=%d next=%d complete=%t", len(page), next, complete)
	}
	if page[0].ChargingPorts[0].Port != 1 || len(page[0].ChargingPorts) != 1 {
		t.Fatalf("page2 sample ports=%v want only port 1", page[0].ChargingPorts)
	}

	// 第 3 页：剩余 1 条，complete=true，nextAfterID 清零。
	page, next, complete, err = api.collectChargingSamples(ctx, deviceID, 1, base.Add(-time.Minute), next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || len(page) != 1 || next != 0 {
		t.Fatalf("page3 len=%d next=%d complete=%t want len=1 next=0 complete=true", len(page), next, complete)
	}
}

// 端到端验证批量端点：一次调用取回两个设备+端口的证据，
// 且窗口起点过滤掉更早的历史心跳。
func TestChargingEvidenceBatchEndpoint(t *testing.T) {
	url := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if url == "" {
		t.Skip("set disposable gateway database URL")
	}
	ctx := context.Background()
	database, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	orm, err := dbconn.WrapGORM(database)
	if err != nil {
		t.Fatal(err)
	}
	unique := uuid.NewString()
	deviceA, deviceB := "board-a-"+unique, "board-b-"+unique
	windowStart := time.Now().UTC().Add(-30 * time.Minute)
	old := windowStart.Add(-time.Hour)
	var cleanup []uint64
	cleanup = append(cleanup,
		insertHeartbeatEvent(t, ctx, database, deviceA, "old-"+unique, old, protocol.PortTelemetry{Port: 1, PowerDeciWatts: 5}),
		insertHeartbeatEvent(t, ctx, database, deviceA, "new-a-"+unique, windowStart.Add(time.Minute), protocol.PortTelemetry{Port: 1, PowerDeciWatts: 50}),
		insertHeartbeatEvent(t, ctx, database, deviceB, "new-b-"+unique, windowStart.Add(2*time.Minute), protocol.PortTelemetry{Port: 3, PowerDeciWatts: 70}),
	)
	defer func() {
		for _, id := range cleanup {
			database.ExecContext(ctx, "DELETE FROM device_event WHERE id = ?", id)
		}
	}()

	r := httpapi.NewRouter()
	(TelemetryAPI{DB: orm, ServiceToken: "service"}).Register(r)
	body := fmt.Sprintf(`{"requests":[
		{"device_id":%q,"port_no":1,"started_at":%q},
		{"device_id":%q,"port_no":3,"started_at":%q}]}`, deviceA, windowStart.Format(time.RFC3339Nano), deviceB, windowStart.Format(time.RFC3339Nano))
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/internal/charging-evidence", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Token", "service")
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Data struct {
			Evidence []struct {
				DeviceID    string                   `json:"device_id"`
				PortNo      uint8                    `json:"port_no"`
				Samples     []chargingEvidenceSample `json:"samples"`
				Complete    bool                     `json:"complete"`
				NextAfterID uint64                   `json:"next_after_id"`
			} `json:"evidence"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Evidence) != 2 {
		t.Fatalf("evidence entries=%d want=2", len(out.Data.Evidence))
	}
	first := out.Data.Evidence[0]
	if first.DeviceID != deviceA || first.PortNo != 1 || !first.Complete || first.NextAfterID != 0 {
		t.Fatalf("entry0=%+v", first)
	}
	// 窗口起点之前的历史心跳不得进入证据。
	if len(first.Samples) != 1 || first.Samples[0].ChargingPorts[0].PowerDeciWatts != 50 {
		t.Fatalf("entry0 samples=%+v want one sample with power 50", first.Samples)
	}
	second := out.Data.Evidence[1]
	if second.DeviceID != deviceB || len(second.Samples) != 1 || second.Samples[0].ChargingPorts[0].Port != 3 {
		t.Fatalf("entry1=%+v", second)
	}
}
