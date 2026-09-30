package pricing

import (
	"errors"
	"testing"
)

// 整站电价表就是 device_id 为空的那条规则。要它的时候过去跑的是与查设备规则
// 同一条查询，只是少了设备过滤，于是一个从未整站定价、却给某个桩单独定了价的
// 站点，会把那台设备的费率当成整站费率报出去。
func TestStationRuleIsNeverADeviceRule(t *testing.T) {
	store, done := offerTestStore(t)
	defer done()
	exec := t.Context()
	const stationID = 9802

	if err := store.DB.Exec(
		"INSERT INTO station(id,name,status,longitude,latitude) VALUES(?,?, 'active',116.4,39.9)",
		stationID, "整站口径隔离站点").Error; err != nil {
		t.Fatal(err)
	}
	defer store.DB.Exec("DELETE FROM pricing_rule WHERE station_id=?", stationID)
	defer store.DB.Exec("DELETE FROM station WHERE id=?", stationID)

	// 只存在一条设备范围的规则，站点本身从未被定价。
	if err := store.DB.Exec(
		"INSERT INTO pricing_rule(name,station_id,device_id,version,status,spec_json,channel) "+
			"VALUES('设备单独定价',?,'DEV-ONLY',1,'active',?,'default')",
		stationID, schemeRuleJSON(t, 100, 100),
	).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := store.ActiveStationRule(exec, stationID); !errors.Is(err, ErrRuleUnavailable) {
		t.Fatalf("整站口径返回了设备规则，err=%v", err)
	}

	// 同一台设备仍然能解析到自己的规则：这次收窄不能顺手破坏它本来要保护的
	// 那条覆盖路径。
	rule, err := store.ActiveDeviceRule(exec, stationID, "DEV-ONLY")
	if err != nil {
		t.Fatalf("设备规则查不到: %v", err)
	}
	if rule.DeviceID != "DEV-ONLY" {
		t.Fatalf("设备口径拿到的不是自己的规则: %+v", rule)
	}

	// 一旦站点整站定价完成，整站规则能被查到，而设备规则仍然覆盖它。
	if err := store.DB.Exec(
		"INSERT INTO pricing_rule(name,station_id,device_id,version,status,spec_json,channel) "+
			"VALUES('整站默认',?,NULL,1,'active',?,'default')",
		stationID, schemeRuleJSON(t, 30, 100),
	).Error; err != nil {
		t.Fatal(err)
	}
	stationRule, err := store.ActiveStationRule(exec, stationID)
	if err != nil {
		t.Fatalf("整站规则查不到: %v", err)
	}
	if stationRule.DeviceID != "" {
		t.Fatalf("整站口径取到了设备规则 %q", stationRule.DeviceID)
	}
	if got, want := stationRule.Spec.Electric.Periods[0].ElectricCents, int64(30); got != want {
		t.Fatalf("整站电价取错: got %d want %d", got, want)
	}
	deviceRule, err := store.ActiveDeviceRule(exec, stationID, "DEV-ONLY")
	if err != nil {
		t.Fatal(err)
	}
	if deviceRule.DeviceID != "DEV-ONLY" {
		t.Fatalf("设备规则被整站规则盖掉了: %+v", deviceRule)
	}
}
