package billing

import (
	"strings"
	"testing"
)

func TestCalculationNumberPreservesChargeIdentityAndHistoricNumbers(t *testing.T) {
	for _, orderNo := range []string{
		"C20261001104200534824051408265201",
		"C20261002000000board_ABC-9912",
		"C20261002000000" + strings.Repeat("A", 47) + "99",
	} {
		if got := CalculationNumber(orderNo, 68); got != "B"+orderNo[1:] {
			t.Fatalf("new charge identity changed: %q -> %q", orderNo, got)
		}
	}
	for _, orderNo := range []string{
		"20261001104200534824051408265201",
		"CH883bda3b89ef42d8837fe127a5087056",
		"DEMO-202610-68", "billing-fixture", "",
		"C20261301104200board01", "C20261001104200board00",
		"C20261001104200board/101", "C2026100110420001",
		"C20261002000000" + strings.Repeat("A", 48) + "01",
	} {
		if got := CalculationNumber(orderNo, 68); got != "FEE00000000000000000068" {
			t.Fatalf("historic or malformed order changed: %q -> %q", orderNo, got)
		}
	}
}
