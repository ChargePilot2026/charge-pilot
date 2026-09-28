package billing

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Orders interface {
	Due(context.Context) ([]uint64, error)
	Read(context.Context, uint64) (Source, error)
	Apply(context.Context, Result) error
	Defer(context.Context, uint64, string, string) error
}
type Service struct {
	Store        Store
	Orders       Orders
	Splits       SplitResolver
	ServiceToken string
	// Bills issues the customer-visible bill once the fee is known. It is
	// optional so the billing job still runs in deployments that have not
	// migrated the bill tables yet.
	Bills BillIssuer
}

// BillIssuer is the subset of the bill store billing needs, kept as an
// interface so this package does not depend on the charge module.
type BillIssuer interface {
	Issue(ctx context.Context, chargeOrderID uint64) (uint64, error)
}

func (s Service) Run(ctx context.Context) (int, error) {
	delivered, deliveryErr := s.deliverPending(ctx)
	ids, err := s.Orders.Due(ctx)
	if err != nil {
		return 0, err
	}
	count := delivered
	first := deliveryErr
	for _, id := range ids {
		source, err := s.Orders.Read(ctx, id)
		if err == nil {
			var result Result
			result, err = s.Store.Calculate(ctx, source)
			if errors.Is(err, pricing.ErrMeterReview) || errors.Is(err, pricing.ErrInvalidPricing) {
				payload, _ := json.Marshal(source)
				err = s.Store.DB.WithContext(ctx).Table("manual_fee_review").Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).Create(map[string]any{"charge_order_id": id, "order_no": source.OrderNo, "reason": "实际计量或计费快照需核实", "energy_wh": source.Meter.ChargedWh, "created_month": time.Now().UTC().Format("2006-01"), "source_json": string(payload)}).Error
				if err == nil {
					err = s.Orders.Defer(ctx, id, "manual_review", "实际计量或计费快照需核实")
				}
				if err == nil {
					continue
				}
			} else if err == nil {
				err = s.Orders.Apply(ctx, result)
				if err == nil {
					err = s.Store.MarkDelivered(ctx, id)
				}
				if err == nil {
					// The bill is issued from the same committed fee, so what the
					// customer is shown cannot drift from what was charged.
					if s.Bills != nil {
						if _, billErr := s.Bills.Issue(ctx, id); billErr != nil && first == nil {
							first = billErr
						}
					}
					// Allocation is recorded after the fee is durably known so a
					// settlement always has a persisted calculation to attach to.
					if splitErr := s.settleCalculation(ctx, source, result); splitErr != nil && first == nil {
						first = splitErr
					}
					count++
					continue
				}
			}
		}
		if err != nil {
			// Do not expose storage errors in the operator-facing queue.
			_ = s.Orders.Defer(ctx, id, "pending", "计费依赖暂不可用，等待重试")
			if first == nil {
				first = fmt.Errorf("bill order %d: %w", id, err)
			}
		}
	}
	if settled, splitErr := s.settleBacklog(ctx); splitErr != nil {
		if first == nil {
			first = splitErr
		}
	} else {
		count += settled
	}
	return count, first
}
func (s Service) Register(r *gin.Engine) {
	r.POST("/api/v1/internal/billing/dispatch", func(c *gin.Context) {
		got, want := sha256.Sum256([]byte(c.GetHeader("X-Service-Token"))), sha256.Sum256([]byte(s.ServiceToken))
		if s.ServiceToken == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			httpapi.Write(c, 401, 1001, "service token invalid", nil)
			return
		}
		n, err := s.Run(c.Request.Context())
		if err != nil {
			httpapi.Write(c, 503, 5003, "计费任务稍后重试", gin.H{"completed": n})
			return
		}
		httpapi.OK(c, gin.H{"completed": n})
	})
}

