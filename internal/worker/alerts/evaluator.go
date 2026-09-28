package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Evaluator turns raw telemetry into alert events using the operator-configured
// rules. gateway_db owns telemetry and admin_db owns rules and alerts; the worker
// holds both connections, so no cross-schema SQL is required.
type Evaluator struct {
	GatewayDB *gorm.DB
	AdminDB   *gorm.DB
	BatchSize int
}

type rule struct {
	ID              uint64
	Name            string
	DeviceIDPattern string
	Metric          string
	Op              string
	Threshold       []byte
	WindowSeconds   uint32
	Severity        string
	Enabled         bool
}

func (rule) TableName() string { return "alert_rule" }

// threshold is either a single number or a [low, high] pair for "between".
type threshold struct {
	Low  decimal.Decimal
	High decimal.Decimal
}

// Evaluate reads recent telemetry once and applies every enabled rule. An alert
// is raised only when the device had no active alert for that rule, and is
// auto-resolved once the metric returns to normal, so operators are not paged
// repeatedly for one ongoing fault.
func (e Evaluator) Evaluate(ctx context.Context) (int, error) {
	if e.GatewayDB == nil || e.AdminDB == nil {
		return 0, errors.New("alert evaluator is not configured")
	}
	limit := e.BatchSize
	if limit <= 0 {
		limit = 500
	}
	rules, err := e.rules(ctx)
	if err != nil {
		return 0, err
	}
	if len(rules) == 0 {
		return 0, nil
	}
	// Only metrics some enabled rule watches are read, so a busy fleet does not
	// pull the whole telemetry table into memory.
	metrics := map[string]bool{}
	for _, r := range rules {
		metrics[r.Metric] = true
	}
	samples, err := e.samples(ctx, metrics, limit)
	if err != nil {
		return 0, err
	}
	raised := 0
	var first error
	for _, sample := range samples {
		for _, r := range rules {
			if r.Metric != sample.Metric || !matchesDevice(r.DeviceIDPattern, sample.DeviceID) {
				continue
			}
			bound, err := parseThreshold(r.Threshold)
			if err != nil {
				if first == nil {
					first = fmt.Errorf("rule %d threshold: %w", r.ID, err)
				}
				continue
			}
			breached := evaluate(r.Op, sample.Value, bound)
			changed, err := e.reconcileAlert(ctx, r, sample, breached)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			if changed {
				raised++
			}
		}
	}
	return raised, first
}

func (e Evaluator) rules(ctx context.Context) ([]rule, error) {
	rows := []rule{}
	if err := e.AdminDB.WithContext(ctx).Table("alert_rule").
		Where("enabled = 1 AND deleted_at IS NULL").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

type sample struct {
	DeviceID string
	Metric   string
	Value    decimal.Decimal
	TS       time.Time
}

func (e Evaluator) samples(ctx context.Context, metrics map[string]bool, limit int) ([]sample, error) {
	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, nil
	}
	rows := []sample{}
	// value_num is DECIMAL, so it is scanned as a string and converted exactly.
	if err := e.GatewayDB.WithContext(ctx).Table("telemetry").
		Select("device_id, metric, value_num, ts").
		Where("metric IN ? AND value_num IS NOT NULL AND ts >= DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 5 MINUTE)", names).
		Order("ts DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	// A zero reading is legitimate, so rows are kept as scanned; the SQL already
	// excludes the NULL value_num that decimal cannot represent.
	if len(rows) == 0 {
		return nil, nil
	}
	return rows, nil
}

func parseThreshold(raw []byte) (threshold, error) {
	var single decimal.Decimal
	if err := json.Unmarshal(raw, &single); err == nil {
		return threshold{Low: single, High: single}, nil
	}
	var pair []decimal.Decimal
	if err := json.Unmarshal(raw, &pair); err == nil && len(pair) == 2 && pair[0].LessThanOrEqual(pair[1]) {
		return threshold{Low: pair[0], High: pair[1]}, nil
	}
	return threshold{}, errors.New("threshold must be a number or an ordered [low, high] pair")
}

// evaluate applies one operator. "between" is inclusive on both ends so a rule
// author does not have to reason about a boundary that does not exist.
func evaluate(op string, value decimal.Decimal, bound threshold) bool {
	switch op {
	case ">":
		return value.GreaterThan(bound.Low)
	case "<":
		return value.LessThan(bound.Low)
	case ">=":
		return value.GreaterThanOrEqual(bound.Low)
	case "<=":
		return value.LessThanOrEqual(bound.Low)
	case "==":
		return value.Equal(bound.Low)
	case "!=":
		return !value.Equal(bound.Low)
	case "between":
		return value.GreaterThanOrEqual(bound.Low) && value.LessThanOrEqual(bound.High)
	default:
		return false
	}
}

// matchesDevice supports the documented '*' wildcard plus literal identifiers.
func matchesDevice(pattern, deviceID string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == deviceID
	}
	parts := strings.Split(pattern, "*")
	rest := deviceID
	for i, part := range parts {
		if part == "" {
			continue
		}
		index := strings.Index(rest, part)
		if index < 0 {
			return false
		}
		if i == 0 && index != 0 {
			return false
		}
		rest = rest[index+len(part):]
	}
	if last := parts[len(parts)-1]; last != "" && !strings.HasSuffix(deviceID, last) {
		return false
	}
	return true
}

