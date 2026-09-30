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

// 一块板子能报什么，是它的固件决定的，不是运营决定的。
//
// 厂商协议在结束充电帧里带一个电量读数，
// 在计量帧里带一张分段功率表。帧里两者都
// 没有的桩，压根没法按 kWh 或按功率
// 计费——只能告诉它要跑多久。照样给这样
// 的桩下发一份费率，就是那种几周后以一笔
// 对不上的结算才暴露的错误，所以能力按设
// 备记录下来，在发布费率之前先校验一遍。
//
// 默认是拒绝。能力从未声明过的设备一律当作什么
// 都不报，因为新协议不承诺它没有明确携带的字段。

// deviceCapability 是一块板子当前声明的计量能力，映射 device_meta 的四列。
// 默认一律视为"不上报"：没声明过能力的设备，等于什么都不报，因为新协议不承诺
// 它没有明确携带的字段。
type deviceCapability struct {
	DeviceID              string `gorm:"column:device_id"`               // 设备号，全网唯一，是这块板子的标识
	ChargeMode            string `gorm:"column:charge_mode"`             // 设备充电类型（厂商协议值），空串表示尚未归类；另有取值 none 仅用于任务日志
	ReportsEnergy         bool   `gorm:"column:reports_energy"`          // 是否在结束充电帧里上报电量，决定能否按电量计费
	ReportsSegmentedPower bool   `gorm:"column:reports_segmented_power"` // 是否上报分段功率表，决定能否按功率计费
}

// capabilityBlock 给出这块设备跑
// 不了这个计费方式的原因，能跑时返回空串。时长计
// 费永远放行——板子自己计分钟数、自己停，不需要
// 平台测到什么；其余四种都落在电量或分段功率上。
// 拒绝理由是写给运营看的：只回一句「不支持」的拒
// 绝，会被人当成检查太严，直接把检查关掉绕过去。
func capabilityBlock(mode pricing.ChargeMode, cap deviceCapability) string {
	switch mode {
	case pricing.ModeDeviceDuration:
		// 板子自己数自己的分钟数、自己停。我们这边什么都不测，
		// 所以也就没有什么需要它上报的。
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

// modeNeverSet 标记
// 「改这次费率之前既没有计费规则、
// 没有声明过充电类型」的设备。它只
// 是任务日志里的一个值，不是计费方
// 式，永远不会下发到设备协议里去。
const modeNeverSet = "none"

// switchTarget 是一次
// 费率切换要下发到的一台设备，连同
// 它现在跑的计费方式和它能测到什
// 么。能力随行携带而不是二次查询，
// 证能力校验和任务日志说的是同一批
// 板子、同一时刻读到的快照。
type switchTarget struct {
	DeviceID string           // 设备号
	Before   string           // 切换前实际生效的计费方式；从未设定过时为 modeNeverSet
	Cap      deviceCapability // 该设备的计量能力声明
}

// resolveSwitchTargets 列出这
// 次发布范围内受影响的设备，以及每台当前正在跑的计
// 费方式。deviceID 为空表示整个站点，否则
// 只看这一台。设备级规则优先，没有自己的规则就落在
// 站点默认规则上，所以 before 是站点默认的
// 计费方式而不是空值；连规则都没有时退回设备上自己
// 声明的充电类型，再没有才标成modeNeverSet。
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
	// running 按 device_meta.
	// device_id 的写法索引当前生效
	// 的计费方式，这样跑在站点默认上的设备也
	// 能报出这个默认值，而不是空值。
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
			// 没有任何规则覆盖这台设备。运营在设备上
			// 自己声明的充电类型是次优答案；一台从没
			// 被归过类的设备拿到的是一个标记，而不是
			// 马上要设上去的计费方式——否则第一次应
			// 用就会看起来像是从它自己改成了它自己。
			before = row.ChargeMode
		}
		if before == "" {
			before = modeNeverSet
		}
		targets = append(targets, switchTarget{DeviceID: row.DeviceID, Before: before, Cap: row})
	}
	return targets, nil
}

// validDeviceChargeMode 校验设备上记录
// 的充电类型是否属于
// 「服务端计费/设备计费」两类的六种
// 计费方式之一。协议里叫得出名字、但
// 计费引擎算不出价的类型，在这里就拒
// 掉，而不是先存下来，等到要出账、要
// 产出结算的那天才发现。
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

// checkMetering 挑出跑
// 不了这个计费方式的目标设备，每台都
// 带上设备号和原因——整站发布时如果
// 悄悄跳过三块没有电表的板子，就会有
// 三个桩还按旧费率在充，而任何一份日
// 志里都没有一句话说得清这件事。
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
	// ReportsEnergy、ReportsSegmentedPower 这两项能力
	// 用指针传，是为了让表单
	// 漏填其中一项时直接报成参数错误，
	// 而不是把运营根本没看过的那项
	// 能力默默降级成 false。
	ReportsEnergy         *bool `json:"reports_energy"`          // 是否上报电量；必填，nil 视为未申报
	ReportsSegmentedPower *bool `json:"reports_segmented_power"` // 是否上报分段功率；必填，nil 视为未申报
}

// updateDeviceMetering 申报某块板子能上报什么。
// 设备必须已存在（这里只改能力，不建设备），改前快照
// 与改动内容在同一事务内写审计——这是这份信息唯一靠
// 人手填进来的地方，所以和其它任何改变设备行为的写入
// 一样要留痕。
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
	// 充电类型是厂商协议里的一个取值，不是随便写的字符串。
	// 留空记的是「这块板子还没归类」，和「已归成某个常规
	// 类型」是两回事。
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

// stationModeOf 取出站点默认规则
// 当前生效的计费方式；站点还没有生效的默认规则
// 时返回 false，口径 JSON 解析不出
// 来也按「没有」处理，宁可让上层跳过这次校验。
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

// checkImportAgainstStation 在导入设备时校
// 验：设备要加入的站点
// 若已按电量或功率计费，这块板子必须声
// 明了对应的计量能力，否则直接报错。能
// 力检查必须放在设备进门的地方做，因为
// 发布时那次只覆盖了当时已存在的板子，
// 而硬件到位前就先定好价钱的站点是常
// 事，半年后才到货的那块板子从没经过那
// 检查——不在这儿拦下来，站点就会按它
// 压根测不到的费率收钱。
//
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

// errMeteringBlocked 表示这台设备进不了这个站点：站点在跑的
// 计费方式它测不了。这条消息会原样送到运营眼前，所以必须点明
// 是哪台设备、该怎么处理。
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

// newDevicesOnly 只留
// 下库里还没有的设备：已经在站的设备
// 不会因为这次导入改变计费方式，不该
// 被「能不能跑这个费率」的校验拦下来。
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
