package charge

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DevelopmentPaymentAPI is registered only by PAYMENT_MODE=simulation. It
// confirms the authenticated user's own simulated checkout via the normal
// callback transaction; it cannot accept arbitrary amounts or identities.
type DevelopmentPaymentAPI struct {
	Auth  identity.SessionAuthenticator
	DB    *gorm.DB
	Store PaymentCallbackStore
}

func (a DevelopmentPaymentAPI) Register(r *gin.Engine) {
	r.POST("/api/v1/user/development/payments/confirm", a.confirm)
}
func (a DevelopmentPaymentAPI) confirm(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		MerchantOrderNo string `json:"merchant_order_no" binding:"required,max=64"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "invalid payment request")
		return
	}
	var order struct {
		ID         uint64
		OrderNo    string
		BizType    string
		TotalCents int64
		OpenID     string `gorm:"column:openid"`
		CreatedAt  time.Time
	}
	err := a.DB.WithContext(c.Request.Context()).Table("payment_order p").Select("p.id,p.order_no,p.biz_type,p.total_cents,p.created_at,u.openid").Joins("JOIN user u ON u.id=p.user_id").Where("p.order_no=? AND p.user_id=? AND p.pay_method='wechat'", in.MerchantOrderNo, user).Take(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "payment not found", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "payment unavailable", nil)
		return
	}
	var prepay ChargePrepayRecord
	var params struct {
		Provider string `json:"provider"`
	}
	if err = a.DB.WithContext(c.Request.Context()).Where("payment_order_id=?", order.ID).Take(&prepay).Error; err != nil || json.Unmarshal(prepay.ParamsJSON, &params) != nil || params.Provider != "simulation" {
		httpapi.Write(c, 409, 3001, "payment is not a simulated checkout", nil)
		return
	}
	// Stable provider receipt values make concurrent/repeated confirmations use
	// exactly the same callback digest rather than generating a second payment.
	result, err := a.Store.Apply(c.Request.Context(), VerifiedPayment{Provider: "simulation", MerchantID: "local-simulation", AppID: a.Store.ExpectedAppID, MerchantOrderNo: order.OrderNo, TransactionID: "SIMH5" + order.OrderNo, OpenID: order.OpenID, PaidCents: order.TotalCents, PaidAt: order.CreatedAt})
	if errors.Is(err, ErrPaymentCallbackConflict) {
		httpapi.Write(c, 409, 3001, "simulation payment conflict", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "simulation confirmation unavailable", nil)
		return
	}
	httpapi.OK(c, result)
}
