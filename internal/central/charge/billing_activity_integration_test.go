package charge

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

// The order hook runs inside the settlement transaction. It must grant exactly
// one coupon per qualifying order, and a broken rule must not stop the charge
// from being billed.
func TestApplyOrderCampaignsGrantsThresholdOnce(t *testing.T) {
	orm := activityDB(t)
	fx := newActivityFixture(t, orm, "threshold_redeem", 5, 0)
	defer fx.cleanup()
	now := time.Now().UTC()
	if err := orm.Exec("UPDATE coupon_activity_rule SET threshold_cents = 3000 WHERE id = ?", fx.ruleID).Error; err != nil {
		t.Fatal(err)
	}
	order := ChargeOrderRecord{OrderNo: "AC" + uuid.NewString()[:10], UserID: fx.userID}

	// Below the threshold nothing is granted.
	applyOrderCampaigns(orm, order, 2999)
	if n := fx.grantRows(t); n != 0 {
		t.Fatalf("a 2999-cent order granted %d coupons", n)
	}
	// Qualifying order grants once.
	applyOrderCampaigns(orm, order, 3500)
	if n := fx.grantRows(t); n != 1 {
		t.Fatalf("a 3500-cent order granted %d coupons, want 1", n)
	}
	// Re-evaluating the same order must not grant a second coupon.
	applyOrderCampaigns(orm, order, 3500)
	if n := fx.grantRows(t); n != 1 {
		t.Fatalf("re-evaluation granted %d coupons, want 1", n)
	}
	_ = now
}

// A holiday campaign has no amount condition: it fires for every settled order
// inside its window, but the per-user limit still applies.
func TestApplyOrderCampaignsHolidayRespectsPerUserLimit(t *testing.T) {
	orm := activityDB(t)
	fx := newActivityFixture(t, orm, "holiday", 1, 0)
	defer fx.cleanup()

	first := ChargeOrderRecord{OrderNo: "AC" + uuid.NewString()[:10], UserID: fx.userID}
	applyOrderCampaigns(orm, first, 100)
	if n := fx.grantRows(t); n != 1 {
		t.Fatalf("holiday campaign granted %d coupons, want 1", n)
	}
	second := ChargeOrderRecord{OrderNo: "AC" + uuid.NewString()[:10], UserID: fx.userID}
	applyOrderCampaigns(orm, second, 100)
	if n := fx.grantRows(t); n != 1 {
		t.Fatalf("a second holiday order breached the per-user cap: %d", n)
	}
}

// A rule pointing at a deleted coupon must not abort the settlement. The charge
// has to be billed either way.
func TestApplyOrderCampaignsSurvivesBrokenRule(t *testing.T) {
	url := os.Getenv("TEST_USER_DATABASE_URL")
	if url == "" {
		t.Skip("set a disposable user database URL")
	}
	db, err := dbconn.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO user (openid) VALUES (?)", "act-broken-"+uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	var userID uint64
	if err := orm.Raw("SELECT LAST_INSERT_ID()").Scan(&userID).Error; err != nil {
		t.Fatal(err)
	}
	defer func() { _ = orm.Exec("DELETE FROM user WHERE id = ?", userID) }()

	now := time.Now().UTC()
	if err := orm.Exec(`INSERT INTO coupon_activity_rule
		(name, trigger_type, coupon_id, threshold_cents, max_grants, per_user_limit, status, start_at, end_at)
		VALUES ('坏规则', 'threshold_redeem', 99999999, 100, 0, 1, 'active', ?, ?)`,
		now.Add(-time.Hour), now.Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	badRuleID := lastID(t, orm)
	defer func() { _ = orm.Exec("DELETE FROM coupon_activity_rule WHERE id = ?", badRuleID) }()

	order := ChargeOrderRecord{OrderNo: "AC" + uuid.NewString()[:10], UserID: userID}
	// Must not panic and must not surface an error to the caller.
	applyOrderCampaigns(orm, order, 5000)
}
