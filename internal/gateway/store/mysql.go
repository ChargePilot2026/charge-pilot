package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDeviceNotProvisioned = errors.New("device is not provisioned for this protocol")

type MySQLSink struct{ DB *gorm.DB }

func (s MySQLSink) Register(ctx context.Context, registration protocol.Registration) error {
	if s.DB == nil {
		return errors.New("gateway database is unavailable")
	}
	var device deviceRow
	// 厂商是按适配器类别匹配的，不是按它的业务编码。
	// vendor_code 是给运维看的标识（形如 "V-DC589"），说明不了
	// 设备讲的是什么协议；adapter_class 才是标明方言的那个字段，
	// 开通接口在创建设备之前检查的也正是它。
	// 改用 vendor_code 去匹配协议名，结果是任何已开通的设备
	// 都注册不上，而这个故障一直没人发现，
	// 因为监听器把由此产生的错误丢掉了。
	err := s.DB.WithContext(ctx).Table("device AS d").Select("d.id").
		Joins("JOIN vendor AS v ON v.id = d.vendor_id").
		Where("d.device_id = ? AND d.status = 'enabled' AND d.deleted_at IS NULL AND v.adapter_class = ? AND v.status = 'enabled' AND v.deleted_at IS NULL", registration.DeviceID, registration.Protocol).
		Take(&device).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrDeviceNotProvisioned
	}
	if err != nil {
		return fmt.Errorf("verify device: %w", err)
	}
	now := registration.ReceivedAt.UTC()
	result := s.DB.WithContext(ctx).Model(&deviceRow{}).Where("id = ?", device.ID).Updates(map[string]any{
		"firmware_version": registration.SoftwareVersion,
		"registered_at":    gorm.Expr("COALESCE(registered_at, ?)", now),
		"last_seen_at":     now,
	})
	if result.Error != nil {
		return fmt.Errorf("record device registration: %w", result.Error)
	}
	return nil
}

