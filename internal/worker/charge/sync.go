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
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

// Synchronizer 把设备启动回执同步给 central；回执清单与上报标记
// 经 gateway 内部端点读写，本组件不持有 gateway 库句柄。
type Synchronizer struct {
	Gateway       serviceclient.Client
	GatewayURL    string
	CentralURL    string
	ServiceToken  string
	CommandFilter string
	Client        *http.Client
}

var ErrCentralConflict = errors.New("central rejected device start result")

type startResult struct {
	CommandID     string    `json:"command_id"`
	ChargeOrderID uint64    `json:"charge_order_id"`
	OrderNo       string    `json:"order_no"`
	DeviceID      string    `json:"device_id"`
	PortNo        uint8     `json:"port_no"`
	PortID        uint64    `json:"port_id"`
	Success       bool      `json:"success"`
	ResultCode    uint8     `json:"result_code"`
	OccurredAt    time.Time `json:"occurred_at"`
}

// gatewayStartResult 是 gateway start-results 端点返回的命令列子集。
type gatewayStartResult struct {
	CommandID     string     `json:"command_id"`
	StopCommandID string     `json:"stop_command_id"`
	ChargeOrderID uint64     `json:"charge_order_id"`
	OrderNo       string     `json:"order_no"`
	DeviceID      string     `json:"device_id"`
	PortNo        uint8      `json:"port_no"`
	PortID        *int64     `json:"port_id"`
	Status        string     `json:"status"`
	ResultCode    *int16     `json:"result_code"`
	AckAt         *time.Time `json:"ack_at"`
}

// SyncBatch 可以安全重放：
// central 按命令/订单只存一份结果，
// 而 gateway 只有在 central 确认落库之后
// 才把结果标记为已上报。
func (s Synchronizer) SyncBatch(ctx context.Context) (int, error) {
	if s.GatewayURL == "" || s.ServiceToken == "" {
		return 0, errors.New("start result synchronizer is not configured")
	}
	base, err := url.Parse(s.CentralURL)
	if err != nil || base.Host == "" || base.User != nil || base.Scheme != "http" && base.Scheme != "https" {
		return 0, errors.New("invalid central URL")
	}
	gateway := newGatewaySyncAPI(s.Gateway, s.GatewayURL, s.ServiceToken)
	path := "/api/v1/internal/start-results"
	if s.CommandFilter != "" {
		path += "?command_id=" + url.QueryEscape(s.CommandFilter)
	}
	var reply struct {
		Items []gatewayStartResult `json:"items"`
	}
	if err := gateway.get(ctx, path, &reply); err != nil {
		return 0, err
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	count := 0
	var firstError error
	reported := make([]string, 0, len(reply.Items))
	for _, row := range reply.Items {
		if row.PortID == nil || *row.PortID <= 0 || row.ResultCode == nil || row.AckAt == nil || *row.ResultCode < 0 || *row.ResultCode > 255 {
			firstError = errors.New("invalid persisted start result")
			continue
		}
		result := startResult{CommandID: row.CommandID, ChargeOrderID: row.ChargeOrderID,
			OrderNo: row.OrderNo, DeviceID: row.DeviceID, PortNo: row.PortNo,
			PortID: uint64(*row.PortID), Success: row.Status == "acked",
			ResultCode: uint8(*row.ResultCode), OccurredAt: row.AckAt.UTC()}
		if result.Success != (result.ResultCode == 0) {
			firstError = errors.New("inconsistent persisted start result")
			continue
		}
		if result.ResultCode > 3 {
			// A compensation STOP (254) proves the output is now off, not that
			// START never happened. Keep the payment pending confirmation.
			firstError = errors.New("startup outcome needs review; compensation is not a no-charge proof")
			continue
		}
		if err := postStartResult(ctx, client, *base, s.ServiceToken, result); err != nil {
			if errors.Is(err, ErrCentralConflict) && result.Success {
				if compensateErr := s.compensate(ctx, client, result); compensateErr != nil && firstError == nil {
					firstError = compensateErr
				}
				continue
			}
			if firstError == nil {
				firstError = err
			}
			continue
		}
		reported = append(reported, row.CommandID)
		count++
	}
	if len(reported) > 0 {
		var mark struct {
			Marked int `json:"marked"`
		}
		if err := gateway.post(ctx, "/api/v1/internal/start-results/mark-reported", map[string]any{"command_ids": reported}, &mark); err != nil {
			return count, err
		}
	}
	return count, firstError
}

func postStartResult(ctx context.Context, client *http.Client, base url.URL, token string, result startResult) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/internal/charge-orders/" + url.PathEscape(result.OrderNo) + "/start-result"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(payload))
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
		if response.StatusCode == http.StatusConflict {
			return ErrCentralConflict
		}
		return fmt.Errorf("central start result: HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Code int `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Code != 0 {
		return fmt.Errorf("central start result: code %d", envelope.Code)
	}
	return nil
}

func (s Synchronizer) compensate(ctx context.Context, client *http.Client, result startResult) error {
	base, err := url.Parse(s.GatewayURL)
	if err != nil || base.Host == "" || base.User != nil || base.Scheme != "http" && base.Scheme != "https" {
		return errors.New("invalid gateway URL")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/internal/charge-orders/" + url.PathEscape(result.OrderNo) + "/compensate"
	data, err := json.Marshal(map[string]string{"command_id": result.CommandID})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Service-Token", s.ServiceToken)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway compensation: HTTP %d", response.StatusCode)
	}
	return nil
}
