package admin

import (
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A charge offer is the station-scoped, sellable copy of a template package.
// It is no longer created or edited by hand: an offer that exists without a
// matching template package is a package priced against a tariff nobody
// published. The only operator action left is taking one off a station, which
// is what disabling means here.
//
// The offer used to carry a code derived from the package, the station and the
// device, because the code column was NOT NULL with no default and an insert
// leaving it out failed outright. That made the apply path hash the device id
// down to a digest just to fit a string nobody looked up -- offers are found
// by station_id + device_id. Migration admin_db/0045 drops the column.

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

// disableChargeOffer takes one package off a station without touching the
// station's tariff or its other packages. Orders already sold against it keep
// their own copy of the terms.
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
