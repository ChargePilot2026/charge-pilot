package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/finance"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	ErrRuleUnavailable = errors.New("active station pricing rule unavailable")
	ErrInvalidPricing  = errors.New("invalid quote input or pricing rule")
	energyPattern      = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3})?$`)
	beijing            = time.FixedZone("Asia/Shanghai", 8*3600)
)

type Period struct {
	Period             string `json:"period"`
	Start              string `json:"start"`
	End                string `json:"end"`
	ElectricPriceCents int64  `json:"electric_price_cents"`
	ServicePriceCents  *int64 `json:"service_price_cents,omitempty"`
}

type Rule struct {
	ID                    uint64   `json:"rule_id"`
	StationID             uint64   `json:"station_id"`
	Version               uint32   `json:"version"`
	Mode                  string   `json:"mode"`
	Periods               []Period `json:"time_of_use"`
	ServiceCentsPerKWh    int64    `json:"service_cents_per_kwh"`
	ServiceCentsPerMinute int64    `json:"service_cents_per_minute"`
	MinimumCents          int64    `json:"minimum_cents"`
}

type Estimate struct {
	EstimatedKWh     string `json:"estimated_kwh"`
	EstimatedMinutes uint16 `json:"estimated_minutes"`
	ElectricCents    int64  `json:"electric_cents"`
	ServiceCents     int64  `json:"service_cents"`
	TotalCents       int64  `json:"total_cents"`
	ChargeMode       uint8  `json:"charge_mode"`
	ChargeQuantity   uint16 `json:"charge_quantity"`
}

type Store struct{ DB *gorm.DB }

func (s Store) ActiveStationRule(ctx context.Context, stationID uint64) (Rule, error) {
	if s.DB == nil || stationID == 0 || stationID > math.MaxInt64 {
		return Rule{}, ErrRuleUnavailable
	}
	var row pricingRuleRow
	result := s.DB.WithContext(ctx).Table("pricing_rule AS r").
		Select(`r.id, r.station_id, r.mode, r.time_of_use_json,
			r.service_fee_cents_per_kwh, r.service_fee_cents_per_min,
			r.min_charge_cents, r.version`).
		Joins("JOIN station AS s ON s.id = r.station_id").
		Where(`r.station_id = ? AND s.status = 'active' AND s.deleted_at IS NULL
			AND r.status = 'active' AND r.deleted_at IS NULL
			AND (r.effective_from IS NULL OR r.effective_from <= NOW(3))
			AND (r.effective_to IS NULL OR r.effective_to > NOW(3))`, stationID).
		Order("r.version DESC").Order("r.id DESC").Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return Rule{}, ErrRuleUnavailable
	}
	if result.Error != nil {
		return Rule{}, result.Error
	}
	var periods []Period
	if json.Unmarshal(row.TimeOfUseJSON, &periods) != nil || len(periods) == 0 {
		return Rule{}, ErrInvalidPricing
	}
	return Rule{ID: row.ID, StationID: stationID, Version: row.Version, Mode: row.Mode, Periods: periods,
		ServiceCentsPerKWh: row.ServiceFeeCentsPerKWh, ServiceCentsPerMinute: row.ServiceFeeCentsPerMinute,
		MinimumCents: row.MinChargeCents}, nil
}

type pricingRuleRow struct {
	ID                       uint64 `gorm:"column:id"`
	StationID                uint64 `gorm:"column:station_id"`
	Mode                     string `gorm:"column:mode"`
	TimeOfUseJSON            []byte `gorm:"column:time_of_use_json"`
	ServiceFeeCentsPerKWh    int64  `gorm:"column:service_fee_cents_per_kwh"`
	ServiceFeeCentsPerMinute int64  `gorm:"column:service_fee_cents_per_min"`
	MinChargeCents           int64  `gorm:"column:min_charge_cents"`
	Version                  uint32 `gorm:"column:version"`
}

func EstimateCharge(rule Rule, energy string, minutes uint16, start time.Time) (Estimate, error) {
	if rule.ID == 0 || !energyPattern.MatchString(energy) || minutes == 0 || minutes > 600 || rule.MinimumCents < 0 || rule.ServiceCentsPerKWh < 0 || rule.ServiceCentsPerMinute < 0 {
		return Estimate{}, ErrInvalidPricing
	}
	kwh, err := decimal.NewFromString(energy)
	if err != nil || kwh.LessThan(decimal.New(1, -3)) || kwh.GreaterThan(decimal.NewFromInt(100)) {
		return Estimate{}, ErrInvalidPricing
	}
	wh := kwh.Mul(decimal.NewFromInt(1000)).IntPart()
	if wh < 1 || wh > 100000 {
		return Estimate{}, ErrInvalidPricing
	}
	periodAt, err := compilePeriods(rule.Periods)
	if err != nil {
		return Estimate{}, err
	}
	if rule.Mode != "kwh" && rule.Mode != "minute" && rule.Mode != "mixed" {
		return Estimate{}, ErrInvalidPricing
	}
	slices := make([]finance.EnergySlice, 0, minutes)
	baseWh, remainder := wh/int64(minutes), wh%int64(minutes)
	local := start.In(beijing).Truncate(time.Minute)
	for i := 0; i < int(minutes); i++ {
		minute := local.Add(time.Duration(i) * time.Minute)
		period := periodAt[minute.Hour()*60+minute.Minute()]
		thisWh := baseWh
		if int64(i) < remainder {
			thisWh++
		}
		service := rule.ServiceCentsPerKWh
		if period.ServicePriceCents != nil {
			service = *period.ServicePriceCents
		}
		if rule.Mode == "minute" {
			service = 0
		}
		slices = append(slices, finance.EnergySlice{Energy: finance.EnergyUnit(thisWh * 10), ElectricCentsPerKWh: period.ElectricPriceCents, ServiceCentsPerKWh: service})
	}
	fees, err := finance.PriceEnergy(slices)
	if err != nil {
		return Estimate{}, ErrInvalidPricing
	}
	serviceCents := int64(fees.ServiceCents)
	if rule.Mode == "minute" || rule.Mode == "mixed" {
		if rule.ServiceCentsPerMinute > math.MaxInt64/int64(minutes) || serviceCents > math.MaxInt64-rule.ServiceCentsPerMinute*int64(minutes) {
			return Estimate{}, ErrInvalidPricing
		}
		serviceCents += rule.ServiceCentsPerMinute * int64(minutes)
	}
	if int64(fees.ElectricCents) > math.MaxInt64-serviceCents {
		return Estimate{}, ErrInvalidPricing
	}
	total := int64(fees.ElectricCents) + serviceCents
	if total < rule.MinimumCents {
		serviceCents += rule.MinimumCents - total
		total = rule.MinimumCents
	}
	if total <= 0 {
		return Estimate{}, ErrInvalidPricing
	}
	return Estimate{EstimatedKWh: kwh.StringFixed(3), EstimatedMinutes: minutes,
		ElectricCents: int64(fees.ElectricCents), ServiceCents: serviceCents, TotalCents: total,
		ChargeMode: 0, ChargeQuantity: minutes}, nil
}

func compilePeriods(periods []Period) ([1440]Period, error) {
	var schedule [1440]Period
	var filled [1440]bool
	for _, period := range periods {
		start, err := clockMinute(period.Start)
		if err != nil || start == 1440 {
			return schedule, ErrInvalidPricing
		}
		end, err := clockMinute(period.End)
		if err != nil || period.ElectricPriceCents < 0 || period.ServicePriceCents != nil && *period.ServicePriceCents < 0 {
			return schedule, ErrInvalidPricing
		}
		if end == start {
			return schedule, ErrInvalidPricing
		}
		if end < start {
			end += 1440
		}
		if end-start > 1440 {
			return schedule, ErrInvalidPricing
		}
		for minute := start; minute < end; minute++ {
			index := minute % 1440
			if filled[index] {
				return schedule, ErrInvalidPricing
			}
			filled[index] = true
			schedule[index] = period
		}
	}
	for _, isFilled := range filled {
		if !isFilled {
			return schedule, ErrInvalidPricing
		}
	}
	return schedule, nil
}

func clockMinute(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' || value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' || value[3] < '0' || value[3] > '9' || value[4] < '0' || value[4] > '9' {
		return 0, ErrInvalidPricing
	}
	hour, err := strconv.Atoi(value[:2])
	if err != nil {
		return 0, ErrInvalidPricing
	}
	minute, err := strconv.Atoi(value[3:])
	if err != nil || hour > 24 || minute > 59 || hour == 24 && minute != 0 || hour < 0 || minute < 0 {
		return 0, ErrInvalidPricing
	}
	return hour*60 + minute, nil
}

// ValidateRule is shared by publication and pricing so an accepted tariff can
// actually be used for payment estimates. Limits keep all monetary products safe.
func ValidateRule(rule Rule) error {
	if rule.Mode != "kwh" && rule.Mode != "minute" && rule.Mode != "mixed" {
		return ErrInvalidPricing
	}
	for _, amount := range []int64{rule.ServiceCentsPerKWh, rule.ServiceCentsPerMinute, rule.MinimumCents} {
		if amount < 0 || amount > 1000000 {
			return ErrInvalidPricing
		}
	}
	if len(rule.Periods) == 0 || len(rule.Periods) > 48 {
		return ErrInvalidPricing
	}
	for _, period := range rule.Periods {
		if period.ElectricPriceCents > 1000000 || period.ServicePriceCents != nil && *period.ServicePriceCents > 1000000 {
			return ErrInvalidPricing
		}
	}
	_, err := compilePeriods(rule.Periods)
	return err
}
