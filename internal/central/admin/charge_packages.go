package admin

import (
	"errors"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A charge package is a reusable template that is not sellable on its own.
// Applying it to a station is what creates a charge_offer, and the offer keeps
// its own copy of the price and duration: changing the package afterwards must
// not change what a station already sells, nor what a paid order settled
// against. The two resources are therefore named separately — packages here,
// offers in the pricing package that the mini program reads.

// errAlreadyReported unwinds the transaction after the handler has already
// written its own response. Returning it keeps a refusal from being reported a
// second time by the generic failure path as an opaque database error.
var errAlreadyReported = errors.New("response already written")

type chargePackageInput struct {
	Code            string `json:"code"`
	Name            string `json:"name"`
	Mode            string `json:"mode"`
	PriceCents      int64  `json:"price_cents"`
	DurationMinutes uint16 `json:"duration_minutes"`
	Status          string `json:"status"`
	ExpectedVersion uint32 `json:"expected_version"`
}

func validChargePackage(in chargePackageInput) bool {
	return validText(in.Code, 64) && validText(in.Name, 128) &&
		in.PriceCents > 0 && in.PriceCents <= 1000000 && (in.Status == "active" || in.Status == "disabled") &&
		(in.Mode == "amount" && in.DurationMinutes == 0 || in.Mode == "package" && in.DurationMinutes > 0 && in.DurationMinutes <= 600)
}

func (a ResourceAPI) registerChargePackages(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/charge-packages", a.Auth.Require("pricing.read"), a.chargePackages)
	r.POST("/api/v1/admin/settings/charge-packages", a.Auth.Require("pricing.rule.create"), a.createChargePackage)
	r.PUT("/api/v1/admin/settings/charge-packages/:id", a.Auth.Require("pricing.rule.update"), a.updateChargePackage)
	r.POST("/api/v1/admin/settings/charge-packages/:id/apply", a.Auth.Require("pricing.rule.create"), a.applyChargePackage)
}

func (a ResourceAPI) chargePackages(c *gin.Context) {
	rows := []map[string]any{}
	// The applied stations are carried on the row so an operator can see where a
	// package is already in use before trying to apply it again, which is
	// rejected. Without this the rejection would be the only way to find out.
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("charge_package p").
		Select("p.id,p.code,p.name,p.mode,p.price_cents,p.duration_minutes,p.status,p.version," +
			"(SELECT GROUP_CONCAT(CONCAT(s.name,'（',s.id,'）') ORDER BY s.id SEPARATOR '、')" +
			" FROM charge_offer o JOIN station s ON s.id=o.station_id AND s.deleted_at IS NULL" +
			" WHERE o.package_id=p.id) AS applied_stations").
		Order("p.id").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

func (a ResourceAPI) createChargePackage(c *gin.Context) {
	var in chargePackageInput
	if !decodeResource(c, &in) {
		return
	}
	if !validChargePackage(in) || in.ExpectedVersion != 0 {
		httpapi.BadRequest(c, "充电套餐参数无效")
		return
	}
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	p := c.MustGet("admin_profile").(Profile)
	var id uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		row := map[string]any{"code": in.Code, "name": in.Name, "mode": in.Mode, "price_cents": in.PriceCents, "duration_minutes": in.DurationMinutes, "status": in.Status, "version": 1}
		if err := tx.Table("charge_package").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, p, "charge_package.create", "charge_package", id, nil, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": 1})
}

func (a ResourceAPI) updateChargePackage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in chargePackageInput
	if !decodeResource(c, &in) {
		return
	}
	if !validChargePackage(in) || in.ExpectedVersion == 0 {
		httpapi.BadRequest(c, "充电套餐参数无效")
		return
	}
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	p := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before struct {
			Version uint32
		}
		if err := tx.Table("charge_package").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&before).Error; err != nil {
			return err
		}
		if before.Version != in.ExpectedVersion {
			return errConflict
		}
		// The price and duration are deliberately not propagated to the offers
		// already applied. An offer is a snapshot of what the station sold.
		row := map[string]any{"code": in.Code, "name": in.Name, "mode": in.Mode, "price_cents": in.PriceCents, "duration_minutes": in.DurationMinutes, "status": in.Status, "version": before.Version + 1}
		if err := tx.Table("charge_package").Where("id=?", id).Updates(row).Error; err != nil {
			return err
		}
		return resourceAudit(tx, p, "charge_package.update", "charge_package", id, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": in.ExpectedVersion + 1})
}

type applyPackageInput struct {
	StationID uint64 `json:"station_id"`
}

// applyChargePackage makes a package sellable at a station by copying it into
// an offer. Applying the same package to the same station twice is refused:
// the second attempt would create a duplicate entry in the mini program's list
// with no way to tell the two apart.
func (a ResourceAPI) applyChargePackage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in applyPackageInput
	if !decodeResource(c, &in) {
		return
	}
	if in.StationID == 0 {
		httpapi.BadRequest(c, "请选择要应用到的站点")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	var offerID uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var pkg struct {
			Code            string
			Name            string
			Mode            string
			PriceCents      int64
			DurationMinutes uint16
			Status          string
		}
		if err := tx.Table("charge_package").Where("id=?", id).Take(&pkg).Error; err != nil {
			return err
		}
		// A disabled package is refused rather than applied into a station as an
		// invisible offer, which would look like a silent failure to the operator.
		if pkg.Status != "active" {
			httpapi.Write(c, 409, 1009, "套餐已停用，请先启用后再应用", nil)
			return errAlreadyReported
		}
		var station Station
		if err := tx.Where("id=? AND status='active' AND deleted_at IS NULL", in.StationID).Take(&station).Error; err != nil {
			return err
		}
		var existing int64
		if err := tx.Table("charge_offer").Where("package_id=? AND station_id=?", id, in.StationID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			httpapi.Write(c, 409, 1009, "该套餐已应用到此站点", nil)
			return errAlreadyReported
		}
		row := map[string]any{
			"station_id": in.StationID, "package_id": id,
			"code": pkg.Code, "name": pkg.Name, "mode": pkg.Mode,
			"price_cents": pkg.PriceCents, "duration_minutes": pkg.DurationMinutes,
			"status": "active", "version": 1,
		}
		if err := tx.Table("charge_offer").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&offerID).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "charge_package.apply", "charge_offer", offerID,
			nil, map[string]any{"package_id": id, "station_id": in.StationID}, c.ClientIP(), httpapi.RequestID(c))
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
