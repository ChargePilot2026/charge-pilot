package admin

import (
	"errors"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A package template is a prepaid cap a rider can pick. It is deliberately not
// part of a pricing template: the cap settles on its own price, so it stays
// valid whichever tariff is running, and one tariff can be paired with several
// different package sets. Applying one to a station or a device copies it into
// a charge_offer, which is what the mini program reads.

type packageTemplateInput struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	PriceCents      int64  `json:"price_cents"`
	DurationMinutes uint16 `json:"duration_minutes"`
	MinChargeCents  int64  `json:"min_charge_cents"`
	ShowRemark      bool   `json:"show_remark"`
	CardDefault     bool   `json:"card_default"`
	SortOrder       int    `json:"sort_order"`
	Status          string `json:"status"`
	ExpectedVersion uint32 `json:"expected_version"`
}

// validPackageTemplate keeps the package vocabulary identical to what
// settlement can actually charge. A "package" kind without a duration, or an
// amount without a positive cap, would be sellable and unpriceable.
func validPackageTemplate(in packageTemplateInput) bool {
	if !validText(in.Name, 64) || in.Status != "active" && in.Status != "disabled" {
		return false
	}
	if in.MinChargeCents < 0 || in.MinChargeCents > 1000000 || in.SortOrder < 0 || in.SortOrder > 10000 {
		return false
	}
	switch in.Kind {
	case "amount":
		return in.PriceCents > 0 && in.PriceCents <= 1000000 && in.DurationMinutes == 0
	case "package":
		// A duration package is settled by the tariff, so it carries no cap of
		// its own. Giving it one would silently cap a rider who keeps charging.
		return in.PriceCents == 0 && in.DurationMinutes > 0 && in.DurationMinutes <= 600
	default:
		return false
	}
}

func packageFields(in packageTemplateInput) map[string]any {
	return map[string]any{
		"name": strings.TrimSpace(in.Name), "kind": in.Kind,
		"price_cents": in.PriceCents, "duration_minutes": in.DurationMinutes,
		"min_charge_cents": in.MinChargeCents,
		"show_remark":      in.ShowRemark, "card_default": in.CardDefault, "status": in.Status,
		// The display order is part of what a rider sees, so it is written
		// rather than validated and dropped.
		"sort_order": in.SortOrder,
	}
}

func (a ResourceAPI) registerPackageTemplates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/package-templates", a.Auth.Require("pricing.read"), a.packageTemplates)
	r.POST("/api/v1/admin/settings/package-templates", a.Auth.Require("pricing.rule.create"), a.createPackageTemplate)
	r.PUT("/api/v1/admin/settings/package-templates/:id", a.Auth.Require("pricing.rule.update"), a.updatePackageTemplate)
	r.POST("/api/v1/admin/settings/package-templates/:id/disable", a.Auth.Require("pricing.rule.update"), a.disablePackageTemplate)
	r.POST("/api/v1/admin/settings/package-templates/:id/apply", a.Auth.Require("pricing.rule.create"), a.applyPackageTemplate)
}

