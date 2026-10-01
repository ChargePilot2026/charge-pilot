package control

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

type Compensation struct {
	Store   store.MySQLSink
	Devices *protocol.Registry
}

func (c Compensation) Request(ctx context.Context, orderNo, commandID string) error {
	job, needsStop, err := c.Store.CompensateStart(ctx, orderNo, commandID)
	if err != nil || !needsStop {
		return err
	}
	if !c.Devices.Connected(job.DeviceID) {
		return nil
	}
	if err := c.Store.MarkStopSent(ctx, job.CommandID); err != nil {
		return err
	}
	return c.Devices.Send(ctx, job.DeviceID, job.Wire)
}

// RetryStopping 在进程重启或 START 结果不确定后的设备重连时重试停止，不创建第二条 START。
func (c Compensation) RetryStopping(ctx context.Context) error {
	jobs, err := c.Store.StoppingJobs(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, job := range jobs {
		if !c.Devices.Connected(job.DeviceID) {
			continue
		}
		if err := c.Store.MarkStopSent(ctx, job.CommandID); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := c.Devices.Send(ctx, job.DeviceID, job.Wire); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type CompensationAPI struct {
	Service      Compensation
	ServiceToken string
}

func (a CompensationAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/charge-orders/:order_no/compensate", a.handle)
}

func (a CompensationAPI) handle(c *gin.Context) {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return
	}
	var body struct {
		CommandID string `json:"command_id" binding:"required,uuid"`
	}
	if c.Param("order_no") == "" || len(c.Param("order_no")) > 64 || c.ShouldBindJSON(&body) != nil {
		httpapi.BadRequest(c, "invalid compensation request")
		return
	}
	if err := a.Service.Request(c.Request.Context(), c.Param("order_no"), body.CommandID); err != nil {
		if errors.Is(err, store.ErrOrderConflict) {
			httpapi.Write(c, http.StatusConflict, 2000, "command does not match order", nil)
			return
		}
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "compensation send failed", nil)
		return
	}
	httpapi.OK(c, gin.H{"accepted": true})
}
