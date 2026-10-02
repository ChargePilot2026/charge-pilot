package settlement

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 本文件与 internal/central/coupon 的集成测试各自持有一份活动夹具：
// coupon 测试在 external test 包中无法复用 charge 的内部测试符号，
// 两边均只经原始 SQL 建数据，保持独立可维护。

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
