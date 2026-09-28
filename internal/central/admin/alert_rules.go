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

type AlertRuleRow struct {
	ID              uint64 `json:"id"`
	Name            string `json:"name"`
	DeviceIDPattern string `json:"device_id_pattern"`
	Metric          string `json:"metric"`
	Op              string `json:"op"`
	Threshold       any    `json:"threshold"`
	WindowSeconds   uint32 `json:"window_seconds"`
	Severity        string `json:"severity"`
	Enabled         bool   `json:"enabled"`
	OpenAlerts      int64  `json:"open_alerts" gorm:"-"`
}

type AlertSubscriptionRow struct {
	ID                    uint64  `json:"id"`
	RuleID                *uint64 `json:"rule_id"`
	Severity              *string `json:"severity"`
	WebhookSubscriptionID *uint64 `json:"webhook_subscription_id"`
	AdminUserID           *uint64 `json:"admin_user_id"`
	Enabled               bool    `json:"enabled"`
}

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

var alertMetrics = map[string]bool{
	"voltage_v": true, "current_a": true, "temperature_c": true,
	"battery_soc": true, "power_w": true, "meter_kwh": true,
}

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

func normalizeBool(value bool) bool { return value }

// alertRuleInput is shared by create and update so both validate identically.
type alertRuleInput struct {
	Name            string `json:"name"`
	DeviceIDPattern string `json:"device_id_pattern"`
	Metric          string `json:"metric"`
	Op              string `json:"op"`
	Threshold       any    `json:"threshold"`
	WindowSeconds   uint32 `json:"window_seconds"`
	Severity        string `json:"severity"`
	Enabled         *bool  `json:"enabled"`
}

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
func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("null")
	}
	return encoded
}

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

func (a ResourceAPI) alertSubscriptions(c *gin.Context) {
	rows := []AlertSubscriptionRow{}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("alert_subscription").Order("id DESC").Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": rows})
}

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
