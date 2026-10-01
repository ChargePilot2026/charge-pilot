package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
)

// 使用真实数据库验证规则查询与 SQL 过滤条件。

func offerTestStore(t *testing.T) (Store, func()) {
	t.Helper()
	raw := os.Getenv("TEST_ADMIN_DATABASE_URL")
	if raw == "" {
		t.Skip("disposable MySQL required")
	}
	db, err := dbconn.Open(context.Background(), raw)
	if err != nil {
		t.Skipf("disposable MySQL required: %v", err)
	}
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Skipf("disposable MySQL required: %v", err)
	}
	return Store{DB: orm}, func() { _ = db.Close() }
}

func schemeRuleJSON(t *testing.T, rate, paid int64) string {
	t.Helper()
	s := Scheme{Name: "方案", Amount: &AmountMode{Algorithm: ModeServerEnergy, Periods: []Period{{EndMinute: 1440, ElectricCents: rate}}}, Packages: []Package{{ID: 1, Name: "金额", Mode: "amount", PriceCents: paid}}}.Normalized()
	raw, err := json.Marshal(s.SpecFor(s.Packages[0]))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func TestOffersFollowWholeAppliedScheme(t *testing.T) {
	store, done := offerTestStore(t)
	defer done()
	ctx := t.Context()
	const site = 9801
	if err := store.DB.Exec("INSERT INTO station(id,name,status,longitude,latitude) VALUES(?,'整体方案站点','active',116.4,39.9)", site).Error; err != nil {
		t.Fatal(err)
	}
	defer store.DB.Exec("DELETE FROM station WHERE id=?", site)
	defer store.DB.Exec("DELETE FROM pricing_rule WHERE station_id=?", site)
	if err := store.DB.Exec("INSERT INTO pricing_rule(name,station_id,version,status,spec_json) VALUES('站点方案',?,1,'active',?)", site, schemeRuleJSON(t, 50, 100)).Error; err != nil {
		t.Fatal(err)
	}
	inherited, err := store.ActiveOffers(ctx, site, "INHERITED")
	if err != nil || len(inherited) != 1 || inherited[0].PriceCents != 100 {
		t.Fatalf("inherited=%+v err=%v", inherited, err)
	}
	if err := store.DB.Exec("INSERT INTO pricing_rule(name,station_id,device_id,version,status,spec_json) VALUES('设备整套方案',?,'INDEPENDENT',1,'active',?)", site, schemeRuleJSON(t, 80, 200)).Error; err != nil {
		t.Fatal(err)
	}
	independent, err := store.ActiveOffers(ctx, site, "INDEPENDENT")
	if err != nil || len(independent) != 1 || independent[0].PriceCents != 200 {
		t.Fatalf("independent=%+v err=%v", independent, err)
	}
	if _, err := store.ActiveOffer(ctx, site, "INDEPENDENT", inherited[0].ID); !errors.Is(err, ErrOfferUnavailable) {
		t.Fatalf("station offer merged into independent scheme: %v", err)
	}
	if err := store.DB.Exec("UPDATE pricing_rule SET status='disabled' WHERE station_id=? AND device_id='INDEPENDENT'", site).Error; err != nil {
		t.Fatal(err)
	}
	restored, err := store.ActiveOffers(ctx, site, "INDEPENDENT")
	if err != nil || len(restored) != 1 || restored[0].ID != inherited[0].ID {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
}
