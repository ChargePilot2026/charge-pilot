package pricing

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestTemplateUnitsAreExplicitButFrozenLegacyRulesRemainExecutable(t *testing.T) {
	for _, basis := range []TierPriceBasis{"", TierPerHourAtCeiling, "unknown", TierPerKWh} {
		spec := realtimeSpec()
		spec.TierPriceBasis = basis
		if err := ValidateSpec(spec); err != nil {
			t.Fatalf("legacy execution validator rejected %q: %v", basis, err)
		}
		err := ValidateTemplateSpec(spec)
		if basis == TierPerKWh {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrLegacyPricing) {
			t.Fatalf("new publication accepted legacy basis %q: %v", basis, err)
		}
	}
}

func TestPowerAndEnergyBillingUseClearUnits(t *testing.T) {
	for _, mode := range []ChargeMode{ModeServerRealtimePower, ModeServerMaxPower, ModeServerEnergy} {
		spec := realtimeSpec()
		spec.Mode = mode
		spec.Electric.Basis = mode.BasisFor()
		spec.Electric.Periods[0].Tiers = []Tier{{MaxWatts: 200, ElectricCents: 100, ServiceCents: 50}}
		if mode != ModeServerRealtimePower {
			spec.TierPriceBasis = ""
		}
		spec.Service = &ServiceLine{Basis: ServiceMinutePower}
		wantElectric, wantService := int64(20), int64(10)
		if mode == ModeServerMaxPower {
			wantElectric, wantService = 100, 50
		}
		if mode == ModeServerEnergy {
			spec.Electric.Periods[0] = Period{EndMinute: 1440, ElectricCents: 100}
			spec.Service = &ServiceLine{Basis: ServiceEnergy, CentsPerKWh: 50}
		}
		if err := ValidateTemplateSpec(spec); err != nil {
			t.Fatal(err)
		}
		fee, err := Cost(spec, hourUsage(200, 200))
		if err != nil || fee.ElectricCents != wantElectric || fee.ServiceCents != wantService {
			t.Fatalf("%s: fee=%+v err=%v", mode, fee, err)
		}
	}
}

func TestLegacyEquivalentPricesPreserveQuotesSettlementAndSnapshots(t *testing.T) {
	for _, basis := range []TierPriceBasis{"", TierPerHourAtCeiling} {
		spec := realtimeSpec()
		spec.TierPriceBasis = basis
		spec.Electric.Periods = []Period{
			{EndMinute: 660, Tiers: []Tier{{MaxWatts: 200, ElectricCents: 83, ServiceCents: 12}, {MaxWatts: 9990, ElectricCents: 120, ServiceCents: 30}}},
			{EndMinute: 1440, Tiers: []Tier{{MaxWatts: 200, ElectricCents: 100, ServiceCents: 15}, {MaxWatts: 9990, ElectricCents: 150, ServiceCents: 40}}},
		}
		spec.Service = &ServiceLine{Basis: ServiceMinutePower}
		spec.LossRateBP = 250
		spec.Multiplier = &ChannelMultiplier{TempBP: 11000, CardBP: 8500}
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		var converted Spec
		if err := json.Unmarshal(raw, &converted); err != nil {
			t.Fatal(err)
		}
		for pi := range converted.Electric.Periods {
			for ti := range converted.Electric.Periods[pi].Tiers {
				tier := &converted.Electric.Periods[pi].Tiers[ti]
				tier.ElectricCents = tier.ElectricCents * int64(tier.MaxWatts) / 1000
			}
		}
		converted.TierPriceBasis = TierPerKWh
		if err := ValidateTemplateSpec(converted); err != nil {
			t.Fatal(err)
		}
		for _, watts := range []uint32{200, 400, 9990} {
			for _, channel := range []Channel{ChannelDefault, ChannelTemp, ChannelCard} {
				usage := hourUsage(uint64(watts), watts)
				usage.End = usage.Start.Add(2 * time.Hour)
				usage.Samples = []Sample{
					{Start: usage.Start, End: usage.Start.Add(time.Hour), EnergyWh: uint64(watts) / 2, PowerW: watts},
					{Start: usage.Start.Add(time.Hour), End: usage.End, EnergyWh: uint64(watts) - uint64(watts)/2, PowerW: watts},
				}
				usage.Channel = channel
				oldFee, err := Cost(spec, usage)
				if err != nil {
					t.Fatal(err)
				}
				newFee, err := Cost(converted, usage)
				if err != nil || !reflect.DeepEqual(oldFee, newFee) {
					t.Fatalf("legacy conversion changed fee: old=%+v new=%+v err=%v", oldFee, newFee, err)
				}
			}
		}
		var originalQuote Estimate
		var originalActual Fee
		for i, current := range []Spec{spec, converted} {
			rule := Rule{ID: 1, Version: 1, Spec: current}
			usage := hourUsage(200, 200)
			quote, err := EstimateCharge(rule, "0.2", 60, usage.Start, 0)
			if err != nil {
				t.Fatal(err)
			}
			fee, err := PriceActual(rule, ActualMeter{StartedAt: usage.Start, EndedAt: usage.End, ChargedWh: 200, ChargedSeconds: 3600,
				Segments: []MeterSegment{{StartedAt: usage.Start, EndedAt: usage.End, EnergyWh: 200, PeakW: 200}}})
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				originalQuote, originalActual = quote, fee
			} else if !reflect.DeepEqual(quote, originalQuote) || !reflect.DeepEqual(fee, originalActual) {
				t.Fatalf("conversion changed quote or measured settlement: %+v %+v", quote, fee)
			}
		}
		unchanged, _ := json.Marshal(spec)
		if string(unchanged) != string(raw) {
			t.Fatal("frozen legacy snapshot mutated")
		}
	}
}
