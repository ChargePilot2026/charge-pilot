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

// The casework queue is fed by exactly this endpoint. It must accept a rating
// for a finished order, refuse a second one, and never leak another customer's
// order.
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
	// The ids here are fixed while the order number is not, so nothing left
	// behind can be matched by name on the next run and the primary keys collide
	// immediately. Without this the test passes once per database and reports a
	// duplicate-key failure that looks like a product problem.
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

	// An in-progress order cannot be rated yet.
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

	// Out-of-range and unknown values are rejected before the lookup.
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

	// A valid submission lands with the order's device recorded.
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

	// A second submission for the same order is refused.
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
