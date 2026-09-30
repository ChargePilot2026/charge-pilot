package billing

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// settlementCleanup 摘掉一次 settlementFixture 留下的全部痕迹。
//
// 这些夹具写的是共享开发库，不是每轮现建的库：站点名是裸 UUID、分账模板
// 挂在站点上、计费单和结算行挂在模板上。少了这一步，每跑一次结算集成测试
// 就往 admin_db 塞一个站点，几轮之后后台的站点列表被测试垃圾淹没，demo
// 站点被挤到最后一页，结算包也因为库越滚越大而开始偶发失败。
//
// 删除顺序与写入顺序相反：先摘按结算单号落库的收款凭据与计费单，再摘
// 结算头与它的参与方金额，最后才轮到分账模板和站点。少删一张表，孤儿行
// 就会顶住下一轮的唯一键。
//
// settlementFloor 兜住另一种痕迹：调度器跑一遍会把库里所有到期的计费单都结掉，
// 不只是本测试造的那一张。这些顺带结出来的行按结算单号认不出来（它们属于别的
// 计费单），只能按"本轮之前库里最大 id"划线，清理时把线以上的一并摘掉。
func settlementCleanup(t *testing.T, stationName, calculationNo string, month string) {
	t.Helper()
	billingDB := openSettlementDB(t, "TEST_BILLING_DATABASE_URL")
	// 记下本轮开始时结算表的最大主键。清理时凡是更大的都是这轮新写的——它可能
	// 是本测试结的，也可能是调度器顺手把别人的待结算一起结了。
	var floor int64
	if err := billingDB.Raw("SELECT COALESCE(MAX(id),0) FROM settlement").Row().Scan(&floor); err != nil {
		t.Fatalf("读取结算表水位失败: %v", err)
	}
	t.Cleanup(func() {
		adminDB := openSettlementDB(t, "TEST_ADMIN_DATABASE_URL")
		statements := []struct {
			db    *gorm.DB
			query string
		}{
			{billingDB, "DELETE FROM settlement_party_amount WHERE settlement_id > " + strconv.FormatInt(floor, 10)},
			{billingDB, "DELETE FROM settlement WHERE id > " + strconv.FormatInt(floor, 10)},
			// settlement 按 created_month 做 RANGE 分区，分区键进 WHERE 才好
			// 让分区裁剪生效；漏掉它不是错，但会让清理扫全表。
			{billingDB, "DELETE FROM settlement_party_amount WHERE settlement_id IN (SELECT id FROM settlement WHERE created_month = '" + month + "' AND order_no = (SELECT order_no FROM fee_calculation WHERE calculation_no = '" + calculationNo + "'))"},
			{billingDB, "DELETE FROM settlement WHERE created_month = '" + month + "' AND fee_calculation_id = (SELECT id FROM fee_calculation WHERE calculation_no = '" + calculationNo + "')"},
			{billingDB, "DELETE FROM fee_receipt WHERE calculation_no = '" + calculationNo + "'"},
			{billingDB, "DELETE FROM fee_calculation WHERE calculation_no = '" + calculationNo + "'"},
			{adminDB, "DELETE FROM split_party WHERE split_template_id IN (SELECT id FROM split_template WHERE code = 'tpl" + stationName + "')"},
			{adminDB, "DELETE FROM split_template WHERE code = 'tpl" + stationName + "'"},
			{adminDB, "DELETE FROM station WHERE name = '" + stationName + "'"},
		}
		for _, statement := range statements {
			if err := statement.db.Exec(statement.query).Error; err != nil {
				// 清理失败必须喊出来。静默吞掉的话，这一轮的垃圾会一直留在
				// 库里，下一轮测试再被它绊倒，而症状指向的是完全无关的地方。
				t.Errorf("清理结算夹具失败 (%v): %s", err, statement.query)
			}
		}
	})
}

func settlementFixture(t *testing.T, ctx context.Context, mode string, ratios []int32, electric, service int64) (uint64, uint64, uint64) {
	t.Helper()
	adminDB := openSettlementDB(t, "TEST_ADMIN_DATABASE_URL")
	station := uuid.NewString()
	orderNo := "settlement-" + uuid.NewString()
	month := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	calculationNo := "FEE" + orderNo
	settlementCleanup(t, station, calculationNo, month.Format("2006-01-02"))
	if err := adminDB.Exec("INSERT INTO station(name,longitude,latitude,split_template_id) VALUES(?,0,0,NULL)", station).Error; err != nil {
		t.Fatal(err)
	}
	var stationID uint64
	adminDB.Table("station").Where("name=?", station).Pluck("id", &stationID)
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
	// 这个站点不挂分账模板，所以走不了 settlementFixture 的清理，只能自己摘。
	t.Cleanup(func() {
		if err := adminDB.Exec("DELETE FROM station WHERE name = ?", name).Error; err != nil {
			t.Errorf("清理无模板站点夹具失败: %v", err)
		}
	})
	if err := adminDB.Exec("INSERT INTO station(name,longitude,latitude) VALUES(?,0,0)", name).Error; err != nil {
		t.Fatal(err)
	}
	adminDB.Table("station").Where("name=?", name).Pluck("id", &bare)
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
