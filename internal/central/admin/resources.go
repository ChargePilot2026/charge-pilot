package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// ResourceStore 通过各自 schema 的连接读写中台的每个模块。
// 这里既不写跨 schema SQL，也不碰网关的数据库。
//
// ResourceStore 持有各业务模块自己的库连接：AdminDB 站点/设备/定价，UserDB 用户与订单，
// BillingDB 计费与分账。三个句柄分开是为了不出现跨库 SQL。
type ResourceStore struct{ AdminDB, UserDB, BillingDB *gorm.DB }

// ResourceAPI 是除登录鉴权外全部后台业务接口的处理器，聚合了数据句柄、鉴权中间件
// 和调用网关所需的地址与令牌。
type ResourceAPI struct {
	Store                    ResourceStore // 各模块的数据库句柄
	Auth                     API           // 鉴权与权限中间件（API.Register 负责认证路由）
	GatewayURL, ServiceToken string        // 调用网关用的地址与内部服务令牌
	ExportDir                string        // 导出任务的落地目录
	// PhoneKey 是部署级的手机号解密密钥（AES-256，32 字节）。充电用户列表要显示
	// 完整号码，库里只有密文，取明文必须用它。留空时该列留空而不是报错——没有密钥
	// 就退化成"只能看到没绑号的用户"，不阻断整个页面。
	PhoneKey []byte
}

// Register 把所有后台业务路由挂到引擎上：先交给各业务域自己的 register 函数，
// 再补站点、设备、订单这三组直接定义在本文件里的接口。
func (a ResourceAPI) Register(r *gin.Engine) {
	a.registerOperations(r)
	a.registerPricing(r)
	a.registerPricingTemplates(r)
	a.registerPricingCandidates(r)
	a.registerChargeOffers(r)
	a.registerPackageTemplates(r)
	a.registerDevicePricing(r)
	a.registerDeviceMetering(r)
	a.registerSwitchTasks(r)
	a.registerCoupons(r)
	a.registerActivityRules(r)
	a.registerChargeUsers(r)
	a.registerAuditLogs(r)
	a.registerRoles(r)
	a.registerSplitTemplates(r)
	a.registerCasework(r)
	a.registerInvoices(r)
	a.registerMeterReviews(r)
	a.registerRefunds(r)
	a.registerWalletRisks(r)
	a.registerImports(r)
	a.registerVendors(r)
	a.registerFinanceOps(r)
	a.registerWebhookDelivery(r)
	a.registerAlertRules(r)
	a.registerAdminUsers(r)
	ExportTask{Store: a.Store, Auth: a.Auth, ExportDir: a.ExportDir}.register(r)
	OtaAPI{Store: a.Store, Auth: a.Auth, GatewayURL: a.GatewayURL, ServiceToken: a.ServiceToken}.registerOta(r)
	r.GET("/api/v1/admin/stations", a.Auth.Require("station.read"), a.stations)
	r.GET("/api/v1/admin/stations/:id", a.Auth.Require("station.read"), a.station)
	r.POST("/api/v1/admin/stations", a.Auth.Require("station.create"), a.createStation)
	r.PUT("/api/v1/admin/stations/:id", a.Auth.Require("station.update"), a.updateStation)
	r.PUT("/api/v1/admin/stations/:id/split-template", a.Auth.Require("station.update"), a.Auth.Require("finance.split_template.create"), a.bindStationSplitTemplate)
	r.GET("/api/v1/admin/devices", a.Auth.Require("device.read"), a.devices)
	r.GET("/api/v1/admin/devices/:id", a.Auth.Require("device.read"), a.device)
	r.GET("/api/v1/admin/orders", a.Auth.Require("order.read"), a.orders)
	r.GET("/api/v1/admin/orders/:id", a.Auth.Require("order.read"), a.order)
	r.GET("/api/v1/admin/orders/:id/timeline", a.Auth.Require("order.read"), a.timeline)
}

// PageQuery 是全后台统一的分页与筛选入参，各列表接口在此基础上再拼自己的条件。
type PageQuery struct {
	Page            int    // 页码，从 1 开始
	PageSize        int    // 每页条数，1–100
	Keyword, Status string // 关键词模糊查询条件；状态过滤，须落在各接口给定的白名单内
}

