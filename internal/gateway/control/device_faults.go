package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerDeviceFaults 把 device_event 故障证据暴露给 central 的告警同步：
// 未处理故障列表、处理标记与最新心跳查询。全部要求服务令牌，
// 原始事件载荷原样返回，由调用方按协议语义解析。
func (a TelemetryAPI) registerDeviceFaults(r *gin.Engine) {
	r.GET("/api/v1/internal/device-faults", a.deviceFaults)
	r.POST("/api/v1/internal/device-faults/mark-processed", a.markFaultsProcessed)
	r.GET("/api/v1/internal/devices/:device_id/latest-heartbeat", a.latestHeartbeat)
}

// deviceFaultReport 是一条未处理的 dc589 故障事件，载荷为原始 event_json。
type deviceFaultReport struct {
	ID         uint64    `json:"id"`
	EventKey   string    `json:"event_key"`
	ReceivedAt time.Time `json:"received_at"`
	Payload    string    `json:"payload"`
}

// deviceFaults 按 id 升序返回未处理故障，limit 1–100，默认 100。
func (a TelemetryAPI) deviceFaults(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			httpapi.BadRequest(c, "limit 须为 1–100")
			return
		}
		limit = parsed
	}
	var reports []deviceFaultReport
	if err := a.DB.WithContext(c.Request.Context()).Table("device_event").
		Select("id, event_key, received_at, event_json AS payload").
		Where("event_type = 'fault' AND protocol_name = 'dc589' AND processed_at IS NULL").
		Order("id").Limit(limit).Find(&reports).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "故障事件暂不可读取", nil)
		return
	}
	if reports == nil {
		reports = []deviceFaultReport{}
	}
	httpapi.OK(c, gin.H{"reports": reports})
}

// markFaultsProcessed 批量推进故障事件的处理时间；已处理的行跳过，
// 返回实际推进条数。行锁保证与并发扫描的顺序一致。
func (a TelemetryAPI) markFaultsProcessed(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var body struct {
		IDs []uint64 `json:"ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.IDs) == 0 || len(body.IDs) > 100 {
		httpapi.BadRequest(c, "ids 须为 1–100 个事件主键")
		return
	}
	marked := int64(0)
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var rows []struct{ ID uint64 }
		if err := tx.Table("device_event").Select("id").
			Where("id IN ? AND processed_at IS NULL", body.IDs).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		result := tx.Table("device_event").Where("id IN ? AND processed_at IS NULL", body.IDs).
			Update("processed_at", gorm.Expr("UTC_TIMESTAMP(3)"))
		marked = result.RowsAffected
		return result.Error
	})
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "处理标记暂不可写", nil)
		return
	}
	httpapi.OK(c, gin.H{"marked": marked})
}

// latestHeartbeat 返回某设备在指定时间之后的最新 dc589 心跳原始载荷；
// 没有更新的心跳时 found 为 false，供恢复判定区分"无证据"与"未恢复"。
// after_key 以事件键为水印（取该事件的接收时间），优先于 after；
// 恢复判定用它锚定"最近一次故障之后"的心跳。
func (a TelemetryAPI) latestHeartbeat(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	device := c.Param("device_id")
	if device == "" || len(device) > 64 {
		httpapi.BadRequest(c, "设备无效")
		return
	}
	after, err := time.Parse(time.RFC3339Nano, c.Query("after"))
	if err != nil {
		httpapi.BadRequest(c, "after 须为 RFC 3339 时间")
		return
	}
	if key := c.Query("after_key"); key != "" {
		var source struct {
			ReceivedAt time.Time `gorm:"column:received_at"`
		}
		found := a.DB.WithContext(c.Request.Context()).Table("device_event").
			Select("received_at").Where("device_id = ? AND event_key = ?", device, key).Take(&source)
		if found.Error != nil {
			if errors.Is(found.Error, gorm.ErrRecordNotFound) {
				httpapi.OK(c, gin.H{"found": false})
				return
			}
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "心跳证据暂不可读取", nil)
			return
		}
		after = source.ReceivedAt
	}
	var row struct {
		ReceivedAt time.Time       `gorm:"column:received_at"`
		Payload    json.RawMessage `gorm:"column:event_json"`
	}
	found := a.DB.WithContext(c.Request.Context()).Table("device_event").
		Select("received_at, event_json").
		Where("device_id = ? AND protocol_name = 'dc589' AND event_type = 'heartbeat' AND received_at > ?", device, after.UTC()).
		Order("received_at DESC, id DESC").Take(&row)
	if found.Error != nil {
		if errors.Is(found.Error, gorm.ErrRecordNotFound) {
			httpapi.OK(c, gin.H{"found": false})
			return
		}
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "心跳证据暂不可读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"found": true, "received_at": row.ReceivedAt, "payload": string(row.Payload)})
}
