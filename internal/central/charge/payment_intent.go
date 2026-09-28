package charge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrPaymentIntentConflict = errors.New("payment intent conflicts with another request or active port")

type PaymentIntent struct {
	IntentID        string           `json:"intent_id"`
	MerchantOrderNo string           `json:"merchant_order_no"`
	PaymentOrderID  uint64           `json:"payment_order_id"`
	UserID          uint64           `json:"user_id"`
	OpenID          string           `json:"-"`
	DeviceID        string           `json:"device_id"`
	PortNo          uint8            `json:"port_no"`
	PortCode        string           `json:"port_id"`
	StationID       uint64           `json:"station_id"`
	Estimate        pricing.Estimate `json:"estimate"`
	ExpiresAt       time.Time        `json:"expires_at"`
	Status          string           `json:"status"`
}

type IntentInput struct {
	UserID          uint64
	ClientRequestID string
	Port            ScanResult
	Energy          string
	Minutes         uint16
	Rule            pricing.Rule
}

type PaymentIntentStore struct{ DB *gorm.DB }

// Reserve creates a payment record and a short-lived port hold. It deliberately
// does not insert charge_order; only verified payment callbacks may do that.
func (s PaymentIntentStore) Reserve(ctx context.Context, input IntentInput) (PaymentIntent, error) {
	if s.DB == nil || input.UserID == 0 || uuid.Validate(input.ClientRequestID) != nil ||
		input.Port.Kind != "port" || input.Port.Port == nil || !input.Port.Port.Available ||
		input.Port.StationID == 0 || input.Port.StationID != input.Rule.StationID ||
		input.Port.Port.PortID == "" || input.Port.Port.DeviceID != input.Port.DeviceID || input.Port.Port.PortNo == 0 {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	var previous PaymentIntentRecord
	err := s.DB.WithContext(ctx).Where("user_id = ? AND client_request_id = ?", input.UserID, input.ClientRequestID).Take(&previous).Error
	if err == nil {
		return existingIntent(previous, input)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return PaymentIntent{}, err
	}
	estimate, err := pricing.EstimateCharge(input.Rule, input.Energy, input.Minutes, time.Now())
	if err != nil {
		return PaymentIntent{}, err
	}
	if err := s.DB.WithContext(ctx).Model(&PaymentIntentRecord{}).Where("status = 'initiated' AND expires_at < NOW(3)").Update("status", "expired").Error; err != nil {
		return PaymentIntent{}, err
	}
	intentID := uuid.NewString()
	merchantOrderNo := "PAY" + strings.ReplaceAll(uuid.NewString(), "-", "")
	expiresAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Millisecond)
	snapshot, err := json.Marshal(struct {
		Rule       pricing.Rule     `json:"rule"`
		Estimate   pricing.Estimate `json:"estimate"`
		ComputedAt time.Time        `json:"computed_at"`
	}{Rule: input.Rule, Estimate: estimate, ComputedAt: time.Now().UTC()})
	if err != nil {
		return PaymentIntent{}, err
	}
	var openid string
	var paymentID uint64
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var identity struct {
			OpenID string `gorm:"column:openid"`
		}
		if err := tx.Table("user").Select("openid").Where("id = ? AND status = 'active' AND deleted_at IS NULL", input.UserID).Take(&identity).Error; err != nil {
			return err
		}
		openid = identity.OpenID
		paymentOrder := PaymentOrderRecord{OrderNo: merchantOrderNo, BizType: "charge", BizID: 0,
			UserID: input.UserID, PayMethod: "wechat", TotalCents: estimate.TotalCents,
			Status: "initiated", ExpiredAt: sql.NullTime{Time: expiresAt, Valid: true}, CreatedMonth: utcDate()}
		if err := tx.Create(&paymentOrder).Error; err != nil {
			return err
		}
		paymentID = paymentOrder.ID
		intent := PaymentIntentRecord{IntentID: intentID, ClientRequestID: input.ClientRequestID,
			MerchantOrderNo: merchantOrderNo, PaymentOrderID: paymentID, UserID: input.UserID, OpenID: openid,
			DeviceID: input.Port.DeviceID, PortNo: input.Port.Port.PortNo, PortCode: input.Port.Port.PortID,
			StationID: input.Port.StationID, PricingRuleID: input.Rule.ID, PricingRuleVersion: input.Rule.Version,
			PricingSnapshot: snapshot, EstimatedKWh: estimate.EstimatedKWh, EstimatedMinutes: estimate.EstimatedMinutes,
			ElectricCents: estimate.ElectricCents, ServiceCents: estimate.ServiceCents, TotalCents: estimate.TotalCents,
			ChargeMode: estimate.ChargeMode, ChargeQuantity: estimate.ChargeQuantity, Status: "initiated", ExpiresAt: expiresAt}
		if err := tx.Create(&intent).Error; err != nil {
			if isMySQLDuplicate(err) {
				return ErrPaymentIntentConflict
			}
			return err
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return PaymentIntent{}, err
	}
	if paymentID == 0 {
		return PaymentIntent{}, err
	}
	return PaymentIntent{IntentID: intentID, MerchantOrderNo: merchantOrderNo, PaymentOrderID: paymentID, UserID: input.UserID,
		OpenID: openid, DeviceID: input.Port.DeviceID, PortNo: input.Port.Port.PortNo, PortCode: input.Port.Port.PortID,
		StationID: input.Port.StationID, Estimate: estimate, ExpiresAt: expiresAt, Status: "initiated"}, nil
}