func (a ResourceAPI) packageTemplates(c *gin.Context) {
	rows := []map[string]any{}
	// Where a package is already on sale rides along on the row, so an operator
	// can see that before applying it again to the same target, which is
	// refused.
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_package_template p").
		Select("p.id,p.name,p.kind,p.price_cents,p.duration_minutes,p.min_charge_cents," +
			"p.show_remark,p.card_default,p.sort_order,p.status,p.version," +
			"(SELECT GROUP_CONCAT(DISTINCT CONCAT(IF(o.device_id IS NULL,'全场','设备 '),o.device_id) ORDER BY o.station_id SEPARATOR '、')" +
			" FROM charge_offer o WHERE o.package_template_id=p.id AND o.status='active' AND o.deleted_at IS NULL) AS applied_targets").
		Where("p.deleted_at IS NULL").Order("p.sort_order,p.id").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

func (a ResourceAPI) createPackageTemplate(c *gin.Context) {
	var in packageTemplateInput
	if !decodeResource(c, &in) {
		return
	}
	if !validPackageTemplate(in) || in.ExpectedVersion != 0 {
		httpapi.BadRequest(c, "套餐模板参数无效：按金额须填金额，按时长须填时长且不填金额")
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	var id uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		row := packageFields(in)
		row["version"] = 1
		if err := tx.Table("pricing_package_template").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "pricing.package_template.create", "pricing_package_template", id, nil, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": 1})
}

func (a ResourceAPI) updatePackageTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in packageTemplateInput
	if !decodeResource(c, &in) {
		return
	}
	if !validPackageTemplate(in) || in.ExpectedVersion == 0 {
		httpapi.BadRequest(c, "套餐模板参数无效：按金额须填金额，按时长须填时长且不填金额")
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before struct{ Version uint32 }
		if err := tx.Table("pricing_package_template").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if before.Version != in.ExpectedVersion {
			return errConflict
		}
		row := packageFields(in)
		row["version"] = before.Version + 1
		if err := tx.Table("pricing_package_template").Where("id=?", id).Updates(row).Error; err != nil {
			return err
		}
		// Offers already on sale keep their own copy: changing a package must
		// not change what a station is already selling, nor what a paid order
		// settled against.
		return resourceAudit(tx, actor, "pricing.package_template.update", "pricing_package_template", id, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": in.ExpectedVersion + 1})
}

// disablePackageTemplate stops a package being applied again. Offers already on
// sale stay where they are; a yard that is selling it can keep selling.
func (a ResourceAPI) disablePackageTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before struct{ Version uint32 }
		if err := tx.Table("pricing_package_template").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("pricing_package_template").Where("id=?", id).
			Updates(map[string]any{"status": "disabled", "version": before.Version + 1}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "pricing.package_template.disable", "pricing_package_template", id, before,
			map[string]any{"status": "disabled"}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

type applyPackageInput struct {
	StationID uint64 `json:"station_id"`
	DeviceID  string `json:"device_id"`
}

// applyPackageTemplate puts one package on sale at a station or on one device.
// The same package may sit both yard-wide and on a specific device; an offer is
// only refused when that exact target already sells it.
func (a ResourceAPI) applyPackageTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in applyPackageInput
	if !decodeResource(c, &in) {
		return
	}
	if in.StationID == 0 || in.DeviceID != "" && !deviceIDPattern.MatchString(in.DeviceID) {
		httpapi.BadRequest(c, "请选择站点")
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	var offerID uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var pkg struct {
			Name            string
			Kind            string
			PriceCents      int64
			DurationMinutes uint16
			MinChargeCents  int64
			ShowRemark      bool
			CardDefault     bool
			Status          string
		}
		if err := tx.Table("pricing_package_template").Where("id=? AND deleted_at IS NULL", id).Take(&pkg).Error; err != nil {
			return err
		}
		// A disabled package is refused rather than pushed into a yard as an
		// offer nobody can see, which would read as a silent failure.
		if pkg.Status != "active" {
			httpapi.Write(c, 409, 1009, "套餐模板已停用，请先启用后再应用", nil)
			return errAlreadyReported
		}
		var station Station
		if err := tx.Where("id=? AND status='active' AND deleted_at IS NULL", in.StationID).Take(&station).Error; err != nil {
			return err
		}
		if in.DeviceID != "" {
			var known int64
			if err := tx.Table("device_meta").
				Where("device_id=? AND station_id=? AND deleted_at IS NULL", in.DeviceID, in.StationID).
				Count(&known).Error; err != nil {
				return err
			}
			if known == 0 {
				httpapi.Write(c, 404, 1004, "该设备不属于此站点", nil)
				return errAlreadyReported
			}
		}
		var existing int64
		query := tx.Table("charge_offer").Where("package_template_id=? AND station_id=? AND deleted_at IS NULL", id, in.StationID)
		if in.DeviceID == "" {
			query = query.Where("device_id IS NULL")
		} else {
			query = query.Where("device_id=?", in.DeviceID)
		}
		if err := query.Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			httpapi.Write(c, 409, 1009, "该套餐已在此处上架", nil)
			return errAlreadyReported
		}
		row := map[string]any{
			"station_id": in.StationID, "device_id": nullableDevice(in.DeviceID),
			"package_template_id": id, "name": pkg.Name, "mode": pkg.Kind,
			"price_cents": pkg.PriceCents, "duration_minutes": pkg.DurationMinutes,
			"min_charge_cents": pkg.MinChargeCents, "show_remark": pkg.ShowRemark,
			"card_default": pkg.CardDefault, "status": "active", "version": 1,
		}
		if err := tx.Table("charge_offer").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&offerID).Error; err != nil {
			return err
		}
		// The code is derived from the offer id, which is unique, so a code can
		// be written in the same insert rather than patched in afterwards.
		if err := tx.Table("charge_offer").Where("id=?", offerID).
			Update("code", offerCodeFor(id, offerID)).Error; err != nil {
			return err
		}
		row["code"] = offerCodeFor(id, offerID)
		return resourceAudit(tx, actor, "pricing.package_template.apply", "charge_offer", offerID,
			nil, map[string]any{"package_template_id": id, "station_id": in.StationID, "device_id": in.DeviceID},
			c.ClientIP(), httpapi.RequestID(c))
	})
	if errors.Is(err, errAlreadyReported) {
		return
	}
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"offer_id": offerID})
}
