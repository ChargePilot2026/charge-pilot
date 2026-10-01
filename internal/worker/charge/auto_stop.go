package charge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AutoStopper struct {
	UserDB, GatewayDB        *gorm.DB
	GatewayURL, ServiceToken string
	Client                   *http.Client
}

type autoStopOrder struct {
	ID              uint64    `gorm:"column:id"`
	OrderNo         string    `gorm:"column:order_no"`
	UserID          uint64    `gorm:"column:user_id"`
	DeviceID        string    `gorm:"column:device_id"`
	PortNo          uint8     `gorm:"column:port_no"`
	StartedAt       time.Time `gorm:"column:started_at"`
	PricingSnapshot []byte    `gorm:"column:pricing_snapshot"`
}

// Run 根据持久化订单和设备证据停机。
// 断电判定要求设备心跳未过期，不能仅凭网络断开认定用户拔枪。
func (s AutoStopper) Run(ctx context.Context) (int, error) {
	if s.UserDB == nil || s.GatewayDB == nil || s.ServiceToken == "" {
		return 0, errors.New("auto stop is not configured")
	}
	now := time.Now().UTC()
	stopped := 0
	var afterID uint64
	for {
		orders := []autoStopOrder{}
		err := s.UserDB.WithContext(ctx).Table("charge_order c").
			Select("c.id,c.order_no,c.user_id,c.device_id,c.port_no,c.started_at,p.pricing_snapshot").
			Joins("JOIN charge_order_pricing p ON p.charge_order_id=c.id").
			Where("c.status='charging' AND c.started_at IS NOT NULL AND c.deleted_at IS NULL AND c.id>?", afterID).
			Order("c.id").Limit(100).Find(&orders).Error
		if err != nil {
			return stopped, err
		}
		if len(orders) == 0 {
			break
		}
		for _, order := range orders {
			var contract struct {
				Rule  pricing.Rule   `json:"rule"`
				Offer *pricing.Offer `json:"offer"`
			}
			if json.Unmarshal(order.PricingSnapshot, &contract) != nil || contract.Offer == nil || !contract.Offer.Valid() {
				continue
			}
			stop := false
			var frozenFee *pricing.Fee
			cutoff, reason := now, "automatic_stop"
			if contract.Offer.Mode == "duration" {
				var card struct{ PurchasedMinutes uint16 }
				if err := s.UserDB.WithContext(ctx).Table("card_charge").Where("charge_order_id=?", order.ID).Find(&card).Error; err != nil {
					return stopped, err
				}
				if card.PurchasedMinutes > 0 {
					contract.Offer.DurationMinutes = card.PurchasedMinutes
					if !now.Before(order.StartedAt.Add(time.Duration(card.PurchasedMinutes) * time.Minute)) {
						shouldStop, err := s.freezeCardDeadline(ctx, order.ID, now)
						if err != nil {
							return stopped, err
						}
						if shouldStop {
							if err := s.requestStop(ctx, order); err != nil {
								return stopped, err
							}
							stopped++
						}
						continue
					}
				}
			}
			if contract.Offer.Mode == "duration" && !now.Before(order.StartedAt.Add(time.Duration(contract.Offer.DurationMinutes)*time.Minute)) {
				stop = true
				cutoff = order.StartedAt.Add(time.Duration(contract.Offer.DurationMinutes) * time.Minute)
				reason = "duration_exhausted"
			}
			if contract.Offer.Mode == "amount" && contract.Rule.Spec.TimeCharge != nil && contract.Rule.Spec.TimeCharge.MaxMinutes > 0 {
				limit := order.StartedAt.Add(time.Duration(contract.Rule.Spec.TimeCharge.MaxMinutes) * time.Minute)
				if !now.Before(limit) {
					stop = true
					cutoff = limit
					reason = "duration_limit"
				}
			}
			if !stop {
				var rows []workerDeviceEventRow
				err := s.GatewayDB.WithContext(ctx).Where("device_id=? AND event_type='heartbeat' AND received_at>=?", order.DeviceID, order.StartedAt).
					Order("id DESC").Limit(10080).Find(&rows).Error
				if err != nil {
					return stopped, err
				}
				for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
					rows[left], rows[right] = rows[right], rows[left]
				}
				samples := make([]protocol.Event, 0, len(rows))
				for _, row := range rows {
					var event protocol.Event
					if err := json.Unmarshal(row.EventJSON, &event); err != nil {
						return stopped, err
					}
					samples = append(samples, event)
				}
				if noPowerForMinute(samples, order.PortNo, now) {
					stop = true
				}
				if !stop && contract.Offer.Mode == "amount" {
					if latest, ok := latestMeter(samples, order.PortNo, now); ok {
						plan, capped, feeErr := pricing.BudgetStopAtMeter(contract.Rule, contract.Offer, measuredMeter(order, latest, samples))
						if feeErr != nil {
							log.Printf("charge budget unavailable order=%s device=%s port=%d sampled_at=%s error=%v", order.OrderNo, order.DeviceID, order.PortNo, latest.at.Format(time.RFC3339), feeErr)
						} else if plan.ShouldStop {
							frozenFee = capped
							stop = true
							cutoff = latest.at
							reason = "budget_exhausted"
							log.Printf("charge budget exhausted order=%s device=%s port=%d budget_cents=%d fee_lower_bound_cents=%d exact_split=%t sampled_at=%s", order.OrderNo, order.DeviceID, order.PortNo, contract.Offer.PriceCents, plan.AccruedCents, capped != nil, latest.at.Format(time.RFC3339))
						}
					}
				}

			}
			if stop {
				row := map[string]any{"charge_order_id": order.ID, "cutoff_at": cutoff, "reason": reason}
				if frozenFee != nil {
					row["electric_cents"] = frozenFee.ElectricCents
					row["service_cents"] = frozenFee.ServiceCents
				}
				if err := s.UserDB.WithContext(ctx).Table("charge_billing_cutoff").Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).Create(row).Error; err != nil {
					return stopped, err
				}
				if err := s.requestStop(ctx, order); err != nil {
					return stopped, err
				}
				stopped++
			}
		}
		afterID = orders[len(orders)-1].ID
	}
	return stopped, nil
}

