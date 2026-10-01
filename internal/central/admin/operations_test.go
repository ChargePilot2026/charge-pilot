package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRetiredAdminRoutesAreRemoved(t *testing.T) {
	router := gin.New()
	(ResourceAPI{}).Register(router)
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodPut, "/api/v1/admin/risk-config"},
		{http.MethodGet, "/api/v1/admin/customer-service"},
		{http.MethodPost, "/api/v1/admin/customer-service"},
		{http.MethodPut, "/api/v1/admin/customer-service/1"},
		{http.MethodDelete, "/api/v1/admin/customer-service/1"},
		{http.MethodGet, "/api/v1/admin/alert-rules"},
		{http.MethodPost, "/api/v1/admin/alert-rules"},
		{http.MethodPut, "/api/v1/admin/alert-rules/1"},
		{http.MethodDelete, "/api/v1/admin/alert-rules/1"},
		{http.MethodPost, "/api/v1/admin/alert-rules/1/resolve"},
		{http.MethodGet, "/api/v1/admin/alert-subscriptions"},
		{http.MethodPost, "/api/v1/admin/alert-subscriptions"},
		{http.MethodDelete, "/api/v1/admin/alert-subscriptions/1"},
		{http.MethodGet, "/api/v1/admin/ota/packages"},
		{http.MethodPost, "/api/v1/admin/ota/packages"},
		{http.MethodDelete, "/api/v1/admin/ota/packages/1"},
		{http.MethodGet, "/api/v1/admin/ota/schedules"},
		{http.MethodPost, "/api/v1/admin/ota/schedules"},
		{http.MethodPost, "/api/v1/admin/ota/schedules/1/trigger"},
		{http.MethodPost, "/api/v1/admin/ota/schedules/1/cancel"},
	} {
		t.Run(endpoint.method+" "+endpoint.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(endpoint.method, endpoint.path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("retired route returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
	for _, path := range []string{"feedback", "device-fault-reports", "billing/wallet-risks", "alerts"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/"+path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("retained route %s returned %d", path, response.Code)
		}
	}
	for _, path := range []string{"alerts/1/ack"} {
		method := http.MethodPut
		if path == "alerts/1/ack" {
			method = http.MethodPost
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/admin/"+path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("retained route %s returned %d", path, response.Code)
		}
	}
	var document struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openAPIDocument, &document); err != nil {
		t.Fatal(err)
	}
	for path := range document.Paths {
		if strings.Contains(path, "customer-service") || strings.Contains(path, "alert-rules") || strings.Contains(path, "alert-subscriptions") || strings.Contains(path, "/ota/") {
			t.Fatalf("retired route still documented: %s", path)
		}
	}
}

func TestRetiredPermissionsPreserveFeedbackWalletAndDeviceAlerts(t *testing.T) {
	codes := []string{
		"customer_service.read", "feedback.read", "customer_service.create", "feedback.reply", "customer_service.update", "wallet.read", "customer_service.delete",
		"ota.read", "ota.package.create", "ota.package.delete", "ota.schedule.create", "ota.schedule.trigger", "settings.ota.update",
		"alert.rule.create", "alert.rule.update", "alert.rule.delete", "alert.subscription.create", "alert.read", "alert.ack", "alert.risk_config.update",
		"membership.create", "pricing.template.create",
	}
	want := []string{"feedback.read", "feedback.reply", "wallet.read", "alert.read", "alert.ack"}
	if got := withoutRetiredPermissions(codes); !reflect.DeepEqual(got, want) {
		t.Fatalf("visible permissions = %v, want %v", got, want)
	}
}

// TestNormalizeRowsTurnsTinyintFlagsIntoBooleans 验证数据库 tinyint(1) 在响应中转换为布尔值。
// 设备能力字段必须输出 true/false，保持前端严格布尔判断与后端能力校验一致。
func TestNormalizeRowsTurnsTinyintFlagsIntoBooleans(t *testing.T) {
	rows := []map[string]any{{
		"device_id":               "demo_DC589-0001",
		"reports_energy":          int64(1),
		"reports_segmented_power": int64(0),
		"enabled":                 int64(1),
		"station_id":              int64(14),
		"status":                  "active",
	}}
	normalizeRows(rows)

	row := rows[0]
	for _, name := range []string{"reports_energy", "enabled"} {
		if v, ok := row[name].(bool); !ok || !v {
			t.Errorf("%s = %#v，期望 true", name, row[name])
		}
	}
	if v, ok := row["reports_segmented_power"].(bool); !ok || v {
		t.Errorf("reports_segmented_power = %#v，期望 false", row["reports_segmented_power"])
	}
	// 布尔归一化不应改变数字和字符串列的类型。
	if _, ok := row["station_id"].(int64); !ok {
		t.Errorf("station_id 被改成了 %#v，不该动", row["station_id"])
	}
	if row["status"] != "active" {
		t.Errorf("status = %#v，不该动", row["status"])
	}
}
