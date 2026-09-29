package control

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

type fixedPaidOrder struct{ order store.PaidOrder }

func (f fixedPaidOrder) PaidOrder(context.Context, string) (store.PaidOrder, error) {
	return f.order, nil
}

type fixedActiveOrder struct{ order store.ActiveOrder }

func (f fixedActiveOrder) ActiveStop(context.Context, string, uint64) (store.ActiveOrder, error) {
	return f.order, nil
}

type recordingSession struct{ commands []protocol.Command }

func (s *recordingSession) Send(_ context.Context, command protocol.Command) error {
	s.commands = append(s.commands, command)
	return nil
}
func (s *recordingSession) Close() error { return nil }

func TestPaidStartIsDurableAndRequiresMatchedDeviceACK(t *testing.T) {
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
	vendor, err := db.ExecContext(ctx, "INSERT INTO vendor (vendor_code,vendor_name,adapter_class) VALUES (?,?,?)", "dc589-"+unique, "test vendor", "dc589")
	if err != nil {
		t.Fatal(err)
	}
	vendorID, _ := vendor.LastInsertId()
	defer db.ExecContext(ctx, "DELETE FROM vendor WHERE id = ?", vendorID)
	if _, err := db.ExecContext(ctx, "INSERT INTO device (device_id,vendor_id,port_count) VALUES (?,?,1)", deviceID, vendorID); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DELETE FROM device WHERE device_id = ?", deviceID)
	if _, err := db.ExecContext(ctx, "INSERT INTO device_port (device_id,port_no,port_code) VALUES (?,1,?)", deviceID, "port-"+unique); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DELETE FROM device_port WHERE device_id = ?", deviceID)
	defer db.ExecContext(ctx, "DELETE FROM charge_command WHERE order_no = ?", orderNo)
	defer db.ExecContext(ctx, "DELETE FROM charge_stop_command WHERE order_no = ?", orderNo)
	defer db.ExecContext(ctx, "DELETE FROM telemetry WHERE device_id = ?", deviceID)
	defer db.ExecContext(ctx, "DELETE FROM device_event WHERE device_id = ?", deviceID)
	defer db.ExecContext(ctx, "DELETE FROM event_outbox WHERE event_id IN (SELECT event_key FROM device_event WHERE device_id = ?)", deviceID)
	paid := store.PaidOrder{ChargeOrderID: chargeOrderID, PaymentOrderID: 456, OrderNo: orderNo, UserID: 789, DeviceID: deviceID, PortNo: 1, ChargeMode: 4, ChargeQuantity: 600}
	registry := &protocol.Registry{}
	session := &recordingSession{}
	detach := registry.Attach(deviceID, session, nil)
	defer detach()
	service := StartService{Orders: fixedPaidOrder{paid}, Store: store.MySQLSink{DB: testGORMDB(t, db)}, Devices: registry}
	first, err := service.Start(ctx, orderNo)
	if err != nil || first.Status != "sent" || len(session.commands) != 1 {
		t.Fatalf("first start: %+v sends=%d err=%v", first, len(session.commands), err)
	}
	second, err := service.Start(ctx, orderNo)
	if err != nil || second.CommandID != first.CommandID || len(session.commands) != 1 {
		t.Fatalf("duplicate START: %+v sends=%d err=%v", second, len(session.commands), err)
	}
	wrongSession := first.Wire.SessionID
	wrongSession[0] ^= 1
	if err := (store.MySQLSink{DB: testGORMDB(t, db)}).Record(ctx, protocol.Event{Protocol: "dc589", DeviceID: deviceID, Port: 1, Type: protocol.StartResult, SessionID: wrongSession, RawPayload: []byte{0, 1}, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	status, err := (store.MySQLSink{DB: testGORMDB(t, db)}).StartStatus(ctx, orderNo)
	if err != nil || status != "sent" {
		t.Fatalf("unmatched ACK changed state: %s %v", status, err)
	}
	if err := (store.MySQLSink{DB: testGORMDB(t, db)}).Record(ctx, protocol.Event{Protocol: "dc589", DeviceID: deviceID, Port: 1, Type: protocol.StartResult, SessionID: first.Wire.SessionID, RawPayload: []byte{0, 1}, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	status, err = (store.MySQLSink{DB: testGORMDB(t, db)}).StartStatus(ctx, orderNo)
	if err != nil || status != "acked" {
		t.Fatalf("matched ACK not applied: %s %v", status, err)
	}
	var portStatus, currentOrder string
	if err := db.QueryRowContext(ctx, "SELECT status,current_order_id FROM device_port WHERE device_id = ? AND port_no = 1", deviceID).Scan(&portStatus, &currentOrder); err != nil || portStatus != "charging" || currentOrder != orderNo {
		t.Fatalf("port=%s order=%s err=%v", portStatus, currentOrder, err)
	}
	var portID uint64
	if err := db.QueryRowContext(ctx, "SELECT id FROM device_port WHERE device_id = ? AND port_no = 1", deviceID).Scan(&portID); err != nil {
		t.Fatal(err)
	}
	stopService := UserStopService{Orders: fixedActiveOrder{store.ActiveOrder{ChargeOrderID: paid.ChargeOrderID, OrderNo: orderNo, UserID: paid.UserID, DeviceID: deviceID, PortNo: 1, PortID: portID, StartCommandID: first.CommandID}}, Store: store.MySQLSink{DB: testGORMDB(t, db)}, Devices: registry}
	userStop, err := stopService.Stop(ctx, orderNo, paid.UserID)
	if err != nil || userStop.Status != "sent" || len(session.commands) != 2 || session.commands[1].Kind != protocol.CommandStop {
		t.Fatalf("user STOP: %+v sends=%d err=%v", userStop, len(session.commands), err)
	}
	if err := (store.MySQLSink{DB: testGORMDB(t, db)}).Record(ctx, protocol.Event{Protocol: "dc589", DeviceID: deviceID, Port: 1, Type: protocol.StopResult, ResultCode: 0x10, SessionID: userStop.Wire.SessionID, RawPayload: []byte{0x10, 1}, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	userStop, err = (store.MySQLSink{DB: testGORMDB(t, db)}).ExistingUserStop(ctx, orderNo, paid.UserID)
	if err != nil || userStop.Status != "acked" {
		t.Fatalf("user stop ACK: %+v %v", userStop, err)
	}
	if err := (Compensation{Store: store.MySQLSink{DB: testGORMDB(t, db)}, Devices: registry}).Request(ctx, orderNo, first.CommandID); err != nil {
		t.Fatal(err)
	}
	if len(session.commands) != 3 || session.commands[2].Kind != protocol.CommandStop {
		t.Fatalf("STOP not dispatched after conflict: %+v", session.commands)
	}
	if err := (store.MySQLSink{DB: testGORMDB(t, db)}).Record(ctx, protocol.Event{Protocol: "dc589", DeviceID: deviceID, Port: 1, Type: protocol.StopResult, ResultCode: 0x10, SessionID: first.StopWire.SessionID, RawPayload: []byte{0x10, 1}, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	status, err = (store.MySQLSink{DB: testGORMDB(t, db)}).StartStatus(ctx, orderNo)
	if err != nil || status != "rejected" {
		t.Fatalf("STOP compensation not completed: %s %v", status, err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM device_port WHERE device_id = ? AND port_no = 1 AND status = 'idle' AND current_order_id IS NULL", deviceID).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("port not released: count=%d err=%v", remaining, err)
	}
	// The ACK is not a payment result and does not complete user-side order
	// settlement; the worker must report it back to central separately.
}
