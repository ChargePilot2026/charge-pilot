package billing

import (
	"fmt"
	"time"
	"unicode"
)

// CalculationNumber keeps the charge request identity in new billing numbers.
// Historic numeric, CH and fixture order numbers retain their original FEE rule.
func CalculationNumber(orderNo string, chargeOrderID uint64) string {
	if prefixedChargeOrderNumber(orderNo) {
		return "B" + orderNo[1:]
	}
	return fmt.Sprintf("FEE%020d", chargeOrderID)
}

func prefixedChargeOrderNumber(no string) bool {
	// C + 14 timestamp digits + nonempty device + exactly two port digits.
	if len(no) < 18 || len(no) > 64 || no[0] != 'C' {
		return false
	}
	if _, err := time.Parse("20060102150405", no[1:15]); err != nil {
		return false
	}
	port := no[len(no)-2:]
	if port[0] < '0' || port[0] > '9' || port[1] < '0' || port[1] > '9' || port == "00" {
		return false
	}
	for _, r := range no[15 : len(no)-2] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
