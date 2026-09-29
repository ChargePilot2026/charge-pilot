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

// SessionAPI serves device connection audit maintenance.
//
// A session row is normally closed by the serving process when the connection
// ends. The rows that stay open are the ones whose process died mid-session, and
// they accumulate silently, so this endpoint is how an operator finds and closes
// them out.
type SessionAPI struct {
	DB           *gorm.DB
	ServiceToken string
	// IdleThreshold is how long a session must have been silent before it is
	// treated as abandoned. It is a floor, not a guarantee: a device that is
	// still connected but has gone quiet is never reaped while its process
	// lives, because only that process knows the connection is still held.
	IdleThreshold time.Duration
	// MaxRows bounds one cleanup so a large backlog cannot hold a long
	// transaction; the caller can simply run it again.
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

// IdleSession is one abandoned connection as reported to an operator.
type IdleSession struct {
	SessionID    string    `json:"session_id"`
	DeviceID     string    `json:"device_id"`
	Protocol     string    `json:"protocol"`
	RemoteAddr   string    `json:"remote_addr"`
	StartedAt    time.Time `json:"started_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

// idleQuery matches sessions that are still open and have not been heard from
// inside the threshold. ended_at IS NULL is the open-session marker, so a
// session that already closed gracefully is never reaped: its counters are
// final and re-stamping them would rewrite a true record.
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

// ListIdle reports abandoned sessions without changing anything, so an operator
// can see what a cleanup would close before deciding to run it.
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

// CleanupIdle closes abandoned sessions, stamping the terminal fields a
// graceful disconnect would have written so a reaped row is never mistaken for
// a live one.
func (a SessionAPI) cleanupIdle(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	ctx := c.Request.Context()
	now := time.Now().UTC()

	// The candidate rows are read and closed inside one transaction, and each
	// update re-checks ended_at IS NULL, so a session that a live process
	// closed between the read and the write is not stamped as abandoned.
	var closed int64
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []deviceSessionKey
		if err := a.idleQuery(ctx, tx).
			Select("id", "created_month").Limit(a.limit()).Scan(&candidates).Error; err != nil {
			return err
		}
		for _, key := range candidates {
			// The row is addressed by the id and month it was read with, so a
			// session another writer closes in between is not stamped here.
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

// deviceSessionKey addresses one row. The table is partitioned by created_month,
// which is part of the primary key, so both columns are needed to identify it.
// created_month is a DATE and is therefore compared as a date, not as the
// timestamp it was seeded with.
type deviceSessionKey struct {
	ID           uint64    `gorm:"column:id"`
	CreatedMonth time.Time `gorm:"column:created_month"`
}
