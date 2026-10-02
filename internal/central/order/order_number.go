package order

import (
	"errors"
	"fmt"
	"time"
	"unicode"

	"gorm.io/gorm"
)

// ErrChargeNumberConflict 表示充电单号与设备标识、端口或既有单号冲突。
// 独立于 payment 包的哨兵：order 作为 payment 的上游被依赖方，不得反向 import payment。
var ErrChargeNumberConflict = errors.New("charge order number conflicts with device identity, port or an existing order")

// The start request is numbered before the device ACK exists. Keep this number
// immutable for payment, command and callback correlation; StartedAt records ACK time.
func ChargeOrderNumber(at time.Time, device string, port uint8) (string, error) {
	if at.IsZero() || device == "" || port == 0 || port > 99 {
		return "", ErrChargeNumberConflict
	}
	for _, r := range device {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return "", ErrChargeNumberConflict
		}
	}
	stamp := pricingTime(at)
	if len(stamp) != 14 {
		return "", ErrChargeNumberConflict
	}
	no := fmt.Sprintf("C%s%s%02d", stamp, device, port)
	if len(no) > 64 {
		return "", ErrChargeNumberConflict
	}
	return no, nil
}
func pricingTime(at time.Time) string {
	return at.In(time.FixedZone("CST", 8*3600)).Format("20060102150405")
}

func NewChargeOrderNumber(tx *gorm.DB, device string, port uint8) (string, error) {
	no, err := ChargeOrderNumber(time.Now(), device, port)
	if err != nil {
		return "", err
	}
	var count int64
	if err := tx.Model(&ChargeOrderRecord{}).Where("order_no=?", no).Count(&count).Error; err != nil {
		return "", err
	}
	if count != 0 {
		return "", ErrChargeNumberConflict
	}
	return no, nil
}
