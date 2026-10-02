// Package payment 承载支付订单家族的表记录类型：
// payment_order / charge_payment_intent / charge_prepay /
// payment_callback_idempotent。渠道适配器在 central/channel。
package payment

import (
	"database/sql"
	"time"
)

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
	OfferID            sql.NullInt64 `gorm:"column:offer_id"`
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
	CouponGrantID      uint64        `gorm:"column:coupon_grant_id"`
	DiscountCents      int64         `gorm:"column:discount_cents"`
	ChargeMode         uint8         `gorm:"column:charge_mode"`
	ChargeQuantity     uint16        `gorm:"column:charge_quantity"`
	Status             string        `gorm:"column:status"`
	ExpiresAt          time.Time     `gorm:"column:expires_at"`
	PaidAt             sql.NullTime  `gorm:"column:paid_at"`
	ChargeOrderID      sql.NullInt64 `gorm:"column:charge_order_id"`
}

func (PaymentIntentRecord) TableName() string { return "charge_payment_intent" }

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
