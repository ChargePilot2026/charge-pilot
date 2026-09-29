package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

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

// offerCode identifies a package on sale by what it is rather than by a row
// number, so it is derived from the three things that make a sale unique: the
// package, the station, and the device it is scoped to.
//
// It has to be derivable *before* the insert, because the code column is NOT
// NULL with no default and an insert that leaves it out fails outright. That is
// why this takes the target rather than the offer id.
//
// Uniqueness only has to hold within a station (uk_station_code), and a device
// id runs to 64 characters, so the device part is a short digest and not the id.
func offerCodeFor(packageTemplateID, stationID uint64, deviceID string) string {
	if deviceID == "" {
		return fmt.Sprintf("P%08d-%08d-Y", packageTemplateID, stationID)
	}
	sum := sha256.Sum256([]byte(deviceID))
	return fmt.Sprintf("P%08d-%08d-D%s", packageTemplateID, stationID, hex.EncodeToString(sum[:4]))
}

func (a ResourceAPI) registerChargeOffers(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/charge-offers", a.Auth.Require("pricing.read"), a.chargeOffers)
	r.POST("/api/v1/admin/settings/charge-offers/:id/disable", a.Auth.Require("pricing.rule.update"), a.disableChargeOffer)
}

func (a ResourceAPI) chargeOffers(c *gin.Context) {
	rows := []map[string]any{}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("charge_offer o").
		Select("o.id,o.station_id,s.name AS station_name,o.device_id,o.package_template_id," +
			"o.code,o.name,o.mode,o.price_cents,o.duration_minutes,o.min_charge_cents," +
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
func (a ResourceAPI) disableChargeOffer(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before struct{ StationID uint64 }
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
