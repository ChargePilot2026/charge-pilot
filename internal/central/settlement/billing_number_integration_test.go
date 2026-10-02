package settlement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	orderpkg "github.com/ChargePilot2026/charge-pilot/internal/central/order"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/billing"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Every write remains inside these two rollback transactions. In particular,
// fee delivery and refund outbox fixtures never become visible to local workers.
func TestBillingNumbersRoundTripNewAndHistoricOrders(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_BILLING_DATABASE_URL") == "" {
		t.Skip("MySQL user and billing databases required")
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
	for _, kind := range []string{"C", "numeric", "CH"} {
		t.Run(kind, func(t *testing.T) {
			userTx, billingTx := userDB.Begin(), billingDB.Begin()
			if userTx.Error != nil || billingTx.Error != nil {
				t.Fatalf("begin fixture transactions: %v %v", userTx.Error, billingTx.Error)
			}
			t.Cleanup(func() { userTx.Rollback(); billingTx.Rollback() })
			exec := func(db *gorm.DB, query string, args ...any) {
				t.Helper()
				if err := db.Exec(query, args...).Error; err != nil {
					t.Fatal(err)
				}
			}
			tag := strings.ReplaceAll(uuid.NewString(), "-", "")
			start := time.Date(2026, 10, 1, 2, 30, 0, 0, time.UTC)
			end := start.Add(time.Hour)
			orderNo, err := orderpkg.ChargeOrderNumber(start, tag, 1)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "numeric" {
				orderNo = orderNo[1:]
			} else if kind == "CH" {
				orderNo = "CH" + tag
			}
			// No user row or existing account is modified for this numbering test.
			userID := uint64(9223372036854000000)
			payment := payment.PaymentOrderRecord{OrderNo: "number-pay-" + tag, BizType: "charge", UserID: userID,
				PayMethod: "wechat", TotalCents: 100, PaidCents: 100, Status: "paid", CreatedMonth: dbutil.MonthStart()}
			if err := userTx.Create(&payment).Error; err != nil {
				t.Fatal(err)
			}
			order := orderpkg.ChargeOrderRecord{OrderNo: orderNo, UserID: userID, DeviceID: tag, PortNo: 1,
				PaymentOrderID: sql.NullInt64{Int64: int64(payment.ID), Valid: true}, Status: "completed", PaymentStatus: "paid",
				StartedAt: sql.NullTime{Time: start, Valid: true}, EndedAt: sql.NullTime{Time: end, Valid: true},
				ChargedKWh: sql.NullString{String: "1.000", Valid: true}, ChargedSeconds: sql.NullInt64{Int64: 3600, Valid: true}, CreatedMonth: dbutil.MonthStart()}
			if err := userTx.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			exec(userTx, "UPDATE payment_order SET biz_id=? WHERE id=?", order.ID, payment.ID)
			scheme := pricing.Scheme{Name: "numbering fixture", Amount: &pricing.AmountMode{
				Algorithm: pricing.ModeServerEnergy, Periods: []pricing.Period{{EndMinute: 1440, ElectricCents: 50, ServiceCents: 25}}},
				Packages: []pricing.Package{{ID: 1, Name: "1元", Mode: "amount", PriceCents: 100}}}.Normalized()
			rule := pricing.Rule{ID: 1, StationID: 1, Version: 1, Spec: scheme.SpecFor(scheme.Packages[0])}
			offer := scheme.Offers(rule)[0]
			snapshot, _ := json.Marshal(map[string]any{"rule": rule, "offer": offer})
			meter, _ := json.Marshal(orderpkg.EndMeter{ChargedWh: 1000, ChargedSeconds: 3600, EndedAt: end})
			exec(userTx, "INSERT INTO charge_order_pricing(charge_order_id,payment_intent_id,user_id,port_code,pricing_snapshot) VALUES(?,?,?,?,?)", order.ID, uuid.NewString(), userID, tag, string(snapshot))
			exec(userTx, "INSERT INTO charge_end_receipt(charge_order_id,stop_command_id,meter_json) VALUES(?,?,?)", order.ID, uuid.NewString(), string(meter))
			exec(userTx, "INSERT INTO charge_billing_job(charge_order_id) VALUES(?)", order.ID)
			orders, store := BillingOrders{DB: userTx}, billing.Store{DB: billingTx}
			source, err := orders.Read(ctx, order.ID)
			if err != nil {
				t.Fatal(err)
			}
			result, err := store.Calculate(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("FEE%020d", order.ID)
			if kind == "C" {
				want = "B" + orderNo[1:]
			}
			if result.CalculationNo != want || result.TotalCents != 75 {
				t.Fatalf("calculation identity or fee: %+v, want %s", result, want)
			}
			if err := orders.Apply(ctx, result); err != nil {
				t.Fatal(err)
			}
			// Read the durable pending payload rather than constructing a new result:
			// delivery retries must preserve both old FEE and new B identities.
			var delivery struct{ PayloadJSON []byte }
			if err := billingTx.Table("fee_delivery").Where("charge_order_id=?", order.ID).Take(&delivery).Error; err != nil {
				t.Fatal(err)
			}
			var replay billing.Result
			if err := json.Unmarshal(delivery.PayloadJSON, &replay); err != nil {
				t.Fatal(err)
			}
			if replay.CalculationNo != want {
				t.Fatalf("delivery changed identity: %s, want %s", replay.CalculationNo, want)
			}
			if err := orders.Apply(ctx, replay); err != nil {
				t.Fatalf("delivery replay: %v", err)
			}
			if again, err := store.Calculate(ctx, source); err != nil || again.CalculationNo != want {
				t.Fatalf("calculation replay changed identity: %+v %v", again, err)
			}
			var receipt struct{ CalculationNo string }
			if err := userTx.Table("charge_fee_receipt").Where("charge_order_id=?", order.ID).Take(&receipt).Error; err != nil || receipt.CalculationNo != want {
				t.Fatalf("user fee receipt identity: %+v %v", receipt, err)
			}
			for table, condition := range map[string]string{"charge_event_log": "charge_order_id=? AND event='fee_calculated'", "refund_record": "biz_id=? AND biz_type='charge'"} {
				var count int64
				if err := userTx.Table(table).Where(condition, order.ID).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("replay duplicated %s: count=%d err=%v", table, count, err)
				}
			}
			replay.CalculationNo = "FEE-wrong-order"
			if err := orders.Apply(ctx, replay); !errors.Is(err, billing.ErrConflict) {
				t.Fatalf("unrelated calculation number accepted: %v", err)
			}
		})
	}
}
