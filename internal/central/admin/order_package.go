package admin

import (
	"context"
	"encoding/json"

	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
)

// OrderPackage uses the order's frozen contract. Editing or deleting the live
// station scheme must never change what the customer bought on this order.
type OrderPackage struct {
	Offer       pricing.Offer      `json:"offer"`
	Scheme      *pricing.Scheme    `json:"scheme,omitempty"`
	BillingMode pricing.ChargeMode `json:"billing_mode"`
	RuleVersion uint32             `json:"rule_version"`
	Card        *OrderCardPurchase `json:"card,omitempty"`
}

// Card purchases can grow after the first swipe; keep the original unit offer
// intact and expose the accumulated entitlement separately.
type OrderCardPurchase struct {
	PaidCents        int64  `json:"paid_cents"`
	PurchasedMinutes uint16 `json:"purchased_minutes"`
	MaxMinutes       uint16 `json:"max_minutes"`
}

func packageFromSnapshot(raw []byte) *OrderPackage {
	var frozen struct {
		Rule  pricing.Rule   `json:"rule"`
		Offer *pricing.Offer `json:"offer"`
	}
	if json.Unmarshal(raw, &frozen) != nil || frozen.Offer == nil || !frozen.Offer.Valid() {
		return nil
	}
	return &OrderPackage{Offer: *frozen.Offer, Scheme: frozen.Rule.Spec.Scheme,
		BillingMode: frozen.Rule.Spec.Mode, RuleVersion: frozen.Rule.Version}
}

func (s ResourceStore) orderPackage(ctx context.Context, id uint64) (*OrderPackage, error) {
	var snapshot charge.ChargePricingSnapshotRecord
	if err := s.UserDB.WithContext(ctx).Select("pricing_snapshot").Where("charge_order_id = ?", id).Find(&snapshot).Error; err != nil {
		return nil, err
	}
	out := packageFromSnapshot(snapshot.PricingSnapshot)
	if out == nil {
		return nil, nil
	}
	var card struct {
		ChargeOrderID uint64
		OrderCardPurchase
	}
	if err := s.UserDB.WithContext(ctx).Table("card_charge").
		Select("charge_order_id,paid_cents,purchased_minutes,max_minutes").
		Where("charge_order_id = ?", id).Find(&card).Error; err != nil {
		return nil, err
	}
	if card.ChargeOrderID != 0 {
		out.Card = &card.OrderCardPurchase
	}
	return out, nil
}
