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

// deviceCapability 是一块板子当前声明的计量能力，映射 device_meta 的四列。
// 默认一律视为"不上报"：没声明过能力的设备，等于什么都不报，因为新协议不承诺
// 它没有明确携带的字段。
type deviceCapability struct {
	DeviceID              string `gorm:"column:device_id"`               // 设备号，全网唯一，是这块板子的标识
	ChargeMode            string `gorm:"column:charge_mode"`             // 设备充电类型（厂商协议值），空串表示尚未归类；另有取值 none 仅用于任务日志
	ReportsEnergy         bool   `gorm:"column:reports_energy"`          // 是否在结束充电帧里上报电量，决定能否按电量计费
	ReportsSegmentedPower bool   `gorm:"column:reports_segmented_power"` // 是否上报分段功率表，决定能否按功率计费
}

// capabilityBlock returns why this mode cannot run on this device, or an empty
// string when it can. The reasons are written for an operator: a refusal that
// only says "unsupported" gets worked around by turning the check off.
// capabilityBlock 给出这块设备跑不了这个计费方式的原因，能跑时返回空串。
// 时长计费永远放行——板子自己计分钟数、自己停，不需要平台测到什么；
// 其余四种都落在电量或分段功率上。拒绝理由写给运营看，只写"不支持"会被人直接把检查关掉。
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
// modeNeverSet 标记"改这次费率之前既没有计费规则、也没有声明过充电类型"的设备。
// 它只是任务日志里的一个值，不是计费方式，永远不会下发到设备。
const modeNeverSet = "none"

// switchTarget is one device a tariff change has to reach, with the mode it is
// running today and what it can measure. Carrying the capability here rather
// than re-reading it keeps the check and the task log describing the same set
// of boards, read under the same snapshot.
// switchTarget 是一次费率切换要下发到的一台设备，连同它现在跑的计费方式和它能测到什么。
// 能力随行携带而不是二次查询，保证能力校验和任务日志说的是同一批板子、同一时刻的快照。
type switchTarget struct {
	DeviceID string           // 设备号
	Before   string           // 切换前实际生效的计费方式；从未设定过时为 modeNeverSet
	Cap      deviceCapability // 该设备的计量能力声明
}

