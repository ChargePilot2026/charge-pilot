package pricing

import (
	"fmt"
	"time"
)

// Scheme is the complete commercial configuration. Templates, applied copies
// and order snapshots all use this same value; device overrides never merge it.
type Scheme struct {
	Name     string       `json:"name"`
	Remark   string       `json:"remark"`
	Amount   *AmountMode  `json:"amount,omitempty"`
	Energy   *EnergyMode  `json:"energy,omitempty"`
	Packages []Package    `json:"packages"`
	Policy   AmountPolicy `json:"policy"`
	Stop     TimeCharge   `json:"stop"`
	Card     CardPolicy   `json:"card"`
	Display  Display      `json:"display"`
}

type AmountMode struct {
	Algorithm ChargeMode `json:"algorithm"`
	Periods   []Period   `json:"periods"`
}
type EnergyMode struct {
	ElectricCents int64 `json:"electric_cents"`
	ServiceCents  int64 `json:"service_cents"`
}
type Package struct {
	ID         uint64 `json:"id"`
	Name       string `json:"name"`
	Mode       string `json:"mode"` // amount, duration, energy
	PriceCents int64  `json:"price_cents"`
	Minutes    uint16 `json:"minutes,omitempty"`
	KWh        uint16 `json:"kwh,omitempty"`
}
type AmountPolicy struct {
	FreeMinutes      int    `json:"free_minutes"`
	MinElectricCents int64  `json:"min_electric_cents"`
	MaxMinutes       uint16 `json:"max_minutes"`
	LossRateBP       int32  `json:"loss_rate_bp"`
	ChannelBP        int32  `json:"channel_bp"`
}
type CardPolicy struct {
	PackageID  uint64 `json:"package_id"`
	MaxMinutes uint16 `json:"max_minutes"`
}

func (s Scheme) Normalized() Scheme {
	if s.Policy.MaxMinutes == 0 {
		s.Policy.MaxMinutes = 600
	}
	if s.Card.MaxMinutes == 0 {
		s.Card.MaxMinutes = 600
	}
	if s.Policy.ChannelBP == 0 {
		s.Policy.ChannelBP = 10000
	}
	return s
}