func (s MySQLSink) Record(ctx context.Context, event protocol.Event) error {
	if s.DB == nil {
		return errors.New("gateway database is unavailable")
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal device event: %w", err)
	}
	key := eventKey(event)
	// outbox 里装的是全平台统一的那种信封——event_id、event_type、
	// source、occurred_at 加一个 data 载荷——因为这就是每个消费方
	// 都会解析、平台其余部分也都在发布的那个结构。
	// 如果直接发裸事件，下游什么都读不了：事件自己的字段名是
	// "Type" 而非 "event_type"，消费方按类型去查自然一无所获，
	// 这条记录随后就被丢弃了。
	//
	// device_event.event_json 则原样保留事件本身，
	// 因为那一列是重放的凭据；重新编码一遍，
	// 意味着你重放的东西已经不再是板子当初发来的东西。
	envelope, err := json.Marshal(map[string]any{
		"event_id":    key,
		"event_type":  string(event.Type),
		"source":      "gateway",
		"occurred_at": event.ReceivedAt.UTC(),
		"data":        json.RawMessage(data),
	})
	if err != nil {
		return fmt.Errorf("marshal device event envelope: %w", err)
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&deviceEventRow{
			EventKey: key, Protocol: event.Protocol, DeviceID: event.DeviceID,
			EventType: string(event.Type), PortNo: event.Port, EventJSON: data, ReceivedAt: event.ReceivedAt.UTC(),
		})
		if inserted.Error != nil {
			return fmt.Errorf("persist device event: %w", inserted.Error)
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&deviceOutboxRow{EventID: key, Stream: "device_event_stream", EnvelopeJSON: envelope}).Error; err != nil {
			return fmt.Errorf("queue device event: %w", err)
		}
		if inserted.RowsAffected == 1 && event.Type == protocol.StartResult {
			if err := applyStartAck(ctx, tx, event); err != nil {
				return fmt.Errorf("apply start acknowledgement: %w", err)
			}
		}
		if inserted.RowsAffected == 1 && event.Type == protocol.StopResult {
			if err := applyStopAck(ctx, tx, event); err != nil {
				return fmt.Errorf("apply stop acknowledgement: %w", err)
			}
			if err := applyUserStopAck(ctx, tx, event); err != nil {
				return fmt.Errorf("apply user stop acknowledgement: %w", err)
			}
		}
		if event.Type == protocol.Heartbeat {
			if err := tx.Model(&deviceRow{}).Where("device_id = ? AND status = 'enabled' AND deleted_at IS NULL", event.DeviceID).
				Update("last_seen_at", event.ReceivedAt.UTC()).Error; err != nil {
				return fmt.Errorf("touch device heartbeat: %w", err)
			}
		}
		if inserted.RowsAffected == 1 {
			if err := insertMeasurements(ctx, tx, event); err != nil {
				return fmt.Errorf("persist device telemetry: %w", err)
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

// stopAckAccepted 判断停止回执是否意味着充电已经结束。
//
// 0x10 是干净停止，0x01 表示端口本来就是空闲的，两者到达的终态相同。
// 0x00（无此端口）和 0x04（端口故障）则不然：
// 平台要求结束一次充电却被告知没结束，
// 这必须记录下来，而不是随手丢掉。
func stopAckAccepted(code uint8) bool { return code == 0x10 || code == 0x01 }

func applyUserStopAck(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	var command chargeStopCommandRow
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND port_no = ? AND session_id = ?", event.DeviceID, event.Port, hex.EncodeToString(event.SessionID[:])).Take(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if stopAckAccepted(event.ResultCode) {
		if command.Status == "sent" {
			return tx.Model(&chargeStopCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
				Updates(map[string]any{"status": "acked", "result_code": int16(event.ResultCode)}).Error
		}
		return nil
	}
	// 设备拒绝了。没有这段处理，这一行会永远停在 "sent"，
	// 没人能区分"充电还在跑"和"压根没被要求停止过"。
	if command.Status == "sent" {
		return tx.Model(&chargeStopCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
			Updates(map[string]any{
				"status":      "rejected",
				"result_code": int16(event.ResultCode),
				"rejected_at": event.ReceivedAt.UTC(),
			}).Error
	}
	return nil
}

func applyStartAck(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	var command chargeCommandRow
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND port_no = ? AND session_id = ?", event.DeviceID, event.Port, hex.EncodeToString(event.SessionID[:])).Take(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 保留配不上的 ACK 供事后排查；它们无法改变任何订单。
		return nil
	}
	if err != nil {
		return err
	}
	if !command.PortID.Valid || command.PortID.Int64 <= 0 {
		return ErrOrderConflict
	}
	if event.ResultCode == 0 && command.Status == "sent" {
		changed := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
			Updates(map[string]any{"status": "acked", "error": nil, "result_code": 0, "ack_at": event.ReceivedAt.UTC()})
		if err := requireOne(changed); err != nil {
			return ErrOrderConflict
		}
		port := tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status = 'idle'", command.PortID.Int64, command.OrderNo).Update("status", "charging")
		if err := requireOne(port); err != nil {
			return ErrPortUnavailable
		}
	}
	if event.ResultCode != 0 && (command.Status == "sent" || command.Status == "stopping") {
		updated := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status IN ('sent','stopping')", command.CommandID).
			Updates(map[string]any{"status": "rejected", "error": fmt.Sprintf("device rejected START code %d", event.ResultCode), "result_code": event.ResultCode, "ack_at": event.ReceivedAt.UTC()})
		if updated.Error != nil {
			return updated.Error
		}
		return tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status = 'idle'", command.PortID.Int64, command.OrderNo).
			Update("current_order_id", nil).Error
	}
	return nil
}

func applyStopAck(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	var command chargeCommandRow
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND port_no = ? AND stop_session_id = ? AND status = 'stopping'", event.DeviceID, event.Port, hex.EncodeToString(event.SessionID[:])).Take(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// 0x10 表示输出已停；0x01 表示端口本来就是空闲的。设备拒绝时
	// 命令会留在 "stopping"，因为端口确实还在充电，
	// 假装已经停了等于把一个正在用的端口放出去。
	if !stopAckAccepted(event.ResultCode) {
		return nil
	}
	if !command.PortID.Valid || command.PortID.Int64 <= 0 {
		return ErrOrderConflict
	}
	changed := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'stopping'", command.CommandID).
		Updates(map[string]any{"status": "rejected", "result_code": 254, "ack_at": event.ReceivedAt.UTC(), "error": "STOP compensation confirmed"})
	if err := requireOne(changed); err != nil {
		return ErrOrderConflict
	}
	port := tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status IN ('idle','charging')", command.PortID.Int64, command.OrderNo).
		Updates(map[string]any{"status": "idle", "current_order_id": nil})
	if err := requireOne(port); err != nil {
		return ErrPortUnavailable
	}
	return nil
}

func insertMeasurements(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	// 边写边收集，这样汇总表能按每种粒度一条语句写完，
	// 而不是一个指标一条。
	rollup := make([]AggregateSample, 0, 8)
	insert := func(port sql.NullInt16, metric, value string) error {
		rollup = append(rollup, AggregateSample{DeviceID: event.DeviceID, Port: port, Metric: metric,
			Value: value, TS: event.ReceivedAt.UTC()})
		return tx.WithContext(ctx).Create(&telemetryRow{DeviceID: event.DeviceID, PortNo: port, Metric: metric,
			ValueNum: value, TS: event.ReceivedAt.UTC()}).Error
	}
	if event.Type == protocol.Heartbeat {
		if err := insert(sql.NullInt16{}, "signal", decimal.NewFromInt(int64(event.Signal)).String()); err != nil {
			return err
		}
		if event.PortStates != nil {
			if err := insert(sql.NullInt16{}, "voltage_v", decimal.NewFromInt(int64(event.VoltageV)).String()); err != nil {
				return err
			}
			if err := insert(sql.NullInt16{}, "temperature_c", decimal.NewFromInt(int64(event.TemperatureC)).String()); err != nil {
				return err
			}
		}
	}
	for _, port := range event.ChargingPorts {
		id := sql.NullInt16{Int16: int16(port.Port), Valid: true}
		if port.ChargedMWh != 0 {
			if err := insert(id, "meter_kwh", decimal.NewFromInt(int64(port.ChargedMWh)).Shift(-6).StringFixed(6)); err != nil {
				return err
			}
		}
		if err := insert(id, "power_w", decimal.NewFromInt(int64(port.PowerDeciWatts)).Shift(-1).StringFixed(1)); err != nil {
			return err
		}
	}
	if event.Type == protocol.ChargeEnd {
		id := sql.NullInt16{Int16: int16(event.Port), Valid: true}
		if err := insert(id, "meter_kwh", decimal.NewFromInt(int64(event.EnergyMilliKWh)).Shift(-3).StringFixed(3)); err != nil {
			return err
		}
	}
	return refreshAggregates(ctx, tx, rollup)
}

func requireOne(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func eventKey(event protocol.Event) string {
	if event.Type == protocol.Heartbeat || event.Type == protocol.Telemetry {
		return uuid.NewString()
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(event.Protocol))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(event.DeviceID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(event.Type))
	_, _ = hash.Write(event.SessionID[:])
	_, _ = hash.Write(event.RawPayload)
	return hex.EncodeToString(hash.Sum(nil))
}
