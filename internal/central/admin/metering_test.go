package admin

import (
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

// 发布前按设备能力校验计费模式；未声明能力时默认拒绝。

func meterless() deviceCapability {
	return deviceCapability{DeviceID: "DC589TEST001"}
}

func TestCapabilityAllowsOnlyWhatTheBoardCanReport(t *testing.T) {
	for _, mode := range []pricing.ChargeMode{pricing.ModeDeviceDuration, pricing.ModeDeviceEnergy, pricing.ModeDevicePower, pricing.ModeServerEnergy, pricing.ModeServerMaxPower, pricing.ModeServerRealtimePower} {
		if reason := capabilityBlock(mode, deviceCapability{ProtocolAdapter: "dc589"}); reason != "" {
			t.Fatalf("%s: %s", mode, reason)
		}
		if reason := capabilityBlock(mode, deviceCapability{ProtocolAdapter: "unknown", ReportsEnergy: true, ReportsSegmentedPower: true}); reason == "" {
			t.Fatal("manual flags granted unknown protocol abilities")
		}
	}
}

// 未知协议不推断计量能力；仅接受协议明确支持的电量或功率字段。
func TestUnclassifiedDeviceIsRefusedEveryMeteredMode(t *testing.T) {
	cap := deviceCapability{DeviceID: "DC589NEW001", ChargeMode: string(pricing.ModeDeviceDuration)}
	for _, mode := range []pricing.ChargeMode{
		pricing.ModeServerRealtimePower, pricing.ModeServerMaxPower, pricing.ModeServerEnergy,
		pricing.ModeDeviceEnergy, pricing.ModeDevicePower,
	} {
		if reason := capabilityBlock(mode, cap); reason == "" {
			t.Fatalf("%s was allowed on an unclassified device", mode)
		}
	}
}

// 错误结果应逐一列出不支持当前计费模式的设备。

// 新增设备也必须通过站点当前费率的能力校验。
func TestCheckImportAgainstStationRefusesABoardThatCannotBeMetered(t *testing.T) {
	db := dbWithStationMode(t, pricing.ModeServerEnergy)
	station := stationOf(t, db)
	devices := []ImportDevice{
		{DeviceID: "DC589OK001", StationID: station, ProtocolAdapter: "dc589"},
		{DeviceID: "DC589BAD01", StationID: station},
	}
	// 验证整批设备兼容性校验：任一设备不兼容时拒绝整批请求。
	if err := checkImportAgainstStation(db, devices); err == nil {
		t.Fatal("a batch containing an unmeasurable board was accepted")
	} else {
		var blocked *errMeteringBlocked
		if !errors.As(err, &blocked) {
			t.Fatalf("err = %v, want the unmeasurable board refused by name", err)
		}
		if blocked.device != "DC589BAD01" || blocked.station != station {
			t.Fatalf("refusal names %+v, want DC589BAD01 at station %d", blocked, station)
		}
	}
	if err := checkImportAgainstStation(db,
		[]ImportDevice{{DeviceID: "DC589OK001", StationID: station, ProtocolAdapter: "dc589"}}); err != nil {
		t.Fatalf("a fully measurable batch was refused: %v", err)
	}
}

// 未配置费率或仅按时长计费的站点不要求设备具备电表能力。
func TestCheckImportAgainstStationAllowsWhenNothingNeedsAMeter(t *testing.T) {
	unpriced := dbWithStationMode(t, "")
	station := stationOf(t, unpriced)
	if err := checkImportAgainstStation(unpriced,
		[]ImportDevice{{DeviceID: "DC589NEW001", StationID: station}}); err != nil {
		t.Fatalf("a board was refused from a station with no tariff: %v", err)
	}
	timed := dbWithStationMode(t, pricing.ModeDeviceDuration)
	station = stationOf(t, timed)
	if err := checkImportAgainstStation(timed,
		[]ImportDevice{{DeviceID: "DC589NEW002", StationID: station, ProtocolAdapter: "dc589"}}); err != nil {
		t.Fatalf("a board was refused from a duration-priced station: %v", err)
	}
}

// dbWithStationMode 创建指定默认计费模式的站点夹具，空模式表示无默认规则。
// 未配置集成测试连接串时跳过。
func dbWithStationMode(t *testing.T, mode pricing.ChargeMode) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_ADMIN_DATABASE_URL")
	if raw == "" {
		t.Skip("set disposable MySQL to run the station capability check")
	}
	db, err := dbconn.Open(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	// 换一个空闲的站点 ID，同一轮里的第二个用例才不会在主键上和第一个撞车。
	var next uint64
	if err := orm.Raw("SELECT COALESCE(MAX(id),0)+9000 FROM station").Scan(&next).Error; err != nil {
		t.Fatal(err)
	}
	if err := orm.Exec("INSERT INTO station(id,name,status,longitude,latitude) VALUES(?,?,'active',116.4,39.9)", next,
		"计量能力站点").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		orm.Exec("DELETE FROM pricing_rule WHERE station_id=?", next)
		orm.Exec("DELETE FROM station WHERE id=?", next)
	})
	lastCapabilityStation.Store(next)
	if mode != "" {
		spec := pricing.Spec{Mode: mode}
		if mode.ServerBilled() {
			spec.Electric = &pricing.ElectricLine{Basis: mode.BasisFor(),
				Periods: []pricing.Period{{EndMinute: 1440, ElectricCents: 100}}}
		}
		blob, _ := json.Marshal(spec)
		if err := orm.Exec("INSERT INTO pricing_rule(template_id,station_id,device_id,name,spec_json,channel,version,status) VALUES(?,?,NULL,?,?,'default',1,'active')",
			1, next, "计量能力默认规则", string(blob)).Error; err != nil {
			t.Fatal(err)
		}
	}
	return orm
}

// lastCapabilityStation 保存辅助函数创建的站点 ID，供测试定位校验对象。
var lastCapabilityStation atomic.Uint64

func stationOf(t *testing.T, _ *gorm.DB) uint64 {
	t.Helper()
	return lastCapabilityStation.Load()
}
