package alerts

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/google/uuid"
)

func TestDeviceSmokeAlertWithoutRulesRecoversAndRaisesAgain(t *testing.T) {
	if os.Getenv("TEST_GATEWAY_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	gatewayDB, adminDB := openAlertsDB(t, "TEST_GATEWAY_DATABASE_URL"), openAlertsDB(t, "TEST_ADMIN_DATABASE_URL")
	device := "smoke-" + uuid.NewString()
	t.Cleanup(func() {
		gatewayDB.Exec("DELETE FROM event_outbox WHERE envelope_json LIKE ?", "%"+device+"%")
		gatewayDB.Exec("DELETE FROM device_event WHERE device_id = ?", device)
		gatewayDB.Exec("DELETE FROM telemetry WHERE device_id = ?", device)
		gatewayDB.Exec("DELETE FROM telemetry_aggregate_15min WHERE device_id = ?", device)
		gatewayDB.Exec("DELETE FROM telemetry_aggregate_hourly WHERE device_id = ?", device)
		adminDB.Exec("DELETE FROM alert_event WHERE device_id = ?", device)
	})
	sink := store.MySQLSink{DB: gatewayDB}
	syncer := DeviceSynchronizer{GatewayDB: gatewayDB, AdminDB: adminDB}
	at := time.Now().UTC().Truncate(time.Millisecond)
	fault := protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.Fault, Port: 255, FaultCode: 0xbb, RawPayload: []byte{255, 187, 0, 0, 0}, ReceivedAt: at}
	record := func(event protocol.Event) {
		t.Helper()
		if err := sink.Record(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	run := func() {
		t.Helper()
		if _, err := syncer.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	check := func(count int64, status string) {
		t.Helper()
		var n int64
		if err := adminDB.Table("alert_event").Where("device_id = ? AND metric = 'smoke' AND rule_id IS NULL", device).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		var row deviceAlert
		if err := adminDB.Table("alert_event").Where("device_id = ?", device).Order("id DESC").Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		if n != count || row.Status != status {
			t.Fatalf("alerts=%d status=%s, want %d/%s", n, row.Status, count, status)
		}
	}
	record(fault)
	run()
	check(1, "active")
	adminDB.Table("alert_event").Where("device_id = ?", device).Update("status", "acknowledged")
	fault.ReceivedAt = at.Add(time.Second)
	record(fault)
	run()
	check(1, "acknowledged")
	// 早于最新故障的正常心跳不能关闭告警。
	heartbeat := protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, ReceivedAt: at.Add(500 * time.Millisecond), PortStates: []uint8{0, 0}}
	record(heartbeat)
	run()
	check(1, "acknowledged")
	// 无安全状态的信号心跳也不能当作恢复。
	heartbeat.ReceivedAt, heartbeat.PortStates = at.Add(2*time.Second), nil
	record(heartbeat)
	run()
	check(1, "acknowledged")
	heartbeat.ReceivedAt, heartbeat.PortStates, heartbeat.DeviceStatus = at.Add(3*time.Second), []uint8{0, 0}, 2
	record(heartbeat)
	run()
	check(1, "acknowledged")
	heartbeat.ReceivedAt, heartbeat.DeviceStatus = at.Add(4*time.Second), 0
	record(heartbeat)
	run()
	check(1, "auto_resolved")
	// 相同 C0 报文在恢复后再次上报，必须产生新告警；同一次存储重试仍幂等。
	fault.ReceivedAt = at.Add(5 * time.Second)
	record(fault)
	record(fault)
	run()
	run()
	check(2, "active")
}

func TestFaultMetricAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		port, code       uint8
		metric, severity string
	}{
		{255, 187, "smoke", "fatal"},
		{255, 170, "high_temperature", "critical"},
		{255, 53, "device_fault", "critical"},
		{2, 4, "port_fault_2", "critical"},
	} {
		metric, severity, _ := faultMetric(protocol.Event{Port: tc.port, FaultCode: tc.code})
		if metric != tc.metric || severity != tc.severity {
			t.Fatalf("fault %d/%d mapped to %s/%s", tc.port, tc.code, metric, severity)
		}
	}
	heartbeat := protocol.Event{DeviceStatus: 2, PortStates: []uint8{3, 0}}
	if faultRecovered("smoke", heartbeat) || faultRecovered("port_fault_1", heartbeat) || !faultRecovered("port_fault_2", heartbeat) || faultRecovered("port_fault_3", heartbeat) {
		t.Fatal("recovery does not respect board/individual port state")
	}
}