func (e Evaluator) reconcileAlert(ctx context.Context, r rule, s sample, breached bool) (bool, error) {
	month := time.Date(s.TS.Year(), s.TS.Month(), 1, 0, 0, 0, 0, time.UTC)
	var existing struct {
		ID     uint64 `gorm:"column:id"`
		Status string `gorm:"column:status"`
	}
	// Lock the open alert for this device+rule so two evaluations cannot both
	// decide to raise it.
	found := e.AdminDB.WithContext(ctx).Table("alert_event").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND rule_id = ? AND created_month = ? AND status IN ('active','acknowledged')", s.DeviceID, r.ID, month).
		Order("id DESC").Take(&existing)
	if found.Error != nil && !errors.Is(found.Error, gorm.ErrRecordNotFound) {
		return false, found.Error
	}
	thresholdText := s.Value.String()
	if !breached {
		if found.RowsAffected == 0 {
			return false, nil
		}
		// The condition cleared: close the alert automatically so the operator
		// does not have to confirm a recovery they did not perform.
		if existing.Status == "auto_resolved" {
			return false, nil
		}
		if err := e.AdminDB.WithContext(ctx).Table("alert_event").
			Where("id = ? AND created_month = ?", existing.ID, month).
			Updates(map[string]any{"status": "auto_resolved", "resolved_at": gorm.Expr("UTC_TIMESTAMP(3)"), "note": "指标恢复正常，系统自动恢复"}).Error; err != nil {
			return false, err
		}
		return true, nil
	}
	if found.RowsAffected > 0 {
		// Already open: update the observed value so operators see it drift.
		if err := e.AdminDB.WithContext(ctx).Table("alert_event").
			Where("id = ? AND created_month = ?", existing.ID, month).
			Updates(map[string]any{"value": s.Value}).Error; err != nil {
			return false, err
		}
		return false, nil
	}
	eventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("alert:%d:%s:%s:%s", r.ID, s.DeviceID, s.Metric, s.TS.UTC().Format(time.RFC3339Nano)))).String()
	row := map[string]any{
		"device_id": s.DeviceID, "rule_id": r.ID, "severity": r.Severity, "metric": s.Metric,
		"value": s.Value, "threshold": thresholdText, "status": "active", "event_id": eventID, "created_month": month,
	}
	if err := e.AdminDB.WithContext(ctx).Table("alert_event").Create(row).Error; err != nil {
		return false, err
	}
	// Fan the new alert out to every enabled subscription so webhook and
	// in-console routing use the same source of truth.
	if err := e.notifySubscribers(ctx, r, s, eventID, thresholdText); err != nil {
		return true, err
	}
	return true, nil
}

// notifySubscribers queues a webhook event for each subscription bound to this
// rule or its severity. Delivery itself happens in the webhook worker, so a
// subscriber outage cannot slow down evaluation.
func (e Evaluator) notifySubscribers(ctx context.Context, r rule, s sample, eventID, thresholdText string) error {
	var targets []struct {
		SubscriptionID *uint64 `gorm:"column:webhook_subscription_id"`
		Enabled        bool    `gorm:"column:enabled"`
	}
	if err := e.AdminDB.WithContext(ctx).Table("alert_subscription").
		Where("enabled = 1 AND (rule_id = ? OR (rule_id IS NULL AND severity = ?))", r.ID, r.Severity).
		Find(&targets).Error; err != nil {
		return err
	}
	for _, target := range targets {
		if target.SubscriptionID == nil {
			continue
		}
		envelope, err := json.Marshal(map[string]any{
			"event_id": eventID, "event_type": "alert", "source": "alert_engine",
			"occurred_at": time.Now().UTC(),
			"data": map[string]any{
				"device_id": s.DeviceID, "rule_id": r.ID, "severity": r.Severity, "metric": s.Metric,
				"value": s.Value.String(), "threshold": thresholdText,
			},
		})
		if err != nil {
			return err
		}
		if err := e.AdminDB.WithContext(ctx).Table("event_outbox").Create(map[string]any{
			"event_id": uuid.NewSHA1(uuid.NameSpaceURL, []byte(eventID+fmt.Sprint(*target.SubscriptionID))).String(),
			"stream":   "charge_events_stream", "envelope_json": string(envelope),
		}).Error; err != nil {
			return err
		}
	}
	return nil
}
