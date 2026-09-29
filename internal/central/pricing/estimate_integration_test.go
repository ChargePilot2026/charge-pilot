package pricing

import (
	"errors"
	"testing"
)

// A station tariff is the rule with no device_id. Asking for it used to run the
// same query as asking for a device's rule, minus the device filter, so a
// station that had never been priced as a whole but did have one pile priced on
// its own reported that pile's tariff as the station's.
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

	// Only a device-scoped rule exists. The station itself was never priced.
	if err := store.DB.Exec(
		"INSERT INTO pricing_rule(name,station_id,device_id,version,status,spec_json,channel) "+
			"VALUES('设备单独定价',?,'DEV-ONLY',1,'active',?,'default')",
		stationID, `{"mode":"server_energy","electric":{"basis":"energy","periods":[{"end_minute":1440,"electric_cents":100}]}}`,
	).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := store.ActiveStationRule(exec, stationID); !errors.Is(err, ErrRuleUnavailable) {
		t.Fatalf("整站口径返回了设备规则，err=%v", err)
	}

	// The same device still resolves to its own rule: the narrowing must not
	// break the override path it was meant to protect.
	rule, err := store.ActiveDeviceRule(exec, stationID, "DEV-ONLY")
	if err != nil {
		t.Fatalf("设备规则查不到: %v", err)
	}
	if rule.DeviceID != "DEV-ONLY" {
		t.Fatalf("设备口径拿到的不是自己的规则: %+v", rule)
	}

	// Once the station is priced as a whole, the station rule is found and the
	// device still overrides it.
	if err := store.DB.Exec(
		"INSERT INTO pricing_rule(name,station_id,device_id,version,status,spec_json,channel) "+
			"VALUES('整站默认',?,NULL,1,'active',?,'default')",
		stationID, `{"mode":"server_energy","electric":{"basis":"energy","periods":[{"end_minute":1440,"electric_cents":30}]}}`,
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
