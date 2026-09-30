package control

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"gorm.io/gorm"
	"net/http"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

type StopAuthorizer interface {
	ActiveStop(context.Context, string, uint64) (store.ActiveOrder, error)
}

type UserStopService struct {
	Orders  StopAuthorizer
	Store   store.MySQLSink
	Devices *protocol.Registry
}

func (s UserStopService) Stop(ctx context.Context, orderNo string, userID uint64) (store.StopReservation, error) {
	reservation, err := s.Store.ExistingUserStop(ctx, orderNo, userID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return store.StopReservation{}, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		active, err := s.Orders.ActiveStop(ctx, orderNo, userID)
		if err != nil {
			return store.StopReservation{}, err
		}
		reservation, err = s.Store.ReserveUserStop(ctx, active)
		if err != nil {
			return store.StopReservation{}, err
		}
	}
	if reservation.Status == "pending" && s.Devices.Connected(reservation.DeviceID) {
		if err := s.Store.MarkUserStopSent(ctx, reservation.CommandID); err != nil {
			return store.StopReservation{}, err
		}
		reservation.Status = "sent"
		// 写入失败或只写了一半的状态依然留在库里，重试循环会重发
		// 同一条 STOP 的 session。HTTP 响应永远不会声称已经断电。
		_ = s.Devices.Send(ctx, reservation.DeviceID, reservation.Wire)
	}
	return reservation, nil
}

func (s UserStopService) RetryPending(ctx context.Context) error {
	jobs, err := s.Store.PendingUserStops(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, job := range jobs {
		if !s.Devices.Connected(job.DeviceID) {
			continue
		}
		if err := s.Store.MarkUserStopSent(ctx, job.CommandID); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := s.Devices.Send(ctx, job.DeviceID, job.Wire); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type UserStopAPI struct {
	Service      UserStopService
	ServiceToken string
}

func (a UserStopAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/charge-orders/stop", a.handle)
}

func (a UserStopAPI) handle(c *gin.Context) {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return
	}
	var request struct {
		OrderNo string `json:"order_no" binding:"required,max=64"`
		UserID  uint64 `json:"user_id" binding:"required"`
		Source  string `json:"source" binding:"required,oneof=user_app admin auto"`
	}
	if c.ShouldBindJSON(&request) != nil {
		httpapi.BadRequest(c, "invalid stop request")
		return
	}
	stop, err := a.Service.Stop(c.Request.Context(), request.OrderNo, request.UserID)
	if errors.Is(err, ErrNotCharging) || errors.Is(err, store.ErrOrderConflict) || errors.Is(err, store.ErrPortUnavailable) {
		httpapi.Write(c, http.StatusConflict, 2000, "charge order is not stoppable", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "stop command unavailable", nil)
		return
	}
	httpapi.Write(c, http.StatusAccepted, 0, "accepted", gin.H{"accepted": true, "stopped": false, "command_id": stop.CommandID, "status": stop.Status})
}
