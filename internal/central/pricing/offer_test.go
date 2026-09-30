package pricing

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
)

// 这些用例需要真实数据库：它们守的缺陷在 SQL 里而不在 Go 里，
// 假的实现会无条件同意查询本来应该查出的任何结果，也就等于什么都没验。

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

// 下了架的套餐必须停止被售卖。后台列表一直都有过滤下架标记，
// 而充电用户这一侧的读取没有，于是运营方以为已经没了的东西一直在卖。
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
	// 两行的 status 都是 active，只有一行被下架，所以这条查询唯一可能写错的
	// 就是 deleted_at 过滤——而这正是被守的缺陷。
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
	// 按 id 去问它也必须以同样的方式失败，否则充电用户只要直接挑列表里
	// 藏起来的那个就能开单。
	if _, err := store.ActiveOffer(exec, 9801, "", 7001); !errors.Is(err, ErrOfferUnavailable) {
		t.Fatalf("a retired package is still startable: %v", err)
	}
}

// 自己单卖某个套餐的设备，不应该同时还拿到该套餐的整站版本。
// 这个函数的注释一向是这么写的；而查询把两条都列了出来，
// 于是充电用户会看到同一个套餐两次。
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

	// 套餐 7100 整站发售，同时又在这台设备上以另一套价格单卖。
	// 套餐 7200 只整站发售。
	// 区分套餐靠的是它卖的是哪个模板、归属哪台设备，而不是一个 code：
	// 那一列自 admin_db/0045 起已经没有了。
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
	// 套餐靠归属设备来区分。这台设备必须恰好看到一个整站套餐——7200——
	// 而 7100 只在它自己那一份里出现。
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
	// 同一站点的另一台设备仍然看得到整站版本，这正是把一个套餐限定到
	// 某个桩的意义所在。
	others, err := store.ActiveOffers(context.Background(), 9802, "OTHERDEVICE9")
	if err != nil {
		t.Fatal(err)
	}
	// 什么都没覆盖的设备看到的是两个整站套餐。
	if len(others) != 2 {
		t.Fatalf("a station-wide package disappeared for a device that does not override it: %+v", others)
	}
	for _, offer := range others {
		if offer.DeviceID != "" {
			t.Fatalf("a device-scoped offer leaked to a device that does not own it: %+v", offer)
		}
	}
}
