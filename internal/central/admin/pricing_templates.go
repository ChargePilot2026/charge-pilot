package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A pricing template is the whole commercial offer in one object: what is
// charged and on what basis, which packages a rider may pick, and what the mini
// program may reveal. It is inert until it is applied to a station, and the
// application copies all three parts. Editing a template afterwards cannot
// change a station that is already running it, nor a settled order.

// errAlreadyReported unwinds the transaction after the handler has already
// written its own response. Returning it keeps a refusal from being reported a
// second time by the generic failure path as an opaque database error.
var errAlreadyReported = errors.New("response already written")

// A pricing template is only the tariff. Charge packages live in their own
// template pool: a package is a prepaid cap settled on its own price, so it
// stays valid whichever tariff is running, and one tariff can be paired with
// several different package sets.
type pricingTemplateInput struct {
	Name    string           `json:"name"`
	Remark  string           `json:"remark"`
	Spec    pricing.Spec     `json:"spec"`
	Display *pricing.Display `json:"display"`
	// Named expected_version, not version, because every sibling that takes an
	// optimistic lock spells it that way: the package template, the station
	// policy, the device rule. One resource calling it something else turns a
	// client written against the others into a 400 for no visible reason.
	ExpectedVersion uint32 `json:"expected_version"`
}

// validTemplate is the single gate every create and update passes through, so
// an unusable tariff or package is refused as bad input rather than surfacing
// later as a database error halfway through the write.
func validTemplate(in pricingTemplateInput) bool {
	return validText(in.Name, 64) && len([]rune(in.Remark)) <= 255 &&
		pricing.ValidateSpec(in.Spec) == nil
}

func (a ResourceAPI) registerPricingTemplates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/pricing-templates", a.Auth.Require("pricing.read"), a.pricingTemplates)
	r.GET("/api/v1/admin/settings/pricing-templates/:id", a.Auth.Require("pricing.read"), a.pricingTemplateDetail)
	r.POST("/api/v1/admin/settings/pricing-templates", a.Auth.Require("pricing.rule.create"), a.createPricingTemplate)
	r.PUT("/api/v1/admin/settings/pricing-templates/:id", a.Auth.Require("pricing.rule.update"), a.updatePricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-templates/:id/disable", a.Auth.Require("pricing.rule.update"), a.disablePricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-templates/:id/copy", a.Auth.Require("pricing.rule.create"), a.copyPricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-templates/:id/apply", a.Auth.Require("pricing.rule.create"), a.applyPricingTemplate)
}

type pricingTemplateRow struct {
	ID          uint64 `gorm:"column:id"`
	Name        string `gorm:"column:name"`
	Remark      string `gorm:"column:remark"`
	SpecJSON    []byte `gorm:"column:spec_json"`
	DisplayJSON []byte `gorm:"column:display_json"`
	Status      string `gorm:"column:status"`
	Version     uint32 `gorm:"column:version"`
}

