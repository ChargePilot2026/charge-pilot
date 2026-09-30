package admin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AlertRuleRow 是告警规则（alert_rule 表）的列表行映射。
// 告警规则由客户运营在 PC 后台自助配置，alerts worker 按它把网关遥测转成告警事件。
type AlertRuleRow struct {
	ID              uint64 `json:"id"`                   // 规则主键
	Name            string `json:"name"`                 // 规则名称，最长 128 个字符
	DeviceIDPattern string `json:"device_id_pattern"`    // 设备匹配模式：整串设备号，或用 `*` 通配（如 `GW-*`）；`*` 表示匹配全部设备
	Metric          string `json:"metric"`               // 监控指标：voltage_v 电压 / current_a 电流 / temperature_c 温度 / battery_soc 电量 / power_w 功率 / meter_kwh 电量
	Op              string `json:"op"`                   // 比较符：> < >= <= == != between（between 时阈值必须是 [下限, 上限]）
	Threshold       any    `json:"threshold"`            // 阈值：普通比较符是一个数字，between 时是 [下限, 上限] 两个数字
	WindowSeconds   uint32 `json:"window_seconds"`       // 判定窗口秒数，上限 86400（1 天）；0 表示不做时间窗约束
	Severity        string `json:"severity"`             // 严重级别：warning / critical / fatal，决定订阅方怎么分级通知
	Enabled         bool   `json:"enabled"`              // 是否启用；停用的规则不再参与评估
	OpenAlerts      int64  `json:"open_alerts" gorm:"-"` // 该规则当前未闭环的告警数（active + acknowledged），列表时另行统计，非表字段
}

// AlertSubscriptionRow 是告警订阅（alert_subscription 表）的行映射：
// 决定一条告警发给谁——按规则、按严重级别，落到 Webhook 订阅或指定管理员账号。
// 前三个筛选条件都是可空的，空表示这一维不限制。
type AlertSubscriptionRow struct {
	ID                    uint64  `json:"id"`                      // 订阅主键
	RuleID                *uint64 `json:"rule_id"`                 // 只接收该规则的告警；nil 表示不限规则
	Severity              *string `json:"severity"`                // 只接收该严重级别的告警（warning/critical/fatal）；nil 表示不限级别
	WebhookSubscriptionID *uint64 `json:"webhook_subscription_id"` // 推送到该 Webhook 订阅；nil 表示不走 Webhook
	AdminUserID           *uint64 `json:"admin_user_id"`           // 站内信通知该管理员账号；nil 表示不发站内信
	Enabled               bool    `json:"enabled"`                 // 是否启用
}

// registerAlertRules 注册告警规则、告警订阅、告警消解和风控配置的后台接口。
// 注意"人工消解某条规则的全部告警"用的是 alert.ack 权限，与删除规则的权限分开。
func (a ResourceAPI) registerAlertRules(r *gin.Engine) {
	r.GET("/api/v1/admin/alert-rules", a.Auth.Require("alert.read"), a.alertRules)
	r.POST("/api/v1/admin/alert-rules", a.Auth.Require("alert.rule.create"), a.createAlertRule)
	r.PUT("/api/v1/admin/alert-rules/:id", a.Auth.Require("alert.rule.update"), a.updateAlertRule)
	r.DELETE("/api/v1/admin/alert-rules/:id", a.Auth.Require("alert.rule.delete"), a.deleteAlertRule)
	r.GET("/api/v1/admin/alert-subscriptions", a.Auth.Require("alert.read"), a.alertSubscriptions)
	r.POST("/api/v1/admin/alert-subscriptions", a.Auth.Require("alert.subscription.create"), a.createAlertSubscription)
	r.DELETE("/api/v1/admin/alert-subscriptions/:id", a.Auth.Require("alert.subscription.create"), a.deleteAlertSubscription)
	r.POST("/api/v1/admin/alert-rules/:id/resolve", a.Auth.Require("alert.ack"), a.resolveAlertRuleAlerts)
	r.PUT("/api/v1/admin/risk-config", a.Auth.Require("alert.risk_config.update"), a.saveRiskConfig)
}

// alertMetrics 是允许配置告警的指标白名单：
// 只有网关实际上报的这些量才能配规则，避免写进去一个永远不会有样本的指标。
var alertMetrics = map[string]bool{
	"voltage_v": true, "current_a": true, "temperature_c": true,
	"battery_soc": true, "power_w": true, "meter_kwh": true,
}

