package admin

import (
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registerPricing keeps only the station-scoped views. Creating a rule is no
// longer a direct write: a template is created first and then applied to a
// station, so the endpoints that bound a station at creation time are gone.
//
// registerPricing 只保留站点维度的只读与停用能力。计费规则已不再直接创建：先建
// 计费模板，再把它应用到某个站点，因此那些"创建时就要绑定站点"的旧接口已经移除。
func (a ResourceAPI) registerPricing(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/charge-rules", a.Auth.Require("pricing.read"), a.pricingRules)
	r.GET("/api/v1/admin/settings/pricing-rule-templates/:id/usage", a.Auth.Require("pricing.read"), a.pricingTemplateUsage)
	r.POST("/api/v1/admin/settings/charge-rules/:id/disable", a.Auth.Require("pricing.rule.update"), a.disablePricing)
}

// pricingRules 返回站点维度生效的计费规则列表，左连站点带出站点名（站点被软删除时
// 为空）。这里只读：规则的增删改都要通过"模板 + 应用到站点"完成。
func (a ResourceAPI) pricingRules(c *gin.Context) {
	rows := []map[string]any{}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_rule r").Select("r.id,r.name,r.station_id,s.name AS station_name,r.template_id,r.spec_json,r.channel,r.version,r.status,r.effective_from,r.effective_to").Joins("LEFT JOIN station s ON s.id=r.station_id AND s.deleted_at IS NULL").Where("r.deleted_at IS NULL").Order("r.id DESC").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

// pricingTemplateUsage reports which stations a template is currently running,
// so an operator can see the blast radius before disabling or editing it.
//
// pricingTemplateUsage 列出某个模板当前铺在哪些站点（含已停用的历史版本），运营在
// 停用或改模板之前先看影响面。
func (a ResourceAPI) pricingTemplateUsage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	rows := []map[string]any{}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_rule r").
		Select("r.id AS rule_id,r.station_id,s.name AS station_name,r.version,r.status").
		Joins("JOIN station s ON s.id=r.station_id AND s.deleted_at IS NULL").
		Where("r.template_id=? AND r.deleted_at IS NULL", id).Order("r.station_id,r.version DESC").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

// disablePricing 停用一条计费规则。只 UPDATE 处于 active 的行；命中 0 行说明它已经
// 是停用状态，按幂等处理直接当成功返回，不写审计。事务内先对所属站点加 UPDATE 行锁，
// 避免与站点侧并发操作交叉。已按旧规则产生的订单不受影响。
func (a ResourceAPI) disablePricing(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	// 只需要规则 ID、归属站点和当前状态。
	var row struct {
		ID        uint64 // 规则主键。
		StationID uint64 // 规则所属站点 ID；0 表示未绑定站点。
		Status    string // 规则当前状态：active 生效中、disabled 已停用。
	}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_rule").Where("id=? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if row.StationID != 0 {
			var station Station
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", row.StationID).Take(&station).Error; err != nil {
				return err
			}
		}
		result := tx.Table("pricing_rule").Where("id=? AND status='active' AND deleted_at IS NULL", id).Update("status", "disabled")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		return resourceAudit(tx, p, "pricing.rule.disable", "pricing_rule", id, gin.H{"status": "active"}, gin.H{"status": "disabled"}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "status": "disabled"})
}
