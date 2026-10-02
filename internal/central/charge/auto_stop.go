package charge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"gorm.io/gorm"
)

// 每次向 gateway 批量取证的最大订单数，与 gateway 批量端点上界一致。
const autoStopEvidenceChunk = 20

// 单批订单取证的最多翻页次数；7 天窗口按每分钟一条心跳计通常一页足够，
// 翻页上限防止异常高密度心跳把一次停机扫描拖死。
const autoStopEvidenceMaxPages = 8

// AutoStopper 在 central 侧根据持久化订单与设备证据判定停机。
// 计费截止点与停机判定同属一个进程：判定与冻结同事务语义，无跨进程中间态；
// 设备心跳证据经 gateway 批量端点按订单拉取，不直读 gateway 库。
type AutoStopper struct {
	UserDB *gorm.DB
	// Gateway 调用 gateway 内部端点：批量取证与停机下发。
	Gateway      serviceclient.Client
	GatewayURL   string
	ServiceToken string
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
	if s.UserDB == nil || s.GatewayURL == "" || s.ServiceToken == "" {
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
		evidence, err := s.evidence(ctx, orders)
		if err != nil {
			return stopped, err
		}
		for _, order := range orders {
			var contract struct {
				Rule  pricing.Rule   `json:"rule"`
				Offer *pricing.Offer `json:"offer"`
			}
			if json.Unmarshal(order.PricingSnapshot, &contract) != nil || contract.Offer == nil || !contract.Offer.Valid() {
				continue
			}
			samples := evidence[order.ID]
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
				// 计费截止点按订单主键首写冻结：已有记录表明此前已冻结，
				// 视为冲突返回错误交由外层定时循环重试，不做静默空更新。
				var frozen int64
				if err := s.UserDB.WithContext(ctx).Table("charge_billing_cutoff").Where("charge_order_id=?", order.ID).Count(&frozen).Error; err != nil {
					return stopped, err
				}
				if frozen > 0 {
					return stopped, fmt.Errorf("charge billing cutoff already frozen for order %s", order.OrderNo)
				}
				if err := s.UserDB.WithContext(ctx).Table("charge_billing_cutoff").Create(row).Error; err != nil {
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

// evidence 按订单分批向 gateway 取证，返回订单 ID 到心跳样本的映射。
// 单批超过一页时按 next_after_id 续取，翻页上限 autoStopEvidenceMaxPages。
func (s AutoStopper) evidence(ctx context.Context, orders []autoStopOrder) (map[uint64][]protocol.Event, error) {
	out := make(map[uint64][]protocol.Event, len(orders))
	type request struct {
		DeviceID  string    `json:"device_id"`
		PortNo    uint8     `json:"port_no"`
		StartedAt time.Time `json:"started_at"`
		AfterID   uint64    `json:"after_id"`
	}
	type evidenceEntry struct {
		Samples     []protocol.Event
		Complete    bool
		NextAfterID uint64
	}
	for start := 0; start < len(orders); start += autoStopEvidenceChunk {
		chunk := orders[start:min(start+autoStopEvidenceChunk, len(orders))]
		requests := make([]request, len(chunk))
		for i, order := range chunk {
			requests[i] = request{DeviceID: order.DeviceID, PortNo: order.PortNo, StartedAt: order.StartedAt}
		}
		for page := 0; page < autoStopEvidenceMaxPages; page++ {
			var response struct {
				Code int
				Data struct {
					Evidence []evidenceEntry
				}
			}
			if err := s.Gateway.Post(ctx, s.GatewayURL, s.ServiceToken, "/api/v1/internal/charging-evidence", map[string]any{"requests": requests}, &response); err != nil {
				return nil, err
			}
			if response.Code != 0 || len(response.Data.Evidence) != len(requests) {
				return nil, errors.New("charging evidence response incomplete")
			}
			complete := true
			for i := range requests {
				entry := response.Data.Evidence[i]
				out[chunk[i].ID] = append(out[chunk[i].ID], entry.Samples...)
				requests[i].AfterID = entry.NextAfterID
				if !entry.Complete {
					complete = false
				}
			}
			if complete {
				break
			}
		}
	}
	return out, nil
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
	meter.Segments = pricing.MeasuredSegments(order.StartedAt, terminal, samples)
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

// requestStop 向 gateway 下发停机；gateway 受理后异步回执，
// 本调用只确认意图被受理（202），不确认端口已停。
func (s AutoStopper) requestStop(ctx context.Context, order autoStopOrder) error {
	return s.Gateway.Post(ctx, s.GatewayURL, s.ServiceToken, "/api/v1/internal/charge-orders/stop", map[string]any{
		"order_no": order.OrderNo, "user_id": order.UserID, "source": "auto",
	}, nil)
}
