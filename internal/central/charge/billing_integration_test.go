package charge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	orderpkg "github.com/ChargePilot2026/charge-pilot/internal/central/order"
	refundpkg "github.com/ChargePilot2026/charge-pilot/internal/central/refund"
	settlementpkg "github.com/ChargePilot2026/charge-pilot/internal/central/settlement"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/channel"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type scopedBillingOrders struct {
	settlementpkg.BillingOrders
	ID uint64
}

func (s scopedBillingOrders) Due(ctx context.Context) ([]uint64, error) {
	all, err := s.BillingOrders.Due(ctx)
	out := []uint64{}
	for _, id := range all {
		if id == s.ID {
			out = append(out, id)
		}
	}
	return out, err
}

func TestActualBillingPersistsAndRefundsOnce(t *testing.T) {
	if os.Getenv("TEST_BILLING_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	open := func(key string) *gorm.DB {
		db, err := dbconn.Open(ctx, os.Getenv(key))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return testGORMDB(t, db)
	}
	userDB, billingDB := open("TEST_USER_DATABASE_URL"), open("TEST_BILLING_DATABASE_URL")
	exec := func(db *gorm.DB, q string, args ...any) {
		t.Helper()
		if err := db.Exec(q, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	// cleanupBillingFixture 按依赖顺序清理计费、凭据、复核、退款、支付、订单及用户夹具。
	// 清理使用本轮订单和支付标识，避免残留计费记录占用后续调度的取数窗口。
	cleanupBillingFixture := func(name string) {
		t.Cleanup(func() {
			statements := []struct {
				db    *gorm.DB
				query string
				args  []any
			}{
				{billingDB, "DELETE FROM manual_fee_review WHERE order_no = ?", []any{name}},
				{billingDB, "DELETE FROM fee_delivery WHERE charge_order_id IN (SELECT charge_order_id FROM fee_calculation WHERE order_no = ?)", []any{name}},
				{billingDB, "DELETE FROM fee_receipt WHERE calculation_no IN (SELECT calculation_no FROM fee_calculation WHERE order_no = ?)", []any{name}},
				{billingDB, "DELETE FROM fee_calculation WHERE order_no = ?", []any{name}},
				// 先按订单归属清理退款 outbox，再删除订单，避免失去定位夹具的关联记录。
				{userDB, "DELETE FROM event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json, '$.charge_order_id')) IN (SELECT CAST(id AS CHAR) FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_event_log WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_meter_review WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM refund_success_receipt WHERE refund_record_id IN (SELECT id FROM refund_record WHERE biz_id IN (SELECT id FROM charge_order WHERE order_no = ?))", []any{name}},
				{userDB, "DELETE FROM wallet_refund_part WHERE refund_record_id IN (SELECT id FROM refund_record WHERE biz_id IN (SELECT id FROM charge_order WHERE order_no = ?))", []any{name}},
				{userDB, "DELETE FROM refund_review WHERE refund_record_id IN (SELECT id FROM refund_record WHERE biz_id IN (SELECT id FROM charge_order WHERE order_no = ?))", []any{name}},
				{userDB, "DELETE FROM wallet_txn WHERE user_id IN (SELECT id FROM user WHERE openid = ?)", []any{name}},
				{userDB, "DELETE FROM refund_record WHERE biz_id IN (SELECT id FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_fee_receipt WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_end_receipt WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_billing_job WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_bill WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_order_pricing WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_prepay WHERE payment_order_id IN (SELECT id FROM payment_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_order WHERE order_no = ?", []any{name}},
				{userDB, "DELETE FROM payment_order WHERE order_no = ?", []any{name}},
				{userDB, "DELETE FROM user WHERE openid = ?", []any{name}},
			}
			for _, statement := range statements {
				if err := statement.db.Exec(statement.query, statement.args...).Error; err != nil {
					// 清理失败必须报告测试错误，防止残留夹具影响后续运行。
					t.Errorf("清理计费夹具失败 (%v): %s", err, statement.query)
				}
			}
		})
	}
	fixture := func(wh uint32, variable bool) (uint64, uint64) {
		name := "billing-" + uuid.NewString()
		cleanupBillingFixture(name)
		exec(userDB, "INSERT INTO user(openid) VALUES(?)", name)
		var uid uint64
		userDB.Table("user").Where("openid=?", name).Pluck("id", &uid)
		exec(userDB, "INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,wechat_transaction_id,status,created_month) VALUES(?,'charge',0,?,'wechat',250,250,?,'paid',?)", name, uid, "SIM-"+name, dbutil.MonthStart())
		var pid uint64
		userDB.Table("payment_order").Where("order_no=?", name).Pluck("id", &pid)
		start := time.Date(2026, 9, 29, 3, 30, 0, 0, time.UTC)
		end := start.Add(time.Hour)
		exec(userDB, "INSERT INTO charge_order(order_no,user_id,device_id,port_no,payment_order_id,status,started_at,ended_at,charged_kwh,charged_seconds,created_month) VALUES(?,?,'BILLING-TEST',1,?,'completed',?,?,?,3600,?)", name, uid, pid, start, end, fmt.Sprintf("%d.%03d", wh/1000, wh%1000), dbutil.MonthStart())
		var id uint64
		userDB.Table("charge_order").Where("order_no=?", name).Pluck("id", &id)
		exec(userDB, "UPDATE payment_order SET biz_id=? WHERE id=?", id, pid)
		scheme := pricing.Scheme{Name: "结算方案", Amount: &pricing.AmountMode{Algorithm: pricing.ModeServerEnergy, Periods: []pricing.Period{{EndMinute: 1440, ElectricCents: 50, ServiceCents: 25}}}, Packages: []pricing.Package{{ID: 1, Name: "2.5元", Mode: "amount", PriceCents: 250}}}.Normalized()
		if variable {
			scheme.Amount.Periods = []pricing.Period{{EndMinute: 720, ElectricCents: 50, ServiceCents: 25}, {EndMinute: 1440, ElectricCents: 100, ServiceCents: 25}}
		}
		rule := pricing.Rule{ID: 1, StationID: 1, Version: 1, Spec: scheme.SpecFor(scheme.Packages[0])}
		offer := scheme.Offers(rule)[0]
		snap, _ := json.Marshal(map[string]any{"rule": rule, "offer": offer})
		meter, _ := json.Marshal(orderpkg.EndMeter{ChargedWh: wh, ChargedSeconds: 3600, EndedAt: end})
		exec(userDB, "INSERT INTO charge_order_pricing(charge_order_id,payment_intent_id,user_id,port_code,pricing_snapshot) VALUES(?,?,?,?,?)", id, uuid.NewString(), uid, name, string(snap))
		exec(userDB, "INSERT INTO charge_end_receipt(charge_order_id,stop_command_id,meter_json) VALUES(?,?,?)", id, uuid.NewString(), string(meter))
		exec(userDB, "INSERT INTO charge_billing_job(charge_order_id) VALUES(?)", id)
		exec(userDB, "INSERT INTO charge_prepay(payment_order_id,params_json) VALUES(?,?)", pid, `{"provider":"simulation"}`)
		return id, pid
	}
	id, pid := fixture(1000, false)
	sourceOrders := scopedBillingOrders{DB: userDB, ID: id}
	store := billing.Store{DB: billingDB}
	source, err := sourceOrders.Read(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// 尽管按月分区，并发消费者共享的仍是同一张全局回执。
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { ; _, err := store.Calculate(ctx, source); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	service := billing.Service{Store: store, Orders: sourceOrders}
	if _, err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var fee struct{ ElectricCents, ServiceCents, TotalCents int64 }
	userDB.Table("charge_order").Where("id=?", id).Take(&fee)
	if fee.ElectricCents != 50 || fee.ServiceCents != 25 || fee.TotalCents != 75 {
		t.Fatalf("fee %+v", fee)
	}
	var count int64
	billingDB.Table("fee_calculation").Where("charge_order_id=?", id).Count(&count)
	if count != 1 {
		t.Fatalf("fees %d", count)
	}
	var refund refundpkg.RefundRecord
	if err := userDB.Where("payment_order_id=?", pid).Take(&refund).Error; err != nil || refund.RefundCents != 175 {
		t.Fatalf("refund %+v %v", refund, err)
	}
	result, err := store.Calculate(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	result.TotalCents++
	if err := sourceOrders.Apply(ctx, result); !errors.Is(err, billing.ErrConflict) {
		t.Fatalf("tampered result accepted: %v", err)
	}
	source.Rule.Spec.Electric.Periods[0].ServiceCents++
	if _, err := store.Calculate(ctx, source); !errors.Is(err, billing.ErrConflict) {
		t.Fatalf("modified snapshot accepted: %v", err)
	}
	// 模拟用户事务提交后计费确认丢失。
	exec(billingDB, "UPDATE fee_delivery SET delivered=0,scheduled_at=UTC_TIMESTAMP(3) WHERE charge_order_id=?", id)
	if _, err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	userDB.Model(&refundpkg.RefundRecord{}).Where("payment_order_id=?", pid).Count(&count)
	if count != 1 {
		t.Fatalf("duplicate refund %d", count)
	}
	executor := refundpkg.RefundExecutor{DB: userDB, Provider: channel.Simulator{}, ProviderName: "simulation"}
	if err := executor.Execute(ctx, refund.ID); err != nil {
		t.Fatal(err)
	}
	var orderStates []string
	userDB.Table("charge_order").Where("id=?", id).Pluck("status", &orderStates)
	if len(orderStates) != 1 || orderStates[0] != "completed" {
		t.Fatalf("partial refund must finish order: %v", orderStates)
	}
	reviewID, _ := fixture(1000, true)
	reviewService := billing.Service{Store: store, Orders: scopedBillingOrders{BillingOrders: settlementpkg.BillingOrders{DB: userDB}, ID: reviewID}}
	if _, err := reviewService.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var jobStates []string
	userDB.Table("charge_billing_job").Where("charge_order_id=?", reviewID).Pluck("status", &jobStates)
	if len(jobStates) != 1 || jobStates[0] != "manual_review" {
		t.Fatalf("missing meter not queued: %v", jobStates)
	}
	billingDB.Table("fee_calculation").Where("charge_order_id=?", reviewID).Count(&count)
	if count != 0 {
		t.Fatal("missing meter billed")
	}

	raw, err := sourceOrders.BillingOrders.Read(ctx, reviewID)
	if err != nil {
		t.Fatal(err)
	}
	split := raw.Meter.StartedAt.Add(30 * time.Minute)
	segments := []pricing.MeterSegment{{StartedAt: raw.Meter.StartedAt, EndedAt: split, EnergyWh: 200}, {StartedAt: split, EndedAt: raw.Meter.EndedAt, EnergyWh: 800}}
	reviewStore := sourceOrders.BillingOrders
	requestID := uuid.NewString()
	proposal, err := reviewStore.ProposeMeter(ctx, reviewID, 101, requestID, "设备分段读数人工核实", segments)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := reviewStore.ProposeMeter(ctx, reviewID, 101, requestID, "设备分段读数人工核实", segments)
	if err != nil || replay.ID != proposal.ID {
		t.Fatalf("proposal replay %+v %v", replay, err)
	}
	if _, err := reviewStore.ProposeMeter(ctx, reviewID, 101, requestID, "changed", segments); !errors.Is(err, billing.ErrConflict) {
		t.Fatal("changed replay accepted")
	}
	if err := reviewStore.ReviewMeter(ctx, reviewID, proposal.ID, 101, true, ""); !errors.Is(err, billing.ErrConflict) {
		t.Fatal("self approval accepted")
	}
	if _, err := reviewStore.ProposeMeter(ctx, reviewID, 102, uuid.NewString(), "duplicate", segments); !errors.Is(err, billing.ErrConflict) {
		t.Fatal("parallel proposal accepted")
	}
	if err := reviewStore.ReviewMeter(ctx, reviewID, proposal.ID, 102, false, "依据不充分"); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.ReviewMeter(ctx, reviewID, proposal.ID, 103, true, ""); !errors.Is(err, billing.ErrConflict) {
		t.Fatal("rejected proposal approved")
	}
	proposal, err = reviewStore.ProposeMeter(ctx, reviewID, 101, uuid.NewString(), "补充分时仪表读数", segments)
	if err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.ReviewMeter(ctx, reviewID, proposal.ID, 102, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.ReviewMeter(ctx, reviewID, proposal.ID, 102, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := reviewService.Run(ctx); err != nil {
		t.Fatal(err)
	}
	userDB.Table("charge_order").Where("id=?", reviewID).Take(&fee)
	if fee.ElectricCents != 90 || fee.ServiceCents != 25 || fee.TotalCents != 115 {
		t.Fatalf("corrected measured fee %+v", fee)
	}
	// Pluck 要求目标是切片；用它去扫一个标量会失败。
	var reviewStates []string
	billingDB.Table("manual_fee_review").Where("charge_order_id=?", reviewID).Pluck("status", &reviewStates)
	if len(reviewStates) != 1 || reviewStates[0] != "resolved" {
		t.Fatalf("review not resolved %v", reviewStates)
	}
	var endReceipt orderpkg.EndReceiptRecord
	userDB.Where("charge_order_id=?", reviewID).Take(&endReceipt)
	var originalMeter orderpkg.EndMeter
	json.Unmarshal(endReceipt.MeterJSON, &originalMeter)
	if len(originalMeter.Segments) != 0 {
		t.Fatal("original meter overwritten")
	}
	debtID, debtPayment := fixture(6000, false)
	debtService := billing.Service{Store: store, Orders: scopedBillingOrders{BillingOrders: settlementpkg.BillingOrders{DB: userDB}, ID: debtID}}
	if _, err := debtService.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var shortfall int64
	userDB.Table("charge_fee_receipt").Where("charge_order_id=?", debtID).Pluck("shortfall_cents", &shortfall)
	if shortfall != 0 {
		t.Fatalf("shortfall %d", shortfall)
	}
	userDB.Model(&refundpkg.RefundRecord{}).Where("payment_order_id=?", debtPayment).Count(&count)
	if count != 0 {
		t.Fatal("underpaid order refunded")
	}
}
