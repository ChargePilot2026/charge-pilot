package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestChargeUserRechargesPaginationAndScopeIntegration(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("set disposable MySQL and Redis URLs")
	}
	ctx := context.Background()
	db := openFinanceDB(t, "TEST_USER_DATABASE_URL")
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Exec("DROP TEMPORARY TABLE IF EXISTS `user`").Error; err != nil {
			t.Errorf("drop temporary users: %v", err)
		}
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("rollback recharge fixtures: %v", err)
		}
	})
	// 大 ID 在连接内的同结构临时表验证，避免事务回滚后留下公共表的自增序列变更。
	var tableName, definition string
	if err := tx.Raw("SHOW CREATE TABLE `user`").Row().Scan(&tableName, &definition); err != nil {
		t.Fatal(err)
	}
	const userDefinitionPrefix = "CREATE TABLE `user`"
	if tableName != "user" || !strings.HasPrefix(definition, userDefinitionPrefix) {
		t.Fatal("unexpected user table definition")
	}
	definition = strings.Replace(definition, userDefinitionPrefix, "CREATE TEMPORARY TABLE `user`", 1)
	if err := tx.Exec(definition).Error; err != nil {
		t.Fatal(err)
	}
	options, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(options)
	t.Cleanup(func() { _ = cache.Close() })
	sessions := Sessions{Redis: cache}
	jwt, err := auth.NewJWT(strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")
	issueActor := func(name string, allow bool) string {
		t.Helper()
		role := struct {
			ID   uint64
			Code string
			Name string
		}{Code: "recharges_" + name + "_" + tag, Name: name}
		if err := tx.Table("role").Create(&role).Error; err != nil {
			t.Fatal(err)
		}
		if allow {
			result := tx.Exec("INSERT INTO role_permission(role_id,permission_id) SELECT ?,id FROM permission WHERE code='charge_user.read'", role.ID)
			if result.Error != nil || result.RowsAffected != 1 {
				t.Fatalf("seed read permission: rows=%d err=%v", result.RowsAffected, result.Error)
			}
		}
		account := Account{Username: "recharges-" + name + "-" + tag, PasswordHash: "unused-test-password-hash", RoleID: role.ID, Status: "active"}
		if err := tx.Create(&account).Error; err != nil {
			t.Fatal(err)
		}
		sid, refresh, err := sessions.Create(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := sessions.Revoke(ctx, refresh); err != nil {
				t.Errorf("revoke fixture session: %v", err)
			}
		})
		now := time.Now().Unix()
		token, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(account.ID, 10), Kind: "admin", SessionID: sid, IssuedAt: now, ExpiresAt: now + 3600})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	allowedToken, deniedToken := issueActor("reader", true), issueActor("denied", false)
	const userID = uint64(9007199254741115)
	start := time.Date(2077, 6, 15, 9, 0, 0, 0, time.UTC)
	for i := uint64(0); i < 4; i++ {
		var deletedAt any
		status := "active"
		if i == 2 {
			deletedAt = start
		}
		if i == 3 {
			status = "frozen"
		}
		if err := tx.Exec("INSERT INTO user(id,openid,status,deleted_at) VALUES (?,?,?,?)", userID+i, fmt.Sprintf("recharge-user-%s-%d", tag, i), status, deletedAt).Error; err != nil {
			t.Fatal(err)
		}
	}
	var payments []charge.PaymentOrderRecord
	states := []struct {
		status, payment string
		paid, refunded  int64
	}{
		{"initiated", "pending", 0, 0}, {"closed", "pending", 0, 0}, {"failed", "pending", 0, 0},
		{"paid", "paid", 500, 0}, {"partial_refunded", "partial_refunded", 500, 125}, {"refunded", "refunded", 500, 500},
	}
	createPayment := func(owner uint64, biz string, index int, deleted bool) charge.PaymentOrderRecord {
		t.Helper()
		state := states[3]
		if index < len(states) {
			state = states[index]
		}
		payment := charge.PaymentOrderRecord{OrderNo: fmt.Sprintf("recharge-%s-%d", tag, index), UserID: owner,
			BizType: biz, PayMethod: "wechat", TotalCents: 500, PaidCents: state.paid, RefundedCents: state.refunded,
			Status: state.status, CreatedMonth: time.Date(2077, 6, 1, 0, 0, 0, 0, time.UTC)}
		if state.paid > 0 {
			payment.PaidAt = sql.NullTime{Time: start, Valid: true}
		}
		if err := tx.Create(&payment).Error; err != nil {
			t.Fatal(err)
		}
		created := start
		if index == 0 {
			created = start.Add(2 * time.Minute)
		} else if index == 1 {
			created = start.Add(time.Minute)
		}
		values := map[string]any{"created_at": created}
		if deleted {
			values["deleted_at"] = start
		}
		if err := tx.Table("payment_order").Where("id=?", payment.ID).Updates(values).Error; err != nil {
			t.Fatal(err)
		}
		return payment
	}
	for i := 0; i < 30; i++ {
		payments = append(payments, createPayment(userID, "wallet_recharge", i, false))
	}
	createPayment(userID, "wallet_recharge", 30, true)
	createPayment(userID, "charge", 31, false)
	otherPayment := createPayment(userID+1, "wallet_recharge", 32, false)
	createPayment(userID+2, "wallet_recharge", 33, false)
	store := ResourceStore{UserDB: tx}
	api := ResourceAPI{Store: store, Auth: API{Store: Store{DB: tx}, Sessions: sessions, JWT: jwt}}
	router := gin.New()
	api.registerChargeUsers(router)
	request := func(t *testing.T, id uint64, query, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/charge-users/%d/recharges%s", id, query), nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	decodePage := func(t *testing.T, response *httptest.ResponseRecorder) Page[PaymentOrderView] {
		t.Helper()
		if response.Code != http.StatusOK {
			t.Fatalf("recharge list status=%d", response.Code)
		}
		var envelope struct {
			Data Page[PaymentOrderView] `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	// 不同创建时间优先；同一创建时间按 ID 倒序，翻页不重复或遗漏。
	want := []charge.PaymentOrderRecord{payments[0], payments[1]}
	for i := len(payments) - 1; i >= 2; i-- {
		want = append(want, payments[i])
	}
	t.Run("default and stable pagination", func(t *testing.T) {
		page := decodePage(t, request(t, userID, "", allowedToken))
		if page.Page != 1 || page.PageSize != 20 || page.Total != 30 || len(page.Items) != 20 {
			t.Fatalf("default page: page=%d size=%d total=%d rows=%d", page.Page, page.PageSize, page.Total, len(page.Items))
		}
		for pageNo := 1; pageNo <= 3; pageNo++ {
			page = decodePage(t, request(t, userID, fmt.Sprintf("?page=%d&page_size=10", pageNo), allowedToken))
			if page.Page != pageNo || page.PageSize != 10 || page.Total != 30 || len(page.Items) != 10 {
				t.Fatalf("page %d: total=%d rows=%d", pageNo, page.Total, len(page.Items))
			}
			for i, row := range page.Items {
				if row.PaymentOrderID != want[(pageNo-1)*10+i].ID {
					t.Fatalf("page %d row %d has wrong order", pageNo, i)
				}
			}
		}
	})
	t.Run("scope and actual payment states", func(t *testing.T) {
		response := request(t, userID, "?page_size=100", allowedToken)
		page := decodePage(t, response)
		if page.Total != 30 || len(page.Items) != 30 || !strings.Contains(response.Body.String(), `"user_id":"9007199254741115"`) {
			t.Fatal("recharge scope or string user ID lost")
		}
		byID := make(map[uint64]PaymentOrderView, len(page.Items))
		for _, row := range page.Items {
			if row.UserID != userID || row.BizType != "wallet_recharge" || row.ChargeOrderID != nil || row.ChargeOrderNo != nil {
				t.Fatal("recharge list leaked another user or charging order")
			}
			byID[row.PaymentOrderID] = row
		}
		for i, state := range states {
			row := byID[payments[i].ID]
			if row.Status != state.status || row.TotalCents != 500 || row.PaidCents != state.paid || row.RefundedCents != state.refunded ||
				row.PaymentStatus != state.payment || (row.PaidAt == nil) != (state.paid == 0) {
				t.Fatalf("payment state %s changed", state.status)
			}
		}
		global, err := store.PaymentOrders(ctx, PaymentOrderQuery{PageQuery: PageQuery{Page: 1, PageSize: 20}, OrderNo: otherPayment.OrderNo})
		if err != nil || global.Total != 1 || len(global.Items) != 1 || global.Items[0].UserID != userID+1 {
			t.Fatalf("global payment list acquired an unintended user filter: err=%v", err)
		}
	})
	t.Run("empty pages and frozen users", func(t *testing.T) {
		for _, sample := range []struct {
			id    uint64
			query string
			total int64
		}{{userID, "?page=4&page_size=10", 30}, {userID + 3, "", 0}} {
			response := request(t, sample.id, sample.query, allowedToken)
			page := decodePage(t, response)
			if page.Total != sample.total || page.Items == nil || len(page.Items) != 0 || !strings.Contains(response.Body.String(), `"items":[]`) {
				t.Fatal("empty recharge page did not return an empty array")
			}
		}
	})
	t.Run("permissions missing users and query overrides", func(t *testing.T) {
		for _, sample := range []struct {
			id     uint64
			query  string
			token  string
			status int
		}{
			{userID, "", "", http.StatusUnauthorized},
			{userID, "", deniedToken, http.StatusForbidden},
			{userID + 2, "", allowedToken, http.StatusNotFound},
			{userID + 4, "", allowedToken, http.StatusNotFound},
			{userID, fmt.Sprintf("?user_id=%d", userID+1), allowedToken, http.StatusBadRequest},
			{userID, "?biz_type=charge", allowedToken, http.StatusBadRequest},
			{userID, "?page_size=101", allowedToken, http.StatusBadRequest},
		} {
			if response := request(t, sample.id, sample.query, sample.token); response.Code != sample.status {
				t.Fatalf("scope/permission response=%d want=%d", response.Code, sample.status)
			}
		}
	})
}
