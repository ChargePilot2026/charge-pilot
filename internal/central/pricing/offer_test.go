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
// always filtered the retired column; the charging-user read did not, so
// something an operator believed was gone kept being sold.
func TestRetiredOffersAreNotSold(t *testing.T) {
	store, done := offerTestStore(t)
	defer done()
	exec := t.Context()
	if err := store.DB.Exec(
		"INSERT INTO station(id,name,status,longitude,latitude) VALUES(9801,'下架站点','active',116.4,39.9)").Error; err != nil {
		t.Fatal(err)
	}
	defer store.DB.Exec("DELETE FROM charge_offer WHERE station_id=9801")
	defer store.DB.Exec("DELETE FROM station WHERE id=9801")
	// Both rows are active. Only one is retired, so the only thing the query can
	// possibly be getting wrong is the deleted_at filter — which is the defect.
	for i, retired := range []string{"live", "retired"} {
		if err := store.DB.Exec(
			"INSERT INTO charge_offer(station_id,device_id,name,mode,price_cents,status,version,package_template_id,deleted_at) "+
				"VALUES(9801,NULL,'套餐','amount',100,'active',1,?,IF(?='retired',UTC_TIMESTAMP(),NULL))",
			7000+i, retired).Error; err != nil {
			t.Fatal(err)
		}
	}
	offers, err := store.ActiveOffers(exec, 9801, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].ID == 0 || offers[0].PriceCents != 100 {
		t.Fatalf("a retired package is still being sold: %+v", offers)
	}
	// Asking for it by id must fail the same way, or the charging user simply starts the
	// one the list is hiding.
	if _, err := store.ActiveOffer(exec, 9801, "", 7001); !errors.Is(err, ErrOfferUnavailable) {
		t.Fatalf("a retired package is still startable: %v", err)
	}
}

// A device that sells a package on its own does not also get the station-wide
// version of it. The comment on this function has always said so; the query
// listed both, so the charging user saw the same package twice.
func TestDeviceOfferOverridesTheStationWideOne(t *testing.T) {
	store, done := offerTestStore(t)
	defer done()
	if err := store.DB.Exec(
		"INSERT INTO station(id,name,status,longitude,latitude) VALUES(9802,'覆盖站点','active',116.4,39.9)").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Exec(
		"INSERT INTO device_meta(device_id,station_id,status) VALUES('OVRDEVICE1',9802,'enabled')").Error; err != nil {
		t.Fatal(err)
	}
	defer store.DB.Exec("DELETE FROM charge_offer WHERE station_id=9802")
	defer store.DB.Exec("DELETE FROM device_meta WHERE device_id='OVRDEVICE1'")
	defer store.DB.Exec("DELETE FROM station WHERE id=9802")

	// Package 7100 is sold station-wide and also, differently, on this one device.
	// Package 7200 is sold station-wide only.
	// Offers are told apart by which template they sell and which device they
	// are scoped to, not by a code: the column is gone as of admin_db/0045.
	rows := []struct {
		pkg      int
		deviceID any
	}{
		{7100, nil}, {7100, "OVRDEVICE1"},
		{7200, nil},
	}
	for _, row := range rows {
		if err := store.DB.Exec(
			"INSERT INTO charge_offer(station_id,device_id,name,mode,price_cents,status,version,package_template_id) "+
				"VALUES(9802,?,'套餐','amount',100,'active',1,?)", row.deviceID, row.pkg).Error; err != nil {
			t.Fatal(err)
		}
	}
	offers, err := store.ActiveOffers(context.Background(), 9802, "OVRDEVICE1")
	if err != nil {
		t.Fatal(err)
	}
	// Offers are told apart by device scope. The device must see exactly one
	// station-wide offer -- the 7200 -- and the 7100 only in its own copy.
	stationWide, deviceScoped := 0, 0
	for _, offer := range offers {
		if offer.DeviceID == "" {
			stationWide++
			continue
		}
		deviceScoped++
		if offer.DeviceID != "OVRDEVICE1" {
			t.Fatalf("an offer for another device leaked in: %+v", offer)
		}
	}
	if stationWide != 1 || deviceScoped != 1 {
		t.Fatalf("the device-scoped package did not override the station-wide one: %+v", offers)
	}
	// A different device on the same station still sees the station-wide version,
	// which is the point of scoping an offer to one pile.
	others, err := store.ActiveOffers(context.Background(), 9802, "OTHERDEVICE9")
	if err != nil {
		t.Fatal(err)
	}
	// A device that does not override anything sees both station-wide offers.
	if len(others) != 2 {
		t.Fatalf("a station-wide package disappeared for a device that does not override it: %+v", others)
	}
	for _, offer := range others {
		if offer.DeviceID != "" {
			t.Fatalf("a device-scoped offer leaked to a device that does not own it: %+v", offer)
		}
	}
}
