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
	"strings"
	"time"

	chargejobdb "github.com/ChargePilot2026/charge-pilot/internal/worker/charge/generated"
)

type Synchronizer struct {
	GatewayDB     *sql.DB
	CentralURL    string
	GatewayURL    string
	CommandFilter string
	ServiceToken  string
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

// SyncBatch is replay-safe: central stores one result per command/order, and
// gateway marks the result reported only after central confirms persistence.
func (s Synchronizer) SyncBatch(ctx context.Context) (int, error) {
	if s.GatewayDB == nil || s.ServiceToken == "" {
		return 0, errors.New("start result synchronizer is not configured")
	}
	base, err := url.Parse(s.CentralURL)
	if err != nil || base.Host == "" || base.User != nil || base.Scheme != "http" && base.Scheme != "https" {
		return 0, errors.New("invalid central URL")
	}
	queries := chargejobdb.New(s.GatewayDB)
	rows, err := queries.UnreportedStartResults(ctx, chargejobdb.UnreportedStartResultsParams{CommandFilter: s.CommandFilter})
	if err != nil {
		return 0, err
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	count := 0
	var firstError error
	for _, row := range rows {
		if !row.PortID.Valid || row.PortID.Int64 <= 0 || !row.ResultCode.Valid || !row.AckAt.Valid || row.ResultCode.Int16 < 0 || row.ResultCode.Int16 > 255 {
			firstError = errors.New("invalid persisted start result")
			continue
		}
		result := startResult{CommandID: row.CommandID, ChargeOrderID: row.ChargeOrderID,
			OrderNo: row.OrderNo, DeviceID: row.DeviceID, PortNo: row.PortNo,
			PortID: uint64(row.PortID.Int64), Success: row.Status == chargejobdb.ChargeCommandStatusAcked,
			ResultCode: uint8(row.ResultCode.Int16), OccurredAt: row.AckAt.Time.UTC()}
		if result.Success != (result.ResultCode == 0) {
			firstError = errors.New("inconsistent persisted start result")
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
		if err := queries.MarkStartResultReported(ctx, row.CommandID); err != nil {
			return count, err
		}
		count++
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
