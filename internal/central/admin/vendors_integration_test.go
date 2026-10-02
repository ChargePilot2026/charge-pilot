package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/provision"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// 使用真实会话、权限和两个独立数据库，验证后台通过网关管理厂商。
func TestAdminVendorsIntegration(t *testing.T) {
	for _, key := range []string{"TEST_ADMIN_DATABASE_URL", "TEST_GATEWAY_DATABASE_URL", "TEST_REDIS_URL"} {
		if os.Getenv(key) == "" {
			t.Skipf("disposable environment required: %s", key)
		}
	}
	ctx := context.Background()
	adb, gdb := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL"), openFinanceDB(t, "TEST_GATEWAY_DATABASE_URL")
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	prefix := "vendor-test-" + suffix
	roleIDs, accountIDs := []uint64{}, []uint64{}
	t.Cleanup(func() {
		for _, query := range []string{
			"DELETE FROM audit_log WHERE module='vendor' AND actor_id IN ?",
			"DELETE FROM admin_data_scope WHERE admin_user_id IN ?",
			"DELETE FROM admin_user WHERE id IN ?",
		} {
			if len(accountIDs) > 0 {
				if err := adb.Exec(query, accountIDs).Error; err != nil {
					t.Error(err)
				}
			}
		}
		for _, table := range []string{"role_permission", "role"} {
			column := "role_id"
			if table == "role" {
				column = "id"
			}
			if len(roleIDs) > 0 {
				if err := adb.Table(table).Where(column+" IN ?", roleIDs).Delete(nil).Error; err != nil {
					t.Error(err)
				}
			}
		}
		if err := gdb.Exec("DELETE FROM vendor WHERE vendor_code LIKE ?", prefix+"%").Error; err != nil {
			t.Error(err)
		}
	})
	opts, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(opts)
	t.Cleanup(func() { _ = cache.Close() })
	jwt, err := auth.NewJWT(strings.Repeat("vendor-test-secret", 3))
	if err != nil {
		t.Fatal(err)
	}
	authAPI := API{Store: Store{DB: adb}, Sessions: Sessions{Redis: cache}, JWT: jwt}
	gr := httpapi.NewRouter()
	provision.API{DB: gdb, ServiceToken: "test-vendor-service"}.Register(gr)
	gateway := httptest.NewServer(gr)
	t.Cleanup(gateway.Close)
	api := ResourceAPI{Store: ResourceStore{AdminDB: adb}, Auth: authAPI, GatewayURL: gateway.URL, ServiceToken: "test-vendor-service"}
	router := httpapi.NewRouter()
	api.registerVendors(router)
	newRole := func(code, permission string) string {
		t.Helper()
		code = prefix + "-" + code
		if err := adb.Exec("INSERT INTO role (code,name) VALUES (?,?)", code, code).Error; err != nil {
			t.Fatal(err)
		}
		var id uint64
		if err := adb.Table("role").Where("code=?", code).Pluck("id", &id).Error; err != nil {
			t.Fatal(err)
		}
		roleIDs = append(roleIDs, id)
		if err := adb.Exec("INSERT INTO role_permission(role_id,permission_id) SELECT ?,id FROM permission WHERE code=?", id, permission).Error; err != nil {
			t.Fatal(err)
		}
		return code
	}
	token := func(name, role string) (string, uint64) {
		t.Helper()
		name = prefix + "-" + name
		if err := adb.Exec("INSERT INTO admin_user(username,password_hash,role_id) SELECT ?,'unused-test-hash',id FROM role WHERE code=? AND deleted_at IS NULL", name, role).Error; err != nil {
			t.Fatal(err)
		}
		var account Account
		if err := adb.Where("username=?", name).Take(&account).Error; err != nil {
			t.Fatal(err)
		}
		accountIDs = append(accountIDs, account.ID)
		sid, refresh, err := authAPI.Sessions.Create(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = authAPI.Sessions.Revoke(ctx, refresh) })
		raw, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(account.ID, 10), Kind: "admin", SessionID: sid, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return raw, account.ID
	}
	full, fullID := token("full", "customer_admin")
	readonly, _ := token("readonly", newRole("readonly", "vendor.read"))
	onboarding, _ := token("onboarding", newRole("onboarding", "device.import"))
	call := func(raw, method, path string, body any, status int) json.RawMessage {
		t.Helper()
		encoded, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "/api/v1/admin/"+path, strings.NewReader(string(encoded)))
		req.Header.Set("Content-Type", "application/json")
		if raw != "" {
			req.Header.Set("Authorization", "Bearer "+raw)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, out.Code, status, out.Body.String())
		}
		var response struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Data
	}
	input := vendorInput{VendorCode: prefix + "-a", VendorName: "  测试厂商 A  ", AdapterClass: "dc589", Protocol: "tcp", Status: "enabled"}
	var first vendorRow
	if err := json.Unmarshal(call(full, "POST", "vendors", input, 200), &first); err != nil {
		t.Fatal(err)
	}
	if first.ID == 0 || first.VendorName != "测试厂商 A" {
		t.Fatalf("创建厂商失败：%+v", first)
	}
	call(full, "POST", "vendors", input, 409)
	path := fmt.Sprintf("vendors/%d", first.ID)
	call("", "GET", "vendors", nil, 401)
	call(readonly, "GET", path, nil, 200)
	call(readonly, "POST", "vendors", input, 403)
	call(readonly, "PUT", path, input, 403)
	call(onboarding, "GET", "vendors", nil, 403)
	var options Page[vendorRow]
	if err := json.Unmarshal(call(onboarding, "GET", "vendor-options?keyword="+url.QueryEscape(prefix)+"&status=disabled", nil, 200), &options); err != nil {
		t.Fatal(err)
	}
	if len(options.Items) != 1 || options.Items[0].ID != first.ID || options.Items[0].Status != "enabled" {
		t.Fatalf("设备选项未强制启用状态：%+v", options)
	}
	input.VendorName, input.Status = "厂商 A 已更新", "disabled"
	call(full, "PUT", path, input, 200)
	if err := json.Unmarshal(call(onboarding, "GET", "vendor-options?keyword="+url.QueryEscape(prefix), nil, 200), &options); err != nil {
		t.Fatal(err)
	}
	if len(options.Items) != 0 {
		t.Fatalf("停用厂商仍可新建设备：%+v", options)
	}
	input.VendorCode, input.VendorName, input.Status = prefix+"-b", "厂商 B", "enabled"
	var second vendorRow
	if err := json.Unmarshal(call(full, "POST", "vendors", input, 200), &second); err != nil {
		t.Fatal(err)
	}
	// 通过真实后台数据范围限制同一会话，客户端 ids 不能扩大范围。
	if err := adb.Exec("INSERT INTO admin_data_scope(admin_user_id,scope_type,scope_id,created_by) VALUES (?,'vendor',?,?)", fullID, first.ID, fullID).Error; err != nil {
		t.Fatal(err)
	}
	var page Page[vendorRow]
	if err := json.Unmarshal(call(full, "GET", fmt.Sprintf("vendors?keyword=%s&ids=%d", url.QueryEscape(prefix), second.ID), nil, 200), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != first.ID || page.Total != 1 {
		t.Fatalf("厂商范围泄漏：%+v", page)
	}
	for _, permission := range page.Permissions {
		if permission == "vendor.create" {
			t.Fatal("范围内账号不应出现创建按钮")
		}
	}
	call(full, "GET", fmt.Sprintf("vendors/%d", second.ID), nil, 404)
	call(full, "PUT", fmt.Sprintf("vendors/%d", second.ID), input, 404)
	call(full, "POST", "vendors", input, 403)
	if err := json.Unmarshal(call(full, "GET", "vendor-options?keyword="+url.QueryEscape(prefix), nil, 200), &options); err != nil {
		t.Fatal(err)
	}
	if len(options.Items) != 0 {
		t.Fatal("设备厂商选项绕过数据范围")
	}
	var audited int64
	if err := adb.Table("audit_log").Where("module='vendor' AND actor_id=?", fullID).Count(&audited).Error; err != nil || audited != 3 {
		t.Fatalf("厂商写入未审计：count=%d err=%v", audited, err)
	}
	// 预设设备运维角色可管理厂商，客服和财务不会自动获得写权限。
	for _, role := range []string{"customer_admin", "customer_ops", "dev_admin", "customer_cs", "customer_finance"} {
		// dev_admin 仅部分部署已有，迁移给已有角色授权，不额外创建开发账号角色。
		if role == "dev_admin" {
			var existing int64
			if err := adb.Table("role").Where("code=? AND deleted_at IS NULL", role).Count(&existing).Error; err != nil {
				t.Fatal(err)
			}
			if existing == 0 {
				continue
			}
		}
		var count int64
		if err := adb.Table("role_permission rp").Joins("JOIN role r ON r.id=rp.role_id").Joins("JOIN permission p ON p.id=rp.permission_id").Where("r.code=? AND p.code IN ?", role, []string{"vendor.read", "vendor.create", "vendor.update"}).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		want := int64(3)
		if role == "customer_cs" || role == "customer_finance" {
			want = 0
		}
		if count != want {
			t.Fatalf("role %s vendor permissions got %d want %d", role, count, want)
		}
	}
}
