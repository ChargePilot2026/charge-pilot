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

// ResourceStore reads each central module through its own schema connection.
// No cross-schema SQL or gateway database access is used here.
type ResourceStore struct{ AdminDB, UserDB, BillingDB *gorm.DB }
type ResourceAPI struct {
	Store                    ResourceStore
	Auth                     API
	GatewayURL, ServiceToken string
}

func (a ResourceAPI) Register(r *gin.Engine) {
	a.registerOperations(r)
	a.registerPricing(r)
	a.registerCoupons(r)
	a.registerCasework(r)
	a.registerInvoices(r)
	a.registerMeterReviews(r)
	a.registerRefunds(r)
	a.registerWalletRisks(r)
	a.registerImports(r)
	r.GET("/api/v1/admin/stations", a.Auth.Require("station.read"), a.stations)
	r.GET("/api/v1/admin/stations/:id", a.Auth.Require("station.read"), a.station)
	r.POST("/api/v1/admin/stations", a.Auth.Require("station.create"), a.createStation)
	r.PUT("/api/v1/admin/stations/:id", a.Auth.Require("station.update"), a.updateStation)
	r.GET("/api/v1/admin/devices", a.Auth.Require("device.read"), a.devices)
	r.GET("/api/v1/admin/devices/:id", a.Auth.Require("device.read"), a.device)
	r.GET("/api/v1/admin/orders", a.Auth.Require("order.read"), a.orders)
	r.GET("/api/v1/admin/orders/:id", a.Auth.Require("order.read"), a.order)
	r.GET("/api/v1/admin/orders/:id/timeline", a.Auth.Require("order.read"), a.timeline)
}

type PageQuery struct {
	Page            int
	PageSize        int
	Keyword, Status string
}
type Page[T any] struct {
	Items       []T      `json:"items"`
	Total       int64    `json:"total"`
	Page        int      `json:"page"`
	PageSize    int      `json:"page_size"`
	Permissions []string `json:"permissions,omitempty"`
}

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
func likePattern(value string) string {
	return "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(value) + "%"
}
func pathID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpapi.BadRequest(c, "ID 必须为正整数")
		return 0, false
	}
	return id, true
}
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

// Decode bounded write bodies and reject misspelled fields rather than silently
// overwriting records with default values. Domain validation follows decoding.
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
