// Package delivery 承载向进程外的投递：Webhook 订阅端点与监管报送端点的
// 签名和 HTTP 发送。订阅配置、投递日志与报送状态都在 central_db，
// 由 central 提供内部端点读写；本包不持有任何数据库句柄。
package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/netguard"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/redis/go-redis/v9"
)

// WebhookDeliverer 给管理端事件订阅签名并投递。订阅清单与投递日志
// 分别经 central 的内部端点读取和上报，共享流里的条目在投递完成后
// XDEL 删除——此前只 XACK（且消费组从未建立）导致每次扫描重复外投。
type WebhookDeliverer struct {
	Stream       *redis.Client
	Central      serviceclient.Client
	CentralURL   string
	ServiceToken string
	Client       *http.Client
	BatchSize    int
	Streams      []string
}

// Subscription 是 central 返回的启用的订阅；Secret 仅在内部网络上明文传输。
type Subscription struct {
	ID         uint64   `json:"id"`
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Secret     string   `json:"secret"`
	EventTypes []string `json:"event_types"`
}

// DeliveryRecord 是一次投递尝试的结果，由 central 写入 webhook_delivery_log。
// attempt_count 由 central 在 upsert 时自增，不由投递方计算。
type DeliveryRecord struct {
	SubscriptionID uint64  `json:"subscription_id"`
	EventID        string  `json:"event_id"`
	EventType      string  `json:"event_type"`
	RequestBody    string  `json:"request_body"`
	ResponseStatus *int    `json:"response_status,omitempty"`
	ResponseBody   *string `json:"response_body,omitempty"`
	Error          string  `json:"error,omitempty"`
	DurationMS     int64   `json:"duration_ms"`
}

// webhookEvent 是共享流条目解析后的事件信封。
type webhookEvent struct {
	EventID    string          `json:"event_id"`
	EventType  string          `json:"event_type"`
	Source     string          `json:"source"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

// ErrDeliveryRejected 表示不可恢复的订阅端 4xx 拒绝，结果直接进入死信状态。
var ErrDeliveryRejected = errors.New("webhook endpoint rejected the event")

// EventStreams 返回可订阅流，其名称与 Outbox 发布目标一致。
var EventStreams = []string{
	"charge_events_stream",
	"charge_started_stream",
	"charge_ended_stream",
	"charge_payment_confirmed_stream",
	"charge_start_rejected_stream",
	"refund_required_stream",
	"refund_succeeded_stream",
	"device_event_stream",
}

func (d WebhookDeliverer) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (d WebhookDeliverer) batchSize() int {
	if d.BatchSize > 0 {
		return d.BatchSize
	}
	return 100
}

// PublishBatch 消费配置流并投递匹配订阅，把投递结果批量上报 central 后
// 删除已处理的共享流条目，支持崩溃后由持久日志重放。
func (d WebhookDeliverer) PublishBatch(ctx context.Context) (int, error) {
	if d.Stream == nil || d.CentralURL == "" {
		return 0, errors.New("webhook deliverer is not configured")
	}
	subs, err := d.subscriptions(ctx)
	if err != nil {
		return 0, err
	}
	// No active subscriptions means no delivery work. Leave shared streams
	// untouched for their business consumers and future subscriptions.
	if len(subs) == 0 {
		return 0, nil
	}
	streams := d.Streams
	if len(streams) == 0 {
		streams = EventStreams
	}
	var records []DeliveryRecord
	handled := map[string][]string{}
	delivered := 0
	var first error
	for _, stream := range streams {
		entries, err := d.Stream.XRangeN(ctx, stream, "-", "+", int64(d.batchSize())).Result()
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		for _, entry := range entries {
			event, err := parseStreamEntry(entry.Values)
			if err != nil {
				// 无法解析的共享流记录不由此消费者确认或删除。
				// 记录跳过原因，保留消息供所属服务处理。
				log.Printf("webhook delivery skipped an unreadable %s entry %s: %v", stream, entry.ID, err)
				continue
			}
			n, err := d.deliver(ctx, event, subs, &records)
			delivered += n
			if err != nil && first == nil {
				first = err
			}
			if err == nil {
				handled[stream] = append(handled[stream], entry.ID)
			}
		}
	}
	if err := d.report(ctx, records); err != nil && first == nil {
		// 结果未落库时保留共享流条目，下次扫描重投；至少一次语义。
		first = err
	} else {
		for stream, ids := range handled {
			if len(ids) == 0 {
				continue
			}
			if err := d.Stream.XDel(ctx, stream, ids...).Err(); err != nil && first == nil {
				first = err
			}
		}
	}
	return delivered, first
}

// subscriptions 从 central 拉取启用订阅清单；每次批处理只调用一次。
func (d WebhookDeliverer) subscriptions(ctx context.Context) ([]Subscription, error) {
	var reply struct {
		Code int
		Data struct {
			Items []Subscription `json:"items"`
		}
	}
	if err := d.Central.GetJSON(ctx, d.CentralURL, d.ServiceToken, "/api/v1/internal/webhook-subscriptions", &reply); err != nil {
		return nil, err
	}
	if reply.Code != 0 {
		return nil, errors.New("webhook subscriptions rejected")
	}
	return reply.Data.Items, nil
}

// report 把投递结果分块上报 central；空结果不产生请求。
func (d WebhookDeliverer) report(ctx context.Context, records []DeliveryRecord) error {
	for start := 0; start < len(records); start += 100 {
		end := min(start+100, len(records))
		var reply struct {
			Code int
			Data struct {
				Recorded int `json:"recorded"`
			}
		}
		if err := d.Central.Post(ctx, d.CentralURL, d.ServiceToken, "/api/v1/internal/webhook-deliveries/record", map[string]any{"items": records[start:end]}, &reply); err != nil {
			return err
		}
		if reply.Code != 0 {
			return errors.New("webhook delivery records rejected")
		}
	}
	return nil
}

func parseStreamEntry(values map[string]any) (webhookEvent, error) {
	get := func(key string) string {
		switch v := values[key].(type) {
		case string:
			return v
		case []byte:
			return string(v)
		default:
			return ""
		}
	}
	payload := get("payload")
	if payload == "" {
		return webhookEvent{}, errors.New("stream entry has no payload")
	}
	var envelope struct {
		EventID    string          `json:"event_id"`
		EventType  string          `json:"event_type"`
		Source     string          `json:"source"`
		OccurredAt time.Time       `json:"occurred_at"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return webhookEvent{}, err
	}
	if envelope.EventType == "" {
		return webhookEvent{}, errors.New("event has no type")
	}
	source := get("source")
	if source == "" {
		source = envelope.Source
	}
	return webhookEvent{EventID: envelope.EventID, EventType: envelope.EventType, Source: source, OccurredAt: envelope.OccurredAt, Data: envelope.Data}, nil
}

