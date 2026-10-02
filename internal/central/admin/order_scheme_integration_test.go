package admin

import (
	"context"
	"encoding/json"
	"fmt"
	orderpkg "github.com/ChargePilot2026/charge-pilot/internal/central/order"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestOrderSchemeNamesUseFrozenSnapshotsInOneBatch(t *testing.T) {
	for _, key := range []string{"TEST_USER_DATABASE_URL", "TEST_ADMIN_DATABASE_URL", "TEST_BILLING_DATABASE_URL"} {
		if os.Getenv(key) == "" {
			t.Skipf("MySQL schemas required: %s", key)
		}
	}
	ctx := context.Background()
	begin := func(key string) *gorm.DB {
		t.Helper()
		tx := openFinanceDB(t, key).Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		t.Cleanup(func() {
			if err := tx.Rollback().Error; err != nil {
				t.Errorf("rollback scheme fixtures: %v", err)
			}
		})
		return tx
	}
	userDB, adminDB, billingDB := begin("TEST_USER_DATABASE_URL"), begin("TEST_ADMIN_DATABASE_URL"), begin("TEST_BILLING_DATABASE_URL")
	store := ResourceStore{UserDB: userDB, AdminDB: adminDB, BillingDB: billingDB}
	tag := uuid.NewString()
	user := struct {
		ID     uint64
		Openid string
	}{Openid: "frozen-scheme-" + tag}
	if err := userDB.Table("user").Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	spec := pricing.Spec{Mode: pricing.ModeDeviceDuration, Scheme: &pricing.Scheme{Name: "下单时方案 A"}}
	specJSON, _ := json.Marshal(spec)
	liveRule := struct {
		ID       uint64
		Name     string
		Status   string
		SpecJSON string `gorm:"column:spec_json"`
	}{Name: "current-scheme-" + tag, Status: "active", SpecJSON: string(specJSON)}
	if err := adminDB.Table("pricing_rule").Create(&liveRule).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	device := "frozen-" + tag
	orders := make([]orderpkg.ChargeOrderRecord, 5)
	for i := range orders {
		order := orderpkg.ChargeOrderRecord{OrderNo: fmt.Sprintf("frozen-%s-%d", tag, i), UserID: user.ID, DeviceID: device, PortNo: 1, Status: "completed", PaymentStatus: "paid", CreatedMonth: month}
		if err := userDB.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		orders[i] = order
		if i == 2 { // no snapshot exists for this historical order.
			continue
		}
		name := "下单时方案 A"
		if i == 1 {
			name = " 下单时方案 B "
		} else if i == 4 {
			name = "  "
		}
		rule := pricing.Rule{ID: liveRule.ID, Version: 1, Spec: pricing.Spec{Mode: pricing.ModeDeviceDuration, Scheme: &pricing.Scheme{Name: name}}}
		packageName := "价格档：60分钟"
		if i == 1 {
			packageName = " 价格档：120分钟 "
		} else if i == 4 {
			packageName = "仅套餐名称"
		}
		offer := pricing.Offer{ID: uint64(101 + i), StationID: 1, Name: packageName, Mode: "duration", PriceCents: 100, DurationMinutes: 60}
		raw, err := json.Marshal(map[string]any{"rule": rule, "offer": offer})
		if err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			raw = []byte(`{}`)
		}
		if err := userDB.Create(&orderpkg.ChargePricingSnapshotRecord{ChargeOrderID: order.ID, PaymentIntentID: uuid.NewString(), UserID: user.ID, PortCode: device + ":1", PricingSnapshot: raw}).Error; err != nil {
			t.Fatal(err)
		}
	}
	reads := 0
	callback := "scheme_snapshot_reads_" + tag
	if err := userDB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "charge_order_pricing" {
			reads++
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { userDB.Callback().Query().Remove(callback) })
	assertPage := func() {
		t.Helper()
		before := reads
		page, err := store.Orders(ctx, OrderQuery{PageQuery: PageQuery{Page: 1, PageSize: 10}, DeviceID: device})
		if err != nil || page.Total != 5 || len(page.Items) != 5 {
			t.Fatalf("scheme page: count=%d total=%d err=%v", len(page.Items), page.Total, err)
		}
		if reads-before != 1 {
			t.Fatalf("snapshot reads=%d for five orders, want one batch", reads-before)
		}
		byID := map[uint64]OrderView{}
		for _, row := range page.Items {
			byID[row.OrderID] = row
		}
		for i, expected := range []string{"下单时方案 A", "下单时方案 B", "", "", ""} {
			got := byID[orders[i].ID].SelectedSchemeName
			if (expected == "" && got != nil) || (expected != "" && (got == nil || *got != expected)) {
				t.Fatalf("order %d frozen scheme=%v want %q", i, got, expected)
			}
		}
		for i, expected := range []string{"价格档：60分钟", "价格档：120分钟", "", "", "仅套餐名称"} {
			got := byID[orders[i].ID].SelectedPackageName
			if (expected == "" && got != nil) || (expected != "" && (got == nil || *got != expected)) {
				t.Fatalf("order %d frozen package=%v want %q", i, got, expected)
			}
		}
	}
	assertPage()
	changed, _ := json.Marshal(pricing.Spec{Mode: pricing.ModeDeviceDuration, Scheme: &pricing.Scheme{Name: "现在已改为方案 C"}})
	if err := adminDB.Table("pricing_rule").Where("id=?", liveRule.ID).Updates(map[string]any{"spec_json": string(changed), "version": 2}).Error; err != nil {
		t.Fatal(err)
	}
	assertPage() // Changes to the current rule cannot rewrite the purchased names.
	if err := adminDB.Table("pricing_rule").Where("id=?", liveRule.ID).Update("deleted_at", now).Error; err != nil {
		t.Fatal(err)
	}
	assertPage()
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("admin_profile", Profile{}) })
	router.GET("/orders/:id", (ResourceAPI{Store: store}).order)
	before := reads
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/orders/%d", orders[0].ID), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("order detail returned %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data OrderView `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data.SelectedSchemeName == nil || *envelope.Data.SelectedSchemeName != "下单时方案 A" || envelope.Data.SelectedPackageName == nil || *envelope.Data.SelectedPackageName != "价格档：60分钟" || envelope.Data.SelectedPackage == nil || envelope.Data.SelectedPackage.Offer.Name != "价格档：60分钟" {
		t.Fatalf("detail snapshot name/price band: %s err=%v", response.Body.String(), err)
	}
	if reads-before != 1 {
		t.Fatalf("detail duplicated its package snapshot read: %d", reads-before)
	}
}
