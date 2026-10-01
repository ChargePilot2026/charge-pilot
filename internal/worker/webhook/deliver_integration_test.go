package worker_test

import (
	"context"
	"os"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	webhook "github.com/ChargePilot2026/charge-pilot/internal/worker/webhook"
	"github.com/redis/go-redis/v9"
)

// 验证 Webhook 消费者保留无法识别的共享流记录，不确认或删除其他服务拥有的事件。
func TestUnreadableEntryIsLeftForItsOwner(t *testing.T) {
	adminURL, redisURL := os.Getenv("TEST_ADMIN_DATABASE_URL"), os.Getenv("TEST_STREAM_REDIS_URL")
	if adminURL == "" || redisURL == "" {
		t.Skip("set disposable admin database and stream Redis URLs")
	}
	ctx := context.Background()
	adminDB, err := dbconn.Open(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	adminORM, err := dbconn.WrapGORM(adminDB)
	if err != nil {
		t.Fatal(err)
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	stream := redis.NewClient(options)
	defer stream.Close()
	const name = "webhook-unreadable-test-stream"
	_ = stream.Del(ctx, name).Err()
	t.Cleanup(func() { _ = stream.Del(ctx, name).Err() })

	// 设备原始事件使用 Type 字段，不含 event_type，需兼容该载荷格式。
	entry, err := stream.XAdd(ctx, &redis.XAddArgs{Stream: name, Values: map[string]any{
		"event_id": "evt-1", "source": "gateway",
		"payload": `{"Type":"heartbeat","DeviceID":"9000000000000001","Protocol":"dc589"}`,
	}}).Result()
	if err != nil {
		t.Fatal(err)
	}

	// 不配置订阅，单独验证未识别记录在消费前后仍然保留。
	deliverer := webhook.WebhookDeliverer{AdminDB: adminORM, Stream: stream, Streams: []string{name}}
	if _, err := deliverer.PublishBatch(ctx); err != nil {
		t.Fatal(err)
	}

	length, err := stream.XLen(ctx, name).Result()
	if err != nil {
		t.Fatal(err)
	}
	if length != 1 {
		t.Fatalf("the stream holds %d entries, want 1: an entry this consumer cannot read was destroyed", length)
	}
	kept, err := stream.XRange(ctx, name, entry, entry).Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Fatalf("entry %s was removed from the stream by a consumer that does not own it", entry)
	}
}
