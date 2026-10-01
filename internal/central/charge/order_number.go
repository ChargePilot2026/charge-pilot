package charge

import (
	"fmt"
	"time"
	"unicode"

	"gorm.io/gorm"
)

// The start request is numbered before the device ACK exists. Keep this number
// immutable for payment, command and callback correlation; StartedAt records ACK time.
func chargeOrderNumber(at time.Time, device string, port uint8) (string, error) {
	if at.IsZero() || device == "" || port == 0 || port > 99 {
		return "", ErrPaymentIntentConflict
	}
	for _, r := range device {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return "", ErrPaymentIntentConflict
		}
	}
	stamp := pricingTime(at)
	if len(stamp) != 14 {
		return "", ErrPaymentIntentConflict
	}
	no := fmt.Sprintf("C%s%s%02d", stamp, device, port)
	if len(no) > 64 {
		return "", ErrPaymentIntentConflict
	}
	return no, nil
}
func pricingTime(at time.Time) string {
	return at.In(time.FixedZone("CST", 8*3600)).Format("20060102150405")
}

func newChargeOrderNumber(tx *gorm.DB, device string, port uint8) (string, error) {
	no, err := chargeOrderNumber(time.Now(), device, port)
	if err != nil {
		return "", err
	}
	var count int64
	if err := tx.Model(&ChargeOrderRecord{}).Where("order_no=?", no).Count(&count).Error; err != nil {
		return "", err
	}
	if count != 0 {
		return "", ErrPaymentIntentConflict
	}
	return no, nil
}
