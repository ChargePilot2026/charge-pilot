package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func chargeProcessTestTransaction(t *testing.T) *gorm.DB {
	t.Helper()
	address := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if address == "" {
		t.Skip("set a gateway MySQL URL")
	}
	db, err := dbconn.Open(t.Context(), address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tx := testGORMDB(t, db).Begin(&sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	return tx
}

func chargeProcessDevice(t *testing.T, tx *gorm.DB, device string, count int) {
	t.Helper()
	if err := tx.Exec("INSERT INTO device(device_id,vendor_id,port_count,status) VALUES(?,1,?,'disabled')", device, count).Error; err != nil {
		t.Fatal(err)
	}
}

func chargeProcessPort(t *testing.T, tx *gorm.DB, device string, port uint8, order string, commandStatus string, ack time.Time) uint64 {
	t.Helper()
	row := devicePortRow{DeviceID: device, PortNo: port, PortCode: uuid.NewString(), Status: "charging"}
	if order != "" {
		row.CurrentOrderID = sql.NullString{String: order, Valid: true}
	}
	if err := tx.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if commandStatus != "" {
		command := chargeCommandRow{CommandID: uuid.NewString(), StopCommandID: uuid.NewString(),
			ChargeOrderID: row.ID + 900000000, PaymentOrderID: row.ID + 910000000,
			OrderNo: order, UserID: 1, DeviceID: device, PortNo: port, PortCode: row.PortCode,
			PortID: sql.NullInt64{Int64: int64(row.ID), Valid: true}, Status: commandStatus,
			AckAt: sql.NullTime{Time: ack, Valid: true}}
		if err := tx.Create(&command).Error; err != nil {
			t.Fatal(err)
		}
	}
	return row.ID
}

func TestChargeProcessMatchesOnlyAcknowledgedCurrentOrdersAndReplayOnce(t *testing.T) {
	tx := chargeProcessTestTransaction(t)
	device := "process-" + uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	chargeProcessDevice(t, tx, device, 6)
	order := "order-" + uuid.NewString()
	id := chargeProcessPort(t, tx, device, 1, order, "acked", now.Add(-time.Minute))
	chargeProcessPort(t, tx, device, 2, "", "", now)
	chargeProcessPort(t, tx, device, 3, "pending-"+uuid.NewString(), "sent", now.Add(-time.Minute))
	chargeProcessPort(t, tx, device, 4, "unmatched-"+uuid.NewString(), "", now)
	chargeProcessPort(t, tx, device, 5, "new-"+uuid.NewString(), "acked", now.Add(time.Second))
	zeroOrder := "zero-" + uuid.NewString()
	zeroID := chargeProcessPort(t, tx, device, 6, zeroOrder, "acked", now.Add(-time.Minute))
	meters := []protocol.PortTelemetry{}
	for port := uint8(1); port <= 6; port++ {
		meters = append(meters, protocol.PortTelemetry{Port: port, PowerDeciWatts: 1535, ChargedMWh: 23000,
			RemainingMWh: 123000, ChargedSeconds: 65, RemainingSecs: 125})
	}
	meters[5] = protocol.PortTelemetry{Port: 6}
	e := protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, ReceivedAt: now,
		Signal: 0, DeviceStatus: 0, VoltageV: 0, TemperatureC: 0,
		PortStates: []uint8{1, 0, 1, 1, 1, 0}, ChargingPorts: meters, RawPayload: []byte{0xA4, 1}}
	sink := MySQLSink{DB: tx}
	for range 2 {
		if err := sink.Record(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	var rows []chargeProcessRow
	if err := tx.Where("device_id=?", device).Order("port_no").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].OrderNo != order || rows[0].ChargeOrderID != id+900000000 || rows[1].OrderNo != zeroOrder || rows[1].ChargeOrderID != zeroID+900000000 {
		t.Fatalf("unexpected order attribution: %+v", rows)
	}
	got := rows[0]
	if !got.TS.Equal(now) || got.PowerDeciwatts != 1535 || got.ChargedMWh != 23000 || got.RemainingMWh != 123000 || got.ChargedSeconds != 65 || got.RemainingSeconds != 125 || got.SignalStrength != 0 || got.PortStatus == nil || *got.PortStatus != 1 || got.VoltageV == nil || *got.VoltageV != 0 || got.TemperatureC == nil || *got.TemperatureC != 0 || got.DeviceStatus == nil || *got.DeviceStatus != 0 {
		t.Fatalf("missing or changed measurements: %+v", got)
	}
	if rows[1].PowerDeciwatts != 0 || rows[1].ChargedMWh != 0 || rows[1].PortStatus == nil || *rows[1].PortStatus != 0 {
		t.Fatalf("zero readings were lost: %+v", rows[1])
	}
	for table, want := range map[string]int64{"device_event": 1, "event_outbox": 1, "telemetry": 15} {
		var count int64
		query := tx.Table(table)
		if table == "event_outbox" {
			query = query.Where("event_id=?", eventKey(e))
		} else {
			query = query.Where("device_id=?", device)
		}
		if err := query.Count(&count).Error; err != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	// A later identical wire frame is a new sample, not an event replay.
	e.ReceivedAt = now.Add(15 * time.Second)
	if err := sink.Record(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := tx.Table("charge_process").Where("order_no=?", order).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("new heartbeat missing: count=%d err=%v", count, err)
	}
}

func TestHeartbeatLatestReportsPreserveShortOldAndEqualTimeEvents(t *testing.T) {
	tx := chargeProcessTestTransaction(t)
	device := "reports-" + uuid.NewString()
	chargeProcessDevice(t, tx, device, 2)
	order := "active-" + uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	portID := chargeProcessPort(t, tx, device, 1, order, "acked", now.Add(-time.Minute))
	if err := tx.Exec("INSERT INTO device_port(device_id,port_no,port_code,status) VALUES(?,2,?,'idle')", device, uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	sink := MySQLSink{DB: tx}
	base := protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, ReceivedAt: now,
		Signal: 7, PortStates: []uint8{1, 4}}
	for _, e := range []protocol.Event{base,
		{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, ReceivedAt: now.Add(time.Second), Signal: 0},
		{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, ReceivedAt: now.Add(-time.Second), Signal: 9, PortStates: []uint8{4, 0}},
		{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, ReceivedAt: now, Signal: 6, PortStates: []uint8{5, 5}},
	} {
		if err := sink.Record(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	var state struct {
		SignalStrength *uint8
		SignalAt       time.Time
		LastSeenAt     time.Time
	}
	if err := tx.Table("device").Select("signal_strength,signal_at,last_seen_at").Where("device_id=?", device).Scan(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.SignalStrength == nil || *state.SignalStrength != 0 || !state.SignalAt.Equal(now.Add(time.Second)) || !state.LastSeenAt.Equal(now.Add(time.Second)) {
		t.Fatalf("latest signal did not retain zero: %+v", state)
	}
	var ports []struct {
		Status           string
		CurrentOrderID   *string
		ReportedStatus   *uint8
		ReportedStatusAt time.Time
	}
	if err := tx.Table("device_port").Where("device_id=?", device).Order("port_no").Scan(&ports).Error; err != nil {
		t.Fatal(err)
	}
	if len(ports) != 2 || ports[0].ReportedStatus == nil || *ports[0].ReportedStatus != 1 || !ports[0].ReportedStatusAt.Equal(now) || ports[1].ReportedStatus == nil || *ports[1].ReportedStatus != 4 || ports[0].Status != "charging" || ports[0].CurrentOrderID == nil || *ports[0].CurrentOrderID != order || ports[1].Status != "idle" {
		t.Fatalf("short/stale report cleared state or changed reservation: %+v (port %d)", ports, portID)
	}
	var count int64
	if err := tx.Table("charge_process").Where("device_id=?", device).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("no charging block should not create process: %d err=%v", count, err)
	}
}

func TestChargeProcessMissingOptionalReportStaysNullAndNoSessionCreatesNone(t *testing.T) {
	tx := chargeProcessTestTransaction(t)
	device := "optional-" + uuid.NewString()
	chargeProcessDevice(t, tx, device, 1)
	now := time.Now().UTC().Truncate(time.Millisecond)
	order := "optional-" + uuid.NewString()
	portID := chargeProcessPort(t, tx, device, 1, order, "acked", now.Add(-time.Minute))
	e := protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, ReceivedAt: now,
		ChargingPorts: []protocol.PortTelemetry{{Port: 1}}}
	if err := (MySQLSink{DB: tx}).Record(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	var row chargeProcessRow
	if err := tx.Where("order_no=?", order).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.PortStatus != nil || row.VoltageV != nil || row.TemperatureC != nil || row.DeviceStatus != nil {
		t.Fatalf("missing optional block invented values: %+v", row)
	}
	if err := tx.Table("device_port").Where("id=?", portID).Updates(map[string]any{"current_order_id": nil, "status": "idle"}).Error; err != nil {
		t.Fatal(err)
	}
	e.ReceivedAt = now.Add(15 * time.Second)
	e.PortStates = []uint8{1}
	e.Signal = 4
	if err := (MySQLSink{DB: tx}).Record(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := tx.Table("charge_process").Where("device_id=?", device).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("unowned port invented a process sample: %d err=%v", count, err)
	}
	var status struct{ ReportedStatus *uint8 }
	if err := tx.Table("device_port").Where("id=?", portID).Scan(&status).Error; err != nil || status.ReportedStatus == nil || *status.ReportedStatus != 1 {
		t.Fatalf("unowned port report was discarded: %+v err=%v", status, err)
	}
}
