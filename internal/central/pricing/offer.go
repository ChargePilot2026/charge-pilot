package pricing

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

type Offer struct {
	ID        uint64 `json:"id" gorm:"column:id"`
	StationID uint64 `json:"station_id" gorm:"column:station_id"`
	// DeviceID 为空表示这个套餐是整站发售的。
	DeviceID        string `json:"device_id,omitempty" gorm:"column:device_id"`
	Name            string `json:"name" gorm:"column:name"`
	Mode            string `json:"mode" gorm:"column:mode"`
	PriceCents      int64  `json:"price_cents" gorm:"column:price_cents"`
	DurationMinutes uint16 `json:"duration_minutes" gorm:"column:duration_minutes"`
}

var ErrOfferUnavailable = errors.New("charging offer unavailable")

func (o Offer) Valid() bool {
	return o.ID != 0 && o.StationID != 0 && o.Name != "" && o.PriceCents > 0 && o.PriceCents <= 1000000 &&
		(o.Mode == "amount" && o.DurationMinutes == 0 || o.Mode == "package" && o.DurationMinutes > 0 && o.DurationMinutes <= 600)
}

// ActiveOffers 列出充电用户能在这台设备上选的套餐：挂在它上面的那些，
// 加上它自己没有覆盖掉的整站套餐。
func (s Store) ActiveOffers(ctx context.Context, stationID uint64, deviceID string) ([]Offer, error) {
	if s.DB == nil || stationID == 0 {
		return nil, ErrOfferUnavailable
	}
	rows := []Offer{}
	// 下架行在这里被过滤掉，因为这一列存在的意义正是让一个套餐可以下架而不
	// 丢掉它的历史——而后台列表一直都有这个过滤。充电用户这一侧的读取如果
	// 忽略这一列，就会继续在卖一个运营方以为已经没了的东西。
	query := s.DB.WithContext(ctx).Table("charge_offer").
		Where("station_id=? AND status='active' AND deleted_at IS NULL AND (mode <> 'package' OR price_cents > 0)", stationID)
	if deviceID != "" {
		query = query.Where("device_id = ? OR device_id IS NULL", deviceID)
		// 整站套餐如果这台设备自己也在单卖，就以设备自己那份为准。两条都列出来
		// 会让同一个套餐在充电用户的列表里出现两次，这正是上面那句话一向说的
		// 不能发生的情况。
		query = query.Where(`device_id = ? OR package_template_id NOT IN (
			SELECT package_template_id FROM charge_offer
			WHERE station_id = ? AND device_id = ? AND status = 'active' AND deleted_at IS NULL
            AND (mode <> 'package' OR price_cents > 0)
		)`, deviceID, stationID, deviceID)
	}
	if err := query.Order("device_id IS NULL ASC, mode, price_cents, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	available := make([]Offer, 0, len(rows))
	for _, row := range rows {
		// 旧编辑器曾发布无售价的时长套餐；保留后台可修复，用户端不再售卖。
		if row.Mode == "package" && row.PriceCents <= 0 {
			continue
		}
		if !row.Valid() {
			return nil, ErrOfferUnavailable
		}
		available = append(available, row)
	}
	return available, nil
}

func (s Store) ActiveOffer(ctx context.Context, stationID uint64, deviceID string, id uint64) (Offer, error) {
	if s.DB == nil || stationID == 0 || id == 0 {
		return Offer{}, ErrOfferUnavailable
	}
	var row Offer
	result := s.DB.WithContext(ctx).Table("charge_offer").
		Where("id=? AND station_id=? AND status='active' AND deleted_at IS NULL", id, stationID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return Offer{}, ErrOfferUnavailable
	}
	if result.Error != nil {
		return Offer{}, result.Error
	}
	// 挂在别的设备上的套餐在这里不算在售，哪怕这行本身是 active 的。
	if deviceID != "" && row.DeviceID != "" && row.DeviceID != deviceID {
		return Offer{}, ErrOfferUnavailable
	}
	if !row.Valid() {
		return Offer{}, ErrOfferUnavailable
	}
	return row, nil
}
