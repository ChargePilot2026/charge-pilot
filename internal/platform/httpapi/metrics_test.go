package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func metricsRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	m := NewMetrics("testsvc")
	router := NewRouter(m)
	m.Register(router)
	router.GET("/api/v1/devices/:device_id/telemetry", func(c *gin.Context) { OK(c, gin.H{"ok": true}) })
	return router
}

func scrape(t *testing.T, router http.Handler) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("scrape returned %d", recorder.Code)
	}
	return recorder.Body.String()
}

// route 标签必须是匹配到的那个模板。本项目的路径里嵌了
// 设备 ID 和订单号，所以按原始路径打标签会每次请求都新造一条
// 时间序列，把抓取直接搞挂。
func TestMetricsLabelRoutesByTemplateNotRawPath(t *testing.T) {
	router := metricsRouter()
	for _, id := range []string{"device-aaa", "device-bbb", "device-ccc"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v1/devices/"+id+"/telemetry", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("request returned %d", recorder.Code)
		}
	}
	body := scrape(t, router)
	if strings.Contains(body, "device-aaa") || strings.Contains(body, "device-bbb") {
		t.Fatalf("metric labels leaked a device id:\n%s", body)
	}
	if !strings.Contains(body, `route="/api/v1/devices/:device_id/telemetry"`) {
		t.Fatalf("expected the route template label, got:\n%s", body)
	}
	// 三台不同的设备不能产生三条序列。
	if got := strings.Count(body, `route="/api/v1/devices/:device_id/telemetry",status="200"`); got != 1 {
		t.Fatalf("expected 1 counter series, found %d", got)
	}
}

// 一个没匹配到任何路由的请求必须收敛到同一个标签，
// 否则未认证的扫描器就能随意撑大序列数量。
func TestMetricsCollapseUnmatchedRequests(t *testing.T) {
	router := metricsRouter()
	for i := 0; i < 3; i++ {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest("GET", "/no/such/path/"+string(rune('a'+i)), nil))
	}
	body := scrape(t, router)
	if !strings.Contains(body, `route="unmatched"`) {
		t.Fatalf("expected unmatched requests to share one label, got:\n%s", body)
	}
	if strings.Contains(body, "/no/such/path") {
		t.Fatalf("unmatched raw path leaked into a label:\n%s", body)
	}
}

func TestMetricsExposeValidExposition(t *testing.T) {
	router := metricsRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v1/devices/dev-1/telemetry", nil))
	body := scrape(t, router)

	for _, want := range []string{
		"# TYPE chargepilot_http_requests_total counter",
		"# TYPE chargepilot_http_request_duration_seconds histogram",
		"chargepilot_service_info{service=\"testsvc\"} 1",
		"chargepilot_uptime_seconds{service=\"testsvc\"}",
		"chargepilot_goroutines{service=\"testsvc\"}",
		"chargepilot_http_request_duration_seconds_bucket",
		"le=\"+Inf\"",
		"chargepilot_http_request_duration_seconds_count",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("exposition missing %q:\n%s", want, body)
		}
	}
	// 每一行非注释内容都必须带上 service 标签，或者属于 HELP/TYPE
	// 那一组，而且不允许有空行。
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "chargepilot_") {
			t.Fatalf("unexpected exposition line: %q", line)
		}
	}
}
