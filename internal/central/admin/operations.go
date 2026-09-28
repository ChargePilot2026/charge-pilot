package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errConflict = errors.New("当前记录状态不允许此操作，请刷新后重试")

func (a ResourceAPI) registerOperations(r *gin.Engine) {
	get := func(path, perm, table, columns string, soft bool) {
		r.GET("/api/v1/admin/"+path, a.Auth.Require(perm), func(c *gin.Context) {
			rows := []map[string]any{}
			q := a.Store.AdminDB.WithContext(c.Request.Context()).Table(table).Select(columns)
			if soft {
				q = q.Where("deleted_at IS NULL")
			}
			if err := q.Order("id DESC").Find(&rows).Error; err != nil {
				resourceFailure(c, err)
				return
			}
			normalizeRows(rows)
			httpapi.OK(c, gin.H{"items": rows})
		})
	}
	get("users", "admin_user.read", "admin_user_role", "id,username,display_name,role_id,status", true)
	get("roles", "admin_user.read", "role", "id,code,name", true)
	get("alerts", "alert.read", "alert_event", "id,device_id,severity,metric,status,created_at", false)
	get("announcements", "announcement.read", "announcement", "id,title,content,scope,target_ids,status,start_at,end_at", true)
	get("customer-service", "customer_service.read", "customer_service_config", "id,agent_wechat,agent_name,path,priority,enabled,working_hours_json", false)
	get("webhooks", "webhook.read", "webhook_subscription", "id,name,url,enabled,event_types,LEFT(secret,8) AS secret_prefix", true)
	get("ota/packages", "ota.read", "ota_package", "id,code,version,size_bytes,status", true)
	get("ota/schedules", "ota.read", "ota_schedule", "id,package_id,rollout_strategy,status,scheduled_at", false)
	get("billing/settlements", "finance.read", "settled_record", "id,settlement_no,period_start,period_end,total_cents,status", false)
	r.POST("/api/v1/admin/users", a.Auth.Require("admin_user.create"), a.createUser)
	r.POST("/api/v1/admin/alerts/:id/ack", a.Auth.Require("alert.ack"), a.ackAlert)
	r.POST("/api/v1/admin/announcements", a.Auth.Require("announcement.create"), a.createAnnouncement)
	r.POST("/api/v1/admin/customer-service", a.Auth.Require("customer_service.create"), a.saveSeat)
	r.PUT("/api/v1/admin/customer-service/:id", a.Auth.Require("customer_service.update"), a.saveSeat)
	r.DELETE("/api/v1/admin/customer-service/:id", a.Auth.Require("customer_service.delete"), a.disableSeat)
	r.POST("/api/v1/admin/webhooks", a.Auth.Require("webhook.create"), a.createWebhook)
	r.GET("/api/v1/admin/whitelabel", a.Auth.Require("whitelabel.read"), a.whitelabel)
	r.PUT("/api/v1/admin/whitelabel", a.Auth.Require("whitelabel.update"), a.saveWhitelabel)
}

