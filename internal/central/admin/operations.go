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
	"github.com/ChargePilot2026/charge-pilot/internal/platform/netguard"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// errConflict 表示这条记录当前的状态不接受这次写入（例如期望版本号已被别人改过），
// 由 resourceFailure 统一转成 409，提示前端刷新后重试。
var errConflict = errors.New("当前记录状态不允许此操作，请刷新后重试")

// registerOperations 挂载运营类接口：后台账号、告警事件、公告、
// Webhook 订阅、白标配置。列表接口大多由 get 闭包统一生成，只有写操作
// 和语义特殊的白标配置需要单独的处理器。
func (a ResourceAPI) registerOperations(r *gin.Engine) {
	// get 生成一个"按 id 倒序列出整表"的列表接口：soft 为真时过滤软删除行，
	// columns 决定返回哪些列——敏感列在 SQL 表达式里就裁掉（如 webhook 只取
	// secret 前 8 位），不会先查出来再丢弃。
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
	get("alerts", "alert.read", "alert_event", "id,device_id,severity,metric,status,created_at", false)
	get("announcements", "announcement.read", "announcement", "id,title,content,scope,target_ids,status,start_at,end_at", true)
	get("webhooks", "webhook.read", "webhook_subscription", "id,name,url,enabled,event_types,LEFT(secret,8) AS secret_prefix", true)
	r.POST("/api/v1/admin/admin-users", a.Auth.Require("admin_user.create"), a.createUser)
	r.POST("/api/v1/admin/alerts/:id/ack", a.Auth.Require("alert.ack"), a.ackAlert)
	r.POST("/api/v1/admin/announcements", a.Auth.Require("announcement.create"), a.createAnnouncement)
	r.POST("/api/v1/admin/webhooks", a.Auth.Require("webhook.create"), a.createWebhook)
	r.GET("/api/v1/admin/whitelabel", a.Auth.Require("whitelabel.read"), a.whitelabel)
	r.PUT("/api/v1/admin/whitelabel", a.Auth.Require("whitelabel.update"), a.saveWhitelabel)
}

// normalizeRows 将数据库字节值转换为 JSON 可用的响应值。
// JSON 字段解析为数组或对象，解析失败保持原值；白名单内的 tinyint(1) 字段转为布尔值。
// 新增数据库布尔字段时需同步更新白名单，Go 计算的布尔响应字段无需登记。
var booleanColumns = map[string]bool{
	"enabled":                 true,
	"reports_energy":          true,
	"reports_segmented_power": true,
	"user_visible":            true,
}

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
			if booleanColumns[k] {
				switch n := v.(type) {
				case int64:
					row[k] = n != 0
				case uint64:
					row[k] = n != 0
				case int8:
					row[k] = n != 0
				case bool:
					// 已经是布尔值，原样留着。
				}
			}
		}
	}
}

// validText 校验自由文本字段：去掉首尾空白后不能为空，且按 rune 计数不超过 max
// （按 rune 而不是字节，避免中文名按字节超限）。
func validText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}

// httpsURL 只接受可对外访问的 HTTPS 链接：必须能解析、scheme 为 https、有主机名、
// 不允许夹带 user：pass 凭据，整串长度不超过 512。Webhook 地址、
// 白标 logo 与协议链接都走这条校验。
func httpsURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && len(s) <= 512
}

// adminWrite 在同一事务中完成业务写入和审计。
// id=0 时新增并回填主键；否则锁定原记录，读取更新前快照后执行更新。
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

