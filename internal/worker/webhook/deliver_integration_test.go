package worker_test

import (
	"context"
	"os"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	webhook "github.com/ChargePilot2026/charge-pilot/internal/worker/webhook"
	"github.com/redis/go-redis/v9"
)

// 投递器与拥有这些流的服务共用同一批流。
// 所以一条它读不懂的记录并不是它可以销毁的记录：
// 删掉一条曾把所有设备事件直接删光，
// 因为设备事件把类型写在 "Type" 里，
// 而这个解析器预期的那层信封把它叫作 "event_type"。
// 这条记录必须留下来，
// 让拥有它的服务仍能读到。
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

	// gateway 记录设备事件的方式：
	// 类型写在 "Type" 里，
	// 而根本没有叫 event_type 的字段。
	entry, err := stream.XAdd(ctx, &redis.XAddArgs{Stream: name, Values: map[string]any{
		"event_id": "evt-1", "source": "gateway",
		"payload": `{"Type":"heartbeat","DeviceID":"9000000000000001","Protocol":"dc589"}`,
	}}).Result()
	if err != nil {
		t.Fatal(err)
	}

	// 这里没有配置任何订阅，所以投递本身无事可做；
	// 本测试要看的正是这条记录在此之前与之后会怎样。
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
