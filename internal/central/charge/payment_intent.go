package charge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	paymentdb "github.com/ChargePilot2026/charge-pilot/internal/central/charge/paymentgenerated"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
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

type PaymentIntentStore struct{ DB *sql.DB }

// Reserve creates a payment record and a short-lived port hold. It deliberately
// does not insert charge_order; only verified payment callbacks may do that.
func (s PaymentIntentStore) Reserve(ctx context.Context, input IntentInput) (PaymentIntent, error) {
	if s.DB == nil || input.UserID == 0 || uuid.Validate(input.ClientRequestID) != nil ||
		input.Port.Kind != "port" || input.Port.Port == nil || !input.Port.Port.Available ||
		input.Port.StationID == 0 || input.Port.StationID != input.Rule.StationID ||
		input.Port.Port.PortID == "" || input.Port.Port.DeviceID != input.Port.DeviceID || input.Port.Port.PortNo == 0 {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	q := paymentdb.New(s.DB)
	if previous, err := q.PaymentIntentByRequest(ctx, paymentdb.PaymentIntentByRequestParams{UserID: input.UserID, ClientRequestID: input.ClientRequestID}); err == nil {
		return existingIntent(previous, input)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return PaymentIntent{}, err
	}
	estimate, err := pricing.EstimateCharge(input.Rule, input.Energy, input.Minutes, time.Now())
	if err != nil {
		return PaymentIntent{}, err
	}
	if err := q.ExpireStaleIntents(ctx); err != nil {
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
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return PaymentIntent{}, err
	}
	defer tx.Rollback()
	txq := paymentdb.New(tx)
	openid, err := txq.UserOpenID(ctx, input.UserID)
	if err != nil {
		return PaymentIntent{}, err
	}
	payment, err := txq.InsertPaymentOrderForIntent(ctx, paymentdb.InsertPaymentOrderForIntentParams{OrderNo: merchantOrderNo, UserID: input.UserID, TotalCents: estimate.TotalCents, ExpiredAt: sql.NullTime{Time: expiresAt, Valid: true}})
	if err != nil {
		return PaymentIntent{}, err
	}
	paymentID, err := payment.LastInsertId()
	if err != nil {
		return PaymentIntent{}, fmt.Errorf("payment order id unavailable: %w", err)
	}
	if paymentID <= 0 {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	err = txq.InsertChargePaymentIntent(ctx, paymentdb.InsertChargePaymentIntentParams{
		IntentID: intentID, ClientRequestID: input.ClientRequestID, MerchantOrderNo: merchantOrderNo,
		PaymentOrderID: uint64(paymentID), UserID: input.UserID, Openid: openid,
		DeviceID: input.Port.DeviceID, PortNo: input.Port.Port.PortNo, PortCode: input.Port.Port.PortID,
		StationID: input.Port.StationID, PricingRuleID: input.Rule.ID, PricingRuleVersion: input.Rule.Version,
		PricingSnapshot: snapshot, EstimatedKwh: estimate.EstimatedKWh, EstimatedMinutes: estimate.EstimatedMinutes,
		ElectricCents: estimate.ElectricCents, ServiceCents: estimate.ServiceCents, TotalCents: estimate.TotalCents,
		ChargeMode: estimate.ChargeMode, ChargeQuantity: estimate.ChargeQuantity, ExpiresAt: expiresAt,
	})
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return PaymentIntent{}, ErrPaymentIntentConflict
		}
		return PaymentIntent{}, err
	}
	if err := tx.Commit(); err != nil {
		return PaymentIntent{}, err
	}
	return PaymentIntent{IntentID: intentID, MerchantOrderNo: merchantOrderNo, PaymentOrderID: uint64(paymentID), UserID: input.UserID,
		OpenID: openid, DeviceID: input.Port.DeviceID, PortNo: input.Port.Port.PortNo, PortCode: input.Port.Port.PortID,
		StationID: input.Port.StationID, Estimate: estimate, ExpiresAt: expiresAt, Status: "initiated"}, nil
}

func existingIntent(row paymentdb.PaymentIntentByRequestRow, input IntentInput) (PaymentIntent, error) {
	if row.PortCode != input.Port.Port.PortID || row.DeviceID != input.Port.DeviceID || row.EstimatedMinutes != input.Minutes || row.PricingRuleID != input.Rule.ID || row.PricingRuleVersion != input.Rule.Version || row.Status != paymentdb.ChargePaymentIntentStatusInitiated || time.Now().After(row.ExpiresAt) {
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
		UserID: row.UserID, OpenID: row.Openid, DeviceID: row.DeviceID, PortNo: row.PortNo, PortCode: row.PortCode,
		StationID: row.StationID, Estimate: snapshot.Estimate, ExpiresAt: row.ExpiresAt, Status: string(row.Status)}, nil
}
