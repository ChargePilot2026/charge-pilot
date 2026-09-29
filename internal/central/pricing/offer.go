package pricing

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

type Offer struct {
	ID        uint64 `json:"id" gorm:"column:id"`
	StationID uint64 `json:"station_id" gorm:"column:station_id"`
	// DeviceID is empty for an offer sold across the whole yard.
	DeviceID        string `json:"device_id,omitempty" gorm:"column:device_id"`
	Code            string `json:"code" gorm:"column:code"`
	Name            string `json:"name" gorm:"column:name"`
	Mode            string `json:"mode" gorm:"column:mode"`
	PriceCents      int64  `json:"price_cents" gorm:"column:price_cents"`
	DurationMinutes uint16 `json:"duration_minutes" gorm:"column:duration_minutes"`
}

var ErrOfferUnavailable = errors.New("charging offer unavailable")

func (o Offer) Valid() bool {
	return o.ID != 0 && o.StationID != 0 && o.Code != "" && o.Name != "" && o.PriceCents >= 0 && o.PriceCents <= 1000000 &&
		(o.Mode == "amount" && o.DurationMinutes == 0 || o.Mode == "package" && o.DurationMinutes > 0 && o.DurationMinutes <= 600)
}

// ActiveOffers lists what a rider can pick on this device: the ones assigned to
// it, plus the yard-wide ones it has not overridden.
func (s Store) ActiveOffers(ctx context.Context, stationID uint64, deviceID string) ([]Offer, error) {
	if s.DB == nil || stationID == 0 {
		return nil, ErrOfferUnavailable
	}
	rows := []Offer{}
	query := s.DB.WithContext(ctx).Table("charge_offer").
		Where("station_id=? AND status='active'", stationID)
	if deviceID != "" {
		query = query.Where("device_id = ? OR device_id IS NULL", deviceID)
	}
	if err := query.Order("device_id IS NULL ASC, mode, price_cents, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if !row.Valid() {
			return nil, ErrOfferUnavailable
		}
	}
	return rows, nil
}

func (s Store) ActiveOffer(ctx context.Context, stationID uint64, deviceID string, id uint64) (Offer, error) {
	if s.DB == nil || stationID == 0 || id == 0 {
		return Offer{}, ErrOfferUnavailable
	}
	var row Offer
	result := s.DB.WithContext(ctx).Table("charge_offer").
		Where("id=? AND station_id=? AND status='active'", id, stationID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return Offer{}, ErrOfferUnavailable
	}
	if result.Error != nil {
		return Offer{}, result.Error
	}
	// An offer assigned to a different device is not for sale here, even
	// though the row itself is active.
	if deviceID != "" && row.DeviceID != "" && row.DeviceID != deviceID {
		return Offer{}, ErrOfferUnavailable
	}
	if !row.Valid() {
		return Offer{}, ErrOfferUnavailable
	}
	return row, nil
}
