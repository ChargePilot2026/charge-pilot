package delivery_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/delivery"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/redis/go-redis/v9"
)

// mockCentral 模拟 central 的订阅清单与投递日志上报端点。
type mockCentral struct {
	mu        sync.Mutex
	subs      []delivery.Subscription
	recorded  [][]delivery.DeliveryRecord
	sawRecord bool
}

func (m *mockCentral) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "service" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/internal/webhook-subscriptions":
			m.mu.Lock()
			subs := m.subs
			m.mu.Unlock()
			writeReply(t, w, map[string]any{"code": 0, "data": map[string]any{"items": subs}})
		case "/api/v1/internal/webhook-deliveries/record":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Items []delivery.DeliveryRecord `json:"items"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("record body: %v", err)
			}
			m.mu.Lock()
			m.sawRecord = true
			m.recorded = append(m.recorded, req.Items)
			m.mu.Unlock()
			writeReply(t, w, map[string]any{"code": 0, "data": map[string]any{"recorded": len(req.Items)}})
		default:
			t.Errorf("unexpected central call: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func writeReply(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func openStream(t *testing.T) (*redis.Client, context.Context) {
	t.Helper()
	redisURL := os.Getenv("TEST_STREAM_REDIS_URL")
	if redisURL == "" {
		t.Skip("set disposable stream Redis URL")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	return redis.NewClient(options), context.Background()
}

// 平移原 worker/webhook 场景：无法解析的共享流记录保留给属主，不被删除。
func TestUnreadableEntryIsLeftForItsOwner(t *testing.T) {
	stream, ctx := openStream(t)
	defer stream.Close()
	mock := &mockCentral{}
	central := mock.server(t)
	defer central.Close()

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

	// 订阅清单为空时不触碰共享流；单独验证未识别记录消费前后都保留。
	deliverer := delivery.WebhookDeliverer{Stream: stream, Central: serviceclient.Client{Timeout: 5 * time.Second}, CentralURL: central.URL, ServiceToken: "service", Streams: []string{name}}
	if _, err := deliverer.PublishBatch(ctx); err != nil {
		t.Fatal(err)
	}
	length, err := stream.XLen(ctx, name).Result()
	if err != nil {
		t.Fatal(err)
	}
	if length != 1 {
		t.Fatalf("the stream holds %d entries, want 1", length)
	}
	kept, err := stream.XRange(ctx, name, entry, entry).Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Fatalf("entry %s was removed by a consumer that does not own it", entry)
	}
}

// B4 修复：投递完成（含无需投递）的条目从共享流删除，重复扫描不再外投。
func TestHandledEntryIsDeletedAndNotRescanned(t *testing.T) {
	stream, ctx := openStream(t)
	defer stream.Close()
	mock := &mockCentral{}
	central := mock.server(t)
	defer central.Close()

	const name = "webhook-handled-test-stream"
	_ = stream.Del(ctx, name).Err()
	t.Cleanup(func() { _ = stream.Del(ctx, name).Err() })

	mock.mu.Lock()
	mock.subs = []delivery.Subscription{{ID: 1, Name: "audit", URL: "http://insecure.example.com/hook", Secret: "s", EventTypes: []string{"charge_started"}}}
	mock.mu.Unlock()
	if _, err := stream.XAdd(ctx, &redis.XAddArgs{Stream: name, Values: map[string]any{
		"source":  "gateway",
		"payload": `{"event_id":"evt-nomatch","event_type":"charge_ended","occurred_at":"2026-09-29T12:00:00Z","data":{}}`,
	}}).Result(); err != nil {
		t.Fatal(err)
	}

	deliverer := delivery.WebhookDeliverer{Stream: stream, Central: serviceclient.Client{Timeout: 5 * time.Second}, CentralURL: central.URL, ServiceToken: "service", Streams: []string{name}}
	if _, err := deliverer.PublishBatch(ctx); err != nil {
		t.Fatal(err)
	}
	// 不匹配任何订阅的事件视为已处理（无需外投），条目删除且不留记录。
	length, err := stream.XLen(ctx, name).Result()
	if err != nil {
		t.Fatal(err)
	}
	if length != 0 {
		t.Fatalf("handled entry kept in stream: len=%d", length)
	}
	mock.mu.Lock()
	if mock.sawRecord {
		t.Fatal("no-match delivery produced a log record")
	}
	mock.mu.Unlock()

	// 匹配订阅但 URL 未过 netguard：记录失败、保留条目待重试。
	if _, err := stream.XAdd(ctx, &redis.XAddArgs{Stream: name, Values: map[string]any{
		"source":  "gateway",
		"payload": `{"event_id":"evt-blocked","event_type":"charge_started","occurred_at":"2026-09-29T12:00:00Z","data":{}}`,
	}}).Result(); err != nil {
		t.Fatal(err)
	}
	if _, err := deliverer.PublishBatch(ctx); err == nil {
		t.Fatal("blocked delivery should surface an error")
	}
	mock.mu.Lock()
	if !mock.sawRecord {
		t.Fatal("failed delivery was not reported to central")
	}
	if len(mock.recorded) != 1 || len(mock.recorded[0]) != 1 || mock.recorded[0][0].Error == "" {
		t.Fatalf("unexpected records %+v", mock.recorded)
	}
	mock.mu.Unlock()
	length, err = stream.XLen(ctx, name).Result()
	if err != nil {
		t.Fatal(err)
	}
	if length != 1 {
		t.Fatalf("failed entry should stay for retry: len=%d", length)
	}
}