// settleCalculation records the allocation for one freshly calculated fee. The
// calculation number is derived from the order id, so the settlement no carries
// the same deterministic identity as the fee it splits.
func (s Service) settleCalculation(ctx context.Context, source Source, result Result) error {
	// Settlement is an additional ledger on top of a valid fee. Without a
	// resolver the deployment simply does not split, which must never fail the
	// charge that was already billed correctly.
	if source.Rule.StationID == 0 || s.Splits.AdminDB == nil {
		return nil
	}
	calculationID, err := s.Store.CalculationID(ctx, source.ChargeOrderID)
	if err != nil {
		return err
	}
	template, err := s.Splits.Resolve(ctx, source.Rule.StationID)
	if err != nil {
		if errors.Is(err, ErrNoSplitTemplate) || errors.Is(err, gorm.ErrRecordNotFound) {
			// A station without an active split template is a configuration gap,
			// not a billing failure: the fee stays valid and settlement retries.
			return nil
		}
		return err
	}
	month, err := s.Store.CalculationMonth(ctx, calculationID)
	if err != nil {
		return err
	}
	_, _, err = s.Store.Settle(ctx, calculationID, template, result.ActualFee, month)
	return err
}

// CalculationID resolves the persisted fee_calculation primary key for an order.
// The calculation number is derived from the order id, but the settlement ledger
// references the real row id, so it is read from the receipt instead of guessed.
func (s Store) CalculationID(ctx context.Context, chargeOrderID uint64) (uint64, error) {
	var row struct {
		CalculationID *uint64 `gorm:"column:calculation_id"`
	}
	if err := s.DB.WithContext(ctx).Table("fee_receipt").Select("calculation_id").Where("charge_order_id = ?", chargeOrderID).Take(&row).Error; err != nil {
		return 0, err
	}
	if row.CalculationID == nil {
		return 0, ErrConflict
	}
	return *row.CalculationID, nil
}

// settleBacklog covers fees calculated before settlement existed, or a station
// that gained a split template later. It never rewrites an existing settlement.
func (s Service) settleBacklog(ctx context.Context) (int, error) {
	if s.Splits.AdminDB == nil {
		return 0, nil
	}
	pending, err := s.Store.SettlementsDue(ctx, 50)
	if err != nil {
		return 0, err
	}
	count, first := 0, error(nil)
	for _, row := range pending {
		if row.StationID == nil {
			continue
		}
		template, err := s.Splits.Resolve(ctx, *row.StationID)
		if err != nil {
			if errors.Is(err, ErrNoSplitTemplate) || errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if first == nil {
				first = err
			}
			continue
		}
		month, err := s.Store.CalculationMonth(ctx, row.CalculationID)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		_, created, err := s.Store.Settle(ctx, row.CalculationID, template, pricing.ActualFee{
			ElectricCents: row.ElectricCents, ServiceCents: row.ServiceCents, TotalCents: row.TotalCents,
		}, month)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if created {
			count++
		}
	}
	return count, first
}

// MarkDelivered is separated from user commit; retries recover either side.
func (s Store) MarkDelivered(ctx context.Context, id uint64) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("fee_delivery").Where("charge_order_id=?", id).Updates(map[string]any{"delivered": true, "delivered_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error; err != nil {
			return err
		}
		return tx.Table("manual_fee_review").Where("charge_order_id=? AND status='pending'", id).Updates(map[string]any{"status": "resolved", "resolved_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error
	})
}

func (s Service) deliverPending(ctx context.Context) (int, error) {
	var rows []struct {
		ChargeOrderID uint64
		PayloadJSON   []byte
	}
	if err := s.Store.DB.WithContext(ctx).Table("fee_delivery").Where("delivered=0 AND scheduled_at<=UTC_TIMESTAMP(3)").Order("scheduled_at,charge_order_id").Limit(50).Find(&rows).Error; err != nil {
		return 0, err
	}
	count := 0
	var first error
	for _, row := range rows {
		var result Result
		err := json.Unmarshal(row.PayloadJSON, &result)
		if err == nil && result.Source.ChargeOrderID != row.ChargeOrderID {
			err = ErrConflict
		}
		if err == nil {
			err = s.Orders.Apply(ctx, result)
		}
		if err == nil {
			err = s.Store.MarkDelivered(ctx, row.ChargeOrderID)
		}
		if err == nil {
			count++
			continue
		}
		_ = s.Store.DB.WithContext(ctx).Table("fee_delivery").Where("charge_order_id=? AND delivered=0", row.ChargeOrderID).Updates(map[string]any{"attempts": gorm.Expr("attempts+1"), "scheduled_at": time.Now().UTC().Add(time.Minute)}).Error
		if first == nil {
			first = err
		}
	}
	return count, first
}
