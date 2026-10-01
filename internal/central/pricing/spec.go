package pricing

import (
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrInvalidPricing  = errors.New("invalid quote input or pricing rule")
	ErrMeterReview     = errors.New("实际计量不足或矛盾，需补充分时计量后核算")
	ErrRuleUnavailable = errors.New("active station pricing rule unavailable")
	ErrLegacyPricing   = errors.New("实时功率模板使用旧版电价，请先编辑并转换为元/度后再保存、复制或应用")

	beijing = time.FixedZone("Asia/Shanghai", 8*3600)
)

const maxRateCents = 1000000

// Tier 表示功率阶梯的一个档位，仅保存上界；下界由上一档上界加 1 W 推导。
// 连续上界表示保证阶梯不重叠、不留计价空档。
type Tier struct {
	// MaxWatts 是含端点的上界。第一档下限为 0，各档上界必须严格递增。
	MaxWatts int `json:"max_watts"`
	// ElectricCents 在两种功率算法中均为每小时的分。
	ElectricCents int64 `json:"electric_cents"`
	// ServiceCents 是同一档位的每小时服务费。
	ServiceCents int64 `json:"service_cents,omitempty"`
}

// Period 表示一天中的费率时段，以严格递增的结束分钟组成连续覆盖链。
type Period struct {
	ServiceCents int64 `json:"service_cents,omitempty"`
	// EndMinute 是从零点起的分钟数，取值 1..1440。数值必须严格递增，且最后一个
	// 必须正好是 1440，这样一整天才被完整覆盖，既没有空档也没有重叠。
	EndMinute int `json:"end_minute"`
	// ElectricCents 是电量计费的单一电价，单位分/kWh；该口径不使用阶梯。
	ElectricCents int64 `json:"electric_cents,omitempty"`
	// Tiers 是该时段内适用的功率阶梯；电量口径下为空。
	Tiers []Tier `json:"tiers,omitempty"`
}

// ElectricLine 表示服务端电价配置；设备计费模式不配置服务端电价。
type ElectricLine struct {
	Basis   ServerBasis `json:"basis"`
	Periods []Period    `json:"periods"`
}

// ServiceLine 定义服务费算法；nil 和 ServiceNone 均不收费。
// 指针保留未配置与显式配置零费率的区别，供编辑器往返保存。
type ServiceLine struct {
	Basis           ServiceBasis `json:"basis"`
	CentsPerKWh     int64        `json:"cents_per_kwh,omitempty"`
	CentsPerMinute  int64        `json:"cents_per_minute,omitempty"`
	CentsPerSession int64        `json:"cents_per_session,omitempty"`
}

// Channel 是这次充电的启动入口，它决定套用哪一套费率倍率。
type Channel string

const (
	ChannelDefault Channel = "default"
	ChannelTemp    Channel = "temp"
	ChannelCard    Channel = "card"
)

// ChannelMultiplier 以基点表示电费倍率，10000 为 1.0；不影响服务费。
type ChannelMultiplier struct {
	TempBP int32 `json:"temp_bp"`
	CardBP int32 `json:"card_bp"`
}

func (m *ChannelMultiplier) electricBP(channel Channel) int32 {
	if m == nil {
		return 10000
	}
	if channel == ChannelTemp && m.TempBP > 0 {
		return m.TempBP
	}
	if channel == ChannelCard && m.CardBP > 0 {
		return m.CardBP
	}
	return 10000
}

// TimeCharge 保存按时长充电的停止条件，不定义费率。
type TimeCharge struct {
	// StopWhenFull 启用设备充满自停，不等待时长耗尽。
	StopWhenFull bool `json:"stop_when_full"`
	// MaxMinutes 限制按时长计费的最长分钟数，0 表示不限。
	MaxMinutes uint16 `json:"max_minutes,omitempty"`
	// FloatPowerDeciWatts 与 FloatSeconds 描述电池不再接受满电流之后的低电流尾段。
	FloatPowerDeciWatts uint16 `json:"float_power_deci_watts,omitempty"`
	FloatSeconds        uint16 `json:"float_seconds,omitempty"`
}