func (d WebhookDeliverer) deliver(ctx context.Context, event webhookEvent, subs []Subscription, records *[]DeliveryRecord) (int, error) {
	count := 0
	var firstErr error
	for _, sub := range subs {
		if !subscriptionWants(sub.EventTypes, event.EventType) {
			continue
		}
		if err := d.deliverOne(ctx, sub, event, records); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		count++
	}
	return count, firstErr
}

func subscriptionWants(types []string, eventType string) bool {
	for _, t := range types {
		if t == "*" || t == eventType {
			return true
		}
	}
	return false
}

// deliverOne 发出一个带签名的请求并登记结果。投递日志就是审计凭证，
// 无论投递成功还是失败都要上报，由 central 落库。
func (d WebhookDeliverer) deliverOne(ctx context.Context, sub Subscription, event webhookEvent, records *[]DeliveryRecord) error {
	body, err := json.Marshal(map[string]any{
		"event_id": event.EventID, "event_type": event.EventType, "source": event.Source,
		"occurred_at": event.OccurredAt, "data": event.Data,
	})
	if err != nil {
		return err
	}
	record := DeliveryRecord{SubscriptionID: sub.ID, EventID: event.EventID, EventType: event.EventType, RequestBody: string(body)}
	if err := netguard.ValidatePublicHTTPS(sub.URL); err != nil {
		record.Error = truncateText(err.Error(), 255)
		*records = append(*records, record)
		return err
	}
	// 签名覆盖实际发送的请求体字节，不对载荷重新序列化。
	timestamp := time.Now().UTC().Unix()
	signature := signPayload(sub.Secret, timestamp, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		record.Error = truncateText(err.Error(), 255)
		*records = append(*records, record)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ChargePilot-Event", event.EventType)
	req.Header.Set("X-ChargePilot-Event-Id", event.EventID)
	req.Header.Set("X-ChargePilot-Timestamp", fmt.Sprint(timestamp))
	req.Header.Set("X-ChargePilot-Signature", signature)

	start := time.Now()
	resp, err := d.client().Do(req)
	record.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		record.Error = truncateText(err.Error(), 255)
		*records = append(*records, record)
		return err
	}
	defer resp.Body.Close()
	// 只读取有限长度的响应前缀，限制诊断日志大小。
	responseBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if readErr != nil {
		readErr = fmt.Errorf("read webhook response: %w", readErr)
	}
	responseText := string(responseBytes)
	record.ResponseStatus = &resp.StatusCode
	record.ResponseBody = &responseText
	*records = append(*records, record)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if readErr != nil {
		return fmt.Errorf("%w: %v", ErrDeliveryRejected, readErr)
	}
	return fmt.Errorf("%w: status %d", ErrDeliveryRejected, resp.StatusCode)
}

func truncateText(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

func signPayload(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