// Page[T] 是列表接口的统一响应体，额外带上权限清单供前端决定按钮可见性。
type Page[T any] struct {
	Items       []T      `json:"items"`                 // 当页数据
	Total       int64    `json:"total"`                 // 符合条件的总条数
	Page        int      `json:"page"`                  // 当前页码
	PageSize    int      `json:"page_size"`             // 每页条数
	Permissions []string `json:"permissions,omitempty"` // 操作人权限码；为空时整个字段省略
}

// parsePage 解析并校验通用分页参数：page ≥ 1、page_size 1–100（page 上限 1000000），
// 关键词不超过 128 字符，状态必须在该接口传入的白名单内（留空表示不按状态过滤）。
// 任一项不合法直接写 400 并返回 false，调用方必须立即 return。
func parsePage(c *gin.Context, statuses string) (PageQuery, bool) {
	q := PageQuery{Page: 1, PageSize: 20, Keyword: strings.TrimSpace(c.Query("keyword")), Status: c.Query("status")}
	for name, target := range map[string]*int{"page": &q.Page, "page_size": &q.PageSize} {
		if raw, exists := c.GetQuery(name); exists {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || (name == "page_size" && n > 100) || (name == "page" && n > 1000000) {
				httpapi.BadRequest(c, "分页参数无效：page ≥ 1，page_size 为 1–100")
				return q, false
			}
			*target = n
		}
	}
	if utf8.RuneCountInString(q.Keyword) > 128 || !oneOf(q.Status, statuses) {
		httpapi.BadRequest(c, "关键词过长或状态无效")
		return q, false
	}
	return q, true
}

// oneOf 判断值是否落在以空格分隔的白名单里；空值一律放行，表示不按这一项过滤。
func oneOf(value, allowed string) bool {
	if value == "" {
		return true
	}
	for _, v := range strings.Fields(allowed) {
		if value == v {
			return true
		}
	}
	return false
}

// likePattern 把关键词转成 LIKE 模式，并转义 !、%、_ 三个通配符，
// 否则用户输入的百分号会变成通配、搜出范围之外的数据。
func likePattern(value string) string {
	return "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(value) + "%"
}

// pathID 读取路径参数 :id 并要求它是正整数，不合法直接写 400 并返回 false。
// 本包所有 :id 路由的形参都必须命名为 id，否则这里取不到值。
func pathID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "ID 必须为正整数")
		return 0, false
	}
	return id, true
}

// resourceFailure 把存储层错误翻译成统一响应：业务冲突 409、记录不存在 404、
// 唯一索引冲突 409，其余一律 503 通用提示，不把底层错误暴露给前端。
func resourceFailure(c *gin.Context, err error) {
	var duplicate *mysql.MySQLError
	switch {
	case errors.Is(err, errConflict):
		httpapi.Write(c, 409, 2009, err.Error(), nil)
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.Write(c, 404, 1004, "记录不存在或已删除", nil)
	case errors.As(err, &duplicate) && duplicate.Number == 1062:
		httpapi.Write(c, 409, 2009, "编码已存在，请使用其他编码", nil)
	default:
		httpapi.Write(c, 503, 5003, "数据暂时无法读取或保存，请稍后重试", nil)
	}
}

// 解析写入请求体时限制长度，并拒绝拼错的字段，
// 而不是拿默认值静默覆盖已有记录。业务校验在解析之后。
//
// decodeResource 解析写入请求体：限长 64KB、拒绝未知字段、且只允许一个 JSON 对象，
// 避免拼错的字段被静默丢弃后用默认值覆盖已有数据。业务校验在解析之后由各接口自己做。
func decodeResource(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		httpapi.BadRequest(c, "请求内容无效或包含未知字段")
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		httpapi.BadRequest(c, "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}

// resourceAudit 写一条审计记录：把变更前后的快照序列化成 JSON，一并记下操作人、
// 对象、客户端 IP 和请求号。必须传业务事务的 tx 才能与业务改动同生共死；
// 跨库的业务需改用 audit_log.go 里的 auditToAdmin / flushAudit。
func resourceAudit(tx *gorm.DB, p Profile, action, target string, id uint64, before, after any, ip, requestID string) error {
	old, err := json.Marshal(before)
	if err != nil {
		return err
	}
	next, err := json.Marshal(after)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if len(requestID) > 64 {
		requestID = requestID[:64]
	}
	return tx.Table("audit_log").Create(map[string]any{"actor_id": p.ID, "actor_name": p.Username, "module": target, "action": action, "target_type": target, "target_id": strconv.FormatUint(id, 10), "before_json": string(old), "after_json": string(next), "client_ip": ip, "request_id": requestID, "created_month": time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}).Error
}
