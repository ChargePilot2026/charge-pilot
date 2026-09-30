package admin

import (
	"encoding/json"
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// 一个只列"当前能选的项目"的选择器，
// 会让运营猜不出为什么到处都看得见的模板不在列表里。
// 所以这里返回全部候选：每个候选要么带一个空的 unavailable_reason，
// 要么带上它选不了的具体原因——这份列表本身就是文档。

// candidateRow 是 pricing_template 表的一行原始模板数据。Spec 保留原始 JSON，是下面
// 判断"计费口径是否已失效"的输入：解析失败或校验不过的模板会被直接标成不可用。
type candidateRow struct {
	ID      uint64          `gorm:"column:id"`        // 模板主键。
	Name    string          `gorm:"column:name"`      // 模板名称。
	Remark  string          `gorm:"column:remark"`    // 模板备注。
	Status  string          `gorm:"column:status"`    // 模板状态：active 可用、disabled 停用。
	Version uint32          `gorm:"column:version"`   // 模板版本号，每次改口径递增。
	Spec    json.RawMessage `gorm:"column:spec_json"` // 计费口径原文（spec_json），交由 pricing 引擎按同一套规则校验。
}

// pricingTemplateCandidates 返回全部计费模板候选，并逐个说明"为什么选不了"：
// unavailable_reason 为空即可选，否则是该模板当前不可用的具体原因。带上 station_id
// 和 device_id 时还会给出两块额外信息——该范围内正在跑的规则（currently_applied*），
// 以及该范围内设备的计量能力校验结果，这样运营在填表之前就知道会不会被拒。
func (a ResourceAPI) pricingTemplateCandidates(c *gin.Context) {
	ctx := c.Request.Context()
	rows := []candidateRow{}
	if err := a.Store.AdminDB.WithContext(ctx).Table("pricing_template").
		Select("id,name,remark,status,version,spec_json").
		Where("deleted_at IS NULL").Order("id").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	stationID, ok := queryStationID(c, false)
	if !ok || stationID > 0 && !a.requireStationScope(c, stationID) {
		return
	}
	preserve, ok := queryBool(c, "preserve_device_overrides")
	if !ok {
		return
	}
	deviceID := c.Query("device_id")
	if deviceID != "" && (stationID == 0 || !deviceIDPattern.MatchString(deviceID)) {
		httpapi.BadRequest(c, "请选择有效的站点与设备")
		return
	}
	if stationID > 0 && !a.requirePricingTargetScope(c, stationID, deviceID) {
		return
	}
	// 这里当前在跑的是什么，运营能看清自己将要替换掉的是哪一条，
	// 而不是事后从拒绝提示里才知道。
	inUse := map[uint64]string{}
	// 这个范围真正会写到的板子。站点电表撑不住的模板在这里就被拒掉，
	// 而且用的是与真正执行时同一套话术，
	// 而不是先让它可以被选中，
	// 等运营填完一整张表再把人挡在门外。
	var targets []switchTarget
	if stationID > 0 {
		if deviceID != "" {
			var known int64
			if err := a.Store.AdminDB.WithContext(ctx).Table("device_meta").Where("station_id=? AND device_id=? AND deleted_at IS NULL", stationID, deviceID).Count(&known).Error; err != nil {
				resourceFailure(c, err)
				return
			}
			if known == 0 {
				httpapi.Write(c, 404, 1004, "该设备不属于此站点", nil)
				return
			}
		}
		resolved, err := resolvePublicationTargets(a.Store.AdminDB.WithContext(ctx), stationID, deviceID, preserve)
		if err != nil {
			resourceFailure(c, err)
			return
		}
		targets = resolved
	}
	if stationID > 0 {
		// 已在该范围内生效的规则：device_id 为空表示站点默认规则，其余为设备级规则。
		var rules []struct {
			TemplateID uint64  `gorm:"column:template_id"` // 规则引用的模板 ID。
			Name       string  `gorm:"column:name"`        // 规则名称。
			DeviceID   *string `gorm:"column:device_id"`   // 规则作用的设备编号；nil 表示站点默认规则。
		}
		query := a.Store.AdminDB.WithContext(ctx).Table("pricing_rule").
			Select("template_id,name,device_id").
			Where("station_id=?", stationID).Where(effectiveRuleSQL)
		if deviceID != "" {
			query = query.Where("device_id = ?", deviceID)
		} else {
			query = query.Where("device_id IS NULL")
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
		// 引擎已经算不出来的存量计费口径不该摆上候选，
		// 在这里说清楚，比等到执行时才发现要便宜得多。
		if reason == "" && (json.Unmarshal(row.Spec, &spec) != nil || pricing.ValidateSpec(spec) != nil) {
			reason = "计费口径已失效，请重新编辑"
		}
		if reason == "" && pricing.ValidateTemplateSpec(spec) != nil {
			reason = pricing.ErrLegacyPricing.Error()
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

// parseStationID 把站点过滤值转成设备查询需要的 ID。
// 不是数字的过滤值就当作解析不出任何设备，
// 于是选择器照样列出全部模板、不加过滤——和不传这个过滤是一个效果，
// 对一个来自下拉框的值来说，这比回 400 更合适。

// parseStationID 把站点过滤值转成设备查询要用的 ID。解析失败一律返回 0（等价于
// 不按站点过滤），而不是回 400——这个值来自下拉框，返回 0 至少还能给出未过滤的
// 完整列表，比一个空列表有用。
func parseStationID(raw string) uint64 {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// registerPricingCandidates 把候选选择器挂在
// 与模板列表相同的那个读取权限后面。
//
// registerPricingCandidates 把候选选择器挂在与模板列表相同的读取权限 pricing.read 下。
func (a ResourceAPI) registerPricingCandidates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/pricing-template-candidates", a.Auth.Require("pricing.read"), a.pricingTemplateCandidates)
}
