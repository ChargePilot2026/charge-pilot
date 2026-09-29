package pricing

// Display is what the mini program is allowed to reveal. It is presentation
// only and never changes what is charged, so it is kept out of Spec.
type Display struct {
	// Shown while a session is running.
	ShowEnergy bool `json:"show_energy"`
	ShowPower  bool `json:"show_power"`
	ShowTariff bool `json:"show_tariff"`
	// Shown on the order and settlement screens.
	ShowFeeSplit   bool `json:"show_fee_split"`
	FeeSplitInline bool `json:"fee_split_inline"`
	ShowFeeOnEnd   bool `json:"show_fee_on_end"`
	// Shown before starting.
	ShowMethod bool `json:"show_method"`
	ShowRule   bool `json:"show_rule"`
	HideUnit   bool `json:"hide_unit"`
}

// DefaultDisplay shows the running-session figures and the fee breakdown, which
// is the least an operator can publish without withholding what was charged.
func DefaultDisplay() Display {
	return Display{ShowEnergy: true, ShowPower: true, ShowFeeSplit: true, FeeSplitInline: true, ShowFeeOnEnd: true, ShowMethod: true}
}
