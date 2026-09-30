package outbox

import (
	"context"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"os"
	"testing"
	"time"
)

func TestScheduledConsumersReturnWhenStreamsAreEmpty(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("disposable Redis required")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	defer client.Close()
	stream := "qa-empty-" + uuid.NewString()
	defer client.Del(context.Background(), stream)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	consumer := ResultConsumer{UserDB: &gorm.DB{}, Stream: client, Streams: []string{stream}, Group: uuid.NewString()}
	if n, err := consumer.ConsumeOnce(ctx); err != nil || n != 0 {
		t.Fatalf("empty result batch blocked: %d %v", n, err)
	}
	dlq := DLQ{WorkerDB: &gorm.DB{}, Stream: client, Streams: []string{stream}, Consumer: uuid.NewString()}
	if n, err := dlq.ConsumeBatch(ctx, func(context.Context, string, string, string, []byte) error { return nil }); err != nil || n != 0 {
		t.Fatalf("empty DLQ batch blocked: %d %v", n, err)
	}
}
