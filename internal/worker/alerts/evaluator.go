package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Evaluator 按运维配置的规则，把原始遥测转成告警事件。
// telemetry 归 gateway_db 管，规则与告警归 admin_db 管；
// worker 同时持有这两个连接，因此不需要跨库 SQL。
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

// threshold 要么是单个数值，要么是 "between" 用的 [low, high] 区间。
type threshold struct {
	Low  decimal.Decimal
	High decimal.Decimal
}

// Evaluate 读取一次最近的遥测，并套用每条已启用的规则。
// 只有当该设备在这条规则上还没有活跃告警时才会新发告警；
// 指标恢复正常后告警会自动关闭，
// 这样同一个持续故障不会反复呼叫运维。
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
	// 只读取有启用规则在监看的指标，
	// 这样设备规模再大也不会把整张遥测表拉进内存。
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

// 列名与 Go 字段名不一致，所以显式映射；没有这些 tag 时 GORM 扫出来全是零值，
// 任何阈值都匹配不上。
type sample struct {
	DeviceID string          `gorm:"column:device_id"`
	Metric   string          `gorm:"column:metric"`
	Value    decimal.Decimal `gorm:"column:value_num"`
	TS       time.Time       `gorm:"column:ts"`
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
	// value_num 是 DECIMAL 类型，所以先按字符串扫出来再精确转换。
	if err := e.GatewayDB.WithContext(ctx).Table("telemetry").
		Select("device_id, metric, value_num, ts").
		Where("metric IN ? AND value_num IS NOT NULL AND ts >= DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 5 MINUTE)", names).
		Order("ts DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	// 读数为零是合法值，所以扫出来什么就保留什么；SQL 已经排除了
	// decimal 无法表示的 NULL value_num。
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

// evaluate 套用一个比较算子。"between" 两端都含，
// 这样配规则的人不必去推理一个并不存在的边界。
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

// matchesDevice 支持文档里约定的 '*' 通配符，以及完全字面匹配的设备标识。
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
	// 锁住该设备+规则上未关闭的告警，
	// 避免两次评估都决定把它发出来。
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
		// 条件已解除：自动关闭告警，
		// 省得运维去确认一个自己并没有做过的恢复。
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
		// 已经在开：更新观测值，让运维看得到它正在往哪边漂。
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
	// 把新告警分发给每个启用的订阅，让 webhook 与控制台通知
	// 用的是同一个事实来源。
	if err := e.notifySubscribers(ctx, r, s, eventID, thresholdText); err != nil {
		return true, err
	}
	return true, nil
}

// notifySubscribers 为绑定了这条规则或该严重级别的每个订阅，
// 各排一个 webhook 事件。真正的投递发生在 webhook worker 里，
// 所以订阅方故障不会拖慢评估。
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
		// 事件 id 由告警和订阅推导而来，
		// 所以重跑评估必须复用已有的 outbox 行，
		// 而不是撞唯一键失败。
		outboxID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(eventID+fmt.Sprint(*target.SubscriptionID))).String()
		insert := e.AdminDB.WithContext(ctx).Table("event_outbox").Create(map[string]any{
			"event_id": outboxID, "stream": "charge_events_stream", "envelope_json": string(envelope),
		})
		if insert.Error != nil && isDuplicateKey(insert.Error) {
			return nil
		}
		if insert.Error != nil {
			return insert.Error
		}
	}
	return nil
}

// isDuplicateKey 判定 MySQL 唯一约束冲突；对这些确定性事件 id 来说，
// 它意味着这条通知早已入过队。
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	var dup *mysql.MySQLError
	return errors.As(err, &dup) && dup.Number == 1062
}