// createUser 校验用户名长度 3–64、密码长度 12–72 字节及角色权限。
// 角色须存在且权限非空，授予的权限不得超出调用者权限。
// 密码以 bcrypt 散列保存；审计仅记录用户名和角色。
func (a ResourceAPI) createUser(c *gin.Context) {
	// in 是新建后台账号的请求体，全部字段必填。
	var in struct {
		Username    string `json:"username"`     // 登录名，3–64 位英文、数字、下划线、点或连字符
		DisplayName string `json:"display_name"` // 姓名或昵称，最多 64 字符
		Password    string `json:"password"`     // 初始密码，12–72 字节，入库前做 bcrypt
		RoleID      uint64 `json:"role_id"`      // 角色 id，0 视为无效
		Phone       string `json:"phone"`        // 联系电话，可空，最长 32 字符
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
	codes = withoutRetiredPermissions(codes)
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
		if err := tx.Table("admin_user").Create(row).Error; err != nil {
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

// ackAlert 认领一条告警：只允许 active（未处理）→ acknowledged（已认领），
// 对已认领的告警重复提交按幂等返回成功，其它状态报冲突。认领人取当前登录者。
func (a ResourceAPI) ackAlert(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	err := a.Store.AdminDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// row 只取状态列：active 表示未认领，acknowledged 表示已被认领。
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

// createAnnouncement 发布公告。scope 取 global（全局）、station（站点）、city（城市）；
// 非全局必须给 target_ids（目标站点或城市 id，最多 100 个，每项不超过 64 字符），
// end_at 为空表示长期有效。落库即 status=published，不做草稿中间态。
func (a ResourceAPI) createAnnouncement(c *gin.Context) {
	// in 是发布公告的请求体。
	var in struct {
		Title     string     `json:"title"`      // 公告标题，必填，最多 128 字符
		Content   string     `json:"content"`    // 公告正文，必填，最多 20000 字符
		Scope     string     `json:"scope"`      // 投放范围：global / station / city
		TargetIDs []string   `json:"target_ids"` // 目标 id 列表，scope 非 global 时必填
		StartAt   time.Time  `json:"start_at"`   // 生效时间，必填
		EndAt     *time.Time `json:"end_at"`     // 失效时间，可空表示不限；非空时必须晚于 start_at
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

// createWebhook 校验公网 HTTPS 地址及 1–3 个不重复的订阅事件，生成 32 字节随机 secret。
// secret 保存到订阅表用于投递签名，仅在创建响应中返回；后续查询不返回。
func (a ResourceAPI) createWebhook(c *gin.Context) {
	// in 是新建订阅的请求体。
	var in struct {
		Name       string   `json:"name"`        // 订阅名称，必填，最多 64 字符
		URL        string   `json:"url"`         // 接收地址，必须是可公网访问的 HTTPS 链接
		EventTypes []string `json:"event_types"` // 订阅的事件类型，1–3 个，不重复
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Name, 64) || !httpsURL(in.URL) || len(in.EventTypes) == 0 || len(in.EventTypes) > 3 {
		httpapi.BadRequest(c, "请填写名称、HTTPS URL 和订阅事件")
		return
	}
	// 保存前拒绝内网目标；投递时再次校验解析地址，防止 DNS rebinding。
	if err := netguard.ValidatePublicHTTPS(in.URL); err != nil {
		httpapi.Write(c, 400, 1002, err.Error(), nil)
		return
	}
	seen := map[string]bool{}
	for _, v := range in.EventTypes {
		if v == "" || !oneOf(v, "alert charge_ended refund_completed") || seen[v] {
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

// whiteKeys 声明白标可写字段及长度上限；新增字段必须显式登记。
var whiteKeys = map[string]int{"miniprogram_name": 64, "miniprogram_logo_url": 512, "admin_logo_url": 512, "theme_color": 9, "service_phone": 32, "service_wechat_id": 64, "icp_record_no": 128, "custom_domain": 253, "agreement_url": 512, "privacy_url": 512, "about_us": 20000}

// whiteRead 读出当前生效的白标配置。whitelabel_config 只有 id=1 这一行；
// 没有记录时返回内置默认值，存量数据里不在白名单内的键被丢弃且不覆盖默认值。
func (a ResourceAPI) whiteRead(c *gin.Context) (map[string]string, error) {
	// row 只取 config_json 一列；指针为 nil 表示这行还没有写过配置。
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

// whitelabel 返回合并默认值后的白标配置。
func (a ResourceAPI) whitelabel(c *gin.Context) {
	v, e := a.whiteRead(c)
	if e != nil {
		resourceFailure(c, e)
		return
	}
	httpapi.OK(c, v)
}

// saveWhitelabel 整体覆盖 id=1 的白标 JSON，仅接受允许的键。
// 非空 URL 必须使用 HTTPS，名称和 #RRGGBB 或 #RRGGBBAA 主题色必填，非空域名须合法。
// 调用方必须提交完整配置；省略的键按默认值处理。
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

// hasPermission 判断当前操作者是否已持有某项权限编码，服务于"不能授予自己没有的权限"这类检查。
func hasPermission(p Profile, code string) bool {
	for _, v := range p.Permissions {
		if v == code {
			return true
		}
	}
	return false
}
