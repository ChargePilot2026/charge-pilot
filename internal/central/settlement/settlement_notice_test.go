package settlement

import (
	"github.com/ChargePilot2026/charge-pilot/internal/central/order"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"testing"
)

func TestSettlementNoticeUsesFrozenDisplayAndFinalAmounts(t *testing.T) {
	fee := pricing.Fee{ElectricCents: 80, ServiceCents: 40, TotalCents: 120}
	notice := settlementNotice("event", order.ChargeOrderRecord{ID: 1, UserID: 2}, pricing.Display{}, fee, 80)
	if _, ok := notice["total_cents"]; ok {
		t.Fatal("hidden end fee leaked into notification")
	}
	notice = settlementNotice("event", order.ChargeOrderRecord{ID: 1, UserID: 2}, pricing.Display{ShowFeeOnEnd: true}, fee, 80)
	if notice["total_cents"] != int64(120) || notice["refund_cents"] != int64(80) {
		t.Fatal(notice)
	}
	if _, ok := notice["electric_cents"]; ok {
		t.Fatal("hidden split leaked")
	}
}
