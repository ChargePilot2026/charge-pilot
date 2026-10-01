package control

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SessionAPI 提供设备会话审计及遗留会话清理接口。
// 正常连接由所属进程关闭，进程退出后遗留的开放记录由运维清理。
type SessionAPI struct {
	DB           *gorm.DB
	ServiceToken string
	// IdleThreshold 是候选会话的最小静默时间；所属进程仍活跃时不回收连接。
	IdleThreshold time.Duration
	// MaxRows 限制单次清理的规模，避免积压过大时把一个长事务吊住；
	// 调用方重跑一次就行。
	MaxRows int
}

func (a SessionAPI) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/device-sessions", a.listIdle)
	router.POST("/api/v1/internal/device-sessions/cleanup-idle", a.cleanupIdle)
}

func (a SessionAPI) authorized(c *gin.Context) bool {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return false
	}
	return true
}

// IdleSession 是向运维报告的一条被遗弃的连接。
type IdleSession struct {
	SessionID    string    `json:"session_id"`
	DeviceID     string    `json:"device_id"`
	Protocol     string    `json:"protocol"`
	RemoteAddr   string    `json:"remote_addr"`
	StartedAt    time.Time `json:"started_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

// idleQuery 查询超过静默阈值且 ended_at 为 NULL 的会话，不修改已正常关闭的记录。
func (a SessionAPI) idleQuery(ctx context.Context, db *gorm.DB) *gorm.DB {
	if db == nil {
		db = a.DB
	}
	return db.WithContext(ctx).Table("device_session").
		Where("ended_at IS NULL AND last_active_at < ?", time.Now().UTC().Add(-a.threshold()))
}

func (a SessionAPI) threshold() time.Duration {
	if a.IdleThreshold > 0 {
		return a.IdleThreshold
	}
	return 15 * time.Minute
}

func (a SessionAPI) limit() int {
	if a.MaxRows > 0 {
		return a.MaxRows
	}
	return 500
}

// listIdle 只报告被遗弃的会话而不做任何改动，这样运维可以先看清一次清理
// 会关掉哪些，再决定要不要跑。
func (a SessionAPI) listIdle(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var sessions []IdleSession
	if err := a.idleQuery(c.Request.Context(), nil).
		Select("session_id", "device_id", "protocol", "remote_addr", "started_at", "last_active_at").
		Order("last_active_at ASC").Limit(a.limit()).Scan(&sessions).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "设备会话查询失败", nil)
		return
	}
	if sessions == nil {
		sessions = []IdleSession{}
	}
	httpapi.OK(c, gin.H{
		"items":              sessions,
		"count":              len(sessions),
		"idle_threshold_sec": int(a.threshold().Seconds()),
	})
}

// CleanupIdle 关闭遗留会话并补齐断连终态字段。
func (a SessionAPI) cleanupIdle(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	ctx := c.Request.Context()
	now := time.Now().UTC()

	// 在同一事务中读取并关闭候选会话；更新再次检查 ended_at IS NULL，避免覆盖并发正常关闭的记录。
	var closed int64
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []deviceSessionKey
		if err := a.idleQuery(ctx, tx).
			Select("id", "created_month").Limit(a.limit()).Scan(&candidates).Error; err != nil {
			return err
		}
		for _, key := range candidates {
			// 用读取时的 ID 和分区月份寻址，并保留开放状态条件，防止覆盖已关闭会话。
			month := time.Date(key.CreatedMonth.UTC().Year(), key.CreatedMonth.UTC().Month(), key.CreatedMonth.UTC().Day(), 0, 0, 0, 0, time.UTC)
			result := tx.Table("device_session").
				Where("id = ? AND created_month = ? AND ended_at IS NULL", key.ID, month).
				Updates(map[string]any{
					"ended_at":     now,
					"close_reason": "abandoned",
				})
			if result.Error != nil {
				return result.Error
			}
			closed += result.RowsAffected
		}
		return nil
	})
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "设备会话清理失败", nil)
		return
	}
	httpapi.OK(c, gin.H{
		"closed":             closed,
		"idle_threshold_sec": int(a.threshold().Seconds()),
	})
}

// deviceSessionKey 使用 ID 与 created_month 联合主键定位会话；月份按 DATE 比较。
type deviceSessionKey struct {
	ID           uint64    `gorm:"column:id"`
	CreatedMonth time.Time `gorm:"column:created_month"`
}