// spendCapReached 判断服务端计费会话是否达到费率费用上限。
// 上限为零或费率无法计价时返回 false，由已有额度和停机规则处理。
func spendCapReached(rule pricing.Rule, samples []protocol.Event, order autoStopOrder, now time.Time) bool {
	spec := rule.Spec
	if !spec.Mode.ServerBilled() || spec.SpendCapCents <= 0 {
		return false
	}
	latest, ok := latestMeter(samples, order.PortNo, now)
	if !ok {
		// 费用上限判断必须使用未过期的计量读数；设备不可达由其他规则处理。
		return false
	}
	plan, err := pricing.StopAtMeter(rule, measuredMeter(order, latest, samples))
	if err != nil {
		// ErrMeterReview 表示分段电量不一致，需要计费核实，不作为消费超限停机依据。
		return false
	}
	return plan.ShouldStop
}

// measuredMeter 用最新读数和遥测能佐证的各段数据，
// 为运行中的会话拼出计量记录，
// 让消费上限判定与结算对同一份测量结果定价。
func measuredMeter(order autoStopOrder, latest meterReading, samples []protocol.Event) pricing.ActualMeter {
	ended := latest.at
	meter := pricing.ActualMeter{
		StartedAt:      order.StartedAt,
		EndedAt:        ended,
		ChargedWh:      latest.wh,
		ChargedSeconds: latest.seconds,
	}
	// 将当前读数转换为终止事件视图，供 measuredSegments 校验完整区间，不生成额外测量。
	terminal := protocol.Event{
		DeviceID: order.DeviceID, Port: order.PortNo, Type: protocol.ChargeEnd,
		StartedAt: ended.Add(-time.Duration(latest.seconds) * time.Second),
		EndedAt:   ended, ReceivedAt: ended,
		ChargedSeconds: latest.seconds, EnergyMilliKWh: latest.wh,
	}
	meter.Segments = measuredSegments(order.StartedAt, terminal, samples)
	return meter
}

type meterReading struct {
	at          time.Time
	wh, seconds uint32
}

func latestMeter(samples []protocol.Event, port uint8, now time.Time) (meterReading, bool) {
	for _, e := range slices.Backward(samples) {

		if now.Sub(e.ReceivedAt) > 30*time.Second {
			break
		}
		for _, p := range e.ChargingPorts {
			if p.Port == port && p.ChargedMWh%1000 == 0 {
				return meterReading{at: e.ReceivedAt, wh: p.ChargedMWh / 1000, seconds: p.ChargedSeconds}, true
			}
		}
	}
	return meterReading{}, false
}
func noPowerForMinute(samples []protocol.Event, port uint8, now time.Time) bool {
	if len(samples) == 0 || now.Sub(samples[len(samples)-1].ReceivedAt) > 30*time.Second {
		return false
	}
	var since time.Time
	for _, e := range samples {
		if e.Type != protocol.Heartbeat {
			continue
		}
		seen, positive := false, false
		for _, p := range e.ChargingPorts {
			if p.Port == port {
				seen = true
				positive = p.PowerDeciWatts > 0
				break
			}
		}
		if !seen {
			// 其他端口的遥测无法证明这个端口没有电。
			since = time.Time{}
			continue
		}
		if positive {
			since = time.Time{}
		} else if since.IsZero() {
			since = e.ReceivedAt
		}
	}
	return !since.IsZero() && now.Sub(since) >= time.Minute
}

func (s AutoStopper) requestStop(ctx context.Context, order autoStopOrder) error {
	base, err := url.Parse(s.GatewayURL)
	if err != nil || base.Host == "" || base.User != nil || base.Scheme != "http" && base.Scheme != "https" {
		return errors.New("invalid auto stop gateway URL")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/internal/charge-orders/stop"
	body, _ := json.Marshal(map[string]any{"order_no": order.OrderNo, "user_id": order.UserID, "source": "auto"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Service-Token", s.ServiceToken)
	req.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("auto stop for %s returned %d", order.OrderNo, resp.StatusCode)
	}
	return nil
}