// Validation returns actionable missing/conflicting inputs and never invents rates.
func (s Scheme) Validate() error {
	s = s.Normalized()
	if len([]rune(s.Name)) == 0 || len([]rune(s.Name)) > 64 || len([]rune(s.Remark)) > 255 {
		return fmt.Errorf("请填写方案名称（最多64字）及有效说明")
	}
	if len(s.Packages) == 0 || len(s.Packages) > 99 {
		return fmt.Errorf("请配置1～99个支付套餐")
	}
	if s.Policy.MaxMinutes < 60 || s.Policy.MaxMinutes > 4320 || s.Card.MaxMinutes < 60 || s.Card.MaxMinutes > 4320 {
		return fmt.Errorf("金额与刷卡时长上限须为1～72小时")
	}
	if s.Policy.FreeMinutes < 0 || s.Policy.FreeMinutes > int(s.Policy.MaxMinutes) || s.Policy.MinElectricCents < 0 || s.Policy.MinElectricCents > maxRateCents {
		return fmt.Errorf("免费时长或最低电费无效")
	}
	// The redesign deliberately leaves these semantics unconfirmed. Fail closed
	// instead of importing the old engine's coefficient order.
	if s.Policy.LossRateBP != 0 || s.Policy.ChannelBP != 10000 {
		return fmt.Errorf("损耗与渠道系数组合规则尚未确认，当前请关闭损耗并使用1倍系数")
	}
	if s.Amount != nil {
		if !s.Amount.Algorithm.ServerBilled() {
			return fmt.Errorf("金额模式须选择最大功率、实时功率或电量算法")
		}
		spec := Spec{Mode: s.Amount.Algorithm, Electric: &ElectricLine{Basis: s.Amount.Algorithm.BasisFor(), Periods: s.Amount.Periods}}
		if ValidateSpec(spec) != nil {
			return fmt.Errorf("请完整填写全天时段及电费、服务费，功率档位须递增")
		}
	}
	if s.Energy != nil && (s.Energy.ElectricCents < 0 || s.Energy.ServiceCents < 0 || s.Energy.ElectricCents+s.Energy.ServiceCents <= 0 || s.Energy.ElectricCents > maxRateCents || s.Energy.ServiceCents > maxRateCents) {
		return fmt.Errorf("请填写设备电量的固定电费与服务费单价")
	}
	ids := map[uint64]bool{}
	amount, energy, card := false, false, s.Card.PackageID == 0
	for _, p := range s.Packages {
		if p.ID == 0 || p.ID > 99 || ids[p.ID] || p.Name == "" || len([]rune(p.Name)) > 64 {
			return fmt.Errorf("套餐编号须唯一（1～99），名称不能为空")
		}
		ids[p.ID] = true
		switch p.Mode {
		case "amount":
			amount = true
			if s.Amount == nil || p.Minutes != 0 || p.KWh != 0 || p.PriceCents <= 0 || p.PriceCents < s.Policy.MinElectricCents || p.PriceCents > maxRateCents {
				return fmt.Errorf("金额套餐须绑定金额算法，价格不能低于最低电费")
			}
		case "duration":
			if p.Minutes == 0 || p.Minutes > 4320 || p.KWh != 0 || p.PriceCents <= 0 || p.PriceCents > maxRateCents {
				return fmt.Errorf("时长套餐须填写价格与1～4320分钟")
			}
			if p.ID == s.Card.PackageID {
				card = true
				if p.Minutes > s.Card.MaxMinutes {
					return fmt.Errorf("刷卡套餐不能超过累计上限")
				}
			}
		case "energy":
			energy = true
			if s.Energy == nil || p.KWh < 1 || p.KWh > 65 || p.Minutes != 0 {
				return fmt.Errorf("电量套餐须为1～65整数度并配置固定单价")
			}
			if p.PriceCents != int64(p.KWh)*(s.Energy.ElectricCents+s.Energy.ServiceCents) || p.PriceCents > maxRateCents {
				return fmt.Errorf("电量套餐价格必须由度数及电费、服务费自动计算")
			}
		default:
			return fmt.Errorf("套餐仅支持金额、时长、电量")
		}
	}
	if s.Amount != nil && !amount || s.Energy != nil && !energy {
		return fmt.Errorf("已启用的模式必须配置支付套餐")
	}
	if !card {
		return fmt.Errorf("刷卡须明确选择一个已有时长套餐")
	}
	return nil
}

func (s Scheme) Package(id uint64) (Package, bool) {
	for _, p := range s.Packages {
		if p.ID == id {
			return p, true
		}
	}
	return Package{}, false
}

// SpecFor is the execution view of a complete frozen scheme and one package.
func (s Scheme) SpecFor(p Package) Spec {
	s = s.Normalized()
	spec := Spec{Scheme: &s, Display: s.Display, TimeCharge: &s.Stop, CardMaxMinutes: s.Card.MaxMinutes}
	switch p.Mode {
	case "amount":
		if s.Amount != nil {
			spec.Mode = s.Amount.Algorithm
			spec.Electric = &ElectricLine{Basis: spec.Mode.BasisFor(), Periods: s.Amount.Periods}
		}
		spec.FreeMinutes = s.Policy.FreeMinutes
		spec.MinElectricCents = s.Policy.MinElectricCents
		spec.TimeCharge = &TimeCharge{StopWhenFull: s.Stop.StopWhenFull, MaxMinutes: s.Policy.MaxMinutes}
	case "duration":
		spec.Mode = ModeDeviceDuration
	case "energy":
		spec.Mode = ModeDeviceEnergy
	}
	return spec
}

func (s Scheme) Offers(rule Rule) []Offer {
	rows := make([]Offer, 0, len(s.Packages))
	for _, p := range s.Packages {
		rows = append(rows, Offer{ID: rule.ID*100 + p.ID, PackageID: p.ID, StationID: rule.StationID, DeviceID: rule.DeviceID, Name: p.Name, Mode: p.Mode, PriceCents: p.PriceCents, DurationMinutes: p.Minutes, EnergyWh: uint32(p.KWh) * 1000, MaxMinutes: s.Normalized().Policy.MaxMinutes})
	}
	return rows
}

