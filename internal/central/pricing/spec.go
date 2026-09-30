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

	beijing = time.FixedZone("Asia/Shanghai", 8*3600)
)

const maxRateCents = 1000000

// Tier 是功率阶梯中的一档。这里只存上界：某一档的下界永远是上一档的上界加一瓦，
// 编辑器正是靠这一点保证各档不会重叠。两端各自独立存储才会让一份电价表中间漏出
// 空档——而阶梯里的空档，意味着落在空档里的那次充电根本无法计价。
type Tier struct {
	// MaxWatts 是含端点的上界。第一档必须为 0，且各档数值必须严格递增。
	MaxWatts int `json:"max_watts"`
	// ElectricCents 在电量口径下是每 kWh 的分，在功率口径下是每小时的分，
	// 具体由 TierPriceBasis 决定。
	ElectricCents int64 `json:"electric_cents"`
	// ServiceCents 只有服务费口径为 ServiceMinutePower 时才会被读取。
	ServiceCents int64 `json:"service_cents,omitempty"`
}

// Period 是一天中的一个分时段。一天被存成一条「结束分钟」的链，而不是一组独立的
// 起止时间对，理由与阶梯只存上界相同：链装不下空档，而一对起止时间太容易漏。
type Period struct {
	// EndMinute 是从零点起的分钟数，取值 1..1440。数值必须严格递增，且最后一个
	// 必须正好是 1440，这样一整天才被完整覆盖，既没有空档也没有重叠。
	EndMinute int `json:"end_minute"`
	// ElectricCents 是该时段的单一电价，也是电量口径下唯一会用到的费率字段：
	// 电量电价表没有阶梯，给它加一个就等于加一个没人读的字段。
	ElectricCents int64 `json:"electric_cents,omitempty"`
	// Tiers 是该时段内适用的功率阶梯；电量口径下为空。
	Tiers []Tier `json:"tiers,omitempty"`
}

// ElectricLine 给出电费本身。服务端计费模式必须有它，设备计费模式则没有：
// 设备计费的充电在整套系统里都不存在电价，也就不存在可以拿来电费的
// 任何东西。
type ElectricLine struct {
	Basis   ServerBasis `json:"basis"`
	Periods []Period    `json:"periods"`
}

// ServiceLine 给出服务费的算法。nil 的 *ServiceLine 与 ServiceNone 口径都表示
// 不收服务费；之所以保留指针，是为了让编辑器来回保存时「没有配置」和
// 「配置成零」仍然可区分。
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

// ChannelMultiplier 以基点表示倍数，10000 即 1.0x。它只作用于电费一项：真实后台
// 把卡费率显示成电费上的一个倍率，所以在这里把服务费倍率一起折进来，
// 等于凭空造一个电价表从未发布过的折扣。
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

// TimeCharge 携带的是决定「按时长计费的充电怎么结束」的设置，而不是它花多少钱。
type TimeCharge struct {
	// StopWhenFull 让电池充满即结束充电，而不是把时间跑完。
	StopWhenFull bool `json:"stop_when_full"`
	// MaxMinutes 限制按时长计费的最长分钟数，0 表示不限。
	MaxMinutes uint16 `json:"max_minutes,omitempty"`
	// FloatPowerDeciWatts 与 FloatSeconds 描述电池不再接受满电流之后的低电流尾段。
	FloatPowerDeciWatts uint16 `json:"float_power_deci_watts,omitempty"`
	FloatSeconds        uint16 `json:"float_seconds,omitempty"`
}

