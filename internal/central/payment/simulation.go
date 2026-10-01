package payment

import (
	"context"
	"regexp"
)

// Simulator 仅在 PAYMENT_MODE=simulation 时使用，返回模拟支付参数。
type Simulator struct{}

var simulationOrderNumber = regexp.MustCompile(`^(P[1-9][0-9]{4,18}|PAY[A-Za-z0-9]+)$`)

func (Simulator) Prepay(_ context.Context, request PrepayRequest) (PrepayParams, error) {
	if request.OpenID == "" || request.AmountCents <= 0 || !simulationOrderNumber.MatchString(request.MerchantOrderNo) {
		return PrepayParams{}, ErrInvalidPayment
	}
	return PrepayParams{Provider: "simulation", PrepayID: "SIM" + request.MerchantOrderNo}, nil
}
