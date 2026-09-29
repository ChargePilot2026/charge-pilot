package admin

import (
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Two things live here, both keyed to a device rather than a yard:
//
//   - which tariff a device runs (device_pricing_assignment)
//   - which packages it sells (charge_offer.device_id)
//
// A device with neither inherits the yard default. That inheritance is what
// keeps a yard-wide rollout one click while still letting a single pile run
// something different, which is the arrangement real yards actually use.

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

// devicePricingMatrix returns, per device, the tariff it actually runs and the
// packages it sells. A device with no assignment and no device rule shows the
// yard's, so the operator sees the effective state rather than the intent.
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
	// The device's own rule wins; with no rule of its own it inherits the yard
	// default, which is the row with a null device_id.
	err := a.Store.AdminDB.WithContext(ctx).Table("device_meta d").
		Select(`d.device_id, d.model, d.status AS device_status,
			d.charge_mode, d.reports_energy, d.reports_segmented_power,
			COALESCE(own.name, yard.name) AS template_name,
			COALESCE(own.template_id, yard.template_id) AS template_id,
			own.id AS own_rule_id, own.version AS own_version, yard.version AS yard_version,
			COALESCE(own.spec_json, yard.spec_json) AS spec_json,
			COALESCE(own.device_id, yard.device_id) AS effective_rule_device_id,
			(SELECT COUNT(*) FROM charge_offer o
			  WHERE o.station_id = d.station_id AND o.status='active'
			    AND (o.device_id = d.device_id OR o.device_id IS NULL)) AS offer_count,
			(SELECT GROUP_CONCAT(o.name ORDER BY o.price_cents SEPARATOR '、') FROM charge_offer o
			  WHERE o.station_id = d.station_id AND o.status='active'
			    AND (o.device_id = d.device_id OR o.device_id IS NULL)) AS offer_names`).
		Joins(`LEFT JOIN pricing_rule own ON own.id = (
				SELECT id FROM pricing_rule WHERE device_id = d.device_id AND status='active' AND deleted_at IS NULL
				ORDER BY version DESC, id DESC LIMIT 1)`).
		Joins(`LEFT JOIN pricing_rule yard ON yard.id = (
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

type resetDeviceInput struct {
	StationID uint64 `json:"station_id"`
	DeviceID  string `json:"device_id"`
}

// resetDevicePricing hands a device back to the yard default: its own rule is
// disabled and its device-scoped packages are taken off sale. The yard-wide
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
// a rider must top up, and what happens to their money when a start fails.
// A refund is two independent questions: when one is allowed, and where the
// money lands. The commercial back office renders the pair as a single string
// such as "限时退款(时效外不退款)-原路退回", which is how a single enum came to
// be written here originally — and an enum can only ever answer one of the two.
// They are stored and edited separately.
type stationPolicyInput struct {
	ForceRecharge           bool   `json:"force_recharge"`
	MinBalanceCents         int64  `json:"min_balance_cents"`
	ScanRefundPath          string `json:"scan_refund_path"`
	ScanRefundRule          string `json:"scan_refund_rule"`
	CardRefundPath          string `json:"card_refund_path"`
	CardRefundRule          string `json:"card_refund_rule"`
	TimeoutStartRefund      bool   `json:"timeout_start_refund"`
	VerifyPhoneBeforeCharge bool   `json:"verify_phone_before_charge"`
	ExpectedVersion         uint32 `json:"expected_version"`
}

var refundPaths = map[string]bool{"balance": true, "original": true}

func validStationPolicy(in stationPolicyInput) bool {
	if in.MinBalanceCents < 0 || in.MinBalanceCents > 1000000 {
		return false
	}
	scan := map[string]bool{"none": true, "realtime": true, "time_limited": true}
	card := map[string]bool{"none": true, "realtime": true, "time_limited_prorated": true}
	return refundPaths[in.ScanRefundPath] && refundPaths[in.CardRefundPath] &&
		scan[in.ScanRefundRule] && card[in.CardRefundRule]
}

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
		httpapi.BadRequest(c, "场地策略参数无效")
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
