package charge

import (
	"database/sql"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type ChargeOrderRecord struct {
	ID             uint64         `gorm:"column:id;primaryKey"`
	OrderNo        string         `gorm:"column:order_no"`
	UserID         uint64         `gorm:"column:user_id"`
	DeviceID       string         `gorm:"column:device_id"`
	PortNo         uint8          `gorm:"column:port_no"`
	PortCode       sql.NullString `gorm:"column:port_code"`
	PaymentOrderID sql.NullInt64  `gorm:"column:payment_order_id"`
	Status         string         `gorm:"column:status"`
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

type PaymentOrderRecord struct {
	ID                  uint64         `gorm:"column:id;primaryKey"`
	OrderNo             string         `gorm:"column:order_no"`
	BizType             string         `gorm:"column:biz_type"`
	BizID               uint64         `gorm:"column:biz_id"`
	UserID              uint64         `gorm:"column:user_id"`
	PayMethod           string         `gorm:"column:pay_method"`
	TotalCents          int64          `gorm:"column:total_cents"`
	PaidCents           int64          `gorm:"column:paid_cents"`
	RefundedCents       int64          `gorm:"column:refunded_cents"`
	WechatTransactionID sql.NullString `gorm:"column:wechat_transaction_id"`
	Status              string         `gorm:"column:status"`
	PaidAt              sql.NullTime   `gorm:"column:paid_at"`
	ExpiredAt           sql.NullTime   `gorm:"column:expired_at"`
	CreatedMonth        time.Time      `gorm:"column:created_month;primaryKey"`
}

func (PaymentOrderRecord) TableName() string { return "payment_order" }

type PaymentIntentRecord struct {
	IntentID           string        `gorm:"column:intent_id;primaryKey"`
	ClientRequestID    string        `gorm:"column:client_request_id"`
	MerchantOrderNo    string        `gorm:"column:merchant_order_no"`
	PaymentOrderID     uint64        `gorm:"column:payment_order_id"`
	UserID             uint64        `gorm:"column:user_id"`
	OpenID             string        `gorm:"column:openid"`
	DeviceID           string        `gorm:"column:device_id"`
	PortNo             uint8         `gorm:"column:port_no"`
	PortCode           string        `gorm:"column:port_code"`
	StationID          uint64        `gorm:"column:station_id"`
	PricingRuleID      uint64        `gorm:"column:pricing_rule_id"`
	PricingRuleVersion uint32        `gorm:"column:pricing_rule_version"`
	PricingSnapshot    []byte        `gorm:"column:pricing_snapshot"`
	EstimatedKWh       string        `gorm:"column:estimated_kwh"`
	EstimatedMinutes   uint16        `gorm:"column:estimated_minutes"`
	ElectricCents      int64         `gorm:"column:electric_cents"`
	ServiceCents       int64         `gorm:"column:service_cents"`
	TotalCents         int64         `gorm:"column:total_cents"`
	ChargeMode         uint8         `gorm:"column:charge_mode"`
	ChargeQuantity     uint16        `gorm:"column:charge_quantity"`
	Status             string        `gorm:"column:status"`
	ExpiresAt          time.Time     `gorm:"column:expires_at"`
	PaidAt             sql.NullTime  `gorm:"column:paid_at"`
	ChargeOrderID      sql.NullInt64 `gorm:"column:charge_order_id"`
}

func (PaymentIntentRecord) TableName() string { return "charge_payment_intent" }

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

type EventOutboxRecord struct {
	ID           uint64 `gorm:"column:id;primaryKey"`
	EventID      string `gorm:"column:event_id"`
	Stream       string `gorm:"column:stream"`
	EnvelopeJSON []byte `gorm:"column:envelope_json"`
}

func (EventOutboxRecord) TableName() string { return "event_outbox" }

type RefundRecord struct {
	ExecutionPolicy string         `gorm:"column:execution_policy;default:manual_review"`
	NextAttemptAt   time.Time      `gorm:"column:next_attempt_at;default:CURRENT_TIMESTAMP(3)"`
	RetryCount      uint32         `gorm:"column:retry_count"`
	ID              uint64         `gorm:"column:id;primaryKey"`
	RefundNo        string         `gorm:"column:refund_no"`
	PaymentOrderID  uint64         `gorm:"column:payment_order_id"`
	UserID          uint64         `gorm:"column:user_id"`
	BizType         string         `gorm:"column:biz_type"`
	BizID           uint64         `gorm:"column:biz_id"`
	RefundCents     int64          `gorm:"column:refund_cents"`
	Reason          sql.NullString `gorm:"column:reason"`
	Status          string         `gorm:"column:status"`
	CreatedMonth    time.Time      `gorm:"column:created_month;primaryKey"`
}

func (RefundRecord) TableName() string { return "refund_record" }

type ChargePricingSnapshotRecord struct {
	ChargeOrderID   uint64 `gorm:"column:charge_order_id;primaryKey"`
	PaymentIntentID string `gorm:"column:payment_intent_id"`
	UserID          uint64 `gorm:"column:user_id"`
	PortCode        string `gorm:"column:port_code"`
	PricingSnapshot []byte `gorm:"column:pricing_snapshot"`
}

func (ChargePricingSnapshotRecord) TableName() string { return "charge_order_pricing" }

type PaymentCallbackDigestRecord struct {
	WechatTransactionID string `gorm:"column:wechat_transaction_id;primaryKey"`
	RequestDigest       string `gorm:"column:request_digest"`
}

func (PaymentCallbackDigestRecord) TableName() string { return "payment_callback_idempotent" }

type ChargePrepayRecord struct {
	PaymentOrderID uint64         `gorm:"column:payment_order_id;primaryKey"`
	ParamsJSON     []byte         `gorm:"column:params_json"`
	PrepayID       sql.NullString `gorm:"column:prepay_id"`
}

func (ChargePrepayRecord) TableName() string { return "charge_prepay" }

func isMySQLDuplicate(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
