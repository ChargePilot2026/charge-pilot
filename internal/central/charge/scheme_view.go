package charge

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/gin-gonic/gin"
)

func (a UserQueryAPI) schemeView(ctx context.Context, order ChargeOrderRecord, payload gin.H) error {
	var snapshot ChargePricingSnapshotRecord
	if err := a.DB.WithContext(ctx).Where("charge_order_id=?", order.ID).Find(&snapshot).Error; err != nil {
		return err
	}
	var frozen struct {
		Rule  pricing.Rule   `json:"rule"`
		Offer *pricing.Offer `json:"offer"`
	}
	if len(snapshot.PricingSnapshot) > 0 && json.Unmarshal(snapshot.PricingSnapshot, &frozen) == nil && frozen.Rule.Spec.Scheme != nil {
		payload["display"] = frozen.Rule.Spec.Scheme.Display
		payload["offer"] = frozen.Offer
		if frozen.Rule.Spec.Display.ShowTariff {
			payload["tariff"] = frozen.Rule.Spec.Scheme.Amount
			payload["tariff_lines"] = tariffLines(*frozen.Rule.Spec.Scheme, frozen.Offer)
		}
		if frozen.Rule.Spec.Display.ShowRule {
			payload["rule_description"] = frozen.Rule.Spec.Scheme.Remark
		}
	}
	var job struct{ Status string }
	if err := a.DB.WithContext(ctx).Table("charge_billing_job").Where("charge_order_id=?", order.ID).Find(&job).Error; err != nil {
		return err
	}
	payload["billing_status"] = job.Status
	operations := []CardOperation{}
	if err := a.DB.WithContext(ctx).Where("charge_order_id=?", order.ID).Order("created_at").Find(&operations).Error; err != nil {
		return err
	}
	payload["card_operations"] = operations
	payload["start_confirmation_pending"] = order.Status == "paid"
	var cutoff struct {
		CutoffAt *string `json:"cutoff_at"`
		Reason   string  `json:"reason"`
	}
	if err := a.DB.WithContext(ctx).Table("charge_billing_cutoff").Where("charge_order_id=?", order.ID).Find(&cutoff).Error; err != nil {
		return err
	}
	payload["billing_cutoff"] = cutoff
	return nil
}

func tariffLines(s pricing.Scheme, offer *pricing.Offer) []string {
	lines := []string{}
	if offer == nil {
		return lines
	}
	if offer.Mode == "duration" {
		return []string{fmt.Sprintf("套餐 ¥%.2f / %d分钟，按实际完整分钟结算", float64(offer.PriceCents)/100, offer.DurationMinutes)}
	}
	if offer.Mode == "energy" && s.Energy != nil {
		return []string{fmt.Sprintf("电费 ¥%.2f / 度 · 服务费 ¥%.2f / 度", float64(s.Energy.ElectricCents)/100, float64(s.Energy.ServiceCents)/100)}
	}
	if s.Amount == nil {
		return lines
	}
	begin := 0
	clock := func(n int) string { return fmt.Sprintf("%02d:%02d", n/60, n%60) }
	for _, p := range s.Amount.Periods {
		span := clock(begin) + "～" + clock(p.EndMinute)
		if s.Amount.Algorithm == pricing.ModeServerEnergy {
			lines = append(lines, fmt.Sprintf("%s · 电费 ¥%.2f / 度 · 服务费 ¥%.2f / 度", span, float64(p.ElectricCents)/100, float64(p.ServiceCents)/100))
		} else {
			low := 0
			for _, tier := range p.Tiers {
				lines = append(lines, fmt.Sprintf("%s · %d～%d W · 电费 ¥%.2f / 小时 · 服务费 ¥%.2f / 小时", span, low, tier.MaxWatts, float64(tier.ElectricCents)/100, float64(tier.ServiceCents)/100))
				low = tier.MaxWatts + 1
			}
		}
		begin = p.EndMinute
	}
	return lines
}
