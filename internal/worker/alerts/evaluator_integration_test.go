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

// Telemetry is stored under value_num. If the sample struct is not mapped to
// that column the engine reads zero and no threshold can ever fire.
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

// A rule with a matching device must raise exactly one alert, and a re-run must
// reuse the same outbox row instead of colliding on the unique event id.
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

	// A subscription is what turns a raised alert into a queued notification;
	// without one the alert is still recorded but nothing is published.
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
	// The evaluator is correct to raise one alert per matching rule, and a shared
	// database already carries a wildcard temperature rule that this reading also
	// trips. Counting the whole run would therefore be asserting on whatever else
	// the database happens to hold, so the count is scoped to the rule this test
	// created.
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
	// Re-evaluating must not duplicate the alert or trip the outbox unique key.
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

// An active alert closes itself once the reading returns below the threshold,
// so operators are not paged for a fault that has already cleared.
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
	// Scoped to this rule for the same reason as above: a wildcard rule already in
	// the database trips on the same reading, and counting the whole run would be
	// asserting on the rest of the database rather than on this behaviour.
	if raised, err := e.Evaluate(ctx); err != nil || raised < 1 {
		t.Fatalf("breach: raised=%d err=%v", raised, err)
	}
	// Replace the reading with a healthy one and evaluate again.
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
