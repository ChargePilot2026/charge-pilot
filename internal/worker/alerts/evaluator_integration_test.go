package alerts

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func openAlertsDB(t *testing.T, key string) *gorm.DB {
	t.Helper()
	db, err := dbconn.Open(context.Background(), os.Getenv(key))
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

// 遥测存在 value_num 这一列下。
// 如果 sample 结构体没映射到这一列，
// 引擎读到的就是零，任何阈值都不会触发。
func TestTelemetrySampleReadsValueNum(t *testing.T) {
	if os.Getenv("TEST_GATEWAY_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	gatewayDB := openAlertsDB(t, "TEST_GATEWAY_DATABASE_URL")
	device := "TEST-TEMP-" + uuid.NewString()[:8]
	if err := gatewayDB.Exec("INSERT INTO telemetry(device_id,port_no,metric,value_num,ts) VALUES(?,1,'temperature_c',97.25,?)", device, time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gatewayDB.Exec("DELETE FROM telemetry WHERE device_id = ?", device) })

	e := Evaluator{GatewayDB: gatewayDB}
	samples, err := e.samples(ctx, map[string]bool{"temperature_c": true}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range samples {
		if s.DeviceID != device {
			continue
		}
		found = true
		if s.Value.String() != "97.25" {
			t.Fatalf("sample value = %s, want 97.25 (value_num was not mapped)", s.Value)
		}
	}
	if !found {
		t.Fatal("inserted telemetry was not returned")
	}
}

// 设备匹配上的规则必须恰好产生一条告警；
// 重跑时必须复用同一行 outbox，
// 而不是撞上唯一事件 id。
func TestAlertRaisesOnceAndOutboxIsIdempotent(t *testing.T) {
	if os.Getenv("TEST_GATEWAY_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	gatewayDB := openAlertsDB(t, "TEST_GATEWAY_DATABASE_URL")
	adminDB := openAlertsDB(t, "TEST_ADMIN_DATABASE_URL")
	device := "TEST-TEMP-" + uuid.NewString()[:8]
	ruleName := "test-rule-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		gatewayDB.Exec("DELETE FROM telemetry WHERE device_id = ?", device)
		adminDB.Exec("DELETE FROM event_outbox WHERE envelope_json LIKE ?", "%"+device+"%")
		adminDB.Exec("DELETE FROM alert_event WHERE device_id = ?", device)
		adminDB.Exec("DELETE FROM alert_rule WHERE name = ?", ruleName)
	})

	if err := adminDB.Exec("INSERT INTO alert_rule(name,device_id_pattern,metric,op,threshold,window_seconds,severity,enabled) VALUES(?,?,'temperature_c','>',CAST(80 AS JSON),60,'critical',1)",
		ruleName, device).Error; err != nil {
		t.Fatal(err)
	}
	if err := gatewayDB.Exec("INSERT INTO telemetry(device_id,port_no,metric,value_num,ts) VALUES(?,1,'temperature_c',95.5,?)", device, time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}

	// 订阅才是把已产生的告警变成待发通知的那一环；
	// 没有订阅，告警照样入库，但什么都不会发出去。
	var ruleID uint64
	adminDB.Table("alert_rule").Where("name = ?", ruleName).Pluck("id", &ruleID)
	if err := adminDB.Exec("INSERT INTO webhook_subscription(name,url,event_types,secret,enabled) VALUES(?,?,'[\"alert\"]',?,1)",
		"test-hook-"+device, "https://hooks.example.com/"+device, uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	var subID uint64
	adminDB.Table("webhook_subscription").Where("name = ?", "test-hook-"+device).Pluck("id", &subID)
	t.Cleanup(func() {
		adminDB.Exec("DELETE FROM alert_subscription WHERE webhook_subscription_id = ?", subID)
		adminDB.Exec("DELETE FROM webhook_subscription WHERE id = ?", subID)
	})
	if err := adminDB.Exec("INSERT INTO alert_subscription(rule_id,severity,webhook_subscription_id,enabled) VALUES(?,'critical',?,1)", ruleID, subID).Error; err != nil {
		t.Fatal(err)
	}

	e := Evaluator{GatewayDB: gatewayDB, AdminDB: adminDB}
	raised, err := e.Evaluate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 评估器对每条匹配的规则各发一条告警是正确的；
	// 而这个共享库里本来就有一条通配的温度规则
	// 会被同一条读数触发。
	// 所以统计整轮结果等于在断言数据库里恰好还有什么别的数据；
	// 因此计数被限定在本测试自己创建的那条规则上。
	if raised < 1 {
		t.Fatalf("raised = %d, want this rule's breach to be among them", raised)
	}
	var events int64
	adminDB.Table("alert_event").Where("device_id = ? AND rule_id = ?", device, ruleID).Count(&events)
	if events != 1 {
		t.Fatalf("alert events for this rule = %d, want 1", events)
	}
	var outbox int64
	adminDB.Table("event_outbox").Where("envelope_json LIKE ?", "%"+device+"%").Count(&outbox)
	if outbox != 1 {
		t.Fatalf("outbox rows = %d, want 1", outbox)
	}
	// 重新评估既不能复制出重复告警，
	// 也不能撞上 outbox 的唯一键。
	if _, err := e.Evaluate(ctx); err != nil {
		t.Fatalf("re-evaluation failed on replay: %v", err)
	}
	adminDB.Table("alert_event").Where("device_id = ? AND rule_id = ?", device, ruleID).Count(&events)
	if events != 1 {
		t.Fatalf("alert events after replay = %d, want 1", events)
	}
	adminDB.Table("event_outbox").Where("envelope_json LIKE ?", "%"+device+"%").Count(&outbox)
	if outbox != 1 {
		t.Fatalf("outbox rows after replay = %d, want 1", outbox)
	}
}

// 一旦读数回落到阈值以下，活跃告警会自行关闭，
// 这样已经自行恢复的故障不会继续呼叫运维。
func TestAlertAutoResolvesWhenReadingRecovers(t *testing.T) {
	if os.Getenv("TEST_GATEWAY_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	gatewayDB := openAlertsDB(t, "TEST_GATEWAY_DATABASE_URL")
	adminDB := openAlertsDB(t, "TEST_ADMIN_DATABASE_URL")
	device := "TEST-TEMP-" + uuid.NewString()[:8]
	ruleName := "test-recover-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		gatewayDB.Exec("DELETE FROM telemetry WHERE device_id = ?", device)
		adminDB.Exec("DELETE FROM event_outbox WHERE envelope_json LIKE ?", "%"+device+"%")
		adminDB.Exec("DELETE FROM alert_event WHERE device_id = ?", device)
		adminDB.Exec("DELETE FROM alert_rule WHERE name = ?", ruleName)
	})
	if err := adminDB.Exec("INSERT INTO alert_rule(name,device_id_pattern,metric,op,threshold,window_seconds,severity,enabled) VALUES(?,?,'temperature_c','>',CAST(80 AS JSON),60,'warning',1)",
		ruleName, device).Error; err != nil {
		t.Fatal(err)
	}
	var ruleID uint64
	adminDB.Table("alert_rule").Where("name = ?", ruleName).Pluck("id", &ruleID)
	e := Evaluator{GatewayDB: gatewayDB, AdminDB: adminDB}

	gatewayDB.Exec("INSERT INTO telemetry(device_id,port_no,metric,value_num,ts) VALUES(?,1,'temperature_c',95.5,?)", device, time.Now().UTC())
	// 出于和上面相同的理由限定在这条规则上：
	// 库里已有的通配规则会被同一条读数触发，
	// 统计整轮结果等于在断言数据库里其余的数据，
	// 而不是断言这里要验的行为。
	if raised, err := e.Evaluate(ctx); err != nil || raised < 1 {
		t.Fatalf("breach: raised=%d err=%v", raised, err)
	}
	// 把这条读数换成一个正常值，再评估一次。
	gatewayDB.Exec("DELETE FROM telemetry WHERE device_id = ?", device)
	gatewayDB.Exec("INSERT INTO telemetry(device_id,port_no,metric,value_num,ts) VALUES(?,1,'temperature_c',40.0,?)", device, time.Now().UTC())
	if _, err := e.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	adminDB.Table("alert_event").Select("status").Where("device_id = ? AND rule_id = ?", device, ruleID).Take(&status)
	if status != "auto_resolved" {
		t.Fatalf("alert status = %q, want auto_resolved", status)
	}
}
