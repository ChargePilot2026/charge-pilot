package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// 厂商归网关所有；后台通过服务接口管理，不能另建一份厂商表或跨库写入。
type vendorRow struct {
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

type vendorInput struct {
	VendorCode   string `json:"vendor_code"`
	VendorName   string `json:"vendor_name"`
	AdapterClass string `json:"adapter_class"`
	Protocol     string `json:"protocol"`
	Status       string `json:"status"`
}

func (a ResourceAPI) registerVendors(r *gin.Engine) {
	r.GET("/api/v1/admin/vendors", a.Auth.Require("vendor.read"), a.listVendors)
	r.GET("/api/v1/admin/vendors/:id", a.Auth.Require("vendor.read"), a.getVendor)
	r.POST("/api/v1/admin/vendors", a.Auth.Require("vendor.create"), a.createVendor)
	r.PUT("/api/v1/admin/vendors/:id", a.Auth.Require("vendor.update"), a.updateVendor)
	// 设备开通只需要可选厂商，不要求同时持有厂商管理权限。
	r.GET("/api/v1/admin/vendor-options", a.Auth.Require("device.import"), a.vendorOptions)
}

func (s DataScope) AllowsVendor(id uint64) bool {
	if s.Unrestricted || len(s.VendorIDs) == 0 {
		return true
	}
	for _, allowed := range s.VendorIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

func vendorQuery(page PageQuery, scope DataScope) url.Values {
	query := url.Values{
		"page": {strconv.Itoa(page.Page)}, "page_size": {strconv.Itoa(page.PageSize)},
		"keyword": {page.Keyword}, "status": {page.Status},
	}
	// 范围来自登录账号，绝不转发客户端自己给的 ids。
	if !scope.Unrestricted && len(scope.VendorIDs) > 0 {
		query.Set("ids", vendorIDs(scope.VendorIDs))
	}
	return query
}

func vendorIDs(ids []uint64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatUint(id, 10)
	}
	return strings.Join(parts, ",")
}

func (a ResourceAPI) vendorScope(c *gin.Context) (DataScope, bool) {
	scope, err := LoadDataScope(c.Request.Context(), a.Store.AdminDB, c.MustGet("admin_profile").(Profile))
	if err != nil {
		resourceFailure(c, err)
		return scope, false
	}
	return scope, true
}

func (a ResourceAPI) listVendors(c *gin.Context)   { a.listVendorPage(c, false) }
func (a ResourceAPI) vendorOptions(c *gin.Context) { a.listVendorPage(c, true) }

func (a ResourceAPI) listVendorPage(c *gin.Context, optionsOnly bool) {
	page, ok := parsePage(c, "enabled disabled")
	if !ok {
		return
	}
	if optionsOnly {
		page.Status = "enabled"
	}
	scope, ok := a.vendorScope(c)
	if !ok {
		return
	}
	var out Page[vendorRow]
	if err := a.requestVendor(c.Request.Context(), http.MethodGet, "", vendorQuery(page, scope), nil, &out, httpapi.RequestID(c)); err != nil {
		vendorFailure(c, err)
		return
	}
	if out.Items == nil {
		out.Items = []vendorRow{}
	}
	for _, vendor := range out.Items {
		if !scope.AllowsVendor(vendor.ID) || (optionsOnly && vendor.Status != "enabled") {
			vendorFailure(c, errVendorService)
			return
		}
	}
	// 只有后台鉴权档案可以授予权限，不能信任上游同名响应字段。
	out.Permissions = nil
	permissions := c.MustGet("admin_profile").(Profile).Permissions
	for _, permission := range permissions {
		// 有厂商范围的账号不能创建一个范围之外的新厂商。
		if permission == "vendor.create" && !scope.Unrestricted && len(scope.VendorIDs) > 0 {
			continue
		}
		out.Permissions = append(out.Permissions, permission)
	}
	httpapi.OK(c, out)
}

func (a ResourceAPI) visibleVendorID(c *gin.Context) (uint64, bool) {
	id, ok := pathID(c)
	if !ok {
		return 0, false
	}
	scope, ok := a.vendorScope(c)
	if !ok {
		return 0, false
	}
	if !scope.AllowsVendor(id) {
		httpapi.Write(c, http.StatusNotFound, 1004, "厂商不存在或不在数据范围内", nil)
		return 0, false
	}
	return id, true
}

func (a ResourceAPI) getVendor(c *gin.Context) {
	id, ok := a.visibleVendorID(c)
	if !ok {
		return
	}
	var out vendorRow
	if err := a.requestVendor(c.Request.Context(), http.MethodGet, "/"+strconv.FormatUint(id, 10), nil, nil, &out, httpapi.RequestID(c)); err != nil {
		vendorFailure(c, err)
		return
	}
	httpapi.OK(c, out)
}

func (a ResourceAPI) createVendor(c *gin.Context) {
	scope, ok := a.vendorScope(c)
	if !ok {
		return
	}
	if !scope.Unrestricted && len(scope.VendorIDs) > 0 {
		httpapi.Write(c, http.StatusForbidden, 1003, "创建厂商需要不受厂商数据范围限制的账号", nil)
		return
	}
	var in vendorInput
	if !decodeResource(c, &in) {
		return
	}
	var out vendorRow
	if err := a.requestVendor(c.Request.Context(), http.MethodPost, "", nil, in, &out, httpapi.RequestID(c)); err != nil {
		vendorFailure(c, err)
		return
	}
	a.auditToAdmin(c, "create", "vendor", out.ID, nil, out, "")
	httpapi.OK(c, out)
}

func (a ResourceAPI) updateVendor(c *gin.Context) {
	id, ok := a.visibleVendorID(c)
	if !ok {
		return
	}
	var in vendorInput
	if !decodeResource(c, &in) {
		return
	}
	path := "/" + strconv.FormatUint(id, 10)
	var before, out vendorRow
	if err := a.requestVendor(c.Request.Context(), http.MethodGet, path, nil, nil, &before, httpapi.RequestID(c)); err != nil {
		vendorFailure(c, err)
		return
	}
	if err := a.requestVendor(c.Request.Context(), http.MethodPut, path, nil, in, &out, httpapi.RequestID(c)); err != nil {
		vendorFailure(c, err)
		return
	}
	a.auditToAdmin(c, "update", "vendor", id, before, out, "")
	httpapi.OK(c, out)
}

type vendorServiceError struct {
	status, code int
	message      string
}

func (e *vendorServiceError) Error() string { return e.message }

var errVendorService = &vendorServiceError{http.StatusServiceUnavailable, 5003, "厂商服务暂时不可用，请稍后重试"}

func vendorFailure(c *gin.Context, err error) {
	var serviceError *vendorServiceError
	if !errors.As(err, &serviceError) {
		serviceError = errVendorService
	}
	httpapi.Write(c, serviceError.status, serviceError.code, serviceError.message, nil)
}

// 限制超时和响应大小；内部认证失败返回服务不可用，不能让前端误以为登录已失效。
func (a ResourceAPI) requestVendor(ctx context.Context, method, path string, query url.Values, body, out any, requestID string) error {
	if a.GatewayURL == "" || a.ServiceToken == "" {
		return errVendorService
	}
	endpoint := strings.TrimRight(a.GatewayURL, "/") + "/api/v1/internal/vendors" + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return errVendorService
		}
		payload = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return errVendorService
	}
	req.Header.Set("X-Service-Token", a.ServiceToken)
	req.Header.Set("X-Request-ID", requestID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errVendorService
	}
	defer resp.Body.Close()
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return errVendorService
	}
	var envelope struct {
		Code    *int            `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return errVendorService
	}
	if envelope.Code == nil {
		return errVendorService
	}
	if resp.StatusCode != http.StatusOK || *envelope.Code != 0 {
		switch resp.StatusCode {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict:
			if envelope.Message != "" && *envelope.Code != 0 {
				return &vendorServiceError{resp.StatusCode, *envelope.Code, envelope.Message}
			}
		}
		return errVendorService
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, out) != nil || !validVendorResponse(out) {
		return errVendorService
	}
	return nil
}

func validVendorResponse(out any) bool {
	validRow := func(row vendorRow) bool {
		return row.ID > 0 && row.VendorCode != "" && row.VendorName != "" && row.AdapterClass != "" && row.Protocol != "" && (row.Status == "enabled" || row.Status == "disabled")
	}
	switch result := out.(type) {
	case *vendorRow:
		return validRow(*result)
	case *Page[vendorRow]:
		if result.Page < 1 || result.PageSize < 1 || result.PageSize > 100 || result.Total < int64(len(result.Items)) {
			return false
		}
		for _, row := range result.Items {
			if !validRow(row) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
