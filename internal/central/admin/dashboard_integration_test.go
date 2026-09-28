package admin

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestDashboardUsesBeijingDatesAndOnlySettledFees(t *testing.T) {
	userURL, adminURL := os.Getenv("TEST_USER_DATABASE_URL"), os.Getenv("TEST_ADMIN_DATABASE_URL")
	if userURL == "" || adminURL == "" {
		t.Skip("set disposable user/admin database URLs")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, userURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adminDB, err := dbconn.Open(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	userORM, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	adminORM, err := dbconn.WrapGORM(adminDB)
	if err != nil {
		t.Fatal(err)
	}
	dashboard := Dashboard{UserDB: userORM, AdminDB: adminORM}
	// 16:00 UTC is midnight Beijing; a yesterday row must not count as today.
	now := time.Date(2030, 1, 2, 16, 30, 0, 0, time.UTC)
	before, err := dashboard.Read(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "dashboard-" + uuid.NewString()
	defer db.Exec("DELETE FROM charge_order WHERE device_id = ?", prefix)
	for _, row := range []struct {
		suffix, status string
		at             time.Time
		cents          any
	}{
		{"yesterday", "completed", now.Add(-time.Hour), int64(90)},
		{"today", "completed", now, int64(110)},
		{"unbilled", "completed", now, nil},
		{"charging", "charging", now, nil},
	} {
		var ended any = row.at
		if row.status == "charging" {
			ended = nil
		}
		if _, err := db.Exec(`INSERT INTO charge_order (order_no,user_id,device_id,port_no,status,ended_at,total_cents,created_month,created_at) VALUES (?,99999999,?,1,?,?,?,?,?)`, prefix+row.suffix, prefix, row.status, ended, row.cents, "2030-01-01", row.at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := dashboard.Read(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChargingOrders != before.ChargingOrders+1 || got.TodayCompletedOrders != before.TodayCompletedOrders+1 || got.TodaySettledCents != before.TodaySettledCents+110 {
		t.Fatal(got, before)
	}
	if got.DailyTrend[5].SettledCents != before.DailyTrend[5].SettledCents+90 || got.DailyTrend[6].Day != "2030-01-03" {
		t.Fatal(got.DailyTrend)
	}
}
