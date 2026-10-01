package outbox_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/worker/outbox"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Identical ids in the two domain outboxes must never update the other domain.
func TestCentralAdminOutboxKeepsUserEventsIndependent(t *testing.T) {
	url, redisURL := os.Getenv("TEST_ADMIN_DATABASE_URL"), os.Getenv("TEST_STREAM_REDIS_URL")
	if url == "" || redisURL == "" {
		t.Skip("disposable central database and Redis required")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	stream := redis.NewClient(options)
	t.Cleanup(func() { stream.Close() })
	key := uuid.NewString()
	streamName := "test-admin-outbox-" + key
	// Keep this fixture's id above both existing sequences without deleting any
	// unrelated events in the disposable database.
	var id uint64
	if err := db.QueryRowContext(ctx, "SELECT GREATEST(COALESCE((SELECT MAX(id) FROM event_outbox),0),COALESCE((SELECT MAX(id) FROM admin_event_outbox),0))+1").Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "DELETE FROM event_outbox WHERE event_id=?", key)
		db.ExecContext(ctx, "DELETE FROM admin_event_outbox WHERE event_id=?", key)
		stream.Del(ctx, streamName)
	})
	for _, table := range []string{"event_outbox", "admin_event_outbox"} {
		if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" (id,event_id,stream,envelope_json,scheduled_at) VALUES (?,?,?,'{}',?)", id, key, streamName, time.Now().UTC().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (outbox.Publisher{Source: "admin", DB: testGORMDB(t, db), Stream: stream}).PublishBatch(ctx); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]string{"event_outbox": "pending", "admin_event_outbox": "published"} {
		var status string
		if err := db.QueryRowContext(ctx, "SELECT status FROM "+table+" WHERE event_id=?", key).Scan(&status); err != nil || status != want {
			t.Fatalf("%s status=%s want=%s error=%v", table, status, want, err)
		}
	}
	if n, err := stream.XLen(ctx, streamName).Result(); err != nil || n != 1 {
		t.Fatalf("delivered=%d error=%v", n, err)
	}
}
