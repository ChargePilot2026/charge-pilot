package pricing

import (
	"github.com/shopspring/decimal"
	"sort"
	"time"
)

// costUsage 先对整场充电计算完整分钟，再按秒分配边界分钟，不逐片段取整。
// 不同费率间的电量分配必须有计量证据，不能按经过时间估算。
func costUsage(spec Spec, usage Usage) (Fee, error) {
	if ValidateSpec(spec) != nil {
		return Fee{}, ErrInvalidPricing
	}
	if !spec.Mode.ServerBilled() {
		return Fee{}, ErrNotServerBilled
	}
	if usage.Start.IsZero() || usage.End.Before(usage.Start) || usage.End.Sub(usage.Start) > 7*24*time.Hour {
		return Fee{}, ErrInvalidPricing
	}
	fee := Fee{Basis: spec.Electric.Basis}
	if usage.EnergyWh == 0 || spec.FreeMinutes > 0 && usage.minutes() <= int64(spec.FreeMinutes) {
		return fee, nil
	}
	end := usage.Start.Add(time.Duration(usage.minutes()) * time.Minute)
	samples := usage.Samples
	if len(samples) == 0 {
		samples = []Sample{{Start: usage.Start, End: usage.End, EnergyWh: usage.EnergyWh}}
	}
	if len(samples) > maxSlices {
		return Fee{}, ErrMeterReview
	}
	cursor := usage.Start
	totalWh := uint64(0)
	electric, service := decimal.Zero, decimal.Zero
	type peakPeriod struct {
		start, timeEnd time.Time
		minutes        decimal.Decimal
		peak           uint32
		period         Period
	}
	peaks := map[string]peakPeriod{}
	for _, sample := range samples {
		if !sample.Start.Equal(cursor) || sample.End.Before(sample.Start) || sample.End.After(usage.End) {
			return Fee{}, ErrMeterReview
		}
		cursor = sample.End
		totalWh += sample.EnergyWh
		if spec.Mode == ModeServerEnergy {
			if !specIsUniformOver(spec, sample.Start, sample.End.Add(-time.Nanosecond)) {
				return Fee{}, ErrMeterReview
			}
			period := periodAt(spec, sample.Start)
			kwh := decimal.NewFromInt(int64(sample.EnergyWh)).Div(decimalThousand)
			fee.Fragments = append(fee.Fragments, fragment(sample.Start, sample.End, 0, kwh, "度", period.ElectricCents, period.ServiceCents))
			electric = electric.Add(kwh.Mul(decimal.NewFromInt(period.ElectricCents)))
			service = service.Add(kwh.Mul(decimal.NewFromInt(period.ServiceCents)))
			continue
		}
		if sample.PowerW == 0 && !sample.PowerKnown {
			return Fee{}, ErrMeterReview
		}
		for at := sample.Start; at.Before(sample.End) && at.Before(end); {
			period := periodAt(spec, at)
			local := at.In(beijing)
			boundary := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, beijing).Add(time.Duration(period.EndMinute) * time.Minute)
			next := sample.End
			if next.After(end) {
				next = end
			}
			if next.After(boundary) {
				next = boundary
			}
			minutes := decimal.NewFromInt(int64(next.Sub(at))).Div(decimal.NewFromInt(int64(time.Minute)))
			if spec.Mode == ModeServerMaxPower {
				key := local.Format("2006-01-02") + boundary.Format("15:04")
				v := peaks[key]
				if v.start.IsZero() {
					v.start = at
				}
				v.timeEnd = next
				v.period = period
				v.minutes = v.minutes.Add(minutes)
				if sample.PowerW > v.peak {
					v.peak = sample.PowerW
				}
				peaks[key] = v
			} else {
				tier := tierFor(period, sample.PowerW)
				fee.Fragments = append(fee.Fragments, fragment(at, next, sample.PowerW, minutes.Div(decimal.NewFromInt(60)), "小时", tier.ElectricCents, tier.ServiceCents))
				electric = electric.Add(decimal.NewFromInt(tier.ElectricCents).Mul(minutes).Div(decimal.NewFromInt(60)))
				service = service.Add(decimal.NewFromInt(tier.ServiceCents).Mul(minutes).Div(decimal.NewFromInt(60)))
			}
			at = next
		}
	}
	if !cursor.Equal(usage.End) || totalWh != usage.EnergyWh {
		return Fee{}, ErrMeterReview
	}
	for _, v := range peaks {
		tier := tierFor(v.period, v.peak)
		fee.Fragments = append(fee.Fragments, fragment(v.start, v.timeEnd, v.peak, v.minutes.Div(decimal.NewFromInt(60)), "小时", tier.ElectricCents, tier.ServiceCents))
		electric = electric.Add(decimal.NewFromInt(tier.ElectricCents).Mul(v.minutes).Div(decimal.NewFromInt(60)))
		service = service.Add(decimal.NewFromInt(tier.ServiceCents).Mul(v.minutes).Div(decimal.NewFromInt(60)))
	}
	sort.Slice(fee.Fragments, func(i, j int) bool { return fee.Fragments[i].StartedAt.Before(fee.Fragments[j].StartedAt) })
	var ok bool
	fee.ElectricCents, ok = toCents(electric)
	if !ok {
		return Fee{}, ErrInvalidPricing
	}
	fee.ServiceCents, ok = toCents(service)
	if !ok {
		return Fee{}, ErrInvalidPricing
	}
	if usage.minutes() > 0 && fee.ElectricCents < spec.MinElectricCents {
		fee.ElectricCents = spec.MinElectricCents
	}
	fee.TotalCents = fee.ElectricCents + fee.ServiceCents
	fee.BillableWh = usage.EnergyWh
	return fee, nil
}

// CutoffMeter 从原始计量证据生成计费截止片段，物理总量由其他字段保留。
// 截止点位于电量片段内部时缺乏可靠累计读数，须核实；功率和时长可直接裁剪。
func CutoffMeter(spec Spec, m ActualMeter, cutoff time.Time) (ActualMeter, error) {
	if !cutoff.Before(m.EndedAt) {
		return m, nil
	}
	if cutoff.Before(m.StartedAt) {
		return ActualMeter{}, ErrMeterReview
	}
	if spec.Mode == ModeDeviceDuration {
		seconds := uint32(cutoff.Sub(m.StartedAt) / time.Second)
		if m.ChargedSeconds > seconds {
			m.ChargedSeconds = seconds
		}
		m.EndedAt = cutoff
		return m, nil
	}
	segments := []MeterSegment{}
	wh := uint32(0)
	for _, s := range m.Segments {
		if !s.StartedAt.Before(cutoff) {
			break
		}
		if s.EndedAt.After(cutoff) {
			if spec.Mode == ModeServerEnergy || spec.Mode == ModeDeviceEnergy {
				return ActualMeter{}, ErrMeterReview
			}
			if s.PowerW == nil || spec.Mode == ModeServerMaxPower && s.PeakW > *s.PowerW {
				return ActualMeter{}, ErrMeterReview
			}
			// Energy is irrelevant to the power tariff but the full original
			// segment is kept as evidence of actual charging; no inferred Wh.
			s.EndedAt = cutoff
		}
		segments = append(segments, s)
		wh += s.EnergyWh
	}
	if len(segments) == 0 || !segments[len(segments)-1].EndedAt.Equal(cutoff) {
		return ActualMeter{}, ErrMeterReview
	}
	m.Segments = segments
	m.ChargedWh = wh
	m.EndedAt = cutoff
	m.ChargedSeconds = uint32(cutoff.Sub(m.StartedAt) / time.Second)
	return m, nil
}
