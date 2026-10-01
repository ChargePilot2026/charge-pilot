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

// 按设备记录能力，全部意义就在于：板子跑不了的费率在发布之前就被拒掉。
// 下面六种情况是这条规则必须做对的，另有一种用来证明默认值确实是拒绝。

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

// 谁都没分类过的设备，在引擎看来就等于什么都不测。新协议不承诺它没有
// 携带的字段，所以因为「大部分板子都支持」就猜某块板子支持 kWh，等于把
// 这一列本来要防的那类错误又请回来。
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

// 每块被拒的板子都要点名。只说「有些设备不支持」的拒绝，会让运营去猜是哪几块；
// 而没被点名的那些，就会继续按旧费率充电，日志里什么都不留。

// 站点在硬件到货之前就先定好费率是常事，而后来才出现的那块板子
// 从没经过发布时那次能力校验。这就是拦住它的那道检查。
func TestCheckImportAgainstStationRefusesABoardThatCannotBeMetered(t *testing.T) {
	db := dbWithStationMode(t, pricing.ModeServerEnergy)
	station := stationOf(t, db)
	devices := []ImportDevice{
		{DeviceID: "DC589OK001", StationID: station, ProtocolAdapter: "dc589"},
		{DeviceID: "DC589BAD01", StationID: station},
	}
	// 整批一起拒，不是拒一半：一支只有部分设备能计价的机队，等于要运营手工去对账。
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

// 还没定费率的站点接受任何板子；按时长计费的也一样，它压根不需要电表。
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

// dbWithStationMode 起一个一次性数据库，里面放一个站点，其默认规则正按给定
// 计费方式运行。计费方式为空表示这个站点根本没有生效的默认规则。
//
// 它读的是集成测试套件其余部分用的同一个连接串，没有配置就跳过，
// 这样上面的单测在没有数据库时照样能跑。
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

// lastCapabilityStation 记下辅助函数分配到的 ID，测试据此定位这次检查实际会看的那个站点。
var lastCapabilityStation atomic.Uint64

func stationOf(t *testing.T, _ *gorm.DB) uint64 {
	t.Helper()
	return lastCapabilityStation.Load()
}
