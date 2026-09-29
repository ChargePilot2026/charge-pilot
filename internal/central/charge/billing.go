package charge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BillingOrders struct{ DB *gorm.DB }

func (s BillingOrders) Due(ctx context.Context) ([]uint64, error) {
	ids := []uint64{}
	err := s.DB.WithContext(ctx).Table("charge_billing_job").Where("status='pending' AND next_attempt_at<=UTC_TIMESTAMP(3)").Order("next_attempt_at,charge_order_id").Limit(50).Pluck("charge_order_id", &ids).Error
	return ids, err
}
func (s BillingOrders) Defer(ctx context.Context, id uint64, status, reason string) error {
	return s.DB.WithContext(ctx).Table("charge_billing_job").Where("charge_order_id=? AND status<>'done'", id).Updates(map[string]any{"status": status, "last_error": reason, "attempts": gorm.Expr("attempts+1"), "next_attempt_at": time.Now().UTC().Add(time.Minute)}).Error
}
func (s BillingOrders) Read(ctx context.Context, id uint64) (billing.Source, error) {
	return readBillingSource(s.DB.WithContext(ctx), id)
}
func readBillingSource(db *gorm.DB, id uint64) (billing.Source, error) {
	source, err := readOriginalBillingSource(db, id)
	if err != nil {
		return source, err
	}
	var review MeterReview
	result := db.Where("charge_order_id=? AND status='approved'", id).Order("id DESC").Limit(1).Find(&review)
	if result.Error != nil {
		return source, result.Error
	}
	if result.RowsAffected == 0 {
		return source, nil
	}
	var original, corrected billing.Source
	if json.Unmarshal(review.OriginalJSON, &original) != nil || json.Unmarshal(review.CorrectedJSON, &corrected) != nil {
		return source, billing.ErrConflict
	}
	current, _ := json.Marshal(source)
	saved, _ := json.Marshal(original)
	if string(current) != string(saved) {
		return source, billing.ErrConflict
	}
	source.Meter.Segments = corrected.Meter.Segments
	current, _ = json.Marshal(source)
	saved, _ = json.Marshal(corrected)
	if string(current) != string(saved) {
		return source, billing.ErrConflict
	}
	return source, nil
}
func readOriginalBillingSource(db *gorm.DB, id uint64) (billing.Source, error) {
	var order ChargeOrderRecord
	if err := db.Where("id=? AND ended_at IS NOT NULL AND deleted_at IS NULL", id).Take(&order).Error; err != nil {
		return billing.Source{}, err
	}
	if !order.StartedAt.Valid || (order.Status != "completed" && order.Status != "refunding" && order.Status != "refunded") {
		return billing.Source{}, billing.ErrConflict
	}
	var snapshot ChargePricingSnapshotRecord
	var end EndReceiptRecord
	if err := db.Where("charge_order_id=?", id).Take(&snapshot).Error; err != nil {
		return billing.Source{}, err
	}
	if err := db.Where("charge_order_id=?", id).Take(&end).Error; err != nil {
		return billing.Source{}, err
	}
	var contract struct {
		Rule pricing.Rule `json:"rule"`
	}
	var meter EndMeter
	if json.Unmarshal(snapshot.PricingSnapshot, &contract) != nil || json.Unmarshal(end.MeterJSON, &meter) != nil || !meter.EndedAt.Equal(order.EndedAt.Time) {
		return billing.Source{}, billing.ErrConflict
	}
	return billing.Source{ChargeOrderID: id, OrderNo: order.OrderNo, UserID: order.UserID, Rule: contract.Rule, Meter: pricing.ActualMeter{StartedAt: order.StartedAt.Time, EndedAt: meter.EndedAt, ChargedWh: meter.ChargedWh, ChargedSeconds: meter.ChargedSeconds, Segments: meter.Segments}}, nil
}
func (s BillingOrders) Apply(ctx context.Context, result billing.Result) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var reference ChargeOrderRecord
	if err := s.DB.WithContext(ctx).Select("id,payment_order_id").Where("id=? AND deleted_at IS NULL", result.Source.ChargeOrderID).Take(&reference).Error; err != nil {
		return err
	}
	if !reference.PaymentOrderID.Valid {
		return billing.ErrConflict
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var payment PaymentOrderRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", reference.PaymentOrderID.Int64).Take(&payment).Error; err != nil {
			return err
		}
		var order ChargeOrderRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", reference.ID).Take(&order).Error; err != nil {
			return err
		}
		if !order.PaymentOrderID.Valid || uint64(order.PaymentOrderID.Int64) != payment.ID || payment.PayMethod != "wechat" || (payment.Status != "paid" && payment.Status != "partial_refunded" && payment.Status != "refunded") || payment.PaidCents != payment.TotalCents || payment.BizID != order.ID || payment.BizType != "charge" || payment.UserID != order.UserID || payment.PaidCents <= 0 || payment.RefundedCents < 0 || payment.RefundedCents > payment.PaidCents {
			return billing.ErrConflict
		}
		source, err := readBillingSource(tx, order.ID)
		if err != nil {
			return err
		}
		original, _ := json.Marshal(source)
		provided, _ := json.Marshal(result.Source)
		if string(original) != string(provided) {
			return billing.ErrConflict
		}
		fee, err := pricing.PriceActual(source.Rule, source.Meter)
		if err != nil || fee != result.ActualFee || result.CalculationNo != fmt.Sprintf("FEE%020d", order.ID) {
			return billing.ErrConflict
		}
		var existing struct{ ResultJSON []byte }
		previous := tx.Table("charge_fee_receipt").Where("charge_order_id=?", order.ID).Find(&existing)
		if previous.Error != nil {
			return previous.Error
		}
		if previous.RowsAffected > 0 {
			var old billing.Result
			if json.Unmarshal(existing.ResultJSON, &old) != nil {
				return billing.ErrConflict
			}
			saved, _ := json.Marshal(old)
			if string(saved) != string(encoded) {
				return billing.ErrConflict
			}
			return tx.Table("charge_billing_job").Where("charge_order_id=?", order.ID).Updates(map[string]any{"status": "done", "last_error": nil}).Error
		}
		shortfall := result.TotalCents - payment.PaidCents
		if shortfall < 0 {
			shortfall = 0
		}
		if err := tx.Table("charge_fee_receipt").Create(map[string]any{"charge_order_id": order.ID, "calculation_no": result.CalculationNo, "result_json": string(encoded), "shortfall_cents": shortfall}).Error; err != nil {
			return err
		}
		if err := tx.Model(&ChargeOrderRecord{}).Where("id=?", order.ID).Updates(map[string]any{"electric_cents": result.ElectricCents, "service_cents": result.ServiceCents, "total_cents": result.TotalCents}).Error; err != nil {
			return err
		}
		// Order campaigns are evaluated here rather than when the device sent
		// the end frame, because a threshold cannot be decided before the fee
		// is known. This sits after the receipt insert, so a replayed
		// settlement returns early and never grants twice.
		applyOrderCampaigns(tx, order, result.TotalCents)
		var reserved int64
		if err := tx.Model(&RefundRecord{}).Select("COALESCE(SUM(refund_cents),0)").Where("payment_order_id=? AND status IN ('pending','processing') AND deleted_at IS NULL", payment.ID).Scan(&reserved).Error; err != nil {
			return err
		}
		surplus := payment.PaidCents - result.TotalCents - payment.RefundedCents - reserved
		if surplus > 0 {
			refund := RefundRecord{RefundNo: fmt.Sprintf("FEE-R%020d", order.ID), PaymentOrderID: payment.ID, UserID: order.UserID, BizType: "charge", BizID: order.ID, RefundCents: surplus, Reason: sql.NullString{String: "实际计费后的预付差额", Valid: true}, Status: "pending", ExecutionPolicy: "automatic", CreatedMonth: utcDate()}
			if err := tx.Create(&refund).Error; err != nil {
				return err
			}
			if err := tx.Model(&ChargeOrderRecord{}).Where("id=? AND status='completed'", order.ID).Update("status", "refunding").Error; err != nil {
				return err
			}
			eventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("billing-refund:"+result.CalculationNo)).String()
			payload, _ := json.Marshal(map[string]any{"event_id": eventID, "refund_no": refund.RefundNo, "charge_order_id": order.ID, "refund_cents": surplus})
			if err := tx.Create(&EventOutboxRecord{EventID: eventID, Stream: "refund_required_stream", EnvelopeJSON: payload}).Error; err != nil {
				return err
			}
		}
		eventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("billing-complete:"+result.CalculationNo)).String()
		if err := tx.Create(&ChargeEventLogRecord{ChargeOrderID: order.ID, EventID: eventID, Event: "fee_calculated", Actor: "billing", Detail: fmt.Sprintf("electric=%d service=%d total=%d shortfall=%d", result.ElectricCents, result.ServiceCents, result.TotalCents, shortfall), OccurredAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
		// A shortfall becomes a collectable debt in the same transaction, so the
		// amount billed and the amount owed can never disagree. The unique key
		// on charge_order_id makes a replay a no-op without an empty SET clause.
		if shortfall > 0 {
			debtNo := fmt.Sprintf("DEBT%020d", order.ID)
			insert := tx.Table("charge_debt").Create(map[string]any{
				"debt_no": debtNo, "charge_order_id": order.ID, "payment_order_id": payment.ID,
				"user_id": order.UserID, "debt_cents": shortfall, "paid_cents": 0, "status": "unpaid",
			})
			if insert.Error != nil && isDuplicate(insert.Error) {
				// The debt already exists from an earlier attempt; that is the
				// expected outcome of a replayed billing dispatch.
				insert = nil
			}
			if insert != nil {
				return insert.Error
			}
		}
		return tx.Table("charge_billing_job").Where("charge_order_id=?", order.ID).Updates(map[string]any{"status": "done", "last_error": nil}).Error
	})
}

var _ billing.Orders = BillingOrders{}

// applyOrderCampaigns grants any threshold or holiday coupon the order earned.
//
// Settlement is the money path: a coupon must never be able to fail or roll it
// back. A rule that does not apply is not an error, and a rule that errors is
// logged and swallowed, because the alternative — aborting the transaction —
// would leave a real, already-done charge unbilled. The grant is idempotent on
// the order number, so a later retry or a manual re-run can safely re-evaluate.
func applyOrderCampaigns(tx *gorm.DB, order ChargeOrderRecord, totalCents int64) {
	now := time.Now().UTC()
	for _, trigger := range []string{"threshold_redeem", "holiday"} {
		if _, err := ApplyActivityRules(tx, activityEvent{
			TriggerType: trigger,
			UserID:      order.UserID,
			EventKey:    activityEventKeyForOrder(order.OrderNo),
			AmountCents: totalCents,
			Now:         now,
		}); err != nil && !errors.Is(err, errActivityNotApplicable) {
			log.Printf("ACTIVITY RULE FAILED trigger=%s order=%s user=%d: %v", trigger, order.OrderNo, order.UserID, err)
		}
	}
}
