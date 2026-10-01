package charge

import (
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// DebtAPI 提供用户欠费查询和支付接口，应付金额始终读取数据库，不接受客户端金额覆盖。
type DebtAPI struct {
	Auth     identity.SessionAuthenticator
	Debts    DebtStore
	Provider PrepayProvider
}

func (a DebtAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/debts", a.list)
	r.POST("/api/v1/user/debts/:id/pay", a.pay)
}

func (a DebtAPI) list(c *gin.Context) {
	page, size := 1, 20
	for name, target := range map[string]*int{"page": &page, "page_size": &size} {
		if raw := c.Query(name); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				httpapi.BadRequest(c, "分页参数无效：page ≥ 1，page_size 为 1–100")
				return
			}
			*target = parsed
		}
	}
	status := c.Query("status")
	if !oneOfStatus(status, "unpaid partial settled waived") {
		httpapi.BadRequest(c, "欠费状态无效")
		return
	}
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	rows, total, err := a.Debts.ListDebts(c.Request.Context(), userID, status, page, size)
	if err != nil {
		httpapi.Write(c, 503, 5003, "欠费记录暂时无法读取", nil)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, gin.H{
			"debt_no": row.DebtNo, "charge_order_id": row.ChargeOrderID, "debt_cents": row.DebtCents,
			"paid_cents": row.PaidCents, "outstanding_cents": row.OutstandingCents, "status": row.Status,
			"created_at": row.CreatedAt,
		})
	}
	httpapi.OK(c, gin.H{"items": out, "total": total, "page": page, "page_size": size})
}

func oneOfStatus(value, allowed string) bool {
	if value == "" {
		return true
	}
	return slices.Contains(splitFields(allowed), value)
}

func splitFields(value string) []string {
	out := []string{}
	current := ""
	for _, r := range value {
		if r == ' ' {
			if current != "" {
				out = append(out, current)
			}
			current = ""
			continue
		}
		current += string(r)
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

func (a DebtAPI) pay(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "欠费记录 ID 无效")
		return
	}
	var in struct {
		RequestID string `json:"request_id"`
	}
	if c.ShouldBindJSON(&in) != nil || in.RequestID == "" {
		httpapi.BadRequest(c, "请提供请求号")
		return
	}
	userID, ok := a.Auth.Authenticate(c)
	if !ok {
		httpapi.Write(c, 401, 1001, "登录已失效，请重新登录", nil)
		return
	}
	ctx := c.Request.Context()
	var owned struct {
		UserID uint64 `gorm:"column:user_id"`
	}
	if err := a.Debts.DB.WithContext(ctx).Table("charge_debt").Select("user_id").Where("id = ?", id).Take(&owned).Error; err != nil {
		httpapi.Write(c, 404, 1004, "欠费记录不存在", nil)
		return
	}
	// 别人的欠款必须与不存在的欠款无从分辨。
	if owned.UserID != userID {
		httpapi.Write(c, 404, 1004, "欠费记录不存在", nil)
		return
	}
	paymentOrderID, orderNo, amountCents, openID, err := a.Debts.OpenDebtPayment(ctx, id, in.RequestID)
	if err != nil {
		httpapi.Write(c, 409, 2009, "欠费状态已变化，请刷新后重试", nil)
		return
	}
	params, err := a.Provider.Prepay(ctx, payment.PrepayRequest{
		MerchantOrderNo: orderNo, OpenID: openID, AmountCents: amountCents, ExpiresAt: time.Now().Add(30 * time.Minute),
	})
	if err != nil {
		if errors.Is(err, payment.ErrInvalidPayment) {
			httpapi.Write(c, 400, 1002, "无法创建支付订单", nil)
			return
		}
		httpapi.Write(c, 503, 5003, "支付渠道暂不可用，请稍后重试", nil)
		return
	}
	if err := a.Debts.SavePrepay(ctx, paymentOrderID, params); err != nil {
		httpapi.Write(c, 503, 5003, "支付参数保存失败，请重试", nil)
		return
	}
	httpapi.OK(c, gin.H{"payment_order_id": paymentOrderID, "order_no": orderNo, "amount_cents": amountCents, "payment_params": params})
}
