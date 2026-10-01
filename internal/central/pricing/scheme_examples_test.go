package pricing

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func exampleScheme(mode ChargeMode, periods []Period, price int64) Scheme {
	return Scheme{Name: "验收方案", Amount: &AmountMode{Algorithm: mode, Periods: periods}, Packages: []Package{{ID: 1, Name: "金额套餐", Mode: "amount", PriceCents: price}}, Policy: AmountPolicy{MaxMinutes: 600}, Card: CardPolicy{MaxMinutes: 600}}.Normalized()
}
func examplePeriod(electric, service int64) Period {
	return Period{EndMinute: 1440, Tiers: []Tier{{MaxWatts: 200, ElectricCents: electric, ServiceCents: service}, {MaxWatts: 1000, ElectricCents: 120, ServiceCents: 60}}}
}
func exampleMeter(start time.Time, minutes []int, watts []uint32, wh []uint32) ActualMeter {
	m := ActualMeter{StartedAt: start}
	at := start
	for i, n := range minutes {
		power := watts[i]
		next := at.Add(time.Duration(n) * time.Minute)
		m.Segments = append(m.Segments, MeterSegment{StartedAt: at, EndedAt: next, EnergyWh: wh[i], PeakW: power, PowerW: &power})
		m.ChargedWh += wh[i]
		m.ChargedSeconds += uint32(n * 60)
		at = next
	}
	m.EndedAt = at
	return m
}
func settleExample(t *testing.T, s Scheme, m ActualMeter, electric, service, total, refund int64) {
	t.Helper()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(s)
	preview, err := s.Preview(1, m)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("preview mutated configuration")
	}
	got := preview.Settlement
	if preview.Status != "calculated" || got.ElectricCents != electric || got.ServiceCents != service || got.TotalCents != total || preview.RefundCents != refund {
		t.Fatalf("got %+v; want electric=%d service=%d total=%d refund=%d", preview, electric, service, total, refund)
	}
}
func TestRedesignDocumentExamples(t *testing.T) {
	start := time.Date(2026, 9, 30, 9, 30, 0, 0, beijing)
	t.Run("01 maximum ordinary", func(t *testing.T) {
		settleExample(t, exampleScheme(ModeServerMaxPower, []Period{examplePeriod(80, 40)}, 300), exampleMeter(start, []int{60}, []uint32{180}, []uint32{180}), 80, 40, 120, 180)
	})
	t.Run("02 maximum tier promotion", func(t *testing.T) {
		settleExample(t, exampleScheme(ModeServerMaxPower, []Period{examplePeriod(80, 40)}, 300), exampleMeter(start, []int{20, 40}, []uint32{180, 600}, []uint32{60, 120}), 120, 60, 180, 120)
	})
	t.Run("03 realtime hourly segments", func(t *testing.T) {
		settleExample(t, exampleScheme(ModeServerRealtimePower, []Period{examplePeriod(80, 40)}, 300), exampleMeter(start, []int{20, 40}, []uint32{180, 600}, []uint32{60, 120}), 107, 53, 160, 140)
	})
	t.Run("04 independent period peaks", func(t *testing.T) {
		p := examplePeriod(80, 40)
		p.EndMinute = 600
		settleExample(t, exampleScheme(ModeServerMaxPower, []Period{p, examplePeriod(100, 50)}, 300), exampleMeter(start, []int{30, 30}, []uint32{600, 180}, []uint32{300, 90}), 110, 55, 165, 135)
	})
	t.Run("05 realtime period and power changes", func(t *testing.T) {
		p := examplePeriod(60, 30)
		p.EndMinute = 600
		s := exampleScheme(ModeServerRealtimePower, []Period{p, examplePeriod(90, 30)}, 300)
		m := exampleMeter(start.Add(10*time.Minute), []int{20, 20, 40}, []uint32{180, 180, 600}, []uint32{60, 60, 400})
		settleExample(t, s, m, 130, 60, 190, 110)
	})
	t.Run("06 energy actual period readings", func(t *testing.T) {
		s := exampleScheme(ModeServerEnergy, []Period{{EndMinute: 600, ElectricCents: 60, ServiceCents: 20}, {EndMinute: 1440, ElectricCents: 100, ServiceCents: 30}}, 300)
		settleExample(t, s, exampleMeter(start, []int{30, 30}, []uint32{180, 600}, []uint32{400, 800}), 104, 32, 136, 164)
	})
	t.Run("07 paid budget", func(t *testing.T) {
		settleExample(t, exampleScheme(ModeServerMaxPower, []Period{examplePeriod(80, 40)}, 100), exampleMeter(start, []int{50}, []uint32{180}, []uint32{150}), 67, 33, 100, 0)
	})
	t.Run("07 tier promotion never creates debt", func(t *testing.T) {
		settleExample(t, exampleScheme(ModeServerMaxPower, []Period{examplePeriod(80, 40)}, 100), exampleMeter(start, []int{20, 40}, []uint32{180, 600}, []uint32{60, 400}), 66, 34, 100, 0)
	})
	t.Run("08 ten hour limit", func(t *testing.T) {
		settleExample(t, exampleScheme(ModeServerMaxPower, []Period{examplePeriod(80, 40)}, 2000), exampleMeter(start, []int{600}, []uint32{180}, []uint32{1800}), 800, 400, 1200, 800)
	})
	t.Run("09 duration complete minutes floor cents", func(t *testing.T) {
		s := Scheme{Name: "时长", Packages: []Package{{ID: 1, Name: "120分钟", Mode: "duration", PriceCents: 300, Minutes: 120}}}.Normalized()
		m := exampleMeter(start, []int{61}, []uint32{180}, []uint32{180})
		m.EndedAt = m.EndedAt.Add(30 * time.Second)
		m.ChargedSeconds += 30
		settleExample(t, s, m, 0, 152, 152, 148)
	})
	t.Run("10 integer purchase fractional consumption", func(t *testing.T) {
		s := Scheme{Name: "电量", Energy: &EnergyMode{ElectricCents: 80, ServiceCents: 20}, Packages: []Package{{ID: 1, Name: "3度", Mode: "energy", KWh: 3, PriceCents: 300}}}.Normalized()
		settleExample(t, s, exampleMeter(start, []int{60}, []uint32{600}, []uint32{1375}), 109, 28, 137, 163)
	})
	t.Run("11 free conditions", func(t *testing.T) {
		s := exampleScheme(ModeServerMaxPower, []Period{examplePeriod(80, 40)}, 200)
		s.Policy.FreeMinutes = 5
		m := exampleMeter(start, []int{5}, []uint32{180}, []uint32{15})
		m.EndedAt = m.EndedAt.Add(59 * time.Second)
		m.ChargedSeconds += 59
		m.Segments[0].EndedAt = m.EndedAt
		settleExample(t, s, m, 0, 0, 0, 200)
		settleExample(t, s, exampleMeter(start, []int{6}, []uint32{180}, []uint32{18}), 8, 4, 12, 188)
	})
	t.Run("12 minimum electricity only", func(t *testing.T) {
		s := exampleScheme(ModeServerMaxPower, []Period{examplePeriod(20, 30)}, 200)
		s.Policy.MinElectricCents = 50
		settleExample(t, s, exampleMeter(start, []int{60}, []uint32{180}, []uint32{180}), 50, 30, 80, 120)
	})
	t.Run("13 independent rounding", func(t *testing.T) {
		settleExample(t, exampleScheme(ModeServerMaxPower, []Period{examplePeriod(50, 25)}, 300), exampleMeter(start, []int{40}, []uint32{180}, []uint32{120}), 33, 17, 50, 250)
	})
	t.Run("14 card aggregate purchases", func(t *testing.T) {
		s := Scheme{Name: "刷卡", Packages: []Package{{ID: 1, Name: "120分钟", Mode: "duration", PriceCents: 200, Minutes: 120}}, Card: CardPolicy{PackageID: 1, MaxMinutes: 600}}.Normalized()
		offer := s.Offers(Rule{ID: 1, StationID: 1})[0]
		offer.PurchaseCount = 2
		offer.PriceCents = 400
		offer.DurationMinutes = 240
		m := exampleMeter(start, []int{150}, []uint32{180}, []uint32{450})
		got, err := SettleSession(s.SpecFor(s.Packages[0]), m, &offer, ActualFromMeter(m))
		if err != nil || got.TotalCents != 250 || 1000-offer.PriceCents+(offer.PriceCents-got.TotalCents) != 750 {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("16 missing readings reviewed", func(t *testing.T) {
		s := exampleScheme(ModeServerEnergy, []Period{{EndMinute: 600, ElectricCents: 60}, {EndMinute: 1440, ElectricCents: 100}}, 300)
		m := exampleMeter(start, []int{60}, []uint32{600}, []uint32{1200})
		m.Segments = nil
		p, err := s.Preview(1, m)
		if err != nil || p.Status != "meter_review" {
			t.Fatalf("%+v %v", p, err)
		}
	})
}
func TestSchemeValidationAndSnapshotIsolation(t *testing.T) {
	s := exampleScheme(ModeServerRealtimePower, []Period{examplePeriod(80, 40)}, 300)
	s.Packages = append(s.Packages, Package{ID: 2, Name: "时长", Mode: "duration", PriceCents: 200, Minutes: 120}, Package{ID: 3, Name: "电量", Mode: "energy", PriceCents: 300, KWh: 3})
	s.Energy = &EnergyMode{ElectricCents: 80, ServiceCents: 20}
	s.Card.PackageID = 2
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(s)
	var frozen Scheme
	if json.Unmarshal(encoded, &frozen) != nil {
		t.Fatal("snapshot decode")
	}
	s.Amount.Periods[0].Tiers[0].ElectricCents = 999
	s.Packages[0].PriceCents = 900
	if frozen.Amount.Periods[0].Tiers[0].ElectricCents != 80 || frozen.Packages[0].PriceCents != 300 {
		t.Fatal("snapshot changed")
	}
	bad := frozen
	bad.Packages = append([]Package(nil), frozen.Packages...)
	bad.Packages[2].PriceCents = 299
	if bad.Validate() == nil {
		t.Fatal("manual energy price accepted")
	}
	bad = frozen
	bad.Policy.MinElectricCents = 301
	if bad.Validate() == nil {
		t.Fatal("minimum exceeds package")
	}
	bad = frozen
	bad.Card.PackageID = 1
	if bad.Validate() == nil {
		t.Fatal("amount card package accepted")
	}
	bad = frozen
	bad.Policy.LossRateBP = 100
	if bad.Validate() == nil {
		t.Fatal("unconfirmed loss enabled")
	}
	if frozen.ValidateCapabilities(Capabilities{Duration: true, Energy: true, MaxMinutes: 600}) == nil {
		t.Fatal("unverified card capability accepted")
	}
	if frozen.ValidateCapabilities(Capabilities{Duration: true, Energy: true, MaxMinutes: 600, OnlineCard: true, ReportsEnergy: true, ReportsSegmentedPower: true}) != nil {
		t.Fatal("verified modes refused")
	}
}
func TestCompleteMinutesDoNotDisappearAcrossTinyFragments(t *testing.T) {
	s := exampleScheme(ModeServerRealtimePower, []Period{examplePeriod(80, 40)}, 300)
	start := time.Date(2026, 9, 30, 9, 0, 0, 0, beijing)
	u := Usage{Start: start, End: start.Add(61 * time.Second), EnergyWh: 61}
	for i := range 61 {
		u.Samples = append(u.Samples, Sample{Start: start.Add(time.Duration(i) * time.Second), End: start.Add(time.Duration(i+1) * time.Second), EnergyWh: 1, PowerW: 180})
	}
	f, err := Cost(s.SpecFor(s.Packages[0]), u)
	if err != nil || f.TotalCents != 2 {
		t.Fatalf("%+v %v", f, err)
	}
	u.End = start.Add(59 * time.Second)
	u.Samples = u.Samples[:59]
	u.EnergyWh = 59
	f, err = Cost(s.SpecFor(s.Packages[0]), u)
	if err != nil || f.TotalCents != 0 {
		t.Fatalf("subminute: %+v %v", f, err)
	}
}
func TestCutoffRequiresReliableEnergyAndClipsTime(t *testing.T) {
	start := time.Date(2026, 9, 30, 9, 0, 0, 0, beijing)
	m := exampleMeter(start, []int{30, 30}, []uint32{180, 600}, []uint32{200, 800})
	s := exampleScheme(ModeServerEnergy, []Period{{EndMinute: 1440, ElectricCents: 100}}, 300).SpecFor(Package{Mode: "amount"})
	if _, err := CutoffMeter(s, m, start.Add(20*time.Minute)); !errors.Is(err, ErrMeterReview) {
		t.Fatal("guessed energy cutoff")
	}
	got, err := CutoffMeter(s, m, start.Add(30*time.Minute))
	if err != nil || got.ChargedWh != 200 {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = CutoffMeter(Spec{Mode: ModeDeviceDuration}, m, start.Add(20*time.Minute))
	if err != nil || got.ChargedSeconds != 1200 {
		t.Fatalf("%+v %v", got, err)
	}
}