// alertRules 分页返回告警规则列表，支持按关键词、enabled 过滤。
// 阈值是 JSON 列（数字或 [下限, 上限]），GORM 无法直接扫进 any 字段，所以单独查一次补上；
// 同时统计每条规则当前未闭环的告警数，让运营一眼看出哪条规则正在刷屏。
func (a ResourceAPI) alertRules(c *gin.Context) {
	page, ok := parsePage(c, "")
	if !ok {
		return
	}
	out := Page[AlertRuleRow]{Items: []AlertRuleRow{}, Page: page.Page, PageSize: page.PageSize}
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("alert_rule").Where("deleted_at IS NULL")
	if page.Keyword != "" {
		query = query.Where("name LIKE ? OR metric LIKE ?", likePattern(page.Keyword), likePattern(page.Keyword))
	}
	if raw := c.Query("enabled"); raw == "true" || raw == "false" {
		query = query.Where("enabled = ?", raw == "true")
	}
	if err := query.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := query.Order("id DESC").Offset((page.Page - 1) * page.PageSize).Limit(page.PageSize).Find(&out.Items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	// Threshold is a JSON column holding either a number or a [low, high]
	// pair, so it is fetched separately: GORM cannot scan it into an any-typed
	// row field. Casting to CHAR keeps the driver from handing back a blob.
	thresholds := map[uint64][]byte{}
	ids := make([]uint64, 0, len(out.Items))
	for _, row := range out.Items {
		ids = append(ids, row.ID)
	}
	if len(ids) > 0 {
		var stored []struct {
			ID        uint64 `gorm:"column:id"`
			Threshold []byte `gorm:"column:threshold"`
		}
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("alert_rule").
			Select("id, CAST(threshold AS CHAR) AS threshold").
			Where("id IN ?", ids).Find(&stored).Error; err == nil {
			for _, row := range stored {
				thresholds[row.ID] = row.Threshold
			}
		}
	}
	openCounts := map[uint64]int64{}
	if len(ids) > 0 {
		var counts []struct {
			RuleID uint64 `gorm:"column:rule_id"`
			Total  int64  `gorm:"column:total"`
		}
		if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("alert_event").
			Select("rule_id, COUNT(*) AS total").
			Where("rule_id IN ? AND status IN ('active','acknowledged')", ids).
			Group("rule_id").Find(&counts).Error; err == nil {
			for _, row := range counts {
				openCounts[row.RuleID] = row.Total
			}
		}
	}
	for i := range out.Items {
		out.Items[i].Enabled = normalizeBool(out.Items[i].Enabled)
		out.Items[i].OpenAlerts = openCounts[out.Items[i].ID]
		if raw, present := thresholds[out.Items[i].ID]; present && len(raw) > 0 {
			out.Items[i].Threshold = json.RawMessage(raw)
		}
	}
	httpapi.OK(c, out)
}

// normalizeBool 归一化 MySQL 驱动返回的布尔列（TINYINT），保证接口吐出来的是 true/false 而不是 0/1。
func normalizeBool(value bool) bool { return value }

// alertRuleInput is shared by create and update so both validate identically.

// alertRuleInput 是告警规则的创建与修改入参，两者共用一份结构以保证校验口径一致。
type alertRuleInput struct {
	Name            string `json:"name"`              // 规则名称，非空且不超过 128 个字符
	DeviceIDPattern string `json:"device_id_pattern"` // 设备匹配模式，非空、不超过 128 个字符；`*` 表示全部设备
	Metric          string `json:"metric"`            // 监控指标，必须在 alertMetrics 白名单里
	Op              string `json:"op"`                // 比较符：> < >= <= == != between
	Threshold       any    `json:"threshold"`         // 阈值：between 时为 [下限, 上限]，其余为单个数字
	WindowSeconds   uint32 `json:"window_seconds"`    // 判定窗口秒数，上限 86400
	Severity        string `json:"severity"`          // 严重级别：warning / critical / fatal
	// Enabled 用指针区分"没传这个字段"和"明确要置为 false"：
	// 修改时只有显式传了才改，没传就保持原值。
	Enabled *bool `json:"enabled"`
}

// validate 校验告警规则入参，返回归一化后的阈值 JSON 文本。
// 阈值在这里被提前解析并按比较符定型，保证 evaluator 之后一定能读懂它，
// 不会出现"规则存进去了却永远不触发"的情况。
func (in alertRuleInput) validate() (string, bool) {
	if !validText(in.Name, 128) || !alertMetrics[in.Metric] {
		return "", false
	}
	if !oneOf(in.Op, "> < >= <= == != between") || !oneOf(in.Severity, "warning critical fatal") {
		return "", false
	}
	if in.DeviceIDPattern == "" || utf8.RuneCountInString(in.DeviceIDPattern) > 128 {
		return "", false
	}
	if in.WindowSeconds > 86400 {
		return "", false
	}
	encoded, err := encodeThreshold(in.Threshold, in.Op)
	if err != nil {
		return "", false
	}
	return encoded, true
}

// encodeThreshold validates the value the evaluator will later parse, so an
// unparseable rule can never be saved and silently never fire.

// encodeThreshold 按比较符把阈值定型成 evaluator 能解析的 JSON 文本：
// between 必须是低 ≤ 高的一对数字，其余比较符必须是单个数字。
func encodeThreshold(value any, op string) (string, error) {
	if op == "between" {
		pair, ok := value.([]any)
		if !ok || len(pair) != 2 {
			return "", fmt.Errorf("between requires [low, high]")
		}
		low, lowOK := toFloat(pair[0])
		high, highOK := toFloat(pair[1])
		if !lowOK || !highOK || low > high {
			return "", fmt.Errorf("between requires an ordered pair")
		}
		return string(mustJSON([]float64{low, high})), nil
	}
	single, ok := toFloat(value)
	if !ok {
		return "", fmt.Errorf("threshold must be a number")
	}
	return string(mustJSON(single)), nil
}

// mustJSON encodes a validated number or pair; marshalling those types cannot
// fail, so an error here would be a programming mistake rather than input.

// mustJSON 序列化已经校验过的数字或数字对；这两种类型序列化不可能失败，
// 万一失败属于程序错误而不是入参问题，兜底成 null。
func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("null")
	}
	return encoded
}

