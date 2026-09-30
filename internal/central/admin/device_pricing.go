package admin

import (
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 这个文件里有两件事，都挂在设备上而不是站点上：
//
//   - 设备跑哪套计费规则（device_pricing_assignment）
//   - 设备在卖哪些套餐（charge_offer.device_id）
//
// 两样都没有的设备继承站点默认值。
// 正是这个继承让整站下发只要一键，同时单桩还能跑另一套规则——
// 真实站点就是这么用的。

// registerDevicePricing 挂载设备计费矩阵、设备回退到站点默认、站点策略三类路由，
// 权限点分别是 pricing.read 与 pricing.rule.update。
func (a ResourceAPI) registerDevicePricing(r *gin.Engine) {
	r.GET("/api/v1/admin/stations/:id/configuration", a.Auth.Require("station.read"), a.Auth.Require("pricing.read"), a.stationConfiguration)
	r.GET("/api/v1/admin/settings/device-pricing", a.Auth.Require("pricing.read"), a.devicePricingMatrix)
	r.POST("/api/v1/admin/settings/device-pricing/reset", a.Auth.Require("pricing.rule.update"), a.resetDevicePricing)
	r.GET("/api/v1/admin/settings/station-policies", a.Auth.Require("pricing.read"), a.stationPolicies)
	// 路径参数取名 :id 是因为 pathID 读的就是这个名字，本包其它路由也一样。
	// 早先叫 :station_id 时 c.Param("id") 拿不到值，
	// 于是处理器对每个请求都回 "ID 必须为正整数"——
	// 等于这条退款策略永远存不进去。
	r.PUT("/api/v1/admin/settings/station-policies/:id", a.Auth.Require("pricing.rule.update"), a.saveStationPolicy)
}

// devicePricingMatrix 是 GET /api/v1/admin/settings/device-pricing 的处理函数：
// 按站点逐台设备列出"实际在跑什么计费规则、在卖什么套餐"，没有独立规则的设备显示站点默认。
// 查询里 device_id 为空的那条 pricing_rule 是站点默认规则，SQL 别名为 station_default，
// 对外 JSON 字段是 station_version（早先是 yard / yard_version，现已统一为站点口径）。
//
// devicePricingMatrix 逐台设备返回它实际在跑的计费规则和在卖的套餐。
// 既没有独立规则、也没有设备级规则的设备显示站点默认值，
// 这样运营看到的是生效后的状态，而不是当初的意图。
//
// 同一行里还带着每块板子能计量什么。
// 否则"因为计量能力被拒"就是一条死路：运营被告知问题出在板子上，
// 却既看不到也改不了让它变成这样的那个事实。
func (a ResourceAPI) devicePricingMatrix(c *gin.Context) {
	ctx := c.Request.Context()
	stationID, ok := queryStationID(c, true)
	if !ok || !a.requireStationScope(c, stationID) {
		return
	}
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	var station struct {
		ID     uint64 `gorm:"column:id"`
		Status string
	}
	if err := a.Store.AdminDB.WithContext(ctx).Table("station").Where("id=? AND deleted_at IS NULL", stationID).
		Take(&station).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	rows := []map[string]any{}
	// 设备自己的规则优先；设备没有独立规则时继承站点默认，也就是 device_id 为空的那一行。
	// SQL 里的 station_default 是"站点默认规则"这一侧的连接别名，输出字段 station_version
	// 是站点默认规则的版本号（早先叫 yard/yard_version，现已统一为站点口径）。
	query := a.Store.AdminDB.WithContext(ctx).Table("device_meta d").
		Select(`d.device_id, d.model, d.status AS device_status,
			d.charge_mode, d.reports_energy, d.reports_segmented_power,
			COALESCE(own.name, station_default.name) AS template_name,
			COALESCE(own.template_id, station_default.template_id) AS template_id,
			own.id AS own_rule_id, own.version AS own_version, station_default.version AS station_version,
			own_latest.version AS own_latest_version,
			COALESCE(own.spec_json, station_default.spec_json) AS spec_json,
			COALESCE(own.device_id, station_default.device_id) AS effective_rule_device_id,
				(SELECT COUNT(*) FROM charge_offer o
				  WHERE o.station_id = d.station_id AND o.status='active' AND o.deleted_at IS NULL
				    AND (o.device_id = d.device_id OR o.device_id IS NULL)
				    AND (o.device_id = d.device_id OR o.package_template_id NOT IN (
				      SELECT package_template_id FROM charge_offer WHERE station_id=d.station_id
				      AND device_id=d.device_id AND status='active' AND deleted_at IS NULL))) AS offer_count,
				(SELECT GROUP_CONCAT(o.name ORDER BY o.price_cents SEPARATOR '、') FROM charge_offer o
				  WHERE o.station_id = d.station_id AND o.status='active' AND o.deleted_at IS NULL
				    AND (o.device_id = d.device_id OR o.device_id IS NULL)
				    AND (o.device_id = d.device_id OR o.package_template_id NOT IN (
				      SELECT package_template_id FROM charge_offer WHERE station_id=d.station_id
				      AND device_id=d.device_id AND status='active' AND deleted_at IS NULL))) AS offer_names`).
		Joins(`LEFT JOIN pricing_rule own ON own.id = (
					SELECT id FROM pricing_rule WHERE device_id = d.device_id AND status='active' AND deleted_at IS NULL
					AND (effective_from IS NULL OR effective_from<=NOW(3)) AND (effective_to IS NULL OR effective_to>NOW(3))
					ORDER BY version DESC, id DESC LIMIT 1)`).
		Joins(`LEFT JOIN pricing_rule station_default ON station_default.id = (
				SELECT id FROM pricing_rule WHERE station_id = d.station_id AND device_id IS NULL
					  AND status='active' AND deleted_at IS NULL
					  AND (effective_from IS NULL OR effective_from<=NOW(3)) AND (effective_to IS NULL OR effective_to>NOW(3))
					ORDER BY version DESC, id DESC LIMIT 1)`).
		// own_latest_version 故意不过滤 status：下发模板时的乐观锁读的是该范围
		// 「最新一条」规则，不管它是不是还在生效（见 applyPricingTemplate）。
		// 上面的 own_version 只看生效规则，两者口径不同，客户端要按前者回填版本号，
		// 否则一台刚被「重置为站点默认」停用了独立规则的设备会永远对不上锁。
		Joins(`LEFT JOIN pricing_rule own_latest ON own_latest.id = (
				SELECT id FROM pricing_rule WHERE device_id = d.device_id AND deleted_at IS NULL
				ORDER BY version DESC, id DESC LIMIT 1)`).
		Where("d.station_id=? AND d.deleted_at IS NULL", station.ID).
		Order("d.device_id")
	err := scope.ApplyVendors(query, "d.vendor_id").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	// 整站范围（device_id 为空）的那条链也要一份：客户端把模板下发到整站时，
	// 乐观锁比对的正是这一条链的最新版，同样不过滤 status。
	var stationLatest struct{ Version uint32 }
	if err := a.Store.AdminDB.WithContext(ctx).Table("pricing_rule").
		Where("station_id=? AND device_id IS NULL AND deleted_at IS NULL", station.ID).
		Order("version DESC").Limit(1).Take(&stationLatest).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{
		"station_id": station.ID, "items": rows,
		"station_latest_version": stationLatest.Version,
		"permissions":            c.MustGet("admin_profile").(Profile).Permissions,
	})
}

