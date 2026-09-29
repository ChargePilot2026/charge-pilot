package store

import (
	"database/sql"
	"time"
)

type deviceRow struct {
	ID uint64 `gorm:"column:id;primaryKey"`
}

func (deviceRow) TableName() string { return "device" }

type devicePortRow struct {
	ID             uint64         `gorm:"column:id;primaryKey"`
	DeviceID       string         `gorm:"column:device_id"`
	PortNo         uint8          `gorm:"column:port_no"`
	PortCode       string         `gorm:"column:port_code"`
	Status         string         `gorm:"column:status"`
	CurrentOrderID sql.NullString `gorm:"column:current_order_id"`
}

func (devicePortRow) TableName() string { return "device_port" }

type chargeCommandRow struct {
	CommandID      string         `gorm:"column:command_id;primaryKey"`
	StopCommandID  string         `gorm:"column:stop_command_id"`
	ChargeOrderID  uint64         `gorm:"column:charge_order_id"`
	PaymentOrderID uint64         `gorm:"column:payment_order_id"`
	OrderNo        string         `gorm:"column:order_no"`
	UserID         uint64         `gorm:"column:user_id"`
	DeviceID       string         `gorm:"column:device_id"`
	PortNo         uint8          `gorm:"column:port_no"`
	PortCode       string         `gorm:"column:port_code"`
	PortID         sql.NullInt64  `gorm:"column:port_id"`
	OwnsPort       bool           `gorm:"column:owns_port"`
	Status         string         `gorm:"column:status"`
	SessionID      sql.NullString `gorm:"column:session_id"`
	StopSessionID  sql.NullString `gorm:"column:stop_session_id"`
	ResultReported bool           `gorm:"column:result_reported"`
	ResultCode     sql.NullInt16  `gorm:"column:result_code"`
	AckAt          sql.NullTime   `gorm:"column:ack_at"`
	ChargeMode     uint8          `gorm:"column:charge_mode"`
	Quantity       uint16         `gorm:"column:quantity"`
}

func (chargeCommandRow) TableName() string { return "charge_command" }

type chargeStopCommandRow struct {
	CommandID      string         `gorm:"column:command_id;primaryKey"`
	StartCommandID string         `gorm:"column:start_command_id"`
	ChargeOrderID  uint64         `gorm:"column:charge_order_id"`
	OrderNo        string         `gorm:"column:order_no"`
	UserID         uint64         `gorm:"column:user_id"`
	DeviceID       string         `gorm:"column:device_id"`
	PortNo         uint8          `gorm:"column:port_no"`
	PortID         sql.NullInt64  `gorm:"column:port_id"`
	Status         string         `gorm:"column:status"`
	SessionID      sql.NullString `gorm:"column:session_id"`
	ResultCode     sql.NullInt16  `gorm:"column:result_code"`
	RejectedAt     sql.NullTime   `gorm:"column:rejected_at"`
}

func (chargeStopCommandRow) TableName() string { return "charge_stop_command" }

type deviceEventRow struct {
	ID          uint64       `gorm:"column:id;primaryKey"`
	EventKey    string       `gorm:"column:event_key"`
	Protocol    string       `gorm:"column:protocol_name"`
	DeviceID    string       `gorm:"column:device_id"`
	EventType   string       `gorm:"column:event_type"`
	PortNo      uint8        `gorm:"column:port_no"`
	EventJSON   []byte       `gorm:"column:event_json"`
	ProcessedAt sql.NullTime `gorm:"column:processed_at"`
	ReceivedAt  time.Time    `gorm:"column:received_at"`
}

func (deviceEventRow) TableName() string { return "device_event" }

type deviceOutboxRow struct {
	EventID      string `gorm:"column:event_id;primaryKey"`
	Stream       string `gorm:"column:stream"`
	EnvelopeJSON []byte `gorm:"column:envelope_json"`
}

func (deviceOutboxRow) TableName() string { return "event_outbox" }

type telemetryRow struct {
	DeviceID string        `gorm:"column:device_id"`
	PortNo   sql.NullInt16 `gorm:"column:port_no"`
	Metric   string        `gorm:"column:metric"`
	ValueNum string        `gorm:"column:value_num"`
	TS       time.Time     `gorm:"column:ts"`
}

func (telemetryRow) TableName() string { return "telemetry" }
