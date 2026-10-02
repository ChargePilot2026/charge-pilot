package order

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol/dc589"
	"net/http"
	"strconv"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StartAuthorization 是网关启动充电的只读校验入口，仅返回订单和支付记录均已支付的订单。
// 支付状态由支付回调更新。
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
			AND ((c.charge_mode IN (0,4) AND c.charge_quantity BETWEEN 1 AND 4320)
			OR (c.charge_mode = 1 AND c.charge_quantity BETWEEN 1000 AND 65000))`, orderNo).
		Take(&order)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusConflict, 2000, "order is not fully paid", nil)
		return
	}
	if result.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "order storage unavailable", nil)
		return
	}
	consumer, cardNumber, balanceUnits := uint8(2), uint32(0), uint16(0)
	// 跨家族读：card_charge 归属 card 家族，此处用局部行投影避免 order 反向依赖 card。
	var cardSession struct {
		ChargeOrderID    uint64
		CardNo           string
		WalletAfterCents int64
	}
	if err := a.DB.WithContext(c.Request.Context()).Table("card_charge").Where("charge_order_id=?", order.ChargeOrderID).Find(&cardSession).Error; err != nil {
		httpapi.Write(c, 503, 5001, "刷卡执行快照暂不可读取", nil)
		return
	}
	if cardSession.ChargeOrderID != 0 {
		n, err := strconv.ParseUint(cardSession.CardNo, 10, 32)
		if err != nil || n == 0 {
			httpapi.Write(c, 409, 2000, "冻结卡号无效", nil)
			return
		}
		consumer = 3
		cardNumber = uint32(n)
		balanceUnits = dc589.CardBalanceUnits(cardSession.WalletAfterCents)
	}
	httpapi.OK(c, gin.H{"consumer_type": consumer, "card_number": cardNumber, "card_balance_units": balanceUnits,
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
