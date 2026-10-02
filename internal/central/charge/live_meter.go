package charge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/central/card"
	orderpkg "github.com/ChargePilot2026/charge-pilot/internal/central/order"
	"net/url"
	"slices"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"gorm.io/gorm"
)

type LiveMeterView struct {
	At             time.Time `json:"at"`
	Stale          bool      `json:"stale"`
	KWh            float64   `json:"kwh"`
	Seconds        uint32    `json:"seconds"`
	Fee            *LiveFee  `json:"fee,omitempty"`
	FeeUnavailable string    `json:"fee_unavailable,omitempty"`
}

type LiveFee struct {
	ElectricCents int64 `json:"electric_cents"`
	ServiceCents  int64 `json:"service_cents"`
	TotalCents    int64 `json:"total_cents"`
}

type LiveMeterService struct {
	DB                       *gorm.DB
	GatewayURL, ServiceToken string
	Client                   serviceclient.Client
}

// Read does not mutate settlement fields. Fees price the last observed meter,
// never wall-clock time after a disconnection or the current station template.
func (s LiveMeterService) Read(ctx context.Context, id uint64, device string, port uint8, start time.Time) (*LiveMeterView, error) {
	var response struct {
		Code int
		Data struct {
			Samples  []protocol.Event
			Complete bool
		}
	}
	path := fmt.Sprintf("/api/v1/internal/devices/%s/charging-samples?port_no=%d&started_at=%s", url.PathEscape(device), port, url.QueryEscape(start.UTC().Format(time.RFC3339Nano)))
	if err := s.Client.GetJSON(ctx, s.GatewayURL, s.ServiceToken, path, &response); err != nil {
		return nil, err
	}
	if response.Code != 0 {
		return nil, errors.New("meter service unavailable")
	}
	view, meter, ok := liveMeterEvidence(device, port, start, response.Data.Samples, time.Now().UTC())
	if !ok {
		return nil, nil
	}
	if !response.Data.Complete {
		view.FeeUnavailable = "计量证据超过范围，费用待结算"
		return view, nil
	}
	var snapshot orderpkg.ChargePricingSnapshotRecord
	if err := s.DB.WithContext(ctx).Where("charge_order_id=?", id).Take(&snapshot).Error; err != nil {
		return view, err
	}
	var contract struct {
		Rule  pricing.Rule   `json:"rule"`
		Offer *pricing.Offer `json:"offer"`
	}
	if json.Unmarshal(snapshot.PricingSnapshot, &contract) != nil || contract.Offer == nil {
		view.FeeUnavailable = "缺少订单计费快照"
		return view, nil
	}
	var card card.CardCharge
	if err := s.DB.WithContext(ctx).Where("charge_order_id=?", id).Find(&card).Error; err != nil {
		return view, err
	}
	if card.ChargeOrderID != 0 {
		base := contract.Offer.PriceCents
		if base <= 0 || card.PaidCents <= 0 || card.PaidCents%base != 0 || card.PaidCents/base > 65535 {
			view.FeeUnavailable = "刷卡购买记录待确认"
			return view, nil
		}
		contract.Offer.PurchaseCount = uint16(card.PaidCents / base)
		contract.Offer.PriceCents = card.PaidCents
		contract.Offer.DurationMinutes = card.PurchasedMinutes
	}
	fee, err := pricing.PriceOfferActual(contract.Rule, contract.Offer, meter)
	if err != nil {
		view.FeeUnavailable = "计量不足以计算当前费用"
		return view, nil
	}
	view.Fee = &LiveFee{ElectricCents: fee.ElectricCents, ServiceCents: fee.ServiceCents, TotalCents: fee.TotalCents}
	return view, nil
}

func liveMeterEvidence(device string, port uint8, start time.Time, samples []protocol.Event, now time.Time) (*LiveMeterView, pricing.ActualMeter, bool) {
	for i, e := range slices.Backward(samples) {

		if e.Type != protocol.Heartbeat || e.DeviceID != device || e.ReceivedAt.Before(start) || e.ReceivedAt.After(now) {
			continue
		}
		for _, p := range e.ChargingPorts {
			if p.Port == port {
				view := &LiveMeterView{At: e.ReceivedAt, Stale: now.Sub(e.ReceivedAt) > 30*time.Second, KWh: float64(p.ChargedMWh) / 1e6, Seconds: p.ChargedSeconds}
				end := protocol.Event{DeviceID: device, Port: port, StartedAt: e.ReceivedAt.Add(-time.Duration(p.ChargedSeconds) * time.Second), EndedAt: e.ReceivedAt, ReceivedAt: e.ReceivedAt, ChargedSeconds: p.ChargedSeconds, EnergyMilliKWh: p.ChargedMWh / 1000}
				meter := pricing.ActualMeter{StartedAt: start, EndedAt: e.ReceivedAt, ChargedWh: p.ChargedMWh / 1000, ChargedSeconds: p.ChargedSeconds, Segments: pricing.MeasuredSegments(start, end, samples[:i+1])}
				// 实时估算以首次观测负载补齐最初不超过 30 秒的心跳区间。
				// 最终结算仍使用严格的计量证据校验。
				if len(meter.Segments) > 0 && meter.Segments[0].PowerW == nil && meter.Segments[0].EndedAt.Sub(start) <= 30*time.Second {
					for _, sample := range samples[:i+1] {
						found := false
						for _, initial := range sample.ChargingPorts {
							if initial.Port == port && sample.DeviceID == device {
								power := (initial.PowerDeciWatts + 5) / 10
								meter.Segments[0].PowerW = &power
								meter.Segments[0].PeakW = power
								found = true
								break
							}
						}
						if found {
							break
						}
					}
				}
				// Short sampling interruptions can be estimated from the preceding
				// observed load, but never turn this into final settlement evidence.
				for j := 1; j < len(meter.Segments); j++ {
					segment := &meter.Segments[j]
					previous := meter.Segments[j-1]
					if segment.PowerW == nil && segment.EndedAt.Sub(segment.StartedAt) <= 180*time.Second && previous.PowerW != nil {
						power := *previous.PowerW
						for _, sample := range samples[:i+1] {
							for _, reading := range sample.ChargingPorts {
								if reading.Port == port && sample.DeviceID == device {
									at := end.StartedAt.Add(time.Duration(reading.ChargedSeconds) * time.Second)
									if !at.After(segment.StartedAt) {
										power = (reading.PowerDeciWatts + 5) / 10
									}
								}
							}
						}
						segment.PowerW = &power
						segment.PeakW = power
						for _, sample := range samples[:i+1] {
							for _, reading := range sample.ChargingPorts {
								if reading.Port == port && sample.DeviceID == device {
									at := end.StartedAt.Add(time.Duration(reading.ChargedSeconds) * time.Second)
									if at.After(segment.StartedAt) && !at.After(segment.EndedAt) {
										segment.PeakW = max(segment.PeakW, (reading.PowerDeciWatts+5)/10)
									}
								}
							}
						}
					}
				}
				if p.ChargedMWh%1000 != 0 {
					meter.ReviewRequired = true
				}
				return view, meter, true
			}
		}
	}
	return nil, pricing.ActualMeter{}, false
}
