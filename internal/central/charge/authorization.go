package charge

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	chargedb "github.com/ChargePilot2026/charge-pilot/internal/central/charge/generated"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// StartAuthorization is a read-only guard for the device gateway. It returns
// an order only after both the charge order and its payment record are marked
// paid. The payment callback implementation must perform that transition.
type StartAuthorization struct {
	DB           *sql.DB
	ServiceToken string
}

func (a StartAuthorization) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/charge-orders/:order_no/start-authorization", a.handle)
}

func (a StartAuthorization) handle(c *gin.Context) {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return
	}
	orderNo := strings.TrimSpace(c.Param("order_no"))
	if len(orderNo) == 0 || len(orderNo) > 64 {
		httpapi.BadRequest(c, "invalid order number")
		return
	}
	order, err := chargedb.New(a.DB).PaidStartAuthorization(c.Request.Context(), orderNo)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not fully paid", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "order storage unavailable", nil)
		return
	}
	httpapi.OK(c, gin.H{
		"charge_order_id":  order.ChargeOrderID,
		"order_no":         order.OrderNo,
		"user_id":          order.UserID,
		"device_id":        order.DeviceID,
		"port_no":          order.PortNo,
		"payment_order_id": order.PaymentOrderID,
		"charge_mode":      order.ChargeMode.Int16,
		"charge_quantity":  order.ChargeQuantity.Int16,
	})
}