func existingIntent(row PaymentIntentRecord, input IntentInput) (PaymentIntent, error) {
	if row.PortCode != input.Port.Port.PortID || row.DeviceID != input.Port.DeviceID || row.EstimatedMinutes != input.Minutes || row.PricingRuleID != input.Rule.ID || row.PricingRuleVersion != input.Rule.Version || row.Status != "initiated" || time.Now().After(row.ExpiresAt) {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	var snapshot struct {
		Estimate pricing.Estimate `json:"estimate"`
	}
	requestedEnergy, err := decimal.NewFromString(input.Energy)
	if json.Unmarshal(row.PricingSnapshot, &snapshot) != nil || err != nil || snapshot.Estimate.EstimatedKWh != requestedEnergy.StringFixed(3) {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	return PaymentIntent{IntentID: row.IntentID, MerchantOrderNo: row.MerchantOrderNo, PaymentOrderID: row.PaymentOrderID,
		UserID: row.UserID, OpenID: row.OpenID, DeviceID: row.DeviceID, PortNo: row.PortNo, PortCode: row.PortCode,
		StationID: row.StationID, Estimate: snapshot.Estimate, ExpiresAt: row.ExpiresAt, Status: row.Status}, nil
}

func (s PaymentIntentStore) PrepayParams(ctx context.Context, paymentOrderID uint64) (payment.PrepayParams, error) {
	var record ChargePrepayRecord
	if err := s.DB.WithContext(ctx).Where("payment_order_id = ?", paymentOrderID).Take(&record).Error; err != nil {
		return payment.PrepayParams{}, err
	}
	var params payment.PrepayParams
	if err := json.Unmarshal(record.ParamsJSON, &params); err != nil || params.PrepayID == "" || params.Provider == "" {
		return payment.PrepayParams{}, ErrPaymentIntentConflict
	}
	return params, nil
}

func (s PaymentIntentStore) SavePrepay(ctx context.Context, paymentOrderID uint64, params payment.PrepayParams) error {
	if s.DB == nil || paymentOrderID == 0 || params.PrepayID == "" || params.Provider == "" {
		return ErrPaymentIntentConflict
	}
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	record := ChargePrepayRecord{PaymentOrderID: paymentOrderID, ParamsJSON: data, PrepayID: sql.NullString{String: params.PrepayID, Valid: true}}
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "payment_order_id"}}, DoNothing: true}).Create(&record).Error
}
