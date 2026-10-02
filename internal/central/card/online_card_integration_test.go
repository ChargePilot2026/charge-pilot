package card_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/card"
	orderpkg "github.com/ChargePilot2026/charge-pilot/internal/central/order"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// testGORMDB 与 charge 包测试内的同名助手保持一致；两边独立维护，
// 因本测试为 external test 包，无法复用 charge 的内部测试助手。
func testGORMDB(t *testing.T, db *sql.DB) *gorm.DB {
	t.Helper()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

func TestCardServerExtensionDebitsOnceAndRefundsUnusedMinutes(t *testing.T) {
	url := os.Getenv("TEST_USER_DATABASE_URL")
	if url == "" {
		t.Skip("set a migrated disposable user MySQL database")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	orm := testGORMDB(t, db)
	create := func(query string, args ...any) uint64 {
		t.Helper()
		result, err := db.ExecContext(ctx, query, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return uint64(id)
	}
	user := create("INSERT INTO user(openid,status) VALUES(?,'active')", "card-test-"+uuid.NewString())
	device := "card-" + uuid.NewString()
	portCode := device + ":1"
	t.Cleanup(func() {
		tables := []string{"card_operation", "card_charge", "charge_manual_settlement", "charge_billing_cutoff", "charge_fee_receipt", "charge_end_receipt", "charge_start_receipt", "charge_billing_job", "charge_event_log", "charge_order_pricing", "active_port_charge"}
		for _, table := range tables {
			if err := orm.Exec("DELETE FROM "+table+" WHERE charge_order_id IN (SELECT id FROM charge_order WHERE user_id=?)", user).Error; err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
		statements := []string{"DELETE FROM event_outbox WHERE JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.user_id'))=? OR JSON_UNQUOTE(JSON_EXTRACT(envelope_json,'$.charge_order_id')) IN (SELECT CAST(id AS CHAR) FROM charge_order WHERE user_id=?)", "DELETE FROM refund_record WHERE user_id=?", "DELETE FROM charge_payment_intent WHERE user_id=?", "DELETE FROM charge_order WHERE user_id=?", "DELETE FROM payment_order WHERE user_id=?", "DELETE FROM wallet_txn WHERE user_id=?", "DELETE FROM wallet_account WHERE user_id=?", "DELETE FROM online_card WHERE user_id=?", "DELETE FROM user WHERE id=?"}
		for i, query := range statements {
			args := []any{user}
			if i == 0 {
				args = append(args, user)
			}
			if err := orm.Exec(query, args...).Error; err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	create("INSERT INTO wallet_account(user_id,balance_cents,status) VALUES(?,1000,'active')", user)
	number := uuid.New()
	cardNo := strconv.FormatUint(uint64(binary.LittleEndian.Uint32(number[:4])|1), 10)
	cardRow := card.OnlineCard{CardNo: cardNo, UserID: user, Status: "active"}
	if err := orm.Create(&cardRow).Error; err != nil {
		t.Fatal(err)
	}
	s := pricing.Scheme{Name: "服务器刷卡测试", Packages: []pricing.Package{{ID: 1, Name: "120分钟", Mode: "duration", PriceCents: 200, Minutes: 120}}, Card: pricing.CardPolicy{PackageID: 1, MaxMinutes: 600}, Display: pricing.DefaultDisplay()}.Normalized()
	rule := pricing.Rule{ID: 31, StationID: 9, Version: 1, Spec: s.SpecFor(s.Packages[0])}
	port := card.PortRef{Found: true, Kind: "port", DeviceID: device, StationID: 9, PortID: portCode, PortNo: 1, Online: true, Available: true}
	store := card.CardStore{DB: orm}
	event := uuid.NewString()
	first, err := store.Swipe(ctx, cardNo, event, port, rule)
	if err != nil {
		t.Fatal(err)
	}
	assertConcurrentReplay := func(event string, expected card.CardOperation) {
		t.Helper()
		var group sync.WaitGroup
		for range 5 {
			group.Go(func() {
				op, err := store.Swipe(ctx, cardNo, event, port, pricing.Rule{})
				if err != nil || op.OperationID != expected.OperationID {
					t.Errorf("replay %+v %v", op, err)
				}
			})
		}
		group.Wait()
	}
	assertConcurrentReplay(event, first)
	var order orderpkg.ChargeOrderRecord
	if err := orm.Where("id=?", first.ChargeOrderID).Take(&order).Error; err != nil {
		t.Fatal(err)
	}
	if order.ChargeMode != 4 || order.ChargeQuantity != 600 || first.Status != "confirming" {
		t.Fatalf("order %+v operation %+v", order, first)
	}
	startID := uuid.NewString()
	started := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	result := orderpkg.StartResult{CommandID: startID, ChargeOrderID: order.ID, OrderNo: order.OrderNo, DeviceID: device, PortNo: 1, PortID: user + 900000, Success: true, OccurredAt: started}
	if _, err := (orderpkg.StartResultStore{DB: orm}).Apply(ctx, result); err != nil {
		t.Fatal(err)
	}
	port.Available = false
	extensionEvent := uuid.NewString()
	extension, err := store.Swipe(ctx, cardNo, extensionEvent, port, pricing.Rule{})
	if err != nil || extension.Status != "confirmed" || extension.Kind != "extend" {
		t.Fatalf("%+v %v", extension, err)
	}
	assertConcurrentReplay(extensionEvent, extension)
	var session card.CardCharge
	if err := orm.Where("charge_order_id=?", order.ID).Take(&session).Error; err != nil {
		t.Fatal(err)
	}
	var balance int64
	orm.Table("wallet_account").Where("user_id=?", user).Pluck("balance_cents", &balance)
	if session.PurchasedMinutes != 240 || session.PaidCents != 400 || balance != 600 {
		t.Fatalf("%+v balance=%d", session, balance)
	}
	var storedOrder orderpkg.ChargeOrderRecord
	if err := orm.Where("id=?", order.ID).Take(&storedOrder).Error; err != nil || storedOrder.BusinessStatus != "charging" || storedOrder.PaymentStatus != "paid" {
		t.Fatalf("card extension statuses: %+v err=%v", storedOrder, err)
	}
	// The new current scheme is absent; committed event replay still succeeds.
	if err := orm.Model(&cardRow).Where("id=?", cardRow.ID).Update("status", "lost").Error; err != nil {
		t.Fatal(err)
	}
	assertConcurrentReplay(extensionEvent, extension)
	if _, err := store.Swipe(ctx, cardNo, uuid.NewString(), port, rule); !errors.Is(err, card.ErrCardOperation) {
		t.Fatalf("lost card: %v", err)
	}
	end := orderpkg.EndResult{OrderNo: order.OrderNo, ChargeOrderID: order.ID, StartCommandID: startID, StopCommandID: uuid.NewString(), DeviceID: device, PortNo: 1, PortID: result.PortID, Meter: orderpkg.EndMeter{EndedAt: started.Add(150 * time.Minute), ChargedSeconds: 150 * 60, ChargedWh: 1000}}
	if _, err := (orderpkg.EndResultStore{DB: orm}).Apply(ctx, end); err != nil {
		t.Fatal(err)
	}
	orders := settlement.BillingOrders{DB: orm}
	source, err := orders.Read(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	fee, err := billing.PriceSource(source)
	if err != nil || fee.TotalCents != 250 {
		t.Fatalf("%+v %v", fee, err)
	}
	receipt := billing.Result{CalculationNo: billing.CalculationNumber(order.OrderNo, order.ID), Source: source, ActualFee: fee}
	for range 2 {
		if err := orders.Apply(ctx, receipt); err != nil {
			t.Fatal(err)
		}
	}
	orm.Table("wallet_account").Where("user_id=?", user).Pluck("balance_cents", &balance)
	if balance != 750 {
		t.Fatalf("want final balance 750 after 150 refund, got %d", balance)
	}
	var debits, refunds int64
	orm.Table("wallet_txn").Where("user_id=? AND direction='out'", user).Count(&debits)
	orm.Table("wallet_txn").Where("user_id=? AND direction='in'", user).Count(&refunds)
	if debits != 2 || refunds != 1 {
		t.Fatalf("debits=%d refunds=%d", debits, refunds)
	}
	if err := orm.Where("id=?", order.ID).Take(&storedOrder).Error; err != nil || storedOrder.BusinessStatus != "completed" || storedOrder.PaymentStatus != "partial_refunded" {
		t.Fatalf("settled card partial refund statuses: %+v err=%v", storedOrder, err)
	}
}