// resolveSwitchTargets returns the devices a publication of this scope affects,
// with the mode each is running right now. A device with no rule of its own is
// on the station default, so its before-mode is that rule's mode and not a blank.
// resolveSwitchTargets 列出这次发布范围内受影响的设备及其当前计费方式。
// deviceID 为空表示整个站点，否则只看这一台。设备级规则优先，没有自己的规则就落在
// 站点默认规则上，所以 before 是站点默认的计费方式而不是空值；连规则都没有时退回
// 设备上自己声明的充电类型，再没有才标成 modeNeverSet。
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
	// device on the station default reports that default rather than nothing.
	// running 按设备号索引当前生效的计费方式，key 用 device_meta.device_id 的写法，
	// 这样站点默认生效的设备也能报出这个默认值，而不是空值。
	running := map[string]string{}
	// rules 是站点下所有生效中的计费规则：device_id 为 NULL 表示站点默认规则。
	rules := []struct {
		DeviceID *string `gorm:"column:device_id"` // 规则作用的设备号，NULL 表示这条是站点默认
		SpecJSON []byte  `gorm:"column:spec_json"` // 计费口径 JSON，解析失败直接跳过这条规则
	}{}
	if err := tx.Table("pricing_rule").
		Select("device_id, spec_json").
		Where("station_id=? AND status='active' AND deleted_at IS NULL", stationID).
		Find(&rules).Error; err != nil {
		return nil, err
	}
	stationMode := ""
	for _, rule := range rules {
		var spec pricing.Spec
		if unmarshalSpec(rule.SpecJSON, &spec) != nil {
			continue
		}
		if rule.DeviceID == nil {
			if stationMode == "" {
				stationMode = string(spec.Mode)
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
			before = stationMode
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
// validDeviceChargeMode 校验设备上记录的充电类型是否属于"服务端计费/设备计费"两类的六种
// 计费方式之一。协议里叫得出名字、但计费引擎算不出价的类型，在这里就拒掉，
// 而不是先存下来，等要出账时才发现。
func validDeviceChargeMode(mode string) bool {
	return pricing.ChargeMode(strings.TrimSpace(mode)).Valid()
}

// unmarshalSpec 解析 pricing_rule.spec_json 这段计费口径；口径为空或不是合法 JSON
// 一律当解析失败，调用方据此跳过这条规则而不是用一个半截口径继续算钱。
func unmarshalSpec(raw []byte, spec *pricing.Spec) error {
	if len(raw) == 0 {
		return errors.New("计费口径为空")
	}
	return json.Unmarshal(raw, spec)
}

// checkMetering refuses a tariff that at least one target device cannot run.
// Every offending device is named: a station-wide apply that silently skips the
// three boards without meters would leave three piles charging on the old
// tariff with nothing in any log to say so.
// checkMetering 挑出跑不了这个计费方式的目标设备，每台都带上设备号和原因——
// 整站发布时如果悄悄跳过三块没有电表的板子，就会有三个桩还按旧费率在充，日志里却什么都没有。
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

// registerDeviceMetering 挂载设备计量能力申报接口：运营在后台手工登记某块板子
// 能上报什么，只有这一处是人工填的，所以同样要审计。
func (a ResourceAPI) registerDeviceMetering(r *gin.Engine) {
	r.PUT("/api/v1/admin/settings/device-metering", a.Auth.Require("device.metering"), a.updateDeviceMetering)
}

// meteringInput 是设备计量能力申报的请求体，站点与设备必填。
type meteringInput struct {
	StationID  uint64 `json:"station_id"`  // 站点 id，必填
	DeviceID   string `json:"device_id"`   // 设备号，必填且要符合设备号格式
	ChargeMode string `json:"charge_mode"` // 设备充电类型，空串表示尚未归类；非空时须是六种计费方式之一
	// ReportsEnergy and ReportsSegmentedPower are sent as pointers so that a
	// form which omits one of them is a bad request rather than a silent
	// downgrade of a capability the operator never looked at.
	// ReportsEnergy、ReportsSegmentedPower 用指针：漏填直接报参数错误，
	// 而不是把运营没看过的那项能力默默降级成 false。
	ReportsEnergy         *bool `json:"reports_energy"`          // 是否上报电量；必填，nil 视为未申报
	ReportsSegmentedPower *bool `json:"reports_segmented_power"` // 是否上报分段功率；必填，nil 视为未申报
}

// updateDeviceMetering declares what a board can report. It is the one place
// this information is written by hand, so it is audited like any other change
// to how a device behaves.
// updateDeviceMetering 申报某块板子能上报什么。设备必须已存在（这里只改能力，不建设备），
// 改前快照与改动内容在同一事务内写审计。
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

// stationModeOf returns the mode the station's default is charging on, and
// false when the station has no active default yet.
// stationModeOf 取出站点默认规则当前生效的计费方式；站点还没有生效的默认规则时返回 false，
// 口径 JSON 解析不出来也按"没有"处理，宁可让上层跳过这次校验。
func stationModeOf(tx *gorm.DB, stationID uint64) (pricing.ChargeMode, bool, error) {
	// row 只取 spec_json 一列；口径为空时下面 unmarshalSpec 会返回错误。
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

// checkImportAgainstStation refuses adding a board to a station that would then
// charge it on a tariff it cannot measure.
//
// The capability check at apply time only covers the boards that existed when
// the tariff was published. A station priced before its hardware arrived is a
// normal thing to do, and the board that turns up six months later has never
// been through that check — so without this a metered tariff could end up
// running on a pile that never had a meter.
// checkImportAgainstStation 在导入设备时校验：设备要加入的站点若已按电量或功率计费，
// 这块板子必须声明了对应的计量能力，否则直接报错。能力检查必须在设备进门处做，
// 否则半年后到货的板子从没经过那次检查，站点就会按它测不到的费率收钱。
// 站点默认计费方式按站点缓存，一批导入里同一站点只查一次。
func checkImportAgainstStation(tx *gorm.DB, devices []ImportDevice) error {
	cache := map[uint64]pricing.ChargeMode{}
	for _, d := range devices {
		mode, ok := cache[d.StationID]
		if !ok {
			found, exists, err := stationModeOf(tx, d.StationID)
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
// errMeteringBlocked 表示这台设备进不了这个站点：站点在跑的计费方式它测不了。
type errMeteringBlocked struct {
	device  string             // 进不去的设备号
	station uint64             // 目标站点 id
	mode    pricing.ChargeMode // 目标站点默认的计费方式
	reason  string             // 缺哪项能力的中文说明
}

// Error 拼出直接展示给运营的拒绝理由，点明是哪台设备、缺什么能力。
func (e *errMeteringBlocked) Error() string {
	return "设备 " + e.device + " 无法执行该站点的计费方式：" + e.reason
}

// newDevicesOnly drops the boards that already exist, so a check about what a
// board would start running applies only to boards that are about to start.
// newDevicesOnly 只留下库里还没有的设备：已经在站的设备不会因为这次导入改变计费方式，
// 不该被"能不能跑这个费率"的校验拦下来。
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
