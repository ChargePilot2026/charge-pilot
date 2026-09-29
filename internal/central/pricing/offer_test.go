package pricing

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
)

// These need a real database: the defects they guard are in the SQL, not in Go,
// and a fake would have agreed with whatever the query was supposed to say.

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

// A package that was taken off sale must stop being offered. The admin list has
// always filtered the retired column; the rider-facing read did not, so
// something an operator believed was gone kept being sold.
func TestRetiredOffersAreNotSold(t *testing.T) {
	store, done := offerTestStore(t)
	defer done()
	exec := t.Context()
	if err := store.DB.Exec(
		"INSERT INTO station(id,code,name,status,longitude,latitude) VALUES(9801,'RETIRED-A','下架场地','active',116.4,39.9)").Error; err != nil {
		t.Fatal(err)
	}
	defer store.DB.Exec("DELETE FROM charge_offer WHERE station_id=9801")
	defer store.DB.Exec("DELETE FROM station WHERE id=9801")
	// Both rows are active. Only one is retired, so the only thing the query can
	// possibly be getting wrong is the deleted_at filter — which is the defect.
	for i, retired := range []string{"live", "retired"} {
		if err := store.DB.Exec(
			"INSERT INTO charge_offer(station_id,device_id,code,name,mode,price_cents,status,version,package_template_id,deleted_at) "+
				"VALUES(9801,NULL,?,'套餐','amount',100,'active',1,?,IF(?='retired',UTC_TIMESTAMP(),NULL))",
			"RETIRED-"+string(rune('A'+i)), 7000+i, retired).Error; err != nil {
			t.Fatal(err)
		}
	}
	offers, err := store.ActiveOffers(exec, 9801, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].Code != "RETIRED-A" {
		t.Fatalf("a retired package is still being sold: %+v", offers)
	}
	// Asking for it by id must fail the same way, or the rider simply starts the
	// one the list is hiding.
	if _, err := store.ActiveOffer(exec, 9801, "", 7001); !errors.Is(err, ErrOfferUnavailable) {
		t.Fatalf("a retired package is still startable: %v", err)
	}
}

// A device that sells a package on its own does not also get the yard-wide
// version of it. The comment on this function has always said so; the query
// listed both, so the rider saw the same package twice.
func TestDeviceOfferOverridesTheYardWideOne(t *testing.T) {
	store, done := offerTestStore(t)
	defer done()
	if err := store.DB.Exec(
		"INSERT INTO station(id,code,name,status,longitude,latitude) VALUES(9802,'OVERRIDE-A','覆盖场地','active',116.4,39.9)").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Exec(
		"INSERT INTO device_meta(device_id,station_id,status) VALUES('OVRDEVICE1',9802,'enabled')").Error; err != nil {
		t.Fatal(err)
	}
	defer store.DB.Exec("DELETE FROM charge_offer WHERE station_id=9802")
	defer store.DB.Exec("DELETE FROM device_meta WHERE device_id='OVRDEVICE1'")
	defer store.DB.Exec("DELETE FROM station WHERE id=9802")

	// Package 7100 is sold yard-wide and also, differently, on this one device.
	// Package 7200 is sold yard-wide only.
	rows := []struct {
		code     string
		pkg      int
		deviceID any
	}{
		{"YARD-OVR", 7100, nil}, {"DEVICE-OVR", 7100, "OVRDEVICE1"},
		{"YARD-ONLY", 7200, nil},
	}
	for _, row := range rows {
		if err := store.DB.Exec(
			"INSERT INTO charge_offer(station_id,device_id,code,name,mode,price_cents,status,version,package_template_id) "+
				"VALUES(9802,?,?,'套餐','amount',100,'active',1,?)", row.deviceID, row.code, row.pkg).Error; err != nil {
			t.Fatal(err)
		}
	}
	offers, err := store.ActiveOffers(context.Background(), 9802, "OVRDEVICE1")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, offer := range offers {
		seen[offer.Code] = true
	}
	if seen["YARD-OVR"] {
		t.Fatalf("the device-scoped package did not override the yard-wide one: %+v", offers)
	}
	if !seen["DEVICE-OVR"] || !seen["YARD-ONLY"] {
		t.Fatalf("an offer that should be on sale is missing: %+v", offers)
	}
	// A different device on the same yard still sees the yard-wide version,
	// which is the point of scoping an offer to one pile.
	others, err := store.ActiveOffers(context.Background(), 9802, "OTHERDEVICE9")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, offer := range others {
		if offer.Code == "YARD-OVR" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a yard-wide package disappeared for a device that does not override it: %+v", others)
	}
}