// MySQL returns JSON and DECIMAL map columns as bytes. Normalize explicitly so
// JSON values reach browsers as arrays/objects instead of base64 strings.
func normalizeRows(rows []map[string]any) {
	for _, row := range rows {
		for k, v := range row {
			if b, ok := v.([]byte); ok {
				v = string(b)
				row[k] = v
			}
			if k == "event_types" || k == "target_ids" || strings.HasSuffix(k, "_json") {
				if s, ok := v.(string); ok {
					var x any
					if json.Unmarshal([]byte(s), &x) == nil {
						row[k] = x
					}
				}
			}
			if k == "enabled" {
				switch n := v.(type) {
				case int64:
					row[k] = n != 0
				case uint64:
					row[k] = n != 0
				case int8:
					row[k] = n != 0
				}
			}
		}
	}
}
func validText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}
func httpsURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && len(s) <= 512
}
func (a ResourceAPI) adminWrite(c *gin.Context, table, action string, id uint64, values map[string]any) (uint64, bool) {
	p := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var before map[string]any
		if id != 0 {
			if err := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&before).Error; err != nil {
				return err
			}
			if err := tx.Table(table).Where("id = ?", id).Updates(values).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Table(table).Create(values).Error; err != nil {
				return err
			}
			if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
				return err
			}
		}
		return resourceAudit(tx, p, action, table, id, before, values, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return 0, false
	}
	return id, true
}
func (a ResourceAPI) createUser(c *gin.Context) {
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		RoleID      uint64 `json:"role_id"`
		Phone       string `json:"phone"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`).MatchString(in.Username) || len(in.Password) < 12 || len(in.Password) > 72 || utf8.RuneCountInString(in.DisplayName) > 64 || len(in.Phone) > 32 || in.RoleID == 0 {
		httpapi.BadRequest(c, "用户名须为 3–64 位英文/数字，密码须为 12–72 字节，并选择有效角色")
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	var codes []string
	if err := a.Store.AdminDB.Table("role AS r").Joins("JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id").Where("r.id=? AND r.deleted_at IS NULL", in.RoleID).Pluck("p.code", &codes).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if len(codes) == 0 {
		httpapi.BadRequest(c, "角色不存在或没有任何权限")
		return
	}
	for _, code := range codes {
		if !hasPermission(p, code) {
			httpapi.Write(c, 403, 1003, "不能授予自己未拥有的权限", nil)
			return
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		resourceFailure(c, err)
		return
	}
	var id uint64
	err = a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		row := map[string]any{"username": in.Username, "display_name": in.DisplayName, "password_hash": string(hash), "phone": in.Phone, "role_id": in.RoleID, "status": "active"}
		if err := tx.Table("admin_user_role").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, p, "create", "admin_user", id, nil, gin.H{"username": in.Username, "role_id": in.RoleID}, c.ClientIP(), c.GetHeader("X-Request-ID"))
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id})
}
func (a ResourceAPI) ackAlert(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row struct{ Status string }
		if err := tx.Table("alert_event").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; err != nil {
			return err
		}
		if row.Status == "acknowledged" {
			return nil
		}
		if row.Status != "active" {
			return errConflict
		}
		if err := tx.Table("alert_event").Where("id=?", id).Updates(map[string]any{"status": "acknowledged", "acked_by": p.ID, "acked_at": time.Now()}).Error; err != nil {
			return err
		}
		return resourceAudit(tx, p, "ack", "alert", id, row, gin.H{"status": "acknowledged"}, c.ClientIP(), "")
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"acknowledged": true})
}
func (a ResourceAPI) createAnnouncement(c *gin.Context) {
	var in struct {
		Title     string     `json:"title"`
		Content   string     `json:"content"`
		Scope     string     `json:"scope"`
		TargetIDs []string   `json:"target_ids"`
		StartAt   time.Time  `json:"start_at"`
		EndAt     *time.Time `json:"end_at"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Title, 128) || !validText(in.Content, 20000) || in.Scope == "" || !oneOf(in.Scope, "global station city") || in.StartAt.IsZero() || (in.EndAt != nil && !in.EndAt.After(in.StartAt)) || (in.Scope != "global" && len(in.TargetIDs) == 0) {
		httpapi.BadRequest(c, "请填写标题、内容、有效时间和公告目标")
		return
	}
	if len(in.TargetIDs) > 100 {
		httpapi.BadRequest(c, "公告目标最多 100 项")
		return
	}
	for _, t := range in.TargetIDs {
		if !validText(t, 64) {
			httpapi.BadRequest(c, "公告目标无效")
			return
		}
	}
	targets, _ := json.Marshal(in.TargetIDs)
	id, ok := a.adminWrite(c, "announcement", "create", 0, map[string]any{"title": in.Title, "content": in.Content, "scope": in.Scope, "target_ids": string(targets), "start_at": in.StartAt, "end_at": in.EndAt, "status": "published", "created_by": c.MustGet("admin_profile").(Profile).ID})
	if ok {
		httpapi.OK(c, gin.H{"id": id, "status": "published"})
	}
}
func (a ResourceAPI) saveSeat(c *gin.Context) {
	var in struct {
		AgentWechat string  `json:"agent_wechat"`
		AgentName   *string `json:"agent_name"`
		Path        *string `json:"path"`
		Priority    uint32  `json:"priority"`
		Enabled     bool    `json:"enabled"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.AgentWechat, 64) || (in.AgentName != nil && utf8.RuneCountInString(*in.AgentName) > 64) || (in.Path != nil && !httpsURL(*in.Path)) {
		httpapi.BadRequest(c, "请填写客服微信号及有效 HTTPS 入口")
		return
	}
	var id uint64
	var ok bool
	if c.Param("id") != "" {
		id, ok = pathID(c)
		if !ok {
			return
		}
	}
	id, ok = a.adminWrite(c, "customer_service_config", "save", id, map[string]any{"agent_wechat": in.AgentWechat, "agent_name": in.AgentName, "path": in.Path, "priority": in.Priority, "enabled": in.Enabled})
	if ok {
		httpapi.OK(c, gin.H{"id": id})
	}
}
func (a ResourceAPI) disableSeat(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	_, ok = a.adminWrite(c, "customer_service_config", "disable", id, map[string]any{"enabled": false})
	if ok {
		httpapi.OK(c, gin.H{"id": id, "enabled": false})
	}
}
func (a ResourceAPI) createWebhook(c *gin.Context) {
	var in struct {
		Name       string   `json:"name"`
		URL        string   `json:"url"`
		EventTypes []string `json:"event_types"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Name, 64) || !httpsURL(in.URL) || len(in.EventTypes) == 0 || len(in.EventTypes) > 4 {
		httpapi.BadRequest(c, "请填写名称、HTTPS URL 和订阅事件")
		return
	}
	seen := map[string]bool{}
	for _, v := range in.EventTypes {
		if v == "" || !oneOf(v, "alert charge_ended refund_completed ota_completed") || seen[v] {
			httpapi.BadRequest(c, "订阅事件无效或重复")
			return
		}
		seen[v] = true
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		resourceFailure(c, err)
		return
	}
	secret := hex.EncodeToString(b)
	events, _ := json.Marshal(in.EventTypes)
	var id uint64
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("webhook_subscription").Create(map[string]any{"name": in.Name, "url": in.URL, "event_types": string(events), "secret": secret, "enabled": true}).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "create", "webhook", id, nil, in, c.ClientIP(), "")
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"id": id, "secret": secret})
}

var whiteKeys = map[string]int{"miniprogram_name": 64, "miniprogram_logo_url": 512, "admin_logo_url": 512, "theme_color": 9, "service_phone": 32, "service_wechat_id": 64, "icp_record_no": 128, "custom_domain": 253, "agreement_url": 512, "privacy_url": 512, "about_us": 20000}

func (a ResourceAPI) whiteRead(c *gin.Context) (map[string]string, error) {
	var row struct{ ConfigJSON *string }
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("whitelabel_config").Where("id=1").Take(&row).Error
	config := map[string]string{"miniprogram_name": "ChargePilot", "theme_color": "#1677ff"}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return config, nil
	}
	if err != nil {
		return nil, err
	}
	if row.ConfigJSON != nil {
		var saved map[string]string
		if err = json.Unmarshal([]byte(*row.ConfigJSON), &saved); err != nil {
			return nil, err
		}
		for k, v := range saved {
			if _, ok := whiteKeys[k]; ok {
				config[k] = v
			}
		}
	}
	return config, nil
}
func (a ResourceAPI) whitelabel(c *gin.Context) {
	v, e := a.whiteRead(c)
	if e != nil {
		resourceFailure(c, e)
		return
	}
	httpapi.OK(c, v)
}
func (a ResourceAPI) saveWhitelabel(c *gin.Context) {
	in := map[string]string{}
	if !decodeResource(c, &in) {
		return
	}
	for k, v := range in {
		max, ok := whiteKeys[k]
		if !ok || utf8.RuneCountInString(v) > max || (strings.HasSuffix(k, "_url") && v != "" && !httpsURL(v)) {
			httpapi.BadRequest(c, "配置字段、长度或 HTTPS 链接无效")
			return
		}
	}
	if !validText(in["miniprogram_name"], 64) || !regexp.MustCompile(`^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$`).MatchString(in["theme_color"]) {
		httpapi.BadRequest(c, "请填写小程序名称和有效主题色")
		return
	}
	if d := in["custom_domain"]; d != "" && !regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]*\.)+[A-Za-z]{2,63}$`).MatchString(d) {
		httpapi.BadRequest(c, "自定义域名无效")
		return
	}
	b, _ := json.Marshal(in)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("INSERT INTO whitelabel_config (id,name,mini_program_name,theme_color,config_json) VALUES (1,?,?,?,?) ON DUPLICATE KEY UPDATE mini_program_name=VALUES(mini_program_name),theme_color=VALUES(theme_color),config_json=VALUES(config_json)", "default", in["miniprogram_name"], in["theme_color"], string(b)).Error; err != nil {
			return err
		}
		return resourceAudit(tx, c.MustGet("admin_profile").(Profile), "update", "whitelabel", 1, nil, in, c.ClientIP(), "")
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"config": in})
}

func hasPermission(p Profile, code string) bool {
	for _, v := range p.Permissions {
		if v == code {
			return true
		}
	}
	return false
}