// resetDeviceInput 是"把某台设备交还给站点默认"的入参，只认站点加设备这一个组合。
type resetDeviceInput struct {
	StationID        uint64 `json:"station_id"`         // 所属站点 ID，必须为正整数
	DeviceID         string `json:"device_id"`          // 设备 ID，须匹配 ^[A-Za-z0-9_-]{1,64}$
	KeepDeviceOffers bool   `json:"keep_device_offers"` // true only restores the tariff; false preserves the legacy reset behavior.
}

// resetDevicePricing 让一台设备回退到站点默认：停用它自己的计费规则和它独有的在售套餐，
// 站点级的套餐保留，因此设备不会变成无可卖。整段放在一个事务里并对设备行加写锁，
// 避免与并发的规则下发互相覆盖，审计也在同一事务内写入。
//
// resetDevicePricing 把设备交还给站点默认：停用它自己的规则，
// 并把它独有范围内的套餐下架。站点级的套餐留着，
// 设备不会落到无套餐可卖。
func (a ResourceAPI) resetDevicePricing(c *gin.Context) {
	var in resetDeviceInput
	if !decodeResource(c, &in) {
		return
	}
	if in.StationID == 0 || !deviceIDPattern.MatchString(in.DeviceID) {
		httpapi.BadRequest(c, "请选择站点与设备")
		return
	}
	if !a.requirePricingTargetScope(c, in.StationID, in.DeviceID) {
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	var switchTask uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// Serialize reset with publication of station and device rules.
		var station Station
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", in.StationID).Take(&station).Error; err != nil {
			return err
		}
		var device deviceCapability
		if err := tx.Table("device_meta").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("device_id=? AND station_id=? AND deleted_at IS NULL", in.DeviceID, in.StationID).
			Take(&device).Error; err != nil {
			return err
		}
		var inherited pricing.Rule
		if in.KeepDeviceOffers {
			var err error
			inherited, err = (pricing.Store{DB: tx}).ActiveStationRule(c.Request.Context(), in.StationID)
			if errors.Is(err, pricing.ErrRuleUnavailable) || errors.Is(err, pricing.ErrInvalidPricing) {
				httpapi.Write(c, 409, 1009, "站点没有有效的默认计费规则，无法恢复继承", nil)
				return errAlreadyReported
			}
			if err != nil {
				return err
			}
			if reason := capabilityBlock(inherited.Spec.Mode, device); reason != "" {
				httpapi.Write(c, 409, 1009, reason, nil)
				return errAlreadyReported
			}
		}
		if err := tx.Table("pricing_rule").
			Where("station_id=? AND device_id=? AND status='active'", in.StationID, in.DeviceID).
			Update("status", "disabled").Error; err != nil {
			return err
		}
		if !in.KeepDeviceOffers {
			if err := tx.Table("charge_offer").
				Where("station_id=? AND device_id=? AND status='active'", in.StationID, in.DeviceID).
				Update("status", "disabled").Error; err != nil {
				return err
			}
		} else {
			if err := tx.Table("device_meta").Where("device_id=? AND station_id=?", in.DeviceID, in.StationID).
				Update("charge_mode", string(inherited.Spec.Mode)).Error; err != nil {
				return err
			}
			if !inherited.Spec.Mode.ServerBilled() {
				before := device.ChargeMode
				if before == "" {
					before = modeNeverSet
				}
				var err error
				var source struct{ TemplateID uint64 }
				if err := tx.Table("pricing_rule").Select("template_id").Where("id=?", inherited.ID).Take(&source).Error; err != nil {
					return err
				}
				switchTask, err = a.planSwitchTask(tx, actor, in.StationID, source.TemplateID, inherited.Spec.Mode,
					[]switchTarget{{DeviceID: in.DeviceID, Before: before, Cap: device}}, c)
				if err != nil {
					return err
				}
			}
		}
		return resourceAudit(tx, actor, "pricing.device.reset", "device_meta", 0, nil,
			map[string]any{"station_id": in.StationID, "device_id": in.DeviceID, "keep_device_offers": in.KeepDeviceOffers}, c.ClientIP(), httpapi.RequestID(c))
	})
	if errors.Is(err, errAlreadyReported) {
		return
	}
	if err != nil {
		resourceFailure(c, err)
		return
	}
	response := gin.H{"station_id": in.StationID, "device_id": in.DeviceID, "reset": true, "keep_device_offers": in.KeepDeviceOffers}
	if switchTask > 0 {
		response["switch_task_id"] = switchTask
		response["switch_pending"] = true
	}
	httpapi.OK(c, response)
}

