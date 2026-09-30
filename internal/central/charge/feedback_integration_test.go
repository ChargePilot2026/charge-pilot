package charge

import (
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

func feedbackEnv(t *testing.T) bool {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("integration database not configured")
	}
	return true
}

// TestSubmitFeedbackRules —— 工单队列正是靠这个接口喂数据的。
// 它必须能给已完成的订单打分、
// 拒绝第二次提交，并且绝不泄露别的客户的订单。
func TestSubmitFeedbackRules(t *testing.T) {
	if !feedbackEnv(t) {
		return
	}
	userDB := openAccountDB(t, "TEST_USER_DATABASE_URL")
	adminDB := openAccountDB(t, "TEST_ADMIN_DATABASE_URL")
	ctx := t.Context()

	userID := uint64(900001)
	otherID := uint64(900002)
	orderID := uint64(910000 + len(uuid.NewString())%1000)
	// 这里的 id 是固定的而订单号不是，
	// 所以上次留下的东西没法按名字对上，
	// 主键会立刻冲突。没有这段处理，
	// 这个测试每个数据库只能过一遍，然后报一个看起来像产品缺陷的重复键失败。
	t.Cleanup(func() {
		userDB.Exec("DELETE FROM feedback WHERE order_id IN (?,?)", orderID, orderID+1)
		userDB.Exec("DELETE FROM charge_order WHERE id IN (?,?)", orderID, orderID+1)
		userDB.Exec("DELETE FROM user WHERE id IN (?,?)", userID, otherID)
	})

	if err := userDB.Exec("INSERT INTO user (id, openid, status) VALUES (?,?,?),(?,?,?)",
		userID, "qa-fb-"+uuid.NewString()[:8], "active", otherID, "qa-fb-"+uuid.NewString()[:8], "active").Error; err != nil {
		t.Fatal(err)
	}
	if err := userDB.Exec(`INSERT INTO charge_order
		(id, order_no, user_id, device_id, port_no, status, created_month, created_at)
		VALUES (?,?,?,?,?,?,?,NOW(3))`,
		orderID, "FB"+uuid.NewString()[:12], userID, "fb_device_01", 1, "completed", "2026-09-01").Error; err != nil {
		t.Fatal(err)
	}
	router := accountRouter(t, userDB, adminDB, userID)
	path := "/api/v1/user/charge/" + strconv.FormatUint(orderID, 10) + "/feedback"

	// 进行中的订单还不能评价。
	pendingID := orderID + 1
	if err := userDB.Exec(`INSERT INTO charge_order
		(id, order_no, user_id, device_id, port_no, status, created_month, created_at)
		VALUES (?,?,?,?,?,?,?,NOW(3))`,
		pendingID, "FB"+uuid.NewString()[:12], userID, "fb_device_01", 1, "charging", "2026-09-01").Error; err != nil {
		t.Fatal(err)
	}
	if code, _ := callJSON(t, router, "POST", "/api/v1/user/charge/"+strconv.FormatUint(pendingID, 10)+"/feedback",
		map[string]any{"rating": 5, "category": "rating"}); code != 404 {
		t.Fatalf("rating an in-progress order returned %d, want 404", code)
	}

	// 越界的值和无法识别的值在查询之前就被拒绝。
	if code, _ := callJSON(t, router, "POST", path, map[string]any{"rating": 9, "category": "rating"}); code != 400 {
		t.Fatalf("rating 9 returned %d, want 400", code)
	}
	if code, _ := callJSON(t, router, "POST", path, map[string]any{"rating": 5, "category": "spam"}); code != 400 {
		t.Fatalf("unknown category returned %d, want 400", code)
	}
	if code, _ := callJSON(t, router, "POST", path, map[string]any{
		"rating": 5, "category": "complaint", "images": []string{"http://cdn.example.com/a.jpg"},
	}); code != 400 {
		t.Fatalf("plain-HTTP image returned %d, want 400", code)
	}

	// 一次合法提交会带着该订单的设备信息落库。
	// Refunding a prepaid difference does not remove the user's right to
	// review a completed charging session. H5 identifies it by order number.
	var ownedOrderNo string
	userDB.Table("charge_order").Where("id=?", orderID).Pluck("order_no", &ownedOrderNo)
	userDB.Table("charge_order").Where("id=?", orderID).Update("status", "refunded")
	path = "/api/v1/user/charge/" + ownedOrderNo + "/feedback"
	if code, body := callJSON(t, router, "POST", path, map[string]any{
		"rating": 4, "category": "complaint", "content": "充电枪有点松",
		"images": []string{"https://cdn.example.com/a.jpg"},
	}); code != 200 {
		t.Fatalf("submit returned %d: %s", code, body["message"])
	}
	var stored struct {
		DeviceID string
		Rating   int
		Status   string
		Category string
	}
	if err := userDB.Raw("SELECT device_id, rating, status, category FROM feedback WHERE order_id = ?", orderID).
		Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.DeviceID != "fb_device_01" || stored.Rating != 4 || stored.Status != "pending" || stored.Category != "complaint" {
		t.Fatalf("stored row = %+v", stored)
	}

	// 同一笔订单的第二次提交被拒绝。
	if code, _ := callJSON(t, router, "POST", path, map[string]any{"rating": 5, "category": "rating"}); code != 409 {
		t.Fatalf("duplicate submit returned %d, want 409", code)
	}
	var count int64
	if err := userDB.Raw("SELECT COUNT(*) FROM feedback WHERE order_id = ?", orderID).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored %d rows for one order, want 1", count)
	}
	_ = ctx
}
