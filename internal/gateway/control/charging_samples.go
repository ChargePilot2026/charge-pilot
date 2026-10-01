package control

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// Return bounded, port-specific evidence without raw payloads or card data.
func (a TelemetryAPI) chargingSamples(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	device := c.Param("device_id")
	port, err := strconv.ParseUint(c.Query("port_no"), 10, 8)
	if err != nil || port == 0 || len(device) == 0 || len(device) > 64 {
		httpapi.BadRequest(c, "设备或端口无效")
		return
	}
	from, err := time.Parse(time.RFC3339Nano, c.Query("started_at"))
	now := time.Now().UTC()
	if err != nil || from.After(now) || now.Sub(from) > 7*24*time.Hour {
		httpapi.BadRequest(c, "计量时间窗无效")
		return
	}
	var rows []struct{ EventJSON []byte }
	if err := a.DB.WithContext(c.Request.Context()).Table("device_event").Select("event_json").Where("device_id=? AND event_type='heartbeat' AND received_at>=? AND received_at<=?", device, from.UTC(), now).Order("id DESC").Limit(10081).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5003, "计量证据暂不可读取", nil)
		return
	}
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
	type sample struct {
		Type          protocol.EventType
		DeviceID      string
		ReceivedAt    time.Time
		ChargingPorts []protocol.PortTelemetry
	}
	samples := make([]sample, 0, len(rows))
	for _, row := range rows {
		var e protocol.Event
		if json.Unmarshal(row.EventJSON, &e) != nil {
			httpapi.Write(c, 503, 5003, "计量证据不可解析", nil)
			return
		}
		if e.DeviceID != device || e.Type != protocol.Heartbeat {
			continue
		}
		for _, p := range e.ChargingPorts {
			if p.Port == uint8(port) {
				samples = append(samples, sample{Type: protocol.Heartbeat, DeviceID: device, ReceivedAt: e.ReceivedAt, ChargingPorts: []protocol.PortTelemetry{p}})
				break
			}
		}
	}
	httpapi.OK(c, gin.H{"samples": samples, "complete": len(rows) <= 10080})
}