// Spec 包含完整计价配置，预估与结算共用此结构和 Cost 引擎。
type Spec struct {
	Scheme *Scheme    `json:"scheme,omitempty"`
	Mode   ChargeMode `json:"mode"`
	// Electric 仅用于服务端计费；设备计费必须为零值，ValidateSpec 拒绝无效费率配置。
	Electric *ElectricLine `json:"electric,omitempty"`
	// Service 是独立的第二条线。两条线可以任意组合——按电量收电费配按小时收
	// 服务费就是真实且常见的一种组合。
	Service *ServiceLine `json:"service,omitempty"`
	// Multiplier 是一张独立的费率卡，归电费所有。
	Multiplier *ChannelMultiplier `json:"multiplier,omitempty"`
	// TierPriceBasis 只被 BasisRealtimePower 读取。新模板固定为 TierPerKWh；
	// 旧版值及省略值只为已发布规则和冻结的订单快照保留历史计算行为。
	TierPriceBasis TierPriceBasis `json:"tier_price_basis,omitempty"`
	// LossRateBP 按比例抬高计费电量以覆盖线损，10000 表示无损耗。
	LossRateBP int32 `json:"loss_rate_bp,omitempty"`
	// FreeMinutes 表示充电在这么多分钟内结束时整单免单，0 表示不启用该免单。
	FreeMinutes int `json:"free_minutes,omitempty"`
	// MinElectricCents 为电费最低收费，单位为分，不调整服务费。
	MinElectricCents int64 `json:"min_electric_cents,omitempty"`
	// TimeCharge 只对按时长计费的充电生效。
	TimeCharge *TimeCharge `json:"time_charge,omitempty"`
	// SpendCapCents 是服务端计费的消费上限，单位为分；0 表示未配置。
	// 未配置时仍保留设备下发的时间或电量额度限制。
	SpendCapCents int64 `json:"spend_cap_cents,omitempty"`
	// StopGraceSeconds 是平台要求设备停止之后，等待多久才把这次充电视为失控。
	StopGraceSeconds int `json:"stop_grace_seconds,omitempty"`
	// DefaultChargeWay 是本设备上充电默认的鉴权方式，属于展示与策略，不参与计算。
	DefaultChargeWay string `json:"default_charge_way,omitempty"`
	// CardMaxMinutes 是允许的刷卡充电最长时长。
	CardMaxMinutes uint16 `json:"card_max_minutes,omitempty"`
	// Display 控制用户端呈现，与实际计费参数独立。
	Display Display `json:"display"`
}

// Sample 是一段始终落在同一费率时段内的连续用电。无法保证这一点的调用方必须在
// 调用 Cost 之前先切分。
type Sample struct {
	PowerKnown bool
	Start      time.Time
	End        time.Time
	EnergyWh   uint64
	// PowerW 是这段用电的功率。0 表示「用电量与时长反推」，
	// 预估只能这么做。
	PowerW uint32
}

type Usage struct {
	Start    time.Time
	End      time.Time
	EnergyWh uint64
	Samples  []Sample
	Channel  Channel
}

func (u Usage) channel(spec Spec) Channel {
	if u.Channel != "" {
		return u.Channel
	}
	return ChannelDefault
}

func (u Usage) minutes() int64 {
	if u.Start.IsZero() || u.End.IsZero() || !u.End.After(u.Start) {
		return 0
	}
	return int64(u.End.Sub(u.Start) / time.Minute)
}

// Fee 是计价结果。Basis 原样带回，调用方据此渲染正确的单位，而不必自己
// 再推导一遍。
type Fee struct {
	Fragments     []FeeFragment `json:"fragments,omitempty"`
	Basis         ServerBasis   `json:"basis"`
	ElectricCents int64         `json:"electric_cents"`
	ServiceCents  int64         `json:"service_cents"`
	TotalCents    int64         `json:"total_cents"`
	BillableWh    uint64        `json:"billable_wh"`
}

