package charge

import (
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/snowflake"
	"gorm.io/gorm"
)

// Payment numbers identify payments independently of users and charge orders.
// The allocator is shared by all payment sources and user registration.
func newPaymentOrderNumber(tx *gorm.DB) (string, error) {
	id, err := snowflake.Next(tx)
	if err != nil {
		return "", err
	}
	return "P" + strconv.FormatUint(id, 10), nil
}
