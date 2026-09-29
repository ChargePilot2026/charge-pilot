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

// 计费模板把一整套商业方案装在一个对象里：按什么口径收什么费、骑手可以选哪些套餐、
// 小程序可以露出哪些内容。模板在被 apply 到站点之前完全不起作用，apply 会把这三部分一并复制出去；
// 之后再改模板，既影响不到已在跑这份模板的站点，也影响不到已结算的订单。

// errAlreadyReported unwinds the transaction after the handler has already
// written its own response. Returning it keeps a refusal from being reported a
// second time by the generic failure path as an opaque database error.
// errAlreadyReported 表示"响应已经写过"，用于在处理函数已经自己写过响应之后回滚事务；
// 返回它可以避免通用失败处理再把同一个拒绝当成一个看不懂的数据库错误报第二次。
var errAlreadyReported = errors.New("response already written")

// A pricing template is only the tariff. Charge packages live in their own
// template pool: a package is a prepaid cap settled on its own price, so it
// stays valid whichever tariff is running, and one tariff can be paired with
// several different package sets.

// pricingTemplateInput 是计费模板的创建与修改入参，两者共用同一份结构，
// 保证两种写法的校验口径完全一致。模板本身只是一份"报价草案"，只有 apply 到站点后才生效。
type pricingTemplateInput struct {
	Name    string           `json:"name"`    // 模板名称，非空且不超过 64 个字符
	Remark  string           `json:"remark"`  // 模板备注，可空，不超过 255 个字符
	Spec    pricing.Spec     `json:"spec"`    // 计费口径：计费方式、费率阶梯、电费与服务费等，必须能被 pricing.ValidateSpec 判定为可执行
	Display *pricing.Display `json:"display"` // 用户端展示开关；为 nil 表示用 pricing.DefaultDisplay() 的默认值。用指针而不是零值，是为了让"没传"和"传了一个全零的展示配置"这两种情况能被区分开
	// Named expected_version, not version, because every sibling that takes an
	// optimistic lock spells it that way: the package template, the station
	// policy, the device rule. One resource calling it something else turns a
	// client written against the others into a 400 for no visible reason.
	// ExpectedVersion 是乐观锁：调用方声明它看到的目标当前版本号，为 0 表示没带，
	// 修改接口会直接拒绝。所有同类资源统一叫这个名字，前端一套客户端才能通用。
	ExpectedVersion uint32 `json:"expected_version"`
}

// validTemplate is the single gate every create and update passes through, so
// an unusable tariff or package is refused as bad input rather than surfacing
// later as a database error halfway through the write.

// validTemplate 是创建与修改共用的唯一校验闸口：名称非空且不超长、备注不超长、
// 且计费口径本身可执行。不可用的口径在这里就被当作参数错误挡下，
// 而不是写到一半才以数据库错误的形式冒出来。
func validTemplate(in pricingTemplateInput) bool {
	return validText(in.Name, 64) && len([]rune(in.Remark)) <= 255 &&
		pricing.ValidateSpec(in.Spec) == nil
}

// registerPricingTemplates 注册计费模板的六个后台接口，
// 每个接口都挂上各自的权限点：读用 pricing.read，写用 create/update。
func (a ResourceAPI) registerPricingTemplates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/pricing-templates", a.Auth.Require("pricing.read"), a.pricingTemplates)
	r.GET("/api/v1/admin/settings/pricing-templates/:id", a.Auth.Require("pricing.read"), a.pricingTemplateDetail)
	r.POST("/api/v1/admin/settings/pricing-templates", a.Auth.Require("pricing.rule.create"), a.createPricingTemplate)
	r.PUT("/api/v1/admin/settings/pricing-templates/:id", a.Auth.Require("pricing.rule.update"), a.updatePricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-templates/:id/disable", a.Auth.Require("pricing.rule.update"), a.disablePricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-templates/:id/copy", a.Auth.Require("pricing.rule.create"), a.copyPricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-templates/:id/apply", a.Auth.Require("pricing.rule.create"), a.applyPricingTemplate)
}

// pricingTemplateRow 是 pricing_template 表的只读映射，用于把模板行搬进
// detail、copy、apply 这几条路径；JSON 原文保持 []byte 不解析，解析口径只留给调用方。
type pricingTemplateRow struct {
	ID          uint64 `gorm:"column:id"`           // 模板主键
	Name        string `gorm:"column:name"`         // 模板名称
	Remark      string `gorm:"column:remark"`       // 模板备注
	SpecJSON    []byte `gorm:"column:spec_json"`    // 计费口径原文，对应 pricing.Spec；可能存的是过期口径，发布前要重新校验
	DisplayJSON []byte `gorm:"column:display_json"` // 用户端展示开关原文，对应 pricing.Display
	Status      string `gorm:"column:status"`       // 模板状态：active 可被应用；disabled 只允许查看，不允许再下发
	Version     uint32 `gorm:"column:version"`      // 模板版本号，每次修改或停用都 +1，用作 expected_version 乐观锁
}

// pricingTemplates 分页返回计费模板列表，并带上每一行已应用到的站点名称，
// 供运营在把模板下发到别处之前先看清它现在被谁用着（也就是重复应用会被拒的那个前提）。
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

