package control

import (
	"context"
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

// 故障扫描、处理标记与最新心跳查询的鉴权与参数边界。
func TestDeviceFaultEndpointsRequireTokenAndValidBounds(t *testing.T) {
	r := httpapi.NewRouter()
	(TelemetryAPI{ServiceToken: "service"}).Register(r)
	get := func(token, path string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		if token != "" {
			req.Header.Set("X-Service-Token", token)
		}
		r.ServeHTTP(w, req)
		return w.Code
	}
	post := func(token, body string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/internal/device-faults/mark-processed", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-Service-Token", token)
		}
		r.ServeHTTP(w, req)
		return w.Code
	}
	if get("", "/api/v1/internal/device-faults") != 401 || get("wrong", "/api/v1/internal/device-faults") != 401 {
		t.Fatal("device-faults token check failed")
	}
	if get("service", "/api/v1/internal/device-faults?limit=0") != 400 || get("service", "/api/v1/internal/device-faults?limit=101") != 400 {
		t.Fatal("device-faults limit bounds failed")
	}
	if get("service", "/api/v1/internal/devices/d1/latest-heartbeat?after=bad") != 400 {
		t.Fatal("latest-heartbeat after bounds failed")
	}
	if post("", `{"ids":[1]}`) != 401 || post("service", `{}`) != 400 || post("service", `{"ids":[]}`) != 400 {
		t.Fatal("mark-processed bounds failed")
	}
	// 全量路由注册覆盖（静态段与 :device_id 通配冲突会 panic）。
}

// 端到端：故障事件入扫描列表，标记后消失且不会二次推进；
// 最新心跳按设备与时间过滤。
func TestDeviceFaultEndpointsRoundTrip(t *testing.T) {
	url := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if url == "" {
		t.Skip("set disposable gateway database URL")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm := testGORMDB(t, db)
	unique := uuid.NewString()
	deviceID := "fault-" + unique
	at := time.Now().UTC().Add(-time.Minute)
	fault := protocol.Event{Protocol: "dc589", DeviceID: deviceID, Type: protocol.Fault, Port: 255, FaultCode: 0xaa, ReceivedAt: at}
	payload, _ := json.Marshal(fault)
	result, err := db.ExecContext(ctx,
		"INSERT INTO device_event (event_key,protocol_name,device_id,event_type,port_no,event_json,received_at) VALUES (?,?,?,?,?,?,?)",
		"fault-"+unique, "dc589", deviceID, "fault", 0, string(payload), at)
	if err != nil {
		t.Fatal(err)
	}
	faultRowID, _ := result.LastInsertId()
	defer db.ExecContext(ctx, "DELETE FROM device_event WHERE device_id = ?", deviceID)
	heartbeat := protocol.Event{Protocol: "dc589", DeviceID: deviceID, Type: protocol.Heartbeat, ReceivedAt: at.Add(30 * time.Second), DeviceStatus: 0, PortStates: []uint8{0}}
	hbPayload, _ := json.Marshal(heartbeat)
	if _, err := db.ExecContext(ctx,
		"INSERT INTO device_event (event_key,protocol_name,device_id,event_type,port_no,event_json,received_at) VALUES (?,?,?,?,?,?,?)",
		"hb-"+unique, "dc589", deviceID, "heartbeat", 0, string(hbPayload), heartbeat.ReceivedAt); err != nil {
		t.Fatal(err)
	}

	r := httpapi.NewRouter()
	(TelemetryAPI{DB: orm, ServiceToken: "service"}).Register(r)

	// 扫描：未处理故障在列。
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/internal/device-faults", nil)
	req.Header.Set("X-Service-Token", "service")
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("faults status=%d body=%s", w.Code, w.Body.String())
	}
	var list struct {
		Data struct {
			Reports []struct {
				ID       uint64 `json:"id"`
				EventKey string `json:"event_key"`
				Payload  string `json:"payload"`
			} `json:"reports"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, report := range list.Data.Reports {
		if report.ID == uint64(faultRowID) {
			found = true
			if report.EventKey != "fault-"+unique {
				t.Fatalf("event_key=%s", report.EventKey)
			}
		}
	}
	if !found {
		t.Fatal("unprocessed fault not listed")
	}

	// 标记：返回实际推进数；重复标记不再推进。
	mark := func() int64 {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/internal/device-faults/mark-processed",
			strings.NewReader(fmt.Sprintf(`{"ids":[%d]}`, faultRowID)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Service-Token", "service")
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("mark status=%d body=%s", w.Code, w.Body.String())
		}
		var out struct {
			Data struct {
				Marked int64 `json:"marked"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data.Marked
	}
	if n := mark(); n != 1 {
		t.Fatalf("first mark=%d want=1", n)
	}
	if n := mark(); n != 0 {
		t.Fatalf("second mark=%d want=0", n)
	}

	// 最新心跳：after 取故障时刻，返回恢复证据。
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/v1/internal/devices/%s/latest-heartbeat?after=%s", deviceID, at.Format(time.RFC3339Nano)), nil)
	req.Header.Set("X-Service-Token", "service")
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("heartbeat status=%d", w.Code)
	}
	var hb struct {
		Data struct {
			Found   bool   `json:"found"`
			Payload string `json:"payload"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &hb); err != nil {
		t.Fatal(err)
	}
	if !hb.Data.Found || !strings.Contains(hb.Data.Payload, "heartbeat") {
		t.Fatalf("heartbeat=%+v", hb.Data)
	}

	// after 晚于所有心跳：found=false。
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", fmt.Sprintf("/api/v1/internal/devices/%s/latest-heartbeat?after=%s", deviceID, time.Now().UTC().Format(time.RFC3339Nano)), nil)
	req.Header.Set("X-Service-Token", "service")
	r.ServeHTTP(w, req)
	var none struct {
		Data struct {
			Found bool `json:"found"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &none); err != nil {
		t.Fatal(err)
	}
	if none.Data.Found {
		t.Fatal("heartbeat after now must be found=false")
	}
}