// ValidateSpec 共用于发布和计费，确保已发布规则满足结算执行条件。
func ValidateSpec(spec Spec) error {
	if !spec.Mode.Valid() {
		return ErrInvalidPricing
	}
	if spec.FreeMinutes < 0 || spec.FreeMinutes > 1440 {
		return ErrInvalidPricing
	}
	if spec.MinElectricCents < 0 || spec.MinElectricCents > maxRateCents {
		return ErrInvalidPricing
	}
	if spec.SpendCapCents < 0 || spec.SpendCapCents > maxRateCents {
		return ErrInvalidPricing
	}
	if spec.StopGraceSeconds < 0 || spec.StopGraceSeconds > 3600 {
		return ErrInvalidPricing
	}
	if spec.LossRateBP < 0 || spec.LossRateBP > 100000 {
		return ErrInvalidPricing
	}
	if spec.Multiplier != nil {
		for _, bp := range []int32{spec.Multiplier.TempBP, spec.Multiplier.CardBP} {
			if bp < 0 || bp > 100000 {
				return ErrInvalidPricing
			}
		}
	}
	if spec.CardMaxMinutes > 4320 {
		// 刷卡时长使用 uint16 分钟字段，超出范围时拒绝，不截断数值。
		return ErrInvalidPricing
	}
	if spec.TimeCharge != nil {
		if spec.TimeCharge.MaxMinutes > 4320 || spec.TimeCharge.FloatSeconds > 10800 || spec.TimeCharge.FloatPowerDeciWatts > 500 {
			return ErrInvalidPricing
		}
	}
	if spec.Service != nil {
		if !spec.Service.Basis.Valid() {
			return ErrInvalidPricing
		}
		for _, amount := range []int64{spec.Service.CentsPerKWh, spec.Service.CentsPerMinute, spec.Service.CentsPerSession} {
			if amount < 0 || amount > maxRateCents {
				return ErrInvalidPricing
			}
		}
	}
	if !spec.Mode.ServerBilled() {
		// 设备计费不使用服务端费率，拒绝携带无效费率的规则。
		if spec.Electric != nil || spec.Service != nil || spec.Multiplier != nil {
			return ErrInvalidPricing
		}
		return nil
	}
	if spec.Electric == nil {
		return ErrInvalidPricing
	}
	if spec.Electric.Basis != spec.Mode.BasisFor() {
		// 计费模式必须与电价口径一致。
		return ErrInvalidPricing
	}
	for _, period := range spec.Electric.Periods {
		if err := validateTiers(spec.Electric.Basis, period); err != nil {
			return err
		}
	}
	_, err := compilePeriods(spec.Electric.Periods)
	return err
}

// ValidateTemplateSpec 校验新写入或新发布的模板。结算仍用 ValidateSpec，
// 避免把旧订单的冻结价格拒掉或按新算法重算。
func ValidateTemplateSpec(spec Spec) error {
	if spec.Scheme != nil {
		return spec.Scheme.Validate()
	}
	if err := ValidateSpec(spec); err != nil {
		return err
	}
	if spec.Service != nil || spec.SpendCapCents != 0 || spec.TierPriceBasis != "" || spec.LossRateBP != 0 || spec.Multiplier != nil {
		return ErrInvalidPricing
	}
	return nil
}

// compilePeriods 将时段链展开为按分钟索引的费率表。
// 结束分钟必须严格递增并覆盖 [0, 1440)，保证全天无空档或重叠。
func compilePeriods(periods []Period) ([1440]Period, error) {
	var schedule [1440]Period
	if len(periods) == 0 || len(periods) > 48 {
		return schedule, ErrInvalidPricing
	}
	cursor := 0
	for _, period := range periods {
		if period.EndMinute <= cursor || period.EndMinute > 1440 {
			return schedule, ErrInvalidPricing
		}
		for minute := cursor; minute < period.EndMinute; minute++ {
			schedule[minute] = period
		}
		cursor = period.EndMinute
	}
	if cursor != 1440 {
		return schedule, ErrInvalidPricing
	}
	return schedule, nil
}

