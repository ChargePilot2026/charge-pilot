package admin

import (
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"gorm.io/gorm"
	"strings"
)

type deviceCapability struct {
	ProtocolAdapter       string `gorm:"column:protocol_adapter"`
	DeviceID              string `gorm:"column:device_id"`   // 全局唯一设备号。
	ChargeMode            string `gorm:"column:charge_mode"` // 设备充电类型（厂商协议值），空串表示尚未归类；另有取值 none 仅用于任务日志
	ReportsEnergy         bool   `gorm:"-"`                  // 是否在结束充电帧里上报电量，决定能否按电量计费
	ReportsSegmentedPower bool   `gorm:"-"`                  // 是否上报分段功率表，决定能否按功率计费
}

func capabilityBlock(mode pricing.ChargeMode, cap deviceCapability) string {
	abilities, err := pricing.ProtocolCapabilities(cap.ProtocolAdapter)
	if err != nil {
		return err.Error()
	}
	cap.ReportsEnergy, cap.ReportsSegmentedPower = abilities.ReportsEnergy, abilities.ReportsSegmentedPower
	switch mode {
	case pricing.ModeDeviceDuration:
		// 设备端时长计费由固件倒计时并停机，不要求电量或分段功率上报。
		return ""
	case pricing.ModeServerEnergy, pricing.ModeDeviceEnergy:
		if !cap.ReportsEnergy {
			return "设备协议不支持电量上报，无法按电量计费"
		}
	case pricing.ModeServerRealtimePower, pricing.ModeServerMaxPower, pricing.ModeDevicePower:
		if !cap.ReportsSegmentedPower {
			return "设备协议不支持分段功率上报，无法按功率计费"
		}
	}
	return ""
}

func validDeviceChargeMode(mode string) bool {
	return pricing.ChargeMode(strings.TrimSpace(mode)).Valid()
}

func unmarshalSpec(raw []byte, spec *pricing.Spec) error {
	if len(raw) == 0 {
		return errors.New("计费口径为空")
	}
	return json.Unmarshal(raw, spec)
}

func stationModeOf(tx *gorm.DB, stationID uint64) (pricing.ChargeMode, bool, error) {
	// row 只取 spec_json 一列；口径为空时下面 unmarshalSpec 会返回错误。
	row := struct{ SpecJSON []byte }{}
	err := tx.Table("pricing_rule").
		Select("spec_json").
		Where("station_id=? AND device_id IS NULL AND status='active' AND deleted_at IS NULL", stationID).
		Order("version DESC").Limit(1).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var spec pricing.Spec
	if unmarshalSpec(row.SpecJSON, &spec) != nil {
		return "", false, nil
	}
	return spec.Mode, true, nil
}

func checkImportAgainstStation(tx *gorm.DB, devices []ImportDevice) error {
	cache := map[uint64]pricing.ChargeMode{}
	for _, d := range devices {
		mode, ok := cache[d.StationID]
		if !ok {
			found, exists, err := stationModeOf(tx, d.StationID)
			if err != nil {
				return err
			}
			mode, ok = found, exists
			cache[d.StationID] = found
		}
		if !ok {
			continue
		}
		cap := deviceCapability{
			DeviceID:              d.DeviceID,
			ProtocolAdapter:       d.ProtocolAdapter,
			ChargeMode:            d.ChargeMode,
			ReportsEnergy:         d.ReportsEnergy,
			ReportsSegmentedPower: d.ReportsSegmentedPower,
		}
		if reason := capabilityBlock(mode, cap); reason != "" {
			return &errMeteringBlocked{device: d.DeviceID, station: d.StationID, mode: mode, reason: reason}
		}
	}
	return nil
}

type errMeteringBlocked struct {
	device  string             // 进不去的设备号
	station uint64             // 目标站点 id
	mode    pricing.ChargeMode // 目标站点默认的计费方式
	reason  string             // 缺哪项能力的中文说明
}

func (e *errMeteringBlocked) Error() string {
	return "设备 " + e.device + " 无法执行该站点的计费方式：" + e.reason
}

func newDevicesOnly(tx *gorm.DB, devices []ImportDevice) ([]ImportDevice, error) {
	if len(devices) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.DeviceID)
	}
	var known []string
	if err := tx.Table("device_meta").Where("device_id IN ? AND deleted_at IS NULL", ids).
		Pluck("device_id", &known).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(known))
	for _, id := range known {
		seen[id] = true
	}
	arriving := make([]ImportDevice, 0, len(devices))
	for _, d := range devices {
		if !seen[d.DeviceID] {
			arriving = append(arriving, d)
		}
	}
	return arriving, nil
}
