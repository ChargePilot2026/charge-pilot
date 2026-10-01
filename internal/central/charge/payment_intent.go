package charge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	UserID          uint64           `json:"user_id,string"`
	OpenID          string           `json:"-"`
	DeviceID        string           `json:"device_id"`
	PortNo          uint8            `json:"port_no"`
	PortCode        string           `json:"port_id"`
	StationID       uint64           `json:"station_id"`
	Estimate        pricing.Estimate `json:"estimate"`
	ExpiresAt       time.Time        `json:"expires_at"`
	Status          string           `json:"status"`
	CouponGrantID   uint64           `json:"coupon_grant_id,omitempty"`
	// DiscountCents 为券减免金额，PayableCents 为用户实际应付金额；单位均为分。
	DiscountCents int64 `json:"discount_cents"`
	PayableCents  int64 `json:"payable_cents"`
}

type IntentInput struct {
	UserID          uint64
	ClientRequestID string
	Port            ScanResult
	Energy          string
	Minutes         uint16
	Rule            pricing.Rule
	Offer           *pricing.Offer
	// CouponGrantID 可选。
	// 填上就当场算好减免额并冻结进快照，这样之后重放不会按另一个金额扣费。
	CouponGrantID uint64
}

type PaymentIntentStore struct{ DB *gorm.DB }

