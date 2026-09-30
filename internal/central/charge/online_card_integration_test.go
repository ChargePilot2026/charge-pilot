package charge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

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
	defer db.Close()
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
		if err := orm.Exec("DELETE FROM charge_port_lock WHERE port_code=?", portCode).Error; err != nil {
			t.Error(err)
		}
	})
	create("INSERT INTO wallet_account(user_id,balance_cents,status) VALUES(?,1000,'active')", user)
	number := uuid.New()
	cardNo := strconv.FormatUint(uint64(binary.LittleEndian.Uint32(number[:4])|1), 10)
	card := OnlineCard{CardNo: cardNo, UserID: user, Status: "active"}
	if err := orm.Create(&card).Error; err != nil {
		t.Fatal(err)
	}
	s := pricing.Scheme{Name: "服务器刷卡测试", Packages: []pricing.Package{{ID: 1, Name: "120分钟", Mode: "duration", PriceCents: 200, Minutes: 120}}, Card: pricing.CardPolicy{PackageID: 1, MaxMinutes: 600}, Display: pricing.DefaultDisplay()}.Normalized()
	rule := pricing.Rule{ID: 31, StationID: 9, Version: 1, Spec: s.SpecFor(s.Packages[0])}
	port := ScanResult{Kind: "port", DeviceID: device, StationID: 9, Port: &ScanPort{PortID: portCode, DeviceID: device, PortNo: 1, Online: true, Available: true}}
	store := CardStore{DB: orm}
	event := uuid.NewString()
	first, err := store.Swipe(ctx, cardNo, event, port, rule)
	if err != nil {
		t.Fatal(err)
	}
	assertConcurrentReplay := func(event string, expected CardOperation) {
		t.Helper()
		var group sync.WaitGroup
		for i := 0; i < 5; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				op, err := store.Swipe(ctx, cardNo, event, port, pricing.Rule{})
				if err != nil || op.OperationID != expected.OperationID {
					t.Errorf("replay %+v %v", op, err)
				}
			}()
		}
		group.Wait()
	}
	assertConcurrentReplay(event, first)
	var order ChargeOrderRecord
	if err := orm.Where("id=?", first.ChargeOrderID).Take(&order).Error; err != nil {
		t.Fatal(err)
	}
	if order.ChargeMode != 4 || order.ChargeQuantity != 600 || first.Status != "confirming" {
		t.Fatalf("order %+v operation %+v", order, first)
	}
	startID := uuid.NewString()
	started := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	result := StartResult{CommandID: startID, ChargeOrderID: order.ID, OrderNo: order.OrderNo, DeviceID: device, PortNo: 1, PortID: user + 900000, Success: true, OccurredAt: started}
	if _, err := (StartResultStore{DB: orm}).Apply(ctx, result); err != nil {
		t.Fatal(err)
	}
	port.Port.Available = false
	extensionEvent := uuid.NewString()
	extension, err := store.Swipe(ctx, cardNo, extensionEvent, port, pricing.Rule{})
	if err != nil || extension.Status != "confirmed" || extension.Kind != "extend" {
		t.Fatalf("%+v %v", extension, err)
	}
	assertConcurrentReplay(extensionEvent, extension)
	var session CardCharge
	if err := orm.Where("charge_order_id=?", order.ID).Take(&session).Error; err != nil {
		t.Fatal(err)
	}
	var balance int64
	orm.Table("wallet_account").Where("user_id=?", user).Pluck("balance_cents", &balance)
	if session.PurchasedMinutes != 240 || session.PaidCents != 400 || balance != 600 {
		t.Fatalf("%+v balance=%d", session, balance)
	}
	// The new current scheme is absent; committed event replay still succeeds.
	if err := orm.Model(&OnlineCard{}).Where("id=?", card.ID).Update("status", "lost").Error; err != nil {
		t.Fatal(err)
	}
	assertConcurrentReplay(extensionEvent, extension)
	if _, err := store.Swipe(ctx, cardNo, uuid.NewString(), port, rule); !errors.Is(err, ErrCardOperation) {
		t.Fatalf("lost card: %v", err)
	}
	end := EndResult{OrderNo: order.OrderNo, ChargeOrderID: order.ID, StartCommandID: startID, StopCommandID: uuid.NewString(), DeviceID: device, PortNo: 1, PortID: result.PortID, Meter: EndMeter{EndedAt: started.Add(150 * time.Minute), ChargedSeconds: 150 * 60, ChargedWh: 1000}}
	if _, err := (EndResultStore{DB: orm}).Apply(ctx, end); err != nil {
		t.Fatal(err)
	}
	orders := BillingOrders{DB: orm}
	source, err := orders.Read(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	fee, err := billing.PriceSource(source)
	if err != nil || fee.TotalCents != 250 {
		t.Fatalf("%+v %v", fee, err)
	}
	receipt := billing.Result{CalculationNo: fmt.Sprintf("FEE%020d", order.ID), Source: source, ActualFee: fee}
	for i := 0; i < 2; i++ {
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
}
