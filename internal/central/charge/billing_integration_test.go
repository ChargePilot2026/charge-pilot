package charge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type scopedBillingOrders struct {
	BillingOrders
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
	// cleanupBillingFixture 摘掉一条 fixture 留下的全部痕迹。
	//
	// 这条链从 user 一路铺到 billing_db：计费单、收款凭据、投递记录、人工
	// 复核单、退款单都在外面。一次跑测留下的是一条永远不会被结算掉的计费
	// 单——SettlementsDue 按创建时间取最老的一批待结算，几十条这样的孤儿
	// 攒下来，会把后来真正要结算的计费单挤出取数窗口，结算包的测试开始
	// 随机失败，而症状看上去像是排序写错了。
	//
	// 删除顺序与写入顺序相反：先摘按订单号认的计费侧，再摘按支付单/订单
	// 认的用户侧，最后才轮到用户本身。少删一张，孤儿行就顶住下一轮的唯一键。
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
				// 退款会往 event_outbox 写两条事件（退款请求、退款成功）。outbox
				// 是只追加的，没有外键，订单删了它还在，于是每跑一次就多两条，
				// 而它们的 event_id 由退款单号推出，重复触发会撞唯一键。这句
				// 必须在删订单之前跑——子查询还要靠订单行认人。
				{userDB, "DELETE FROM event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json, '$.charge_order_id')) IN (SELECT CAST(id AS CHAR) FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_event_log WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_meter_review WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_debt_receipt WHERE payment_order_id IN (SELECT id FROM payment_order WHERE order_no = ?)", []any{name}},
				{userDB, "DELETE FROM charge_debt WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no = ?)", []any{name}},
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
					// 清理失败必须喊出来：静默吞掉的话，垃圾会一直留在库里，
					// 下一轮测试再被它绊倒，而症状指向完全无关的地方。
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
		exec(userDB, "INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,wechat_transaction_id,status,created_month) VALUES(?,'charge',0,?,'wechat',250,250,?,'paid',?)", name, uid, "SIM-"+name, utcDate())
		var pid uint64
		userDB.Table("payment_order").Where("order_no=?", name).Pluck("id", &pid)
		start := time.Date(2026, 9, 29, 3, 30, 0, 0, time.UTC)
		end := start.Add(time.Hour)
		exec(userDB, "INSERT INTO charge_order(order_no,user_id,device_id,port_no,payment_order_id,status,started_at,ended_at,charged_kwh,charged_seconds,created_month) VALUES(?,?,'BILLING-TEST',1,?,'completed',?,?,?,3600,?)", name, uid, pid, start, end, fmt.Sprintf("%d.%03d", wh/1000, wh%1000), utcDate())
		var id uint64
		userDB.Table("charge_order").Where("order_no=?", name).Pluck("id", &id)
		exec(userDB, "UPDATE payment_order SET biz_id=? WHERE id=?", id, pid)
		rule := pricing.Rule{ID: 1, StationID: 1, Version: 1, Spec: pricing.Spec{Mode: pricing.ModeServerEnergy, Electric: &pricing.ElectricLine{Basis: pricing.BasisEnergy, Periods: []pricing.Period{{EndMinute: 1440, ElectricCents: 50}}}, Service: &pricing.ServiceLine{Basis: pricing.ServiceEnergy, CentsPerKWh: 25}}}
		if variable {
			rule.Spec.Electric.Periods = []pricing.Period{{EndMinute: 720, ElectricCents: 50}, {EndMinute: 1440, ElectricCents: 100}}
		}
		snap, _ := json.Marshal(map[string]any{"rule": rule})
		meter, _ := json.Marshal(EndMeter{ChargedWh: wh, ChargedSeconds: 3600, EndedAt: end})
		exec(userDB, "INSERT INTO charge_order_pricing(charge_order_id,payment_intent_id,user_id,port_code,pricing_snapshot) VALUES(?,?,?,?,?)", id, uuid.NewString(), uid, name, string(snap))
		exec(userDB, "INSERT INTO charge_end_receipt(charge_order_id,stop_command_id,meter_json) VALUES(?,?,?)", id, uuid.NewString(), string(meter))
		exec(userDB, "INSERT INTO charge_billing_job(charge_order_id) VALUES(?)", id)
		exec(userDB, "INSERT INTO charge_prepay(payment_order_id,params_json) VALUES(?,?)", pid, `{"provider":"simulation"}`)
		return id, pid
	}
	id, pid := fixture(1000, false)
	sourceOrders := scopedBillingOrders{BillingOrders: BillingOrders{DB: userDB}, ID: id}
	store := billing.Store{DB: billingDB}
	source, err := sourceOrders.Read(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Concurrent consumers share one global receipt, despite monthly partitions.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.Calculate(ctx, source); errs <- err }()
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
	var refund RefundRecord
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
	source.Rule.Spec.Service.CentsPerKWh++
	if _, err := store.Calculate(ctx, source); !errors.Is(err, billing.ErrConflict) {
		t.Fatalf("modified snapshot accepted: %v", err)
	}
	// Simulate loss of the billing acknowledgement after the user transaction.
	exec(billingDB, "UPDATE fee_delivery SET delivered=0,scheduled_at=UTC_TIMESTAMP(3) WHERE charge_order_id=?", id)
	if _, err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	userDB.Model(&RefundRecord{}).Where("payment_order_id=?", pid).Count(&count)
	if count != 1 {
		t.Fatalf("duplicate refund %d", count)
	}
	executor := RefundExecutor{DB: userDB, Provider: payment.Simulator{}, ProviderName: "simulation"}
	if err := executor.Execute(ctx, refund.ID); err != nil {
		t.Fatal(err)
	}
	var orderStates []string
	userDB.Table("charge_order").Where("id=?", id).Pluck("status", &orderStates)
	if len(orderStates) != 1 || orderStates[0] != "completed" {
		t.Fatalf("partial refund must finish order: %v", orderStates)
	}
	reviewID, _ := fixture(1000, true)
	reviewService := billing.Service{Store: store, Orders: scopedBillingOrders{BillingOrders: BillingOrders{DB: userDB}, ID: reviewID}}
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
	// Pluck requires a slice destination; scanning a scalar through it fails.
	var reviewStates []string
	billingDB.Table("manual_fee_review").Where("charge_order_id=?", reviewID).Pluck("status", &reviewStates)
	if len(reviewStates) != 1 || reviewStates[0] != "resolved" {
		t.Fatalf("review not resolved %v", reviewStates)
	}
	var endReceipt EndReceiptRecord
	userDB.Where("charge_order_id=?", reviewID).Take(&endReceipt)
	var originalMeter EndMeter
	json.Unmarshal(endReceipt.MeterJSON, &originalMeter)
	if len(originalMeter.Segments) != 0 {
		t.Fatal("original meter overwritten")
	}
	debtID, debtPayment := fixture(6000, false)
	debtService := billing.Service{Store: store, Orders: scopedBillingOrders{BillingOrders: BillingOrders{DB: userDB}, ID: debtID}}
	if _, err := debtService.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var shortfall int64
	userDB.Table("charge_fee_receipt").Where("charge_order_id=?", debtID).Pluck("shortfall_cents", &shortfall)
	if shortfall != 200 {
		t.Fatalf("shortfall %d", shortfall)
	}
	userDB.Model(&RefundRecord{}).Where("payment_order_id=?", debtPayment).Count(&count)
	if count != 0 {
		t.Fatal("underpaid order refunded")
	}
}
