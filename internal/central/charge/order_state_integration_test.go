package charge

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestOrderBusinessAndPaymentStatesPersistIndependently(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" {
		t.Skip("MySQL with current user schema required")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, os.Getenv("TEST_USER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	orm := testGORMDB(t, db)
	tag := uuid.NewString()
	user := struct {
		ID     uint64
		Openid string
	}{Openid: "order-states-" + tag}
	var order ChargeOrderRecord
	var payment PaymentOrderRecord
	t.Cleanup(func() {
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{"DELETE FROM charge_order WHERE id=? AND order_no=?", []any{order.ID, order.OrderNo}},
			{"DELETE FROM payment_order WHERE id=? AND order_no=?", []any{payment.ID, payment.OrderNo}},
			{"DELETE FROM user WHERE id=? AND openid=?", []any{user.ID, user.Openid}},
		} {
			if err := orm.Exec(statement.query, statement.args...).Error; err != nil {
				t.Errorf("state fixture cleanup: %v", err)
			}
		}
	})
	if err := orm.Table("user").Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	payment = PaymentOrderRecord{OrderNo: "state-pay-" + tag, BizType: "charge", UserID: user.ID, PayMethod: "wechat", TotalCents: 100, Status: "initiated", CreatedMonth: utcDate()}
	if err := orm.Create(&payment).Error; err != nil {
		t.Fatal(err)
	}
	order = ChargeOrderRecord{OrderNo: "state-order-" + tag, UserID: user.ID, DeviceID: "state-device", PortNo: 1,
		PaymentOrderID: sql.NullInt64{Int64: int64(payment.ID), Valid: true}, Status: "pending_payment", BusinessStatus: "completed", CreatedMonth: utcDate()}
	if err := orm.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	check := func(lifecycle, business, paid string) {
		t.Helper()
		var stored ChargeOrderRecord
		if err := orm.Where("id=? AND order_no=?", order.ID, order.OrderNo).Take(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != lifecycle || stored.BusinessStatus != business || stored.PaymentStatus != paid {
			t.Fatalf("stored statuses lifecycle=%s business=%s payment=%s, want %s/%s/%s", stored.Status, stored.BusinessStatus, stored.PaymentStatus, lifecycle, business, paid)
		}
	}
	check("pending_payment", "pending_start", "pending") // generated field is never client-writable.
	for _, state := range []struct{ lifecycle, business string }{
		{"paid", "pending_start"}, {"charging", "charging"}, {"completed", "completed"},
		{"refunding", "completed"}, {"refunded", "completed"}, {"cancelled", "completed"}, {"failed", "completed"},
	} {
		if err := orm.Model(&ChargeOrderRecord{}).Where("id=?", order.ID).Update("status", state.lifecycle).Error; err != nil {
			t.Fatal(err)
		}
		check(state.lifecycle, state.business, "pending")
	}
	if err := orm.Model(&ChargeOrderRecord{}).Where("id=?", order.ID).Update("status", "completed").Error; err != nil {
		t.Fatal(err)
	}
	applyPayment := func(paid, refunded int64, ledgerStatus, want string) {
		t.Helper()
		if err := orm.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&PaymentOrderRecord{}).Where("id=?", payment.ID).Updates(map[string]any{"paid_cents": paid, "refunded_cents": refunded, "status": ledgerStatus}).Error; err != nil {
				return err
			}
			return SyncOrderPaymentStatus(tx, payment.ID)
		}); err != nil {
			t.Fatal(err)
		}
		check("completed", "completed", want)
	}
	applyPayment(100, 0, "paid", "paid")
	applyPayment(100, 40, "partial_refunded", "partial_refunded")
	rollback := errors.New("fixture abort after state synchronization")
	err = orm.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&PaymentOrderRecord{}).Where("id=?", payment.ID).Updates(map[string]any{"refunded_cents": 100, "status": "refunded"}).Error; err != nil {
			return err
		}
		if err := SyncOrderPaymentStatus(tx, payment.ID); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	check("completed", "completed", "partial_refunded")
	var paid PaymentOrderRecord
	if err := orm.Where("id=?", payment.ID).Take(&paid).Error; err != nil || paid.RefundedCents != 40 {
		t.Fatalf("refund rollback changed amounts: %+v err=%v", paid, err)
	}
	applyPayment(100, 100, "refunded", "refunded")
	// The shared SQL projection and persisted statuses use the same amount semantics.
	for _, ledger := range []string{"initiated", "paid", "closed", "refunded", "partial_refunded", "failed"} {
		for _, amounts := range [][2]int64{{0, 0}, {100, 0}, {100, 40}, {100, 100}} {
			var result string
			query := "SELECT " + PaymentStatusSQL + " FROM (SELECT ? AS paid_cents, ? AS refunded_cents, ? AS status) p"
			if err := orm.Raw(query, amounts[0], amounts[1], ledger).Scan(&result).Error; err != nil || result != SettledPaymentStatus(amounts[0], amounts[1], ledger) {
				t.Fatalf("SQL payment status mismatch: amounts=%v ledger=%s result=%s err=%v", amounts, ledger, result, err)
			}
		}
	}
}
