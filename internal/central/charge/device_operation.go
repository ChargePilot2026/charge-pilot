package charge

import (
	"context"
	"errors"
	"gorm.io/gorm"
)

// The admin database owns operational state; the gateway owns connectivity.
type DeviceOperationReader interface {
	DeviceStatus(context.Context, string) (string, error)
}

type DeviceOperationStore struct{ DB *gorm.DB }

func (s DeviceOperationStore) DeviceStatus(ctx context.Context, device string) (string, error) {
	if s.DB == nil {
		return "", errors.New("device operation storage unavailable")
	}
	var row struct{ Status string }
	err := s.DB.WithContext(ctx).Table("device_meta").Select("status").Where("device_id=? AND deleted_at IS NULL", device).Take(&row).Error
	return row.Status, err
}

func applyDeviceStatus(result *ScanResult, status string) {
	result.DeviceStatus = status
	apply := func(p *ScanPort) {
		p.DeviceStatus = status
		p.Available = p.Available && status == "enabled"
	}
	if result.Port != nil {
		apply(result.Port)
	}
	for i := range result.Ports {
		apply(&result.Ports[i])
	}
}
