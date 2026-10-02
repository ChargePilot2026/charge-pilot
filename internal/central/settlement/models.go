// Package settlement 承载结算家族的表记录类型：charge_fee_receipt 等。
// 结算逻辑仍暂在 central/charge，随批次迁入。
package settlement

import "encoding/json"

// ChargeFeeRecord 保存订单结算回执，费用明细在计价引擎输出的 result_json 中。
type ChargeFeeRecord struct {
	ChargeOrderID  uint64 `gorm:"column:charge_order_id;primaryKey"`
	CalculationNo  string `gorm:"column:calculation_no"`
	ResultJSON     []byte `gorm:"column:result_json"`
	ShortfallCents int64  `gorm:"column:shortfall_cents"`
}

func (ChargeFeeRecord) TableName() string { return "charge_fee_receipt" }

// Fees 把存下来的费用明细解码成扁平的金额字段。
func (r ChargeFeeRecord) Fees() (electric, service, total int64, ok bool) {
	// 回执存的是完整的计费 Result，
	// 其中费用字段就在顶层，与 source 块并列。
	var flat struct {
		ElectricCents int64 `json:"electric_cents"`
		ServiceCents  int64 `json:"service_cents"`
		TotalCents    int64 `json:"total_cents"`
	}
	if err := json.Unmarshal(r.ResultJSON, &flat); err != nil {
		return 0, 0, 0, false
	}
	return flat.ElectricCents, flat.ServiceCents, flat.TotalCents, true
}
