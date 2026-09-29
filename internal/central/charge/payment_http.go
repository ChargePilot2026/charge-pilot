package charge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type PrepayProvider interface {
	Prepay(context.Context, payment.PrepayRequest) (payment.PrepayParams, error)
}

type PaymentStartAPI struct {
	Auth     identity.SessionAuthenticator
	Scan     ScanAPI
	Pricing  pricing.Store
	Intents  PaymentIntentStore
	Provider PrepayProvider
}

func (a PaymentStartAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/user/scan/offers", a.offers)
	router.POST("/api/v1/user/scan/start", a.start)
}

func (a PaymentStartAPI) offers(c *gin.Context) {
	var body struct {
		PortID string `json:"port_id"`
	}
	if c.ShouldBindJSON(&body) != nil || !userScanCodePattern.MatchString(body.PortID) {
		httpapi.BadRequest(c, "端口编码无效")
		return
	}
	port, status := a.Scan.lookup(c.Request.Context(), body.PortID)
	if status != http.StatusOK || port.Kind != "port" || port.Port == nil {
		httpapi.Write(c, http.StatusNotFound, 1004, "端口不存在", nil)
		return
	}
	rows, err := a.Pricing.ActiveOffers(c.Request.Context(), port.StationID, port.DeviceID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "充电方案暂时无法读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"station_id": port.StationID, "port_id": body.PortID, "items": rows})
}

func (a PaymentStartAPI) start(c *gin.Context) {
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	if a.Provider == nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "payment provider unavailable", nil)
		return
	}
	var body struct {
		ClientRequestID string `json:"client_request_id"`
		PortID          string `json:"port_id"`
		OfferID         uint64 `json:"offer_id"`
	}
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || uuid.Validate(body.ClientRequestID) != nil || !userScanCodePattern.MatchString(body.PortID) {
		httpapi.BadRequest(c, "invalid payment request")
		return
	}
	if body.OfferID == 0 {
		httpapi.BadRequest(c, "请选择后台发布的充电方案")
		return
	}
	{
		previous, replayErr := a.Intents.Replay(c.Request.Context(), userID, body.ClientRequestID, body.PortID, body.OfferID, 0)
		if errors.Is(replayErr, ErrPaymentIntentConflict) {
			httpapi.Write(c, http.StatusConflict, 2001, "payment intent unavailable", nil)
			return
		}
		if replayErr != nil {
			httpapi.Write(c, http.StatusServiceUnavailable, 5001, "payment storage unavailable", nil)
			return
		}
		if previous != nil {
			a.finishStart(c, *previous)
			return
		}
	}
	port, status := a.Scan.lookup(c.Request.Context(), body.PortID)
	if status == http.StatusNotFound {
		httpapi.Write(c, http.StatusNotFound, 1004, "port not found", nil)
		return
	}
	if status != http.StatusOK {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "gateway unavailable", nil)
		return
	}
	if port.Kind != "port" || port.Port == nil || !port.Port.Available || port.StationID == 0 {
		httpapi.Write(c, http.StatusConflict, 2001, "port unavailable", nil)
		return
	}
	rule, err := a.Pricing.ActiveDeviceRule(c.Request.Context(), port.StationID, port.DeviceID)
	if errors.Is(err, pricing.ErrRuleUnavailable) || errors.Is(err, pricing.ErrInvalidPricing) {
		httpapi.Write(c, http.StatusConflict, 2004, "pricing unavailable", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "pricing storage unavailable", nil)
		return
	}
	selected, lookupErr := a.Pricing.ActiveOffer(c.Request.Context(), port.StationID, port.DeviceID, body.OfferID)
	if errors.Is(lookupErr, pricing.ErrOfferUnavailable) {
		httpapi.Write(c, http.StatusConflict, 2004, "充电方案已下架", nil)
		return
	}
	if lookupErr != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "充电方案暂时无法读取", nil)
		return
	}
	intent, err := a.Intents.Reserve(c.Request.Context(), IntentInput{UserID: userID, ClientRequestID: body.ClientRequestID,
		Port: port, Rule: rule, Offer: &selected})
	if errors.Is(err, pricing.ErrInvalidPricing) {
		httpapi.BadRequest(c, "invalid charging estimate")
		return
	}
	if errors.Is(err, ErrCouponNotFound) {
		httpapi.BadRequest(c, "优惠券不存在或未发放给该账号")
		return
	}
	if errors.Is(err, ErrCouponExhausted) {
		httpapi.Write(c, http.StatusConflict, 2001, "优惠券已过期或已使用", nil)
		return
	}
	if errors.Is(err, ErrCouponThreshold) {
		httpapi.BadRequest(c, "未达到该优惠券的使用门槛")
		return
	}
	if errors.Is(err, ErrPaymentIntentConflict) {
		httpapi.Write(c, http.StatusConflict, 2001, "payment intent unavailable", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "payment storage unavailable", nil)
		return
	}
	a.finishStart(c, intent)
}