func (a ResourceAPI) pricingTemplates(c *gin.Context) {
	rows := []map[string]any{}
	// Applied stations ride along on the row so an operator can see where a
	// template is in use before applying it elsewhere, which is what the
	// duplicate-application refusal below is about.
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_template t").
		Select("t.id,t.name,t.remark,t.status,t.version," +
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

// pricingTemplateDetail serves the read-only view: the whole object resolved
// into the shape the editor renders, so the detail screen and the edit form can
// never disagree about what a template contains.
func (a ResourceAPI) pricingTemplateDetail(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var row pricingTemplateRow
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_template").
		Where("id=? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	var spec pricing.Spec
	var display pricing.Display
	if json.Unmarshal(row.SpecJSON, &spec) != nil || json.Unmarshal(row.DisplayJSON, &display) != nil {
		resourceFailure(c, pricing.ErrInvalidPricing)
		return
	}
	httpapi.OK(c, gin.H{
		"id": row.ID, "name": row.Name, "remark": row.Remark, "status": row.Status,
		"version": row.Version, "spec": spec, "display": display,
		"permissions": c.MustGet("admin_profile").(Profile).Permissions,
	})
}

func (a ResourceAPI) createPricingTemplate(c *gin.Context) {
	var in pricingTemplateInput
	if !decodeResource(c, &in) {
		return
	}
	if !validTemplate(in) {
		httpapi.BadRequest(c, "计费模板参数无效：请检查计费口径是否可执行、套餐参数是否完整")
		return
	}
	display := pricing.DefaultDisplay()
	if in.Display != nil {
		display = *in.Display
	}
	spec, _ := json.Marshal(in.Spec)
	shown, _ := json.Marshal(display)
	actor := c.MustGet("admin_profile").(Profile)
	var id uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		row := map[string]any{
			"name": strings.TrimSpace(in.Name), "remark": strings.TrimSpace(in.Remark),
			"spec_json": string(spec), "display_json": string(shown), "status": "active", "version": 1,
		}
		if err := tx.Table("pricing_template").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "pricing.template.create", "pricing_template", id, nil, row, c.ClientIP(), httpapi.RequestID(c))
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
	if !validTemplate(in) {
		httpapi.BadRequest(c, "计费模板参数无效：请检查计费口径是否可执行、套餐参数是否完整")
		return
	}
	if in.ExpectedVersion == 0 {
		httpapi.BadRequest(c, "缺少版本号，请刷新后重试")
		return
	}
	display := pricing.DefaultDisplay()
	if in.Display != nil {
		display = *in.Display
	}
	spec, _ := json.Marshal(in.Spec)
	shown, _ := json.Marshal(display)
	actor := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before struct{ Version uint32 }
		if err := tx.Table("pricing_template").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if before.Version != in.ExpectedVersion {
			return errConflict
		}
		// Applied rules are deliberately untouched. Those rows are what stations
		// are charging against, and what already-paid orders were settled with.
		row := map[string]any{
			"name": strings.TrimSpace(in.Name), "remark": strings.TrimSpace(in.Remark),
			"spec_json": string(spec), "display_json": string(shown), "version": before.Version + 1,
		}
		if err := tx.Table("pricing_template").Where("id=?", id).Updates(row).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "pricing.template.update", "pricing_template", id, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": in.ExpectedVersion + 1})
}

// copyPricingTemplate duplicates a template so an operator can try a variant
// without touching the one stations are already running.
func (a ResourceAPI) copyPricingTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Name, 64) {
		httpapi.BadRequest(c, "请填写模板名称")
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	var newID uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var source pricingTemplateRow
		if err := tx.Table("pricing_template").Where("id=? AND deleted_at IS NULL", id).Take(&source).Error; err != nil {
			return err
		}
		row := map[string]any{
			"name": strings.TrimSpace(in.Name), "remark": source.Remark,
			"spec_json": string(source.SpecJSON), "display_json": string(source.DisplayJSON),
			"status": "active", "version": 1,
		}
		if err := tx.Table("pricing_template").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&newID).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "pricing.template.copy", "pricing_template", newID,
			map[string]any{"source": id}, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": newID, "version": 1})
}

