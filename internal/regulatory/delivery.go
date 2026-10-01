package regulatory

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/netguard"
	"github.com/google/uuid"
)

type Sender interface {
	Mode() string
	Send(context.Context, Event) error
}

type SimulationSender struct{}

func (SimulationSender) Mode() string                      { return "simulation" }
func (SimulationSender) Send(context.Context, Event) error { return nil }

// HTTPSender 是可替换的基线适配器。
// 真实监管方的字段映射、签名方案和国密算法
// 必须依据其对接规范另行提供。
type HTTPSender struct {
	Endpoint string
	Secret   string
	Client   *http.Client
}

func (HTTPSender) Mode() string { return "http" }

func (s HTTPSender) Send(ctx context.Context, event Event) error {
	if s.Secret == "" {
		return errors.New("regulatory signing secret missing")
	}
	if err := netguard.ValidatePublicHTTPS(s.Endpoint); err != nil {
		return err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(s.Secret))
	_, _ = mac.Write([]byte(ts + "."))
	_, _ = mac.Write(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-ChargePilot-Event-Id", event.EventID)
	request.Header.Set("X-ChargePilot-Timestamp", ts)
	request.Header.Set("X-ChargePilot-Signature", hex.EncodeToString(mac.Sum(nil)))
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("regulatory receiver HTTP %d", response.StatusCode)
	}
	return nil
}

type Deliverer struct {
	DB     *sql.DB
	Sender Sender
}

type queuedReport struct {
	ID         uint64
	Event      Event
	Attempts   uint32
	LeaseToken string
}

// RunBatch 以租约领取持久化事件；失败事件保留，按有界指数退避重试。
func (d Deliverer) RunBatch(ctx context.Context) (int, error) {
	if d.DB == nil || d.Sender == nil {
		return 0, errors.New("regulatory deliverer not configured")
	}
	now := time.Now().UTC()
	rows, err := d.DB.QueryContext(ctx, `SELECT id,event_id,object_type,object_key,CAST(payload_json AS CHAR),attempts
		FROM regulatory_report WHERE status IN ('queued','processing') AND next_attempt_at <= ?
		AND (lease_until IS NULL OR lease_until < ?) ORDER BY id LIMIT 20`, now, now)
	if err != nil {
		return 0, err
	}
	var pending []queuedReport
	for rows.Next() {
		var report queuedReport
		var payload string
		if err := rows.Scan(&report.ID, &report.Event.EventID, &report.Event.ObjectType, &report.Event.ObjectKey, &payload, &report.Attempts); err != nil {
			rows.Close()
			return 0, err
		}
		report.Event.Data = json.RawMessage(payload)
		pending = append(pending, report)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	var delivered int
	var failures []error
	for _, report := range pending {
		report.LeaseToken = uuid.NewString()
		claim, err := d.DB.ExecContext(ctx, `UPDATE regulatory_report SET status='processing',lease_token=?,lease_until=?
			WHERE id=? AND status IN ('queued','processing') AND (lease_until IS NULL OR lease_until < ?)`,
			report.LeaseToken, time.Now().UTC().Add(2*time.Minute), report.ID, time.Now().UTC())
		if err != nil {
			failures = append(failures, err)
			continue
		}
		affected, _ := claim.RowsAffected()
		if affected == 0 {
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		sendErr := d.Sender.Send(sendCtx, report.Event)
		cancel()
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if sendErr == nil {
			result, finishErr := d.DB.ExecContext(finishCtx, `UPDATE regulatory_report SET status='delivered',attempts=attempts+1,
				delivered_at=?,delivered_mode=?,last_error=NULL,lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=?`,
				time.Now().UTC(), d.Sender.Mode(), report.ID, report.LeaseToken)
			if finishErr == nil {
				count, _ := result.RowsAffected()
				if count == 1 {
					delivered++
				} else {
					finishErr = errors.New("regulatory report lease lost")
				}
			}
			if finishErr != nil {
				failures = append(failures, finishErr)
			}
		} else {
			message := sendErr.Error()
			if len(message) > 512 {
				message = message[:512]
			}
			_, finishErr := d.DB.ExecContext(finishCtx, `UPDATE regulatory_report SET status='queued',attempts=attempts+1,
				next_attempt_at=?,last_error=?,lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=?`,
				time.Now().UTC().Add(retryDelay(report.Attempts+1)), message, report.ID, report.LeaseToken)
			failures = append(failures, errors.Join(sendErr, finishErr))
		}
		finishCancel()
	}
	return delivered, errors.Join(failures...)
}

func retryDelay(attempt uint32) time.Duration {
	if attempt > 11 {
		attempt = 11
	}
	return time.Duration(1<<attempt) * time.Second
}
