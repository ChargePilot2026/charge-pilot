package charge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// EndSynchronizer 把设备结束事件同步给 central；事件清单、命令查询、
// 计量证据、回执冻结、端口释放与处理标记全部经 gateway 内部端点，
// 本组件不持有 gateway 库句柄。
type EndSynchronizer struct {
	Gateway      serviceclient.Client
	GatewayURL   string
	CentralURL   string
	ServiceToken string
	Client       *http.Client
}

// gatewayCommand 与 sync.go 的命令列子集同构。
type gatewayCommand = gatewayStartResult

type endResult struct {
	OrderNo        string `json:"order_no"`
	ChargeOrderID  uint64 `json:"charge_order_id"`
	StartCommandID string `json:"start_command_id"`
	StopCommandID  string `json:"stop_command_id"`
	DeviceID       string `json:"device_id"`
	PortNo         uint8  `json:"port_no"`
	PortID         uint64 `json:"port_id"`
	Meter          struct {
		ChargedWh      uint32                 `json:"charged_wh"`
		ChargedSeconds uint32                 `json:"charged_seconds"`
		EndedAt        time.Time              `json:"ended_at"`
		StopReason     uint8                  `json:"stop_reason"`
		Segments       []pricing.MeterSegment `json:"segments,omitempty"`
	} `json:"meter"`
}

// SyncBatch 在 central 提交最终读数后处理 BB；此前 gateway 保留端口占用，防止新订单复用。
func (s EndSynchronizer) SyncBatch(ctx context.Context) (int, error) {
	if s.GatewayURL == "" || s.ServiceToken == "" {
		return 0, errors.New("charge end synchronizer is not configured")
	}
	base, err := url.Parse(s.CentralURL)
	if err != nil || base.Host == "" || base.User != nil || base.Scheme != "http" && base.Scheme != "https" {
		return 0, errors.New("invalid central URL")
	}
	gateway := newGatewaySyncAPI(s.Gateway, s.GatewayURL, s.ServiceToken)
	var list struct {
		Items []struct {
			ID      uint64 `json:"id"`
			Payload string `json:"payload"`
		} `json:"items"`
	}
	if err := gateway.get(ctx, "/api/v1/internal/end-events", &list); err != nil {
		return 0, err
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	count := 0
	var first error
	processed := make([]uint64, 0, len(list.Items))
	for _, item := range list.Items {
		var event protocol.Event
		if err := json.Unmarshal([]byte(item.Payload), &event); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		orderID, err := strconv.ParseUint(event.OrderNumber, 10, 64)
		if err != nil || orderID == 0 || (event.ConsumerType != 2 && event.ConsumerType != 3) || event.EndedAt.IsZero() {
			if first == nil {
				first = errors.New("invalid dc589 charge end event")
			}
			continue
		}
		var commandReply struct {
			Found   bool           `json:"found"`
			Command gatewayCommand `json:"command"`
		}
		if err := gateway.get(ctx, "/api/v1/internal/charge-commands/by-order/"+strconv.FormatUint(orderID, 10), &commandReply); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if !commandReply.Found {
			if first == nil {
				first = errors.New("charge end has no persisted start command")
			}
			continue
		}
		command := commandReply.Command
		if command.DeviceID != event.DeviceID || command.PortNo != event.Port || command.Status != "acked" || command.PortID == nil || *command.PortID <= 0 {
			if first == nil {
				first = errors.New("charge end does not match an acknowledged START")
			}
			continue
		}
		result := endResult{OrderNo: command.OrderNo, ChargeOrderID: orderID, StartCommandID: command.CommandID, StopCommandID: command.StopCommandID, DeviceID: event.DeviceID, PortNo: event.Port, PortID: uint64(*command.PortID)}
		result.Meter.ChargedWh, result.Meter.ChargedSeconds = event.EnergyMilliKWh, event.ChargedSeconds
		result.Meter.EndedAt, result.Meter.StopReason = event.EndedAt, event.StopReason
		result, err = s.freezeEndResult(ctx, gateway, command, item.ID, event, result)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := postEndResult(ctx, client, *base, s.ServiceToken, result); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		var release struct {
			Released bool `json:"released"`
			Already  bool `json:"already"`
		}
		if err := gateway.post(ctx, "/api/v1/internal/ports/release", map[string]any{"port_id": result.PortID, "order_no": result.OrderNo}, &release); err != nil || !release.Released {
			// central 已确认而端口释放失败是硬错误：占用不释放会阻塞新订单。
			if err == nil {
				err = errors.New("gateway port could not be released after settlement")
			}
			return count, fmt.Errorf("gateway port could not be released after settlement: %w", err)
		}
		processed = append(processed, item.ID)
		count++
	}
	if len(processed) > 0 {
		var mark struct {
			Marked int `json:"marked"`
		}
		if err := gateway.post(ctx, "/api/v1/internal/device-events/mark-processed", map[string]any{"ids": processed}, &mark); err != nil {
			return count, err
		}
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
