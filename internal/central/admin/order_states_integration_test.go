package admin

import (
	"context"
	"database/sql"
	orderpkg "github.com/ChargePilot2026/charge-pilot/internal/central/order"
	paymentpkg "github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	refundpkg "github.com/ChargePilot2026/charge-pilot/internal/central/refund"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSeparateOrderListsAndThreeChargingSources(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" {
		t.Skip("MySQL with current user schema required")
	}
	ctx := context.Background()
	db := openFinanceDB(t, "TEST_USER_DATABASE_URL")
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	// All fixtures remain private to this transaction and are always rolled back.
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("rollback fixtures: %v", err)
		}
	})
	store := ResourceStore{UserDB: tx}
	page := PageQuery{Page: 1, PageSize: 10}
	before, err := store.PaymentOrders(ctx, PaymentOrderQuery{PageQuery: page})
	if err != nil {
		t.Fatal(err)
	}
	tag := uuid.NewString()
	user := struct {
		ID     uint64
		Openid string
	}{Openid: "order-list-" + tag}
	if err := tx.Table("user").Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	paymentSpecs := []struct {
		method, biz, status string
		paid, refunded      int64
	}{
		{"wechat", "charge", "partial_refunded", 100, 20},
		{"balance", "charge", "paid", 100, 0},
		{"balance", "charge", "refunded", 200, 200},
		{"wechat", "wallet_recharge", "paid", 500, 0},
		{"wechat", "charge", "initiated", 0, 0},
		{"wechat", "charge", "paid", 100, 0}, // soft-deleted payment is never joined.
	}
	payments := make([]paymentpkg.PaymentOrderRecord, len(paymentSpecs))
	for i, spec := range paymentSpecs {
		p := paymentpkg.PaymentOrderRecord{OrderNo: "list-pay-" + tag + string(rune('a'+i)), UserID: user.ID,
			BizType: spec.biz, PayMethod: spec.method, TotalCents: 500, PaidCents: spec.paid,
			RefundedCents: spec.refunded, Status: spec.status, CreatedMonth: month}
		if err := tx.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		payments[i] = p
		// Future timestamps make pagination assertions independent of existing data.
		if err := tx.Table("payment_order").Where("id=?", p.ID).Update("created_at", time.Now().UTC().Add(time.Hour+time.Duration(i)*time.Second)).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Table("payment_order").Where("id=?", payments[5].ID).Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	orderSpecs := []struct {
		payment     int
		status, pay string
	}{
		{0, "completed", "partial_refunded"}, {1, "charging", "paid"},
		{2, "failed", "refunded"}, {5, "paid", "pending"},
	}
	orders := make([]orderpkg.ChargeOrderRecord, len(orderSpecs))
	for i, spec := range orderSpecs {
		order := orderpkg.ChargeOrderRecord{OrderNo: "list-charge-" + tag + string(rune('a'+i)), UserID: user.ID,
			DeviceID: "list-device-" + tag, PortNo: 1, Status: spec.status, PaymentStatus: spec.pay,
			PaymentOrderID: sql.NullInt64{Int64: int64(payments[spec.payment].ID), Valid: true}, CreatedMonth: month}
		if err := tx.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		orders[i] = order
	}
	if err := tx.Exec("INSERT INTO card_charge(charge_order_id,card_id,port_code,paid_cents,purchased_minutes,max_minutes,card_no,wallet_after_cents,package_json) VALUES(?,1,?,200,30,60,?,0,'{}')", orders[2].ID, tag, tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&refundpkg.RefundRecord{RefundNo: "list-refund-" + tag, PaymentOrderID: payments[0].ID,
		UserID: user.ID, BizType: "charge", BizID: orders[0].ID, RefundCents: 10, Status: "pending", CreatedMonth: month}).Error; err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct {
		source, business, pay string
		order                 int
		refund                int64
	}{
		{"payment", "completed", "partial_refunded", 0, 20},
		{"balance", "charging", "paid", 1, 0},
		{"card", "completed", "refunded", 2, 200},
	} {
		got, err := store.Orders(ctx, OrderQuery{PageQuery: page, DeviceID: orders[0].DeviceID,
			StartSource: spec.source, BusinessStatus: spec.business, PaymentStatus: spec.pay})
		if err != nil || got.Total != 1 || len(got.Items) != 1 {
			t.Fatalf("%s query total=%d items=%d err=%v", spec.source, got.Total, len(got.Items), err)
		}
		row := got.Items[0]
		if row.OrderID != orders[spec.order].ID || row.StartSource == nil || *row.StartSource != spec.source || row.RefundedCents == nil || *row.RefundedCents != spec.refund {
			t.Fatalf("%s row=%+v", spec.source, row)
		}
		if spec.order == 0 && (row.PaymentStatus != "partial_refunded" || row.RefundStatus != "processing") {
			t.Fatalf("pending additional refund obscured settled payment state: %+v", row)
		}
	}
	missing, err := store.Orders(ctx, OrderQuery{PageQuery: page, OrderNo: orders[3].OrderNo})
	if err != nil || len(missing.Items) != 1 {
		t.Fatalf("missing payment query: %+v err=%v", missing, err)
	}
	if row := missing.Items[0]; row.StartSource != nil || row.RefundedCents != nil || row.PaymentOrderStatus != nil {
		t.Fatalf("deleted payment fabricated a source or amount: %+v", row)
	}
	// Even malformed historical ownership/duplicate/recharge links cannot corrupt payment pagination.
	var duplicateID uint64
	for i, link := range []struct {
		payment int
		owner   uint64
	}{
		{0, user.ID + 100000}, {0, user.ID}, {3, user.ID},
	} {
		order := orderpkg.ChargeOrderRecord{OrderNo: "list-bad-link-" + tag + string(rune('a'+i)), UserID: link.owner,
			DeviceID: "other-list-" + tag, PortNo: 1, Status: "completed", PaymentStatus: "paid",
			PaymentOrderID: sql.NullInt64{Int64: int64(payments[link.payment].ID), Valid: true}, CreatedMonth: month}
		if err := tx.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			duplicateID = order.ID
		}
		if i < 2 {
			offset := time.Hour
			if i == 0 {
				offset = 100 * time.Hour
			}
			if err := tx.Table("charge_order").Where("id=?", order.ID).Update("created_at", time.Now().UTC().Add(offset)).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	all, err := store.PaymentOrders(ctx, PaymentOrderQuery{PageQuery: PageQuery{Page: 1, PageSize: 2}})
	if err != nil || all.Total != before.Total+5 || len(all.Items) != 2 || all.Items[0].PaymentOrderID != payments[4].ID || all.Items[1].PaymentOrderID != payments[3].ID {
		t.Fatalf("payment pagination before=%d after=%+v err=%v", before.Total, all, err)
	}
	for i, spec := range paymentSpecs[:5] {
		want := orderpkg.SettledPaymentStatus(spec.paid, spec.refunded, spec.status)
		got, err := store.PaymentOrders(ctx, PaymentOrderQuery{PageQuery: page, OrderNo: payments[i].OrderNo,
			BizType: spec.biz, PayMethod: spec.method, PaymentStatus: want})
		if err != nil || got.Total != 1 || len(got.Items) != 1 {
			t.Fatalf("payment %d query=%+v err=%v", i, got, err)
		}
		row := got.Items[0]
		if row.PaymentStatus != want || row.RefundedCents != spec.refunded {
			t.Fatalf("payment %d row=%+v", i, row)
		}
		if i == 3 || i == 4 {
			if row.ChargeOrderID != nil || row.ChargeOrderNo != nil {
				t.Fatalf("unassociated/recharge payment has a charging link: %+v", row)
			}
		} else if row.ChargeOrderID == nil || row.ChargeOrderNo == nil {
			t.Fatalf("charging payment lacks its link: %+v", row)
		}
		if i == 0 && (row.ChargeOrderID == nil || *row.ChargeOrderID != duplicateID) {
			t.Fatalf("payment linked another user's charging order: %+v", row)
		}
		wrong, err := store.PaymentOrders(ctx, PaymentOrderQuery{PageQuery: page, OrderNo: payments[i].OrderNo, PaymentStatus: "unknown"})
		if err != nil || wrong.Total != 0 || len(wrong.Items) != 0 {
			t.Fatalf("payment state filter ignored: %+v err=%v", wrong, err)
		}
	}
	deleted, err := store.PaymentOrders(ctx, PaymentOrderQuery{PageQuery: page, OrderNo: payments[5].OrderNo})
	if err != nil || deleted.Total != 0 || len(deleted.Items) != 0 {
		t.Fatalf("deleted payment returned: %+v err=%v", deleted, err)
	}
}
