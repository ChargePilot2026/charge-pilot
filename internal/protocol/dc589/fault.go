package dc589

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// FaultMetric 把 dc589 故障事件翻译成告警维度：指标名、严重级别与说明。
// 设备级故障（端口 0xFF）与端口级故障分别命名，恢复判定按同一维度进行。
// 这是协议语义：故障码与端口的含义由 dc589 帧定义决定，不随调用方改变。
func FaultMetric(event protocol.Event) (metric, severity, note string) {
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

// FaultRecovered 依据故障之后的最新心跳判断告警是否已恢复：
// 设备级告警要求设备状态归零，端口告警要求端口状态不大于 2。
// 缺少端口状态的心跳（仅信号强度）不能证明恢复。
func FaultRecovered(metric string, heartbeat protocol.Event) bool {
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
