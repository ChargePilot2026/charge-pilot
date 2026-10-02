package outbox

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// 失败记录不得确认：保留 pending 由消费组重投，重试预算耗尽后转死信。
// 第一轮失败后 pending 必须仍在（旧实现先 XAck，消息永不重试）；
// 第二轮（重投后）attempts 达到上限，记录被移入死信流并从源流删除。
func TestDLQRetriesThenDeadLetters(t *testing.T) {
	if os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable Redis required")
	}
	ctx := context.Background()
	options, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { client.Close() })

	stream := "qa-dlq-" + uuid.NewString()[:8]
	t.Cleanup(func() { client.Del(ctx, stream, stream+DeadLetterSuffix) })
	dlq := DLQ{Stream: client, Consumer: "qa-" + uuid.NewString()[:8], Streams: []string{stream}, MaxAttempts: 2}
	// 组从 "$" 起步，之后写入的消息才会被 ">" 读到。
	if err := client.XGroupCreateMkStream(ctx, stream, dlq.Consumer, "$").Err(); err != nil {
		t.Fatal(err)
	}
	msgID, err := client.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{
		"event_id": "evt-1", "source": "qa", "payload": "{}",
	}}).Result()
	if err != nil {
		t.Fatal(err)
	}
	_ = msgID

	failing := func(context.Context, string, string, string, []byte) error { return errHandlerFailed }
	if _, err := dlq.ConsumeBatch(ctx, failing); err == nil {
		t.Fatal("handler failure must surface as batch error")
	}
	// B1：失败后不得确认，pending 保留待重投。
	pending := pendingIDs(ctx, t, client, stream, dlq.Consumer)
	if len(pending) != 1 {
		t.Fatalf("pending after first failure = %d, want 1 (failed record must stay pending)", len(pending))
	}

	// B2：重投后投递次数达到上限，记录转死信；转死信成功即本轮处理完成。
	if _, err := dlq.ConsumeBatch(ctx, failing); err != nil {
		t.Fatalf("dead-letter transfer should complete cleanly: %v", err)
	}
	pending = pendingIDs(ctx, t, client, stream, dlq.Consumer)
	if len(pending) != 0 {
		t.Fatalf("pending after dead-letter = %d, want 0", len(pending))
	}
	dead, err := client.XLen(ctx, stream+DeadLetterSuffix).Result()
	if err != nil {
		t.Fatal(err)
	}
	if dead != 1 {
		t.Fatalf("dead-letter stream length = %d, want 1", dead)
	}

	// 成功路径：新消息处理后被确认并删除，不留 pending。
	if _, err := client.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{
		"event_id": "evt-2", "source": "qa", "payload": "{}",
	}}).Result(); err != nil {
		t.Fatal(err)
	}
	n, err := dlq.ConsumeBatch(ctx, func(context.Context, string, string, string, []byte) error { return nil })
	if err != nil || n != 1 {
		t.Fatalf("successful batch: n=%d err=%v, want 1 nil", n, err)
	}
	if pending := pendingIDs(ctx, t, client, stream, dlq.Consumer); len(pending) != 0 {
		t.Fatalf("pending after success = %d, want 0", len(pending))
	}
}

var errHandlerFailed = errString("handler failed")

type errString string

func (e errString) Error() string { return string(e) }

func pendingIDs(ctx context.Context, t *testing.T, client *redis.Client, stream, group string) []string {
	t.Helper()
	entries, err := client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: stream, Group: group, Start: "-", End: "+", Count: 100,
	}).Result()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(entries))
	for _, item := range entries {
		ids = append(ids, item.ID)
	}
	return ids
}

func mustXLen(t *testing.T, ctx context.Context, client *redis.Client, stream string) int64 {
	t.Helper()
	n, err := client.XLen(ctx, stream).Result()
	if err != nil {
		t.Fatal(err)
	}
	return n
}
