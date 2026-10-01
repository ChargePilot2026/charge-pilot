package outbox

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestRefundResultsPersistPartialAndFullOrderPaymentStates(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" {
		t.Skip("MySQL with current order state columns required")
	}
	ctx := context.Background()
	conn, err := dbconn.Open(ctx, os.Getenv("TEST_USER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	db, err := dbconn.WrapGORM(conn)
	if err != nil {
		t.Fatal(err)
	}
	tag := uuid.NewString()
	user := struct {
		ID     uint64
		Openid string
	}{Openid: "worker-states-" + tag}
	var payment charge.PaymentOrderRecord
	var order charge.ChargeOrderRecord
	var refunds []charge.RefundRecord
	t.Cleanup(func() {
		cleanup := func(query string, args ...any) {
			if err := db.Exec(query, args...).Error; err != nil {
				t.Errorf("worker state fixture cleanup: %v", err)
			}
		}
		for _, refund := range refunds {
			cleanup("DELETE FROM refund_success_receipt WHERE refund_record_id=?", refund.ID)
			cleanup("DELETE FROM refund_record WHERE id=? AND refund_no=?", refund.ID, refund.RefundNo)
		}
		cleanup("DELETE FROM charge_order WHERE id=? AND order_no=?", order.ID, order.OrderNo)
		cleanup("DELETE FROM payment_order WHERE id=? AND order_no=?", payment.ID, payment.OrderNo)
		cleanup("DELETE FROM user WHERE id=? AND openid=?", user.ID, user.Openid)
	})
	if err := db.Table("user").Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	payment = charge.PaymentOrderRecord{OrderNo: "worker-pay-" + tag, BizType: "charge", UserID: user.ID, PayMethod: "wechat", TotalCents: 1000, PaidCents: 1000, Status: "paid", CreatedMonth: month}
	if err := db.Create(&payment).Error; err != nil {
		t.Fatal(err)
	}
	order = charge.ChargeOrderRecord{OrderNo: "worker-order-" + tag, UserID: user.ID, DeviceID: "worker-states", PortNo: 1, Status: "completed", PaymentStatus: "paid", PaymentOrderID: sql.NullInt64{Int64: int64(payment.ID), Valid: true}, CreatedMonth: month}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	for _, cents := range []int64{400, 600, 1} {
		refund := charge.RefundRecord{RefundNo: "worker-rf-" + uuid.NewString(), PaymentOrderID: payment.ID, UserID: user.ID, BizType: "charge", BizID: order.ID, RefundCents: cents, Status: "processing", ExecutionPolicy: "automatic", CreatedMonth: month}
		if err := db.Create(&refund).Error; err != nil {
			t.Fatal(err)
		}
		refunds = append(refunds, refund)
	}
	consumer := ResultConsumer{UserDB: db}
	assertState := func(want string, cents int64) {
		t.Helper()
		var stored charge.ChargeOrderRecord
		if err := db.Where("id=?", order.ID).Take(&stored).Error; err != nil || stored.BusinessStatus != "completed" || stored.Status != "completed" || stored.PaymentStatus != want {
			t.Fatalf("worker order state: %+v err=%v", stored, err)
		}
		var paid charge.PaymentOrderRecord
		if err := db.Where("id=?", payment.ID).Take(&paid).Error; err != nil || paid.RefundedCents != cents || paid.Status != want {
			t.Fatalf("worker ledger state: %+v err=%v", paid, err)
		}
	}
	assertState("paid", 0) // Processing requests have no effect until a successful channel result.
	for i, want := range []string{"partial_refunded", "refunded"} {
		refund := refunds[i]
		result := RefundResult{RefundNo: refund.RefundNo, ChannelRef: "state-channel-" + uuid.NewString(), RefundCents: refund.RefundCents, Success: true}
		for range 2 {
			if err := consumer.post(ctx, result); err != nil {
				t.Fatal(err)
			}
		}
		cents := int64(400)
		if i == 1 {
			cents = 1000
		}
		assertState(want, cents)
	}
	if err := consumer.post(ctx, RefundResult{RefundNo: refunds[2].RefundNo, ChannelRef: "over-refund-" + tag, RefundCents: 1, Success: true}); err == nil {
		t.Fatal("accepted a result beyond the remaining paid amount")
	}
	assertState("refunded", 1000)
	var pending charge.RefundRecord
	if err := db.Where("id=?", refunds[2].ID).Take(&pending).Error; err != nil || pending.Status != "processing" {
		t.Fatalf("excess refund did not roll back: %+v err=%v", pending, err)
	}
}
