package pricing

import (
	"context"
	"database/sql"
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
	// DeviceID is empty for the yard-wide default rule.
	DeviceID string `json:"device_id,omitempty"`
	Version  uint32 `json:"version"`
	Spec     Spec   `json:"spec"`
	// Channel records how this station's sessions start, selecting rate
	// multipliers; it is the rule default, not a per-session override.
	Channel Channel `json:"channel,omitempty"`
}

type Estimate struct {
	EstimatedKWh     string     `json:"estimated_kwh"`
	EstimatedMinutes uint16     `json:"estimated_minutes"`
	Mode             ChargeMode `json:"mode"`
	// Basis is empty on a device-billed mode, which is itself the answer: there
	// is no rate to estimate against.
	Basis         ServerBasis `json:"basis,omitempty"`
	ElectricCents int64       `json:"electric_cents"`
	ServiceCents  int64       `json:"service_cents"`
	TotalCents    int64       `json:"total_cents"`
	// PrepaidCents is set on a device-billed mode and is the amount already
	// collected at payment. It is never derived from a rate.
	PrepaidCents   int64  `json:"prepaid_cents,omitempty"`
	ChargeMode     uint8  `json:"charge_mode"`
	ChargeQuantity uint16 `json:"charge_quantity"`
}

type Store struct{ DB *gorm.DB }

// activeRuleQuery is the one place that decides which published rule a session
// is priced under, so the offer list, the quote and the eventual bill can never
// disagree about which tariff was in force.
func (s Store) activeRuleQuery(ctx context.Context, stationID uint64, deviceID string) *gorm.DB {
	query := s.DB.WithContext(ctx).Table("pricing_rule AS r").
		Select(`r.id, r.station_id, r.device_id, r.spec_json, r.channel, r.version`).
		Joins("JOIN station AS s ON s.id = r.station_id").
		Where(`r.station_id = ? AND s.status = 'active' AND s.deleted_at IS NULL
			AND r.status = 'active' AND r.deleted_at IS NULL
			AND (r.effective_from IS NULL OR r.effective_from <= NOW(3))
			AND (r.effective_to IS NULL OR r.effective_to > NOW(3))`, stationID)
	if deviceID != "" {
		// A device rule overrides the yard default; a device that has never been
		// assigned one inherits whatever its station runs.
		query = query.Where(`r.device_id = ? OR r.device_id IS NULL`, deviceID)
	}
	return query.Order("r.device_id IS NULL ASC, r.version DESC, r.id DESC")
}

// ActiveStationRule prices a session under the yard-wide tariff.
func (s Store) ActiveStationRule(ctx context.Context, stationID uint64) (Rule, error) {
	return s.ActiveDeviceRule(ctx, stationID, "")
}

// ActiveDeviceRule prices a session under that device's own tariff, falling
// back to the yard default. The fallback is what keeps a station-wide rollout a
// single click while still allowing one pile to run something different.
func (s Store) ActiveDeviceRule(ctx context.Context, stationID uint64, deviceID string) (Rule, error) {
	if s.DB == nil || stationID == 0 || stationID > math.MaxInt64 {
		return Rule{}, ErrRuleUnavailable
	}
	var row pricingRuleRow
	result := s.activeRuleQuery(ctx, stationID, deviceID).Take(&row)
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
	device := row.DeviceID.String
	return Rule{ID: row.ID, StationID: stationID, DeviceID: device, Version: row.Version, Spec: spec, Channel: row.Channel}, nil
}

type pricingRuleRow struct {
	ID        uint64         `gorm:"column:id"`
	StationID uint64         `gorm:"column:station_id"`
	DeviceID  sql.NullString `gorm:"column:device_id"`
	SpecJSON  []byte         `gorm:"column:spec_json"`
	Channel   Channel        `gorm:"column:channel"`
	Version   uint32         `gorm:"column:version"`
}

// EstimateCharge spreads the requested energy uniformly across the requested
// minutes and prices it with Cost. The spreading is an admission, not a
// measurement: a quote assumes a flat draw, and the eventual invoice is priced
// from the metered profile through the same function.
//
// On a device-billed mode there is no rate to spread against, so the estimate
// is the prepaid amount and nothing else. Handing back a computed figure there
// would show the rider one number and charge another.
func EstimateCharge(rule Rule, energy string, minutes uint16, start time.Time, prepaidCents int64) (Estimate, error) {
	if rule.ID == 0 || !rule.Spec.Mode.Valid() || minutes == 0 || minutes > 600 {
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
	if !rule.Spec.Mode.ServerBilled() {
		if prepaidCents < 0 || prepaidCents > maxRateCents {
			return Estimate{}, ErrInvalidPricing
		}
		return Estimate{EstimatedKWh: kwh.StringFixed(3), EstimatedMinutes: minutes, Mode: rule.Spec.Mode,
			TotalCents: prepaidCents, PrepaidCents: prepaidCents, ChargeQuantity: minutes}, nil
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
	return Estimate{EstimatedKWh: kwh.StringFixed(3), EstimatedMinutes: minutes, Mode: rule.Spec.Mode, Basis: fee.Basis,
		ElectricCents: fee.ElectricCents, ServiceCents: fee.ServiceCents, TotalCents: fee.TotalCents,
		ChargeMode: 0, ChargeQuantity: minutes}, nil
}
