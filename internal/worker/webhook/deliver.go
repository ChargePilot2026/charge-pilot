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
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/netguard"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WebhookDeliverer signs and posts admin event subscriptions. Subscriptions are
// stored in admin_db, which the worker already owns.
type WebhookDeliverer struct {
	AdminDB   *gorm.DB
	Stream    *redis.Client
	Client    *http.Client
	BatchSize int
	Streams   []string
}

type webhookSubscription struct {
	ID         uint64
	Name       string
	URL        string
	EventTypes []string
	Secret     string
	Enabled    bool
}

func (webhookSubscription) TableName() string { return "webhook_subscription" }

type webhookEvent struct {
	EventID    string          `json:"event_id"`
	EventType  string          `json:"event_type"`
	Source     string          `json:"source"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

// ErrDeliveryRejected marks a permanent failure: retrying cannot fix a 4xx body
// the subscriber rejects, so the entry moves straight to the dead-letter state.
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

// PublishBatch consumes pending events from the configured streams and delivers
// each to every matching subscription. Delivery state is persisted before the
// Redis ack, so a crash replays rather than loses the event.
func (d WebhookDeliverer) PublishBatch(ctx context.Context) (int, error) {
	if d.AdminDB == nil || d.Stream == nil {
		return 0, errors.New("webhook deliverer is not configured")
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
				// A malformed entry cannot be delivered; drop it explicitly so it
				// does not block the stream forever.
				d.Stream.XDel(ctx, stream, entry.ID)
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

// EventStreams lists the streams a subscriber may be attached to. These are the
// same stream names the outbox publisher writes, so no extra configuration is
// required to route a new event type to webhook delivery.
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

// streams returns the configured event streams, defaulting to the standard set.
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
		Where("enabled = 1 AND deleted_at IS NULL").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]webhookSubscription, 0, len(rows))
	for _, sub := range rows {
		if subscriptionWants(sub.EventTypes, eventType) {
			out = append(out, sub)
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

// deliverOne posts a single signed request and records the outcome. The delivery
// log is the audit trail, so it is written whether the post succeeds or fails.
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
	// The signature covers the exact bytes sent, so a subscriber can verify the
	// payload it received rather than a re-serialized variant.
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
	// Read only a bounded prefix of the reply; the stored log keeps diagnostics
	// without letting an oversized response bloat the table.
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
		// Upsert so a retried event keeps one row per subscription instead of
		// appending a duplicate for every attempt.
		return tx.Table("webhook_delivery_log").
			Where("subscription_id = ? AND event_id = ?", sub.ID, event.EventID).
			Assign(values).Clauses(clause.OnConflict{UpdateAll: true}).Create(&values).Error
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
