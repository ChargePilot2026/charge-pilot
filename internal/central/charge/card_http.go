package charge

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strings"
	"time"
)

type CardAPI struct {
	Store        CardStore
	Auth         identity.SessionAuthenticator
	Pricing      pricing.Store
	Scan         ScanAPI
	ServiceToken string
}

func (a CardAPI) Register(r *gin.Engine) {
	r.GET("/api/v1/user/cards", a.cards)
	r.POST("/api/v1/user/cards/:card_no/status", a.cardStatus)
	r.POST("/api/v1/internal/cards/swipe", a.swipe)
	r.GET("/api/v1/internal/cards/balance", a.balance)
}
func (a CardAPI) internal(c *gin.Context) bool {
	p := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	e := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(p[:], e[:]) != 1 {
		httpapi.Write(c, 401, 1001, "service token invalid", nil)
		return false
	}
	return true
}
func (a CardAPI) cards(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	rows := []OnlineCard{}
	if err := a.Store.DB.WithContext(c.Request.Context()).Where("user_id=? AND status<>'unbound'", user).Find(&rows).Error; err != nil {
		httpapi.Write(c, 503, 5001, "卡信息暂不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// Binding is performed by an authorized operator after verifying ownership.
// Merely knowing a printed card number cannot claim an existing user's card.
func (a CardAPI) cardStatus(c *gin.Context) {
	user, ok := a.Auth.Authenticate(c)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Status != "lost" && in.Status != "unbound" {
		httpapi.BadRequest(c, "仅可挂失或解绑自己的卡")
		return
	}
	err := a.Store.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var card OnlineCard
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("card_no=? AND user_id=? AND status<>'unbound'", c.Param("card_no"), user).Take(&card).Error; err != nil {
			return err
		}
		if err := tx.Model(&card).Update("status", in.Status).Error; err != nil {
			return err
		}
		return tx.Table("online_card_audit").Create(map[string]any{"card_id": card.ID, "actor_id": user, "action": "user_" + in.Status, "detail": "用户主动" + in.Status}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, 404, 1004, "卡不存在", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5001, "卡状态暂不可保存", nil)
		return
	}
	httpapi.OK(c, gin.H{"status": in.Status})
}
func (a CardAPI) swipe(c *gin.Context) {
	if !a.internal(c) {
		return
	}
	var in struct {
		CardNo     string    `json:"card_no"`
		EventID    string    `json:"event_id"`
		PortID     string    `json:"port_id"`
		OccurredAt time.Time `json:"occurred_at"`
	}
	if c.ShouldBindJSON(&in) != nil || !userScanCodePattern.MatchString(in.PortID) || strings.TrimSpace(in.CardNo) == "" || len(in.CardNo) > 64 || len(in.EventID) > 128 || strings.TrimSpace(in.EventID) == "" {
		httpapi.BadRequest(c, "刷卡事件、卡号、端口无效")
		return
	}
	var prior CardOperation
	found := a.Store.DB.WithContext(c.Request.Context()).Table("card_operation o").Select("o.*").Joins("JOIN online_card c ON c.id=o.card_id").Joins("JOIN card_charge s ON s.charge_order_id=o.charge_order_id").Where("o.event_id=? AND c.card_no=? AND s.port_code=?", in.EventID, in.CardNo, in.PortID).Find(&prior)
	if found.Error != nil {
		httpapi.Write(c, 503, 5001, "刷卡事件暂不可读取", nil)
		return
	}
	if found.RowsAffected > 0 {
		httpapi.OK(c, prior)
		return
	}
	if in.OccurredAt.IsZero() || time.Since(in.OccurredAt) > 30*time.Second || in.OccurredAt.After(time.Now().Add(time.Second)) {
		httpapi.Write(c, 409, 2009, "刷卡事件已过期，不创建新扣款", nil)
		return
	}
	port, status := a.Scan.lookup(c.Request.Context(), in.PortID)
	if status != http.StatusOK || port.Port == nil || !port.Port.Online {
		httpapi.Write(c, 409, 2009, "设备离线或端口无效", nil)
		return
	}
	var d struct{ ExecutionCapabilities []byte }
	if err := a.Pricing.DB.WithContext(c.Request.Context()).Table("device_meta").Where("station_id=? AND device_id=? AND deleted_at IS NULL", port.StationID, port.DeviceID).Take(&d).Error; err != nil {
		httpapi.Write(c, 503, 5001, "设备能力暂不可读取", nil)
		return
	}
	var cap pricing.Capabilities
	if json.Unmarshal(d.ExecutionCapabilities, &cap) != nil || !cap.OnlineCard || !cap.CardEventIdentity {
		httpapi.Write(c, 409, 2009, "在线卡及移开后重刷事件行为尚未验证", nil)
		return
	}
	var active int64
	if err := a.Store.DB.WithContext(c.Request.Context()).Model(&CardCharge{}).Where("active_port=?", in.PortID).Count(&active).Error; err != nil {
		httpapi.Write(c, 503, 5001, "当前刷卡订单暂不可读取", nil)
		return
	}
	// An extension uses its frozen package even if the current scheme changed.
	// The transaction rechecks the session before any debit.
	var rule pricing.Rule
	if active == 0 {
		var err error
		rule, err = a.Pricing.ActiveDeviceRule(c.Request.Context(), port.StationID, port.DeviceID)
		if err != nil {
			if errors.Is(err, pricing.ErrRuleUnavailable) {
				httpapi.Write(c, 409, 2009, "当前没有可用充电方案", nil)
			} else {
				httpapi.Write(c, 503, 5001, "方案暂不可读取", nil)
			}
			return
		}
	}
	if active == 0 && rule.Spec.Scheme != nil {
		if err := a.Pricing.CheckDeviceScheme(c.Request.Context(), port.StationID, port.DeviceID, *rule.Spec.Scheme); err != nil {
			httpapi.Write(c, 409, 2009, err.Error(), nil)
			return
		}
	}
	op, err := a.Store.Swipe(c.Request.Context(), in.CardNo, in.EventID, port, rule)
	if err != nil {
		if errors.Is(err, ErrCardOperation) || errors.Is(err, ErrPaymentIntentConflict) || errors.Is(err, gorm.ErrRecordNotFound) {
			httpapi.Write(c, 409, 2009, ErrCardOperation.Error(), gin.H{"insufficient_balance": errors.Is(err, ErrCardBalance)})
		} else {
			httpapi.Write(c, 503, 5001, "刷卡操作结果暂不可读取，请按同一事件编号查询", nil)
		}
		return
	}
	httpapi.OK(c, op)
}
