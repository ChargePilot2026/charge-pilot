package admin

import (
	"fmt"
	cardpkg "github.com/ChargePilot2026/charge-pilot/internal/central/card"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
)

func (a ResourceAPI) registerOnlineCards(r *gin.Engine) {
	r.GET("/api/v1/admin/online-cards", a.Auth.Require("user.read"), a.onlineCards)
	r.POST("/api/v1/admin/online-cards/bind", a.Auth.Require("online_card.manage"), a.bindOnlineCard)
	r.POST("/api/v1/admin/online-cards/:id/status", a.Auth.Require("online_card.manage"), a.onlineCardStatus)
}
func (a ResourceAPI) onlineCards(c *gin.Context) {
	rows := []cardpkg.OnlineCard{}
	if err := a.Store.UserDB.WithContext(c.Request.Context()).Order("id DESC").Limit(500).Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}
func (a ResourceAPI) bindOnlineCard(c *gin.Context) {
	var in struct {
		CardNo string            `json:"card_no"`
		UserID httpapi.DecimalID `json:"user_id"`
		Reason string            `json:"reason"`
	}
	if !decodeResource(c, &in) {
		return
	}
	userID := uint64(in.UserID)
	in.CardNo = strings.TrimSpace(in.CardNo)
	if !cardpkg.CanonicalCardNumber(in.CardNo) || userID == 0 || !validText(strings.TrimSpace(in.Reason), 500) {
		httpapi.BadRequest(c, "请填写32位十进制卡号、用户及核验绑定依据")
		return
	}
	var id uint64
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var user struct{ Status string }
		if err := tx.Table("user").Where("id=? AND status='active' AND deleted_at IS NULL", userID).Take(&user).Error; err != nil {
			return err
		}
		var card cardpkg.OnlineCard
		found := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("card_no=?", in.CardNo).Find(&card)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected > 0 {
			id = card.ID
			if card.Status != "unbound" {
				if card.UserID == userID && card.Status == "active" {
					return nil
				}
				return fmt.Errorf("%w: 卡已绑定，须先由原用户解绑", errConflict)
			}
			if err := tx.Model(&card).Where("id=?", id).Updates(map[string]any{"user_id": userID, "status": "active"}).Error; err != nil {
				return err
			}
		} else {
			card = cardpkg.OnlineCard{CardNo: in.CardNo, UserID: userID, Status: "active"}
			if err := tx.Create(&card).Error; err != nil {
				return err
			}
			id = card.ID
		}
		return tx.Table("online_card_audit").Create(map[string]any{"card_id": id, "actor_id": c.MustGet("admin_profile").(Profile).ID, "action": "bind", "detail": in.Reason}).Error
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}
func (a ResourceAPI) onlineCardStatus(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !oneOf(in.Status, "active lost disabled unbound") || in.Status == "" || !validText(in.Reason, 500) {
		httpapi.BadRequest(c, "卡状态与依据无效")
		return
	}
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var card cardpkg.OnlineCard
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&card).Error; err != nil {
			return err
		}
		if card.Status == "unbound" && in.Status != "unbound" {
			return errConflict
		}
		if err := tx.Model(&card).Update("status", in.Status).Error; err != nil {
			return err
		}
		return tx.Table("online_card_audit").Create(map[string]any{"card_id": id, "actor_id": c.MustGet("admin_profile").(Profile).ID, "action": in.Status, "detail": in.Reason}).Error
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}
