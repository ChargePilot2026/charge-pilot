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

// Run stops a charging order only from persisted order and device evidence.
// Network loss alone is not evidence that the customer unplugged: the latest
// device heartbeat must still be fresh for the no-power rule to fire.
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
			if contract.Offer.Mode == "package" && !now.Before(order.StartedAt.Add(time.Duration(contract.Offer.DurationMinutes)*time.Minute)) {
				stop = true
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
						pseudo := protocol.Event{DeviceID: order.DeviceID, Port: order.PortNo, Type: protocol.ChargeEnd, StartedAt: latest.at.Add(-time.Duration(latest.seconds) * time.Second), EndedAt: latest.at, ReceivedAt: latest.at, ChargedSeconds: latest.seconds, EnergyMilliKWh: latest.wh}
						segments := measuredSegments(order.StartedAt, pseudo, samples)
						fee, feeErr := pricing.PriceActual(contract.Rule, pricing.ActualMeter{StartedAt: order.StartedAt, EndedAt: latest.at, ChargedWh: latest.wh, ChargedSeconds: latest.seconds, Segments: segments})
						if feeErr == nil && fee.TotalCents >= contract.Offer.PriceCents {
							stop = true
						}
					}
				}
			}
			if stop {
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
			// Other ports' telemetry cannot prove this one has no power.
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
