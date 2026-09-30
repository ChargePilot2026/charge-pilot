package admin

import (
	"crypto/sha256"
	"database/sql"
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

// 计费模板把一整套商业方案装在一个对象里：按什么口径收什么费、
// 充电用户可以选哪些套餐、小程序可以露出哪些内容。模板在被 apply 到
// 站点之前完全不起作用，apply 会把这三部分一并复制出去；之后再改
// 模板，既影响不到已在跑这份模板的站点，也影响不到已结算的订单。

// errAlreadyReported 表示「响应已经写过」，用于在处理函数已经自己写过
// 响应之后回滚事务；返回它可以避免通用失败处理再把同一个拒绝当成一个
// 看不懂的数据库错误报第二次。
var errAlreadyReported = errors.New("response already written")

// 计费模板只是费率本身。套餐另有一个模板池：套餐是按自己价格结算的预付封顶，
// 所以不管当前在跑哪份费率都仍然有效，一份费率也可以配好几套不同的套餐。
//
// pricingTemplateInput 是计费模板的创建与修改入参，两者共用同一份结构，
// 保证两种写法的校验口径完全一致。模板本身只是一份「报价草案」，只有 apply
// 到站点后才生效。
type pricingTemplateInput struct {
	Name    string           `json:"name"`    // 模板名称，非空且不超过 64 个字符
	Remark  string           `json:"remark"`  // 模板备注，可空，不超过 255 个字符
	Spec    pricing.Spec     `json:"spec"`    // 计费口径：计费方式、费率阶梯、电费与服务费等，必须能被 pricing.ValidateSpec 判定为可执行
	Display *pricing.Display `json:"display"` // 用户端展示开关；为 nil 表示用 pricing.DefaultDisplay() 的默认值。用指针而不是零值，是为了让"没传"和"传了一个全零的展示配置"这两种情况能被区分开
	// 叫 expected_version 而不是 version，是因为每一个带乐观锁的同类
	// 资源都这么叫：套餐模板、站点策略、设备规则。一个资源换个叫法，
	// 照着其它资源写的客户端就会毫无缘由地收到一个 400。
	// ExpectedVersion 是乐观锁：调用方声明它看到的目标当前版本号，为 0
	// 表示没带，修改接口会直接拒绝。所有同类资源统一叫这个名字，前端
	// 一套客户端才能通用。
	ExpectedVersion uint32 `json:"expected_version"`
}

// validTemplate 是创建与修改共用的唯一校验闸口：名称非空且不超长、
// 备注不超长、且计费口径本身可执行。不可用的口径在这里就被当作
// 参数错误挡下，而不是写到一半才以数据库错误的形式冒出来。
func validTemplate(in pricingTemplateInput) bool {
	return validText(in.Name, 64) && len([]rune(in.Remark)) <= 255 &&
		pricing.ValidateTemplateSpec(in.Spec) == nil
}

// registerPricingTemplates 注册计费模板的六个后台接口，
// 每个接口都挂上各自的权限点：读用 pricing.read，写用 create/update。
func (a ResourceAPI) registerPricingTemplates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/pricing-templates", a.Auth.Require("pricing.read"), a.pricingTemplates)
	r.GET("/api/v1/admin/settings/pricing-templates/:id", a.Auth.Require("pricing.read"), a.pricingTemplateDetail)
	r.POST("/api/v1/admin/settings/pricing-templates", a.Auth.Require("pricing.rule.create"), a.createPricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-templates/preview", a.Auth.Require("pricing.read"), a.previewPricingTemplate)
	r.PUT("/api/v1/admin/settings/pricing-templates/:id", a.Auth.Require("pricing.rule.update"), a.updatePricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-templates/:id/disable", a.Auth.Require("pricing.rule.update"), a.disablePricingTemplate)
	// 停用必须能撤回来：否则一次误停用就让这份模板永久
	// 报废，而 apply 还会拿「计费模板已停用，请
	// 先启用后再应用」去要求操作员做一件界面做不到的事。
	r.POST("/api/v1/admin/settings/pricing-templates/:id/enable", a.Auth.Require("pricing.rule.update"), a.enablePricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-templates/:id/copy", a.Auth.Require("pricing.rule.create"), a.copyPricingTemplate)
	r.POST("/api/v1/admin/settings/pricing-templates/:id/apply", a.Auth.Require("pricing.rule.create"), a.applyPricingTemplate)
}

// pricingTemplateRow 是 pricing_template 表的只读映射，用于把模板行搬进
// detail、copy、apply 这几条路径；JSON 原文保持 []byte 不解析，解析口径
// 只留给调用方。
type pricingTemplateRow struct {
	ID          uint64 `gorm:"column:id"`           // 模板主键
	Name        string `gorm:"column:name"`         // 模板名称
	Remark      string `gorm:"column:remark"`       // 模板备注
	SpecJSON    []byte `gorm:"column:spec_json"`    // 计费口径原文，对应 pricing.Spec；可能存的是过期口径，发布前要重新校验
	DisplayJSON []byte `gorm:"column:display_json"` // 用户端展示开关原文，对应 pricing.Display
	Status      string `gorm:"column:status"`       // 模板状态：active 可被应用；disabled 只允许查看，不允许再下发
	Version     uint32 `gorm:"column:version"`      // 模板版本号，每次修改或停用都 +1，用作 expected_version 乐观锁
}

// pricingTemplateListRow 是列表一行的落库形态：模板本体走结构体扫描
// （和详情接口同一套读法，spec_json 才能稳定拿到 []byte），已应用站点
// 是子查询拼出来的字符串。
type pricingTemplateListRow struct {
	ID              uint64         `gorm:"column:id"`
	Name            string         `gorm:"column:name"`
	Remark          string         `gorm:"column:remark"`
	Status          string         `gorm:"column:status"`
	Version         uint32         `gorm:"column:version"`
	SpecJSON        []byte         `gorm:"column:spec_json"`
	AppliedStations sql.NullString `gorm:"column:applied_stations"`
}

// pricingTemplates 分页返回计费模板列表，并带上每一行已应用到的站点名称，
// 供运营在把模板下发到别处之前先看清它现在被谁用着（也就是重复应用会被拒的
// 那个前提）。
func (a ResourceAPI) pricingTemplates(c *gin.Context) {
	scanned := []pricingTemplateListRow{}
	// spec_json 一并带出：列表要显示「计费方式」和「费率」两列，
	// 缺了它前端只能显示占位符，运营就得逐个点「查看」才能知道
	// 这份模板收多少钱。
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_template t").
		Select("t.id,t.name,t.remark,t.status,t.version,t.spec_json," +
			"(SELECT GROUP_CONCAT(CONCAT(s.name,'（',s.id,'）') ORDER BY s.id SEPARATOR '、')" +
			" FROM pricing_rule r JOIN station s ON s.id=r.station_id AND s.deleted_at IS NULL" +
			" WHERE r.template_id=t.id AND r.status='active' AND r.deleted_at IS NULL) AS applied_stations").
		Where("t.deleted_at IS NULL").Order("t.id").Find(&scanned).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	rows := make([]map[string]any, 0, len(scanned))
	for _, r := range scanned {
		row := map[string]any{
			"id": r.ID, "name": r.Name, "remark": r.Remark,
			"status": r.Status, "version": r.Version,
			"applied_stations": r.AppliedStations.String,
		}
		// 解析不出来的按空口径处理：这一行照样列
		// 出来，只是计费方式显示不了，总比整页列
		// 表直接 500、把所有模板都藏起来要好。
		var spec pricing.Spec
		if json.Unmarshal(r.SpecJSON, &spec) == nil && spec.Mode != "" {
			row["spec"] = spec
		}
		rows = append(rows, row)
	}
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

// pricingTemplateDetail 返回单份模板的只读详情，把
// spec_json / display_json 原文解析成结构体，保证详
// 情页和编辑表单看到的永远是同一份内容。原文解析不出来时按计费口径无效处理。
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
// 写入 spec_json / display_json，初始状态 active、版本 1。此时模板还只是
// 草案，不会影响任何已经在跑的站点。
func (a ResourceAPI) createPricingTemplate(c *gin.Context) {
	var in pricingTemplateInput
	if !decodeResource(c, &in) {
		return
	}
	if !validTemplate(in) {
		if errors.Is(pricing.ValidateTemplateSpec(in.Spec), pricing.ErrLegacyPricing) {
			httpapi.BadRequest(c, pricing.ErrLegacyPricing.Error())
		} else {
			httpapi.BadRequest(c, "计费模板参数无效：请检查名称、时段、档位及费率")
		}
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

// updatePricingTemplate 修改模板本体，必须带
// expected_version 乐观锁，不带直接 400。注意修改只改模板
// 草案，已经 apply 出去的 pricing_rule 记录和已结算订单都不受影响。
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
		if errors.Is(pricing.ValidateTemplateSpec(in.Spec), pricing.ErrLegacyPricing) {
			httpapi.BadRequest(c, pricing.ErrLegacyPricing.Error())
		} else {
			httpapi.BadRequest(c, "计费模板参数无效：请检查名称、时段、档位及费率")
		}
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
		// 已经 apply 出去的规则是刻
		// 意不动的：那些行正是站点在按它收
		// 费、已付款订单在按它结算的东西。
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

// copyPricingTemplate 复制一份模板并改用新名
// 字：计费口径和展示配置原样照搬，备注也沿用源模板，新模板从版本
// 1 重新开始计数。用途是「改一版试试」，不影响正在跑的模板。
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
		var spec pricing.Spec
		if json.Unmarshal(source.SpecJSON, &spec) != nil {
			httpapi.Write(c, 409, 1009, "计费模板口径已失效，请重新编辑后再复制", nil)
			return errAlreadyReported
		}
		if err := pricing.ValidateTemplateSpec(spec); err != nil {
			if errors.Is(err, pricing.ErrLegacyPricing) {
				httpapi.Write(c, 409, 1009, pricing.ErrLegacyPricing.Error(), nil)
			} else {
				httpapi.Write(c, 409, 1009, "计费模板口径已失效，请重新编辑后再复制", nil)
			}
			return errAlreadyReported
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
		if !errors.Is(err, errAlreadyReported) {
			resourceFailure(c, err)
		}
		return
	}
	httpapi.OK(c, gin.H{"id": newID, "version": 1})
}

// disablePricingTemplate 停用模板，只改 status
// 并让版本 +1（不走 expected_version 乐观锁，因为停用
// 不需要前端确认版本）。已经 apply 出去的计费规则原样保留——否则正在
// 按它收费的站点会突然没有计费口径可用。
// enablePricingTemplate 把一份被停用的模板放回可应用状
// 态。它只动模板本身，不会去碰已发布到站点的规则——那些规则本来就不受模板停
// 用影响（见 disablePricingTemplate）。已经处于可应用状态时原样返回，不空转一次版本号。
func (a ResourceAPI) enablePricingTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before struct {
			Version uint32
			Status  string
		}
		if err := tx.Table("pricing_template").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if before.Status == "active" {
			return nil
		}
		if err := tx.Table("pricing_template").Where("id=?", id).
			Updates(map[string]any{"status": "active", "version": before.Version + 1}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "pricing.template.enable", "pricing_template", id, before,
			map[string]any{"status": "active"}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

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

// applyTemplateInput 是「把模板发布到某个范围」的入参：
// 目标是整站默认（device_id 为空）还是某一台设备（device_id 有值）
type applyTemplateInput struct {
	StationID uint64 `json:"station_id"` // 目标站点 ID，必须是一个 active 且未删除的站点
	// DeviceID 为空表示发布整站默认口径；有值表示只发布这一台设备的
	// 口径，同一站点下两种费率并存就靠这个字段。
	DeviceID  string `json:"device_id"`
	RequestID string `json:"request_id"` // 发布请求的幂等编号（UUID）：同一次发布重试时靠它返回原结果，不会重复下发一版
	// ExpectedVersion 是目标当前计费规则的版本号，乐观锁：两个运营
	// 同时下发不同模板时只有一个能成功。
	ExpectedVersion         uint32 `json:"expected_version"`
	PreserveDeviceOverrides bool   `json:"preserve_device_overrides,omitempty"`
}

// deviceIDPattern 是网关登记的设备编号格式
// （字母、数字、下划线、短横，1–64 位）。在写任何设备
// 级数据之前先校验，拼错的编号不会留下一条永远不生效的绑定。
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ruleScope 把计费规则的查询范围收敛到某台设备或整站默认
// （device_id 为 NULL 的那条），它是「这次操作取代
// 哪些规则」的唯一定义处，重复应用检查、版本读取和旧规则停用都走它。
func ruleScope(stationID uint64, deviceID string) func(*gorm.DB) *gorm.DB {
	return func(q *gorm.DB) *gorm.DB {
		if deviceID == "" {
			return q.Where("station_id=? AND device_id IS NULL", stationID)
		}
		return q.Where("station_id=? AND device_id=?", stationID, deviceID)
	}
}

// applyPricingTemplate 把模板发布为某台设备或整个站点的在
// 跑计费规则。关键约束：写入 pricing_rule 的是模板的副本，之后再
// 改模板影响不到已在计费的站点和已结算订单；同一模板在同一范围内已经在跑时会被
// 拒绝，运营需要先停用当前规则再重新应用。发布前依次校验：幂等回执、模板在用状
// 态、口径是否仍可执行、站点与设备归属、目标设备是否具备计量能力、重复应用；设
// 备侧计费（非服务端计价）还会为每台目标设备建一条计费切换任务，并把任务号一并返回。
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
	if !a.requirePricingTargetScope(c, in.StationID, in.DeviceID) {
		return
	}
	payload, _ := json.Marshal(in)
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	var id uint64
	var version uint32
	var switchTask uint64
	replayed := false
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// 先查回执，这样响应不确定
		// 之后的重试会拿到原来的结
		// 果，而不是再发布一版。
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
			// The path template is part of the publication intent. Keep the legacy
			// payload digest stable, and verify it against the published snapshot.
			var publishedTemplateID uint64
			if err := tx.Table("pricing_rule").Where("id=?", receipt.RuleID).Pluck("template_id", &publishedTemplateID).Error; err != nil {
				return err
			}
			if publishedTemplateID != templateID {
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
		// 出口处再校验一次：一份存下来的费率还能不能跑，
		// 只有引擎说了才算。
		var spec pricing.Spec
		if json.Unmarshal(tmpl.SpecJSON, &spec) != nil || pricing.ValidateSpec(spec) != nil {
			httpapi.Write(c, 409, 1009, "计费模板口径已失效，请重新编辑后再应用", nil)
			return errAlreadyReported
		}
		if errors.Is(pricing.ValidateTemplateSpec(spec), pricing.ErrLegacyPricing) {
			httpapi.Write(c, 409, 1009, pricing.ErrLegacyPricing.Error(), nil)
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
		// 要写到哪块板子上，以及每块到底跑不
		// 跑得了马上要发布的这份额率。在下面
		// 那道重复应用检查之前先解析出来，这
		// 样拒绝时点名的是跑不了这份费率的板
		// 子，而不是「已经在别处用着」的模板。
		targets, err := resolvePublicationTargets(tx, in.StationID, in.DeviceID, in.PreserveDeviceOverrides)
		if err != nil {
			return err
		}
		// 目标列表为空，是硬件还没到就先
		// 把站点定了价，这是常事，所以这
		// 里不拒。拒的是板子加入一个随后
		// 会跑它测不到的费率的站点；那道
		// 检查在板子进门的地方做，不在这里。
		if blocked := checkMetering(spec.Mode, targets); len(blocked) > 0 {
			// 一台一台点名。整站发布时如果悄悄
			// 跳过三块没有电表的板子，那三块就
			// 会按一份谁也看不见的费率继续充电
			// ——这道检查要防的正是这个。
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
		// 用加锁读而不是 MAX(version)。
		// REPEATABLE READ下普
		// 通聚合读到的是事务开始时那份快照，
		// 于是两个运营同时发布时都会看见同一
		// 个旧版本，于是两个人都赢。
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
		// 只有确认新规则发布得出去之后
		// （上面那些检查已经确认过了），
		// 把在跑的那条规则置为被取代。
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
		// 设备自己那份「现在跑哪种计费方式」的记
		// 录跟着规则一起更新，这样运营在设备矩阵
		// 里看到的计费方式，就和费率日志里的一
		// 致，连一台从来没有自己规则的设备也是
		if err := tx.Table("device_meta").
			Where("station_id=? AND deleted_at IS NULL", in.StationID).
			Where("device_id IN ?", switchDeviceIDs(targets)).
			Updates(map[string]any{"charge_mode": string(spec.Mode)}).Error; err != nil {
			return err
		}
		// 只有设备侧计费的费率才需要下发到硬件：服
		// 务端计费下平台给这次会话定价，板子只要听
		// 开始和停止就行，在那里建任务等于记一条什
		// 么都没有的日志。任务和规则写在同一个事务
		// 里，所以一份已经生效的费率不可能是那种
		// 「上线了却没记录下发过程」的。没有可切换
		// 目标：一条里面没有行的任务只是一条只会让
		// 人困惑的日志，所以空发布不记任务。
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
		// 费率已经生效，板子还没切
		// 换。两半都放进响应里，是
		// 因为只看到 id 的运营
		// 会以为设备已经跑起来了。
		response["switch_task_id"] = switchTask
		response["switch_pending"] = true
	}
	httpapi.OK(c, response)
}

// switchDeviceIDs 取出一次切换涉及的全
// 部设备编号，直接就是 SQL IN 子句要的形状。
func switchDeviceIDs(targets []switchTarget) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.DeviceID)
	}
	return ids
}

// nullableDevice 把空的设备编
// 号翻译成数据库里的 NULL：NULL 表
// 示这条规则是整站默认，有值才表示设备级口径。
func nullableDevice(deviceID string) any {
	if deviceID == "" {
		return nil
	}
	return deviceID
}
