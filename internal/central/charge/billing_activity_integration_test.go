package charge

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

// TestApplyOrderCampaignsGrantsThresholdOnce —— 订单钩子跑在结算事务内部。
// 它必须为每个够条件的订单恰好发一张券，
// 而且坏掉的规则不能妨碍这笔充电开出账单。
func TestApplyOrderCampaignsGrantsThresholdOnce(t *testing.T) {
	orm := activityDB(t)
	fx := newActivityFixture(t, orm, "threshold_redeem", 5, 0)
	defer fx.cleanup()
	now := time.Now().UTC()
	if err := orm.Exec("UPDATE coupon_activity_rule SET threshold_cents = 3000 WHERE id = ?", fx.ruleID).Error; err != nil {
		t.Fatal(err)
	}
	order := ChargeOrderRecord{OrderNo: "AC" + uuid.NewString()[:10], UserID: fx.userID}

	// 没到门槛不发放。
	applyOrderCampaigns(orm, order, 2999)
	if n := fx.grantRows(t); n != 0 {
		t.Fatalf("a 2999-cent order granted %d coupons", n)
	}
	// 够条件的订单发一次。
	applyOrderCampaigns(orm, order, 3500)
	if n := fx.grantRows(t); n != 1 {
		t.Fatalf("a 3500-cent order granted %d coupons, want 1", n)
	}
	// 对同一笔订单重新求值不能发出第二张券。
	applyOrderCampaigns(orm, order, 3500)
	if n := fx.grantRows(t); n != 1 {
		t.Fatalf("re-evaluation granted %d coupons, want 1", n)
	}
	_ = now
}

// TestApplyOrderCampaignsHolidayRespectsPerUserLimit —— 节日活动没有金额条件：
// 窗口期内的每一笔已结算订单都会触发，但单人限领依然生效。
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

// TestApplyOrderCampaignsSurvivesBrokenRule —— 指向已删券的规则不能中止结算。
// 这笔充电无论如何都得开出账单。
func TestApplyOrderCampaignsSurvivesBrokenRule(t *testing.T) {
	url := os.Getenv("TEST_USER_DATABASE_URL")
	if url == "" {
		t.Skip("set a disposable user database URL")
	}
	db, err := dbconn.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
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

	now := time.Now().UTC()
	if err := orm.Exec(`INSERT INTO coupon_activity_rule
		(name, trigger_type, coupon_id, threshold_cents, max_grants, per_user_limit, status, start_at, end_at)
		VALUES ('坏规则', 'threshold_redeem', 99999999, 100, 0, 1, 'active', ?, ?)`,
		now.Add(-time.Hour), now.Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	badRuleID := lastID(t, orm)
	// 发放记录挂在 user_id 上、又没有外键级联：只删用户的话发放记录会留下来，
	// 而它的 source_event_id 是按规则主键算出来的固定值，于是第二轮就撞上
	// uk_coupon_grant_source_event，失败原因指向一个完全无关的唯一键。
	t.Cleanup(func() {
		_ = orm.Exec("DELETE FROM coupon_grant_request WHERE user_id = ?", userID)
		_ = orm.Exec("DELETE FROM coupon_grant WHERE user_id = ?", userID)
		_ = orm.Exec("DELETE FROM coupon_activity_rule WHERE id = ?", badRuleID)
		_ = orm.Exec("DELETE FROM user WHERE id = ?", userID)
	})

	order := ChargeOrderRecord{OrderNo: "AC" + uuid.NewString()[:10], UserID: userID}
	// 不能 panic，也不能把错误抛给调用方。
	applyOrderCampaigns(orm, order, 5000)
}
