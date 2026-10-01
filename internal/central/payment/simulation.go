package payment

import (
	"context"
	"regexp"
)

// Simulator 只在显式开启本地开发用 PAYMENT_MODE 时才接上线。
// 它返回的不是微信支付凭证。
type Simulator struct{}

var simulationOrderNumber = regexp.MustCompile(`^(P[1-9][0-9]{4,18}|PAY[A-Za-z0-9]+)$`)

func (Simulator) Prepay(_ context.Context, request PrepayRequest) (PrepayParams, error) {
	if request.OpenID == "" || request.AmountCents <= 0 || !simulationOrderNumber.MatchString(request.MerchantOrderNo) {
		return PrepayParams{}, ErrInvalidPayment
	}
	return PrepayParams{Provider: "simulation", PrepayID: "SIM" + request.MerchantOrderNo}, nil
}
