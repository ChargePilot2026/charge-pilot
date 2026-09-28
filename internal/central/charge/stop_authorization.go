package charge

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type StopAuthorization struct {
	DB           *gorm.DB
	ServiceToken string
}

func (a StopAuthorization) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/charge-orders/:order_no/stop-authorization", a.handle)
}

func (a StopAuthorization) handle(c *gin.Context) {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return
	}
	userID, err := strconv.ParseUint(c.Query("user_id"), 10, 64)
	if err != nil || userID == 0 || c.Param("order_no") == "" {
		httpapi.BadRequest(c, "invalid stop authorization request")
		return
	}
	var order activeStopAuthorizationRow
	result := a.DB.WithContext(c.Request.Context()).Table("charge_order AS c").
		Select("c.id AS charge_order_id, c.order_no, c.user_id, c.device_id, c.port_no, s.port_id, s.command_id AS start_command_id").
		Joins("JOIN charge_start_receipt AS s ON s.charge_order_id = c.id AND s.success = TRUE").
		Where("c.order_no = ? AND c.user_id = ? AND c.status = 'charging' AND c.deleted_at IS NULL AND s.port_id IS NOT NULL", c.Param("order_no"), userID).
		Take(&order)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not charging for this user", nil)
		return
	}
	if result.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "order storage unavailable", nil)
		return
	}
	if !order.PortID.Valid || order.PortID.Int64 <= 0 {
		httpapi.Write(c, http.StatusConflict, 2000, "port identity unavailable", nil)
		return
	}
	httpapi.OK(c, gin.H{"charge_order_id": order.ChargeOrderID, "order_no": order.OrderNo,
		"user_id": order.UserID, "device_id": order.DeviceID, "port_no": order.PortNo,
		"port_id": order.PortID.Int64, "start_command_id": order.StartCommandID})
}

type activeStopAuthorizationRow struct {
	ChargeOrderID  uint64
	OrderNo        string
	UserID         uint64
	DeviceID       string
	PortNo         uint8
	PortID         sql.NullInt64
	StartCommandID string
}
