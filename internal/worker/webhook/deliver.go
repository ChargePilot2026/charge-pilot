package worker

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
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WebhookDeliverer 给管理端事件订阅签名并投递，直接读取 central_db 的
// 订阅并记录投递日志。
type WebhookDeliverer struct {
	AdminDB   *gorm.DB
	Stream    *redis.Client
	Client    *http.Client
	BatchSize int
	Streams   []string
}

// event_types 为 JSON 列，先读取文本，再解码为字符串切片。
type webhookSubscription struct {
	ID         uint64   `gorm:"column:id"`
	Name       string   `gorm:"column:name"`
	URL        string   `gorm:"column:url"`
	Secret     string   `gorm:"column:secret"`
	Enabled    bool     `gorm:"column:enabled"`
	EventTypes []string `gorm:"-"`
}

func (webhookSubscription) TableName() string { return "webhook_subscription" }

type webhookEvent struct {
	EventID    string          `json:"event_id"`
	EventType  string          `json:"event_type"`
	Source     string          `json:"source"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

// ErrDeliveryRejected 表示不可恢复的订阅端 4xx 拒绝，结果直接进入死信状态。
var ErrDeliveryRejected = errors.New("webhook endpoint rejected the event")

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

// PublishBatch 消费配置流并投递匹配订阅，先持久化投递状态再确认 Redis 消息，支持崩溃后重放。
func (d WebhookDeliverer) PublishBatch(ctx context.Context) (int, error) {
	if d.AdminDB == nil || d.Stream == nil {
		return 0, errors.New("webhook deliverer is not configured")
	}
	// No active subscriptions means no delivery work. Leave shared streams
	// untouched for their business consumers and future subscriptions.
	var active int64
	if err := d.AdminDB.WithContext(ctx).Table("webhook_subscription").Where("enabled=1 AND deleted_at IS NULL").Count(&active).Error; err != nil {
		return 0, err
	}
	if active == 0 {
		return 0, nil
	}
	streams, err := d.streams(ctx)
	if err != nil {
		return 0, err
	}
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
			n, err := d.deliver(ctx, event)
			delivered += n
			if err != nil && first == nil {
				first = err
			}
			if n > 0 || err == nil {
				d.Stream.XAck(ctx, stream, consumerGroup, entry.ID)
			}
		}
	}
	return delivered, first
}

const consumerGroup = "webhook-delivery"

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

// streams 返回配置的事件流；未配置时使用标准事件流。
func (d WebhookDeliverer) streams(ctx context.Context) ([]string, error) {
	if len(d.Streams) > 0 {
		return d.Streams, nil
	}
	return EventStreams, nil
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

func (d WebhookDeliverer) deliver(ctx context.Context, event webhookEvent) (int, error) {
	subs, err := d.subscriptions(ctx, event.EventType)
	if err != nil {
		return 0, err
	}
	count := 0
	var firstErr error
	for _, sub := range subs {
		if err := d.deliverOne(ctx, sub, event); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		count++
	}
	return count, firstErr
}

func (d WebhookDeliverer) subscriptions(ctx context.Context, eventType string) ([]webhookSubscription, error) {
	rows := []webhookSubscription{}
	if err := d.AdminDB.WithContext(ctx).Table("webhook_subscription").
		Select("id, name, url, secret, enabled").
		Where("enabled = 1 AND deleted_at IS NULL").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	var types []struct {
		ID         uint64 `gorm:"column:id"`
		EventTypes []byte `gorm:"column:event_types"`
	}
	if err := d.AdminDB.WithContext(ctx).Table("webhook_subscription").
		Select("id, CAST(event_types AS CHAR) AS event_types").
		Where("id IN ?", ids).Find(&types).Error; err != nil {
		return nil, err
	}
	byID := map[uint64][]string{}
	for _, row := range types {
		var decoded []string
		if len(row.EventTypes) > 0 {
			// 无效载荷不匹配订阅，跳过该记录，不中止其他投递。
			_ = json.Unmarshal(row.EventTypes, &decoded)
		}
		byID[row.ID] = decoded
	}
	out := make([]webhookSubscription, 0, len(rows))
	for _, row := range rows {
		row.EventTypes = byID[row.ID]
		if subscriptionWants(row.EventTypes, eventType) {
			out = append(out, row)
		}
	}
	return out, nil
}

func subscriptionWants(types []string, eventType string) bool {
	for _, t := range types {
		if t == "*" || t == eventType {
			return true
		}
	}
	return false
}

// deliverOne 发出一个带签名的请求并记录结果。投递日志就是审计凭证，
// 无论投递成功还是失败都要写。
func (d WebhookDeliverer) deliverOne(ctx context.Context, sub webhookSubscription, event webhookEvent) error {
	body, err := json.Marshal(map[string]any{
		"event_id": event.EventID, "event_type": event.EventType, "source": event.Source,
		"occurred_at": event.OccurredAt, "data": event.Data,
	})
	if err != nil {
		return err
	}
	attempt := d.attempts(ctx, sub.ID, event.EventID)
	if err := netguard.ValidatePublicHTTPS(sub.URL); err != nil {
		return d.log(ctx, sub, event, string(body), nil, nil, err, attempt, 0)
	}
	// 签名覆盖实际发送的请求体字节，不对载荷重新序列化。
	timestamp := time.Now().UTC().Unix()
	signature := signPayload(sub.Secret, timestamp, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		return d.log(ctx, sub, event, string(body), nil, nil, err, attempt, 0)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ChargePilot-Event", event.EventType)
	req.Header.Set("X-ChargePilot-Event-Id", event.EventID)
	req.Header.Set("X-ChargePilot-Timestamp", fmt.Sprint(timestamp))
	req.Header.Set("X-ChargePilot-Signature", signature)

	start := time.Now()
	resp, err := d.client().Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return d.log(ctx, sub, event, string(body), nil, nil, err, attempt, elapsed.Milliseconds())
	}
	defer resp.Body.Close()
	// 只读取有限长度的响应前缀，限制诊断日志大小。
	responseBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if readErr != nil {
		readErr = fmt.Errorf("read webhook response: %w", readErr)
	}
	responseText := string(responseBytes)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return d.log(ctx, sub, event, string(body), &resp.StatusCode, &responseText, nil, attempt, elapsed.Milliseconds())
	}
	return d.log(ctx, sub, event, string(body), &resp.StatusCode, &responseText, fmt.Errorf("%w: status %d", ErrDeliveryRejected, resp.StatusCode), attempt, elapsed.Milliseconds())
}

func (d WebhookDeliverer) attempts(ctx context.Context, subscriptionID uint64, eventID string) int {
	var existing struct {
		AttemptCount uint32 `gorm:"column:attempt_count"`
	}
	d.AdminDB.WithContext(ctx).Table("webhook_delivery_log").
		Where("subscription_id = ? AND event_id = ?", subscriptionID, eventID).
		Order("id DESC").Limit(1).Find(&existing)
	return int(existing.AttemptCount) + 1
}

func (d WebhookDeliverer) log(ctx context.Context, sub webhookSubscription, event webhookEvent, request string, status *int, responseText *string, cause error, attempt int, duration int64) error {
	var statusValue any
	if status != nil {
		statusValue = *status
	}
	var responseValue any
	if responseText != nil {
		responseValue = *responseText
	}
	var errorValue any
	if cause != nil {
		errorValue = truncateText(cause.Error(), 255)
	}
	values := map[string]any{
		"subscription_id": sub.ID, "event_id": event.EventID, "event_type": event.EventType,
		"request_body": request, "response_status": statusValue, "response_body": responseValue,
		"error_msg": errorValue, "attempt_count": attempt, "duration_ms": duration,
		"delivered_at": time.Now().UTC(),
	}
	err := d.AdminDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 每个订阅 + 事件只占一行，重试时就地更新。
		// 必须显式列出更新列，
		// 因为 MySQL 无法从 map 构造 ON DUPLICATE KEY 子句。
		return tx.Table("webhook_delivery_log").Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "subscription_id"}, {Name: "event_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"event_type", "request_body", "response_status", "response_body", "error_msg", "attempt_count", "duration_ms", "delivered_at"}),
		}).Create(&values).Error
	})
	if err != nil {
		return err
	}
	if cause != nil {
		return cause
	}
	return nil
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
