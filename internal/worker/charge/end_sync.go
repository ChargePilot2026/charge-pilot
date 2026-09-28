package charge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	chargejobdb "github.com/ChargePilot2026/charge-pilot/internal/worker/charge/generated"
)

type EndSynchronizer struct {
	GatewayDB    *sql.DB
	CentralURL   string
	ServiceToken string
	Client       *http.Client
}

type endResult struct {
	OrderNo        string `json:"order_no"`
	ChargeOrderID  uint64 `json:"charge_order_id"`
	StartCommandID string `json:"start_command_id"`
	StopCommandID  string `json:"stop_command_id"`
	DeviceID       string `json:"device_id"`
	PortNo         uint8  `json:"port_no"`
	PortID         uint64 `json:"port_id"`
	Meter          struct {
		ChargedWh      uint32    `json:"charged_wh"`
		ChargedSeconds uint32    `json:"charged_seconds"`
		EndedAt        time.Time `json:"ended_at"`
		StopReason     uint8     `json:"stop_reason"`
	} `json:"meter"`
}

// SyncBatch handles BB only after central commits the final reading. Until
// then gateway keeps the port owned, so another order cannot reuse it.
func (s EndSynchronizer) SyncBatch(ctx context.Context) (int, error) {
	if s.GatewayDB == nil || s.ServiceToken == "" {
		return 0, errors.New("charge end synchronizer is not configured")
	}
	base, err := url.Parse(s.CentralURL)
	if err != nil || base.Host == "" || base.User != nil || base.Scheme != "http" && base.Scheme != "https" {
		return 0, errors.New("invalid central URL")
	}
	q := chargejobdb.New(s.GatewayDB)
	events, err := q.PendingChargeEnds(ctx)
	if err != nil {
		return 0, err
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	count := 0
	var first error
	for _, item := range events {
		var event protocol.Event
		if err := json.Unmarshal(item.EventJson, &event); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		orderID, err := strconv.ParseUint(event.OrderNumber, 10, 64)
		if err != nil || orderID == 0 || event.ConsumerType != 2 || event.EndedAt.IsZero() {
			if first == nil {
				first = errors.New("invalid dc589 charge end event")
			}
			continue
		}
		command, err := q.StartCommandForEnd(ctx, orderID)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if command.DeviceID != event.DeviceID || command.PortNo != event.Port || command.Status != chargejobdb.ChargeCommandStatusAcked || !command.PortID.Valid || command.PortID.Int64 <= 0 {
			if first == nil {
				first = errors.New("charge end does not match an acknowledged START")
			}
			continue
		}
		result := endResult{OrderNo: command.OrderNo, ChargeOrderID: orderID, StartCommandID: command.CommandID, StopCommandID: command.StopCommandID, DeviceID: event.DeviceID, PortNo: event.Port, PortID: uint64(command.PortID.Int64)}
		result.Meter.ChargedWh, result.Meter.ChargedSeconds = event.EnergyMilliKWh, event.ChargedSeconds
		result.Meter.EndedAt, result.Meter.StopReason = event.EndedAt, event.StopReason
		if err := postEndResult(ctx, client, *base, s.ServiceToken, result); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		released, err := q.ReleasePortAfterFinalizedEnd(ctx, chargejobdb.ReleasePortAfterFinalizedEndParams{ID: uint64(command.PortID.Int64), CurrentOrderID: sql.NullString{String: command.OrderNo, Valid: true}})
		if err != nil {
			return count, err
		}
		changed, err := released.RowsAffected()
		if err != nil {
			return count, err
		}
		if changed == 0 {
			port, err := q.PortEndState(ctx, uint64(command.PortID.Int64))
			if err != nil || port.Status != chargejobdb.DevicePortStatusIdle || port.CurrentOrderID.Valid {
				return count, errors.New("gateway port could not be released after settlement")
			}
		}
		if err := q.MarkChargeEndProcessed(ctx, item.ID); err != nil {
			return count, err
		}
		count++
	}
	return count, first
}

func postEndResult(ctx context.Context, client *http.Client, base url.URL, token string, result endResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/internal/charge-orders/" + url.PathEscape(result.OrderNo) + "/end-result"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Service-Token", token)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("central end result: HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Code int `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Code != 0 {
		return fmt.Errorf("central end result: code %d", envelope.Code)
	}
	return nil
}
