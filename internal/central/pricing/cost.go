package pricing

import (
	"github.com/shopspring/decimal"
	"time"
)

const (
	maxSlices     = 7 * 24 * 60
	maxLossRateBP = 100000
)

func Cost(spec Spec, usage Usage) (Fee, error) { return costUsage(spec, usage) }
func tierFor(period Period, watts uint32) Tier {
	lower := uint32(0)
	for _, tier := range period.Tiers {
		if watts >= lower && watts <= uint32(tier.MaxWatts) {
			return tier
		}
		lower = uint32(tier.MaxWatts) + 1
	}
	return period.Tiers[len(period.Tiers)-1]
}

func periodAt(spec Spec, at time.Time) Period {
	schedule, err := compilePeriods(spec.Electric.Periods)
	if err != nil {
		return Period{}
	}
	local := at.In(beijing)
	return schedule[local.Hour()*60+local.Minute()]
}

func toCents(value decimal.Decimal) (int64, bool) {
	rounded := value.Round(0).BigInt()
	if !rounded.IsInt64() {
		return 0, false
	}
	return rounded.Int64(), true
}
