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
	// ConfigResult 覆盖板子对平台下发的那些设置的答复：
	// 心跳周期的确认、参数表的写入结果，以及电源控制的回复。
	// 之所以记录它们，是因为"某个设置被拒绝"是关于一台充电桩
	// 唯一一条运维从别处看不到的信息——板子会带着它原有的值继续跑，
	// 会话本身看起来完全正常。
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

// SessionRecorder 持久化一条连接的终态，好让运维能回答
// 设备何时连上、从哪来、挂了多久、搬了多少数据。
// 它与 Sink 分开，是因为会话只有在连接已经结束之后才拿得到，
// 而一个存不了会话的适配器
// 仍然必须能正常服务设备。
type SessionRecorder interface {
	RecordSession(context.Context, SessionRecord) error
}

// CloseReason 说明连接为什么结束。它是一个简短且稳定的标记，
// 因为这个值会被存下来，之后在审计查询里按它分组。
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
