package store

import (
	"context"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// recordHeartbeatState keeps physical reports separate from port reservations.
// Missing extended blocks cannot clear known port states; zero is a valid report.
func recordHeartbeatState(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	at := event.ReceivedAt.UTC().Truncate(time.Millisecond)
	if err := tx.WithContext(ctx).Model(&deviceRow{}).
		Where("device_id = ? AND deleted_at IS NULL AND (signal_at IS NULL OR signal_at < ?)", event.DeviceID, at).
		Updates(map[string]any{"signal_strength": event.Signal, "signal_at": at}).Error; err != nil {
		return err
	}
	if len(event.PortStates) == 0 {
		return nil
	}
	var state strings.Builder
	state.WriteString("CASE port_no")
	args := make([]any, 0, len(event.PortStates)*2)
	ports := make([]int, 0, len(event.PortStates))
	for i, status := range event.PortStates {
		port := uint8(i + 1)
		ports = append(ports, int(port))
		state.WriteString(" WHEN ? THEN ?")
		args = append(args, port, status)
	}
	state.WriteString(" END")
	return tx.WithContext(ctx).Model(&devicePortRow{}).
		Where("device_id = ? AND port_no IN ? AND deleted_at IS NULL AND (reported_status_at IS NULL OR reported_status_at < ?)", event.DeviceID, ports, at).
		Updates(map[string]any{"reported_status": gorm.Expr(state.String(), args...), "reported_status_at": at}).Error
}

type chargeProcessRow struct {
	ID               uint64 `gorm:"primaryKey"`
	EventKey         string
	ChargeOrderID    uint64
	OrderNo          string
	DeviceID         string
	PortNo           uint8
	TS               time.Time
	PowerDeciwatts   uint32
	ChargedSeconds   uint32
	RemainingSeconds uint32
	ChargedMWh       uint32 `gorm:"column:charged_mwh"`
	RemainingMWh     uint32 `gorm:"column:remaining_mwh"`
	SignalStrength   uint8
	PortStatus       *uint8
	VoltageV         *uint16
	TemperatureC     *int16
	DeviceStatus     *uint8
}

func (chargeProcessRow) TableName() string { return "charge_process" }

// A4 不携带订单标识，只能关联当前端口已确认的命令。
// 以命令确认时间为下界，防止历史上报被归入后来占用端口的新订单。
func insertChargeProcess(ctx context.Context, tx *gorm.DB, key string, event protocol.Event) error {
	if event.Protocol != "dc589" || len(event.ChargingPorts) == 0 {
		return nil
	}
	ports := make([]int, 0, len(event.ChargingPorts))
	for _, port := range event.ChargingPorts {
		ports = append(ports, int(port.Port))
	}
	at := event.ReceivedAt.UTC().Truncate(time.Millisecond)
	// 保存过程记录前锁定端口，保持订单归属不变。
	// 启动确认按命令、端口顺序加锁；此处仅锁端口，避免反向锁定命令形成死锁。
	var locked []devicePortRow
	if err := tx.WithContext(ctx).Select("id").
		Where("device_id=? AND port_no IN ? AND deleted_at IS NULL", event.DeviceID, ports).
		Order("port_no").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&locked).Error; err != nil {
		return err
	}
	var sessions []struct {
		ChargeOrderID uint64
		OrderNo       string
		PortNo        uint8
	}
	if err := tx.WithContext(ctx).Table("device_port AS p").
		Select("c.charge_order_id,c.order_no,p.port_no").
		Joins("JOIN charge_command AS c ON c.order_no=p.current_order_id AND c.device_id=p.device_id AND c.port_no=p.port_no AND c.port_id=p.id").
		Where("p.device_id=? AND p.port_no IN ? AND p.deleted_at IS NULL AND p.status='charging' AND c.status='acked' AND c.ack_at<=?", event.DeviceID, ports, at).
		Scan(&sessions).Error; err != nil {
		return err
	}
	byPort := make(map[uint8]int, len(sessions))
	for i, session := range sessions {
		byPort[session.PortNo] = i
	}
	rows := make([]chargeProcessRow, 0, len(sessions))
	for _, port := range event.ChargingPorts {
		i, ok := byPort[port.Port]
		if !ok {
			continue
		}
		session := sessions[i]
		row := chargeProcessRow{EventKey: key, ChargeOrderID: session.ChargeOrderID, OrderNo: session.OrderNo,
			DeviceID: event.DeviceID, PortNo: port.Port, TS: at, PowerDeciwatts: port.PowerDeciWatts,
			ChargedSeconds: port.ChargedSeconds, RemainingSeconds: port.RemainingSecs,
			ChargedMWh: port.ChargedMWh, RemainingMWh: port.RemainingMWh, SignalStrength: event.Signal}
		if event.PortStates != nil {
			row.VoltageV, row.TemperatureC, row.DeviceStatus = &event.VoltageV, &event.TemperatureC, &event.DeviceStatus
			if port.Port > 0 && int(port.Port) <= len(event.PortStates) {
				row.PortStatus = &event.PortStates[port.Port-1]
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}
