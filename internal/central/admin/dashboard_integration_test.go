package admin

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestDashboardUsesBeijingDatesAndOnlySettledFees(t *testing.T) {
	userURL, adminURL := os.Getenv("TEST_USER_DATABASE_URL"), os.Getenv("TEST_ADMIN_DATABASE_URL")
	if userURL == "" || adminURL == "" {
		t.Skip("set user/admin database URLs; fixtures use UUIDs and are cleaned precisely")
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
	// 16：00 UTC 就是北京时间的零点；昨天那一行不能算进今天。
	now := time.Date(2030, 1, 2, 16, 30, 0, 0, time.UTC)
	before, err := dashboard.Read(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "dashboard-" + uuid.NewString()
	start := time.Date(2030, 1, 2, 16, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	previous := start.Add(-time.Millisecond)
	last := end.Add(-time.Millisecond)
	insert := func(database *sql.DB, query string, args ...any) int64 {
		t.Helper()
		result, err := database.ExecContext(ctx, query, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	type ownedRow struct {
		id  int64
		key string
	}
	var users, stations, devices []ownedRow
	defer func() {
		cleanup := func(database *sql.DB, query string, args ...any) {
			t.Helper()
			if _, err := database.ExecContext(ctx, query, args...); err != nil {
				t.Errorf("fixture cleanup: %v", err)
			}
		}
		cleanup(db, "DELETE FROM charge_order WHERE device_id = ? AND created_month = ?", prefix, "2030-01-01")
		for _, row := range users {
			cleanup(db, "DELETE FROM user WHERE id = ? AND openid = ?", row.id, row.key)
		}
		for _, row := range devices {
			cleanup(adminDB, "DELETE FROM device_meta WHERE id = ? AND device_id = ?", row.id, row.key)
		}
		for _, row := range stations {
			cleanup(adminDB, "DELETE FROM station WHERE id = ? AND name = ?", row.id, row.key)
		}
	}()
	userIDs := map[string]int64{}
	for _, row := range []struct {
		suffix  string
		created time.Time
		deleted any
	}{
		{"existing", previous, nil},
		{"first", start, nil},
		{"last", last, nil},
		{"tomorrow", end, nil},
		{"idle", previous, nil},
		{"failed", previous, nil},
		{"deleted", start, now},
	} {
		openid := prefix + "-" + row.suffix
		id := insert(db, "INSERT INTO user (openid,created_at,deleted_at) VALUES (?,?,?)", openid, row.created, row.deleted)
		users = append(users, ownedRow{id, openid})
		userIDs[row.suffix] = id
	}
	for _, row := range []struct {
		suffix, status, user string
		created              time.Time
		started, ended       any
		cents, deleted       any
	}{
		{"yesterday", "completed", "existing", previous, previous, previous, int64(90), nil},
		{"first", "completed", "first", start, start, start, int64(110), nil},
		{"repeat", "charging", "first", now, now, nil, nil, nil},
		{"last", "completed", "last", last, last, last, int64(210), nil},
		{"pending", "pending_payment", "idle", now, nil, nil, nil, nil},
		{"cancelled", "cancelled", "idle", now, nil, nil, nil, nil},
		{"failed", "failed", "failed", now, nil, nil, nil, nil},
		{"started-today", "charging", "existing", previous, now, nil, nil, nil},
		{"unbilled", "completed", "last", now, now, now, nil, nil},
		{"deleted", "charging", "idle", now, now, nil, nil, now},
		{"tomorrow", "completed", "tomorrow", end, end, end, int64(900), nil},
	} {
		insert(db, `INSERT INTO charge_order
			(order_no,user_id,device_id,port_no,status,started_at,ended_at,total_cents,created_month,created_at,deleted_at)
			VALUES (?,?,?,1,?,?,?,?,?,?,?)`, prefix+"-"+row.suffix, userIDs[row.user], prefix, row.status, row.started, row.ended, row.cents, "2030-01-01", row.created, row.deleted)
	}
	for _, row := range []struct {
		suffix, stationStatus, deviceStatus string
		deleted                             any
	}{
		{"active", "active", "enabled", nil},
		{"disabled", "disabled", "disabled", nil},
		{"deleted", "active", "enabled", now},
	} {
		name := prefix + "-" + row.suffix
		stationID := insert(adminDB, "INSERT INTO station (name,longitude,latitude,status,deleted_at) VALUES (?,116.3,39.9,?,?)", name, row.stationStatus, row.deleted)
		stations = append(stations, ownedRow{stationID, name})
		deviceID := prefix + "-dev-" + row.suffix
		id := insert(adminDB, "INSERT INTO device_meta (device_id,station_id,status,deleted_at) VALUES (?,?,?,?)", deviceID, stationID, row.deviceStatus, row.deleted)
		devices = append(devices, ownedRow{id, deviceID})
	}
	got, err := dashboard.Read(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string][3]int64{
		"charging orders":        {got.ChargingOrders, before.ChargingOrders, 2},
		"today orders":           {got.TodayOrders, before.TodayOrders, 7},
		"today order users":      {got.TodayOrderUsers, before.TodayOrderUsers, 4},
		"today charging users":   {got.TodayChargingUsers, before.TodayChargingUsers, 3},
		"today completed orders": {got.TodayCompletedOrders, before.TodayCompletedOrders, 2},
		"today settled cents":    {got.TodaySettledCents, before.TodaySettledCents, 320},
		"total users":            {got.TotalUsers, before.TotalUsers, 6},
		"new users":              {got.NewUsers, before.NewUsers, 2},
		"stations":               {got.StationCount, before.StationCount, 2},
		"devices":                {got.DeviceCount, before.DeviceCount, 2},
	} {
		if values[0]-values[1] != values[2] {
			t.Errorf("%s fixture contribution = %d, want %d", name, values[0]-values[1], values[2])
		}
	}
	if got.DailyTrend[5].SettledCents != before.DailyTrend[5].SettledCents+90 || got.DailyTrend[6].Day != "2030-01-03" {
		t.Fatal(got.DailyTrend)
	}
}
