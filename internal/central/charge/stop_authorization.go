package charge

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	chargedb "github.com/ChargePilot2026/charge-pilot/internal/central/charge/generated"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

type StopAuthorization struct {
	DB           *sql.DB
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
	order, err := chargedb.New(a.DB).ActiveStopAuthorization(c.Request.Context(), chargedb.ActiveStopAuthorizationParams{OrderNo: c.Param("order_no"), UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not charging for this user", nil)
		return
	}
	if err != nil {
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
