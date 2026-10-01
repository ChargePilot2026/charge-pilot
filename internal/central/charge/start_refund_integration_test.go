package charge

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestRejectedStartQueuesRefundAtomicallyAndIdempotently(t *testing.T) {
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
	user, err := db.ExecContext(ctx, "INSERT INTO user (openid) VALUES (?)", "start-refund-"+unique)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := user.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	month := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	paymentInsert, err := db.ExecContext(ctx, `INSERT INTO payment_order
		(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,wechat_transaction_id,status,paid_at,created_month)
		VALUES (?,'charge',0,?,'wechat',250,250,?,'paid',UTC_TIMESTAMP(3),?)`, "PAY-"+unique, userID, "wx-"+unique, month)
	if err != nil {
		t.Fatal(err)
	}
	paymentID, err := paymentInsert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	orderNo := "ORD-" + unique
	order, err := db.ExecContext(ctx, `INSERT INTO charge_order
		(order_no,user_id,device_id,port_no,payment_order_id,status,payment_status,created_month,charge_mode,charge_quantity)
		VALUES (?,?,?,1,?,'paid','paid',?,4,600)`, orderNo, userID, "BOARD-TEST", paymentID, month)
	if err != nil {
		t.Fatal(err)
	}
	orderID, err := order.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE payment_order SET biz_id = ? WHERE id = ? AND created_month = ?", orderID, paymentID, month); err != nil {
		t.Fatal(err)
	}
	commandID := uuid.NewString()
	defer func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM event_outbox WHERE stream = 'refund_required_stream' AND JSON_UNQUOTE(JSON_EXTRACT(envelope_json, '$.order_no')) = ?", orderNo)
		_, _ = db.ExecContext(ctx, "DELETE FROM event_outbox WHERE event_id = ?", commandID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_event_log WHERE charge_order_id = ?", orderID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_start_receipt WHERE command_id = ?", commandID)
		_, _ = db.ExecContext(ctx, "DELETE FROM refund_success_receipt WHERE refund_record_id IN (SELECT id FROM refund_record WHERE payment_order_id = ?)", paymentID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_prepay WHERE payment_order_id = ?", paymentID)
		_, _ = db.ExecContext(ctx, "DELETE FROM event_outbox WHERE stream = 'refund_succeeded_stream' AND JSON_EXTRACT(envelope_json, '$.payment_order_id') = ?", paymentID)
		_, _ = db.ExecContext(ctx, "DELETE FROM refund_record WHERE payment_order_id = ?", paymentID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_order WHERE id = ? AND created_month = ?", orderID, month)
		_, _ = db.ExecContext(ctx, "DELETE FROM payment_order WHERE id = ? AND created_month = ?", paymentID, month)
		_, _ = db.ExecContext(ctx, "DELETE FROM user WHERE id = ?", userID)
	}()

	result := StartResult{CommandID: commandID, ChargeOrderID: uint64(orderID), OrderNo: orderNo,
		DeviceID: "BOARD-TEST", PortNo: 1, PortID: uint64(orderID) + 1000000,
		Success: false, ResultCode: 1, OccurredAt: time.Now().UTC()}
	store := StartResultStore{DB: testGORMDB(t, db)}
	replayed, err := store.Apply(ctx, result)
	if err != nil || replayed {
		t.Fatalf("first result: replayed=%v err=%v", replayed, err)
	}
	replayed, err = store.Apply(ctx, result)
	if err != nil || !replayed {
		t.Fatalf("replayed result: replayed=%v err=%v", replayed, err)
	}

	var orderStatus, paymentStatus string
	if err := db.QueryRowContext(ctx, "SELECT status FROM charge_order WHERE id = ? AND created_month = ?", orderID, month).Scan(&orderStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT status FROM payment_order WHERE id = ? AND created_month = ?", paymentID, month).Scan(&paymentStatus); err != nil {
		t.Fatal(err)
	}
	var refunds, refundEvents, records int
	var refundCents int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(MAX(refund_cents),0) FROM refund_record WHERE payment_order_id = ?", paymentID).Scan(&refunds, &refundCents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM event_outbox WHERE stream = 'refund_required_stream' AND JSON_UNQUOTE(JSON_EXTRACT(envelope_json, '$.order_no')) = ?", orderNo).Scan(&refundEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_start_receipt WHERE command_id = ?", commandID).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if orderStatus != "refunding" || paymentStatus != "paid" || refunds != 1 || refundCents != 250 || refundEvents != 1 || records != 1 {
		t.Fatalf("order=%s payment=%s refunds=%d amount=%d events=%d receipts=%d", orderStatus, paymentStatus, refunds, refundCents, refundEvents, records)
	}

	var envelopeJSON []byte
	if err := db.QueryRowContext(ctx, "SELECT envelope_json FROM event_outbox WHERE stream = 'refund_required_stream' AND JSON_UNQUOTE(JSON_EXTRACT(envelope_json, '$.order_no')) = ?", orderNo).Scan(&envelopeJSON); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		RefundNo    string `json:"refund_no"`
		PaymentID   uint64 `json:"payment_order_id"`
		ChargeID    uint64 `json:"charge_order_id"`
		AmountCents int64  `json:"amount_cents"`
	}
	if err := json.Unmarshal(envelopeJSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.RefundNo == "" || envelope.PaymentID != uint64(paymentID) || envelope.ChargeID != uint64(orderID) || envelope.AmountCents != refundCents {
		t.Fatalf("refund outbox envelope=%s", envelopeJSON)
	}
	// 让自动入队的退款走一遍"结果未知"的恢复流程。
	orm := testGORMDB(t, db)
	if err := orm.Create(&ChargePrepayRecord{PaymentOrderID: uint64(paymentID), ParamsJSON: []byte(`{"provider":"simulation"}`)}).Error; err != nil {
		t.Fatal(err)
	}
	var refund RefundRecord
	if err := orm.Where("payment_order_id = ?", paymentID).Take(&refund).Error; err != nil {
		t.Fatal(err)
	}
	provider := &lostRefundResponse{}
	executor := RefundExecutor{DB: orm, Provider: provider, ProviderName: "simulation"}
	if err := executor.Execute(ctx, refund.ID); err == nil {
		t.Fatal("lost response should remain unknown")
	}
	var paid PaymentOrderRecord
	if err := orm.Where("id = ?", paymentID).Take(&paid).Error; err != nil || paid.RefundedCents != 0 {
		t.Fatal("unknown outcome credited", paid, err)
	}
	var pendingRefundOrder ChargeOrderRecord
	if err := orm.Where("id=?", orderID).Take(&pendingRefundOrder).Error; err != nil || pendingRefundOrder.BusinessStatus != "completed" || pendingRefundOrder.PaymentStatus != "paid" {
		t.Fatalf("refund request/unknown outcome changed payment state: %+v err=%v", pendingRefundOrder, err)
	}
	if err := orm.Model(&RefundRecord{}).Where("id = ?", refund.ID).Update("next_attempt_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := executor.Execute(ctx, refund.ID); err != nil {
			t.Fatal(err)
		}
	}
	if provider.creates != 1 {
		t.Fatal("refund submitted more than once", provider.creates)
	}
	if err := orm.Where("id = ?", paymentID).Take(&paid).Error; err != nil || paid.RefundedCents != 250 || paid.Status != "refunded" {
		t.Fatal("refund accounting", paid, err)
	}
	var refunded ChargeOrderRecord
	if err := orm.Where("id = ?", orderID).Take(&refunded).Error; err != nil || refunded.Status != "refunded" || refunded.BusinessStatus != "completed" || refunded.PaymentStatus != "refunded" {
		t.Fatal("order refund", refunded, err)
	}
	var receiptCount int64
	if err := orm.Table("refund_success_receipt").Where("refund_record_id = ?", refund.ID).Count(&receiptCount).Error; err != nil || receiptCount != 1 {
		t.Fatal("refund receipt", receiptCount, err)
	}
	bad := provider.result
	bad.RefundCents++
	if err := executor.apply(ctx, refund, payment.RefundRequest{RefundNo: refund.RefundNo, MerchantOrderNo: paid.OrderNo, TransactionID: paid.WechatTransactionID.String, TotalCents: paid.TotalCents, RefundCents: refund.RefundCents}, bad); !errors.Is(err, ErrRefundConflict) {
		t.Fatal("mismatched result accepted", err)
	}

}

// 模拟渠道已受理退款但 HTTP 响应丢失；恢复流程查询原退款，不重复发起资金划转。
type lostRefundResponse struct {
	result  payment.RefundResult
	creates int
}

func (p *lostRefundResponse) QueryRefund(context.Context, payment.RefundRequest) (payment.RefundResult, error) {
	if p.creates == 0 {
		return payment.RefundResult{}, payment.ErrRefundNotFound
	}
	return p.result, nil
}
func (p *lostRefundResponse) CreateRefund(ctx context.Context, r payment.RefundRequest) (payment.RefundResult, error) {
	p.creates++
	p.result, _ = (payment.Simulator{}).CreateRefund(ctx, r)
	return payment.RefundResult{}, errors.New("connection lost after accept")
}
