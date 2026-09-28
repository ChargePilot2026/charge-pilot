package payment

import (
	"context"
	"strings"
)

// Simulator is only wired by the explicit local development PAYMENT_MODE.
// Its response is not a WeChat payment credential.
type Simulator struct{}

func (Simulator) Prepay(_ context.Context, request PrepayRequest) (PrepayParams, error) {
	if request.MerchantOrderNo == "" || request.OpenID == "" || request.AmountCents <= 0 || !strings.HasPrefix(request.MerchantOrderNo, "PAY") {
		return PrepayParams{}, ErrInvalidPayment
	}
	return PrepayParams{Provider: "simulation", PrepayID: "SIM" + request.MerchantOrderNo}, nil
}