// 站点策略管的是钱怎么走，而不是一次充电怎么计价：
// 充电用户是否必须先充值，以及开充失败后他的钱怎么处理。
// 一次退款其实是两个独立问题：什么时候允许退，以及钱退到哪里。
// 对标的商业后台把这一对渲染成一个字符串，例如
// "限时退款(时效外不退款)-原路退回"——本文件最初就是这么写成一个枚举的，
// 而一个枚举永远只能回答其中一个问题，
// 所以这两件事分开存、分开编辑。
// stationPolicyInput 是站点充值与退款策略的入参。充值门槛和退费规则分开存、分开编辑，
// 详见上面的说明：一次退款其实是"允不允许退"和"钱退到哪里"两个独立问题。
//
// 它们是分两个字段存的（path 与 rule），不是各退一个原因。
type stationPolicyInput struct {
	ForceRecharge           bool   `json:"force_recharge"`             // 余额低于门槛时是否强制先充值
	MinBalanceCents         int64  `json:"min_balance_cents"`          // 余额门槛（分），取值 0–1000000
	ScanRefundPath          string `json:"scan_refund_path"`           // 扫码退款的退费去向：balance 退回余额 / original 原路退回
	ScanRefundRule          string `json:"scan_refund_rule"`           // 扫码退费规则：none 不退 / realtime 实时 / time_limited 限时
	CardRefundPath          string `json:"card_refund_path"`           // 刷卡退款的退费去向：balance / original
	CardRefundRule          string `json:"card_refund_rule"`           // 刷卡退费规则：none / realtime / time_limited_prorated（限时按比例退）
	TimeoutStartRefund      bool   `json:"timeout_start_refund"`       // 启动结果不确定（超时）时是否直接退款
	VerifyPhoneBeforeCharge bool   `json:"verify_phone_before_charge"` // 开充前是否强制校验手机号
	ExpectedVersion         uint32 `json:"expected_version"`           // 乐观锁版本号：读到的策略版本；0 表示"我确认当前没有这条策略"，与已有记录不符时返回 409
}

