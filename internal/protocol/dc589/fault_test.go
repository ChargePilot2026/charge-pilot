package dc589

import (
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

func TestFaultMetricNamesDeviceAndPortFaults(t *testing.T) {
	metric, severity, note := FaultMetric(protocol.Event{Port: 255, FaultCode: 0xbb})
	if metric != "smoke" || severity != "fatal" || note == "" {
		t.Fatalf("smoke: %s %s %s", metric, severity, note)
	}
	metric, severity, _ = FaultMetric(protocol.Event{Port: 255, FaultCode: 0xaa})
	if metric != "high_temperature" || severity != "critical" {
		t.Fatalf("temperature: %s %s", metric, severity)
	}
	metric, severity, _ = FaultMetric(protocol.Event{Port: 255, FaultCode: 0x11})
	if metric != "device_fault" || severity != "critical" {
		t.Fatalf("device fault: %s %s", metric, severity)
	}
	metric, severity, _ = FaultMetric(protocol.Event{Port: 3, FaultCode: 0x22})
	if metric != "port_fault_3" || severity != "critical" {
		t.Fatalf("port fault: %s %s", metric, severity)
	}
}

func TestFaultRecoveredRequiresFreshSafeHeartbeat(t *testing.T) {
	if FaultRecovered("smoke", protocol.Event{DeviceStatus: 0, PortStates: []uint8{0}}) != true {
		t.Fatal("safe device status must recover smoke")
	}
	if FaultRecovered("smoke", protocol.Event{DeviceStatus: 2, PortStates: []uint8{0}}) {
		t.Fatal("unsafe device status must not recover")
	}
	if FaultRecovered("port_fault_2", protocol.Event{PortStates: []uint8{0, 1}}) != true {
		t.Fatal("port state <= 2 must recover")
	}
	if FaultRecovered("port_fault_2", protocol.Event{PortStates: []uint8{0, 3}}) {
		t.Fatal("port state > 2 must not recover")
	}
	if FaultRecovered("port_fault_9", protocol.Event{PortStates: []uint8{0, 1}}) {
		t.Fatal("unknown port must not recover")
	}
	if FaultRecovered("port_fault_1", protocol.Event{}) {
		t.Fatal("signal-only heartbeat must not recover")
	}
}
