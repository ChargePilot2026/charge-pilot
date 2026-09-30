package pricing

import (
	"context"
	"errors"
)

// Offer is derived from exactly one applied scheme. Prices and billing rules
// cannot be independently published.
type Offer struct {
	ServerDuration  bool   `json:"server_duration,omitempty"`
	PackageID       uint64 `json:"package_id,omitempty"`
	PurchaseCount   uint16 `json:"purchase_count,omitempty"`
	EnergyWh        uint32 `json:"energy_wh,omitempty"`
	MaxMinutes      uint16 `json:"max_minutes,omitempty"`
	ID              uint64 `json:"id"`
	StationID       uint64 `json:"station_id"`
	DeviceID        string `json:"device_id,omitempty"`
	Name            string `json:"name"`
	Mode            string `json:"mode"`
	PriceCents      int64  `json:"price_cents"`
	DurationMinutes uint16 `json:"duration_minutes"`
}

var ErrOfferUnavailable = errors.New("charging offer unavailable")

func (o Offer) Valid() bool {
	count := int64(o.PurchaseCount)
	if count == 0 {
		count = 1
	}
	return (!o.ServerDuration || o.Mode == "duration") && o.ID > 0 && o.StationID > 0 && o.Name != "" && o.PriceCents > 0 && o.PriceCents <= maxRateCents*count &&
		(o.Mode == "amount" && o.DurationMinutes == 0 && o.EnergyWh == 0 || o.Mode == "duration" && o.DurationMinutes > 0 && o.DurationMinutes <= 4320 && o.EnergyWh == 0 || o.Mode == "energy" && o.DurationMinutes == 0 && o.EnergyWh >= 1000 && o.EnergyWh <= 65000 && o.EnergyWh%1000 == 0)
}
func (s Store) ActiveOffers(ctx context.Context, station uint64, device string) ([]Offer, error) {
	rule, err := s.ActiveDeviceRule(ctx, station, device)
	if err != nil {
		return nil, err
	}
	return rule.Spec.Scheme.Offers(rule), nil
}
func (s Store) ActiveOffer(ctx context.Context, station uint64, device string, id uint64) (Offer, error) {
	rows, err := s.ActiveOffers(ctx, station, device)
	if err != nil {
		return Offer{}, err
	}
	for _, o := range rows {
		if o.ID == id {
			return o, nil
		}
	}
	return Offer{}, ErrOfferUnavailable
}
