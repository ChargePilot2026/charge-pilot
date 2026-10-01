package internaljob

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/outbox"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// OpsAPI 提供流积压、死信查询和手动重放接口，全部要求服务令牌。
type OpsAPI struct {
	WorkerDB     *gorm.DB
	ServiceToken string
	DLQ          outbox.DLQ
	// Replay 把一条停放的记录重新送回它的 handler。
	// 它是注入进来的，这样运维接口就不必依赖某个具体的业务 handler。
	Replay func(ctx context.Context, stream, eventID, source string, payload []byte) error
}

func (a OpsAPI) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/worker/ops/streams", a.authorized(), a.streams)
	router.GET("/api/v1/internal/worker/ops/dlq", a.authorized(), a.deadLetters)
	router.POST("/api/v1/internal/worker/ops/dlq/:stream/replay", a.authorized(), a.replay)
}

func (a OpsAPI) authorized() gin.HandlerFunc {
	return func(c *gin.Context) {
		provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
		expected := sha256.Sum256([]byte(a.ServiceToken))
		if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}

func (a OpsAPI) streams(c *gin.Context) {
	pending, err := a.DLQ.PendingSummary(c.Request.Context())
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "stream 状态暂时不可读", nil)
		return
	}
	dead, err := a.DLQ.DeadLetterCounts(c.Request.Context())
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "stream 状态暂时不可读", nil)
		return
	}
	httpapi.OK(c, gin.H{"pending": pending, "dead_letter": dead, "checked_at": time.Now().UTC()})
}

func (a OpsAPI) deadLetters(c *gin.Context) {
	stream := c.Query("stream")
	if stream == "" {
		httpapi.BadRequest(c, "请指定 stream")
		return
	}
	rows := []map[string]any{}
	query := a.WorkerDB.WithContext(c.Request.Context()).Table("dlq_log").Where("stream = ?", stream)
	if raw := c.Query("status"); raw != "" {
		if raw != "open" && raw != "replayed" && raw != "closed" {
			httpapi.BadRequest(c, "状态无效")
			return
		}
		query = query.Where("status = ?", raw)
	}
	if err := query.Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "死信记录暂时无法读取", nil)
		return
	}
	var cursors []struct {
		Stream        string  `json:"stream"`
		LastID        *string `json:"last_id"`
		ScannedTotal  uint64  `json:"scanned_total"`
		ReplayedTotal uint64  `json:"replayed_total"`
	}
	_ = a.WorkerDB.WithContext(c.Request.Context()).Table("dlq_replay_cursor").Where("stream = ?", stream).Find(&cursors).Error
	httpapi.OK(c, gin.H{"items": rows, "cursors": cursors})
}

// replay 重跑某一条流上停放的记录。
// dlq_replay_cursor 里的滑动游标保证每一轮都能向前推进。
func (a OpsAPI) replay(c *gin.Context) {
	stream := c.Param("stream")
	if stream == "" || len(stream) > 64 {
		httpapi.BadRequest(c, "stream 无效")
		return
	}
	if a.Replay == nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "重放处理器未配置", nil)
		return
	}
	replayed, err := a.DLQ.ReplayOnce(c.Request.Context(), stream, a.Replay)
	if err != nil {
		httpapi.OK(c, gin.H{"stream": stream, "replayed": replayed, "error": err.Error()})
		return
	}
	httpapi.OK(c, gin.H{"stream": stream, "replayed": replayed})
}