// Preview is side-effect free and uses the exact execution/settlement functions.
type Preview struct {
	CutoffAt    *time.Time `json:"cutoff_at,omitempty"`
	WalletAfter *int64     `json:"wallet_after,omitempty"`
	Operations  []string   `json:"operations,omitempty"`
	RawFee      *Fee       `json:"raw_fee,omitempty"`
	PolicyFee   *Fee       `json:"policy_fee,omitempty"`
	Package     Package    `json:"package"`
	PaidCents   int64      `json:"paid_cents"`
	Settlement  Settlement `json:"settlement"`
	RefundCents int64      `json:"refund_cents"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason"`
}

func (s Scheme) Preview(id uint64, meter ActualMeter) (Preview, error) {
	return s.previewPurchases(id, meter, 1)
}

func (s Scheme) previewPurchases(id uint64, meter ActualMeter, count uint16) (Preview, error) {
	if err := s.Validate(); err != nil {
		return Preview{}, err
	}
	p, ok := s.Package(id)
	if !ok {
		return Preview{}, fmt.Errorf("请选择方案内套餐")
	}
	if count < 1 || count > 1 && (p.Mode != "duration" || s.Card.PackageID != id || uint32(p.Minutes)*uint32(count) > uint32(s.Normalized().Card.MaxMinutes)) {
		return Preview{}, fmt.Errorf("刷卡套餐或累计时长不满足追加条件")
	}
	result := Preview{Package: p, PaidCents: p.PriceCents, Status: "calculated", Reason: "提前结束"}
	result.PaidCents = p.PriceCents * int64(count)
	spec := s.SpecFor(p)
	offer := s.Offers(Rule{ID: 1, StationID: 1})[0]
	for _, o := range s.Offers(Rule{ID: 1, StationID: 1}) {
		if o.PackageID == id {
			offer = o
		}
	}
	if count > 1 {
		offer.ServerDuration = true
		offer.PriceCents = result.PaidCents
		offer.DurationMinutes = p.Minutes * count
		offer.PurchaseCount = count
	}
	if p.Mode == "amount" && meter.EndedAt.Sub(meter.StartedAt) >= time.Duration(s.Normalized().Policy.MaxMinutes)*time.Minute {
		result.Reason = "达到最长时长"
		clipped, err := CutoffMeter(spec, meter, meter.StartedAt.Add(time.Duration(s.Normalized().Policy.MaxMinutes)*time.Minute))
		if err != nil {
			meter.ReviewRequired = true
		} else {
			meter = clipped
		}
	}
	// Simulate stop decisions at the supplied actual reading boundaries. There
	// is no interpolation of energy or prediction between two readings.
	if p.Mode == "amount" && !meter.ReviewRequired && len(meter.Segments) > 0 {
		for i, segment := range meter.Segments {
			prefix := meter
			prefix.EndedAt = segment.EndedAt
			prefix.ChargedSeconds = uint32(prefix.EndedAt.Sub(prefix.StartedAt) / time.Second)
			prefix.Segments = meter.Segments[:i+1]
			prefix.ChargedWh = 0
			for _, s := range prefix.Segments {
				prefix.ChargedWh += s.EnergyWh
			}
			fee, err := PriceActual(Rule{ID: 1, Version: 1, Spec: spec}, prefix)
			if err != nil {
				break
			}
			if fee.TotalCents >= p.PriceCents {
				meter = prefix
				at := prefix.EndedAt
				result.CutoffAt = &at
				break
			}
		}
	}
	settled, err := SettleSession(spec, meter, &offer, ActualFromMeter(meter))
	if err == ErrMeterReview {
		result.Status = "meter_review"
		result.Reason = "无法可靠计算／待核对"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Settlement = settled
	if p.Mode == "amount" {
		rawSpec := spec
		rawSpec.FreeMinutes = 0
		rawSpec.MinElectricCents = 0
		if raw, err := PriceActual(Rule{ID: 1, Version: 1, Spec: rawSpec}, meter); err == nil {
			result.RawFee = &raw
		}
		if adjusted, err := PriceActual(Rule{ID: 1, Version: 1, Spec: spec}, meter); err == nil {
			result.PolicyFee = &adjusted
		}
	}
	result.RefundCents = result.PaidCents - settled.TotalCents
	if settled.TotalCents >= result.PaidCents {
		result.Reason = "套餐用尽／余额耗尽"
	}
	return result, nil
}

func (s Scheme) PreviewScenario(id uint64, m ActualMeter, scenario string, wallet int64) (Preview, error) {
	if err := s.Validate(); err != nil {
		return Preview{}, err
	}
	p, ok := s.Package(id)
	if !ok {
		return Preview{}, fmt.Errorf("请选择方案内套餐")
	}
	r := Preview{Package: p, PaidCents: p.PriceCents}
	switch scenario {
	case "start_failed":
		r.Status = "calculated"
		r.Reason = "明确未启动，全额退款"
		r.RefundCents = p.PriceCents
		return r, nil
	case "start_unknown":
		r.Status = "confirming"
		r.Reason = "启动确认中：查询／重放同一操作，保留端口占用"
		return r, nil
	case "card_extend", "card_failed", "card_unknown":
		if p.Mode != "duration" || s.Card.PackageID != id {
			return r, fmt.Errorf("请选择明确指定的刷卡时长套餐")
		}
		if wallet < p.PriceCents {
			r.Status = "rejected"
			r.Reason = "首次刷卡余额不足，不扣费不启动"
			return r, nil
		}
		count := uint16(2)
		ops := []string{"首次扣款并确认启动，设备按累计上限执行"}
		if wallet < 2*p.PriceCents || uint32(p.Minutes)*2 > uint32(s.Normalized().Card.MaxMinutes) {
			count = 1
			ops = append(ops, "余额或累计上限不足，整次拒绝加时")
		} else if scenario == "card_failed" {
			count = 1
			ops = append(ops, "服务器拒绝本次加时，事务回滚，无扣款，原时长不变")
		} else if scenario == "card_unknown" {
			r.PaidCents = 2 * p.PriceCents
			r.Status = "confirming"
			r.Reason = "模拟事务提交后应答丢失：按同一事件编号查询，不再次扣款"
			r.Operations = append(ops, "查询同一事件的事务结果，重试不再次扣款")
			after := wallet - r.PaidCents
			r.WalletAfter = &after
			return r, nil
		} else {
			ops = append(ops, "服务器在同一事务中扣款并增加购买时长")
		}
		result, err := s.previewPurchases(id, m, count)
		result.Settlement.Executor = "server"
		result.Operations = ops
		if err == nil {
			after := wallet - result.Settlement.TotalCents
			result.WalletAfter = &after
		}
		return result, err
	case "":
		return s.Preview(id, m)
	default:
		return Preview{}, fmt.Errorf("预览状态无效")
	}
}

type Capabilities struct {
	StopPolicyVerified bool   `json:"stop_policy_verified"`
	StopWhenFull       bool   `json:"stop_when_full"`
	MaxMinutes         uint16 `json:"max_minutes"`
	Duration           bool   `json:"duration"`
	Energy             bool   `json:"energy"`
	OnlineCard         bool   `json:"online_card"`
	CardEventIdentity  bool   `json:"card_event_identity"`
}

func (s Scheme) ValidateCapabilities(c Capabilities) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if !c.StopPolicyVerified || c.StopWhenFull != s.Stop.StopWhenFull {
		return fmt.Errorf("满充停止配置与设备回读结果不一致或尚未核验；请先核验设备停止策略")
	}
	for _, p := range s.Packages {
		minutes := p.Minutes
		if p.Mode == "amount" {
			minutes = s.Normalized().Policy.MaxMinutes
		}
		if p.Mode != "energy" && (!c.Duration || minutes > c.MaxMinutes) {
			return fmt.Errorf("设备不支持套餐 %s 所需的%d分钟（设备上限%d分钟）", p.Name, minutes, c.MaxMinutes)
		}
		if p.Mode == "energy" && !c.Energy {
			return fmt.Errorf("设备不支持电量执行")
		}
	}
	if s.Card.PackageID != 0 && (!c.OnlineCard || !c.CardEventIdentity || s.Normalized().Card.MaxMinutes > c.MaxMinutes) {
		return fmt.Errorf("设备的在线卡、移开后重刷事件行为或累计时长能力尚未验证")
	}
	return nil
}