// pricingTemplateDetail 返回单份模板的只读详情，把 spec_json / display_json 原文解析成结构体，
// 保证详情页和编辑表单看到的永远是同一份内容。原文解析不出来时按计费口径无效处理。
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

// createPricingTemplate 新建一份计费模板：校验口径、缺省补上默认展示配置，
// 写入 spec_json / display_json，初始状态 active、版本 1。
// 此时模板还只是草案，不会影响任何已经在跑的站点。
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

// updatePricingTemplate 修改模板本体，必须带 expected_version 乐观锁，不带直接 400。
// 注意修改只改模板草案，已经 apply 出去的 pricing_rule 记录和已结算订单都不受影响。
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

// copyPricingTemplate 复制一份模板并改用新名字：计费口径和展示配置原样照搬，备注也沿用源模板，
// 新模板从版本 1 重新开始计数。用途是"改一版试试"，不影响正在跑的模板。
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

// disablePricingTemplate 停用模板，只改 status 并让版本 +1（不走 expected_version 乐观锁，
// 因为停用不需要前端确认版本）。已经 apply 出去的计费规则原样保留——
// 否则正在按它收费的站点会突然没有计费口径可用。
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

// applyTemplateInput 是"把模板发布到某个范围"的入参：
// 目标是整站默认（device_id 为空）还是某一台设备（device_id 有值）。
type applyTemplateInput struct {
	StationID uint64 `json:"station_id"` // 目标站点 ID，必须是一个 active 且未删除的站点
	// DeviceID is empty to publish the station default. A device id publishes
	// that one pile's tariff, which is how a station runs two different tariffs
	// side by side.
	// DeviceID 为空表示发布整站默认口径；有值表示只发布这一台设备的口径，
	// 同一站点下两种费率并存就靠这个字段。
	DeviceID  string `json:"device_id"`
	RequestID string `json:"request_id"` // 发布请求的幂等编号（UUID）：同一次发布重试时靠它返回原结果，不会重复下发一版
	// ExpectedVersion is the target's current rule version, so two operators
	// applying different templates at once cannot both win.
	// ExpectedVersion 是目标当前计费规则的版本号，乐观锁：两个运营同时下发不同模板时只有一个能成功。
	ExpectedVersion uint32 `json:"expected_version"`
}

// devicePattern matches the ids the gateway registers. It is checked before a
// device row is written so a typo cannot create an assignment that silently
// never applies.

// deviceIDPattern 是网关登记的设备编号格式（字母、数字、下划线、短横，1–64 位）。
// 在写任何设备级数据之前先校验，拼错的编号不会留下一条永远不生效的绑定。
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// scope narrows a rule lookup to one device or to the station default, and is the
// single definition of "which rules does this operation supersede".

// ruleScope 把计费规则的查询范围收敛到某台设备或整站默认（device_id 为 NULL 的那条），
// 它是"这次操作取代哪些规则"的唯一定义处，重复应用检查、版本读取和旧规则停用都走它。
func ruleScope(stationID uint64, deviceID string) func(*gorm.DB) *gorm.DB {
	return func(q *gorm.DB) *gorm.DB {
		if deviceID == "" {
			return q.Where("station_id=? AND device_id IS NULL", stationID)
		}
		return q.Where("station_id=? AND device_id=?", stationID, deviceID)
	}
}

// applyPricingTemplate publishes a template as the running rule for one device
// or for the whole station. The rule is a copy of the template, so editing the
// template afterwards cannot change what is already being charged.
//
// Re-applying the same template where it already runs is refused; the operator
// disables the current rule first, which is what the confirmation says.

// applyPricingTemplate 把模板发布为某台设备或整个站点的在跑计费规则。
// 关键约束：写入 pricing_rule 的是模板的副本，之后再改模板影响不到已在计费的站点和已结算订单；
// 同一模板在同一范围内已经在跑时会被拒绝，运营需要先停用当前规则再重新应用。
// 发布前依次校验：幂等回执、模板在用状态、口径是否仍可执行、站点与设备归属、目标设备是否具备计量能力、重复应用；
// 设备侧计费（非服务端计价）还会为每台目标设备建一条计费切换任务，并把任务号一并返回。
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
		// An empty target list is a station being priced before its hardware
		// arrives, which is a normal thing to do, so it is not refused here.
		// What is refused is a board joining a station that would then run a
		// tariff it cannot measure; that check lives where the board is added.
		if blocked := checkMetering(spec.Mode, targets); len(blocked) > 0 {
			// Named one by one. A station-wide apply that quietly skipped the three
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

// switchDeviceIDs 取出一次切换涉及的全部设备编号，直接就是 SQL IN 子句要的形状。
func switchDeviceIDs(targets []switchTarget) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.DeviceID)
	}
	return ids
}

// nullableDevice 把空的设备编号翻译成数据库里的 NULL：
// NULL 表示这条规则是整站默认，有值才表示设备级口径。
func nullableDevice(deviceID string) any {
	if deviceID == "" {
		return nil
	}
	return deviceID
}
