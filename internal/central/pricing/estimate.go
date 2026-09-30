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

// Rule 是一份已发布、绑定到站点的 Spec 副本。结算与预估都读取这份 Spec 并把它
// 交给 Cost，所以全系统只有一处会计算钱。
type Rule struct {
	ID        uint64 `json:"rule_id"`
	StationID uint64 `json:"station_id"`
	// DeviceID 为空表示这条是整站默认规则。
	DeviceID string `json:"device_id,omitempty"`
	Version  uint32 `json:"version"`
	Spec     Spec   `json:"spec"`
	// Channel 记录本站点的充电从哪个入口发起，据此选择费率倍率；
	// 它是这条规则的默认值，不是每次充电的临时覆盖。
	Channel Channel `json:"channel,omitempty"`
}

type Estimate struct {
	EstimatedKWh     string     `json:"estimated_kwh"`
	EstimatedMinutes uint16     `json:"estimated_minutes"`
	Mode             ChargeMode `json:"mode"`
	// Basis 在设备计费模式下为空，而空本身就是答案：
	// 那条路径上根本没有可供估算的费率。
	Basis         ServerBasis `json:"basis,omitempty"`
	ElectricCents int64       `json:"electric_cents"`
	ServiceCents  int64       `json:"service_cents"`
	TotalCents    int64       `json:"total_cents"`
	// PrepaidCents 只在设备计费模式下有值，是支付时已经收走的金额，
	// 它永远不是从费率推出来的。
	PrepaidCents   int64  `json:"prepaid_cents,omitempty"`
	ChargeMode     uint8  `json:"charge_mode"`
	ChargeQuantity uint16 `json:"charge_quantity"`
}

type Store struct{ DB *gorm.DB }

// activeRuleQuery 是唯一决定一次充电按哪条已发布规则计价的地方，
// 于是可选套餐、报价与最终账单不可能对「当时生效的是哪份电价表」产生分歧。
func (s Store) activeRuleQuery(ctx context.Context, stationID uint64, deviceID string) *gorm.DB {
	query := s.DB.WithContext(ctx).Table("pricing_rule AS r").
		Select(`r.id, r.station_id, r.device_id, r.spec_json, r.channel, r.version`).
		Joins("JOIN station AS s ON s.id = r.station_id").
		Where(`r.station_id = ? AND s.status = 'active' AND s.deleted_at IS NULL
			AND r.status = 'active' AND r.deleted_at IS NULL
			AND (r.effective_from IS NULL OR r.effective_from <= NOW(3))
			AND (r.effective_to IS NULL OR r.effective_to > NOW(3))`, stationID)
	if deviceID != "" {
		// 设备规则覆盖整站默认规则；从未被单独定过价的设备沿用站点在跑的那份。
		query = query.Where(`r.device_id = ? OR r.device_id IS NULL`, deviceID)
	} else {
		// 整站口径只认 device_id 为空的那条规则。不加这一句，站点一旦没有整站规则、
		// 却有设备规则，这里就会把某台设备的费率当成整站费率报出去。
		query = query.Where(`r.device_id IS NULL`)
	}
	return query.Order("r.device_id IS NULL ASC, r.version DESC, r.id DESC")
}

// ActiveStationRule 按整站电价表给一次充电计价。
func (s Store) ActiveStationRule(ctx context.Context, stationID uint64) (Rule, error) {
	return s.ActiveDeviceRule(ctx, stationID, "")
}

// ActiveDeviceRule 按那台设备自己的电价表给一次充电计价，设备没有单独规则时
// 回落到整站默认。这个回落正是整站统一定价能一次点完、同时又允许某个桩
// 单独跑另一套费率的原因。
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

// EstimateCharge 把请求的电量在请求的分钟数上均匀铺开，再用 Cost 计价。
// 这种铺开是一个让步而不是一次测量：报价假定取电是平的，而最终发票是由计量
// 曲线经同一个函数算出来的。
//
// 在设备计费模式下没有可供铺开的费率，所以估算值就是预付金额本身，别无其他。
// 在那里返回一个算出来的数字，会让充电用户看到一个数、却被扣掉另一个数。
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
