package charge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestEndKeepsPortOwnedUntilCentralAcceptsMeter(t *testing.T) {
	for _, consumer := range []uint8{2, 3} {
		t.Run(fmt.Sprint(consumer), func(t *testing.T) { testEndKeepsPortOwned(t, consumer) })
	}
}

func testEndKeepsPortOwned(t *testing.T, consumer uint8) {
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
	unique := uuid.NewString()
	chargeOrderID := uint64(100000000000 + time.Now().UnixNano()%900000000000)
	deviceID, orderNo := "board-"+unique, "ORD-"+unique
	port, err := db.ExecContext(ctx, "INSERT INTO device_port (device_id,port_no,port_code,status,current_order_id) VALUES (?,1,?,'charging',?)", deviceID, "port-"+unique, orderNo)
	if err != nil {
		t.Fatal(err)
	}
	portID, _ := port.LastInsertId()
	defer db.ExecContext(ctx, "DELETE FROM device_port WHERE id = ?", portID)
	commandID, stopID := uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO charge_command (command_id,stop_command_id,charge_order_id,payment_order_id,order_no,user_id,device_id,port_no,port_code,port_id,owns_port,status,session_id,stop_session_id,result_reported)
		VALUES (?,?,?,456,?,789,?,1,?,?,TRUE,'acked','010203040506','0708090a0b0c',TRUE)`, commandID, stopID, chargeOrderID, orderNo, deviceID, "port-"+unique, portID); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DELETE FROM charge_command WHERE command_id = ?", commandID)
	defer db.ExecContext(ctx, "DELETE FROM charge_end_delivery WHERE charge_order_id=?", chargeOrderID)
	start := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)
	if _, err := db.ExecContext(ctx, "UPDATE charge_command SET ack_at=? WHERE command_id=?", start, commandID); err != nil {
		t.Fatal(err)
	}
	heartbeat := protocol.Event{Protocol: "dc589", DeviceID: deviceID, Type: protocol.Heartbeat, ReceivedAt: start.Add(5 * time.Minute), ChargingPorts: []protocol.PortTelemetry{{Port: 1, ChargedSeconds: 300, ChargedMWh: 50000}}}
	heartbeatJSON, _ := json.Marshal(heartbeat)
	if _, err := db.ExecContext(ctx, "INSERT INTO device_event(event_key,protocol_name,device_id,event_type,event_json,received_at) VALUES(?,'dc589',?,'heartbeat',?,?)", uuid.NewString(), deviceID, heartbeatJSON, heartbeat.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DELETE FROM device_event WHERE device_id=?", deviceID)
	eventKey := uuid.NewString()
	event := protocol.Event{Protocol: "dc589", DeviceID: deviceID, Port: 1, Type: protocol.ChargeEnd, OrderNumber: fmt.Sprintf("%016d", chargeOrderID), ConsumerType: consumer, EnergyMilliKWh: 125, ChargedSeconds: 600, StartedAt: start, EndedAt: start.Add(10 * time.Minute), ReceivedAt: start.Add(10 * time.Minute)}
	data, _ := json.Marshal(event)
	if _, err := db.ExecContext(ctx, "INSERT INTO device_event (event_key,protocol_name,device_id,event_type,port_no,event_json,received_at) VALUES (?,'dc589',?,'charge_end',1,?,?)", eventKey, deviceID, data, event.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DELETE FROM device_event WHERE event_key = ?", eventKey)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "test-token" || r.URL.Path != "/api/v1/internal/charge-orders/"+orderNo+"/end-result" {
			t.Errorf("bad request: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var result endResult
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil || result.Meter.ChargedWh != 125 || result.PortID != uint64(portID) || len(result.Meter.Segments) != 2 || result.Meter.Segments[0].EnergyWh != 50 || result.Meter.Segments[1].EnergyWh != 75 {
			t.Errorf("bad meter: %+v %v", result, err)
			w.WriteHeader(400)
			return
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer server.Close()
	syncer := EndSynchronizer{GatewayDB: testGORMDB(t, db), CentralURL: server.URL, ServiceToken: "test-token"}
	if count, err := syncer.SyncBatch(ctx); count != 0 || err == nil {
		t.Fatalf("failed end accepted: %d %v", count, err)
	}
	var state string
	if err := db.QueryRowContext(ctx, "SELECT status FROM device_port WHERE id = ?", portID).Scan(&state); err != nil || state != "charging" {
		t.Fatalf("port released early: %s %v", state, err)
	}
	// 删除原始计量事件后，持久化的结束请求仍应保持不变。
	if _, err := db.ExecContext(ctx, "DELETE FROM device_event WHERE device_id=? AND event_type='heartbeat'", deviceID); err != nil {
		t.Fatal(err)
	}
	// 一条接收时间戳更早的迟到上报，
	// 也不该改变用于重试的数据。
	heartbeat.ChargingPorts[0].ChargedMWh = 60000
	heartbeatJSON, _ = json.Marshal(heartbeat)
	if _, err := db.ExecContext(ctx, "INSERT INTO device_event(event_key,protocol_name,device_id,event_type,event_json,received_at) VALUES(?,'dc589',?,'heartbeat',?,?)", uuid.NewString(), deviceID, heartbeatJSON, heartbeat.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	if count, err := syncer.SyncBatch(ctx); count != 1 || err != nil {
		t.Fatalf("retry end: %d %v", count, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT status FROM device_port WHERE id = ?", portID).Scan(&state); err != nil || state != "idle" {
		t.Fatalf("port not released: %s %v", state, err)
	}
}