// refundPaths 是退费去向的取值白名单，与数据库 ENUM 一致。
var refundPaths = map[string]bool{"balance": true, "original": true}

// validStationPolicy 校验站点策略入参：余额门槛在 0–1000000 之间，
// 四个枚举字段必须落在各自的取值集合内。两套退费规则的取值不同（刷卡多一个按比例），所以分开判断。
func validStationPolicy(in stationPolicyInput) bool {
	if in.MinBalanceCents < 0 || in.MinBalanceCents > 1000000 {
		return false
	}
	scan := map[string]bool{"none": true, "realtime": true, "time_limited": true}
	card := map[string]bool{"none": true, "realtime": true, "time_limited_prorated": true}
	return refundPaths[in.ScanRefundPath] && refundPaths[in.CardRefundPath] &&
		scan[in.ScanRefundRule] && card[in.CardRefundRule]
}

// stationPolicies 是 GET /api/v1/admin/settings/station-policies 的处理函数：
// 列出所有站点的充值与退款策略，按站点 ID 升序，一次性返回不分页。
func (a ResourceAPI) stationPolicies(c *gin.Context) {
	scope, ok := a.stationScope(c)
	if !ok {
		return
	}
	stationID, ok := queryStationID(c, false)
	if !ok || stationID > 0 && !a.requireStationScope(c, stationID) {
		return
	}
	rows := []map[string]any{}
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("station_policy p").
		Select("p.station_id, s.name AS station_name, p.force_recharge, p.min_balance_cents," +
			"p.scan_refund_path, p.scan_refund_rule, p.card_refund_path, p.card_refund_rule," +
			"p.timeout_start_refund, p.verify_phone_before_charge, p.version").
		Joins("JOIN station s ON s.id = p.station_id AND s.deleted_at IS NULL").
		Where("p.deleted_at IS NULL")
	if stationID > 0 {
		query = query.Where("p.station_id=?", stationID)
	}
	err := scope.ApplyStations(query, "p.station_id").Order("p.station_id").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions, "can_manage_default": canManageStationDefault(scope)})
}

// saveStationPolicy 是 PUT /api/v1/admin/settings/station-policies/：id 的处理函数。
// 用 expected_version 做乐观锁：已有记录必须版本号一致才允许覆盖，
// 调用方以为存在而实际不存在（或反之）都返回 409，避免静默覆盖别人的改动。
// 站点行与策略行都加写锁，版本自增后写审计。
func (a ResourceAPI) saveStationPolicy(c *gin.Context) {
	stationID, ok := pathID(c)
	if !ok {
		return
	}
	var in stationPolicyInput
	if !decodeResource(c, &in) {
		return
	}
	if !validStationPolicy(in) {
		httpapi.BadRequest(c, "站点策略参数无效")
		return
	}
	if !a.requirePricingTargetScope(c, stationID, "") {
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var station Station
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND status='active' AND deleted_at IS NULL", stationID).Take(&station).Error; err != nil {
			return err
		}
		var before struct{ Version uint32 }
		err := tx.Table("station_policy").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("station_id=? AND deleted_at IS NULL", stationID).Take(&before).Error
		version := uint32(1)
		if err == nil {
			if before.Version != in.ExpectedVersion {
				return errConflict
			}
			version = before.Version + 1
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		} else if in.ExpectedVersion != 0 {
			// 调用方以为策略存在、实际并不存在：底下的数据被人动过，
			// 这时候写下去等于静默覆盖。
			return errConflict
		}
		row := map[string]any{
			"station_id": stationID, "force_recharge": in.ForceRecharge,
			"min_balance_cents": in.MinBalanceCents,
			"scan_refund_path":  in.ScanRefundPath, "scan_refund_rule": in.ScanRefundRule,
			"card_refund_path": in.CardRefundPath, "card_refund_rule": in.CardRefundRule,
			"timeout_start_refund":       in.TimeoutStartRefund,
			"verify_phone_before_charge": in.VerifyPhoneBeforeCharge, "version": version,
		}
		if before.Version > 0 {
			if err := tx.Table("station_policy").Where("station_id=? AND deleted_at IS NULL", stationID).Updates(row).Error; err != nil {
				return err
			}
		} else if err := tx.Table("station_policy").Create(row).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "station.policy.update", "station_policy", stationID, before, row, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"station_id": stationID, "version": in.ExpectedVersion + 1})
}
