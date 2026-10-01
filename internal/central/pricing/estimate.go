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

// Rule 保存已发布且关联站点的 Spec；预估与结算复用同一份规则及 Cost 算法。
type Rule struct {
	ID        uint64 `json:"rule_id"`
	StationID uint64 `json:"station_id"`
	// DeviceID 为空表示这条是整站默认规则。
	DeviceID string `json:"device_id,omitempty"`
	Version  uint32 `json:"version"`
	Spec     Spec   `json:"spec"`
	// Channel 是规则默认的充电启动入口，用于选择费率倍率。
	Channel Channel `json:"channel,omitempty"`
}

type Estimate struct {
	EstimatedKWh     string     `json:"estimated_kwh"`
	EstimatedMinutes uint16     `json:"estimated_minutes"`
	Mode             ChargeMode `json:"mode"`
	// Basis 在设备计费模式下为空，该模式不使用服务端费率估算。
	Basis         ServerBasis `json:"basis,omitempty"`
	ElectricCents int64       `json:"electric_cents"`
	ServiceCents  int64       `json:"service_cents"`
	TotalCents    int64       `json:"total_cents"`
	// PrepaidCents 为设备计费已收的预付款，单位为分，不由服务端费率推算。
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
		// 设备专属规则优先于站点默认规则；无专属规则时使用站点规则。
		query = query.Where(`r.device_id = ? OR r.device_id IS NULL`, deviceID)
	} else {
		// 仅 device_id 为空的规则代表站点默认，避免将设备覆盖规则误用为全站规则。
		query = query.Where(`r.device_id IS NULL`)
	}
	return query.Order("r.device_id IS NULL ASC, r.version DESC, r.id DESC")
}

// ActiveStationRule 按整站电价表给一次充电计价。
func (s Store) ActiveStationRule(ctx context.Context, stationID uint64) (Rule, error) {
	return s.ActiveDeviceRule(ctx, stationID, "")
}

// ActiveDeviceRule 优先读取设备覆盖规则，未配置时使用站点默认规则。
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
	if json.Unmarshal(row.SpecJSON, &spec) != nil || spec.Scheme == nil || spec.Scheme.Validate() != nil {
		return Rule{}, ErrInvalidPricing
	}
	// The rule keeps the full scheme. The selected package supplies the execution
	// view later, within the same frozen rule read.
	spec = spec.Scheme.SpecFor(spec.Scheme.Packages[0])
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

// EstimateCharge 将预计电量均匀分配到请求时长，再调用 Cost 计算报价。
// 报价使用均匀负载假设，最终结算以实际计量为准；设备计费直接返回预付款金额。
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
