package charge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// AutoStopAPI 把停机扫描暴露给 worker 触发；判定、计费截止点冻结与停机
// 下发都在 central 完成，worker 只发"该扫一轮了"的意图。
type AutoStopAPI struct {
	Stopper      AutoStopper
	ServiceToken string
}

func (a AutoStopAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/internal/charge-orders/auto-stop", a.autoStop)
}

func (a AutoStopAPI) autoStop(c *gin.Context) {
	given := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	wanted := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(given[:], wanted[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	stopped, err := a.Stopper.Run(ctx)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "auto stop sweep incomplete; retry scheduled", gin.H{"stopped": stopped})
		return
	}
	httpapi.OK(c, gin.H{"stopped": stopped})
}