// validateTiers 校验单个时段内部的阶梯链，并阻止两种形态并存。阶梯到底允不允许
// 存在由电量口径决定：电量只有唯一电价，功率口径只有唯一阶梯，而一个同时带
// 两者的时段就是一份运营方解释不了的电价表。
func validateTiers(basis ServerBasis, period Period) error {
	if period.ServiceCents < 0 || period.ServiceCents > maxRateCents {
		return ErrInvalidPricing
	}
	if period.ElectricCents < 0 || period.ElectricCents > maxRateCents {
		return ErrInvalidPricing
	}
	if basis == BasisEnergy {
		if len(period.Tiers) > 0 {
			return ErrInvalidPricing
		}
		return nil
	}
	if period.ElectricCents != 0 || len(period.Tiers) == 0 || len(period.Tiers) > 8 {
		return ErrInvalidPricing
	}
	previous := -1
	for _, tier := range period.Tiers {
		if tier.MaxWatts < 0 || tier.MaxWatts <= previous || tier.MaxWatts > 9990 {
			return ErrInvalidPricing
		}
		if tier.ElectricCents < 0 || tier.ElectricCents > maxRateCents ||
			tier.ServiceCents < 0 || tier.ServiceCents > maxRateCents {
			return ErrInvalidPricing
		}
		previous = tier.MaxWatts
	}
	return nil
}

func clockMinute(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' || value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' || value[3] < '0' || value[3] > '9' || value[4] < '0' || value[4] > '9' {
		return 0, ErrInvalidPricing
	}
	hour, err := strconv.Atoi(value[:2])
	if err != nil {
		return 0, ErrInvalidPricing
	}
	minute, err := strconv.Atoi(value[3:])
	if err != nil || hour > 24 || minute > 59 || (hour == 24 && minute != 0) {
		return 0, ErrInvalidPricing
	}
	return hour*60 + minute, nil
}

// specIsUniformOver 判断区间内计费规则是否恒定；恒定时未分段电量可均摊结算。
func specIsUniformOver(spec Spec, start, end time.Time) bool {
	if spec.Electric == nil || (spec.Electric.Basis != BasisEnergy && spec.Electric.Basis != BasisRealtimePower) {
		// 峰值功率及全天固定费率不依赖时段变化，均摊不会改变计费结果。
		return true
	}
	schedule, err := compilePeriods(spec.Electric.Periods)
	if err != nil {
		return false
	}
	var first *Period
	at := start.In(beijing).Truncate(time.Minute)
	for !at.After(end.In(beijing).Truncate(time.Minute)) {
		period := schedule[at.Hour()*60+at.Minute()]
		if first == nil {
			first = &period
		} else if !sameRate(*first, period) {
			return false
		}
		at = at.Add(time.Minute)
	}
	return true
}

// sameRate 比较单一电价和阶梯，判断两个时段费率是否完全一致。
// 仅比较阶梯会误将不同的电量电价视为相同，导致跨费率电量错误分摊。
func sameRate(a, b Period) bool {
	if a.ElectricCents != b.ElectricCents || a.ServiceCents != b.ServiceCents || len(a.Tiers) != len(b.Tiers) {
		return false
	}
	for i := range a.Tiers {
		if a.Tiers[i] != b.Tiers[i] {
			return false
		}
	}
	return true
}

var energyPattern = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3})?$`)

var (
	decimalZero     = decimal.Zero
	decimalOne      = decimal.NewFromInt(1)
	decimalThousand = decimal.NewFromInt(1000)
)

// parseEnergy 把用户填的充电量限制在两轮车一次充电合情合理的范围内，
// 这样格式错误或恶意的输入就到不了计价引擎。
func parseEnergy(value string) (decimal.Decimal, error) {
	kwh, err := decimal.NewFromString(value)
	if err != nil {
		return decimal.Zero, ErrInvalidPricing
	}
	if kwh.LessThan(decimal.New(1, -3)) || kwh.GreaterThan(decimal.NewFromInt(100)) {
		return decimal.Zero, ErrInvalidPricing
	}
	return kwh, nil
}
