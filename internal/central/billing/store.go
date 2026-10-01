package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrConflict = errors.New("billing source conflicts with persisted calculation")

type Source struct {
	FinalFee      *pricing.Fee        `json:"final_fee,omitempty"`
	ReviewID      string              `json:"review_id,omitempty"`
	ChargeOrderID uint64              `json:"charge_order_id"`
	OrderNo       string              `json:"order_no"`
	UserID        uint64              `json:"user_id"`
	Rule          pricing.Rule        `json:"rule"`
	Offer         *pricing.Offer      `json:"offer,omitempty"`
	Meter         pricing.ActualMeter `json:"meter"`
}
type Result struct {
	CalculationNo string `json:"calculation_no"`
	Source        Source `json:"source"`
	pricing.ActualFee
}
type Store struct{ DB *gorm.DB }

func (s Store) Calculate(ctx context.Context, source Source) (Result, error) {
	if source.ChargeOrderID == 0 || source.OrderNo == "" || source.UserID == 0 || source.Rule.StationID == 0 {
		return Result{}, ErrConflict
	}
	fee, err := PriceSource(source)
	if err != nil {
		return Result{}, err
	}
	payload, err := json.Marshal(source)
	if err != nil {
		return Result{}, err
	}
	out := Result{CalculationNo: CalculationNumber(source.OrderNo, source.ChargeOrderID), Source: source, ActualFee: fee}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("fee_receipt").Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).Create(map[string]any{"charge_order_id": source.ChargeOrderID, "source_json": string(payload)}).Error; err != nil {
			return err
		}
		var receipt struct {
			SourceJSON    []byte
			CalculationID *uint64
			CalculationNo *string
		}
		if err := tx.Table("fee_receipt").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", source.ChargeOrderID).Take(&receipt).Error; err != nil {
			return err
		}
		var previous Source
		if json.Unmarshal(receipt.SourceJSON, &previous) != nil {
			return ErrConflict
		}
		prior, _ := json.Marshal(previous)
		if string(prior) != string(payload) {
			return ErrConflict
		}
		if receipt.CalculationID != nil {
			var delivery struct{ PayloadJSON []byte }
			if err := tx.Table("fee_delivery").Where("charge_order_id=?", source.ChargeOrderID).Take(&delivery).Error; err != nil {
				return err
			}
			return json.Unmarshal(delivery.PayloadJSON, &out)
		}
		month := time.Date(source.Meter.EndedAt.Year(), source.Meter.EndedAt.Month(), 1, 0, 0, 0, 0, time.UTC)
		resultJSON, _ := json.Marshal(out)
		row := map[string]any{"calculation_no": out.CalculationNo, "order_no": source.OrderNo, "charge_order_id": source.ChargeOrderID, "user_id": source.UserID, "station_id": source.Rule.StationID, "pricing_rule_id": source.Rule.ID, "pricing_rule_version": source.Rule.Version, "charged_kwh": fmt.Sprintf("%d.%03d", source.Meter.ChargedWh/1000, source.Meter.ChargedWh%1000), "charged_seconds": source.Meter.ChargedSeconds, "electric_cents": fee.ElectricCents, "service_cents": fee.ServiceCents, "total_cents": fee.TotalCents, "breakdown_json": string(resultJSON), "created_month": month}
		if err := tx.Table("fee_calculation").Create(row).Error; err != nil {
			return err
		}
		var id uint64
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		if err := tx.Table("fee_receipt").Where("charge_order_id=?", source.ChargeOrderID).Updates(map[string]any{"calculation_id": id, "calculation_no": out.CalculationNo}).Error; err != nil {
			return err
		}
		return tx.Table("fee_delivery").Create(map[string]any{"charge_order_id": source.ChargeOrderID, "payload_json": string(resultJSON)}).Error
	})
	return out, err
}

// Manual fees are accepted only as a persisted, audited review in the source.
// The charge service compares that source again before applying any money.
func PriceSource(source Source) (pricing.Fee, error) {
	if source.FinalFee != nil {
		f := *source.FinalFee
		if source.ReviewID == "" || source.Offer == nil || f.ElectricCents < 0 || f.ServiceCents < 0 || f.TotalCents != f.ElectricCents+f.ServiceCents || f.TotalCents > source.Offer.PriceCents {
			return pricing.Fee{}, ErrConflict
		}
		return f, nil
	}
	return pricing.PriceOfferActual(source.Rule, source.Offer, source.Meter)
}
