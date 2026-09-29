package admin

import (
	"encoding/json"
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// A picker that only lists the applicable options leaves an operator guessing
// why the template they can see everywhere else is not in the list. So every
// candidate is returned, each carrying either an empty unavailable_reason or the
// specific reason it cannot be picked. The list is the documentation.

type candidateRow struct {
	ID      uint64          `gorm:"column:id"`
	Name    string          `gorm:"column:name"`
	Remark  string          `gorm:"column:remark"`
	Status  string          `gorm:"column:status"`
	Version uint32          `gorm:"column:version"`
	Spec    json.RawMessage `gorm:"column:spec_json"`
}

func (a ResourceAPI) pricingTemplateCandidates(c *gin.Context) {
	ctx := c.Request.Context()
	rows := []candidateRow{}
	if err := a.Store.AdminDB.WithContext(ctx).Table("pricing_template").
		Select("id,name,remark,status,version,spec_json").
		Where("deleted_at IS NULL").Order("id").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	stationID := c.Query("station_id")
	deviceID := c.Query("device_id")
	// What is already running here, so an operator can see what they would be
	// replacing rather than discovering it from the refusal afterwards.
	inUse := map[uint64]string{}
	// The boards this scope would actually write to, so a template the yard's
	// meters cannot support is refused here with the same wording the apply
	// will use, rather than being selectable and then turning the operator
	// away after they have filled the form in.
	var targets []switchTarget
	if stationID != "" {
		resolved, err := resolveSwitchTargets(a.Store.AdminDB.WithContext(ctx), parseStationID(stationID), deviceID)
		if err != nil {
			resourceFailure(c, err)
			return
		}
		targets = resolved
	}
	if len(targets) > 0 {
		var rules []struct {
			TemplateID uint64  `gorm:"column:template_id"`
			Name       string  `gorm:"column:name"`
			DeviceID   *string `gorm:"column:device_id"`
		}
		query := a.Store.AdminDB.WithContext(ctx).Table("pricing_rule").
			Select("template_id,name,device_id").
			Where("station_id=? AND status='active' AND deleted_at IS NULL", stationID)
		if deviceID != "" {
			query = query.Where("device_id = ? OR device_id IS NULL", deviceID)
		}
		if err := query.Find(&rules).Error; err != nil {
			resourceFailure(c, err)
			return
		}
		for _, rule := range rules {
			inUse[rule.TemplateID] = rule.Name
		}
	}
	items := []gin.H{}
	for _, row := range rows {
		var spec pricing.Spec
		reason := ""
		if row.Status != "active" {
			reason = "模板已停用"
		}
		// A stored tariff that the engine can no longer run is not offerable,
		// and saying so here is cheaper than discovering it at apply time.
		if reason == "" && (json.Unmarshal(row.Spec, &spec) != nil || pricing.ValidateSpec(spec) != nil) {
			reason = "计费口径已失效，请重新编辑"
		}
		if reason == "" {
			if blocked := checkMetering(spec.Mode, targets); len(blocked) > 0 {
				reason = blocked[0] + "（共 " + strconv.Itoa(len(blocked)) + " 台设备）"
			}
		}
		current, already := inUse[row.ID]
		items = append(items, gin.H{
			"id": row.ID, "name": row.Name, "remark": row.Remark, "status": row.Status,
			"version": row.Version, "spec": spec,
			"unavailable_reason":   reason,
			"currently_applied":    already,
			"currently_applied_as": current,
		})
	}
	httpapi.OK(c, gin.H{"items": items, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

// parseStationID turns the station filter into the id the device lookup needs.
// A filter that is not a number simply resolves to no devices, which leaves the
// picker showing every template unfiltered — the same as omitting the filter,
// which is a better answer than a 400 for a value that came from a dropdown.
func parseStationID(raw string) uint64 {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// registerPricingCandidates wires the picker behind the same read permission as
// the template list.
func (a ResourceAPI) registerPricingCandidates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/pricing-template-candidates", a.Auth.Require("pricing.read"), a.pricingTemplateCandidates)
}
