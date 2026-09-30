package admin

import (
	"errors"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 套餐模板提供金额消费上限或固定售价的充电时长。
// 上架复制条款到 charge_offer，模板更新不改变在售记录或购买快照。

// packageTemplateInput 是套餐模板新增与更新共用的请求体。
type packageTemplateInput struct {
	Name            string `json:"name"`             // 套餐名称，必填，最多 64 字符
	Kind            string `json:"kind"`             // 套餐种类：amount 按金额封顶、package 固定时长套餐
	PriceCents      int64  `json:"price_cents"`      // 消费上限或套餐售价（分）：两种类型均为 1–1000000
	DurationMinutes uint16 `json:"duration_minutes"` // 时长上限（分钟）：kind=package 时 1–600 且必填，kind=amount 时必须为 0
	MinChargeCents  int64  `json:"min_charge_cents"` // 最低消费（分），0–1000000
	ShowRemark      bool   `json:"show_remark"`      // 是否在小程序上展示套餐说明
	CardDefault     bool   `json:"card_default"`     // 是否作为该目标的默认勾选套餐
	SortOrder       int    `json:"sort_order"`       // 展示顺序，0–10000，越小越靠前；属于用户看到的一部分，所以要落库
	Status          string `json:"status"`           // 模板状态：active 可被应用、disabled 停止被应用
	ExpectedVersion uint32 `json:"expected_version"` // 乐观锁：更新时必填且要等于当前版本号；新增时必须为 0
}

// validPackageTemplate 校验金额方案的消费上限及固定时长套餐的售价和时长。
// validPackageTemplate 不校验 ExpectedVersion，那是更新时的乐观锁，由调用方单独判。
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
		return in.PriceCents > 0 && in.PriceCents <= 1000000 && in.DurationMinutes > 0 && in.DurationMinutes <= 600 && in.MinChargeCents == 0
	default:
		return false
	}
}

// packageFields 把请求体摊成可直接写库的列，名称做一次 TrimSpace。
// 不在这里带 version：版本号由新增/更新各自决定。
func packageFields(in packageTemplateInput) map[string]any {
	return map[string]any{
		"name": strings.TrimSpace(in.Name), "kind": in.Kind,
		"price_cents": in.PriceCents, "duration_minutes": in.DurationMinutes,
		"min_charge_cents": in.MinChargeCents,
		"show_remark":      in.ShowRemark, "card_default": in.CardDefault, "status": in.Status,
		// 展示顺序是充电用户看得见的一部分，所以要落库，
		// 而不是校验完就丢掉。
		"sort_order": in.SortOrder,
	}
}

// registerPackageTemplates 挂载套餐模板的接口：读用 pricing.read，
// 增改停用和上架沿用计费规则模板那套权限（写归 pricing.rule.create / update）。
func (a ResourceAPI) registerPackageTemplates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/package-templates", a.Auth.Require("pricing.read"), a.packageTemplates)
	r.POST("/api/v1/admin/settings/package-templates", a.Auth.Require("pricing.rule.create"), a.createPackageTemplate)
	r.PUT("/api/v1/admin/settings/package-templates/:id", a.Auth.Require("pricing.rule.update"), a.updatePackageTemplate)
	r.POST("/api/v1/admin/settings/package-templates/:id/disable", a.Auth.Require("pricing.rule.update"), a.disablePackageTemplate)
	r.POST("/api/v1/admin/settings/package-templates/:id/apply", a.Auth.Require("pricing.rule.create"), a.applyPackageTemplate)
}

