// Package protocol 定义与厂商无关的设备边界。每家厂商的适配器
// 都可以挂到各自的 TCP 端口上，而不用改动充电逻辑。
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
	// ConfigResult 表示心跳、参数表及电源控制的设备响应，包含拒绝原因，供运维确认配置是否生效。
	ConfigResult     EventType = "config_result"
	Telemetry        EventType = "telemetry"
	TimeSync         EventType = "time_sync"
	CardSwipe        EventType = "card_swipe"
	CardBalanceQuery EventType = "card_balance_query"
)

type Event struct {
	EventID        string
	CardNumber     uint32
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
	// 电表与安全相关字段由厂商适配器在事件被持久化之前解出来。
	// RawPayload 仍然保留，供审计与重放使用。
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

// Sink 负责鉴权与持久化。协议适配器只有在对应的 sink 调用
// 成功之后，才可以确认（ack）一个帧。
type Sink interface {
	Register(context.Context, Registration) error
	Record(context.Context, Event) error
}

// SessionRecorder 在连接结束后持久化时间、地址及流量统计。
// 与 Sink 独立，未实现会话存储的适配器仍可处理设备事件。
type SessionRecorder interface {
	RecordSession(context.Context, SessionRecord) error
}

// CloseReason 表示持久化的连接结束原因，使用稳定标记供审计分组。
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

// Adapter 用某一种厂商私有的线格式服务一条连接。
type Adapter interface {
	Name() string
	ServeConn(context.Context, net.Conn, Sink) error
}
