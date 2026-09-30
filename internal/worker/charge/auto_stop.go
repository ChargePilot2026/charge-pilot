package charge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

// Run 只依据已落库的订单与设备证据来停止充电订单。
// 单凭网络断开不能证明用户拔了枪：最新一次设备心跳必须仍然是新鲜的，
// 断电规则才会触发。
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
						fee, feeErr := pricing.PriceActual(contract.Rule, measuredMeter(order, latest, samples))
						if feeErr == nil && fee.TotalCents >= contract.Offer.PriceCents {
							capped, err := pricing.PriceOfferActual(contract.Rule, contract.Offer, measuredMeter(order, latest, samples))
							if err != nil {
								return stopped, err
							}
							frozenFee = &capped
							stop = true
							cutoff = latest.at
							reason = "budget_exhausted"
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

// spendCapReached 判断一个运行中的服务端计费会话
// 是否已经触到其费率声明的上限。
//
// 上限为 0 表示不设上限，
// 此时会话照旧由开始时下发的额度来终止，与没有上限时一样。
// 引擎已经算不出价的费率同样不产生判定：
// 这是一条停止规则，
// 而从引擎拒绝的费率推导出来的规则，
// 不构成切断别人充电的理由。
// 两种情况都返回 false，把决定权留给既有规则。
func spendCapReached(rule pricing.Rule, samples []protocol.Event, order autoStopOrder, now time.Time) bool {
	spec := rule.Spec
	if !spec.Mode.ServerBilled() || spec.SpendCapCents <= 0 {
		return false
	}
	latest, ok := latestMeter(samples, order.PortNo, now)
	if !ok {
		// 没有新鲜读数就无从声称已经花了多少钱。
		// 桩不可达是另一个问题，在别处处理；
		// 在这里拿陈旧计量去猜，
		// 会停掉那些离上限还很远的会话。
		return false
	}
	plan, err := pricing.StopAtMeter(rule, measuredMeter(order, latest, samples))
	if err != nil {
		// 定不下来的计量是结算复核要回答的问题，不是停止充电的理由。
		// 这里的 ErrMeterReview 意味着各段电量对不上，
		// 那是计费问题，不是超限。
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
	// measuredSegments 需要一个可以测量到头的终止事件，
	// 所以把这条读数本身装扮成终止事件。
	// 它是同一份遥测的视图，不是第二次测量。
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
	for i := len(samples) - 1; i >= 0; i-- {
		e := samples[i]
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
