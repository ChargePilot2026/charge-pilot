package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a ResourceAPI) registerChargingSchemes(r *gin.Engine) {
	p := "/api/v1/admin/settings/charging-schemes"
	r.GET(p, a.Auth.Require("pricing.read"), a.chargingSchemes)
	r.POST(p, a.Auth.Require("pricing.rule.create"), a.saveChargingScheme)
	r.PUT(p+"/:id", a.Auth.Require("pricing.rule.update"), a.saveChargingScheme)
	r.POST(p+"/:id/status", a.Auth.Require("pricing.rule.update"), a.chargingSchemeStatus)
	r.DELETE(p+"/:id", a.Auth.Require("pricing.rule.update"), a.deleteChargingScheme)
	r.POST(p+"/apply", a.Auth.Require("pricing.rule.create"), a.applyChargingScheme)
	r.POST(p+"/preview", a.Auth.Require("pricing.read"), a.previewChargingScheme)
	r.GET("/api/v1/admin/stations/:id/charging-scheme", a.Auth.Require("pricing.read"), a.effectiveChargingScheme)
	r.POST("/api/v1/admin/stations/:id/charging-scheme/inherit", a.Auth.Require("pricing.rule.update"), a.inheritChargingScheme)
	r.GET("/api/v1/admin/settings/device-capabilities", a.Auth.Require("pricing.read"), a.executionCapabilities)
}

type schemeInput struct {
	Scheme          pricing.Scheme `json:"scheme"`
	ExpectedVersion uint32         `json:"expected_version"`
}

