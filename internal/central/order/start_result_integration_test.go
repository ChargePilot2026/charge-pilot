package order_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	orderpkg "github.com/ChargePilot2026/charge-pilot/internal/central/order"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestStartResultChangesPaidOrderOnlyOnce(t *testing.T) {
	url := os.Getenv("TEST_USER_DATABASE_URL")
	if url == "" {
		t.Skip("set disposable user database URL")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	unique := uuid.NewString()
	month := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	user, err := db.ExecContext(ctx, "INSERT INTO user (openid) VALUES (?)", "go-start-result-"+unique)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	defer db.ExecContext(ctx, "DELETE FROM user WHERE id = ?", userID)
	orderNo := "ORD-" + unique
	paid, err := db.ExecContext(ctx, "INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,status,paid_at,created_month) VALUES(?,'charge',0,?,'wechat',100,100,'paid',UTC_TIMESTAMP(3),?)", "PAY-"+unique, userID, month)
	if err != nil {
		t.Fatal(err)
	}
	paymentID, _ := paid.LastInsertId()
	defer db.ExecContext(ctx, "DELETE FROM payment_order WHERE id=?", paymentID)
	order, err := db.ExecContext(ctx, "INSERT INTO charge_order (order_no,user_id,device_id,port_no,status,created_month,charge_mode,charge_quantity,payment_order_id) VALUES (?,?,?,1,'paid',?,4,600,?)", orderNo, userID, "BOARD-TEST", month, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	orderID, _ := order.LastInsertId()
	defer db.ExecContext(ctx, "DELETE FROM charge_order WHERE id = ? AND created_month = ?", orderID, month)
	defer db.ExecContext(ctx, "DELETE FROM active_port_charge WHERE charge_order_id = ?", orderID)
	defer db.ExecContext(ctx, "DELETE FROM charge_event_log WHERE charge_order_id = ?", orderID)
	commandID := uuid.NewString()
	defer db.ExecContext(ctx, "DELETE FROM event_outbox WHERE event_id = ?", commandID)
	defer db.ExecContext(ctx, "DELETE FROM charge_start_receipt WHERE command_id = ?", commandID)
	result := orderpkg.StartResult{CommandID: commandID, ChargeOrderID: uint64(orderID), OrderNo: orderNo, DeviceID: "BOARD-TEST", PortNo: 1, PortID: uint64(orderID) + 1000000, Success: true, ResultCode: 0, OccurredAt: time.Now().UTC()}
	store := orderpkg.StartResultStore{DB: testGORMDB(t, db)}
	replay, err := store.Apply(ctx, result)
	if err != nil || replay {
		t.Fatalf("first result: replay=%v err=%v", replay, err)
	}
	replay, err = store.Apply(ctx, result)
	if err != nil || !replay {
		t.Fatalf("duplicate result: replay=%v err=%v", replay, err)
	}
	var status string
	if err := db.QueryRowContext(ctx, "SELECT status FROM charge_order WHERE id = ? AND created_month = ?", orderID, month).Scan(&status); err != nil || status != "charging" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	var active, events, outbox int
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM active_port_charge WHERE charge_order_id = ?", orderID).Scan(&active)
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM charge_event_log WHERE charge_order_id = ?", orderID).Scan(&events)
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM event_outbox WHERE event_id = ?", commandID).Scan(&outbox)
	if active != 1 || events != 1 || outbox != 1 {
		t.Fatalf("active=%d events=%d outbox=%d", active, events, outbox)
	}
	result.Success, result.ResultCode = false, 1
	if _, err := store.Apply(ctx, result); !errors.Is(err, orderpkg.ErrStartResultConflict) {
		t.Fatalf("conflicting replay accepted: %v", err)
	}
	stopID := uuid.NewString()
	defer db.ExecContext(ctx, "DELETE FROM event_outbox WHERE event_id = ?", stopID)
	defer db.ExecContext(ctx, "DELETE FROM charge_end_receipt WHERE stop_command_id = ?", stopID)
	end := orderpkg.EndResult{OrderNo: orderNo, ChargeOrderID: uint64(orderID), StartCommandID: commandID,
		StopCommandID: stopID, DeviceID: "BOARD-TEST", PortNo: 1, PortID: uint64(orderID) + 1000000,
		Meter: orderpkg.EndMeter{ChargedWh: 125, ChargedSeconds: 600, EndedAt: time.Now().UTC().Add(time.Minute)}}
	ended, err := (orderpkg.EndResultStore{DB: testGORMDB(t, db)}).Apply(ctx, end)
	if err != nil || ended {
		t.Fatalf("first end: replay=%v err=%v", ended, err)
	}
	ended, err = (orderpkg.EndResultStore{DB: testGORMDB(t, db)}).Apply(ctx, end)
	if err != nil || !ended {
		t.Fatalf("end replay: replay=%v err=%v", ended, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT status FROM charge_order WHERE id = ? AND created_month = ?", orderID, month).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("end status=%s err=%v", status, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM active_port_charge WHERE charge_order_id = ?", orderID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("active after end=%d err=%v", active, err)
	}
	end.Meter.ChargedWh++
	if _, err := (orderpkg.EndResultStore{DB: testGORMDB(t, db)}).Apply(ctx, end); !errors.Is(err, orderpkg.ErrEndResultConflict) {
		t.Fatalf("changed meter accepted: %v", err)
	}
}
