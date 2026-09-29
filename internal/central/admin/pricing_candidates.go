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
	stationID := c.Query("station_id")
	deviceID := c.Query("device_id")
	// What is already running here, so an operator can see what they would be
	// replacing rather than discovering it from the refusal afterwards.
	inUse := map[uint64]string{}
	// The boards this scope would actually write to, so a template the station's
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
		// 已在该范围内生效的规则：device_id 为空表示站点默认规则，其余为设备级规则。
		var rules []struct {
			TemplateID uint64  `gorm:"column:template_id"` // 规则引用的模板 ID。
			Name       string  `gorm:"column:name"`        // 规则名称。
			DeviceID   *string `gorm:"column:device_id"`   // 规则作用的设备编号；nil 表示站点默认规则。
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

// registerPricingCandidates wires the picker behind the same read permission as
// the template list.
//
// registerPricingCandidates 把候选选择器挂在与模板列表相同的读取权限 pricing.read 下。
func (a ResourceAPI) registerPricingCandidates(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/pricing-template-candidates", a.Auth.Require("pricing.read"), a.pricingTemplateCandidates)
}
