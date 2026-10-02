package outbox_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/outbox"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestDeviceEventOutboxIntegration(t *testing.T) {
	url, redisURL := os.Getenv("TEST_GATEWAY_DATABASE_URL"), os.Getenv("TEST_STREAM_REDIS_URL")
	if url == "" || redisURL == "" {
		t.Skip("set disposable gateway database and Redis URLs")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	stream := redis.NewClient(options)
	defer stream.Close()
	deviceID := uuid.NewString()
	if err := (store.MySQLSink{DB: testGORMDB(t, db)}).Record(ctx, protocol.Event{Protocol: "dc589", DeviceID: deviceID, Type: protocol.Heartbeat, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var eventKey string
	if err := db.QueryRowContext(ctx, "SELECT event_key FROM device_event WHERE device_id = ?", deviceID).Scan(&eventKey); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DELETE FROM device_event WHERE event_key = ?", eventKey)
	defer db.ExecContext(ctx, "DELETE FROM event_outbox WHERE event_id = ?", eventKey)
	count, err := (outbox.Publisher{Source: "gateway", DB: testGORMDB(t, db), Stream: stream}).PublishBatch(ctx)
	if err != nil || count < 1 {
		t.Fatalf("publish count=%d err=%v", count, err)
	}
	var status string
	if err := db.QueryRowContext(ctx, "SELECT status FROM event_outbox WHERE event_id = ?", eventKey).Scan(&status); err != nil || status != "published" {
		t.Fatalf("outbox status=%q err=%v", status, err)
	}
	entries, err := stream.XRevRangeN(ctx, "device_event_stream", "+", "-", 100).Result()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Values["event_id"] == eventKey {
			found = true
			_ = stream.XDel(ctx, "device_event_stream", entry.ID).Err()
		}
	}
	if !found {
		t.Fatal("event missing from Redis stream")
	}
}
