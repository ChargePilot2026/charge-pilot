package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

func aggregateDB(t *testing.T) *gorm.DB {
	t.Helper()
	url := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if url == "" {
		t.Skip("set a disposable gateway database URL")
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

func sqlPort(p int16) sql.NullInt16 { return sql.NullInt16{Int16: p, Valid: true} }

// 汇总表必须把重复样本折叠进同一个桶，并给出正确的运行均值与极值。
// 重跑同一批数据才是有意思的那个用例：
// 一帧被重放的设备数据否则会让计数翻倍。
func TestRefreshAggregatesFoldsSamplesIntoBuckets(t *testing.T) {
	orm := aggregateDB(t)
	ctx := context.Background()
	deviceID := "agg_test_device"
	cleanup := func() {
		_ = orm.Exec("DELETE FROM telemetry_aggregate_15min WHERE device_id = ?", deviceID)
		_ = orm.Exec("DELETE FROM telemetry_aggregate_hourly WHERE device_id = ?", deviceID)
		_ = orm.Exec("DELETE FROM telemetry WHERE device_id = ?", deviceID)
	}
	cleanup()
	defer cleanup()

	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	port := int16(1)
	samples := []AggregateSample{
		{DeviceID: deviceID, Port: sqlPort(port), Metric: "power_w", Value: "100", TS: base},
		{DeviceID: deviceID, Port: sqlPort(port), Metric: "power_w", Value: "200", TS: base.Add(5 * time.Minute)},
		{DeviceID: deviceID, Port: sqlPort(port), Metric: "power_w", Value: "300", TS: base.Add(10 * time.Minute)},
	}
	// 再加一个不带端口的设备级指标，
	// 它必须落在 NULL 端口下，而不是端口 0。
	samples = append(samples,
		AggregateSample{DeviceID: deviceID, Metric: "signal", Value: "80", TS: base},
		AggregateSample{DeviceID: deviceID, Metric: "signal", Value: "60", TS: base.Add(5 * time.Minute)},
	)

	if err := orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, s := range samples {
			if err := tx.Table("telemetry").Create(map[string]any{
				"device_id": s.DeviceID, "port_no": s.Port, "metric": s.Metric,
				"value_num": s.Value, "ts": s.TS,
			}).Error; err != nil {
				return err
			}
		}
		return RefreshAggregates(ctx, tx, samples)
	}); err != nil {
		t.Fatal(err)
	}

	// 列上的 tag 是必需的：这些别名（avg/mn/mx）与 Go 字段名并不对应，
	// 没有它们的话每个值都会扫成空。
	type bucket struct {
		Avg   string `gorm:"column:avg"`
		Min   string `gorm:"column:mn"`
		Max   string `gorm:"column:mx"`
		Count int    `gorm:"column:count"`
	}
	var power bucket
	if err := orm.Raw(`SELECT CAST(avg_value AS CHAR) avg, CAST(min_value AS CHAR) mn,
		CAST(max_value AS CHAR) mx, count FROM telemetry_aggregate_15min
		WHERE device_id = ? AND metric = 'power_w' AND port_no = 1`, deviceID).
		Scan(&power).Error; err != nil {
		t.Fatal(err)
	}
	if power.Count != 3 {
		t.Fatalf("15min power_w count = %d, want 3", power.Count)
	}
	if power.Min != "100.000000" || power.Max != "300.000000" {
		t.Fatalf("extremes = %s..%s, want 100..300", power.Min, power.Max)
	}
	if power.Avg != "200.000000" {
		t.Fatalf("mean = %s, want 200", power.Avg)
	}

	// 设备级指标必须以 NULL 端口存下来。
	var signalPort *int16
	if err := orm.Raw("SELECT port_no FROM telemetry_aggregate_15min WHERE device_id = ? AND metric = 'signal'", deviceID).
		Scan(&signalPort).Error; err != nil {
		t.Fatal(err)
	}
	if signalPort != nil {
		t.Fatalf("device-level metric stored with port %d, want NULL", *signalPort)
	}
	for _, table := range []string{"telemetry_aggregate_15min", "telemetry_aggregate_hourly"} {
		var signal bucket
		var rows int64
		query := orm.Table(table).Where("device_id=? AND metric='signal' AND port_no IS NULL", deviceID)
		if err := query.Count(&rows).Error; err != nil || rows != 1 {
			t.Fatalf("%s device-level bucket rows=%d err=%v", table, rows, err)
		}
		if err := query.Select("CAST(avg_value AS CHAR) avg, CAST(min_value AS CHAR) mn, CAST(max_value AS CHAR) mx, count").Take(&signal).Error; err != nil {
			t.Fatal(err)
		}
		if signal.Count != 2 || signal.Avg != "70.000000" || signal.Min != "60.000000" || signal.Max != "80.000000" {
			t.Fatalf("%s device-level aggregate=%+v", table, signal)
		}
	}

	// 小时汇总覆盖的是同样那三个样本。
	var hourly bucket
	if err := orm.Raw(`SELECT CAST(avg_value AS CHAR) avg, CAST(min_value AS CHAR) mn,
		CAST(max_value AS CHAR) mx, count FROM telemetry_aggregate_hourly
		WHERE device_id = ? AND metric = 'power_w' AND port_no = 1`, deviceID).
		Scan(&hourly).Error; err != nil {
		t.Fatal(err)
	}
	if hourly.Count != 3 || hourly.Avg != "200.000000" {
		t.Fatalf("hourly bucket = %+v, want count 3 avg 200", hourly)
	}

	// 再折叠一次同一批数据，计数必须翻倍，
	// 这正是需要运行均值而不是简单覆盖的原因。
	if err := RefreshAggregates(ctx, orm, samples[:1]); err != nil {
		t.Fatal(err)
	}
	var again bucket
	if err := orm.Raw(`SELECT CAST(avg_value AS CHAR) avg, CAST(min_value AS CHAR) mn, CAST(max_value AS CHAR) mx, count FROM telemetry_aggregate_15min
		WHERE device_id = ? AND metric = 'power_w' AND port_no = 1`, deviceID).Scan(&again).Error; err != nil {
		t.Fatal(err)
	}
	if again.Count != 4 {
		t.Fatalf("count after a repeated sample = %d, want 4", again.Count)
	}
	// 运行均值应为 (200*3 + 100*1) / 4
	if again.Avg != "175.000000" {
		t.Fatalf("running mean = %s, want 175", again.Avg)
	}
}
