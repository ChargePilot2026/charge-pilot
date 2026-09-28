package charge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"time"
)

type RefundAPI struct {
	Executor     RefundExecutor
	ServiceToken string
}

func (a RefundAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/internal/refunds/dispatch", func(c *gin.Context) {
		given := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
		wanted := sha256.Sum256([]byte(a.ServiceToken))
		if a.ServiceToken == "" || subtle.ConstantTimeCompare(given[:], wanted[:]) != 1 {
			httpapi.Write(c, 401, 1001, "service token invalid", nil)
			return
		}
		if a.Executor.Provider == nil {
			httpapi.OK(c, gin.H{"processed": 0, "enabled": false})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		n, err := a.Executor.Batch(ctx)
		if err != nil {
			httpapi.Write(c, 503, 5003, "refund dispatch incomplete; retry scheduled", gin.H{"processed": n})
			return
		}
		httpapi.OK(c, gin.H{"processed": n, "enabled": true})
	})
}