// Spec 是一份自包含的、完整的充电计价描述。预估与结算都把同一个结构喂给 Cost，
// 两者因此不可能各算各的而对不上。
type Spec struct {
	Mode ChargeMode `json:"mode"`
	// Electric 只在服务端计费模式下被读取。设备计费模式把它留成零值，
	// ValidateSpec 会拒绝任何试图设置它的电价表——设备计费电价表上的费率
	// 是一个永远用不到的费率，而总有一天会有人相信它是生效的。
	Electric *ElectricLine `json:"electric,omitempty"`
	// Service 是独立的第二条线。两条线可以任意组合——按电量收电费配按小时收
	// 服务费就是真实且常见的一种组合。
	Service *ServiceLine `json:"service,omitempty"`
	// Multiplier 是一张独立的费率卡，归电费所有。
	Multiplier *ChannelMultiplier `json:"multiplier,omitempty"`
	// TierPriceBasis 只被 BasisRealtimePower 读取：那里存的是每小时的「分」，
	// 要作用到一段电量上之前必须先换算成每 kWh 的分。它之所以是一个显式的
	// 存储选择，是因为行业里的这个换算是商业决策，而不是算术事实。
	TierPriceBasis TierPriceBasis `json:"tier_price_basis,omitempty"`
	// LossRateBP 按比例抬高计费电量以覆盖线损，10000 表示无损耗。
	LossRateBP int32 `json:"loss_rate_bp,omitempty"`
	// FreeMinutes 表示充电在这么多分钟内结束时整单免单，0 表示不启用该免单。
	FreeMinutes int `json:"free_minutes,omitempty"`
	// MinElectricCents 只兜底电费一项，服务费永远不会被拿来当找零用。
	MinElectricCents int64 `json:"min_electric_cents,omitempty"`
	// TimeCharge 只对按时长计费的充电生效。
	TimeCharge *TimeCharge `json:"time_charge,omitempty"`
	// SpendCapCents 是运行中的服务端计费充电可选的花费上限。0 表示不设上限，
	// 此时充电在平台下发的时间或电量耗尽时结束。它是产品控制而不是协议要求：
	// 充电板仍然照自己那份额度行事。
	SpendCapCents int64 `json:"spend_cap_cents,omitempty"`
	// StopGraceSeconds 是平台要求设备停止之后，等待多久才把这次充电视为失控。
	StopGraceSeconds int `json:"stop_grace_seconds,omitempty"`
	// DefaultChargeWay 是本设备上充电默认的鉴权方式，属于展示与策略，不参与计算。
	DefaultChargeWay string `json:"default_charge_way,omitempty"`
	// CardMaxMinutes 是允许的刷卡充电最长时长。
	CardMaxMinutes uint16 `json:"card_max_minutes,omitempty"`
	// Display 是小程序允许展示的内容。它从不改变实际收费，这正是它与 spec 并列
	// 而不是嵌在 spec 里面的原因。
	Display Display `json:"display,omitempty"`
}

// Sample 是一段始终落在同一费率时段内的连续用电。无法保证这一点的调用方必须在
// 调用 Cost 之前先切分。
type Sample struct {
	Start    time.Time
	End      time.Time
	EnergyWh uint64
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
	Basis         ServerBasis `json:"basis"`
	ElectricCents int64       `json:"electric_cents"`
	ServiceCents  int64       `json:"service_cents"`
	TotalCents    int64       `json:"total_cents"`
	BillableWh    uint64      `json:"billable_wh"`
}

// ValidateSpec 由发布与计价共用：编辑器接受的电价表，必须永远也是结算链路
// 真能执行的那一种。
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
	if spec.CardMaxMinutes > 999 {
		// 固件里的刷卡充电是一个以分钟计的无符号 16 位字段，超过这个值会被
		// 充电板直接拒绝，而不是被静默截断。
		return ErrInvalidPricing
	}
	if spec.TimeCharge != nil {
		if spec.TimeCharge.MaxMinutes > 999 || spec.TimeCharge.FloatSeconds > 10800 || spec.TimeCharge.FloatPowerDeciWatts > 500 {
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
		// 设备计费的电价表完全不带费率。与其放出一份看起来有计价、其实没计价的
		// 电价表，不如把多余的费率直接拒掉。
		if spec.Electric != nil || spec.Service != nil || spec.Multiplier != nil {
			return ErrInvalidPricing
		}
		return nil
	}
	if spec.Electric == nil {
		return ErrInvalidPricing
	}
	if spec.Electric.Basis != spec.Mode.BasisFor() {
		// 计费模式与电价表必须描述同一份东西。一台按峰值功率配置的设备却挂着一份
		// 按电量计价的电价表，就会按一个谁也没同意过的口径收钱。
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

// compilePeriods 把存储的时段链展开成按分钟的查表，链的不变量也在这里落地：
// 结束分钟严格递增、起点为零、终点为 1440。这一条规则就让空档与重叠在
// 表示上不可能出现，所以不必再写一个独立的覆盖检查。
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

// specIsUniformOver 报告这次充电的每一分钟是否按同一种方式计价。若是，把电量
// 均摊就不是一句近似而是精确结果，未分段的计量因此可以不经复核直接结算。
func specIsUniformOver(spec Spec, start, end time.Time) bool {
	if spec.Electric == nil || (spec.Electric.Basis != BasisEnergy && spec.Electric.Basis != BasisRealtimePower) {
		// 峰值功率与一口价电价表不从时钟上读费率：前者只看峰值，后者全天一个价，
		// 所以均摊永远不会把它们算错。
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

// sameRate 报告两个时段的计价是否完全相同。单一电价与阶梯要一起比，因为一个口径
// 只会带其中之一：只比阶梯的话，每一份电量电价表都会因为两边阶梯都为空而显得
// 均匀。跨过电价变更点的充电就会靠「各时段电量大致均分」这句猜测结算。
func sameRate(a, b Period) bool {
	if a.ElectricCents != b.ElectricCents || len(a.Tiers) != len(b.Tiers) {
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
