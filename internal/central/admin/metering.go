package admin

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// What a board can report is decided by its firmware, not by the operator.
//
// The vendor protocol carries an energy reading in the charge-end frame and a
// segmented power table in the metering frame. A pile whose frames have neither
// cannot be billed by kWh or by watt at all — it can only be told how long to
// run. Writing such a tariff anyway is the kind of mistake that only surfaces
// as a settlement that cannot be reconciled weeks later, so the capability is
// recorded per device and checked before a tariff is published.
//
// The default is refusal. A device whose capability has never been declared is
// treated as reporting nothing, because the newer protocol does not promise any
// field it does not explicitly carry.

type deviceCapability struct {
	DeviceID              string `gorm:"column:device_id"`
	ChargeMode            string `gorm:"column:charge_mode"`
	ReportsEnergy         bool   `gorm:"column:reports_energy"`
	ReportsSegmentedPower bool   `gorm:"column:reports_segmented_power"`
}

// capabilityBlock returns why this mode cannot run on this device, or an empty
// string when it can. The reasons are written for an operator: a refusal that
// only says "unsupported" gets worked around by turning the check off.
func capabilityBlock(mode pricing.ChargeMode, cap deviceCapability) string {
	switch mode {
	case pricing.ModeDeviceDuration:
		// The board counts its own minutes and stops itself. Nothing is
		// measured by us, so nothing has to be reported.
		return ""
	case pricing.ModeServerEnergy, pricing.ModeDeviceEnergy:
		if !cap.ReportsEnergy {
			return "未声明电量上报能力，无法按电量计费"
		}
	case pricing.ModeServerRealtimePower, pricing.ModeServerMaxPower, pricing.ModeDevicePower:
		if !cap.ReportsSegmentedPower {
			return "未声明分段功率上报能力，无法按功率计费"
		}
	}
	return ""
}

// modeNeverSet marks a device that had no pricing rule and no declared mode
// before this change. It is a log value, not a mode, and never reaches the
// protocol.
const modeNeverSet = "none"

// switchTarget is one device a tariff change has to reach, with the mode it is
// running today and what it can measure. Carrying the capability here rather
// than re-reading it keeps the check and the task log describing the same set
// of boards, read under the same snapshot.
type switchTarget struct {
	DeviceID string
	Before   string
	Cap      deviceCapability
}

// resolveSwitchTargets returns the devices a publication of this scope affects,
// with the mode each is running right now. A device with no rule of its own is
// on the yard default, so its before-mode is that rule's mode and not a blank.
func resolveSwitchTargets(tx *gorm.DB, stationID uint64, deviceID string) ([]switchTarget, error) {
	rows := []deviceCapability{}
	query := tx.Table("device_meta").
		Select("device_id, charge_mode, reports_energy, reports_segmented_power").
		Where("station_id=? AND deleted_at IS NULL", stationID)
	if deviceID != "" {
		query = tx.Table("device_meta").
			Select("device_id, charge_mode, reports_energy, reports_segmented_power").
			Where("station_id=? AND device_id=? AND deleted_at IS NULL", stationID, deviceID)
	}
	if err := query.Order("device_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	// The modes already running, keyed the way device_meta.device_id is, so a
	// device on the yard default reports that default rather than nothing.
	running := map[string]string{}
	rules := []struct {
		DeviceID *string `gorm:"column:device_id"`
		SpecJSON []byte  `gorm:"column:spec_json"`
	}{}
	if err := tx.Table("pricing_rule").
		Select("device_id, spec_json").
		Where("station_id=? AND status='active' AND deleted_at IS NULL", stationID).
		Find(&rules).Error; err != nil {
		return nil, err
	}
	yardMode := ""
	for _, rule := range rules {
		var spec pricing.Spec
		if unmarshalSpec(rule.SpecJSON, &spec) != nil {
			continue
		}
		if rule.DeviceID == nil {
			if yardMode == "" {
				yardMode = string(spec.Mode)
			}
			continue
		}
		if _, seen := running[*rule.DeviceID]; !seen {
			running[*rule.DeviceID] = string(spec.Mode)
		}
	}
	targets := make([]switchTarget, 0, len(rows))
	for _, row := range rows {
		before := running[row.DeviceID]
		if before == "" {
			before = yardMode
		}
		if before == "" {
			// No rule covers this device. The mode the operator declared on the
			// device itself is the next best answer, and a device that has never
			// been classified at all gets a marker rather than the mode about to
			// be set — otherwise a first-time application would look like a
			// change from itself.
			before = row.ChargeMode
		}
		if before == "" {
			before = modeNeverSet
		}
		targets = append(targets, switchTarget{DeviceID: row.DeviceID, Before: before, Cap: row})
	}
	return targets, nil
}

// validDeviceChargeMode checks the mode recorded against a device. It is one of
// the two-class six-mode set, so anything the protocol calls a charge type but
// the engine cannot price is refused here rather than stored and discovered
// later when a settlement has to be produced.
func validDeviceChargeMode(mode string) bool {
	return pricing.ChargeMode(strings.TrimSpace(mode)).Valid()
}

func unmarshalSpec(raw []byte, spec *pricing.Spec) error {
	if len(raw) == 0 {
		return errors.New("计费口径为空")
	}
	return json.Unmarshal(raw, spec)
}

// checkMetering refuses a tariff that at least one target device cannot run.
// Every offending device is named: a yard-wide apply that silently skips the
// three boards without meters would leave three piles charging on the old
// tariff with nothing in any log to say so.
func checkMetering(mode pricing.ChargeMode, targets []switchTarget) []string {
	blocked := []string{}
	for _, target := range targets {
		reason := capabilityBlock(mode, target.Cap)
		if reason == "" {
			continue
		}
		blocked = append(blocked, target.DeviceID+"："+reason)
	}
	return blocked
}

func (a ResourceAPI) registerDeviceMetering(r *gin.Engine) {
	r.PUT("/api/v1/admin/settings/device-metering", a.Auth.Require("device.update"), a.updateDeviceMetering)
}

type meteringInput struct {
	StationID  uint64 `json:"station_id"`
	DeviceID   string `json:"device_id"`
	ChargeMode string `json:"charge_mode"`
	// ReportsEnergy and ReportsSegmentedPower are sent as pointers so that a
	// form which omits one of them is a bad request rather than a silent
	// downgrade of a capability the operator never looked at.
	ReportsEnergy         *bool `json:"reports_energy"`
	ReportsSegmentedPower *bool `json:"reports_segmented_power"`
}

// updateDeviceMetering declares what a board can report. It is the one place
// this information is written by hand, so it is audited like any other change
// to how a device behaves.
func (a ResourceAPI) updateDeviceMetering(c *gin.Context) {
	var in meteringInput
	if !decodeResource(c, &in) {
		return
	}
	if in.StationID == 0 || !deviceIDPattern.MatchString(in.DeviceID) ||
		in.ReportsEnergy == nil || in.ReportsSegmentedPower == nil {
		httpapi.BadRequest(c, "请选择站点与设备，并同时声明电量与分段功率能力")
		return
	}
	// The charge type is a vendor protocol value, not a free string. Leaving it
	// blank records that the board has not been classified yet, which is
	// different from having been classified as a normal type.
	if in.ChargeMode != "" && !validDeviceChargeMode(in.ChargeMode) {
		httpapi.BadRequest(c, "设备充电类型无效")
		return
	}
	actor := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before deviceCapability
		if err := tx.Table("device_meta").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("device_id, charge_mode, reports_energy, reports_segmented_power").
			Where("device_id=? AND station_id=? AND deleted_at IS NULL", in.DeviceID, in.StationID).
			Take(&before).Error; err != nil {
			return err
		}
		row := map[string]any{
			"charge_mode": in.ChargeMode, "reports_energy": *in.ReportsEnergy,
			"reports_segmented_power": *in.ReportsSegmentedPower,
		}
		if err := tx.Table("device_meta").
			Where("device_id=? AND station_id=? AND deleted_at IS NULL", in.DeviceID, in.StationID).
			Updates(row).Error; err != nil {
			return err
		}
		return resourceAudit(tx, actor, "device.metering.update", "device_meta", 0, before, row,
			c.ClientIP(), httpapi.RequestID(c))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"station_id": in.StationID, "device_id": in.DeviceID, "updated": true})
}

