package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DeviceSynchronizer 将设备主动上报的故障直接转成告警，不依赖遥测阈值规则。
// processed_at 只在管理库提交成功后推进，保留故障报告供失败重试与历史补录。
type DeviceSynchronizer struct {
	GatewayDB *gorm.DB
	AdminDB   *gorm.DB
}

type deviceReport struct {
	ID          uint64
	EventKey    string
	EventJSON   []byte
	ReceivedAt  time.Time
	ProcessedAt *time.Time
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

func (s DeviceSynchronizer) Run(ctx context.Context) (int, error) {
	if s.GatewayDB == nil || s.AdminDB == nil {
		return 0, errors.New("device alert synchronizer is not configured")
	}
	var reports []deviceReport
	if err := s.GatewayDB.WithContext(ctx).Table("device_event").
		Where("event_type = 'fault' AND protocol_name = 'dc589' AND processed_at IS NULL").
		Order("id").Limit(100).Find(&reports).Error; err != nil {
		return 0, err
	}
	raised := 0
	var firstError error
	for _, report := range reports {
		changed := false
		err := s.GatewayDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var current deviceReport
			if err := tx.Table("device_event").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", report.ID).Take(&current).Error; err != nil {
				return err
			}
			if current.ProcessedAt != nil {
				return nil
			}
			var event protocol.Event
			if err := json.Unmarshal(current.EventJSON, &event); err != nil {
				return fmt.Errorf("decode device fault %d: %w", report.ID, err)
			}
			var err error
			changed, err = s.recordFault(ctx, current.EventKey, event)
			if err != nil {
				return err
			}
			return tx.Table("device_event").Where("id = ?", report.ID).Update("processed_at", gorm.Expr("UTC_TIMESTAMP(3)")).Error
		})
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		if changed {
			raised++
		}
	}
	return raised, errors.Join(firstError, s.resolveRecovered(ctx))
}

func faultMetric(event protocol.Event) (metric, severity, note string) {
	if event.Port == 0xff {
		switch event.FaultCode {
		case 0xbb:
			return "smoke", "fatal", "设备上报烟雾告警（0xBB）"
		case 0xaa:
			return "high_temperature", "critical", "设备上报高温告警（0xAA）"
		}
		return "device_fault", "critical", fmt.Sprintf("设备上报故障（0x%02X）", event.FaultCode)
	}
	return fmt.Sprintf("port_fault_%d", event.Port), "critical", fmt.Sprintf("端口 %d 上报故障（0x%02X）", event.Port, event.FaultCode)
}

func (s DeviceSynchronizer) recordFault(ctx context.Context, key string, event protocol.Event) (bool, error) {
	if event.DeviceID == "" || event.ReceivedAt.IsZero() || event.Type != protocol.Fault {
		return false, errors.New("invalid device fault event")
	}
	if event.FaultCode == 0 {
		return false, nil // 端口恢复以随后心跳中的实际状态确认。
	}
	metric, severity, note := faultMetric(event)
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

func (s DeviceSynchronizer) resolveRecovered(ctx context.Context) error {
	var active []deviceAlert
	if err := s.AdminDB.WithContext(ctx).Table("alert_event").
		Where("rule_id IS NULL AND status IN ('active','acknowledged') AND (metric IN ('smoke','high_temperature','device_fault') OR metric LIKE 'port_fault_%')").
		Find(&active).Error; err != nil {
		return err
	}
	for _, alert := range active {
		var source deviceReport
		if err := s.GatewayDB.WithContext(ctx).Table("device_event").Where("event_key = ?", alert.EventID).Take(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		var heartbeat deviceReport
		found := s.GatewayDB.WithContext(ctx).Table("device_event").
			Where("device_id = ? AND protocol_name = 'dc589' AND event_type = 'heartbeat' AND received_at > ?", alert.DeviceID, source.ReceivedAt).
			Order("received_at DESC, id DESC").Take(&heartbeat)
		if errors.Is(found.Error, gorm.ErrRecordNotFound) {
			continue
		}
		if found.Error != nil {
			return found.Error
		}
		var event protocol.Event
		if err := json.Unmarshal(heartbeat.EventJSON, &event); err != nil {
			return err
		}
		if !faultRecovered(alert.Metric, event) {
			continue
		}
		if err := s.AdminDB.WithContext(ctx).Table("alert_event").
			Where("id = ? AND created_month = ? AND event_id = ? AND status IN ('active','acknowledged')", alert.ID, alert.CreatedMonth, alert.EventID).
			Updates(map[string]any{"status": "auto_resolved", "resolved_at": heartbeat.ReceivedAt}).Error; err != nil {
			return err
		}
	}
	return nil
}

func faultRecovered(metric string, heartbeat protocol.Event) bool {
	if heartbeat.PortStates == nil {
		return false // 仅含信号强度的心跳不能证明故障已恢复。
	}
	if metric == "smoke" || metric == "high_temperature" || metric == "device_fault" {
		return heartbeat.DeviceStatus == 0
	}
	port, err := strconv.Atoi(strings.TrimPrefix(metric, "port_fault_"))
	if err != nil || port < 1 || port > len(heartbeat.PortStates) {
		return false
	}
	state := heartbeat.PortStates[port-1]
	return state <= 2
}
