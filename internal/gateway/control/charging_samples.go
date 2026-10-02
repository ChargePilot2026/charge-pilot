package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/gin-gonic/gin"
)

// 停机判定取证的上界：窗口内单设备单端口最多回传的心跳证据行数
// （每分钟一条可覆盖 7 天）。批量端点按条数乘该上界界定响应体积。
const chargingEvidenceMaxRows = 10080

// 一次批量取证请求允许的最大条目数；超出后调用方应分批。
const chargingEvidenceMaxBatch = 20

// chargingEvidenceWindow 是最远可回溯的取证窗口，与分区保留策略对齐。
const chargingEvidenceWindow = 7 * 24 * time.Hour

// chargingEvidenceSample 是一条端口级心跳证据，不含原始报文与卡片数据。
type chargingEvidenceSample struct {
	Type          protocol.EventType
	DeviceID      string
	ReceivedAt    time.Time
	ChargingPorts []protocol.PortTelemetry
}

// collectChargingSamples 读取设备+端口在 [from, 当前] 窗口内的心跳证据，
// 按 id 升序返回。afterID > 0 时只取 id 更大的行，用于游标翻页；
// complete 为真表示窗口内证据已取完，为假时调用方应以返回的
// nextAfterID 继续取下一段。单段行数上限 chargingEvidenceMaxRows。
func (a TelemetryAPI) collectChargingSamples(ctx context.Context, device string, port uint8, from time.Time, afterID uint64, limit int) ([]chargingEvidenceSample, uint64, bool, error) {
	now := time.Now().UTC()
	query := a.DB.WithContext(ctx).Table("device_event").Select("id, event_json").
		Where("device_id=? AND event_type='heartbeat' AND received_at>=? AND received_at<=?", device, from.UTC(), now)
	if afterID > 0 {
		query = query.Where("id > ?", afterID)
	}
	var rows []struct {
		ID        uint64 `gorm:"column:id"`
		EventJSON []byte `gorm:"column:event_json"`
	}
	if err := query.Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, 0, false, err
	}
	complete := len(rows) <= limit
	if !complete {
		rows = rows[:limit]
	}
	samples := make([]chargingEvidenceSample, 0, len(rows))
	var lastID uint64
	for _, row := range rows {
		lastID = row.ID
		var e protocol.Event
		if err := json.Unmarshal(row.EventJSON, &e); err != nil {
			return nil, 0, false, err
		}
		if e.DeviceID != device || e.Type != protocol.Heartbeat {
			continue
		}
		for _, p := range e.ChargingPorts {
			if p.Port == port {
				samples = append(samples, chargingEvidenceSample{Type: protocol.Heartbeat, DeviceID: device, ReceivedAt: e.ReceivedAt, ChargingPorts: []protocol.PortTelemetry{p}})
				break
			}
		}
	}
	var nextAfterID uint64
	if !complete {
		nextAfterID = lastID
	}
	return samples, nextAfterID, complete, nil
}

// Return bounded, port-specific evidence without raw payloads or card data.
// after_id 为游标：传入上一段返回的 next_after_id 可续取更早的证据。
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
	if err != nil || from.After(now) || now.Sub(from) > chargingEvidenceWindow {
		httpapi.BadRequest(c, "计量时间窗无效")
		return
	}
	var afterID uint64
	if raw := c.Query("after_id"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			httpapi.BadRequest(c, "after_id 无效")
			return
		}
		afterID = parsed
	}
	samples, nextAfterID, complete, err := a.collectChargingSamples(c.Request.Context(), device, uint8(port), from, afterID, chargingEvidenceMaxRows)
	if err != nil {
		httpapi.Write(c, 503, 5003, "计量证据暂不可读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"samples": samples, "complete": complete, "next_after_id": nextAfterID})
}

// chargingEvidenceRequest 是批量取证的一条请求，语义与 GET 查询参数一致。
type chargingEvidenceRequest struct {
	DeviceID  string    `json:"device_id"`
	PortNo    uint8     `json:"port_no"`
	StartedAt time.Time `json:"started_at"`
	AfterID   uint64    `json:"after_id"`
}

// chargingEvidence 一次为多个设备+端口取证，供停机判定循环批量拉取，
// 避免每订单一次 HTTP 调用。单条上限与 GET 相同，条目数上限
// chargingEvidenceMaxBatch；任一请求体无效则整体拒绝。
func (a TelemetryAPI) chargingEvidence(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var body struct {
		Requests []chargingEvidenceRequest `json:"requests"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.Requests) == 0 || len(body.Requests) > chargingEvidenceMaxBatch {
		httpapi.BadRequest(c, "批量证据请求须为 1–20 条")
		return
	}
	now := time.Now().UTC()
	for i, req := range body.Requests {
		if req.DeviceID == "" || len(req.DeviceID) > 64 || req.PortNo == 0 || req.StartedAt.IsZero() || req.StartedAt.After(now) || now.Sub(req.StartedAt) > chargingEvidenceWindow {
			httpapi.BadRequest(c, fmt.Sprintf("第 %d 条证据请求无效", i+1))
			return
		}
	}
	type entry struct {
		DeviceID    string                   `json:"device_id"`
		PortNo      uint8                    `json:"port_no"`
		Samples     []chargingEvidenceSample `json:"samples"`
		Complete    bool                     `json:"complete"`
		NextAfterID uint64                   `json:"next_after_id"`
	}
	evidence := make([]entry, 0, len(body.Requests))
	for _, req := range body.Requests {
		samples, nextAfterID, complete, err := a.collectChargingSamples(c.Request.Context(), req.DeviceID, req.PortNo, req.StartedAt, req.AfterID, chargingEvidenceMaxRows)
		if err != nil {
			httpapi.Write(c, 503, 5003, "计量证据暂不可读取", nil)
			return
		}
		evidence = append(evidence, entry{DeviceID: req.DeviceID, PortNo: req.PortNo, Samples: samples, Complete: complete, NextAfterID: nextAfterID})
	}
	httpapi.OK(c, gin.H{"evidence": evidence})
}
