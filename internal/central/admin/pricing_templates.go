package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A pricing rule template is a reusable tariff that is not charged against
// anything until it is applied to a station. Applying writes a station-scoped
// pricing_rule row, which is what billing reads; that row is a copy, so editing
// the template afterwards cannot change what a station already charges.

type pricingTemplateInput struct {
	Name       string           `json:"name"`
	Mode       string           `json:"mode"`
	Periods    []pricing.Period `json:"time_of_use"`
	ServiceKWh int64            `json:"service_fee_cents_per_kwh"`
	ServiceMin int64            `json:"service_fee_cents_per_min"`
	Minimum    int64            `json:"min_charge_cents"`
	Version    uint32           `json:"version"`
}

func (a ResourceAPI) registerPricingTemplates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/pricing-rule-templates", a.Auth.Require("pricing.read"), a.pricingTemplates)
	r.POST("/api/v1/admin/settings/pricing-rule-templates", a.Auth.Require("pricing.rule.create"), a.createPricingTemplate)
	r.PUT("/api/v1/admin/settings/pricing-rule-templates/:id", a.Auth.Require("pricing.rule.update"), a.updatePricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-rule-templates/:id/disable", a.Auth.Require("pricing.rule.update"), a.disablePricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-rule-templates/:id/apply", a.Auth.Require("pricing.rule.create"), a.applyPricingTemplate)
}

func (a ResourceAPI) pricingTemplates(c *gin.Context) {
	rows := []map[string]any{}
	// Applied stations are carried on the row so an operator can see where a
	// template is in use before applying it, which is what the refusal below is
	// about. Without it, the only way to find out is to trigger the refusal.
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_rule_template t").
		Select("t.id,t.name,t.mode,t.time_of_use_json,t.service_fee_cents_per_kwh,t.service_fee_cents_per_min,t.min_charge_cents,t.version,t.status," +
			"(SELECT GROUP_CONCAT(CONCAT(s.name,'（',s.id,'）') ORDER BY s.id SEPARATOR '、')" +
			" FROM pricing_rule r JOIN station s ON s.id=r.station_id AND s.deleted_at IS NULL" +
			" WHERE r.template_id=t.id AND r.status='active' AND r.deleted_at IS NULL) AS applied_stations").
		Where("t.deleted_at IS NULL").Order("t.id").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

// tariff validates the financial inputs once, so a template and an applied rule
// cannot diverge in what they accept.
func tariff(in pricingTemplateInput) (pricing.Rule, bool) {
	rule := pricing.Rule{Mode: in.Mode, Periods: in.Periods, ServiceCentsPerKWh: in.ServiceKWh, ServiceCentsPerMinute: in.ServiceMin, MinimumCents: in.Minimum}
	if pricing.ValidateRule(rule) != nil {
		return pricing.Rule{}, false
	}
	return rule, true
}

func (a ResourceAPI) createPricingTemplate(c *gin.Context) {
	var in pricingTemplateInput
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Name, 128) {
		httpapi.BadRequest(c, "请填写规则名称")
		return
	}
	if _, ok := tariff(in); !ok {
		httpapi.BadRequest(c, "计费规则无效：时段须完整覆盖一天且不能重叠，金额须为非负整数")
		return
	}
	periods, _ := json.Marshal(in.Periods)
	row := map[string]any{
		"name": strings.TrimSpace(in.Name), "mode": in.Mode, "time_of_use_json": string(periods),
		"service_fee_cents_per_kwh": in.ServiceKWh, "service_fee_cents_per_min": in.ServiceMin,
		"min_charge_cents": in.Minimum, "version": 1, "status": "active",
	}
	actor := c.MustGet("admin_profile").(Profile)
	var id uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("pricing_rule_template").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "pricing.rule_template.create", "pricing_rule_template", id, nil, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": 1})
}

func (a ResourceAPI) updatePricingTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in pricingTemplateInput
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Name, 128) {
		httpapi.BadRequest(c, "请填写规则名称")
		return
	}
	if _, ok := tariff(in); !ok {
		httpapi.BadRequest(c, "计费规则无效：时段须完整覆盖一天且不能重叠，金额须为非负整数")
		return
	}
	if in.Version == 0 {
		httpapi.BadRequest(c, "缺少版本号，请刷新后重试")
		return
	}
	periods, _ := json.Marshal(in.Periods)
	actor := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before struct{ Version uint32 }
		if err := tx.Table("pricing_rule_template").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if before.Version != in.Version {
			return errConflict
		}
		// The tariff is deliberately not propagated to the rules already
		// applied. Those rows are what stations are being charged against.
		row := map[string]any{
			"name": strings.TrimSpace(in.Name), "mode": in.Mode, "time_of_use_json": string(periods),
			"service_fee_cents_per_kwh": in.ServiceKWh, "service_fee_cents_per_min": in.ServiceMin,
			"min_charge_cents": in.Minimum, "version": before.Version + 1,
		}
		if err := tx.Table("pricing_rule_template").Where("id=?", id).Updates(row).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "pricing.rule_template.update", "pricing_rule_template", id, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": in.Version + 1})
}

