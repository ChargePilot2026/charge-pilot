// Package order 承载 charge_order 家族的表记录类型。
// 表归属决定包归属：charge_order / charge_order_pricing /
// charge_start_receipt / charge_end_receipt / charge_event_log /
// active_port_charge 的记录定义集中于此，供各家族逻辑包引用。
package order

import (
	"database/sql"
	"time"
)

type ChargeOrderRecord struct {
	DiscountCents  int64          `gorm:"column:discount_cents"`
	ID             uint64         `gorm:"column:id;primaryKey"`
	OrderNo        string         `gorm:"column:order_no"`
	UserID         uint64         `gorm:"column:user_id"`
	DeviceID       string         `gorm:"column:device_id"`
	PortNo         uint8          `gorm:"column:port_no"`
	PortCode       sql.NullString `gorm:"column:port_code"`
	PaymentOrderID sql.NullInt64  `gorm:"column:payment_order_id"`
	Status         string         `gorm:"column:status"`
	BusinessStatus string         `gorm:"column:business_status;->"`             // STORED generated；写入只由内部生命周期产生。
	PaymentStatus  string         `gorm:"column:payment_status;default:pending"` // 实际支付和退款结果的持久状态。
	StartedAt      sql.NullTime   `gorm:"column:started_at"`
	EndedAt        sql.NullTime   `gorm:"column:ended_at"`
	ChargedKWh     sql.NullString `gorm:"column:charged_kwh"`
	ChargedSeconds sql.NullInt64  `gorm:"column:charged_seconds"`
	ChargeMode     uint8          `gorm:"column:charge_mode"`
	ChargeQuantity uint16         `gorm:"column:charge_quantity"`
	FailureReason  sql.NullString `gorm:"column:failure_reason"`
	CreatedMonth   time.Time      `gorm:"column:created_month;primaryKey"`
}

func (ChargeOrderRecord) TableName() string { return "charge_order" }

type StartReceiptRecord struct {
	CommandID     string         `gorm:"column:command_id;primaryKey"`
	ChargeOrderID uint64         `gorm:"column:charge_order_id"`
	OrderNo       sql.NullString `gorm:"column:order_no"`
	DeviceID      sql.NullString `gorm:"column:device_id"`
	PortNo        sql.NullInt16  `gorm:"column:port_no"`
	PortID        sql.NullInt64  `gorm:"column:port_id"`
	Success       bool           `gorm:"column:success"`
	ResultCode    sql.NullInt16  `gorm:"column:result_code"`
	OccurredAt    sql.NullTime   `gorm:"column:occurred_at"`
}

func (StartReceiptRecord) TableName() string { return "charge_start_receipt" }

type EndReceiptRecord struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id;primaryKey"`
	StopCommandID string `gorm:"column:stop_command_id"`
	MeterJSON     []byte `gorm:"column:meter_json"`
}

func (EndReceiptRecord) TableName() string { return "charge_end_receipt" }

type ActivePortChargeRecord struct {
	PortID        uint64    `gorm:"column:port_id;primaryKey"`
	DeviceID      string    `gorm:"column:device_id"`
	PortNo        uint8     `gorm:"column:port_no"`
	ChargeOrderID uint64    `gorm:"column:charge_order_id"`
	UserID        uint64    `gorm:"column:user_id"`
	StartedAt     time.Time `gorm:"column:started_at"`
}

func (ActivePortChargeRecord) TableName() string { return "active_port_charge" }

type ChargeEventLogRecord struct {
	ChargeOrderID uint64    `gorm:"column:charge_order_id"`
	EventID       string    `gorm:"column:event_id"`
	Event         string    `gorm:"column:event"`
	Actor         string    `gorm:"column:actor"`
	Detail        string    `gorm:"column:detail"`
	OccurredAt    time.Time `gorm:"column:occurred_at"`
}

func (ChargeEventLogRecord) TableName() string { return "charge_event_log" }

type ChargePricingSnapshotRecord struct {
	ChargeOrderID   uint64 `gorm:"column:charge_order_id;primaryKey"`
	PaymentIntentID string `gorm:"column:payment_intent_id"`
	UserID          uint64 `gorm:"column:user_id"`
	PortCode        string `gorm:"column:port_code"`
	PricingSnapshot []byte `gorm:"column:pricing_snapshot"`
}

func (ChargePricingSnapshotRecord) TableName() string { return "charge_order_pricing" }