func (a PaymentStartAPI) finishStart(c *gin.Context, intent PaymentIntent) {
	params, err := a.Intents.PrepayParams(c.Request.Context(), intent.PaymentOrderID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		params, err = a.Provider.Prepay(c.Request.Context(), payment.PrepayRequest{MerchantOrderNo: intent.MerchantOrderNo,
			OpenID: intent.OpenID, AmountCents: intent.PayableCents, ExpiresAt: intent.ExpiresAt})
		if err == nil {
			err = a.Intents.SavePrepay(c.Request.Context(), intent.PaymentOrderID, params)
		}
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 3001, "payment prepay unavailable", nil)
		return
	}
	httpapi.OK(c, gin.H{"intent_id": intent.IntentID, "merchant_order_no": intent.MerchantOrderNo,
		"port_id": intent.PortCode, "device_id": intent.DeviceID, "station_id": intent.StationID,
		"electric_cents": intent.Estimate.ElectricCents, "service_cents": intent.Estimate.ServiceCents,
		"total_cents": intent.Estimate.TotalCents, "discount_cents": intent.DiscountCents,
		"payable_cents": intent.PayableCents, "coupon_grant_id": intent.CouponGrantID,
		"payment_params": params, "expires_at": intent.ExpiresAt.UTC()})
}

type NotificationVerifier interface {
	VerifyNotification(context.Context, *http.Request) (payment.VerifiedTransaction, error)
}

type WechatCallbackAPI struct {
	Verifier NotificationVerifier
	Store    PaymentCallbackStore
}

func (a WechatCallbackAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/public/payments/wechat/callback", a.callback)
}

func (a WechatCallbackAPI) callback(c *gin.Context) {
	if a.Verifier == nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "payment callback unavailable", nil)
		return
	}
	transaction, err := a.Verifier.VerifyNotification(c.Request.Context(), c.Request)
	if err != nil {
		httpapi.Write(c, http.StatusBadRequest, 3001, "invalid payment notification", nil)
		return
	}
	_, err = a.Store.Apply(c.Request.Context(), VerifiedPayment{Provider: transaction.Provider, MerchantID: transaction.MerchantID,
		AppID: transaction.AppID, MerchantOrderNo: transaction.MerchantOrderNo, TransactionID: transaction.TransactionID,
		OpenID: transaction.OpenID, PaidCents: transaction.PaidCents, PaidAt: transaction.PaidAt})
	if errors.Is(err, ErrPaymentCallbackConflict) {
		httpapi.Write(c, http.StatusConflict, 3001, "payment does not match intent", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "payment persistence unavailable", nil)
		return
	}
	c.Status(http.StatusNoContent)
}

type SimulationCallbackAPI struct {
	DB           *gorm.DB
	Store        PaymentCallbackStore
	ServiceToken string
}

func (a SimulationCallbackAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/payments/simulate-success", a.callback)
}

func (a SimulationCallbackAPI) callback(c *gin.Context) {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return
	}
	var body struct {
		MerchantOrderNo string `json:"merchant_order_no" binding:"required,max=64"`
	}
	if c.ShouldBindJSON(&body) != nil {
		httpapi.BadRequest(c, "invalid simulation request")
		return
	}
	var intent PaymentIntentRecord
	err := a.DB.WithContext(c.Request.Context()).Where("merchant_order_no = ?", body.MerchantOrderNo).Take(&intent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusNotFound, 1004, "payment intent not found", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "payment intent unavailable", nil)
		return
	}
	result, err := a.Store.Apply(c.Request.Context(), VerifiedPayment{Provider: "simulation", MerchantID: "local-simulation",
		AppID: a.Store.ExpectedAppID, MerchantOrderNo: intent.MerchantOrderNo,
		TransactionID: "SIMTX" + intent.IntentID, OpenID: intent.OpenID,
		PaidCents: intent.TotalCents, PaidAt: time.Now().UTC()})
	if errors.Is(err, ErrPaymentCallbackConflict) {
		httpapi.Write(c, http.StatusConflict, 3001, "simulation payment conflict", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "simulation persistence unavailable", nil)
		return
	}
	httpapi.OK(c, result)
}