// Replay 为重复的客户端请求返回最初冻结下来的结算方案。
// 首次请求之后，已发布的活动或计价规则可能已经变了。
func (s PaymentIntentStore) Replay(ctx context.Context, userID uint64, requestID, portID string, offerID, couponID uint64) (*PaymentIntent, error) {
	if s.DB == nil || userID == 0 || uuid.Validate(requestID) != nil {
		return nil, ErrPaymentIntentConflict
	}
	var row PaymentIntentRecord
	err := s.DB.WithContext(ctx).Where("user_id = ? AND client_request_id = ?", userID, requestID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.PortCode != portID || row.CouponGrantID != couponID || row.Status != "initiated" || !time.Now().Before(row.ExpiresAt) ||
		(row.OfferID.Valid && row.OfferID.Int64 != int64(offerID)) || (!row.OfferID.Valid && offerID != 0) {
		return nil, ErrPaymentIntentConflict
	}
	var snapshot struct {
		Estimate pricing.Estimate `json:"estimate"`
		Offer    *pricing.Offer   `json:"offer"`
	}
	if json.Unmarshal(row.PricingSnapshot, &snapshot) != nil || snapshot.Estimate.TotalCents != row.TotalCents ||
		(row.OfferID.Valid && (snapshot.Offer == nil || snapshot.Offer.ID != offerID)) {
		return nil, ErrPaymentIntentConflict
	}
	return &PaymentIntent{IntentID: row.IntentID, MerchantOrderNo: row.MerchantOrderNo, PaymentOrderID: row.PaymentOrderID,
		UserID: row.UserID, OpenID: row.OpenID, DeviceID: row.DeviceID, PortNo: row.PortNo, PortCode: row.PortCode,
		StationID: row.StationID, Estimate: snapshot.Estimate, ExpiresAt: row.ExpiresAt, Status: row.Status,
		CouponGrantID: row.CouponGrantID, DiscountCents: row.DiscountCents, PayableCents: row.TotalCents - row.DiscountCents}, nil
}

// Reserve 创建支付记录和短期端口预占，不创建 charge_order；订单由验签成功的支付回调创建。
func (s PaymentIntentStore) Reserve(ctx context.Context, input IntentInput) (PaymentIntent, error) {
	if s.DB == nil || input.UserID == 0 || uuid.Validate(input.ClientRequestID) != nil ||
		input.Port.Kind != "port" || input.Port.Port == nil || !input.Port.Port.Available ||
		input.Port.StationID == 0 || input.Port.StationID != input.Rule.StationID ||
		input.Port.Port.PortID == "" || input.Port.Port.DeviceID != input.Port.DeviceID || input.Port.Port.PortNo == 0 {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	if input.Offer != nil && input.CouponGrantID != 0 {
		// 套餐返还按实付金额处理。
		// 券的分摊需要另立一份契约，才能与固定价套餐组合。
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	var previous PaymentIntentRecord
	err := s.DB.WithContext(ctx).Where("user_id = ? AND client_request_id = ?", input.UserID, input.ClientRequestID).Take(&previous).Error
	if err == nil {
		return existingIntent(previous, input)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return PaymentIntent{}, err
	}
	var estimate pricing.Estimate
	if input.Offer != nil {
		if !input.Offer.Valid() || input.Offer.StationID != input.Port.StationID {
			return PaymentIntent{}, pricing.ErrInvalidPricing
		}
		// 固定时长套餐由设备倒计时停机；金额预算由平台根据计量执行计费和停机。
		// 下发的最大时长是设备安全上限，不替代平台的消费封顶规则。
		if input.Rule.Spec.Scheme == nil {
			return PaymentIntent{}, pricing.ErrInvalidPricing
		}
		p, ok := input.Rule.Spec.Scheme.Package(input.Offer.PackageID)
		if !ok || input.Offer.ID != input.Rule.ID*100+p.ID || p.PriceCents != input.Offer.PriceCents || p.Mode != input.Offer.Mode {
			return PaymentIntent{}, pricing.ErrInvalidPricing
		}
		input.Rule.Spec = input.Rule.Spec.Scheme.SpecFor(p)
		minutes, mode := input.Rule.Spec.Scheme.Normalized().Policy.MaxMinutes, uint8(4)
		quantity := minutes
		if input.Offer.Mode == "duration" {
			minutes, mode = input.Offer.DurationMinutes, 0
			quantity = minutes
		} else if input.Offer.Mode == "energy" {
			minutes, mode, quantity = 0, 1, uint16(input.Offer.EnergyWh)
		}
		estimate = pricing.Estimate{Mode: input.Rule.Spec.Mode, EstimatedKWh: "0.000", EstimatedMinutes: minutes, PrepaidCents: input.Offer.PriceCents, TotalCents: input.Offer.PriceCents, ChargeMode: mode, ChargeQuantity: quantity}
	} else {
		return PaymentIntent{}, pricing.ErrOfferUnavailable
	}
	// 创建支付记录和占用端口前校验优惠券并计算减免额，失败时不留预占记录。
	var discount int64
	if input.CouponGrantID != 0 {
		coupons := CouponStore{DB: s.DB}
		discount, err = coupons.Quote(ctx, input.UserID, input.CouponGrantID, estimate.TotalCents)
		if err != nil {
			return PaymentIntent{}, err
		}
	}
	if err := s.DB.WithContext(ctx).Model(&PaymentIntentRecord{}).Where("status = 'initiated' AND expires_at < NOW(3)").Update("status", "expired").Error; err != nil {
		return PaymentIntent{}, err
	}
	intentID := uuid.NewString()
	var merchantOrderNo string
	expiresAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Millisecond)
	snapshot, err := json.Marshal(struct {
		Rule       pricing.Rule     `json:"rule"`
		Estimate   pricing.Estimate `json:"estimate"`
		Offer      *pricing.Offer   `json:"offer,omitempty"`
		ComputedAt time.Time        `json:"computed_at"`
	}{Rule: input.Rule, Estimate: estimate, Offer: input.Offer, ComputedAt: time.Now().UTC()})
	if err != nil {
		return PaymentIntent{}, err
	}
	var openid string
	var paymentID uint64
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCheckoutPort(tx, input.Port.Port.PortID); err != nil {
			return err
		}
		if err := checkoutPortAvailable(tx, input.Port.Port.PortID); err != nil {
			return err
		}
		var identity struct {
			OpenID string `gorm:"column:openid"`
		}
		if err := tx.Table("user").Select("openid").Where("id = ? AND status = 'active' AND deleted_at IS NULL", input.UserID).Take(&identity).Error; err != nil {
			return err
		}
		openid = identity.OpenID
		merchantOrderNo, err = newPaymentOrderNumber(tx)
		if err != nil {
			return err
		}
		payable := estimate.TotalCents - discount
		paymentOrder := PaymentOrderRecord{OrderNo: merchantOrderNo, BizType: "charge", BizID: 0,
			UserID: input.UserID, PayMethod: "wechat", TotalCents: payable,
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
			CouponGrantID: input.CouponGrantID, DiscountCents: discount,
			ChargeMode: estimate.ChargeMode, ChargeQuantity: estimate.ChargeQuantity, Status: "initiated", ExpiresAt: expiresAt}
		if input.Offer != nil {
			intent.OfferID = sql.NullInt64{Int64: int64(input.Offer.ID), Valid: true}
		}
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
		StationID: input.Port.StationID, Estimate: estimate, ExpiresAt: expiresAt, Status: "initiated",
		CouponGrantID: input.CouponGrantID, DiscountCents: discount, PayableCents: estimate.TotalCents - discount}, nil
}

func existingIntent(row PaymentIntentRecord, input IntentInput) (PaymentIntent, error) {
	if row.PortCode != input.Port.Port.PortID || row.DeviceID != input.Port.DeviceID || row.Status != "initiated" || time.Now().After(row.ExpiresAt) {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	if input.Offer != nil {
		if !row.OfferID.Valid || row.OfferID.Int64 != int64(input.Offer.ID) {
			return PaymentIntent{}, ErrPaymentIntentConflict
		}
	} else if row.OfferID.Valid || row.EstimatedMinutes != input.Minutes || row.PricingRuleID != input.Rule.ID || row.PricingRuleVersion != input.Rule.Version {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	// 重放必须使用首次意图绑定的优惠券。
	if row.CouponGrantID != input.CouponGrantID {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	var snapshot struct {
		Estimate pricing.Estimate `json:"estimate"`
		Offer    *pricing.Offer   `json:"offer"`
	}
	if json.Unmarshal(row.PricingSnapshot, &snapshot) != nil {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	if input.Offer != nil {
		if snapshot.Offer == nil || snapshot.Offer.ID != input.Offer.ID {
			return PaymentIntent{}, ErrPaymentIntentConflict
		}
	} else {
		requestedEnergy, err := decimal.NewFromString(input.Energy)
		if err != nil || snapshot.Estimate.EstimatedKWh != requestedEnergy.StringFixed(3) {
			return PaymentIntent{}, ErrPaymentIntentConflict
		}
	}
	if snapshot.Estimate.TotalCents != row.TotalCents {
		return PaymentIntent{}, ErrPaymentIntentConflict
	}
	return PaymentIntent{IntentID: row.IntentID, MerchantOrderNo: row.MerchantOrderNo, PaymentOrderID: row.PaymentOrderID,
		UserID: row.UserID, OpenID: row.OpenID, DeviceID: row.DeviceID, PortNo: row.PortNo, PortCode: row.PortCode,
		StationID: row.StationID, Estimate: snapshot.Estimate, ExpiresAt: row.ExpiresAt, Status: row.Status,
		CouponGrantID: row.CouponGrantID, DiscountCents: row.DiscountCents, PayableCents: row.TotalCents - row.DiscountCents}, nil
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
