package admin

import (
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Two things live here, both keyed to a device rather than a station:
//
//   - which tariff a device runs (device_pricing_assignment)
//   - which packages it sells (charge_offer.device_id)
//
// A device with neither inherits the station default. That inheritance is what
// keeps a station-wide rollout one click while still letting a single pile run
// something different, which is the arrangement real stations actually use.

// registerDevicePricing 挂载设备计费矩阵、设备回退到站点默认、站点策略三类路由，
// 权限点分别是 pricing.read 与 pricing.rule.update。
func (a ResourceAPI) registerDevicePricing(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/device-pricing", a.Auth.Require("pricing.read"), a.devicePricingMatrix)
	r.POST("/api/v1/admin/settings/device-pricing/reset", a.Auth.Require("pricing.rule.update"), a.resetDevicePricing)
	r.GET("/api/v1/admin/settings/station-policies", a.Auth.Require("pricing.read"), a.stationPolicies)
	// The path parameter is named :id because pathID reads that name, and every
	// other route in this package does the same. Naming it :station_id made
	// c.Param("id") empty, so the handler refused every request with "ID 必须为
	// 正整数" — a refund policy that could never be saved.
	r.PUT("/api/v1/admin/settings/station-policies/:id", a.Auth.Require("pricing.rule.update"), a.saveStationPolicy)
}

// devicePricingMatrix 是 GET /api/v1/admin/settings/device-pricing 的处理函数：
// 按站点逐台设备列出"实际在跑什么计费规则、在卖什么套餐"，没有独立规则的设备显示站点默认。
// 查询里 device_id 为空的那条 pricing_rule 是站点默认规则，SQL 别名为 station_default，
// 对外 JSON 字段是 station_version（早先是 yard / yard_version，现已统一为站点口径）。
//
// devicePricingMatrix returns, per device, the tariff it actually runs and the
// packages it sells. A device with no assignment and no device rule shows the
// station's, so the operator sees the effective state rather than the intent.
//
// What each board can measure is on the row too. A tariff refused for metering
// is otherwise a dead end: the operator is told the board is the problem but
// has no way to see or change the fact that made it one.
func (a ResourceAPI) devicePricingMatrix(c *gin.Context) {
	ctx := c.Request.Context()
	var station struct {
		ID uint64 `gorm:"column:id"`
	}
	if err := a.Store.AdminDB.WithContext(ctx).Table("station").Where("id=? AND deleted_at IS NULL", c.Query("station_id")).
		Take(&station).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	rows := []map[string]any{}
	// 设备自己的规则优先；设备没有独立规则时继承站点默认，也就是 device_id 为空的那一行。
	// SQL 里的 station_default 是"站点默认规则"这一侧的连接别名，输出字段 station_version
	// 是站点默认规则的版本号（早先叫 yard/yard_version，现已统一为站点口径）。
	err := a.Store.AdminDB.WithContext(ctx).Table("device_meta d").
		Select(`d.device_id, d.model, d.status AS device_status,
			d.charge_mode, d.reports_energy, d.reports_segmented_power,
			COALESCE(own.name, station_default.name) AS template_name,
			COALESCE(own.template_id, station_default.template_id) AS template_id,
			own.id AS own_rule_id, own.version AS own_version, station_default.version AS station_version,
			COALESCE(own.spec_json, station_default.spec_json) AS spec_json,
			COALESCE(own.device_id, station_default.device_id) AS effective_rule_device_id,
			(SELECT COUNT(*) FROM charge_offer o
			  WHERE o.station_id = d.station_id AND o.status='active'
			    AND (o.device_id = d.device_id OR o.device_id IS NULL)) AS offer_count,
			(SELECT GROUP_CONCAT(o.name ORDER BY o.price_cents SEPARATOR '、') FROM charge_offer o
			  WHERE o.station_id = d.station_id AND o.status='active'
			    AND (o.device_id = d.device_id OR o.device_id IS NULL)) AS offer_names`).
		Joins(`LEFT JOIN pricing_rule own ON own.id = (
				SELECT id FROM pricing_rule WHERE device_id = d.device_id AND status='active' AND deleted_at IS NULL
				ORDER BY version DESC, id DESC LIMIT 1)`).
		Joins(`LEFT JOIN pricing_rule station_default ON station_default.id = (
				SELECT id FROM pricing_rule WHERE station_id = d.station_id AND device_id IS NULL
				  AND status='active' AND deleted_at IS NULL
				ORDER BY version DESC, id DESC LIMIT 1)`).
		Where("d.station_id=? AND d.deleted_at IS NULL", station.ID).
		Order("d.device_id").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	httpapi.OK(c, gin.H{"station_id": station.ID, "items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

// resetDeviceInput 是"把某台设备交还给站点默认"的入参，只认站点加设备这一个组合。
type resetDeviceInput struct {
	StationID uint64 `json:"station_id"` // 所属站点 ID，必须为正整数
	DeviceID  string `json:"device_id"`  // 设备 ID，须匹配 ^[A-Za-z0-9_-]{1,64}$
}

// resetDevicePricing 让一台设备回退到站点默认：停用它自己的计费规则和它独有的在售套餐，
// 站点级的套餐保留，因此设备不会变成无可卖。整段放在一个事务里并对设备行加写锁，
// 避免与并发的规则下发互相覆盖，审计也在同一事务内写入。
//
// resetDevicePricing hands a device back to the station default: its own rule is
// disabled and its device-scoped packages are taken off sale. The station-wide
// ones stay, so the device is not left with nothing to sell.
func (a ResourceAPI) resetDevicePricing(c *gin.Context) {
	var in resetDeviceInput
	if !decodeResource(c, &in) {
		return
	}
	if in.StationID == 0 || !deviceIDPattern.MatchString(in.DeviceID) {
		httpapi.BadRequest(c, "请选择站点与设备")
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var device Station
		if err := tx.Table("device_meta").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("device_id=? AND station_id=? AND deleted_at IS NULL", in.DeviceID, in.StationID).
			Take(&device).Error; err != nil {
			return err
		}
		if err := tx.Table("pricing_rule").
			Where("station_id=? AND device_id=? AND status='active'", in.StationID, in.DeviceID).
			Update("status", "disabled").Error; err != nil {
			return err
		}
		if err := tx.Table("charge_offer").
			Where("station_id=? AND device_id=? AND status='active'", in.StationID, in.DeviceID).
			Update("status", "disabled").Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "pricing.device.reset", "device_meta", 0, nil,
			map[string]any{"station_id": in.StationID, "device_id": in.DeviceID}, c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"station_id": in.StationID, "device_id": in.DeviceID, "reset": true})
}

// A station policy is about moving money, not about pricing a session: whether
// a charging user must top up, and what happens to their money when a start fails.
// A refund is two independent questions: when one is allowed, and where the
// money lands. The commercial back office renders the pair as a single string
// such as "限时退款(时效外不退款)-原路退回", which is how a single enum came to
// be written here originally — and an enum can only ever answer one of the two.
// They are stored and edited separately.
// stationPolicyInput 是站点充值与退款策略的入参。充值门槛和退费规则分开存、分开编辑，
// 详见下方英文说明：一次退款其实是"允不允许退"和"钱退到哪里"两个独立问题。
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
	rows := []map[string]any{}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("station_policy p").
		Select("p.station_id, s.name AS station_name, p.force_recharge, p.min_balance_cents," +
			"p.scan_refund_path, p.scan_refund_rule, p.card_refund_path, p.card_refund_rule," +
			"p.timeout_start_refund, p.verify_phone_before_charge, p.version").
		Joins("JOIN station s ON s.id = p.station_id AND s.deleted_at IS NULL").
		Order("p.station_id").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	httpapi.OK(c, gin.H{"items": rows, "permissions": c.MustGet("admin_profile").(Profile).Permissions})
}

// saveStationPolicy 是 PUT /api/v1/admin/settings/station-policies/:id 的处理函数。
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
			// The caller believes a policy exists and it does not: something
			// changed underneath, and writing now would silently overwrite it.
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
