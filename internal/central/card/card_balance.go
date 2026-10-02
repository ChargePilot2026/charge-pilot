package card

import (
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"strconv"
)

func CanonicalCardNumber(card string) bool {
	n, err := strconv.ParseUint(card, 10, 32)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == card
}

// The board sees spendable shared-wallet balance; no balance is stored on-card.
func (a CardAPI) balance(c *gin.Context) {
	if !a.internal(c) {
		return
	}
	card := c.Query("card_no")
	if !CanonicalCardNumber(card) {
		httpapi.BadRequest(c, "卡号须为非零32位十进制整数")
		return
	}
	var row struct{ BalanceCents int64 }
	err := a.Store.DB.WithContext(c.Request.Context()).Table("online_card c").Select("w.balance_cents-w.frozen_cents AS balance_cents").Joins("JOIN user u ON u.id=c.user_id AND u.status='active' AND u.deleted_at IS NULL").Joins("JOIN wallet_account w ON w.user_id=c.user_id AND w.status='active' AND w.deleted_at IS NULL").Where("c.card_no=? AND c.status='active'", card).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.OK(c, gin.H{"valid": false, "balance_cents": 0})
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "卡状态及余额暂不可读取", nil)
		return
	}
	if row.BalanceCents < 0 {
		row.BalanceCents = 0
	}
	httpapi.OK(c, gin.H{"valid": true, "balance_cents": row.BalanceCents})
}
