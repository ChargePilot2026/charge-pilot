package charge

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/card"
)

// freezeCardDeadline 委托 card.FreezeDeadline；卡时长截止的冻结语义归属 card 家族。
func (s AutoStopper) freezeCardDeadline(ctx context.Context, id uint64, now time.Time) (bool, error) {
	return card.FreezeDeadline(ctx, s.UserDB, id, now)
}