func (a ResourceAPI) chargingSchemes(c *gin.Context) {
	rows := []pricingTemplateRow{}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_template").Where("deleted_at IS NULL").Order("id DESC").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	items := []gin.H{}
	for _, r := range rows {
		var spec pricing.Spec
		if json.Unmarshal(r.SpecJSON, &spec) != nil || spec.Scheme == nil {
			continue
		}
		items = append(items, gin.H{"id": r.ID, "scheme": spec.Scheme, "version": r.Version, "status": r.Status})
	}
	httpapi.OK(c, gin.H{"items": items, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}
func (a ResourceAPI) saveChargingScheme(c *gin.Context) {
	var in schemeInput
	if !decodeResource(c, &in) {
		return
	}
	in.Scheme = in.Scheme.Normalized()
	in.Scheme.Name = strings.TrimSpace(in.Scheme.Name)
	if err := in.Scheme.Validate(); err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	id := uint64(0)
	if c.Request.Method == "PUT" {
		var ok bool
		id, ok = pathID(c)
		if !ok {
			return
		}
	}
	raw, _ := json.Marshal(in.Scheme.SpecFor(in.Scheme.Packages[0]))
	display, _ := json.Marshal(in.Scheme.Display)
	version := uint32(1)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before pricingTemplateRow
		if id > 0 {
			if err := tx.Table("pricing_template").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
				return err
			}
			if before.Version != in.ExpectedVersion {
				return errConflict
			}
			version = before.Version + 1
		}
		row := map[string]any{"name": in.Scheme.Name, "remark": in.Scheme.Remark, "spec_json": string(raw), "display_json": string(display), "version": version}
		if id == 0 {
			row["status"] = "active"
			if err := tx.Table("pricing_template").Create(row).Error; err != nil {
				return err
			}
			if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
				return err
			}
		} else if err := tx.Table("pricing_template").Where("id=?", id).Updates(row).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "charging_scheme.save", "pricing_template", id, before, in.Scheme, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": version})
}
func (a ResourceAPI) chargingSchemeStatus(c *gin.Context) { a.changeChargingScheme(c, false) }
func (a ResourceAPI) deleteChargingScheme(c *gin.Context) { a.changeChargingScheme(c, true) }
func (a ResourceAPI) changeChargingScheme(c *gin.Context, remove bool) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion uint32 `json:"expected_version"`
		Status          string `json:"status"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !remove && in.Status != "active" && in.Status != "disabled" {
		httpapi.BadRequest(c, "状态无效")
		return
	}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before pricingTemplateRow
		if err := tx.Table("pricing_template").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if in.ExpectedVersion != before.Version {
			return errConflict
		}
		row := map[string]any{"version": before.Version + 1, "status": in.Status}
		if remove {
			row["status"] = "disabled"
			row["deleted_at"] = gorm.Expr("UTC_TIMESTAMP(3)")
		}
		if err := tx.Table("pricing_template").Where("id=?", id).Updates(row).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "charging_scheme.status", "pricing_template", id, before, in, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

type applySchemeInput struct {
	RequestID       string          `json:"request_id"`
	StationID       uint64          `json:"station_id"`
	DeviceID        string          `json:"device_id"`
	TemplateID      uint64          `json:"template_id"`
	TemplateVersion uint32          `json:"template_version"`
	ExpectedVersion uint32          `json:"expected_version"`
	Scheme          *pricing.Scheme `json:"scheme,omitempty"`
}

func (a ResourceAPI) applyChargingScheme(c *gin.Context) {
	var in applySchemeInput
	if !decodeResource(c, &in) {
		return
	}
	if uuid.Validate(in.RequestID) != nil || in.StationID == 0 || in.DeviceID != "" && !deviceIDPattern.MatchString(in.DeviceID) || (in.TemplateID == 0) == (in.Scheme == nil) {
		httpapi.BadRequest(c, "请选择模板或完整方案，并提供请求编号与站点")
		return
	}
	if !a.requirePricingTargetScope(c, in.StationID, in.DeviceID) {
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	var id uint64
	var version uint32
	replayed := false
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// Lock the target before checking the receipt so concurrent identical
		// requests serialize and both return the first published result.
		var station Station
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", in.StationID).Take(&station).Error; err != nil {
			return err
		}
		var receipt struct {
			ActorID     uint64
			PayloadHash string
			RuleID      uint64
			Version     uint32
		}
		found := tx.Table("pricing_publication").Where("request_id=?", in.RequestID).Find(&receipt)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected > 0 {
			if receipt.ActorID != actor.ID || receipt.PayloadHash != hash {
				return errConflict
			}
			id, version, replayed = receipt.RuleID, receipt.Version, true
			return nil
		}
		scheme := in.Scheme
		if in.TemplateID > 0 {
			var tmpl pricingTemplateRow
			if err := tx.Table("pricing_template").Clauses(clause.Locking{Strength: "SHARE"}).Where("id=? AND status='active' AND deleted_at IS NULL", in.TemplateID).Take(&tmpl).Error; err != nil {
				return err
			}
			if tmpl.Version != in.TemplateVersion {
				return errConflict
			}
			var spec pricing.Spec
			if json.Unmarshal(tmpl.SpecJSON, &spec) != nil || spec.Scheme == nil {
				return pricing.ErrInvalidPricing
			}
			scheme = spec.Scheme
		}
		normalized := scheme.Normalized()
		scheme = &normalized
		if err := scheme.Validate(); err != nil {
			return fmt.Errorf("%w: %s", errConflict, err)
		}
		if err := checkSchemeTargets(tx, in.StationID, in.DeviceID, *scheme); err != nil {
			return fmt.Errorf("%w: %s", errConflict, err)
		}
		var current struct{ Version uint32 }
		q := ruleScope(in.StationID, in.DeviceID)(tx.Table("pricing_rule"))
		if err := q.Order("version DESC,id DESC").Limit(1).Find(&current).Error; err != nil {
			return err
		}
		if current.Version != in.ExpectedVersion {
			return errConflict
		}
		version = current.Version + 1
		if err := ruleScope(in.StationID, in.DeviceID)(tx.Table("pricing_rule")).Where("status='active'").Update("status", "disabled").Error; err != nil {
			return err
		}
		snapshot, _ := json.Marshal(scheme.SpecFor(scheme.Packages[0]))
		row := map[string]any{"name": scheme.Name, "station_id": in.StationID, "device_id": nullableDevice(in.DeviceID), "template_id": nil, "spec_json": string(snapshot), "channel": "default", "version": version, "status": "active"}
		if in.TemplateID > 0 {
			row["template_id"] = in.TemplateID
		}
		if err := tx.Table("pricing_rule").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		if err := tx.Table("pricing_publication").Create(map[string]any{"request_id": in.RequestID, "actor_id": actor.ID, "payload_hash": hash, "rule_id": id, "version": version}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "charging_scheme.apply", "pricing_rule", id, nil, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": version, "replayed": replayed})
}

func checkSchemeTargets(tx *gorm.DB, station uint64, device string, scheme pricing.Scheme) error {
	var rows []struct {
		DeviceID        string
		ProtocolAdapter string
	}
	q := tx.Table("device_meta").Where("station_id=? AND deleted_at IS NULL", station)
	if device != "" {
		q = q.Where("device_id=?", device)
	} else {
		q = q.Where("device_id NOT IN (SELECT device_id FROM pricing_rule WHERE station_id=? AND device_id IS NOT NULL AND "+effectiveRuleSQL+")", station)
	}
	if err := q.Clauses(clause.Locking{Strength: "SHARE"}).Find(&rows).Error; err != nil {
		return err
	}
	if device != "" && len(rows) != 1 {
		return gorm.ErrRecordNotFound
	}
	for _, d := range rows {
		cap, err := pricing.ProtocolCapabilities(d.ProtocolAdapter)
		if err != nil {
			return fmt.Errorf("设备%s：%s", d.DeviceID, err)
		}
		if err := scheme.ValidateCapabilities(cap); err != nil {
			return fmt.Errorf("设备%s：%s", d.DeviceID, err)
		}
	}
	return nil
}

func (a ResourceAPI) effectiveChargingScheme(c *gin.Context) {
	station, ok := pathID(c)
	if !ok {
		return
	}
	device := c.Query("device_id")
	if !a.requirePricingTargetScope(c, station, device) {
		return
	}
	db := a.Store.AdminDB.WithContext(c.Request.Context())
	if device != "" {
		var count int64
		if err := db.Table("device_meta").Where("station_id=? AND device_id=? AND deleted_at IS NULL", station, device).Count(&count).Error; err != nil {
			resourceFailure(c, err)
			return
		}
		if count != 1 {
			resourceFailure(c, gorm.ErrRecordNotFound)
			return
		}
	}
	var current struct{ Version uint32 }
	if err := ruleScope(station, device)(db.Table("pricing_rule")).Order("version DESC,id DESC").Limit(1).Find(&current).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	rule, err := (pricing.Store{DB: db}).ActiveDeviceRule(c.Request.Context(), station, device)
	if errors.Is(err, pricing.ErrRuleUnavailable) {
		httpapi.OK(c, gin.H{"scheme": nil, "version": current.Version, "inherited": false})
		return
	}
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"scheme": rule.Spec.Scheme, "version": current.Version, "rule_id": rule.ID, "inherited": device != "" && rule.DeviceID == "", "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

func (a ResourceAPI) inheritChargingScheme(c *gin.Context) {
	station, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		DeviceID        string `json:"device_id"`
		ExpectedVersion uint32 `json:"expected_version"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if in.DeviceID == "" || !deviceIDPattern.MatchString(in.DeviceID) {
		httpapi.BadRequest(c, "请选择设备")
		return
	}
	if !a.requirePricingTargetScope(c, station, in.DeviceID) {
		return
	}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var s Station
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", station).Take(&s).Error; err != nil {
			return err
		}
		rule, err := (pricing.Store{DB: tx}).ActiveStationRule(c.Request.Context(), station)
		if err != nil {
			return err
		}
		if err := checkSchemeTargets(tx, station, in.DeviceID, *rule.Spec.Scheme); err != nil {
			return fmt.Errorf("%w: %s", errConflict, err)
		}
		var current struct{ Version uint32 }
		if err := ruleScope(station, in.DeviceID)(tx.Table("pricing_rule")).Order("version DESC,id DESC").Limit(1).Find(&current).Error; err != nil {
			return err
		}
		if current.Version != in.ExpectedVersion {
			return errConflict
		}
		if err := ruleScope(station, in.DeviceID)(tx.Table("pricing_rule")).Where("status='active'").Update("status", "disabled").Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "charging_scheme.inherit", "station", station, nil, in, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"inherited": true})
}

