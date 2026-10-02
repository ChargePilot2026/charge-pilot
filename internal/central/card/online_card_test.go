package card

import (
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"testing"
)

func TestDocumentExample15CardAdditionIsAtomic(t *testing.T) {
	p := pricing.Package{ID: 1, Name: "时长", Mode: "duration", Minutes: 120, PriceCents: 200}
	for _, tc := range []struct {
		name           string
		card           uint64
		purchased, max uint16
		balance        int64
		pending, want  bool
	}{
		{"480 to 600", 1, 480, 600, 200, false, true}, {"reject 720", 1, 600, 600, 1000, false, false}, {"insufficient wallet", 1, 120, 600, 100, false, false}, {"different card", 2, 120, 600, 1000, false, false}, {"unknown result prevents another debit", 1, 120, 600, 1000, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanExtend(tc.card, 1, p, tc.purchased, tc.max, tc.balance, tc.pending); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}
