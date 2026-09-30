package charge

import (
	"context"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

type ManualSettlement struct {
	ChargeOrderID uint64 `json:"charge_order_id"`
	RequestID     string `json:"request_id"`
	ActorID       uint64 `json:"actor_id"`
	ElectricCents int64  `json:"electric_cents"`
	ServiceCents  int64  `json:"service_cents"`
	Reason        string `json:"reason"`
}

func (ManualSettlement) TableName() string { return "charge_manual_settlement" }
func (s BillingOrders) ResolveAmount(ctx context.Context, in ManualSettlement) error {
	if in.ChargeOrderID == 0 || in.ActorID == 0 || uuid.Validate(in.RequestID) != nil || strings.TrimSpace(in.Reason) == "" || len([]rune(in.Reason)) > 1000 || in.ElectricCents < 0 || in.ServiceCents < 0 || in.ElectricCents > 1000000 || in.ServiceCents > 1000000 {
		return billing.ErrConflict
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job struct{ Status string }
		if err := tx.Table("charge_billing_job").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", in.ChargeOrderID).Take(&job).Error; err != nil {
			return err
		}
		var previous ManualSettlement
		err := tx.Where("charge_order_id=? OR request_id=?", in.ChargeOrderID, in.RequestID).Take(&previous).Error
		if err == nil {
			if previous != in {
				return billing.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if job.Status != "manual_review" {
			return billing.ErrConflict
		}
		source, err := readOriginalBillingSource(tx, in.ChargeOrderID)
		if err != nil {
			return err
		}
		if source.Offer == nil || in.ElectricCents+in.ServiceCents > source.Offer.PriceCents {
			return billing.ErrConflict
		}
		if err := tx.Create(&in).Error; err != nil {
			return err
		}
		if err := tx.Create(&ChargeEventLogRecord{ChargeOrderID: in.ChargeOrderID, EventID: in.RequestID, Event: "manual_settlement", Actor: "admin", Detail: in.Reason, OccurredAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
		return tx.Table("charge_billing_job").Where("charge_order_id=?", in.ChargeOrderID).Updates(map[string]any{"status": "pending", "last_error": nil, "next_attempt_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error
	})
}
