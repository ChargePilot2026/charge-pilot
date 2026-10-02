package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol/dc589"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DeviceAlertSync 把 gateway 未处理的 dc589 故障事件转换为 alert_event 告警，
// 并依据故障之后的最新心跳自动恢复已消除的告警。
// alert_event 是 central 自己的表，直接事务写入；device_event 的扫描、
// 处理标记与心跳证据一律经 gateway 内部 HTTP，不直读 gateway 库。
// 故障码翻译（FaultMetric/FaultRecovered）是 dc589 协议语义，见 protocol/dc589。
type DeviceAlertSync struct {
	AdminDB *gorm.DB
	// Gateway 调用 gateway 内部端点取故障事件、推进处理标记、取心跳证据。
	Gateway      serviceclient.Client
	GatewayURL   string
	ServiceToken string
}

type deviceFaultReport struct {
	ID         uint64    `json:"id"`
	EventKey   string    `json:"event_key"`
	ReceivedAt time.Time `json:"received_at"`
	Payload    string    `json:"payload"`
}

type deviceAlert struct {
	ID           uint64
	DeviceID     string
	Metric       string
	EventID      string
	Status       string
	CreatedAt    time.Time
	CreatedMonth time.Time
	ResolvedAt   *time.Time
}

// Run 拉取一批未处理故障，逐条幂等写入告警后批量推进处理标记，
// 最后扫描活跃告警做恢复判定。单条失败不阻塞其余事件。
func (s DeviceAlertSync) Run(ctx context.Context) (int, error) {
	if s.AdminDB == nil || s.GatewayURL == "" || s.ServiceToken == "" {
		return 0, errors.New("device alert sync is not configured")
	}
	var response struct {
		Code int
		Data struct {
			Reports []deviceFaultReport
		}
	}
	if err := s.Gateway.GetJSON(ctx, s.GatewayURL, s.ServiceToken, "/api/v1/internal/device-faults?limit=100", &response); err != nil {
		return 0, err
	}
	if response.Code != 0 {
		return 0, errors.New("device faults unavailable")
	}
	raised := 0
	var firstError error
	processed := []uint64{}
	for _, report := range response.Data.Reports {
		var event protocol.Event
		if err := json.Unmarshal([]byte(report.Payload), &event); err != nil {
			if firstError == nil {
				firstError = fmt.Errorf("decode device fault %d: %w", report.ID, err)
			}
			continue
		}
		changed, err := s.recordFault(ctx, report.EventKey, event)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		processed = append(processed, report.ID)
		if changed {
			raised++
		}
	}
	if len(processed) > 0 {
		var marked struct {
			Code int
			Data struct {
				Marked int64
			}
		}
		if err := s.Gateway.Post(ctx, s.GatewayURL, s.ServiceToken, "/api/v1/internal/device-faults/mark-processed", map[string]any{"ids": processed}, &marked); err != nil {
			return raised, err
		}
		if marked.Code != 0 || marked.Data.Marked < int64(len(processed)) {
			// 有事件未能推进处理标记，留待下一轮重扫；已写告警幂等，不会重复。
			if firstError == nil {
				firstError = fmt.Errorf("marked %d of %d fault reports", marked.Data.Marked, len(processed))
			}
		}
	}
	return raised, errors.Join(firstError, s.resolveRecovered(ctx))
}

// recordFault 以设备+指标维度去重写入告警：同一维度只保留一条活跃告警，
// 持续故障更新其事件键作为恢复判定起点；重复或更早的事件为空操作。
func (s DeviceAlertSync) recordFault(ctx context.Context, key string, event protocol.Event) (bool, error) {
	if event.DeviceID == "" || event.ReceivedAt.IsZero() || event.Type != protocol.Fault {
		return false, errors.New("invalid device fault event")
	}
	if event.FaultCode == 0 {
		return false, nil // 端口恢复以随后心跳中的实际状态确认。
	}
	metric, severity, note := dc589.FaultMetric(event)
	raised := false
	err := s.AdminDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing deviceAlert
		found := tx.Table("alert_event").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("device_id = ? AND metric = ? AND rule_id IS NULL", event.DeviceID, metric).
			Order("created_at DESC, id DESC").Take(&existing)
		if found.Error != nil && !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		if found.RowsAffected > 0 {
			if existing.EventID == key || event.ReceivedAt.Before(existing.CreatedAt) || existing.ResolvedAt != nil && !event.ReceivedAt.After(*existing.ResolvedAt) {
				return nil
			}
			if existing.Status == "active" || existing.Status == "acknowledged" {
				// 保存最近一次报告作为恢复判定的起点；持续故障只保留一条告警。
				return tx.Table("alert_event").Where("id = ? AND created_month = ?", existing.ID, existing.CreatedMonth).
					Updates(map[string]any{"event_id": key, "value": event.FaultCode}).Error
			}
		}
		at := event.ReceivedAt.UTC()
		month := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
		if err := tx.Table("alert_event").Create(map[string]any{
			"device_id": event.DeviceID, "metric": metric, "severity": severity, "value": event.FaultCode,
			"status": "active", "note": note, "event_id": key, "created_at": at, "created_month": month,
		}).Error; err != nil {
			return err
		}
		raised = true
		return nil
	})
	return raised, err
}

// resolveRecovered 对活跃告警取故障之后的最新心跳，
// 依 dc589 恢复语义把已消除的告警标记为 auto_resolved。
// 没有更新心跳即无恢复证据，保持原状态。
func (s DeviceAlertSync) resolveRecovered(ctx context.Context) error {
	var active []deviceAlert
	if err := s.AdminDB.WithContext(ctx).Table("alert_event").
		Where("rule_id IS NULL AND status IN ('active','acknowledged') AND (metric IN ('smoke','high_temperature','device_fault') OR metric LIKE 'port_fault_%')").
		Find(&active).Error; err != nil {
		return err
	}
	for _, alert := range active {
		var heartbeat struct {
			Code int
			Data struct {
				Found      bool      `json:"found"`
				ReceivedAt time.Time `json:"received_at"`
				Payload    string    `json:"payload"`
			}
		}
		// 以最近一次的故障事件键为恢复判定水印：早于该故障的心跳不构成恢复证据。
		path := fmt.Sprintf("/api/v1/internal/devices/%s/latest-heartbeat?after_key=%s", url.PathEscape(alert.DeviceID), url.QueryEscape(alert.EventID))
		if err := s.Gateway.GetJSON(ctx, s.GatewayURL, s.ServiceToken, path, &heartbeat); err != nil {
			return err
		}
		if heartbeat.Code != 0 || !heartbeat.Data.Found {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(heartbeat.Data.Payload), &event); err != nil {
			return err
		}
		if !dc589.FaultRecovered(alert.Metric, event) {
			continue
		}
		if err := s.AdminDB.WithContext(ctx).Table("alert_event").
			Where("id = ? AND created_month = ? AND event_id = ? AND status IN ('active','acknowledged')", alert.ID, alert.CreatedMonth, alert.EventID).
			Updates(map[string]any{"status": "auto_resolved", "resolved_at": heartbeat.Data.ReceivedAt}).Error; err != nil {
			return err
		}
	}
	return nil
}
