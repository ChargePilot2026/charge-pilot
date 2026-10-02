package order

import "testing"

func TestPaymentStatusUsesSettledAmountsRatherThanRefundRequests(t *testing.T) {
	for _, test := range []struct {
		name, ledger, want string
		paid, refunded     int64
	}{
		{"unpaid", "initiated", "pending", 0, 0},
		{"closed unpaid", "closed", "pending", 0, 0},
		{"failed unpaid", "failed", "pending", 0, 0},
		{"paid with no refund", "paid", "paid", 100, 0},
		{"zero-cost settled", "paid", "paid", 0, 0},
		{"paid amount despite stale ledger", "initiated", "paid", 100, 0},
		{"refund flag without settled refund", "refunded", "paid", 100, 0},
		{"partial actual refund", "paid", "partial_refunded", 100, 40},
		{"full actual refund", "paid", "refunded", 100, 100},
		{"additional card payment after partial refund", "partial_refunded", "partial_refunded", 200, 40},
	} {
		t.Run(test.name, func(t *testing.T) {
			if actual := SettledPaymentStatus(test.paid, test.refunded, test.ledger); actual != test.want {
				t.Fatalf("paid=%d refunded=%d ledger=%s got %s want %s", test.paid, test.refunded, test.ledger, actual, test.want)
			}
		})
	}
}
