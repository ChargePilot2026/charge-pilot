package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestAdminProfileEditingKeepsUnchangedRoleAndSession(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("MySQL with seeded permissions required")
	}
	db := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")
	var roleIDs, accountIDs []uint64
	actor := Account{Username: "selfedit-" + tag, PasswordHash: "unused-test-password-hash", Status: "active", AuthVersion: 7}
	t.Cleanup(func() {
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{"DELETE FROM audit_log WHERE actor_id = ? AND actor_name = ?", []any{actor.ID, actor.Username}},
			{"DELETE FROM admin_user_role WHERE id IN ?", []any{accountIDs}},
			{"DELETE FROM role_permission WHERE role_id IN ?", []any{roleIDs}},
			{"DELETE FROM role WHERE id IN ?", []any{roleIDs}},
		} {
			if err := db.Exec(statement.query, statement.args...).Error; err != nil {
				t.Errorf("fixture cleanup: %v", err)
			}
		}
	})
	role := func(suffix string, permissions ...string) uint64 {
		t.Helper()
		row := struct {
			ID   uint64
			Code string
			Name string
		}{Code: "selfedit_" + suffix + "_" + tag, Name: "自定义角色-" + suffix}
		if err := db.Table("role").Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		roleIDs = append(roleIDs, row.ID)
		for _, permission := range permissions {
			result := db.Exec("INSERT INTO role_permission(role_id,permission_id) SELECT ?,id FROM permission WHERE code = ?", row.ID, permission)
			if result.Error != nil || result.RowsAffected != 1 {
				t.Fatalf("seed permission %s: rows=%d err=%v", permission, result.RowsAffected, result.Error)
			}
		}
		return row.ID
	}
	actor.RoleID = role("owner", "admin_user.update", "dashboard.read")
	lowerRole := role("lower", "dashboard.read")
	higherRole := role("higher", "admin_user.update", "dashboard.read", "admin_user.create")
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	accountIDs = append(accountIDs, actor.ID)
	target := Account{Username: "selfedit-target-" + tag, PasswordHash: "unused-test-password-hash", RoleID: actor.RoleID, Status: "active", AuthVersion: 7}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	accountIDs = append(accountIDs, target.ID)
	profile, err := (Store{DB: db}).Profile(context.Background(), actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.RoleName != "自定义角色-owner" || profile.Role != "selfedit_owner_"+tag {
		t.Fatalf("profile did not read custom role name: %+v", profile)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("admin_profile", profile) })
	router.PUT("/admin-users/:id", (ResourceAPI{Store: ResourceStore{AdminDB: db}}).updateAdminUser)
	router.GET("/admin-users", (ResourceAPI{Store: ResourceStore{AdminDB: db}}).listAdminUsers)
	checkRoleName := func(want string) {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin-users?keyword="+actor.Username, nil))
		var out struct {
			Data Page[AdminUserRow] `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil || response.Code != 200 || len(out.Data.Items) != 1 || out.Data.Items[0].RoleName == nil || *out.Data.Items[0].RoleName != want || out.Data.Items[0].RoleCode == nil || *out.Data.Items[0].RoleCode != profile.Role {
			t.Fatalf("list lost actual role name: %s %v", response.Body.String(), err)
		}
		current, err := (Store{DB: db}).Profile(context.Background(), actor.ID)
		if err != nil || current.RoleName != want {
			t.Fatalf("profile lost renamed role: %+v %v", current, err)
		}
	}
	checkRoleName("自定义角色-owner")
	if err := db.Table("role").Where("id=?", actor.RoleID).Update("name", "中文维护组").Error; err != nil {
		t.Fatal(err)
	}
	checkRoleName("中文维护组")
	request := func(id uint64, input any, wantStatus int, revoked bool) {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/admin-users/%d", id), strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, req)
		if response.Code != wantStatus {
			t.Fatalf("update returned %d, want %d: %s", response.Code, wantStatus, response.Body.String())
		}
		if wantStatus == http.StatusOK {
			var envelope struct {
				Data struct {
					SessionsRevoked *bool `json:"sessions_revoked"`
				}
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Data.SessionsRevoked == nil || *envelope.Data.SessionsRevoked != revoked {
				t.Fatalf("sessions_revoked=%v, want %v", envelope.Data.SessionsRevoked, revoked)
			}
		}
	}
	checkAccount := func(id, roleID, version uint64) {
		t.Helper()
		var row Account
		if err := db.Where("id = ?", id).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.RoleID != roleID || row.AuthVersion != version || row.Status != "active" {
			t.Fatalf("role=%d version=%d status=%s, want %d/%d/active", row.RoleID, row.AuthVersion, row.Status, roleID, version)
		}
	}

	// 编辑表单会完整回传原角色和状态；只改资料不能被当成修改权限或撤销登录。
	request(actor.ID, gin.H{"display_name": "Updated profile", "phone": "13800000000", "email": "selfedit@example.com", "role_id": actor.RoleID, "status": "active"}, http.StatusOK, false)
	checkAccount(actor.ID, actor.RoleID, 7)
	var updated AdminUserRow
	if err := db.Table("admin_user_role").Where("id = ?", actor.ID).Take(&updated).Error; err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName == nil || *updated.DisplayName != "Updated profile" || updated.Phone == nil || *updated.Phone != "13800000000" || updated.Email == nil || *updated.Email != "selfedit@example.com" {
		t.Fatalf("profile fields were not updated: %+v", updated)
	}
	request(actor.ID, gin.H{"role_id": actor.RoleID}, http.StatusOK, false)
	request(actor.ID, gin.H{"role_id": lowerRole}, http.StatusConflict, false)
	request(actor.ID, gin.H{"status": "disabled"}, http.StatusConflict, false)
	checkAccount(actor.ID, actor.RoleID, 7)

	// 编辑他人的资料也不撤销会话；真实换角色仍检查授予范围，并让旧会话失效。
	request(target.ID, gin.H{"display_name": "Updated target", "role_id": actor.RoleID, "status": "active"}, http.StatusOK, false)
	checkAccount(target.ID, actor.RoleID, 7)
	request(target.ID, gin.H{"role_id": higherRole}, http.StatusForbidden, false)
	checkAccount(target.ID, actor.RoleID, 7)
	request(target.ID, gin.H{"role_id": lowerRole}, http.StatusOK, true)
	checkAccount(target.ID, lowerRole, 8)
	request(target.ID, gin.H{"role_id": lowerRole}, http.StatusOK, false)
	checkAccount(target.ID, lowerRole, 8)
}
