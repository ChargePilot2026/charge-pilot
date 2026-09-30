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

// PriceActual 从不把均匀分摊出来的预估值当成计量。电量分段只有在前后两段的
// 实际费率一致时才允许跨过费率边界；缺失或自相矛盾的变价费率读数一律送去复核。
//
// 算术本身是 Cost 的：这个函数只负责判断上报的这份计量是否可信到可以交出去。
func PriceActual(rule Rule, meter ActualMeter) (Fee, error) {
	usage, err := usageFromMeter(rule, meter)
	if err != nil {
		return Fee{}, err
	}
	return Cost(rule.Spec, usage)
}

// usageFromMeter 把一条计量记录转成引擎要计价的 Usage。
//
// 这里的校验才是实质内容而不是走过场：它决定上报的这份计量是否可信到可以交给
// Cost。它之所以单独成一个函数，是因为消费封顶必须拿结算将要用的同一份计量
// 来试算——用更宽松的读数去量封顶，就会得到一个在错误时刻触发的封顶，
// 而在账单出错之前没有人会发现。
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

// StopAtMeter 判断一次运行中的充电是否已经触到它那份电价表所声明的消费封顶，
// 依据是一条计量记录。
//
// 这条计量走的是与结算完全相同的校验，所以一次充电绝不会因为一个本来就没法
// 开票的读数而被切断。一份引擎已经算不出价的电价表，或者一个并非服务端计费的
// 模式，得到的会是一份明说这件事的计划而不是一个错误：拿一份引擎拒绝执行的
// 电价表推出来的停止规则，不构成切断别人充电的理由。
func StopAtMeter(rule Rule, meter ActualMeter) (StopPlan, error) {
	usage, err := usageFromMeter(rule, meter)
	if err != nil {
		return StopPlan{}, err
	}
	elapsed := meter.EndedAt.Sub(meter.StartedAt)
	return DecideStop(rule.Spec, usage, elapsed)
}
