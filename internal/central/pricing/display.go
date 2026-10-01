package pricing

// Display 控制用户端展示，不参与计费计算。
type Display struct {
	// 充电运行过程中展示。
	ShowEnergy bool `json:"show_energy"`
	ShowPower  bool `json:"show_power"`
	ShowTariff bool `json:"show_tariff"`
	// 在下单页与结算页展示。
	ShowFeeSplit   bool `json:"show_fee_split"`
	FeeSplitInline bool `json:"fee_split_inline"`
	ShowFeeOnEnd   bool `json:"show_fee_on_end"`
	// 在开始充电之前展示。
	ShowMethod bool `json:"show_method"`
	ShowRule   bool `json:"show_rule"`
	HideUnit   bool `json:"hide_unit"`
}

// DefaultDisplay 展示运行中的各项数值与费用拆分，这是运营方在不至于隐瞒
// 实际收费的前提下最少能发布的东西。
func DefaultDisplay() Display {
	return Display{ShowEnergy: true, ShowPower: true, ShowFeeSplit: true, FeeSplitInline: true, ShowFeeOnEnd: true, ShowMethod: true}
}
