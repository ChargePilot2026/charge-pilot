package charge

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

func measuredSegments(start time.Time, end protocol.Event, samples []protocol.Event) []pricing.MeterSegment {
	return pricing.MeasuredSegments(start, end, samples)
}

func (s EndSynchronizer) meterSegments(ctx context.Context, command workerChargeCommandRow, endID uint64, end protocol.Event) ([]pricing.MeterSegment, error) {
	if !command.AckAt.Valid || end.StartedAt.IsZero() {
		return nil, nil
	}
	var rows []workerDeviceEventRow
	// BB 的事件 ID 圈定了这份不可变证据集的范围。
	// 在 BB 之后才落库的延迟心跳
	// 改不了重放时生成的结束结果负载。
	err := s.GatewayDB.WithContext(ctx).Where("device_id=? AND event_type='heartbeat' AND id<=? AND received_at>=? AND received_at<=?", end.DeviceID, endID, command.AckAt.Time, end.ReceivedAt).Order("id").Limit(10081).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > 10080 {
		return nil, nil
	}
	samples := make([]protocol.Event, 0, len(rows))
	for _, row := range rows {
		var event protocol.Event
		if err := json.Unmarshal(row.EventJSON, &event); err != nil {
			return nil, err
		}
		samples = append(samples, event)
	}
	return measuredSegments(command.AckAt.Time, end, samples), nil
}

// 在第一次 HTTP 尝试之前就把整份内部请求冻结。
// 无论是延迟到达的遥测还是保留期清理，
// 都不允许改动一份重放出去的结束回执。
func (s EndSynchronizer) freezeEndResult(ctx context.Context, command workerChargeCommandRow, id uint64, event protocol.Event, candidate endResult) (endResult, error) {
	var row struct{ PayloadJSON []byte }
	query := s.GatewayDB.WithContext(ctx).Table("charge_end_delivery")
	found := query.Where("device_event_id=?", id).Find(&row)
	if found.Error != nil {
		return candidate, found.Error
	}
	if found.RowsAffected == 0 {
		segments, err := s.meterSegments(ctx, command, id, event)
		if err != nil {
			return candidate, err
		}
		candidate.Meter.Segments = segments
		payload, err := json.Marshal(candidate)
		if err != nil {
			return candidate, err
		}
		if err := s.GatewayDB.WithContext(ctx).Table("charge_end_delivery").Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"device_event_id": gorm.Expr("device_event_id")})}).Create(map[string]any{"device_event_id": id, "charge_order_id": candidate.ChargeOrderID, "payload_json": string(payload)}).Error; err != nil {
			return candidate, err
		}
		if err := s.GatewayDB.WithContext(ctx).Table("charge_end_delivery").Where("device_event_id=?", id).Take(&row).Error; err != nil {
			return candidate, err
		}
	}
	var frozen endResult
	if err := json.Unmarshal(row.PayloadJSON, &frozen); err != nil {
		return candidate, err
	}
	base := frozen
	base.Meter.Segments = nil
	candidate.Meter.Segments = nil
	a, _ := json.Marshal(base)
	b, _ := json.Marshal(candidate)
	if string(a) != string(b) {
		return candidate, errors.New("frozen end result identity conflict")
	}
	return frozen, nil
}