// disablePricingTemplate stops a template being applied again. It leaves the
// rules already applied in place: a station charging under it must not be left
// without a tariff.
func (a ResourceAPI) disablePricingTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before struct{ Version uint32 }
		if err := tx.Table("pricing_template").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("pricing_template").Where("id=?", id).
			Updates(map[string]any{"status": "disabled", "version": before.Version + 1}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "pricing.template.disable", "pricing_template", id, before,
			map[string]any{"status": "disabled"}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

type applyTemplateInput struct {
	StationID uint64 `json:"station_id"`
	// DeviceID is empty to publish the yard default. A device id publishes that
	// one pile's tariff, which is how a yard runs two different tariffs side by
	// side.
	DeviceID  string `json:"device_id"`
	RequestID string `json:"request_id"`
	// ExpectedVersion is the target's current rule version, so two operators
	// applying different templates at once cannot both win.
	ExpectedVersion uint32 `json:"expected_version"`
}

// devicePattern matches the ids the gateway registers. It is checked before a
// device row is written so a typo cannot create an assignment that silently
// never applies.
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// scope narrows a rule lookup to one device or to the yard default, and is the
// single definition of "which rules does this operation supersede".
func ruleScope(stationID uint64, deviceID string) func(*gorm.DB) *gorm.DB {
	return func(q *gorm.DB) *gorm.DB {
		if deviceID == "" {
			return q.Where("station_id=? AND device_id IS NULL", stationID)
		}
		return q.Where("station_id=? AND device_id=?", stationID, deviceID)
	}
}

// applyPricingTemplate publishes a template as the running rule for one device
// or for the whole yard. The rule is a copy of the template, so editing the
// template afterwards cannot change what is already being charged.
//
// Re-applying the same template where it already runs is refused; the operator
// disables the current rule first, which is what the confirmation says.
func (a ResourceAPI) applyPricingTemplate(c *gin.Context) {
	templateID, ok := pathID(c)
	if !ok {
		return
	}
	var in applyTemplateInput
	if !decodeResource(c, &in) {
		return
	}
	if _, err := uuid.Parse(in.RequestID); err != nil || in.StationID == 0 ||
		(in.DeviceID != "" && !deviceIDPattern.MatchString(in.DeviceID)) {
		httpapi.BadRequest(c, "请填写有效请求编号并选择站点")
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	payload, _ := json.Marshal(in)
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	var id uint64
	var version uint32
	var switchTask uint64
	replayed := false
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// The receipt is checked first so a retry after an uncertain response
		// returns the original result instead of publishing a second version.
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

		var tmpl pricingTemplateRow
		if err := tx.Table("pricing_template").Where("id=? AND deleted_at IS NULL", templateID).Take(&tmpl).Error; err != nil {
			return err
		}
		if tmpl.Status != "active" {
			httpapi.Write(c, 409, 1009, "计费模板已停用，请先启用后再应用", nil)
			return errAlreadyReported
		}
		// Re-validated on the way out: the engine is the only authority on
		// whether a stored tariff is still runnable.
		var spec pricing.Spec
		if json.Unmarshal(tmpl.SpecJSON, &spec) != nil || pricing.ValidateSpec(spec) != nil {
			httpapi.Write(c, 409, 1009, "计费模板口径已失效，请重新编辑后再应用", nil)
			return errAlreadyReported
		}
		var station Station
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND status='active' AND deleted_at IS NULL", in.StationID).Take(&station).Error; err != nil {
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
		// What has to be written to which board, and whether each of them can
		// even run what is about to be published. Resolved before the duplicate
		// check below so a refusal names the board that cannot take the tariff
		// instead of the template that is merely already in use somewhere.
		targets, err := resolveSwitchTargets(tx, in.StationID, in.DeviceID)
		if err != nil {
			return err
		}
		// An empty target list is a yard being priced before its hardware
		// arrives, which is a normal thing to do, so it is not refused here.
		// What is refused is a board joining a yard that would then run a
		// tariff it cannot measure; that check lives where the board is added.
		if blocked := checkMetering(spec.Mode, targets); len(blocked) > 0 {
			// Named one by one. A yard-wide apply that quietly skipped the three
			// boards without meters would leave those three charging on a tariff
			// nobody can see, which is the failure this check exists to prevent.
			scope := "以下设备无法执行该计费方式，请先补录计量能力："
			if in.DeviceID != "" {
				scope = "该设备无法执行该计费方式，请先补录计量能力："
			}
			httpapi.Write(c, 409, 1009, scope+strings.Join(blocked, "；"), nil)
			return errAlreadyReported
		}
		var running int64
		if err := ruleScope(in.StationID, in.DeviceID)(tx.Table("pricing_rule")).
			Where("template_id=? AND status='active' AND deleted_at IS NULL", templateID).
			Count(&running).Error; err != nil {
			return err
		}
		if running > 0 {
			scope := "该站点"
			if in.DeviceID != "" {
				scope = "该设备"
			}
			httpapi.Write(c, 409, 1009, "该计费模板已应用于"+scope+"，请先停用后再重新应用", nil)
			return errAlreadyReported
		}
		// Read with a locking read rather than MAX(version). Under REPEATABLE
		// READ a plain aggregate reads the snapshot taken when the transaction
		// began, so two operators applying at the same time would both see the
		// old version and both win.
		var current struct{ Version uint32 }
		if err := ruleScope(in.StationID, in.DeviceID)(tx.Table("pricing_rule")).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Order("version DESC").Limit(1).Take(&current).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if current.Version != in.ExpectedVersion {
			return errConflict
		}
		version = current.Version + 1
		// Supersede the running rule only once the new one is known to be
		// publishable, which the checks above have established.
		if err := ruleScope(in.StationID, in.DeviceID)(tx.Table("pricing_rule")).
			Where("status='active'").Update("status", "disabled").Error; err != nil {
			return err
		}
		row := map[string]any{
			"template_id": templateID, "station_id": in.StationID, "device_id": nullableDevice(in.DeviceID),
			"name": tmpl.Name, "spec_json": string(tmpl.SpecJSON), "channel": "default",
			"version": version, "status": "active",
		}
		if err := tx.Table("pricing_rule").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		if err := tx.Table("pricing_publication").Create(map[string]any{"request_id": in.RequestID,
			"actor_id": actor.ID, "payload_hash": hash, "rule_id": id, "version": version}).Error; err != nil {
			return err
		}
		// The board's own record of the mode it is on follows the rule, so an
		// operator looking at the device matrix sees the same mode the tariff
		// log does even for a device that never had a rule of its own.
		if err := tx.Table("device_meta").
			Where("station_id=? AND deleted_at IS NULL", in.StationID).
			Where("device_id IN ?", switchDeviceIDs(targets)).
			Updates(map[string]any{"charge_mode": string(spec.Mode)}).Error; err != nil {
			return err
		}
		// Only a device-billed tariff has to reach the hardware: under server
		// billing the platform prices the session and the board only has to
		// obey a start and a stop, so a task there would be a record of nothing.
		// The task is written in the same transaction as the rule, so a tariff
		// that is live can never be one whose rollout was not recorded.
		// Nothing to switch to: a task with no rows in it is a log entry that
		// can only ever be confusing, so an empty rollout records no task.
		if !spec.Mode.ServerBilled() && len(targets) > 0 {
			taskID, err := a.planSwitchTask(tx, actor, in.StationID, templateID, spec.Mode, targets, c)
			if err != nil {
				return err
			}
			switchTask = taskID
		}
		return resourceAudit(tx, actor, "pricing.template.apply", "pricing_rule", id, nil, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if errors.Is(err, errAlreadyReported) {
		return
	}
	if err != nil {
		resourceFailure(c, err)
		return
	}
	response := gin.H{"id": id, "version": version, "replayed": replayed}
	if switchTask > 0 {
		// The tariff is live; the boards are not yet switched. Both halves are
		// in the answer because an operator who reads only the id will assume
		// the devices are already running it.
		response["switch_task_id"] = switchTask
		response["switch_pending"] = true
	}
	httpapi.OK(c, response)
}

// switchDeviceIDs is the device list of a rollout, in the shape a SQL IN
// clause takes.
func switchDeviceIDs(targets []switchTarget) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.DeviceID)
	}
	return ids
}

func nullableDevice(deviceID string) any {
	if deviceID == "" {
		return nil
	}
	return deviceID
}
