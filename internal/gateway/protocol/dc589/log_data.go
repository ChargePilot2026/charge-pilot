package dc589

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

var commandNames = map[byte]string{
	0xA0: "设备注册", 0xA1: "注册应答", 0xA2: "远程控制", 0xA3: "远程控制结果",
	0xA4: "心跳遥测", 0xA5: "心跳确认", 0xA6: "设置心跳", 0xA7: "心跳设置结果", 0xA8: "请求校时", 0xA9: "服务器校时",
	0xB0: "查询所有端口", 0xB1: "所有端口状态", 0xB2: "查询单端口", 0xB3: "单端口状态", 0xB4: "本地充电上报", 0xB5: "本地充电确认",
	0xB6: "在线刷卡", 0xB7: "启动充电", 0xB8: "启动结果", 0xB9: "停止充电", 0xBA: "停止结果", 0xBB: "充电结束", 0xBC: "结束确认", 0xBD: "在线卡拒绝",
	0xC0: "设备故障", 0xC1: "故障确认", 0xC2: "功率档位切换", 0xC3: "设置参数", 0xC4: "参数设置结果", 0xC5: "读取参数", 0xC6: "参数回读", 0xC7: "请求同步参数",
	0xD0: "查询卡余额", 0xD1: "卡余额应答", 0xE0: "移除功率控制", 0xE1: "移除功率结果",
}

func codeName(code byte, names map[byte]string) string {
	if name, ok := names[code]; ok {
		return name
	}
	return fmt.Sprintf("未知(0x%02X)", code)
}
func modeName(mode byte) string {
	return codeName(mode, map[byte]string{0: "按时长", 1: "按电量", 4: "平台计费", 10: "长时充电", 11: "长电量充电", 12: "长平台计费"})
}
func consumerName(code byte) string {
	return codeName(code, map[byte]string{0: "投币", 1: "离线卡", 2: "扫码", 3: "在线卡", 4: "免费启动"})
}
func portStatesData(states []byte) []map[string]any {
	ports := make([]map[string]any, 0, len(states))
	for i, state := range states {
		ports = append(ports, map[string]any{"port": i + 1, "state_code": state, "state": codeName(state, map[byte]string{0: "空闲", 1: "充电中", 3: "输出故障", 4: "粘连"})})
	}
	return ports
}
func telemetryData(ports []protocol.PortTelemetry) []map[string]any {
	result := make([]map[string]any, 0, len(ports))
	for _, p := range ports {
		result = append(result, map[string]any{"port": p.Port, "power_w": float64(p.PowerDeciWatts) / 10, "charged_seconds": p.ChargedSeconds, "remaining_seconds": p.RemainingSecs, "charged_kwh": float64(p.ChargedMWh) / 1e6, "remaining_kwh": float64(p.RemainingMWh) / 1e6})
	}
	return result
}

