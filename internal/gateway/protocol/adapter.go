// Package protocol defines the vendor-neutral device boundary. Each vendor
// adapter can be attached to its own TCP port without changing charge logic.
package protocol

import (
	"context"
	"net"
	"time"
)

type Registration struct {
	Protocol        string
	DeviceID        string
	HardwareVersion string
	SoftwareVersion string
	ReceivedAt      time.Time
}

type EventType string

const (
	Heartbeat    EventType = "heartbeat"
	StartResult  EventType = "start_result"
	StopResult   EventType = "stop_result"
	ChargeEnd    EventType = "charge_end"
	Fault        EventType = "fault"
	RemoteResult EventType = "remote_result"
	// ConfigResult covers the board's answer to the settings the platform sent
	// it: the heartbeat-period acknowledgement, the parameter-table write
	// result, and the power-control reply. They are recorded because a rejected
	// setting is the one thing about a pile that an operator cannot see any
	// other way — the board keeps running with the values it already had, and
	// nothing about the session looks wrong.
	ConfigResult EventType = "config_result"
	Telemetry    EventType = "telemetry"
	TimeSync     EventType = "time_sync"
)

type Event struct {
	Protocol       string
	DeviceID       string
	Type           EventType
	Port           uint8
	ResultCode     uint8
	FaultCode      uint8
	EnergyMilliKWh uint32
	PowerDeciWatts uint32
	StopReason     uint8
	ConsumerType   uint8
	ReceivedAt     time.Time
	RawPayload     []byte
	SessionID      [6]byte
	// Meter and safety fields are decoded by the vendor adapter before the
	// event is durably stored. RawPayload remains available for audit/replay.
	OrderNumber    string
	ChargedSeconds uint32
	StartedAt      time.Time
	EndedAt        time.Time
	VoltageV       uint16
	TemperatureC   int16
	DeviceStatus   uint8
	Signal         uint8
	PortStates     []uint8
	ChargingPorts  []PortTelemetry
}

type PortTelemetry struct {
	Port           uint8
	RemainingSecs  uint32
	ChargedSeconds uint32
	RemainingMWh   uint32
	ChargedMWh     uint32
	PowerDeciWatts uint32
}

// Sink owns authentication and durable persistence. A protocol adapter may
// acknowledge a frame only after the corresponding sink call succeeds.
type Sink interface {
	Register(context.Context, Registration) error
	Record(context.Context, Event) error
}

// SessionRecorder persists the terminal state of a connection so operators can
// answer when a device connected, from where, for how long and how much data it
// moved. It is separate from Sink because a session is known only once the
// connection has already ended, and an adapter that cannot persist sessions
// must still be able to serve devices.
type SessionRecorder interface {
	RecordSession(context.Context, SessionRecord) error
}

// CloseReason classifies why a connection ended. It is a short, stable token
// because the value is stored and later grouped in audit queries.
type CloseReason string

const (
	CloseDeviceClosed CloseReason = "device_closed"
	CloseReadTimeout  CloseReason = "read_timeout"
	CloseReadError    CloseReason = "read_error"
	CloseProtocol     CloseReason = "protocol_error"
	CloseRejected     CloseReason = "rejected"
	CloseReplaced     CloseReason = "replaced"
	CloseContextEnded CloseReason = "context_ended"
)

// Adapter serves one connection in one vendor-specific wire format.
type Adapter interface {
	Name() string
	ServeConn(context.Context, net.Conn, Sink) error
}
