package pricing

import (
	"context"
	"encoding/json"
	"fmt"
)

// CheckDeviceScheme covers devices added after station application as well as
// later firmware/capability changes. Frozen paid orders do not reread rates.
func (s Store) CheckDeviceScheme(ctx context.Context, station uint64, device string, scheme Scheme) error {
	var d struct {
		ExecutionCapabilities []byte
		ReportsEnergy         bool
		ReportsSegmentedPower bool
	}
	if err := s.DB.WithContext(ctx).Table("device_meta").Where("station_id=? AND device_id=? AND deleted_at IS NULL", station, device).Take(&d).Error; err != nil {
		return err
	}
	var cap Capabilities
	if json.Unmarshal(d.ExecutionCapabilities, &cap) != nil {
		return fmt.Errorf("设备尚未核验执行能力")
	}
	if err := scheme.ValidateCapabilities(cap); err != nil {
		return err
	}
	if scheme.Energy != nil || scheme.Amount != nil && scheme.Amount.Algorithm == ModeServerEnergy {
		if !d.ReportsEnergy {
			return fmt.Errorf("设备缺少可靠电量计量")
		}
	}
	if scheme.Amount != nil && scheme.Amount.Algorithm != ModeServerEnergy && !d.ReportsSegmentedPower {
		return fmt.Errorf("设备缺少可靠分段功率计量")
	}
	return nil
}
