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

// The whole point of recording a capability per device is that a tariff the
// board cannot run is refused before it is published. These are the six cases
// that rule has to get right, and one case that proves the default is refusal.

func meterless() deviceCapability {
	return deviceCapability{DeviceID: "DC589TEST001"}
}

func TestCapabilityAllowsOnlyWhatTheBoardCanReport(t *testing.T) {
	cases := []struct {
		name  string
		cap   deviceCapability
		mode  pricing.ChargeMode
		block bool
	}{
		{"时长无需计量", meterless(), pricing.ModeDeviceDuration, false},
		{"服务端电量需电量上报", meterless(), pricing.ModeServerEnergy, true},
		{"设备电量需电量上报", meterless(), pricing.ModeDeviceEnergy, true},
		{"服务端实时功率需分段功率", meterless(), pricing.ModeServerRealtimePower, true},
		{"服务端最大功率需分段功率", meterless(), pricing.ModeServerMaxPower, true},
		{"设备功率档位需分段功率", meterless(), pricing.ModeDevicePower, true},
		{"有电量上报则服务端电量放行",
			deviceCapability{ReportsEnergy: true}, pricing.ModeServerEnergy, false},
		{"有电量上报仍然不放行功率",
			deviceCapability{ReportsEnergy: true}, pricing.ModeServerRealtimePower, true},
		{"有分段功率则功率放行",
			deviceCapability{ReportsSegmentedPower: true}, pricing.ModeServerMaxPower, false},
		{"两者都有则两类都放行",
			deviceCapability{ReportsEnergy: true, ReportsSegmentedPower: true}, pricing.ModeDevicePower, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := capabilityBlock(tc.mode, tc.cap)
			if tc.block && reason == "" {
				t.Fatalf("%s was allowed on a board that reports nothing", tc.mode)
			}
			if !tc.block && reason != "" {
				t.Fatalf("%s was refused for %q", tc.mode, reason)
			}
		})
	}
}

// A device nobody has classified measures nothing as far as the engine is
// concerned. The new protocol does not promise any field it does not carry, so
// guessing that a board supports kWh because most of them do would reintroduce
// exactly the class of error the column exists to prevent.
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

// Every blocked board is named. A refusal that says "some devices cannot take
// this" leaves the operator guessing which, and the ones not named are the ones
// that stay on the old tariff with nothing in a log to say so.
func TestCheckMeteringNamesEveryBlockedDevice(t *testing.T) {
	targets := []switchTarget{
		{DeviceID: "A", Cap: deviceCapability{DeviceID: "A"}},
		{DeviceID: "B", Cap: deviceCapability{DeviceID: "B", ReportsEnergy: true, ReportsSegmentedPower: true}},
		{DeviceID: "C", Cap: deviceCapability{DeviceID: "C"}},
	}
	blocked := checkMetering(pricing.ModeServerEnergy, targets)
	if len(blocked) != 2 {
		t.Fatalf("blocked %d devices, want 2: %v", len(blocked), blocked)
	}
	if blocked[0][:1] != "A" || blocked[1][:1] != "C" {
		t.Fatalf("blocked = %v, want the offenders in device order", blocked)
	}
	if got := checkMetering(pricing.ModeDeviceDuration, targets); len(got) != 0 {
		t.Fatalf("a duration tariff blocked %v, want nothing", got)
	}
}

func TestCommonModeCollapsesOnlyWhenTheBoardsAgree(t *testing.T) {
	agree := []switchTarget{{DeviceID: "A", Before: "server_energy"}, {DeviceID: "B", Before: "server_energy"}}
	if got := commonMode(agree, true); got != "server_energy" {
		t.Fatalf("commonMode = %v, want the shared mode", got)
	}
	mixed := []switchTarget{{DeviceID: "A", Before: "server_energy"}, {DeviceID: "B", Before: "device_energy"}}
	if got := commonMode(mixed, true); got != "mixed" {
		t.Fatalf("commonMode = %v, want a marker rather than one board's mode", got)
	}
	// No board has ever been priced, so there is no before-figure to report.
	// A summary filled in with the mode about to be set would make a first-time
	// application look like a change from itself.
	never := []switchTarget{{DeviceID: "A", Before: modeNeverSet}}
	if got := commonMode(never, true); got != modeNeverSet {
		t.Fatalf("commonMode = %v, want %q", got, modeNeverSet)
	}
}

// A station priced before its hardware arrived is normal, and the board that turns
// up later never went through the capability check that ran at publish time.
// This is the check that catches it.
func TestCheckImportAgainstStationRefusesABoardThatCannotBeMetered(t *testing.T) {
	db := dbWithStationMode(t, pricing.ModeServerEnergy)
	station := stationOf(t, db)
	devices := []ImportDevice{
		{DeviceID: "DC589OK001", StationID: station, ReportsEnergy: true},
		{DeviceID: "DC589BAD01", StationID: station},
	}
	// The whole batch is refused, not half of it: a fleet that is partly
	// priceable is one an operator has to reconcile by hand.
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
		[]ImportDevice{{DeviceID: "DC589OK001", StationID: station, ReportsEnergy: true}}); err != nil {
		t.Fatalf("a fully measurable batch was refused: %v", err)
	}
}

// A station with no tariff yet accepts any board, and so does a duration
// tariff, which needs no meter at all.
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
		[]ImportDevice{{DeviceID: "DC589NEW002", StationID: station}}); err != nil {
		t.Fatalf("a board was refused from a duration-priced station: %v", err)
	}
}

// dbWithStationMode stands up a disposable database carrying one station whose
// default is charging on the given mode. An empty mode means the station has no
// active default at all.
//
// It reads the same URL the rest of the integration suite uses, and skips when
// there is none, so the unit tests above stay runnable without a database.
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
	// A free station id, so a second case in the same run does not collide with
	// the first on the primary key.
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

// lastCapabilityStation carries the id the helper allocated, so a test can
// address the station the check will actually look at.
var lastCapabilityStation atomic.Uint64

func stationOf(t *testing.T, _ *gorm.DB) uint64 {
	t.Helper()
	return lastCapabilityStation.Load()
}
