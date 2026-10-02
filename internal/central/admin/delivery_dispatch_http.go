package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/delivery"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// DeliveryDispatchAPI 把 webhook 订阅清单、投递日志与监管报送租约暴露给
// worker 的 delivery 组件；状态读写在 central 单库事务内完成。
type DeliveryDispatchAPI struct {
	Webhook    WebhookDispatch
	Regulatory RegulatoryDispatch
	// ServiceToken 为空时全部端点返回 401，避免未配置即裸奔。
	ServiceToken string
}

func (a DeliveryDispatchAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/internal/webhook-subscriptions", a.authorized(), a.subscriptions)
	r.POST("/api/v1/internal/webhook-deliveries/record", a.authorized(), a.recordDeliveries)
	r.POST("/api/v1/internal/regulatory-reports/claim", a.authorized(), a.claimReports)
	r.POST("/api/v1/internal/regulatory-reports/finish", a.authorized(), a.finishReports)
}

func (a DeliveryDispatchAPI) authorized() gin.HandlerFunc {
	return func(c *gin.Context) {
		given := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
		wanted := sha256.Sum256([]byte(a.ServiceToken))
		if a.ServiceToken == "" || subtle.ConstantTimeCompare(given[:], wanted[:]) != 1 {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}

func (a DeliveryDispatchAPI) subscriptions(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	subs, err := a.Webhook.ActiveSubscriptions(ctx)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "webhook subscriptions unavailable", nil)
		return
	}
	if subs == nil {
		subs = []WebhookSubscription{}
	}
	httpapi.OK(c, gin.H{"items": subs})
}

type recordDeliveriesRequest struct {
	Items []struct {
		SubscriptionID uint64  `json:"subscription_id" binding:"required"`
		EventID        string  `json:"event_id" binding:"required,max=64"`
		EventType      string  `json:"event_type" binding:"required,max=128"`
		RequestBody    string  `json:"request_body" binding:"required,max=65535"`
		ResponseStatus *int    `json:"response_status"`
		ResponseBody   *string `json:"response_body"`
		Error          string  `json:"error"`
		DurationMS     int64   `json:"duration_ms" binding:"min=0"`
	} `json:"items" binding:"required,min=1,max=100"`
}

func (a DeliveryDispatchAPI) recordDeliveries(c *gin.Context) {
	var in recordDeliveriesRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "投递结果格式无效")
		return
	}
	records := make([]delivery.DeliveryRecord, 0, len(in.Items))
	for _, item := range in.Items {
		if item.ResponseStatus != nil && (*item.ResponseStatus < 100 || *item.ResponseStatus > 599) {
			httpapi.BadRequest(c, "response_status 无效")
			return
		}
		if len(item.Error) > 255 {
			httpapi.BadRequest(c, "error 超长")
			return
		}
		records = append(records, delivery.DeliveryRecord{
			SubscriptionID: item.SubscriptionID,
			EventID:        item.EventID,
			EventType:      item.EventType,
			RequestBody:    item.RequestBody,
			ResponseStatus: item.ResponseStatus,
			ResponseBody:   item.ResponseBody,
			Error:          item.Error,
			DurationMS:     item.DurationMS,
		})
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	if err := a.Webhook.RecordDeliveries(ctx, records); err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "投递日志暂时不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"recorded": len(records)})
}

type claimReportsRequest struct {
	Limit int `json:"limit" binding:"min=1,max=20"`
}

func (a DeliveryDispatchAPI) claimReports(c *gin.Context) {
	var in claimReportsRequest
	if err := c.ShouldBindJSON(&in); err != nil || in.Limit == 0 {
		if err == nil {
			in.Limit = 20
		} else {
			httpapi.BadRequest(c, "claim 请求格式无效")
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	items, err := a.Regulatory.Claim(ctx, in.Limit)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "监管报送领取暂时不可用", nil)
		return
	}
	if items == nil {
		items = []ClaimedReport{}
	}
	httpapi.OK(c, gin.H{"items": items})
}

type finishReportsRequest struct {
	Items []FinishItem `json:"items" binding:"required,min=1,max=100"`
}

func (a DeliveryDispatchAPI) finishReports(c *gin.Context) {
	var in finishReportsRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "finish 请求格式无效")
		return
	}
	for _, item := range in.Items {
		if len(item.Error) > 512 {
			httpapi.BadRequest(c, "error 超长")
			return
		}
		if !item.Delivered && item.Mode == "" {
			httpapi.BadRequest(c, "失败回执缺少投递方式")
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	result, err := a.Regulatory.Finish(ctx, in.Items)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "监管报送状态暂时不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"finished": result.Finished, "lost": result.Lost})
}
