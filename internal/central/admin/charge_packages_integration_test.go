package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// A package is a template and an offer is what a station actually sells, so
// these tests cover the split: creating and editing a package touches no offer,
// applying one creates exactly one offer, and applying it twice is refused.

func TestChargePackageTemplateAndApply(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable databases required")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, os.Getenv("TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	adb, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	opts, _ := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	cache := redis.NewClient(opts)
	defer cache.Close()
	jwt, _ := auth.NewJWT(strings.Repeat("p", 32))
	a := API{Store: Store{DB: adb}, Sessions: Sessions{Redis: cache}, JWT: jwt}
	router := httpapi.NewRouter()
	a.Register(router)
	ResourceAPI{Store: ResourceStore{AdminDB: adb}, Auth: a}.Register(router)

	user := "pkg-admin"
	adb.Exec("DELETE FROM admin_user_role WHERE username=?", user)
	if e := adb.Exec("INSERT INTO admin_user_role(username,password_hash,role_id) SELECT ?,?,id FROM role WHERE code='customer_admin' AND deleted_at IS NULL", user, "unused-test-hash").Error; e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { adb.Exec("DELETE FROM admin_user_role WHERE username=?", user) })
	var account Account
	if e := adb.Table("admin_user_role").Where("username=?", user).Take(&account).Error; e != nil {
		t.Fatal(e)
	}
	sid, _, e := a.Sessions.Create(ctx, account)
	if e != nil {
		t.Fatal(e)
	}
	token, e := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(account.ID, 10), Kind: "admin", SessionID: sid, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if e != nil {
		t.Fatal(e)
	}

	callAs := func(tok, method, path string, body any, status int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "/api/v1/admin/settings/"+path, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	call := func(method, path string, body any, status int) map[string]any {
		t.Helper()
		return callAs(token, method, path, body, status)
	}

	t.Cleanup(func() {
		adb.Exec("DELETE FROM charge_offer WHERE package_id IS NOT NULL")
		adb.Exec("DELETE FROM charge_package WHERE name = ?", "月付套餐")
	})

	// A station to apply to.
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	stationCode := "PKGSTATION" + stamp[len(stamp)-6:]
	adb.Exec("INSERT INTO station(code,name,address,longitude,latitude,status) VALUES(?,?,?,116.397,39.908,'active')", stationCode, "套餐验收站点", "测试地址")
	var stationID uint64
	if e := adb.Table("station").Where("code=?", stationCode).Pluck("id", &stationID).Error; e != nil || stationID == 0 {
		t.Fatalf("station fixture: %d %v", stationID, e)
	}
	t.Cleanup(func() { adb.Exec("DELETE FROM station WHERE id=?", stationID) })

	// Creating a package must not require a station: that is the whole point of
	// separating the template from the offer.
	created := call("POST", "charge-packages", gin.H{
		"name": "月付套餐", "mode": "amount",
		"price_cents": 3000, "duration_minutes": 0, "status": "active",
	}, 200)["data"].(map[string]any)
	pkgID := uint64(created["id"].(float64))
	version := uint32(created["version"].(float64))

	// The code is generated from the id, in the same shape as fee receipts, so
	// an operator cannot collide one by typing it and cannot change one later.
	if got := created["code"]; got != packageCode(pkgID) {
		t.Fatalf("generated code = %v, want %q", got, packageCode(pkgID))
	}
	var storedCode string
	if e := adb.Table("charge_package").Where("id=?", pkgID).Pluck("code", &storedCode).Error; e != nil {
		t.Fatal(e)
	}
	if storedCode != packageCode(pkgID) {
		t.Fatalf("stored code = %q, want %q; the temporary value must not survive", storedCode, packageCode(pkgID))
	}

	var offers int64
	adb.Table("charge_offer").Where("package_id=?", pkgID).Count(&offers)
	if offers != 0 {
		t.Fatalf("creating a package created %d offers; it must create none", offers)
	}

	// Parameter validation still applies to the template itself.
	call("POST", "charge-packages", gin.H{"name": "", "mode": "amount", "price_cents": 100, "status": "active"}, 400)
	call("POST", "charge-packages", gin.H{"name": "x", "mode": "package", "price_cents": 100, "duration_minutes": 0, "status": "active"}, 400)
	call("POST", "charge-packages", gin.H{"name": "x", "mode": "amount", "price_cents": 0, "status": "active"}, 400)
	call("POST", "charge-packages", gin.H{"name": "x", "mode": "amount", "price_cents": 100, "status": "nope"}, 400)

	// Applying needs a station.
	call("POST", "charge-packages/"+strconv.FormatUint(pkgID, 10)+"/apply", gin.H{"station_id": 0}, 400)
	call("POST", "charge-packages/999999999999/apply", gin.H{"station_id": stationID}, 404)

	applied := call("POST", "charge-packages/"+strconv.FormatUint(pkgID, 10)+"/apply", gin.H{"station_id": stationID}, 200)["data"].(map[string]any)
	offerID := uint64(applied["offer_id"].(float64))
	if offerID == 0 {
		t.Fatal("apply reported no offer")
	}

	// The offer is a copy of the package, not a reference: later edits to the
	// package must not reach back and change what this station sells.
	var offer struct {
		PackageID  uint64
		PriceCents int64
		Mode       string
		Status     string
	}
	if e := adb.Table("charge_offer").Where("id=?", offerID).Take(&offer).Error; e != nil {
		t.Fatal(e)
	}
	if offer.PackageID != pkgID || offer.PriceCents != 3000 || offer.Mode != "amount" || offer.Status != "active" {
		t.Fatalf("offer does not carry the package values: %+v", offer)
	}

	// Applying the same package to the same station twice is refused, so the
	// mini program never shows two indistinguishable entries.
	call("POST", "charge-packages/"+strconv.FormatUint(pkgID, 10)+"/apply", gin.H{"station_id": stationID}, 409)
	adb.Table("charge_offer").Where("package_id=?", pkgID).Count(&offers)
	if offers != 1 {
		t.Fatalf("after a rejected re-apply there are %d offers, want 1", offers)
	}

	// Editing the package leaves the applied offer alone.
	call("PUT", "charge-packages/"+strconv.FormatUint(pkgID, 10), gin.H{
		"name": "月付套餐改价", "mode": "amount",
		"price_cents": 5000, "duration_minutes": 0, "status": "active",
		"expected_version": version,
	}, 200)
	var after struct{ PriceCents int64 }
	adb.Table("charge_offer").Where("id=?", offerID).Take(&after)
	if after.PriceCents != 3000 {
		t.Fatalf("applied offer price changed to %d when the package was edited; it is a snapshot", after.PriceCents)
	}

	// A stale version is refused rather than silently overwriting.
	call("PUT", "charge-packages/"+strconv.FormatUint(pkgID, 10), gin.H{
		"name": "并发修改", "mode": "amount",
		"price_cents": 6000, "duration_minutes": 0, "status": "active",
		"expected_version": version,
	}, 409)

	// The list carries the applied stations, so the refusal above is
	// discoverable before it is hit.
	list := call("GET", "charge-packages", nil, 200)["data"].(map[string]any)
	found := false
	for _, raw := range list["items"].([]any) {
		row := raw.(map[string]any)
		if uint64(row["id"].(float64)) != pkgID {
			continue
		}
		found = true
		if applied, _ := row["applied_stations"].(string); !strings.Contains(applied, "套餐验收站点") {
			t.Fatalf("applied stations missing from the list row: %q", applied)
		}
	}
	if !found {
		t.Fatal("created package missing from the list")
	}

	// A disabled package cannot be applied, rather than landing on a station as
	// an entry nobody can buy.
	adb.Exec("UPDATE charge_package SET status='disabled' WHERE id=?", pkgID)
	call("POST", "charge-packages/"+strconv.FormatUint(pkgID, 10)+"/apply", gin.H{"station_id": stationID + 0}, 409)

	// The endpoint is behind the same auth as the rest of the settings pages.
	callAs("", "GET", "charge-packages", nil, 401)
}
