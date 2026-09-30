package pricing

import (
	"context"
	"fmt"
)

// ProtocolCapabilities defines the adapter contract. Provisioning with this
// protocol establishes compatibility; operators cannot override its abilities.
func ProtocolCapabilities(adapter string) (Capabilities, error) {
	switch adapter {
	case "dc589":
		return Capabilities{Duration: true, Energy: true, MaxMinutes: 4320,
			OnlineCard: true, StopWhenFull: true, ReportsEnergy: true, ReportsSegmentedPower: true}, nil
	default:
		return Capabilities{}, fmt.Errorf("不支持的设备协议：%s", adapter)
	}
}

func (s Store) DeviceCapabilities(ctx context.Context, station uint64, device string) (string, Capabilities, error) {
	var d struct{ ProtocolAdapter string }
	if err := s.DB.WithContext(ctx).Table("device_meta").Select("protocol_adapter").Where("station_id=? AND device_id=? AND deleted_at IS NULL", station, device).Take(&d).Error; err != nil {
		return "", Capabilities{}, err
	}
	cap, err := ProtocolCapabilities(d.ProtocolAdapter)
	return d.ProtocolAdapter, cap, err
}

// Frozen paid orders retain their rates; new orders use the selected protocol.
func (s Store) CheckDeviceScheme(ctx context.Context, station uint64, device string, scheme Scheme) error {
	_, cap, err := s.DeviceCapabilities(ctx, station, device)
	if err != nil {
		return err
	}
	return scheme.ValidateCapabilities(cap)
}
