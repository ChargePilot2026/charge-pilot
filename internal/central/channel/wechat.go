package channel

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/downloader"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/jsapi"
	"github.com/wechatpay-apiv3/wechatpay-go/services/refunddomestic"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

var ErrInvalidPayment = errors.New("invalid or unverified payment")

// The provider requires 6–32 ASCII characters and a merchant-wide unique ID.
var wechatOrderNumber = regexp.MustCompile(`^[0-9A-Za-z_\-|*]{6,32}$`)

type PrepayRequest struct {
	MerchantOrderNo string
	OpenID          string
	AmountCents     int64
	ExpiresAt       time.Time
}

type PrepayParams struct {
	Provider  string `json:"provider"`
	PrepayID  string `json:"prepay_id"`
	AppID     string `json:"appId"`
	TimeStamp string `json:"timeStamp"`
	NonceStr  string `json:"nonceStr"`
	Package   string `json:"package"`
	SignType  string `json:"signType"`
	PaySign   string `json:"paySign"`
}

type VerifiedTransaction struct {
	Provider        string
	MerchantID      string
	AppID           string
	MerchantOrderNo string
	TransactionID   string
	OpenID          string
	PaidCents       int64
	PaidAt          time.Time
}

type Config struct {
	AppID             string
	MerchantID        string
	CertificateSerial string
	APIv3Key          string
	PrivateKeyPath    string
	NotifyURL         string
}

type WechatDirect struct {
	config  Config
	refunds refunddomestic.RefundsApiService
	jsapi   jsapi.JsapiApiService
	notify  *notify.Handler
}

func NewWechatDirect(ctx context.Context, config Config) (*WechatDirect, error) {
	if config.AppID == "" || config.MerchantID == "" || config.CertificateSerial == "" || len(config.APIv3Key) != 32 ||
		config.PrivateKeyPath == "" || config.NotifyURL == "" {
		return nil, ErrInvalidPayment
	}
	privateKey, err := utils.LoadPrivateKeyWithPath(config.PrivateKeyPath)
	if err != nil {
		return nil, err
	}
	client, err := core.NewClient(ctx, option.WithWechatPayAutoAuthCipher(config.MerchantID, config.CertificateSerial, privateKey, config.APIv3Key))
	if err != nil {
		return nil, err
	}
	visitor := downloader.MgrInstance().GetCertificateVisitor(config.MerchantID)
	return &WechatDirect{config: config, refunds: refunddomestic.RefundsApiService{Client: client}, jsapi: jsapi.JsapiApiService{Client: client},
		notify: notify.NewNotifyHandler(config.APIv3Key, verifiers.NewSHA256WithRSAVerifier(visitor))}, nil
}

func (w *WechatDirect) Prepay(ctx context.Context, request PrepayRequest) (PrepayParams, error) {
	if w == nil || !wechatOrderNumber.MatchString(request.MerchantOrderNo) || request.OpenID == "" || request.AmountCents <= 0 || !request.ExpiresAt.After(time.Now()) {
		return PrepayParams{}, ErrInvalidPayment
	}
	resp, _, err := w.jsapi.PrepayWithRequestPayment(ctx, jsapi.PrepayRequest{
		Appid: new(w.config.AppID), Mchid: new(w.config.MerchantID),
		Description: new("电瓶车充电"), OutTradeNo: new(request.MerchantOrderNo),
		TimeExpire: new(request.ExpiresAt), NotifyUrl: new(w.config.NotifyURL),
		Amount: &jsapi.Amount{Total: new(request.AmountCents), Currency: new("CNY")},
		Payer:  &jsapi.Payer{Openid: new(request.OpenID)},
	})
	if err != nil {
		return PrepayParams{}, err
	}
	if resp == nil || resp.PrepayId == nil || resp.Appid == nil || resp.TimeStamp == nil || resp.NonceStr == nil || resp.Package == nil || resp.SignType == nil || resp.PaySign == nil {
		return PrepayParams{}, ErrInvalidPayment
	}
	return PrepayParams{Provider: "wechat_direct", PrepayID: *resp.PrepayId, AppID: *resp.Appid,
		TimeStamp: *resp.TimeStamp, NonceStr: *resp.NonceStr, Package: *resp.Package,
		SignType: *resp.SignType, PaySign: *resp.PaySign}, nil
}

func (w *WechatDirect) VerifyNotification(ctx context.Context, request *http.Request) (VerifiedTransaction, error) {
	if w == nil || request == nil {
		return VerifiedTransaction{}, ErrInvalidPayment
	}
	transaction := new(payments.Transaction)
	if _, err := w.notify.ParseNotifyRequest(ctx, request, transaction); err != nil {
		return VerifiedTransaction{}, err
	}
	if transaction.TradeState == nil || *transaction.TradeState != "SUCCESS" || transaction.Mchid == nil || transaction.Appid == nil ||
		transaction.OutTradeNo == nil || transaction.TransactionId == nil || transaction.SuccessTime == nil ||
		transaction.Amount == nil || transaction.Amount.Total == nil || transaction.Amount.Currency == nil || *transaction.Amount.Currency != "CNY" ||
		transaction.Payer == nil || transaction.Payer.Openid == nil || transaction.TradeType == nil || *transaction.TradeType != "JSAPI" {
		return VerifiedTransaction{}, ErrInvalidPayment
	}
	paidAt, err := time.Parse(time.RFC3339, *transaction.SuccessTime)
	if err != nil || *transaction.Mchid != w.config.MerchantID || *transaction.Appid != w.config.AppID {
		return VerifiedTransaction{}, ErrInvalidPayment
	}
	return VerifiedTransaction{Provider: "wechat_direct", MerchantID: *transaction.Mchid, AppID: *transaction.Appid,
		MerchantOrderNo: *transaction.OutTradeNo, TransactionID: *transaction.TransactionId,
		OpenID: *transaction.Payer.Openid, PaidCents: *transaction.Amount.Total, PaidAt: paidAt.UTC()}, nil
}
