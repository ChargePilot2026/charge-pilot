package provision

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

const vendorTestToken = "vendor-test-secret"

func vendorTestRouter(a API) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := httpapi.NewRouter()
	a.Register(router)
	return router
}

func callVendor(router *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Service-Token", token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func validVendorInput() vendorInput {
	return vendorInput{VendorCode: "TEST_DC589", VendorName: "测试厂商", AdapterClass: "dc589", Protocol: "tcp", Status: "enabled"}
}

func TestVendorsRequireExactNonemptyServiceToken(t *testing.T) {
	for _, expected := range []string{"", vendorTestToken} {
		router := vendorTestRouter(API{ServiceToken: expected})
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/internal/vendors"},
			{http.MethodGet, "/api/v1/internal/vendors/1"},
			{http.MethodPost, "/api/v1/internal/vendors"},
			{http.MethodPut, "/api/v1/internal/vendors/1"},
		} {
			for _, supplied := range []string{"", "wrong", " " + vendorTestToken, vendorTestToken + " "} {
				rec := callVendor(router, route.method, route.path, supplied, "{}")
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("%s %s with expected %q, supplied %q: %d %s", route.method, route.path, expected, supplied, rec.Code, rec.Body.String())
				}
				var envelope httpapi.Envelope
				if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Code != 1001 || envelope.RequestID == "" {
					t.Fatalf("invalid authentication envelope: %s", rec.Body.String())
				}
			}
		}
	}
	if rec := callVendor(vendorTestRouter(API{ServiceToken: vendorTestToken}), http.MethodGet, "/api/v1/internal/vendors", vendorTestToken, ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("authorized request without database: %d %s", rec.Code, rec.Body.String())
	}
}

func TestVendorInputBoundsAndSupportedTransport(t *testing.T) {
	valid := validVendorInput()
	valid.VendorName = "  测试厂商  "
	if err := validateVendorInput(&valid); err != nil || valid.VendorName != "测试厂商" || !supportedVendorAdapter(valid) {
		t.Fatalf("valid input rejected or not normalized: %+v, %v", valid, err)
	}
	for _, code := range []string{"", "contains space", "dc589::Adapter", "厂家", strings.Repeat("a", 65)} {
		in := validVendorInput()
		in.VendorCode = code
		if validateVendorInput(&in) == nil {
			t.Errorf("accepted invalid code %q", code)
		}
	}
	for _, name := range []string{"", " \t ", strings.Repeat("厂", 129), "厂商\n名称", "厂商\x00名称"} {
		in := validVendorInput()
		in.VendorName = name
		if validateVendorInput(&in) == nil {
			t.Errorf("accepted invalid name %q", name)
		}
	}
	for _, pair := range [][2]string{{"dc589", "mqtt"}, {"dc589", "hybrid"}, {"unknown", "tcp"}} {
		in := validVendorInput()
		in.AdapterClass, in.Protocol = pair[0], pair[1]
		if supportedVendorAdapter(in) {
			t.Errorf("unsupported adapter accepted: %+v", in)
		}
	}
	boundary := validVendorInput()
	boundary.VendorCode, boundary.VendorName = strings.Repeat("a", 64), strings.Repeat("厂", 128)
	if err := validateVendorInput(&boundary); err != nil {
		t.Fatalf("boundary input rejected: %v", err)
	}
}

func TestVendorListQueryBoundsAndFailClosedScope(t *testing.T) {
	parse := func(query string) (vendorListQuery, error) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/internal/vendors?"+query, nil)
		return parseVendorList(c)
	}
	q, err := parse("ids=1,2,2&page=2&page_size=100&status=disabled&keyword=" + url.QueryEscape(" 厂商 "))
	if err != nil || q.Page != 2 || q.PageSize != 100 || q.Keyword != "厂商" || len(q.IDs) != 2 || q.IDs[1] != 2 {
		t.Fatalf("valid query: %+v %v", q, err)
	}
	q, err = parse("")
	if err != nil || q.Page != 1 || q.PageSize != 20 || q.IDs != nil {
		t.Fatalf("defaults: %+v %v", q, err)
	}
	for _, invalid := range []string{"page=", "page=0", "page=1.5", "page=1000001", "page_size=0", "page_size=101", "status=retired", "ids=", "ids=1,", "ids=0", "ids=-1", "ids=1.5", "ids=18446744073709551616", "ids=" + strings.Repeat("1,", 500) + "2", "keyword=" + url.QueryEscape(strings.Repeat("厂", 129))} {
		if _, err := parse(invalid); err == nil {
			t.Errorf("accepted invalid query %q", invalid)
		}
	}
	for _, raw := range []string{"", "0", "-1", "+1", "1.2", "1e2", "0x10", "18446744073709551616"} {
		if _, err := vendorID(raw); err == nil {
			t.Errorf("accepted invalid id %q", raw)
		}
	}
}

func TestVendorHandlersRejectMalformedAndUnsupportedInputsBeforeDatabase(t *testing.T) {
	router := vendorTestRouter(API{ServiceToken: vendorTestToken})
	valid := `{"vendor_code":"V1","vendor_name":"测试厂商","adapter_class":"dc589","protocol":"tcp","status":"enabled"}`
	for _, body := range []string{"{}", "null", "[]", valid + "{}", strings.Replace(valid, `"tcp"`, `"mqtt"`, 1), strings.Replace(valid, `"enabled"`, `"retired"`, 1), strings.TrimSuffix(valid, "}") + `,"config_json":{"secret":"private"}}`, strings.TrimSuffix(valid, "}") + `,"unknown":"` + strings.Repeat("x", 17<<10) + `"}`} {
		if rec := callVendor(router, http.MethodPost, "/api/v1/internal/vendors", vendorTestToken, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid body got %d: %s", rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{"vendors?page=0", "vendors?ids=", "vendors/0", "vendors/not-a-number"} {
		if rec := callVendor(router, http.MethodGet, "/api/v1/internal/"+path, vendorTestToken, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid query or id got %d: %s", rec.Code, rec.Body.String())
		}
	}
}

func TestVendorFailureEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   int
	}{
		{gorm.ErrRecordNotFound, 404, 1004},
		{errVendorCodeImmutable, 409, 2009},
		{errVendorAdapterImmutable, 409, 2009},
		{&mysql.MySQLError{Number: 1062, Message: "private database content"}, 409, 2009},
		{errVendorUnsupportedAdapter, 400, 1005},
		{errors.New("private database credentials"), 503, 5003},
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		vendorFailure(c, tc.err)
		var envelope httpapi.Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || rec.Code != tc.status || envelope.Code != tc.code || strings.Contains(envelope.Message, "private database") {
			t.Fatalf("failure mapping %v: %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}
}