// packageTemplates 列出未删除的套餐模板，按展示顺序排序。
// applied_targets 用子查询拼出这个套餐已经在哪些站点或设备上架，方便运营在重复上架前先看到。
// 响应里带上当前操作者的权限列表，前端据此决定按钮是否可点。
func (a ResourceAPI) packageTemplates(c *gin.Context) {
	rows := []map[string]any{}
	// 这个套餐已经在哪些目标上架，是跟着这一行一起返回的，
	// 这样运营在往同一个目标重复上架（会被拒）之前能先看到。
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

// createPackageTemplate 新建套餐模板，版本号从 1 起，状态取请求里的值。
// 新增时必须传 ExpectedVersion=0，用来把"误把更新请求打成新增"挡在外面。
func (a ResourceAPI) createPackageTemplate(c *gin.Context) {
	var in packageTemplateInput
	if !decodeResource(c, &in) {
		return
	}
	if !validPackageTemplate(in) || in.ExpectedVersion != 0 {
		httpapi.BadRequest(c, "套餐模板参数无效：金额方案须填写消费上限；固定时长套餐须填写售价和 1–600 分钟时长，不另设最低消费")
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

// updatePackageTemplate 更新套餐模板，用 ExpectedVersion 做乐观锁：行锁内比对不上就报冲突，
// 让运营刷新后重试，避免两个人同时改互相覆盖。写成功后版本号 +1。
// 已经在售的 charge_offer 保留自己那份副本：改模板不能顺带改掉某个站点正在卖的套餐，
// 也不能改掉已按它结算过的订单。
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
		httpapi.BadRequest(c, "套餐模板参数无效：金额方案须填写消费上限；固定时长套餐须填写售价和 1–600 分钟时长，不另设最低消费")
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// before 只取版本号，用于乐观锁比对和审计里的改前快照。
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
		// 已在售的 offer 保留自己那份副本：改一个套餐不该改掉某个站点
		// 正在卖的东西，也不该改掉已经按它结算过的订单。
		return resourceAudit(tx, actor, "pricing.package_template.update", "pricing_package_template", id, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "version": in.ExpectedVersion + 1})
}

// disablePackageTemplate 停用套餐模板，让它不能再被应用：只改模板状态并把版本号 +1，
// 已上架的 charge_offer 原样保留，正在卖这个套餐的站点可以继续卖。
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

// applyPackageInput 是把套餐上架到某个目标的请求体。
type applyPackageInput struct {
	StationID uint64 `json:"station_id"`           // 目标站点 id，必填
	DeviceID  string `json:"device_id"`            // 目标设备号，空串表示整站上架；非空时必须是该站点下的设备
	RequestID string `json:"request_id,omitempty"` // Optional correlation UUID; natural target identity supplies replay semantics.
}

// applyPackageTemplate 把套餐上架到某个站点或某台设备。同一个套餐可以既整站在售、
// 又单独挂在某台设备上，只有「这个完全一样的目标上已经在售」才算重复。
// 三种已有情况分别处理：已在售则原样返回那条 offer（重试不该被当成冲突），
// 已下架则以模板当前条款更新同一条 offer 并重新上架，
// 从未上架才新建。响应里的 replayed / relisted 说明这次走的是哪条路径。
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
	if in.RequestID != "" {
		if _, err := uuid.Parse(in.RequestID); err != nil {
			httpapi.BadRequest(c, "请求编号必须为 UUID")
			return
		}
	}
	if !a.requirePricingTargetScope(c, in.StationID, in.DeviceID) {
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	var offerID uint64
	var replayed, relisted bool
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// pkg 是模板当前的样子；上架时把这份值整份拷进 charge_offer，
		// 之后模板再改也不会影响这次上架。
		var pkg struct {
			Name            string // 套餐名称
			Kind            string // 套餐种类：amount / package
			PriceCents      int64  // 金额上限（分）
			DurationMinutes uint16 // 时长上限（分钟）
			MinChargeCents  int64  // 最低消费（分）
			ShowRemark      bool   // 是否展示说明
			CardDefault     bool   // 是否默认勾选
			Status          string // active / disabled
		}
		if err := tx.Table("pricing_package_template").Where("id=? AND deleted_at IS NULL", id).Take(&pkg).Error; err != nil {
			return err
		}
		// 已停用的套餐直接拒掉，而不是作为一条谁也看不见的 offer
		// 推进站点——那看起来就像是一次静悄悄的失败。
		if pkg.Status != "active" {
			httpapi.Write(c, 409, 1009, "套餐模板已停用，请先启用后再应用", nil)
			return errAlreadyReported
		}
		if !validPackageTemplate(packageTemplateInput{Name: pkg.Name, Kind: pkg.Kind, PriceCents: pkg.PriceCents,
			DurationMinutes: pkg.DurationMinutes, MinChargeCents: pkg.MinChargeCents, Status: pkg.Status}) {
			httpapi.Write(c, 409, 1009, "套餐售价或时长无效，请编辑模板补充后再上架", nil)
			return errAlreadyReported
		}
		var station Station
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='active' AND deleted_at IS NULL", in.StationID).Take(&station).Error; err != nil {
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
		// 这个精确目标上已经有什么，决定了答案怎么给，三种情况
		// 确实互不相同。
		//
		// 状态是有讲究的，而且以前不是：套餐下架后留下一条 disabled 的
		// offer，那个检查把它也算成在售，于是再想把套餐上架回去，会
		// 永远被「该套餐已在此处上架」拒掉。运营明明是自己下架的，
		// 却再也放不回去了。
		// existing 是这个精确目标上已有的那条 offer：ID 与状态，状态决定是重放、
		// 重新上架还是新建。查不到记录时 found.Error 是 gorm.ErrRecordNotFound，按新建处理。
		existing := struct {
			ID              uint64
			Status          string
			Name            string
			Mode            string
			PriceCents      int64
			DurationMinutes uint16
		}{}
		query := tx.Table("charge_offer").Select("id,status,name,mode,price_cents,duration_minutes").
			Where("package_template_id=? AND station_id=? AND deleted_at IS NULL", id, in.StationID)
		if in.DeviceID == "" {
			query = query.Where("device_id IS NULL")
		} else {
			query = query.Where("device_id=?", in.DeviceID)
		}
		found := query.Take(&existing)
		if found.Error != nil && !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		if found.Error == nil && existing.Status == "active" {
			// 已经在售，而且就是这个目标。走到这里最常见的原因是响应
			// 不确定之后的重试，它不该看起来像冲突：除非重复自己说
			// 出来了，否则调用方分不清这是重复还是失败。
			offerID, replayed = existing.ID, true
			return nil
		}
		if found.Error == nil {
			// 已下架，而运营现在要把它放回去。以模板当前售价与时长更新同一条 offer 并重新上架，
			// 而不是给同一个套餐的同一个目标再建第二行——那会留下两行
			// 只有一个区别：充电用户能看见的是哪一条。
			if err := tx.Table("charge_offer").Where("id=?", existing.ID).
				Updates(map[string]any{"status": "active", "version": gorm.Expr("version+1"),
					"name": pkg.Name, "mode": pkg.Kind, "price_cents": pkg.PriceCents, "duration_minutes": pkg.DurationMinutes,
					"min_charge_cents": pkg.MinChargeCents, "show_remark": pkg.ShowRemark, "card_default": pkg.CardDefault}).Error; err != nil {
				return err
			}
			offerID, relisted = existing.ID, true
			return resourceAudit(tx, actor, "pricing.package_template.relist", "charge_offer", existing.ID,
				map[string]any{"status": existing.Status, "name": existing.Name, "mode": existing.Mode,
					"price_cents": existing.PriceCents, "duration_minutes": existing.DurationMinutes},
				map[string]any{"status": "active", "name": pkg.Name, "mode": pkg.Kind,
					"price_cents": pkg.PriceCents, "duration_minutes": pkg.DurationMinutes},
				c.ClientIP(), httpapi.RequestID(c))
		}
		// code 是随行写进去的，事后补不上：这一列是 NOT NULL 且没有
		// 默认值，漏掉它的 insert 会直接失败。写成 create 之后的第二
		// 条语句，就意味着 create 从来没有成功过，套餐压根上不了架。
		// 曾经写在第二次写入上方的那条注释声称 code「可以在同一次
		// insert 里写」，而代码做的正好相反。
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
	httpapi.OK(c, gin.H{"offer_id": offerID, "replayed": replayed, "relisted": relisted})
}
