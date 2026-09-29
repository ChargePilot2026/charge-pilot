package admin

import (
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type chargeOfferInput struct {
	StationID       uint64 `json:"station_id"`
	Code            string `json:"code"`
	Name            string `json:"name"`
	Mode            string `json:"mode"`
	PriceCents      int64  `json:"price_cents"`
	DurationMinutes uint16 `json:"duration_minutes"`
	Status          string `json:"status"`
	ExpectedVersion uint32 `json:"expected_version"`
}

func validChargeOffer(in chargeOfferInput) bool {
	return in.StationID != 0 && validText(in.Code, 64) && validText(in.Name, 128) &&
		in.PriceCents > 0 && in.PriceCents <= 1000000 && (in.Status == "active" || in.Status == "disabled") &&
		(in.Mode == "amount" && in.DurationMinutes == 0 || in.Mode == "package" && in.DurationMinutes > 0 && in.DurationMinutes <= 600)
}

func (a ResourceAPI) registerChargeOffers(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/charge-offers", a.Auth.Require("pricing.read"), a.chargeOffers)
	r.POST("/api/v1/admin/settings/charge-offers", a.Auth.Require("pricing.rule.create"), a.createChargeOffer)
	r.PUT("/api/v1/admin/settings/charge-offers/:id", a.Auth.Require("pricing.rule.update"), a.updateChargeOffer)
}

func (a ResourceAPI) chargeOffers(c *gin.Context) {
	rows := []map[string]any{}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("charge_offer o").
		Select("o.id,o.station_id,s.name AS station_name,o.code,o.name,o.mode,o.price_cents,o.duration_minutes,o.status,o.version").
		Joins("JOIN station s ON s.id=o.station_id AND s.deleted_at IS NULL").Order("o.station_id,o.mode,o.price_cents,o.id").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

func (a ResourceAPI) createChargeOffer(c *gin.Context) {
	var in chargeOfferInput
	if !decodeResource(c, &in) {
		return
	}
	if !validChargeOffer(in) || in.ExpectedVersion != 0 {
		httpapi.BadRequest(c, "充电方案参数无效")
		return
	}
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	p := c.MustGet("admin_profile").(Profile)
	var id uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var station Station
		if err := tx.Where("id=? AND status='active' AND deleted_at IS NULL", in.StationID).Take(&station).Error; err != nil {
			return err
		}
		row := map[string]any{"station_id": in.StationID, "code": in.Code, "name": in.Name, "mode": in.Mode, "price_cents": in.PriceCents, "duration_minutes": in.DurationMinutes, "status": in.Status, "version": 1}
		if err := tx.Table("charge_offer").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, p, "charge_offer.create", "charge_offer", id, nil, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": 1})
}

func (a ResourceAPI) updateChargeOffer(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in chargeOfferInput
	if !decodeResource(c, &in) {
		return
	}
	if !validChargeOffer(in) || in.ExpectedVersion == 0 {
		httpapi.BadRequest(c, "充电方案参数无效")
		return
	}
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	p := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before struct {
			StationID uint64
			Version   uint32
		}
		if err := tx.Table("charge_offer").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&before).Error; err != nil {
			return err
		}
		if before.StationID != in.StationID || before.Version != in.ExpectedVersion {
			return errConflict
		}
		row := map[string]any{"code": in.Code, "name": in.Name, "mode": in.Mode, "price_cents": in.PriceCents, "duration_minutes": in.DurationMinutes, "status": in.Status, "version": before.Version + 1}
		if err := tx.Table("charge_offer").Where("id=?", id).Updates(row).Error; err != nil {
			return err
		}
		return resourceAudit(tx, p, "charge_offer.update", "charge_offer", id, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": in.ExpectedVersion + 1})
}