// Allowlist decoded fields rather than serializing protocol structs, which can
// contain card identities, balances, SIM identifiers or panel passwords.
func parsedLogData(f Frame) (map[string]any, error) {
	d := f.Data
	u16 := func(at int) uint16 { return binary.LittleEndian.Uint16(d[at : at+2]) }
	switch f.Command {
	case Register:
		p, err := ParseRegistration(f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"board_id": p.BoardID, "hardware": p.HardwareVersion, "software": p.SoftwareID, "version": p.SoftwareVersion, "signal": p.Signal}, nil
	case RegisterReply:
		status, at, err := ParseRegisterReply(f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"status_code": status, "server_time": at.Format(time.RFC3339)}, nil
	case RemoteControl:
		if len(d) != 10 {
			return nil, ErrPayload
		}
		return map[string]any{"action_code": d[0], "action": codeName(d[0], map[byte]string{1: "重启", 2: "主板升级", 3: "模块升级"}), "use_upgrade_id": d[1] != 0}, nil
	case RemoteResult:
		if len(d) != 2 {
			return nil, ErrPayload
		}
		return map[string]any{"action_code": d[0], "result_code": d[1]}, nil
	case Heartbeat:
		p, err := ParseHeartbeat(f)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"board_id": p.BoardID, "signal": p.Signal, "has_port_telemetry": p.HasPortStatus}
		if p.HasPortStatus {
			result["device_status_code"] = p.DeviceStatus
			result["device_status"] = codeName(p.DeviceStatus, map[byte]string{0: "正常", 1: "高温", 2: "烟雾"})
			result["voltage_v"], result["temperature_c"] = p.VoltageV, p.TemperatureC
			result["ports"], result["charging"] = portStatesData(p.PortStates), telemetryData(p.ChargingPorts)
		}
		return result, nil
	case HeartbeatInterval:
		p, err := ParseHeartbeatInterval(f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"interval_seconds": p.Seconds, "port_telemetry_enabled": p.PortStatus}, nil
	case HeartbeatSetReply:
		ok, err := ParseHeartbeatSetReply(f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"accepted": ok, "result_code": d[0]}, nil
	case HeartbeatReply:
		if len(d) != 1 {
			return nil, ErrPayload
		}
		return map[string]any{"ack_code": d[0]}, nil
	case TimeRequest:
		if len(d) != 6 {
			return nil, ErrPayload
		}
		return map[string]any{"request": "服务器时间"}, nil
	case TimeReply:
		at, err := ParseTimeReply(f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"server_time": at.Format(time.RFC3339)}, nil
	case StartCharge:
		p, err := ParseStartCommand(f)
		if err != nil {
			return nil, err
		}
		order, err := decodeBCD(p.OrderBCD[:])
		if err != nil {
			return nil, err
		}
		result := map[string]any{"port": p.Port, "device_order": order, "mode_code": p.Mode, "mode": modeName(byte(p.Mode)), "consumer": consumerName(p.ConsumerType), "quantity_raw": p.Quantity}
		switch p.Mode {
		case ByEnergy, LongEnergy:
			result["target_kwh"] = float64(p.Quantity) / 1000
		case ByTime, PlatformBilling, LongTime, LongPlatformBilling:
			result["duration_minutes"] = p.Quantity
		}
		return result, nil
	case StopCharge:
		port, err := ParseStopCommand(f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"port": port}, nil
	case StartReply, StopReply:
		p, err := ParseCommandResult(f)
		if err != nil {
			return nil, err
		}
		names := map[byte]string{0: "启动成功", 1: "端口故障", 2: "充电或消费类型不匹配", 3: "无此端口"}
		if f.Command == StopReply {
			names = map[byte]string{0: "无此端口", 1: "端口空闲", 4: "端口故障", 0x10: "停止成功"}
		}
		return map[string]any{"port": p.Port, "result_code": p.Code, "result": codeName(p.Code, names)}, nil
	case ChargeEnd:
		p, err := ParseChargeEnd(f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"port": p.Port, "device_order": p.OrderNumber, "started_at": p.StartedAt.Format(time.RFC3339), "ended_at": p.EndedAt.Format(time.RFC3339), "mode": modeName(p.ChargeType), "consumer": consumerName(p.ConsumerType), "charged_seconds": p.ChargedSeconds, "remaining_seconds": p.RemainingSeconds, "charged_kwh": float64(p.ChargedMWh) / 1e6, "power_w": float64(p.PowerDeciWatts) / 10, "band": p.Band, "stop_reason_code": p.StopReason, "stop_reason": codeName(p.StopReason, map[byte]string{0: "时间用完", 1: "移除充电器", 2: "充满自停", 3: "故障", 4: "功率过大", 5: "离线卡退费", 6: "启动时未连接充电器", 7: "远程停止", 8: "烟雾", 9: "高温", 10: "电量用完"})}, nil
	case ChargeEndReply, FaultReply, 0xB5:
		if len(d) != 2 {
			return nil, ErrPayload
		}
		return map[string]any{"result_code": d[0], "port": d[1]}, nil
	case Fault:
		if len(d) != 5 {
			return nil, ErrPayload
		}
		return map[string]any{"port": d[0], "fault_code": d[1], "fault": codeName(d[1], map[byte]string{0xAA: "高温", 0xBB: "烟雾"})}, nil
	case ChargingBand:
		p, err := ParseChargingBand(f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"port": p.Port, "band_before": d[1], "band_after": d[6], "minutes_before": u16(2), "minutes_after": u16(4), "power_w": float64(p.PowerDeciWatts) / 10}, nil
	case OnlineCardSwipe:
		p, err := ParseCardSwipe(f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"port": p.Port, "card": "[redacted]", "local_debit_yuan": float64(p.LocalDebitTenths) / 10}, nil
	case CardBalanceQuery:
		_, err := ParseCardBalanceQuery(f)
		if err != nil {
			return nil, err
		}
		return map[string]any{"card": "[redacted]"}, nil
	case CardBalanceReply, OnlineCardDenied:
		length := 7
		if f.Command == OnlineCardDenied {
			length = 9
		}
		if len(d) != length {
			return nil, ErrPayload
		}
		return map[string]any{"card": "[redacted]", "balance": "[redacted]", "invalid": d[0] != 0}, nil
	case SetConfig, ConfigReport:
		p, err := DecodeConfig(f)
		if err != nil {
			return nil, err
		}
		unit := "分钟"
		if p.RunMode == 3 || p.RunMode == 4 {
			unit = "0.01kWh"
		}
		return map[string]any{"run_mode_code": p.RunMode, "coin_quantity": p.LocalCoinTime, "card_quantity": p.LocalCardTime, "quantity_unit": unit, "card_refund_enabled": p.CardRefund != 0, "tier_upper_w": p.TierWatts, "tier_ratio_percent": p.TierRatioPercent, "stop_when_full": p.StopWhenFull != 0, "float_power_w": float64(p.FloatDeciWatts) / 10, "float_seconds": p.FloatSeconds, "remove_seconds": p.RemoveSeconds, "temperature_guard_c": p.TemperatureGuard, "temperature_guard_enabled": p.TemperatureGuard != 255}, nil
	case ConfigAck:
		if len(d) != 1 {
			return nil, ErrPayload
		}
		result := "参数已接受"
		if err := ParseConfigAck(f); err != nil {
			result = err.Error()
		}
		return map[string]any{"result_code": d[0], "result": result}, nil
	case ReadConfig, 0xC7:
		if len(d) != 1 {
			return nil, ErrPayload
		}
		return map[string]any{"control_code": d[0]}, nil
	case cmdPowerControl, cmdPowerControlReply:
		if len(d) != 6 {
			return nil, ErrPayload
		}
		result := map[string]any{"control_code": d[0], "operation_code": d[1]}
		if f.Command == cmdPowerControlReply && (u16(2) == powerControlFailure || d[0] == 2) {
			result["result"] = "拒绝"
		} else {
			result["remove_power_w"] = float64(u16(2)) / 10
		}
		return result, nil
	case 0xB0, 0xB2:
		if len(d) != 1 {
			return nil, ErrPayload
		}
		return map[string]any{"port_selector": d[0]}, nil
	case 0xB1:
		if len(d) < 5 || d[4] == 0 || d[4] > 64 || len(d) < 5+int(d[4]) {
			return nil, ErrPayload
		}
		// Reuse the A4 decoder: B1 is the same status block without the identity prefix.
		states := int(d[4])
		block := append([]byte(nil), d...)
		if len(block) == 5+states {
			block = append(block, 0)
		}
		p, err := ParseHeartbeat(Frame{Command: Heartbeat, Data: append(make([]byte, 17), block...)})
		if err != nil {
			return nil, err
		}
		return map[string]any{"device_status_code": p.DeviceStatus, "voltage_v": p.VoltageV, "temperature_c": p.TemperatureC, "ports": portStatesData(p.PortStates), "charging": telemetryData(p.ChargingPorts)}, nil
	case 0xB3, 0xB4:
		if len(d) != 36 {
			return nil, ErrPayload
		}
		result := map[string]any{"port": d[0], "state_code": d[1]}
		if d[1] != 1 {
			return result, nil
		}
		order, err := decodeBCD(d[2:10])
		if err != nil {
			return nil, err
		}
		started, err := decodeTime(d[10:16])
		if err != nil {
			return nil, err
		}
		if d[18] > 59 || d[22] > 59 {
			return nil, ErrPayload
		}
		result["device_order"], result["started_at"] = order, started.Format(time.RFC3339)
		result["mode"], result["consumer"] = modeName(d[19]), consumerName(d[23])
		result["remaining_seconds"], result["charged_seconds"] = uint32(u16(16))*60+uint32(d[18]), uint32(u16(20))*60+uint32(d[22])
		result["remaining_kwh"], result["charged_kwh"], result["power_w"], result["band"] = float64(u16(24))/1000, float64(u16(26))/1000, float64(u16(28))/10, int(d[30])+1
		return result, nil
	default:
		return map[string]any{"decode_status": "unsupported_command"}, nil
	}
}
