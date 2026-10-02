// Package card 承载在线卡家族的表记录类型：card_charge / card_operation 等。
// 卡业务逻辑仍暂在 central/charge（online_card.go 等），随批次迁入。
package card

type CardCharge struct {
	ChargeOrderID    uint64
	CardID           uint64
	PortCode         string
	ActivePort       *string
	PaidCents        int64
	PurchasedMinutes uint16
	MaxMinutes       uint16
	CardNo           string
	WalletAfterCents int64
	PackageJSON      []byte
}

func (CardCharge) TableName() string { return "card_charge" }

type CardOperation struct {
	OperationID   string `json:"operation_id"`
	DeviceID      string `json:"device_id"`
	EventID       string `json:"event_id"`
	ChargeOrderID uint64 `json:"charge_order_id"`
	CardID        uint64 `json:"card_id"`
	Kind          string `json:"kind"`
	PriceCents    int64  `json:"price_cents"`
	Minutes       uint16 `json:"minutes"`
	Status        string `json:"status"`
}

func (CardOperation) TableName() string { return "card_operation" }
