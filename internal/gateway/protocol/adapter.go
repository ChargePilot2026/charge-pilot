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
	Telemetry    EventType = "telemetry"
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
	ReceivedAt     time.Time
	RawPayload     []byte
	SessionID      [6]byte
}

// Sink owns authentication and durable persistence. A protocol adapter may
// acknowledge a frame only after the corresponding sink call succeeds.
type Sink interface {
	Register(context.Context, Registration) error
	Record(context.Context, Event) error
}

// Adapter serves one connection in one vendor-specific wire format.
type Adapter interface {
	Name() string
	ServeConn(context.Context, net.Conn, Sink) error
}
