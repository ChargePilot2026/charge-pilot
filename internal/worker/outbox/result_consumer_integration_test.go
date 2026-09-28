package outbox

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func resultFixture(t *testing.T) (userORM, workerORM *gorm.DB, refundNo, channelRef string, userID, refundCents int64) {
	t.Helper()
	ctx := context.Background()
	userConn, err := dbconn.Open(ctx, os.Getenv("TEST_USER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { userConn.Close() })
	userORM, err = dbconn.WrapGORM(userConn)
	if err != nil {
		t.Fatal(err)
	}
	workerConn, err := dbconn.Open(ctx, os.Getenv("TEST_WORKER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { workerConn.Close() })
	workerORM, err = dbconn.WrapGORM(workerConn)
	if err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()[:8]
	openid := "qa-result-" + suffix
	if err := userORM.Exec("INSERT INTO user(openid) VALUES(?)", openid).Error; err != nil {
		t.Fatal(err)
	}
	userORM.Table("user").Where("openid = ?", openid).Pluck("id", &userID)

	payNo := "QA-RES-PAY-" + suffix
	userORM.Exec("INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,refunded_cents,status,created_month) VALUES(?,'charge',0,?,'wechat',10000,10000,0,'partial_refunded','2026-09-29')", payNo, userID)
	var paymentID uint64
	userORM.Table("payment_order").Where("order_no = ?", payNo).Pluck("id", &paymentID)

	refundNo = "QA-RES-" + suffix
	channelRef = "CHRES" + suffix
	refundCents = 2500
	userORM.Exec("INSERT INTO refund_record(refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,status,execution_policy,created_month) VALUES(?,?,?,'charge',0,?,'processing','automatic','2026-09-29')",
		refundNo, paymentID, userID, refundCents)

	t.Cleanup(func() {
		userORM.Exec("DELETE FROM refund_success_receipt WHERE refund_record_id IN (SELECT id FROM refund_record WHERE refund_no = ?)", refundNo)
		userORM.Exec("DELETE FROM refund_record WHERE refund_no = ?", refundNo)
		userORM.Exec("DELETE FROM payment_order WHERE order_no = ?", payNo)
		userORM.Exec("DELETE FROM user WHERE id = ?", userID)
		month := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
		workerORM.Exec("DELETE FROM comp_tx_log WHERE tx_id LIKE ?", "refund-result:%"+suffix+"*")
		_ = month
	})
	return userORM, workerORM, refundNo, channelRef, userID, refundCents
}

func resultConsumer(t *testing.T, userDB, workerDB *gorm.DB) (ResultConsumer, func()) {
	t.Helper()
	options, err := redis.ParseURL(os.Getenv("TEST_STREAM_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { client.Close() })
	group := "qa-result-" + uuid.NewString()[:8]
	return ResultConsumer{UserDB: userDB, WorkerDB: workerDB, Stream: client, Group: group}, func() {
		client.Close()
	}
}

// A settled refund must move the money exactly once no matter how many times the
// channel redelivers the event.
func TestRefundResultPostsExactlyOnce(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_WORKER_DATABASE_URL") == "" || os.Getenv("TEST_STREAM_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	ctx := context.Background()
	userDB, workerDB, refundNo, channelRef, userID, refundCents := resultFixture(t)
	consumer, closeConsumer := resultConsumer(t, userDB, workerDB)
	defer closeConsumer()

	stream := "refund_succeeded_stream"
	// The handler acknowledges through the consumer group, so the group has to
	// exist exactly as it would after a production startup.
	consumer.Stream.XGroupCreateMkStream(ctx, stream, consumer.consumerGroup(), "0")

	payload, _ := json.Marshal(RefundResult{
		RefundNo: refundNo, ChannelRef: channelRef, RefundCents: refundCents, Success: true,
	})
	entry := Entry{ID: "1-1", EventID: "evt-" + uuid.NewString()[:8], Source: "payment", Payload: payload}

	for round := 1; round <= 3; round++ {
		if err := consumer.Handle(ctx, stream, entry); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}

	var status string
	if err := userDB.Table("refund_record").Select("status").Where("refund_no = ?", refundNo).Take(&status).Error; err != nil {
		t.Fatal(err)
	}
	if status != "success" {
		t.Fatalf("refund status = %q, want success", status)
	}
	// The receipt table is keyed by the refund record, so redelivery cannot add
	// a second one.
	var receipts int64
	userDB.Table("refund_success_receipt").
		Where("refund_record_id IN (SELECT id FROM refund_record WHERE refund_no = ?)", refundNo).Count(&receipts)
	if receipts != 1 {
		t.Fatalf("success receipts = %d, want 1", receipts)
	}
	// The payment's refunded total must have moved exactly once as well.
	var refunded struct {
		RefundedCents int64 `gorm:"column:refunded_cents"`
	}
	userDB.Table("payment_order").Select("refunded_cents").
		Where("user_id = ?", userID).Take(&refunded)
	if refunded.RefundedCents != refundCents {
		t.Fatalf("payment refunded_cents = %d, want %d", refunded.RefundedCents, refundCents)
	}
}

// A channel reporting a different amount than was requested must not be posted;
// it describes some other refund.
func TestRefundResultRejectsAmountMismatch(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_WORKER_DATABASE_URL") == "" || os.Getenv("TEST_STREAM_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	ctx := context.Background()
	userDB, workerDB, refundNo, channelRef, _, refundCents := resultFixture(t)
	consumer, closeConsumer := resultConsumer(t, userDB, workerDB)
	defer closeConsumer()

	stream := "refund_succeeded_stream"
	consumer.Stream.XGroupCreateMkStream(ctx, stream, consumer.consumerGroup(), "0")
	payload, _ := json.Marshal(RefundResult{
		RefundNo: refundNo, ChannelRef: channelRef, RefundCents: refundCents + 1, Success: true,
	})
	entry := Entry{ID: "1-1", EventID: "evt-" + uuid.NewString()[:8], Source: "payment", Payload: payload}
	if err := consumer.Handle(ctx, stream, entry); err == nil {
		t.Fatal("amount mismatch was accepted")
	}
	var status string
	userDB.Table("refund_record").Select("status").Where("refund_no = ?", refundNo).Take(&status)
	if status != "processing" {
		t.Fatalf("refund status = %q, want processing (it must not settle)", status)
	}
}

// A malformed payload can never succeed on retry, so it goes straight to the
// dead-letter stream instead of blocking the consumer group.
func TestRefundResultDeadLettersUnparseablePayload(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_WORKER_DATABASE_URL") == "" || os.Getenv("TEST_STREAM_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	ctx := context.Background()
	userDB, workerDB, _, _, _, _ := resultFixture(t)
	consumer, closeConsumer := resultConsumer(t, userDB, workerDB)
	defer closeConsumer()

	stream := "refund_succeeded_stream"
	consumer.Stream.XGroupCreateMkStream(ctx, stream, consumer.consumerGroup(), "0")
	entry := Entry{ID: "1-1", EventID: "evt-" + uuid.NewString()[:8], Source: "payment", Payload: []byte("not json")}
	if err := consumer.Handle(ctx, stream, entry); err != nil {
		t.Fatalf("dead-lettering should succeed: %v", err)
	}
	dead, err := consumer.Stream.XLen(ctx, stream+DeadLetterSuffix).Result()
	if err != nil {
		t.Fatal(err)
	}
	if dead == 0 {
		t.Fatal("unparseable payload was not dead-lettered")
	}
}