func (a ResourceAPI) previewChargingScheme(c *gin.Context) {
	var in struct {
		Scheme      pricing.Scheme      `json:"scheme"`
		PackageID   uint64              `json:"package_id"`
		Meter       pricing.ActualMeter `json:"meter"`
		Scenario    string              `json:"scenario"`
		WalletCents int64               `json:"wallet_cents"`
	}
	if !decodeResource(c, &in) {
		return
	}
	result, err := in.Scheme.PreviewScenario(in.PackageID, in.Meter, in.Scenario, in.WalletCents)
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	httpapi.OK(c, result)
}
func (a ResourceAPI) executionCapabilities(c *gin.Context) {
	station, ok := queryStationID(c, true)
	if !ok {
		return
	}
	device := c.Query("device_id")
	if !deviceIDPattern.MatchString(device) {
		httpapi.BadRequest(c, "请选择设备")
		return
	}
	if !a.requirePricingTargetScope(c, station, device) {
		return
	}
	adapter, cap, err := (pricing.Store{DB: a.Store.AdminDB}).DeviceCapabilities(c.Request.Context(), station, device)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"protocol_adapter": adapter, "capabilities": cap, "reports_energy": cap.ReportsEnergy, "reports_segmented_power": cap.ReportsSegmentedPower})
}
