package admin

import (
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 充电套餐是模板套餐挂在站点上、可以对外卖的那一份副本。
// 它已经不再由人工新建或编辑：
// 一个没有对应模板套餐的套餐，等于有人拿一份谁都没发布过的计费口径在定价。
// 运营侧剩下的唯一动作就是把某个套餐从某个站点下架，
// 这就是这里"停用"的含义。
//
// 套餐过去会带一个由模板、站点和设备算出来的编码，
// 因为 code 列是 NOT NULL 又没有默认值，插入时漏填它会直接失败。
// 于是下发链路不得不把设备号哈希成一个摘要，只为塞进一个没人会查的字符串——
// 套餐是靠 station_id + device_id 找到的。
// 迁移 admin_db/0045 已经删掉了这一列。

// registerChargeOffers 挂载充电套餐的查看与下架两个接口，没有新建/编辑路由：套餐必须
// 由套餐模板下发，运营侧唯一剩下的动作就是把某个套餐从某个站点下架。
func (a ResourceAPI) registerChargeOffers(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/charge-offers", a.Auth.Require("pricing.read"), a.chargeOffers)
	r.POST("/api/v1/admin/settings/charge-offers/:id/disable", a.Auth.Require("pricing.rule.update"), a.disableChargeOffer)
}

// chargeOffers 按站点/设备列出充电套餐（charge_offer），内连接未删除的站点，
// 按站点、设备、计费方式、价格、主键排序便于人工比对。返回里没有 code 列——套餐靠
// station_id + device_id 定位，不靠编码。
func (a ResourceAPI) chargeOffers(c *gin.Context) {
	rows := []map[string]any{}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("charge_offer o").
		Select("o.id,o.station_id,s.name AS station_name,o.device_id,o.package_template_id," +
			"o.name,o.mode,o.price_cents,o.duration_minutes,o.min_charge_cents," +
			"o.show_remark,o.card_default,o.status,o.version").
		Joins("JOIN station s ON s.id=o.station_id AND s.deleted_at IS NULL").
		Where("o.deleted_at IS NULL").
		Order("o.station_id,o.device_id,o.mode,o.price_cents,o.id").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

// disableChargeOffer 把一个套餐从站点下架，
// 不碰该站点的计费规则，也不动它的其它套餐。
// 已经按它卖出的订单，继续按各自留存的那份条款走。
//
// disableChargeOffer 把一个套餐从站点下架：事务内先对目标行加 UPDATE 行锁且只锁
// active 的记录（已经是 disabled 的会因查不到而回 404），确认拿到之后才改状态并写
// 审计。站点自身的计费规则和该站点的其他套餐都不受影响，已经卖出的订单继续按它们
// 各自留存的那份条款走。
func (a ResourceAPI) disableChargeOffer(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// 审计里只需要记录改之前的归属站点。
		var before struct{ StationID uint64 } // 套餐所属站点 ID。
		if err := tx.Table("charge_offer").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND status='active' AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("charge_offer").Where("id=?", id).
			Updates(map[string]any{"status": "disabled"}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "charge_offer.disable", "charge_offer", id, before,
			map[string]any{"status": "disabled"}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "status": "disabled"})
}
