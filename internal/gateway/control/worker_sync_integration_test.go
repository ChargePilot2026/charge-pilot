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
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type workerSyncFixture struct {
	t   *testing.T
	db  *sql.DB
	ctx context.Context
}

func openWorkerSyncFixture(t *testing.T) (*workerSyncFixture, *gin.Engine) {
	t.Helper()
	if os.Getenv("TEST_GATEWAY_DATABASE_URL") == "" {
		t.Skip("disposable gateway database required")
	}
	ctx := context.Background()
	database, err := dbconn.Open(ctx, os.Getenv("TEST_GATEWAY_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	orm, err := dbconn.WrapGORM(database)
	if err != nil {
		t.Fatal(err)
	}
	api := TelemetryAPI{DB: orm, ServiceToken: "service"}
	router := httpapi.NewRouter()
	api.Register(router)
	return &workerSyncFixture{t: t, db: database, ctx: ctx}, router
}

// 语义对齐原 worker 直读：启动回执列表 + 条件推进已上报。
func TestWorkerSyncStartResultsRoundTrip(t *testing.T) {
	fx, router := openWorkerSyncFixture(t)
	device := "wss-" + uuid.NewString()
	commandID := uuid.NewString()
	fx.cleanup("charge_command", "device_id = ?", device)
	if _, err := fx.db.ExecContext(fx.ctx, `INSERT INTO charge_command(command_id,stop_command_id,charge_order_id,payment_order_id,order_no,user_id,device_id,port_no,port_code,port_id,status,result_code,ack_at)
		VALUES(?,?,'0','0','order-wss-1','0',?,'1','port-wss-1','0','acked','0',UTC_TIMESTAMP(3))`, commandID, uuid.NewString(), device); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/internal/start-results?command_id="+commandID, nil)
	req.Header.Set("X-Service-Token", "service")
	router.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), commandID) {
		t.Fatalf("start-results status=%d body=%s", w.Code, w.Body.String())
	}
	mark := func() int {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/internal/start-results/mark-reported", strings.NewReader(`{"command_ids":["`+commandID+`"]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Service-Token", "service")
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("mark status=%d body=%s", w.Code, w.Body.String())
		}
		var body struct {
			Data struct {
				Marked int `json:"marked"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data.Marked
	}
	if marked := mark(); marked != 1 {
		t.Fatalf("first mark=%d want 1", marked)
	}
	if marked := mark(); marked != 0 {
		t.Fatalf("replay mark=%d want 0", marked)
	}
}

// 语义对齐：端口释放（占用中→已释放；重复释放→already；非空闲非本单→409）。
func TestWorkerSyncPortRelease(t *testing.T) {
	fx, router := openWorkerSyncFixture(t)
	device := "wss-" + uuid.NewString()
	fx.cleanup("device_port", "device_id = ?", device)
	result, err := fx.db.ExecContext(fx.ctx, `INSERT INTO device_port(device_id,port_no,port_code,status,current_order_id) VALUES(?,'1',?,'charging','order-wss-9')`, device, "wss-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	portID, _ := result.LastInsertId()
	t.Cleanup(func() { fx.cleanup("device_port", "id = ?", portID) })

	release := func(order string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"port_id":%d,"order_no":%q}`, portID, order)
		req := httptest.NewRequest("POST", "/api/v1/internal/ports/release", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Service-Token", "service")
		router.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if code, body := release("order-wss-9"); code != 200 || !strings.Contains(body, `"released":true`) || !strings.Contains(body, `"already":false`) {
		t.Fatalf("release status=%d body=%s", code, body)
	}
	if code, body := release("order-wss-9"); code != 200 || !strings.Contains(body, `"already":true`) {
		t.Fatalf("re-release status=%d body=%s", code, body)
	}
}

// 语义对齐：结束回执冻结——首写、同事实重放（segments 可缺省）、异事实冲突。
func TestWorkerSyncFreezeEndDelivery(t *testing.T) {
	fx, router := openWorkerSyncFixture(t)
	freeze := func(eventID uint64, payload string) (int, string) {
		t.Helper()
		payloadJSON, _ := json.Marshal(payload)
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"device_event_id":%d,"charge_order_id":7,"payload":%s}`, eventID, payloadJSON)
		req := httptest.NewRequest("POST", "/api/v1/internal/charge-end-deliveries/freeze", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Service-Token", "service")
		router.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	eventID := uint64(900000001)
	fx.cleanup("charge_end_delivery", "device_event_id = ?", eventID)
	t.Cleanup(func() { fx.cleanup("charge_end_delivery", "device_event_id = ?", eventID) })
	withSegments := `{"order_no":"o-1","meter":{"charged_wh":10,"segments":[{"start":"2026-09-29T12:00:00Z"}]}}`
	if code, body := freeze(eventID, withSegments); code != 200 || !strings.Contains(body, `"frozen":false`) {
		t.Fatalf("first freeze status=%d body=%s", code, body)
	}
	withoutSegments := `{"order_no":"o-1","meter":{"charged_wh":10}}`
	if code, body := freeze(eventID, withoutSegments); code != 200 || !strings.Contains(body, `"frozen":true`) {
		t.Fatalf("replay freeze status=%d body=%s", code, body)
	}
	conflict := `{"order_no":"o-2","meter":{"charged_wh":10}}`
	if code, _ := freeze(eventID, conflict); code != 409 {
		t.Fatalf("conflict freeze status=%d want 409", code)
	}
}

// 语义对齐：刷卡决策冻结——begin 返回事件、finish 首写、重放读存量、推进 retry/done。
func TestWorkerSyncCardDecisionLifecycle(t *testing.T) {
	fx, router := openWorkerSyncFixture(t)
	device := "wss-" + uuid.NewString()
	eventKey := uuid.NewString()
	fx.cleanup("device_event", "device_id = ?", device)
	fx.cleanup("card_event_delivery", "event_key = ?", eventKey)
	event := protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.CardSwipe, EventID: eventKey, Port: 1, CardNumber: 42, ReceivedAt: time.Now().UTC()}
	raw, _ := json.Marshal(event)
	if _, err := fx.db.ExecContext(fx.ctx, `INSERT INTO device_event(event_key,protocol_name,device_id,event_type,port_no,event_json,received_at) VALUES(?,?,?,?,?,?,?)`,
		eventKey, "dc589", device, "card_swipe", 1, string(raw), event.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fx.cleanup("device_event", "device_id = ?", device) })
	if _, err := fx.db.ExecContext(fx.ctx, `INSERT INTO card_event_delivery(event_key) VALUES(?)`, eventKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fx.cleanup("card_event_delivery", "event_key = ?", eventKey) })

	call := func(path, body string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Service-Token", "service")
		router.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	code, body := call("/api/v1/internal/card-events/decide-begin", `{"event_key":"`+eventKey+`"}`)
	if code != 200 || !strings.Contains(body, `"event_json":"{`) {
		t.Fatalf("begin status=%d body=%s", code, body)
	}
	reply := `{"accepted":true,"card_number":42}`
	replyJSON, _ := json.Marshal(reply)
	code, body = call("/api/v1/internal/card-events/decide-finish", `{"event_key":"`+eventKey+`","response_json":`+string(replyJSON)+`}`)
	if code != 200 || !strings.Contains(body, `"stored":true`) {
		t.Fatalf("finish status=%d body=%s", code, body)
	}
	code, body = call("/api/v1/internal/card-events/decide-begin", `{"event_key":"`+eventKey+`"}`)
	// response_json 是 JSON 列，MySQL 规范化空白，按语义比较。
	if code != 200 || !responseFieldEquals(body, "response_json", reply) {
		t.Fatalf("frozen begin status=%d body=%s", code, body)
	}
	loserJSON, _ := json.Marshal(`{"accepted":false}`)
	code, body = call("/api/v1/internal/card-events/decide-finish", `{"event_key":"`+eventKey+`","response_json":`+string(loserJSON)+`}`)
	if code != 200 || !strings.Contains(body, `"stored":false`) || !responseFieldEquals(body, "response_json", reply) {
		t.Fatalf("race finish status=%d body=%s", code, body)
	}
	code, body = call("/api/v1/internal/card-events/advance", `{"event_key":"`+eventKey+`","outcome":"retry","error":"receiver offline"}`)
	if code != 200 || !strings.Contains(body, `"advanced":true`) {
		t.Fatalf("advance retry status=%d body=%s", code, body)
	}
	var attempts int
	var lastError string
	if err := fx.db.QueryRowContext(fx.ctx, "SELECT attempts,last_error FROM card_event_delivery WHERE event_key=?", eventKey).Scan(&attempts, &lastError); err != nil || attempts != 1 || lastError != "receiver offline" {
		t.Fatalf("attempts=%d error=%q err=%v", attempts, lastError, err)
	}
	code, body = call("/api/v1/internal/card-events/advance", `{"event_key":"`+eventKey+`","outcome":"done"}`)
	if code != 200 || !strings.Contains(body, `"advanced":true`) {
		t.Fatalf("advance done status=%d body=%s", code, body)
	}
}

