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
	"gorm.io/gorm"
)

type EndSynchronizer struct {
	GatewayDB    *gorm.DB
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
	var events []workerDeviceEventRow
	err = s.GatewayDB.WithContext(ctx).Where("event_type = 'charge_end' AND processed_at IS NULL AND JSON_EXTRACT(event_json, '$.ConsumerType') = 2").
		Order("id").Limit(50).Find(&events).Error
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
		if err := json.Unmarshal(item.EventJSON, &event); err != nil {
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
		var command workerChargeCommandRow
		err = s.GatewayDB.WithContext(ctx).Where("charge_order_id = ?", orderID).Take(&command).Error
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if command.DeviceID != event.DeviceID || command.PortNo != event.Port || command.Status != "acked" || !command.PortID.Valid || command.PortID.Int64 <= 0 {
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
		released := s.GatewayDB.WithContext(ctx).Model(&workerDevicePortRow{}).
			Where("id = ? AND current_order_id = ? AND status = 'charging'", uint64(command.PortID.Int64), command.OrderNo).
			Updates(map[string]any{"status": "idle", "current_order_id": nil})
		if released.Error != nil {
			return count, released.Error
		}
		if released.RowsAffected == 0 {
			var port workerDevicePortRow
			err := s.GatewayDB.WithContext(ctx).Select("status, current_order_id").Where("id = ?", uint64(command.PortID.Int64)).Take(&port).Error
			if err != nil || port.Status != "idle" || port.CurrentOrderID.Valid {
				return count, errors.New("gateway port could not be released after settlement")
			}
		}
		if err := s.GatewayDB.WithContext(ctx).Model(&workerDeviceEventRow{}).Where("id = ? AND processed_at IS NULL", item.ID).
			Update("processed_at", gorm.Expr("CURRENT_TIMESTAMP(3)")).Error; err != nil {
			return count, err
		}
		count++
	}
	return count, first
}

type workerDeviceEventRow struct {
	ID          uint64       `gorm:"column:id;primaryKey"`
	EventType   string       `gorm:"column:event_type"`
	EventJSON   []byte       `gorm:"column:event_json"`
	ProcessedAt sql.NullTime `gorm:"column:processed_at"`
}

func (workerDeviceEventRow) TableName() string { return "device_event" }

type workerDevicePortRow struct {
	ID             uint64         `gorm:"column:id;primaryKey"`
	Status         string         `gorm:"column:status"`
	CurrentOrderID sql.NullString `gorm:"column:current_order_id"`
}

func (workerDevicePortRow) TableName() string { return "device_port" }

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
