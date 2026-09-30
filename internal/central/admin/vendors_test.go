package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestVendorProxyForwardsConfigurationAndTrace(t *testing.T) {
	want := vendorRow{ID: 7, VendorCode: "vendor_a", VendorName: "厂商 A", AdapterClass: "dc589", Protocol: "tcp", Status: "enabled"}
	input := vendorInput{VendorCode: "vendor_a", VendorName: "厂商 A", AdapterClass: "dc589", Protocol: "tcp", Status: "enabled"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/api/v1/internal/vendors/7" || r.Header.Get("X-Service-Token") != "test-vendor-token" || r.Header.Get("X-Request-ID") != "vendor-test" {
			t.Errorf("错误的代理请求：%s %s", r.Method, r.URL)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.URL.Query().Get("keyword") != "厂商 A&+" {
			t.Error("请求格式或查询参数未保留")
		}
		var got vendorInput
		if json.NewDecoder(r.Body).Decode(&got) != nil || got != input {
			t.Errorf("配置未完整传递：%+v", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": want})
	}))
	defer server.Close()
	api := ResourceAPI{GatewayURL: server.URL + "/", ServiceToken: "test-vendor-token"}
	var got vendorRow
	if err := api.requestVendor(context.Background(), "PUT", "/7", url.Values{"keyword": {"厂商 A&+"}}, input, &got, "vendor-test"); err != nil || got.ID != want.ID || got.VendorName != want.VendorName {
		t.Fatalf("代理结果错误：%+v %v", got, err)
	}
}

func TestVendorProxyFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, raw    string
		status, want int
	}{
		{"invalid", `{"code":1005,"message":"编码无效"}`, 400, 400},
		{"missing", `{"code":1004,"message":"厂商不存在"}`, 404, 404},
		{"duplicate", `{"code":2009,"message":"厂商编码已存在"}`, 409, 409},
		{"service_auth", `{"code":1001,"message":"private-token-error"}`, 401, 503},
		{"database", `{"code":5003,"message":"private-database-error"}`, 503, 503},
		{"malformed", `broken`, 200, 503},
		{"null", `{"code":0,"data":null}`, 200, 503},
		{"missing_code", `{"data":{}}`, 200, 503},
		{"empty_row", `{"code":0,"data":{}}`, 200, 503},
		{"oversize", strings.Repeat(" ", (1<<20)+1), 200, 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.raw))
			}))
			defer server.Close()
			api := ResourceAPI{GatewayURL: server.URL, ServiceToken: "test"}
			var out vendorRow
			err := api.requestVendor(context.Background(), "GET", "/1", nil, nil, &out, "")
			var failure *vendorServiceError
			if !errors.As(err, &failure) || failure.status != test.want {
				t.Fatalf("got %v, want HTTP %d", err, test.want)
			}
			if test.want == 503 && failure.message != errVendorService.message {
				t.Fatalf("服务错误泄漏：%s", failure.message)
			}
		})
	}
}

func TestVendorProxyDoesNotFollowRedirects(t *testing.T) {
	forwarded := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	api := ResourceAPI{GatewayURL: server.URL, ServiceToken: "test"}
	var out vendorRow
	if err := api.requestVendor(context.Background(), "GET", "", nil, nil, &out, ""); !errors.Is(err, errVendorService) || forwarded {
		t.Fatalf("服务令牌不应被转发到重定向目标：forwarded=%v err=%v", forwarded, err)
	}
}

func TestVendorProxyHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	api := ResourceAPI{GatewayURL: server.URL, ServiceToken: "test"}
	var out vendorRow
	if err := api.requestVendor(ctx, "GET", "", nil, nil, &out, ""); !errors.Is(err, errVendorService) {
		t.Fatalf("取消请求应安全失败：%v", err)
	}
}

func TestVendorQueryUsesAccountScope(t *testing.T) {
	for _, scope := range []DataScope{{Unrestricted: true, VendorIDs: []uint64{3}}, {StationIDs: []uint64{5}}, {VendorIDs: []uint64{3, 7}}} {
		q := vendorQuery(PageQuery{Page: 2, PageSize: 20, Keyword: "厂商", Status: "enabled"}, scope)
		want := ""
		if !scope.Unrestricted && len(scope.VendorIDs) > 0 {
			want = "3,7"
		}
		if q.Get("ids") != want || q.Get("status") != "enabled" || q.Get("page") != "2" {
			t.Fatalf("范围查询错误：%s", q.Encode())
		}
		if scope.AllowsVendor(9) != (want == "") || !scope.AllowsVendor(3) {
			t.Fatalf("详情范围与列表不一致：%+v", scope)
		}
	}
}

func TestVendorScopeValidationUsesGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ids") != "3,7" || r.URL.Query().Get("page_size") != "1" {
			t.Error("范围未通过网关验证")
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[],"total":2,"page":1,"page_size":1}}`))
	}))
	defer server.Close()
	api := ResourceAPI{GatewayURL: server.URL, ServiceToken: "test"}
	if err := api.validateScope(context.Background(), "vendor", []uint64{3, 7}); err != nil {
		t.Fatal(err)
	}
	if err := api.validateScope(context.Background(), "vendor", []uint64{0}); !errors.Is(err, errScopeConflict) {
		t.Fatalf("零 ID 应拒绝：%v", err)
	}
}
