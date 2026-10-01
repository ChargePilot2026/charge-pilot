package pricing

import (
	"time"
)

type MeterSegment struct {
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	EnergyWh  uint32    `json:"energy_wh"`
	// PeakW 是该段内可验证的峰值；缺失时不以电量反推功率。
	PeakW uint32 `json:"peak_w,omitempty"`
	// PowerW is a verified constant-power fragment; peak alone cannot price
	// realtime power. Missing fragments are sent to review, never inferred from Wh.
	PowerW *uint32 `json:"power_w,omitempty"`
}

type ActualMeter struct {
	ReviewRequired bool           `json:"review_required,omitempty"`
	StartedAt      time.Time      `json:"started_at"`
	EndedAt        time.Time      `json:"ended_at"`
	ChargedWh      uint32         `json:"charged_wh"`
	ChargedSeconds uint32         `json:"charged_seconds"`
	Segments       []MeterSegment `json:"segments,omitempty"`
}

type ActualFee = Fee

// PriceActual 校验计量证据后调用 Cost 计价，不将均匀分摊的估算值视为实际计量。
// 电量片段仅可跨越相同费率边界；变价区间的读数缺失或矛盾时要求核实。
func PriceActual(rule Rule, meter ActualMeter) (Fee, error) {
	usage, err := usageFromMeter(rule, meter)
	if err != nil {
		return Fee{}, err
	}
	return Cost(rule.Spec, usage)
}

// usageFromMeter 校验计量记录并转换为 Cost 所需的 Usage。
// 消费封顶与最终结算共用此校验，保持计量有效性及计费结果一致。
func usageFromMeter(rule Rule, meter ActualMeter) (Usage, error) {
	if meter.ReviewRequired {
		return Usage{}, ErrMeterReview
	}
	if ValidateSpec(rule.Spec) != nil || rule.ID == 0 || rule.Version == 0 {
		return Usage{}, ErrInvalidPricing
	}
	if meter.StartedAt.IsZero() || meter.EndedAt.Before(meter.StartedAt) || meter.EndedAt.Sub(meter.StartedAt) > 7*24*time.Hour || time.Duration(meter.ChargedSeconds)*time.Second > meter.EndedAt.Sub(meter.StartedAt)+5*time.Minute {
		return Usage{}, ErrMeterReview
	}
	segments := meter.Segments
	if len(segments) == 0 {
		segments = []MeterSegment{{StartedAt: meter.StartedAt, EndedAt: meter.EndedAt, EnergyWh: meter.ChargedWh}}
	}
	if len(segments) > maxSlices {
		return Usage{}, ErrMeterReview
	}
	usage := Usage{Start: meter.StartedAt, End: meter.EndedAt, EnergyWh: uint64(meter.ChargedWh), Channel: rule.Channel}
	// A total energy reading can price a uniform tariff exactly. Changing rates
	// require measured boundary readings; never distribute energy by time.
	if len(meter.Segments) == 0 && !specIsUniformOver(rule.Spec, meter.StartedAt, meter.EndedAt) {
		return Usage{}, ErrMeterReview
	}
	totalWh := uint64(0)
	cursor := meter.StartedAt
	for _, segment := range segments {
		if !segment.StartedAt.Equal(cursor) || segment.EndedAt.Before(segment.StartedAt) || segment.EndedAt.After(meter.EndedAt) || segment.StartedAt.Equal(segment.EndedAt) && segment.EnergyWh > 0 {
			return Usage{}, ErrMeterReview
		}
		cursor = segment.EndedAt
		totalWh += uint64(segment.EnergyWh)
		power := segment.PeakW
		if rule.Spec.Mode == ModeServerRealtimePower {
			if segment.PowerW == nil {
				return Usage{}, ErrMeterReview
			}
			power = *segment.PowerW
		}
		if rule.Spec.Mode == ModeServerMaxPower && power == 0 && segment.PowerW == nil && meter.ChargedWh > 0 {
			return Usage{}, ErrMeterReview
		}
		usage.Samples = append(usage.Samples, Sample{Start: segment.StartedAt, End: segment.EndedAt,
			EnergyWh: uint64(segment.EnergyWh), PowerW: power, PowerKnown: segment.PowerW != nil})
	}
	if !cursor.Equal(meter.EndedAt) || totalWh != uint64(meter.ChargedWh) {
		return Usage{}, ErrMeterReview
	}
	return usage, nil
}

// StopAtMeter 使用结算相同的计量校验判断服务端计费是否达到消费上限。
// 计量或规则无效、模式不适用时返回说明原因的计划，不依据无效数据停机。
func StopAtMeter(rule Rule, meter ActualMeter) (StopPlan, error) {
	usage, err := usageFromMeter(rule, meter)
	if err != nil {
		return StopPlan{}, err
	}
	elapsed := meter.EndedAt.Sub(meter.StartedAt)
	return DecideStop(rule.Spec, usage, elapsed)
}
