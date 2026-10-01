package payment

import (
	"context"
	"strings"
	"testing"
)

func TestPaymentNumbersFitProviderAndSimulationKeepsLegacyRequests(t *testing.T) {
	for _, number := range []string{"P893327963750400123", "P9223372036854775807"} {
		if !wechatOrderNumber.MatchString(number) {
			t.Fatal("provider rejected new number", number)
		}
		params, err := (Simulator{}).Prepay(context.Background(), PrepayRequest{MerchantOrderNo: number, OpenID: "test", AmountCents: 100})
		if err != nil || params.PrepayID != "SIM"+number {
			t.Fatal(number, params, err)
		}
	}
	legacy := "PAYW" + strings.Repeat("a", 32)
	if _, err := (Simulator{}).Prepay(context.Background(), PrepayRequest{MerchantOrderNo: legacy, OpenID: "test", AmountCents: 100}); err != nil {
		t.Fatal("legacy simulation replay rejected", err)
	}
	if wechatOrderNumber.MatchString(legacy) {
		t.Fatal("provider accepts over-length number")
	}
	for _, number := range []string{"P123", "P202610011200009233456789012345678", "PAY/invalid"} {
		if wechatOrderNumber.MatchString(number) {
			t.Fatal("provider accepted invalid number", number)
		}
	}
}