// toFloat 把 JSON 反序列化出来的宽泛类型收敛成 float64。
// 数字正常返回 true；布尔、字符串、对象等非数字值返回 false，由调用方按入参错误拒绝。
func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		parsed, err := v.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

// createAlertRule 新建一条告警规则。设备匹配模式留空时存成 `*`（匹配全部设备），
// enabled 不传时默认启用。写入与审计在同一事务完成。
func (a ResourceAPI) createAlertRule(c *gin.Context) {
	var in alertRuleInput
	if !decodeResource(c, &in) {
		return
	}
	threshold, ok := in.validate()
	if !ok {
		httpapi.BadRequest(c, "请填写有效的名称、指标、比较符、阈值、严重级别和设备匹配模式")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	pattern := in.DeviceIDPattern
	if pattern == "" {
		pattern = "*"
	}
	var id uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("alert_rule").Create(map[string]any{
			"name": in.Name, "device_id_pattern": pattern, "metric": in.Metric, "op": in.Op,
			"threshold": threshold, "window_seconds": in.WindowSeconds, "severity": in.Severity, "enabled": enabled,
		}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "create", "alert_rule", id, nil, in, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// updateAlertRule 修改告警规则的指标、比较符、阈值、窗口、严重级别和设备匹配模式。
// enabled 是"传了才改"；这里没有版本乐观锁，与告警规则这种可随时调整的配置定位一致。
func (a ResourceAPI) updateAlertRule(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in alertRuleInput
	if !decodeResource(c, &in) {
		return
	}
	threshold, valid := in.validate()
	if !valid {
		httpapi.BadRequest(c, "请填写有效的名称、指标、比较符、阈值、严重级别和设备匹配模式")
		return
	}
	values := map[string]any{
		"name": in.Name, "device_id_pattern": in.DeviceIDPattern, "metric": in.Metric, "op": in.Op,
		"threshold": threshold, "window_seconds": in.WindowSeconds, "severity": in.Severity,
	}
	if in.Enabled != nil {
		values["enabled"] = *in.Enabled
	}
	var before map[string]any
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("alert_rule").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", id).Take(&before).Error; err != nil {
			return err
		}
		if err := tx.Table("alert_rule").Where("id = ?", id).Updates(values).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "update", "alert_rule", id, before, in, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// deleteAlertRule 软删除告警规则并同时置为停用：
// 已经产生的告警事件保留不动，只是不再评估这条规则。
func (a ResourceAPI) deleteAlertRule(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	_, ok = a.adminWrite(c, "alert_rule", "delete", id, map[string]any{"deleted_at": time.Now().UTC(), "enabled": false})
	if ok {
		httpapi.OK(c, gin.H{"id": id, "deleted": true})
	}
}

// alertSubscriptions 返回全部告警订阅，按 ID 倒序。订阅是配置类数据，只有启用/停用，没有软删除。
func (a ResourceAPI) alertSubscriptions(c *gin.Context) {
	rows := []AlertSubscriptionRow{}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("alert_subscription").Order("id DESC").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

// createAlertSubscription 新建一条告警订阅。两条硬约束：
// 订阅必须能送达某人（Webhook 订阅或管理员账号至少有一个），也必须限定一个触发范围（规则或严重级别至少有一个），
// 否则它要么无处可发要么会收到全部告警。引用的规则、Webhook、管理员账号都要存在且有效，否则返回冲突。
func (a ResourceAPI) createAlertSubscription(c *gin.Context) {
	var in struct {
		RuleID                *uint64 `json:"rule_id"`
		Severity              *string `json:"severity"`
		WebhookSubscriptionID *uint64 `json:"webhook_subscription_id"`
		AdminUserID           *uint64 `json:"admin_user_id"`
		Enabled               *bool   `json:"enabled"`
	}
	if !decodeResource(c, &in) {
		return
	}
	// A subscription must reach somebody: a webhook or a named operator.
	if in.WebhookSubscriptionID == nil && in.AdminUserID == nil {
		httpapi.BadRequest(c, "请至少指定一个 Webhook 订阅或接收账号")
		return
	}
	if in.RuleID == nil && in.Severity == nil {
		httpapi.BadRequest(c, "请指定规则或严重级别")
		return
	}
	if in.Severity != nil && !oneOf(*in.Severity, "warning critical fatal") {
		httpapi.BadRequest(c, "严重级别无效")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	var id uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if in.RuleID != nil {
			var exists int64
			if err := tx.Table("alert_rule").Where("id = ? AND deleted_at IS NULL", *in.RuleID).Count(&exists).Error; err != nil {
				return err
			}
			if exists == 0 {
				return errConflict
			}
		}
		if in.WebhookSubscriptionID != nil {
			var exists int64
			if err := tx.Table("webhook_subscription").Where("id = ? AND deleted_at IS NULL", *in.WebhookSubscriptionID).Count(&exists).Error; err != nil {
				return err
			}
			if exists == 0 {
				return errConflict
			}
		}
		if in.AdminUserID != nil {
			var exists int64
			if err := tx.Table("admin_user_role").Where("id = ? AND deleted_at IS NULL AND status = 'active'", *in.AdminUserID).Count(&exists).Error; err != nil {
				return err
			}
			if exists == 0 {
				return errConflict
			}
		}
		if err := tx.Table("alert_subscription").Create(map[string]any{
			"rule_id": in.RuleID, "severity": in.Severity, "webhook_subscription_id": in.WebhookSubscriptionID,
			"admin_user_id": in.AdminUserID, "enabled": enabled,
		}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "create", "alert_subscription", id, nil, in, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}

// deleteAlertSubscription 物理删除一条告警订阅（订阅属于配置类数据，不留软删痕迹），
// 订阅不存在时按"记录未找到"处理。
func (a ResourceAPI) deleteAlertSubscription(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var exists int64
		if err := tx.Table("alert_subscription").Where("id = ?", id).Count(&exists).Error; err != nil {
			return err
		}
		if exists == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Table("alert_subscription").Where("id = ?", id).Delete(nil).Error
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "deleted": true})
}

// resolveAlertRuleAlerts closes every open alert raised by one rule, which is
// what an operator does after fixing a site or adjusting a threshold.

// resolveAlertRuleAlerts 把某条规则下所有未闭环的告警（active / acknowledged）一次性标记为 resolved，
// 用于现场修好之后或调完阈值之后的整体消解。备注留空时默认写"运维人工关闭"，写入与审计同事务。
func (a ResourceAPI) resolveAlertRuleAlerts(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if utf8.RuneCountInString(in.Note) > 255 {
		httpapi.BadRequest(c, "备注过长")
		return
	}
	note := strings.TrimSpace(in.Note)
	if note == "" {
		note = "运维人工关闭"
	}
	profile := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var ruleExists int64
		if err := tx.Table("alert_rule").Where("id = ? AND deleted_at IS NULL", id).Count(&ruleExists).Error; err != nil {
			return err
		}
		if ruleExists == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Table("alert_event").Where("rule_id = ? AND status IN ('active','acknowledged')", id).
			Updates(map[string]any{"status": "resolved", "resolved_at": gorm.Expr("UTC_TIMESTAMP(3)"), "note": note}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "resolve", "alert_rule", id, nil, gin.H{"note": note}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "resolved": true})
}

// saveRiskConfig upserts one tunable by key so operators can adjust thresholds
// that the code does not hardcode.

// saveRiskConfig 按键新增或覆盖一条风控配置（频次、金额阈值等代码里没有写死的可调项），
// 值以 JSON 原文存储。存在则整体覆盖，不存在则插入，并记审计。
func (a ResourceAPI) saveRiskConfig(c *gin.Context) {
	var in struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Key, 64) || in.Value == nil {
		httpapi.BadRequest(c, "请填写有效的风控配置键和值")
		return
	}
	encoded, err := json.Marshal(in.Value)
	if err != nil {
		httpapi.BadRequest(c, "配置值无法序列化")
		return
	}
	profile := c.MustGet("admin_profile").(Profile)
	err = a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("risk_config").Clauses(clause.OnConflict{UpdateAll: true}).
			Create(map[string]any{"key": in.Key, "value": string(encoded), "updated_by": profile.ID}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, profile, "update", "risk_config", 0, nil, in, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"key": in.Key})
}