// disablePricingTemplate stops a template being applied again. It deliberately
// leaves the rules already applied in place: a station that is charging under
// it must not be left without a tariff.
func (a ResourceAPI) disablePricingTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before struct{ Version uint32 }
		if err := tx.Table("pricing_rule_template").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("pricing_rule_template").Where("id=?", id).Updates(map[string]any{"status": "disabled", "version": before.Version + 1}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "pricing.rule_template.disable", "pricing_rule_template", id, before, map[string]any{"status": "disabled"}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

type applyTemplateInput struct {
	StationID uint64 `json:"station_id"`
	RequestID string `json:"request_id"`
	// ExpectedVersion is the station's current rule version, so two operators
	// applying different templates at once cannot both win.
	ExpectedVersion uint32 `json:"expected_version"`
}

// applyPricingTemplate publishes a template as the station's active rule.
//
// Applying the same template to a station that already runs it is refused,
// matching how charge packages behave. A station changes tariff by disabling its
// current rule first, which is what the confirmation in the UI says. The station
// keeps a row per version, so refusing is a check on the active row and not a
// constraint on the table.
func (a ResourceAPI) applyPricingTemplate(c *gin.Context) {
	templateID, ok := pathID(c)
	if !ok {
		return
	}
	var in applyTemplateInput
	if !decodeResource(c, &in) {
		return
	}
	if _, err := uuid.Parse(in.RequestID); err != nil || in.StationID == 0 {
		httpapi.BadRequest(c, "请填写有效请求编号并选择站点")
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	payload, _ := json.Marshal(in)
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	var id uint64
	var version uint32
	replayed := false
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// The publication receipt is checked first so a retry after an uncertain
		// response returns the original result instead of publishing a second
		// version.
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

		// The column names are spelled out because none of them can be inferred
		// from the Go field names: GORM would read min_charge_cents as
		// "minimum" and service_fee_cents_per_kwh as "service_k_wh", yielding a
		// zero tariff that would then be applied to a live station.
		var tmpl struct {
			Name          string `gorm:"column:name"`
			Mode          string `gorm:"column:mode"`
			TimeOfUseJSON string `gorm:"column:time_of_use_json"`
			ServiceKWh    int64  `gorm:"column:service_fee_cents_per_kwh"`
			ServiceMin    int64  `gorm:"column:service_fee_cents_per_min"`
			Minimum       int64  `gorm:"column:min_charge_cents"`
			Status        string `gorm:"column:status"`
		}
		if err := tx.Table("pricing_rule_template").Where("id=? AND deleted_at IS NULL", templateID).Take(&tmpl).Error; err != nil {
			return err
		}
		if tmpl.Status != "active" {
			httpapi.Write(c, 409, 1009, "计费规则已停用，请先启用后再应用", nil)
			return errAlreadyReported
		}
		var station Station
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='active' AND deleted_at IS NULL", in.StationID).Take(&station).Error; err != nil {
			return err
		}
		var running int64
		if err := tx.Table("pricing_rule").Where("template_id=? AND station_id=? AND status='active' AND deleted_at IS NULL", templateID, in.StationID).Count(&running).Error; err != nil {
			return err
		}
		if running > 0 {
			httpapi.Write(c, 409, 1009, "该计费规则已应用到此站点，请先停用后再重新应用", nil)
			return errAlreadyReported
		}
		// The current version is read with a locking read rather than
		// MAX(version). Under REPEATABLE READ a plain aggregate reads the
		// snapshot taken when the transaction began, so two operators applying
		// at the same time would both see the old version and both win. A
		// locking read sees the latest committed row and waits for the other
		// transaction instead.
		var current struct{ Version uint32 }
		if err := tx.Table("pricing_rule").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("station_id=?", in.StationID).Order("version DESC").Limit(1).Take(&current).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if current.Version != in.ExpectedVersion {
			return errConflict
		}
		version = current.Version + 1
		if err := tx.Table("pricing_rule").Where("station_id=? AND status='active'", in.StationID).Update("status", "disabled").Error; err != nil {
			return err
		}
		row := map[string]any{
			"template_id": templateID, "station_id": in.StationID,
			"name": tmpl.Name, "mode": tmpl.Mode, "time_of_use_json": tmpl.TimeOfUseJSON,
			"service_fee_cents_per_kwh": tmpl.ServiceKWh, "service_fee_cents_per_min": tmpl.ServiceMin,
			"min_charge_cents": tmpl.Minimum, "version": version, "status": "active",
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
		return resourceAudit(tx, actor, "pricing.rule.apply", "pricing_rule", id, nil, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if errors.Is(err, errAlreadyReported) {
		return
	}
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": version, "replayed": replayed})
}
