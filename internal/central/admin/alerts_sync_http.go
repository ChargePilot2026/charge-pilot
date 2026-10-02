package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// DeviceAlertSyncAPI 把设备告警同步暴露给 worker 触发；同步在 central 完成，
// worker 只按原每秒节奏发触发意图，不接触 device_event 或 alert_event。
type DeviceAlertSyncAPI struct {
	Sync         DeviceAlertSync
	ServiceToken string
}

func (a DeviceAlertSyncAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/internal/device-alerts/sync", a.sync)
}

func (a DeviceAlertSyncAPI) sync(c *gin.Context) {
	given := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	wanted := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(given[:], wanted[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	raised, err := a.Sync.Run(ctx)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "device alert sync incomplete; retry scheduled", gin.H{"raised": raised})
		return
	}
	httpapi.OK(c, gin.H{"raised": raised})
}
