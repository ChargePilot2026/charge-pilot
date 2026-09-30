package payment

import (
	"context"
	"strings"
)

// Simulator 只在显式开启本地开发用 PAYMENT_MODE 时才接上线。
// 它返回的不是微信支付凭证。
type Simulator struct{}

func (Simulator) Prepay(_ context.Context, request PrepayRequest) (PrepayParams, error) {
	if request.MerchantOrderNo == "" || request.OpenID == "" || request.AmountCents <= 0 || !strings.HasPrefix(request.MerchantOrderNo, "PAY") {
		return PrepayParams{}, ErrInvalidPayment
	}
	return PrepayParams{Provider: "simulation", PrepayID: "SIM" + request.MerchantOrderNo}, nil
}
