package charge

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func activityDB(t *testing.T) *gorm.DB {
	t.Helper()
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
	return orm
}

// lastID 读取当前连接最近一次 INSERT 生成的主键。
func lastID(t *testing.T, orm *gorm.DB) uint64 {
	t.Helper()
	var id uint64
	if err := orm.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

type activityFixture struct {
	userID    uint64
	couponID  uint64
	ruleID    uint64
	eventKey  string
	cleanup   func()
	grantRows func(t *testing.T) int64
}

func newActivityFixture(t *testing.T, orm *gorm.DB, ruleTrigger string, perUserLimit, maxGrants int) activityFixture {
	t.Helper()
	now := time.Now().UTC()
	if err := orm.Exec("INSERT INTO user (openid) VALUES (?)", "act-"+uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	userID := lastID(t, orm)
	if err := orm.Exec("INSERT INTO wallet_account (user_id, balance_cents, frozen_cents, status, version) VALUES (?, 0, 0, 'active', 0)", userID).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec(`INSERT INTO coupon
		(name, discount_type, discount_value_cents, valid_hours, total_quota, per_user_quota, status, start_at, end_at)
		VALUES (?, 'amount', 500, 72, 0, 0, 'active', ?, ?)`, "活动券", now.Add(-time.Hour), now.Add(365*24*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	couponID := lastID(t, orm)
	if err := orm.Exec(`INSERT INTO coupon_activity_rule
		(name, trigger_type, coupon_id, threshold_cents, max_grants, per_user_limit, status, start_at, end_at)
		VALUES (?, ?, ?, 0, ?, ?, 'active', ?, ?)`,
		"验收规则", ruleTrigger, couponID, maxGrants, perUserLimit, now.Add(-time.Hour), now.Add(24*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	ruleID := lastID(t, orm)
	eventKey := uuid.NewString()
	return activityFixture{
		userID: userID, couponID: couponID, ruleID: ruleID, eventKey: eventKey,
		cleanup: func() {
			_ = orm.Exec("DELETE FROM coupon_grant_request WHERE user_id = ?", userID)
			_ = orm.Exec("DELETE FROM coupon_grant WHERE coupon_id = ?", couponID)
			_ = orm.Exec("DELETE FROM coupon_activity_rule WHERE id = ?", ruleID)
			_ = orm.Exec("DELETE FROM coupon WHERE id = ?", couponID)
			_ = orm.Exec("DELETE FROM wallet_account WHERE user_id = ?", userID)
			_ = orm.Exec("DELETE FROM user WHERE id = ?", userID)
		},
		grantRows: func(t *testing.T) int64 {
			t.Helper()
			var n int64
			if err := orm.Raw("SELECT COUNT(*) FROM coupon_grant WHERE coupon_id = ? AND user_id = ?", couponID, userID).Scan(&n).Error; err != nil {
				t.Fatal(err)
			}
			return n
		},
	}
}

func runActivity(t *testing.T, orm *gorm.DB, event activityEvent) []activityResult {
	t.Helper()
	var results []activityResult
	err := orm.Transaction(func(tx *gorm.DB) error {
		var err error
		results, err = ApplyActivityRules(tx, event)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return results
}

func TestFirstRechargeGrantsOnce(t *testing.T) {
	orm := activityDB(t)
	fx := newActivityFixture(t, orm, "first_recharge", 1, 0)
	defer fx.cleanup()
	now := time.Now().UTC()
	event := activityEvent{TriggerType: "first_recharge", UserID: fx.userID, EventKey: fx.eventKey, AmountCents: 5000, Now: now}

	if got := runActivity(t, orm, event); len(got) != 1 {
		t.Fatalf("expected one grant, got %d", len(got))
	}
	if n := fx.grantRows(t); n != 1 {
		t.Fatalf("grant rows = %d, want 1", n)
	}
	// 重放同一个触发不能再次发券。
	runActivity(t, orm, event)
	if n := fx.grantRows(t); n != 1 {
		t.Fatalf("grant rows after replay = %d, want 1", n)
	}
	// 同一客户的另一笔充值仍受 per_user_limit 限制。
	second := event
	second.EventKey = uuid.NewString()
	runActivity(t, orm, second)
	if n := fx.grantRows(t); n != 1 {
		t.Fatalf("grant rows after a second recharge = %d, want 1 (per-user cap)", n)
	}
}

func TestThresholdRuleOnlyFiresAboveThreshold(t *testing.T) {
	orm := activityDB(t)
	fx := newActivityFixture(t, orm, "threshold_redeem", 5, 0)
	defer fx.cleanup()
	now := time.Now().UTC()
	if err := orm.Exec("UPDATE coupon_activity_rule SET threshold_cents = 3000 WHERE id = ?", fx.ruleID).Error; err != nil {
		t.Fatal(err)
	}
	// 没到门槛不发放。
	if got := runActivity(t, orm, activityEvent{TriggerType: "threshold_redeem", UserID: fx.userID, EventKey: fx.eventKey, AmountCents: 2999, Now: now}); len(got) != 0 {
		t.Fatalf("a 2999-cent order triggered a 3000-cent rule: %+v", got)
	}
	if n := fx.grantRows(t); n != 0 {
		t.Fatalf("grant rows below threshold = %d, want 0", n)
	}
	if got := runActivity(t, orm, activityEvent{TriggerType: "threshold_redeem", UserID: fx.userID, EventKey: uuid.NewString(), AmountCents: 3000, Now: now}); len(got) != 1 {
		t.Fatalf("a qualifying order granted %d coupons, want 1", len(got))
	}
}

func TestCampaignBudgetStopsGrants(t *testing.T) {
	orm := activityDB(t)
	fx := newActivityFixture(t, orm, "first_recharge", 1, 1)
	defer fx.cleanup()
	now := time.Now().UTC()
	runActivity(t, orm, activityEvent{TriggerType: "first_recharge", UserID: fx.userID, EventKey: fx.eventKey, Now: now})

	// 第二个客户不能花掉已经用完的预算。
	if err := orm.Exec("INSERT INTO user (openid) VALUES (?)", "act-"+uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	otherID := lastID(t, orm)
	defer func() { _ = orm.Exec("DELETE FROM user WHERE id = ?", otherID) }()
	if got := runActivity(t, orm, activityEvent{TriggerType: "first_recharge", UserID: otherID, EventKey: uuid.NewString(), Now: now}); len(got) != 0 {
		t.Fatalf("a spent campaign still granted: %+v", got)
	}
}

func TestExpiredAndDisabledRulesDoNotGrant(t *testing.T) {
	orm := activityDB(t)
	fx := newActivityFixture(t, orm, "first_recharge", 1, 0)
	defer fx.cleanup()
	now := time.Now().UTC()
	if err := orm.Exec("UPDATE coupon_activity_rule SET end_at = ? WHERE id = ?", now.Add(-time.Minute), fx.ruleID).Error; err != nil {
		t.Fatal(err)
	}
	if got := runActivity(t, orm, activityEvent{TriggerType: "first_recharge", UserID: fx.userID, EventKey: fx.eventKey, Now: now}); len(got) != 0 {
		t.Fatalf("an expired rule still granted: %+v", got)
	}
	if err := orm.Exec("UPDATE coupon_activity_rule SET end_at = ?, status = 'disabled' WHERE id = ?", now.Add(24*time.Hour), fx.ruleID).Error; err != nil {
		t.Fatal(err)
	}
	if got := runActivity(t, orm, activityEvent{TriggerType: "first_recharge", UserID: fx.userID, EventKey: fx.eventKey, Now: now}); len(got) != 0 {
		t.Fatalf("a disabled rule still granted: %+v", got)
	}
}

// 验证奖励券已删除时不生成发放记录、不消耗库存或领取次数，也不占用事件幂等键。
func TestRulePointingAtDeletedCouponGrantsNothing(t *testing.T) {
	orm := activityDB(t)
	fx := newActivityFixture(t, orm, "first_recharge", 1, 0)
	defer fx.cleanup()
	now := time.Now().UTC()

	// 把规则指向一张不存在的券：券没了，规则还在。
	if err := orm.Exec("UPDATE coupon_activity_rule SET coupon_id = 99999999 WHERE id = ?", fx.ruleID).Error; err != nil {
		t.Fatal(err)
	}
	event := activityEvent{TriggerType: "first_recharge", UserID: fx.userID, EventKey: fx.eventKey, AmountCents: 5000, Now: now}
	if got := runActivity(t, orm, event); len(got) != 0 {
		t.Fatalf("a rule on a deleted coupon still granted: %+v", got)
	}
	var grants int64
	if err := orm.Table("coupon_grant").Where("user_id = ?", fx.userID).Count(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if grants != 0 {
		t.Fatalf("coupon_grant rows = %d, want 0", grants)
	}
	var requests int64
	if err := orm.Table("coupon_grant_request").Where("user_id = ?", fx.userID).Count(&requests).Error; err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("coupon_grant_request rows = %d, want 0", requests)
	}
}

// TestInviteRewardRequiresEstablishedInviter 邀请奖励不能发给一圈一次性账号：
// 邀请人必须真的用过平台，而且谁也不能邀请自己。
func TestInviteRewardRequiresEstablishedInviter(t *testing.T) {
	orm := activityDB(t)
	fx := newActivityFixture(t, orm, "invite_reward", 5, 0)
	defer fx.cleanup()
	now := time.Now().UTC()

	// 自邀请被拒绝。
	if got := runActivity(t, orm, activityEvent{TriggerType: "invite_reward", UserID: fx.userID, EventKey: fx.eventKey, InviterID: fx.userID, Now: now}); len(got) != 0 {
		t.Fatalf("self-invite granted: %+v", got)
	}
	// 完全没有历史记录的新邀请人被拒绝。
	if err := orm.Exec("INSERT INTO user (openid) VALUES (?)", "act-"+uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	otherID := lastID(t, orm)
	defer func() { _ = orm.Exec("DELETE FROM user WHERE id = ?", otherID) }()
	if got := runActivity(t, orm, activityEvent{TriggerType: "invite_reward", UserID: fx.userID, EventKey: fx.eventKey, InviterID: otherID, Now: now}); len(got) != 0 {
		t.Fatalf("an unused inviter was rewarded: %+v", got)
	}
	// 邀请人须已有一笔结算完成的充值。
	if err := orm.Exec(`INSERT INTO payment_order
		(order_no, biz_type, biz_id, user_id, pay_method, total_cents, paid_cents, status, created_month)
		VALUES (?, 'wallet_recharge', 0, ?, 'wechat', 1000, 1000, 'paid', ?)`, "AC"+uuid.NewString()[:8], otherID, utcDate()).Error; err != nil {
		t.Fatal(err)
	}
	defer func() { _ = orm.Exec("DELETE FROM payment_order WHERE user_id = ?", otherID) }()
	if got := runActivity(t, orm, activityEvent{TriggerType: "invite_reward", UserID: fx.userID, EventKey: uuid.NewString(), InviterID: otherID, Now: now}); len(got) != 1 {
		t.Fatalf("an established inviter was not rewarded: %+v", got)
	}
}