// 语义对齐：结束事件列表与处理标记、计量证据窗口、端口解析。
func TestWorkerSyncEventsAndEvidence(t *testing.T) {
	fx, router := openWorkerSyncFixture(t)
	device := "wss-" + uuid.NewString()
	fx.cleanup("device_event", "device_id = ?", device)
	t.Cleanup(func() { fx.cleanup("device_event", "device_id = ?", device) })
	now := time.Now().UTC()
	insert := func(key string, eventType string, payload protocol.Event, at time.Time) uint64 {
		t.Helper()
		raw, _ := json.Marshal(payload)
		result, err := fx.db.ExecContext(fx.ctx, `INSERT INTO device_event(event_key,protocol_name,device_id,event_type,port_no,event_json,received_at) VALUES(?,?,?,?,?,?,?)`,
			key, "dc589", device, eventType, 1, string(raw), at)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return uint64(id)
	}
	endKey := uuid.NewString()
	endEvent := protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.ChargeEnd, EventID: endKey, ConsumerType: 3, OrderNumber: "12345", ReceivedAt: now.Add(-time.Minute)}
	endID := insert(endKey, "charge_end", endEvent, now.Add(-time.Minute))

	get := func(path string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Service-Token", "service")
		router.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	post := func(path, body string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Service-Token", "service")
		router.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	code, body := get("/api/v1/internal/end-events")
	if code != 200 || !strings.Contains(body, endKey) {
		t.Fatalf("end-events status=%d body=%s", code, body)
	}
	code, body = post("/api/v1/internal/device-events/mark-processed", fmt.Sprintf(`{"ids":[%d]}`, endID))
	if code != 200 || !strings.Contains(body, `"marked":1`) {
		t.Fatalf("mark status=%d body=%s", code, body)
	}
	code, body = post("/api/v1/internal/device-events/mark-processed", fmt.Sprintf(`{"ids":[%d]}`, endID))
	if code != 200 || !strings.Contains(body, `"marked":0`) {
		t.Fatalf("replay mark status=%d body=%s", code, body)
	}

	// 计量证据窗口：[ack_at, end_at] 且 id<=end_id。
	ackAt := now.Add(-time.Minute)
	window := ackAt.Add(30 * time.Second)
	outside := insert(uuid.NewString(), "heartbeat", protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, EventID: uuid.NewString(), ReceivedAt: ackAt.Add(-time.Hour)}, ackAt.Add(-time.Hour))
	hb1 := insert(uuid.NewString(), "heartbeat", protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, EventID: uuid.NewString(), ReceivedAt: window}, window)
	hb2 := insert(uuid.NewString(), "heartbeat", protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, EventID: uuid.NewString(), ReceivedAt: window.Add(time.Second)}, window.Add(time.Second))
	path := fmt.Sprintf("/api/v1/internal/devices/%s/meter-samples?ack_at=%s&end_at=%s&end_id=%d", device, ackAt.Format(time.RFC3339Nano), window.Add(2*time.Second).Format(time.RFC3339Nano), hb2)
	code, body = get(path)
	if code != 200 || strings.Contains(body, fmt.Sprintf(`"id":%d`, outside)) || !strings.Contains(body, fmt.Sprintf(`"id":%d`, hb1)) {
		t.Fatalf("meter-samples status=%d body=%s", code, body)
	}

	code, body = get("/api/v1/internal/ports/resolve?device_id=" + device + "&port_no=1")
	if code != 200 || !strings.Contains(body, `"found":false`) {
		t.Fatalf("resolve missing status=%d body=%s", code, body)
	}
	if _, err := fx.db.ExecContext(fx.ctx, `INSERT INTO device_port(device_id,port_no,port_code) VALUES(?,'1',?)`, device, "wss-port-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	code, body = get("/api/v1/internal/ports/resolve?device_id=" + device + "&port_no=1")
	if code != 200 || !strings.Contains(body, `"found":true`) {
		t.Fatalf("resolve status=%d body=%s", code, body)
	}
}

func (fx *workerSyncFixture) cleanup(table string, where string, args ...any) {
	fx.t.Helper()
	if _, err := fx.db.ExecContext(fx.ctx, fmt.Sprintf("DELETE FROM %s WHERE %s", table, where), args...); err != nil {
		fx.t.Fatal(err)
	}
}

// responseFieldEquals 比较应答 data 内某个字符串字段的 JSON 语义
// （该字段可能来自 MySQL JSON 列，空白与键序被规范化）。
func responseFieldEquals(body, field, want string) bool {
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return false
	}
	got, ok := envelope.Data[field].(string)
	if !ok {
		return false
	}
	var gotValue, wantValue any
	if json.Unmarshal([]byte(got), &gotValue) != nil || json.Unmarshal([]byte(want), &wantValue) != nil {
		return false
	}
	gotNormalized, _ := json.Marshal(gotValue)
	wantNormalized, _ := json.Marshal(wantValue)
	return string(gotNormalized) == string(wantNormalized)
}
