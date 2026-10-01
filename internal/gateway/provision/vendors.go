package provision

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Vendor is the public dictionary entry. Private adapter configuration stays in gateway_db.
type Vendor struct {
	ID           uint64     `json:"id"`
	VendorCode   string     `json:"vendor_code"`
	VendorName   string     `json:"vendor_name"`
	AdapterClass string     `json:"adapter_class"`
	Protocol     string     `json:"protocol"`
	Status       string     `json:"status"`
	EnabledAt    *time.Time `json:"enabled_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (Vendor) TableName() string { return "vendor" }

const vendorColumns = "id,vendor_code,vendor_name,adapter_class,protocol,status,enabled_at,created_at,updated_at"

type vendorInput struct {
	VendorCode   string `json:"vendor_code"`
	VendorName   string `json:"vendor_name"`
	AdapterClass string `json:"adapter_class"`
	Protocol     string `json:"protocol"`
	Status       string `json:"status"`
}

var vendorCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var (
	errVendorCodeImmutable    = errors.New("厂商编码创建后不可修改")
	errVendorAdapterImmutable = errors.New("厂商已有设备，适配器和连接协议不可修改")
)

func (a API) registerVendors(r *gin.Engine) {
	r.GET("/api/v1/internal/vendors", a.listVendors)
	r.GET("/api/v1/internal/vendors/:id", a.getVendor)
	r.POST("/api/v1/internal/vendors", a.createVendor)
	r.PUT("/api/v1/internal/vendors/:id", a.updateVendor)
}

func (a API) vendorAuthorized(c *gin.Context) bool {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return false
	}
	return true
}

func (a API) vendorDBAvailable(c *gin.Context) bool {
	if a.DB == nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "厂商管理暂时不可用", nil)
		return false
	}
	return true
}

func validateVendorInput(in *vendorInput) error {
	if !vendorCodePattern.MatchString(in.VendorCode) {
		return errors.New("厂商编码须为 1–64 个字母、数字、下划线或连字符")
	}
	in.VendorName = strings.TrimSpace(in.VendorName)
	if !utf8.ValidString(in.VendorName) || utf8.RuneCountInString(in.VendorName) < 1 || utf8.RuneCountInString(in.VendorName) > 128 {
		return errors.New("厂商名称须为 1–128 个字符")
	}
	for _, ch := range in.VendorName {
		if unicode.IsControl(ch) {
			return errors.New("厂商名称不能包含控制字符")
		}
	}
	if in.AdapterClass == "" || utf8.RuneCountInString(in.AdapterClass) > 256 {
		return errors.New("请选择厂商适配器")
	}
	if in.Protocol != "tcp" && in.Protocol != "mqtt" && in.Protocol != "hybrid" {
		return errors.New("连接协议无效")
	}
	if in.Status != "enabled" && in.Status != "disabled" {
		return errors.New("厂商状态须为 enabled 或 disabled")
	}
	return nil
}

// The running gateway currently registers only dc589.TCPAdapter in cmd/gateway.
func supportedVendorAdapter(in vendorInput) bool {
	return in.AdapterClass == "dc589" && in.Protocol == "tcp"
}

func bindVendorInput(c *gin.Context) (vendorInput, bool) {
	var in vendorInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		httpapi.BadRequest(c, "厂商参数无效")
		return in, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		httpapi.BadRequest(c, "厂商参数无效")
		return in, false
	}
	if err := validateVendorInput(&in); err != nil {
		httpapi.BadRequest(c, err.Error())
		return in, false
	}
	return in, true
}

func vendorID(raw string) (uint64, error) {
	if raw == "" {
		return 0, errors.New("厂商 ID 须为正整数")
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, errors.New("厂商 ID 须为正整数")
		}
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("厂商 ID 须为正整数")
	}
	return id, nil
}

type vendorListQuery struct {
	Page, PageSize  int
	Keyword, Status string
	IDs             []uint64
}

func parseVendorList(c *gin.Context) (vendorListQuery, error) {
	q := vendorListQuery{Page: 1, PageSize: 20, Keyword: strings.TrimSpace(c.Query("keyword")), Status: c.Query("status")}
	for name, target := range map[string]*int{"page": &q.Page, "page_size": &q.PageSize} {
		if c.Request.URL.Query().Has(name) {
			raw := c.Query(name)
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || (name == "page" && n > 1000000) || (name == "page_size" && n > 100) {
				return q, errors.New("分页参数无效：page 为 1–1000000，page_size 为 1–100")
			}
			*target = n
		}
	}
	if utf8.RuneCountInString(q.Keyword) > 128 || (q.Status != "" && q.Status != "enabled" && q.Status != "disabled") {
		return q, errors.New("关键词过长或状态无效")
	}
	if c.Request.URL.Query().Has("ids") {
		raw := c.Query("ids")
		parts := strings.Split(raw, ",")
		if len(parts) > 500 {
			return q, errors.New("厂商 ID 筛选最多 500 项")
		}
		seen := map[uint64]bool{}
		for _, part := range parts {
			id, err := vendorID(part)
			if err != nil {
				return q, err
			}
			if !seen[id] {
				q.IDs = append(q.IDs, id)
				seen[id] = true
			}
		}
	}
	return q, nil
}

func (a API) listVendors(c *gin.Context) {
	if !a.vendorAuthorized(c) {
		return
	}
	q, err := parseVendorList(c)
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	if !a.vendorDBAvailable(c) {
		return
	}
	query := a.DB.WithContext(c.Request.Context()).Table("vendor").Where("deleted_at IS NULL")
	if q.Keyword != "" {
		pattern := "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(q.Keyword) + "%"
		query = query.Where("(vendor_code LIKE ? ESCAPE '!' OR vendor_name LIKE ? ESCAPE '!')", pattern, pattern)
	}
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if len(q.IDs) > 0 {
		query = query.Where("id IN ?", q.IDs)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		vendorFailure(c, err)
		return
	}
	items := []Vendor{}
	if err := query.Select(vendorColumns).Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&items).Error; err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"items": items, "total": total, "page": q.Page, "page_size": q.PageSize})
}

func (a API) getVendor(c *gin.Context) {
	if !a.vendorAuthorized(c) {
		return
	}
	id, err := vendorID(c.Param("id"))
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	if !a.vendorDBAvailable(c) {
		return
	}
	var row Vendor
	if err := a.DB.WithContext(c.Request.Context()).Select(vendorColumns).Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

func (a API) createVendor(c *gin.Context) {
	if !a.vendorAuthorized(c) {
		return
	}
	in, ok := bindVendorInput(c)
	if !ok {
		return
	}
	if !supportedVendorAdapter(in) {
		httpapi.BadRequest(c, "当前网关仅支持 dc589 适配器与 TCP 连接")
		return
	}
	if !a.vendorDBAvailable(c) {
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := Vendor{VendorCode: in.VendorCode, VendorName: in.VendorName, AdapterClass: in.AdapterClass, Protocol: in.Protocol, Status: in.Status, CreatedAt: now, UpdatedAt: now}
	if in.Status == "enabled" {
		row.EnabledAt = &now
	}
	// 数据库唯一索引限制有效厂商编码，覆盖并发写入。
	if err := a.DB.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

func (a API) updateVendor(c *gin.Context) {
	if !a.vendorAuthorized(c) {
		return
	}
	id, err := vendorID(c.Param("id"))
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	in, ok := bindVendorInput(c)
	if !ok || !a.vendorDBAvailable(c) {
		return
	}
	var row Vendor
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Select(vendorColumns).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
			return err
		}
		if in.VendorCode != row.VendorCode {
			return errVendorCodeImmutable
		}
		if in.AdapterClass != row.AdapterClass || in.Protocol != row.Protocol {
			var devices int64
			if err := tx.Table("device").Where("vendor_id = ?", row.ID).Count(&devices).Error; err != nil {
				return err
			}
			if devices > 0 {
				return errVendorAdapterImmutable
			}
			if !supportedVendorAdapter(in) {
				return errVendorUnsupportedAdapter
			}
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		updates := map[string]any{"vendor_name": in.VendorName, "adapter_class": in.AdapterClass, "protocol": in.Protocol, "status": in.Status, "updated_at": now}
		if in.Status == "enabled" && row.Status != "enabled" {
			updates["enabled_at"] = now
		}
		if err := tx.Model(&Vendor{}).Where("id = ? AND deleted_at IS NULL", row.ID).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Select(vendorColumns).Where("id = ? AND deleted_at IS NULL", row.ID).Take(&row).Error
	})
	if err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, row)
}

var errVendorUnsupportedAdapter = errors.New("当前网关仅支持 dc589 适配器与 TCP 连接")

func vendorFailure(c *gin.Context, err error) {
	var mysqlErr *mysql.MySQLError
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		httpapi.Write(c, http.StatusNotFound, 1004, "厂商不存在", nil)
	case errors.Is(err, errVendorCodeImmutable), errors.Is(err, errVendorAdapterImmutable):
		httpapi.Write(c, http.StatusConflict, 2009, err.Error(), nil)
	case errors.Is(err, errVendorUnsupportedAdapter):
		httpapi.BadRequest(c, err.Error())
	case errors.As(err, &mysqlErr) && mysqlErr.Number == 1062:
		httpapi.Write(c, http.StatusConflict, 2009, "厂商编码已存在", nil)
	default:
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "厂商管理暂时不可用", nil)
	}
}
