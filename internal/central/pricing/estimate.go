package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"gorm.io/gorm"
)

// Rule is the published, station-bound copy of a Spec. Settlement and
// estimation both read the Spec and hand it to Cost, so there is exactly one
// place where money is computed.
type Rule struct {
	ID        uint64 `json:"rule_id"`
	StationID uint64 `json:"station_id"`
	Version   uint32 `json:"version"`
	Spec      Spec   `json:"spec"`
	// Channel records how this station's sessions start, selecting rate
	// multipliers; it is the rule default, not a per-session override.
	Channel Channel `json:"channel,omitempty"`
}

type Estimate struct {
	EstimatedKWh     string `json:"estimated_kwh"`
	EstimatedMinutes uint16 `json:"estimated_minutes"`
	Basis            Basis  `json:"basis"`
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
		Select(`r.id, r.station_id, r.spec_json, r.channel, r.version`).
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
	var spec Spec
	if json.Unmarshal(row.SpecJSON, &spec) != nil || ValidateSpec(spec) != nil {
		return Rule{}, ErrInvalidPricing
	}
	return Rule{ID: row.ID, StationID: stationID, Version: row.Version, Spec: spec, Channel: row.Channel}, nil
}

type pricingRuleRow struct {
	ID        uint64  `gorm:"column:id"`
	StationID uint64  `gorm:"column:station_id"`
	SpecJSON  []byte  `gorm:"column:spec_json"`
	Channel   Channel `gorm:"column:channel"`
	Version   uint32  `gorm:"column:version"`
}

// EstimateCharge spreads the requested energy uniformly across the requested
// minutes and prices it with Cost. The spreading is an admission, not a
// measurement: a quote assumes a flat draw, and the eventual invoice is
// priced from the metered profile through the same function.
func EstimateCharge(rule Rule, energy string, minutes uint16, start time.Time) (Estimate, error) {
	if rule.ID == 0 || rule.Spec.Basis == "" || minutes == 0 || minutes > 600 {
		return Estimate{}, ErrInvalidPricing
	}
	if ValidateSpec(rule.Spec) != nil {
		return Estimate{}, ErrInvalidPricing
	}
	if !energyPattern.MatchString(energy) {
		return Estimate{}, ErrInvalidPricing
	}
	kwh, err := parseEnergy(energy)
	if err != nil {
		return Estimate{}, err
	}
	wh := kwh.Mul(decimalThousand).IntPart()
	if wh < 1 || wh > 100000 {
		return Estimate{}, ErrInvalidPricing
	}
	usage := Usage{Start: start, End: start.Add(time.Duration(minutes) * time.Minute), EnergyWh: uint64(wh), Channel: rule.Channel}
	base := wh / int64(minutes)
	remainder := wh % int64(minutes)
	for i := 0; i < int(minutes); i++ {
		energy := base
		if int64(i) < remainder {
			energy++
		}
		usage.Samples = append(usage.Samples, Sample{
			Start:    start.Add(time.Duration(i) * time.Minute),
			End:      start.Add(time.Duration(i+1) * time.Minute),
			EnergyWh: uint64(energy),
		})
	}
	fee, err := Cost(rule.Spec, usage)
	if err != nil {
		return Estimate{}, ErrInvalidPricing
	}
	if fee.TotalCents <= 0 {
		return Estimate{}, ErrInvalidPricing
	}
	return Estimate{EstimatedKWh: kwh.StringFixed(3), EstimatedMinutes: minutes, Basis: rule.Spec.Basis,
		ElectricCents: fee.ElectricCents, ServiceCents: fee.ServiceCents, TotalCents: fee.TotalCents,
		ChargeMode: 0, ChargeQuantity: minutes}, nil
}
