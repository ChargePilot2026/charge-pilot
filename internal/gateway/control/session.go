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

// SessionAPI 提供设备连接的会话审计维护能力。
//
// 一条 session 记录通常由服务它的进程在连接结束时关闭。
// 仍然开着的那些，就是进程在会话中途死掉的那些，
// 它们会悄无声息地堆积起来，所以这个接口
// 就是运维用来把它们找出来并关掉的入口。
type SessionAPI struct {
	DB           *gorm.DB
	ServiceToken string
	// IdleThreshold 是一条会话静默多久之后才被视为被遗弃。
	// 它是下限而非保证：一条仍然连着、只是不吭声的设备，
	// 只要它的进程还活着就永远不会被回收，
	// 因为只有那个进程自己知道连接还握在手里。
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

// idleQuery 匹配仍然开着、且在阈值之内没有任何动静的会话。
// ended_at IS NULL 是"会话未关闭"的标记，所以一条已经正常关闭的会话
// 永远不会被回收：它的计数器是最终值，
// 重新盖一遍时间戳等于改写真实的记录。
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

// CleanupIdle 关闭被遗弃的会话，并补上正常断连时本该写入的终态字段，
// 这样一条被回收的记录不会被误当成还活着的会话。
func (a SessionAPI) cleanupIdle(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	ctx := c.Request.Context()
	now := time.Now().UTC()

	// 候选行在同一个事务里读出并关闭，每条 update 都会重新检查
	// ended_at IS NULL，所以在读取和写入之间被活跃进程关掉的会话不会被
	// 盖成"被遗弃"。
	var closed int64
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []deviceSessionKey
		if err := a.idleQuery(ctx, tx).
			Select("id", "created_month").Limit(a.limit()).Scan(&candidates).Error; err != nil {
			return err
		}
		for _, key := range candidates {
			// 这一行是用读它时拿到的 id 和月份寻址的，所以中途被别人
			// 关掉的那条会话不会在这里被误盖。
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

// deviceSessionKey 定位一行。该表按 created_month 分区，
// 而这一列属于主键，所以必须两列一起才能确定是哪一行。
// created_month 是 DATE，比较时按日期比，
// 而不是按它写入时携带的那个时间戳。
type deviceSessionKey struct {
	ID           uint64    `gorm:"column:id"`
	CreatedMonth time.Time `gorm:"column:created_month"`
}
