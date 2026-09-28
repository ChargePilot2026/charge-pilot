package billing

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func settlementFixture(t *testing.T, ctx context.Context, mode string, ratios []int32, electric, service int64) (uint64, uint64, uint64) {
	t.Helper()
	adminDB := openSettlementDB(t, "TEST_ADMIN_DATABASE_URL")
	station := uuid.NewString()
	if err := adminDB.Exec("INSERT INTO station(code,name,longitude,latitude,split_template_id) VALUES(?,?,0,0,NULL)", station, station).Error; err != nil {
		t.Fatal(err)
	}
	var stationID uint64
	adminDB.Table("station").Where("code=?", station).Pluck("id", &stationID)
	if err := adminDB.Exec("INSERT INTO split_template(code,name,mode) VALUES(?,?,?)", "tpl"+station, "分账模板", mode).Error; err != nil {
		t.Fatal(err)
	}
	var templateID uint64
	adminDB.Table("split_template").Where("code=?", "tpl"+station).Pluck("id", &templateID)
	for i, ratio := range ratios {
		if err := adminDB.Exec("INSERT INTO split_party(split_template_id,party_code,party_name,ratio_bp) VALUES(?,?,?,?)",
			templateID, "party"+station+string(rune('A'+i)), "参与方"+string(rune('A'+i)), ratio).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := adminDB.Exec("UPDATE station SET split_template_id=? WHERE id=?", templateID, stationID).Error; err != nil {
		t.Fatal(err)
	}

	billingDB := openSettlementDB(t, "TEST_BILLING_DATABASE_URL")
	orderNo := "settlement-" + uuid.NewString()
	month := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	calculationNo := "FEE" + orderNo
	// charge_order_id is the global receipt key. Scanning MAX through GORM keeps
	// the driver conversion in one place instead of hand-writing a CAST.
	var highest struct {
		Value *uint64 `gorm:"column:value"`
	}
	if err := billingDB.Table("fee_receipt").Select("MAX(charge_order_id) AS value").Take(&highest).Error; err != nil {
		t.Fatal(err)
	}
	orderID := uint64(1)
	if highest.Value != nil {
		orderID = *highest.Value + uint64(len(orderNo))
	}
	if err := billingDB.Exec(`INSERT INTO fee_calculation(calculation_no,order_no,charge_order_id,user_id,station_id,charged_kwh,charged_seconds,electric_cents,service_cents,total_cents,created_month)
		VALUES(?,?,?,1,?,1.0000,3600,?,?,?,?)`, calculationNo, orderNo, orderID, stationID, electric, service, electric+service, month).Error; err != nil {
		t.Fatal(err)
	}
	var calculationID uint64
	billingDB.Table("fee_calculation").Where("calculation_no=? AND created_month=?", calculationNo, month).Pluck("id", &calculationID)
	if err := billingDB.Exec("INSERT INTO fee_receipt(charge_order_id,source_json,calculation_id,calculation_no) VALUES(?,?,?,?)",
		orderID, `{}`, calculationID, calculationNo).Error; err != nil {
		t.Fatal(err)
	}
	return calculationID, stationID, orderID
}

func openSettlementDB(t *testing.T, key string) *gorm.DB {
	t.Helper()
	db, err := dbconn.Open(context.Background(), os.Getenv(key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

func TestSettlementSplitsEveryCentExactlyOnce(t *testing.T) {
	if os.Getenv("TEST_BILLING_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	// 3001/6999 with a 60/40 split leaves a remainder on both components; the
	// allocation must still conserve the pool exactly.
	electric, service := int64(3001), int64(6999)
	calculationID, stationID, _ := settlementFixture(t, ctx, "mode_a", []int32{6000, 4000}, electric, service)
	store := Store{DB: openSettlementDB(t, "TEST_BILLING_DATABASE_URL")}
	resolver := SplitResolver{AdminDB: openSettlementDB(t, "TEST_ADMIN_DATABASE_URL")}
	template, err := resolver.Resolve(ctx, stationID)
	if err != nil {
		t.Fatal(err)
	}
	month, err := store.CalculationMonth(ctx, calculationID)
	if err != nil {
		t.Fatal(err)
	}
	fee := actualFee(electric, service)
	settlementID, created, err := store.Settle(ctx, calculationID, template, fee, month)
	if err != nil || !created {
		t.Fatalf("settle: %v created=%v", err, created)
	}
	type party struct {
		Code          string `gorm:"column:party_code"`
		Ratio         uint32 `gorm:"column:ratio_bp"`
		Amount        int64  `gorm:"column:amount_cents"`
		ElectricCents int64  `gorm:"column:electric_cents"`
		ServiceCents  int64  `gorm:"column:service_cents"`
	}
	parties := []party{}
	if err := store.DB.WithContext(ctx).Table("settlement_party_amount").Where("settlement_id=?", settlementID).Order("party_code").Find(&parties).Error; err != nil {
		t.Fatal(err)
	}
	if len(parties) != 2 {
		t.Fatalf("parties %d", len(parties))
	}
	var pool int64
	for _, p := range parties {
		if p.Amount != p.ElectricCents+p.ServiceCents {
			t.Fatalf("party components do not sum: %+v", p)
		}
		pool += p.Amount
	}
	if pool != electric+service {
		t.Fatalf("split pool lost money: %d != %d", pool, electric+service)
	}
	var header struct {
		SplitPool        int64 `gorm:"column:split_pool"`
		ExcludedElectric int64 `gorm:"column:excluded_electric"`
		Total            int64 `gorm:"column:total"`
	}
	if err := store.DB.WithContext(ctx).Table("settlement").Select("split_pool_cents AS split_pool,split_pool_excluded_electric_cents AS excluded_electric,total_cents AS total").Where("id=?", settlementID).Take(&header).Error; err != nil {
		t.Fatal(err)
	}
	if header.SplitPool != electric+service || header.ExcludedElectric != 0 || header.Total != electric+service {
		t.Fatalf("settlement header %+v", header)
	}
	// Replaying the same billing dispatch must not pay the parties twice.
	againID, created, err := store.Settle(ctx, calculationID, template, fee, month)
	if err != nil || created || againID != settlementID {
		t.Fatalf("replay created a second settlement: %d %v %v", againID, created, err)
	}
	var count int64
	store.DB.Table("settlement_party_amount").Where("settlement_id=?", settlementID).Count(&count)
	if count != 2 {
		t.Fatalf("party rows %d", count)
	}
}

func TestSettlementModeBExcludesElectricity(t *testing.T) {
	if os.Getenv("TEST_BILLING_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	electric, service := int64(4001), int64(5003)
	calculationID, stationID, _ := settlementFixture(t, ctx, "mode_b", []int32{5000, 5000}, electric, service)
	store := Store{DB: openSettlementDB(t, "TEST_BILLING_DATABASE_URL")}
	template, err := (SplitResolver{AdminDB: openSettlementDB(t, "TEST_ADMIN_DATABASE_URL")}).Resolve(ctx, stationID)
	if err != nil {
		t.Fatal(err)
	}
	month, _ := store.CalculationMonth(ctx, calculationID)
	settlementID, _, err := store.Settle(ctx, calculationID, template, actualFee(electric, service), month)
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		SplitPool        int64 `gorm:"column:split_pool"`
		ExcludedElectric int64 `gorm:"column:excluded_electric"`
		Total            int64 `gorm:"column:total"`
	}
	if err := store.DB.WithContext(ctx).Table("settlement").Select("split_pool_cents AS split_pool,split_pool_excluded_electric_cents AS excluded_electric,total_cents AS total").Where("id=?", settlementID).Take(&header).Error; err != nil {
		t.Fatal(err)
	}
	if header.SplitPool != service || header.ExcludedElectric != electric {
		t.Fatalf("mode_b header %+v, want pool=%d excluded=%d", header, service, electric)
	}
	var electricSplit int64
	if err := store.DB.WithContext(ctx).Table("settlement_party_amount").Where("settlement_id=?", settlementID).Select("COALESCE(SUM(electric_cents),0)").Scan(&electricSplit).Error; err != nil {
		t.Fatal(err)
	}
	if electricSplit != 0 {
		t.Fatalf("mode_b still split electricity: %d", electricSplit)
	}
}

func TestSettlementRejectsInvalidTemplateRatios(t *testing.T) {
	if os.Getenv("TEST_BILLING_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	// 9000 basis points can never be settled; the resolver must refuse the
	// template instead of silently paying out an unbalanced split.
	_, stationID, _ := settlementFixture(t, ctx, "mode_a", []int32{5000, 4000}, 100, 100)
	resolver := SplitResolver{AdminDB: openSettlementDB(t, "TEST_ADMIN_DATABASE_URL")}
	if _, err := resolver.Resolve(ctx, stationID); err == nil {
		t.Fatal("unbalanced ratios accepted")
	}
	// A station bound to no template at all must fail loudly rather than
	// producing a zero-value settlement.
	var bare uint64
	name := "bare-" + uuid.NewString()
	adminDB := openSettlementDB(t, "TEST_ADMIN_DATABASE_URL")
	if err := adminDB.Exec("INSERT INTO station(code,name,longitude,latitude) VALUES(?,?,0,0)", name, name).Error; err != nil {
		t.Fatal(err)
	}
	adminDB.Table("station").Where("code=?", name).Pluck("id", &bare)
	if _, err := resolver.Resolve(ctx, bare); err == nil {
		t.Fatal("station without split template accepted")
	}
}

func TestSettlementBacklogFillsCalculatedFees(t *testing.T) {
	if os.Getenv("TEST_BILLING_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	electric, service := int64(1000), int64(500)
	calculationID, stationID, _ := settlementFixture(t, ctx, "mode_a", []int32{7000, 3000}, electric, service)
	store := Store{DB: openSettlementDB(t, "TEST_BILLING_DATABASE_URL")}
	pending, err := store.SettlementsDue(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range pending {
		if row.CalculationID == calculationID && row.StationID != nil && *row.StationID == stationID {
			found = true
		}
	}
	if !found {
		t.Fatal("calculated fee missing from the settlement backlog")
	}
	// Running the dispatcher with no due orders must still settle the backlog.
	dispatcher := Service{Store: store, Orders: emptyOrders{}, Splits: SplitResolver{AdminDB: openSettlementDB(t, "TEST_ADMIN_DATABASE_URL")}}
	count, err := dispatcher.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("settled %d, want 1", count)
	}
	if remaining, err := store.SettlementsDue(ctx, 50); err != nil {
		t.Fatal(err)
	} else {
		for _, row := range remaining {
			if row.CalculationID == calculationID {
				t.Fatal("backlog still pending after settlement")
			}
		}
	}
	if _, err := dispatcher.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var settlements int64
	store.DB.Table("settlement").Where("fee_calculation_id=?", calculationID).Count(&settlements)
	if settlements != 1 {
		t.Fatalf("settlements %d", settlements)
	}
}

type emptyOrders struct{}

func (emptyOrders) Due(context.Context) ([]uint64, error)               { return nil, nil }
func (emptyOrders) Read(context.Context, uint64) (Source, error)        { return Source{}, sql.ErrNoRows }
func (emptyOrders) Apply(context.Context, Result) error                 { return nil }
func (emptyOrders) Defer(context.Context, uint64, string, string) error { return nil }

func actualFee(electric, service int64) (fee actualFeeType) {
	return actualFeeType{ElectricCents: electric, ServiceCents: service, TotalCents: electric + service}
}

// The settlement writer takes the pricing result type directly; the alias keeps
// the test helpers readable without importing the pricing package twice.
type actualFeeType = pricing.ActualFee
