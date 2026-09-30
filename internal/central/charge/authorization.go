package charge

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StartAuthorization 是给设备网关用的只读守卫。
// 只有在充电订单和它的支付记录都被标记为已支付之后，
// 它才返回这笔订单。这个状态迁移必须由支付回调的实现来完成。
type StartAuthorization struct {
	DB           *gorm.DB
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
	var order paidStartAuthorizationRow
	result := a.DB.WithContext(c.Request.Context()).Table("charge_order AS c").
		Select(`c.id AS charge_order_id, c.order_no, c.user_id, c.device_id, c.port_no,
			p.id AS payment_order_id, c.charge_mode, c.charge_quantity`).
		Joins(`JOIN payment_order AS p ON p.id = c.payment_order_id
			AND p.biz_type = 'charge' AND p.biz_id = c.id AND p.user_id = c.user_id`).
		Where(`c.order_no = ? AND c.status = 'paid' AND c.deleted_at IS NULL
			AND p.status = 'paid' AND p.paid_cents >= p.total_cents
			AND p.total_cents > 0 AND p.deleted_at IS NULL
			AND ((c.charge_mode IN (0,4,10,12) AND c.charge_quantity BETWEEN 1 AND 600)
			OR (c.charge_mode IN (1,11) AND c.charge_quantity BETWEEN 1 AND 65535))`, orderNo).
		Take(&order)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not fully paid", nil)
		return
	}
	if result.Error != nil {
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
		"charge_mode":      order.ChargeMode,
		"charge_quantity":  order.ChargeQuantity,
	})
}

type paidStartAuthorizationRow struct {
	ChargeOrderID  uint64
	OrderNo        string
	UserID         uint64
	DeviceID       string
	PortNo         uint8
	PaymentOrderID uint64
	ChargeMode     uint8
	ChargeQuantity uint16
}
