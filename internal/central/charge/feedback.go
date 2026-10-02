package charge

import (
	"encoding/json"
	"errors"
	orderpkg "github.com/ChargePilot2026/charge-pilot/internal/central/order"
	"net/url"
	"strconv"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errFeedbackExists = errors.New("order already has feedback")
	errOrderNotReady  = errors.New("order is not a completed order of this user")
)

// submitFeedback 为用户已完成的订单提交反馈。
// 事务中锁定 charge_order 后校验归属、状态和已有反馈，保证每个订单最多提交一次。
func (a UserAccountAPI) submitFeedback(c *gin.Context) {
	userID, ok := a.userID(c)
	if !ok {
		return
	}
	orderID, err := strconv.ParseUint(c.Param("order_id"), 10, 64)
	if err != nil || orderID == 0 {
		orderNo := strings.TrimSpace(c.Param("order_id"))
		if orderNo == "" || len(orderNo) > 64 {
			httpapi.BadRequest(c, "订单号无效")
			return
		}
		var order orderpkg.ChargeOrderRecord
		if err := a.UserDB.WithContext(c.Request.Context()).Select("id").Where("order_no=? AND user_id=? AND deleted_at IS NULL", orderNo, userID).Take(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				httpapi.Write(c, 404, 1004, "订单不存在", nil)
			} else {
				resourceWriteFailure(c, err)
			}
			return
		}
		orderID = order.ID
	}
	var in struct {
		Rating   int8     `json:"rating"`
		Category string   `json:"category"`
		Content  *string  `json:"content"`
		Images   []string `json:"images"`
	}
	if c.ShouldBindJSON(&in) != nil {
		httpapi.BadRequest(c, "反馈请求无效")
		return
	}
	if in.Rating < 1 || in.Rating > 5 || !oneOfStatus(in.Category, "rating complaint suggestion") {
		httpapi.BadRequest(c, "评分或反馈类型无效")
		return
	}
	if in.Content != nil && len([]rune(*in.Content)) > 2000 {
		httpapi.BadRequest(c, "反馈内容过长")
		return
	}
	if len(in.Images) > 5 {
		httpapi.BadRequest(c, "图片最多 5 张")
		return
	}
	for _, image := range in.Images {
		if !httpsImage(image) {
			httpapi.BadRequest(c, "图片链接必须为 HTTPS")
			return
		}
	}
	images, _ := json.Marshal(in.Images)

	var feedbackID uint64
	tx := a.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var order struct {
			ID       uint64
			UserID   uint64
			DeviceID string
			Status   string
		}
		// 订单表按 created_month 分区，此处按订单主键定位需要加锁的记录。
		if err := tx.Table("charge_order").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", orderID).Take(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errOrderNotReady
			}
			return err
		}
		if order.UserID != userID || !oneOfStatus(order.Status, "completed refunding refunded") {
			return errOrderNotReady
		}
		var existing int64
		if err := tx.Table("feedback").Where("order_id = ? AND deleted_at IS NULL", orderID).
			Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return errFeedbackExists
		}
		if err := tx.Table("feedback").Create(map[string]any{
			"user_id": userID, "order_id": orderID, "device_id": order.DeviceID,
			"rating": in.Rating, "category": in.Category, "content": in.Content,
			"images_json": string(images), "status": "pending",
		}).Error; err != nil {
			return err
		}
		return tx.Raw("SELECT LAST_INSERT_ID()").Scan(&feedbackID).Error
	})
	switch {
	case errors.Is(tx, errOrderNotReady):
		// 越权订单与不存在的订单使用相同 not-found 响应，避免泄露订单存在性。
		httpapi.Write(c, 404, 1004, "订单不存在或不可评价", nil)
	case errors.Is(tx, errFeedbackExists):
		httpapi.Write(c, 409, 2009, "该订单已评价", nil)
	case tx != nil:
		httpapi.Write(c, 503, 5003, "反馈提交失败，请稍后重试", nil)
	default:
		httpapi.OK(c, gin.H{"submitted": true, "feedback_id": feedbackID})
	}
}

// httpsImage 校验反馈附件使用 HTTPS 域名链接，拒绝明文 HTTP 和脚本 URL。
func httpsImage(raw string) bool {
	if len(raw) == 0 || len(raw) > 512 || strings.TrimSpace(raw) != raw {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return false
	}
	return parsed.User == nil
}