// yardModeOf returns the mode the station's yard default is charging on, and
// false when the station has no active default yet.
func yardModeOf(tx *gorm.DB, stationID uint64) (pricing.ChargeMode, bool, error) {
	row := struct{ SpecJSON []byte }{}
	err := tx.Table("pricing_rule").
		Select("spec_json").
		Where("station_id=? AND device_id IS NULL AND status='active' AND deleted_at IS NULL", stationID).
		Order("version DESC").Limit(1).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var spec pricing.Spec
	if unmarshalSpec(row.SpecJSON, &spec) != nil {
		return "", false, nil
	}
	return spec.Mode, true, nil
}

// checkImportAgainstYard refuses adding a board to a yard that would then charge
// it on a tariff it cannot measure.
//
// The capability check at apply time only covers the boards that existed when
// the tariff was published. A yard priced before its hardware arrived is a
// normal thing to do, and the board that turns up six months later has never
// been through that check — so without this a metered tariff could end up
// running on a pile that never had a meter.
func checkImportAgainstYard(tx *gorm.DB, devices []ImportDevice) error {
	cache := map[uint64]pricing.ChargeMode{}
	for _, d := range devices {
		mode, ok := cache[d.StationID]
		if !ok {
			found, exists, err := yardModeOf(tx, d.StationID)
			if err != nil {
				return err
			}
			mode, ok = found, exists
			cache[d.StationID] = found
		}
		if !ok {
			continue
		}
		cap := deviceCapability{
			DeviceID:              d.DeviceID,
			ChargeMode:            d.ChargeMode,
			ReportsEnergy:         d.ReportsEnergy,
			ReportsSegmentedPower: d.ReportsSegmentedPower,
		}
		if reason := capabilityBlock(mode, cap); reason != "" {
			return &errMeteringBlocked{device: d.DeviceID, station: d.StationID, mode: mode, reason: reason}
		}
	}
	return nil
}

// errMeteringBlocked names the board that cannot join. The message reaches the
// operator as-is, so it has to say which board and what to do about it.
type errMeteringBlocked struct {
	device  string
	station uint64
	mode    pricing.ChargeMode
	reason  string
}

func (e *errMeteringBlocked) Error() string {
	return "设备 " + e.device + " 无法执行该场地的计费方式：" + e.reason
}

// newDevicesOnly drops the boards that already exist, so a check about what a
// board would start running applies only to boards that are about to start.
func newDevicesOnly(tx *gorm.DB, devices []ImportDevice) ([]ImportDevice, error) {
	if len(devices) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.DeviceID)
	}
	var known []string
	if err := tx.Table("device_meta").Where("device_id IN ? AND deleted_at IS NULL", ids).
		Pluck("device_id", &known).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(known))
	for _, id := range known {
		seen[id] = true
	}
	arriving := make([]ImportDevice, 0, len(devices))
	for _, d := range devices {
		if !seen[d.DeviceID] {
			arriving = append(arriving, d)
		}
	}
	return arriving, nil
}
