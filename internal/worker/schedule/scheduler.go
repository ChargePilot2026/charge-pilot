package schedule

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

// Handler 是在代码里写死白名单的。
// 数据库行可以改调度，但没法让 worker 去执行任意函数。
type Handler func(context.Context) (uint64, error)

type Scheduler struct {
	DB       *gorm.DB
	Handlers map[string]Handler
}

type Task struct {
	ID                   uint64     `gorm:"column:id"`
	Code                 string     `gorm:"column:task_code"`
	CronExpr             string     `gorm:"column:cron_expr"`
	Enabled              bool       `gorm:"column:enabled"`
	NextRunAt            *time.Time `gorm:"column:next_run_at"`
	LastRunAt            *time.Time `gorm:"column:last_run_at"`
	ConsecutiveFailCount uint32     `gorm:"column:consecutive_fail_count"`
}

type executionLog struct {
	ID            uint64    `gorm:"column:id;primaryKey"`
	TaskCode      string    `gorm:"column:task_code"`
	StartedAt     time.Time `gorm:"column:started_at"`
	CreatedMonth  time.Time `gorm:"column:created_month;primaryKey"`
	Status        string    `gorm:"column:status"`
	TriggeredBy   string    `gorm:"column:triggered_by"`
	TriggerReason string    `gorm:"column:trigger_reason"`
}

func (executionLog) TableName() string { return "task_execution_log" }

var parser = cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

func (s Scheduler) RunDue(ctx context.Context) error {
	if s.DB == nil {
		return errors.New("scheduler database unavailable")
	}
	var due []Task
	now := time.Now().UTC()
	codes := make([]string, 0, len(s.Handlers))
	for code := range s.Handlers {
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return nil
	}
	if err := s.DB.WithContext(ctx).Table("scheduled_task").
		Where("task_code IN ? AND enabled = 1 AND next_run_at <= ? AND (lease_until IS NULL OR lease_until < ?)", codes, now, now).
		Order("next_run_at, id").Limit(10).Find(&due).Error; err != nil {
		return err
	}
	var failures []error
	for _, task := range due {
		if _, err := s.execute(ctx, task, false, "cron", ""); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", task.Code, err))
		}
	}
	return errors.Join(failures...)
}

func (s Scheduler) Trigger(ctx context.Context, code, reason string, force bool) (bool, error) {
	if s.Handlers[code] == nil {
		return false, gorm.ErrRecordNotFound
	}
	if s.DB == nil {
		return false, errors.New("scheduler database unavailable")
	}
	var task Task
	err := s.DB.WithContext(ctx).Table("scheduled_task").Where("task_code = ?", code).Take(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, gorm.ErrRecordNotFound
	}
	if err != nil {
		return false, err
	}
	if !task.Enabled && !force {
		return false, ErrPaused
	}
	return s.execute(ctx, task, force, "admin_api", reason)
}

var ErrPaused = errors.New("scheduled task paused")

// execute 在跑任何 handler 之前，先用数据库租约抢占任务。
// 这能防止两个 worker 副本，或一次手动触发与一次 cron 滴答
// 同时执行同一个任务。租约过期则允许崩溃恢复。
func (s Scheduler) execute(ctx context.Context, task Task, force bool, triggeredBy, reason string) (bool, error) {
	handler := s.Handlers[task.Code]
	if handler == nil {
		return false, fmt.Errorf("handler unavailable for %s", task.Code)
	}
	spec, err := parseSpec(task.CronExpr)
	if err != nil {
		return false, fmt.Errorf("invalid cron for %s: %w", task.Code, err)
	}
	now := time.Now().UTC()
	lease := uuid.NewString()
	claim := s.DB.WithContext(ctx).Table("scheduled_task").
		Where("id = ? AND (lease_until IS NULL OR lease_until < ?)", task.ID, now)
	if !force {
		claim = claim.Where("enabled = 1")
	}
	if triggeredBy == "cron" {
		claim = claim.Where("next_run_at <= ?", now)
	}
	result := claim.Updates(map[string]any{"lease_token": lease, "lease_until": now.Add(2 * time.Minute)})
	if result.Error != nil || result.RowsAffected == 0 {
		return false, result.Error
	}
	// 上一个 worker 可能握着这个租约死掉了。
	// 在写入本次尝试之前，先把它那条孤立的 running 记录收尾。
	if err := s.DB.WithContext(ctx).Table("task_execution_log").
		Where("task_code = ? AND status = 'running' AND started_at < ?", task.Code, now.Add(-2*time.Minute)).
		Updates(map[string]any{"status": "failed", "finished_at": now, "error_msg": "worker stopped before completion"}).Error; err != nil {
		_ = s.DB.WithContext(context.Background()).Table("scheduled_task").Where("id = ? AND lease_token = ?", task.ID, lease).
			Updates(map[string]any{"lease_token": nil, "lease_until": nil}).Error
		return true, err
	}
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	log := executionLog{TaskCode: task.Code, StartedAt: now, CreatedMonth: month,
		Status: "running", TriggeredBy: triggeredBy, TriggerReason: reason}
	insert := s.DB.WithContext(ctx).Create(&log)
	if insert.Error != nil {
		_ = s.DB.WithContext(context.Background()).Table("scheduled_task").Where("id = ? AND lease_token = ?", task.ID, lease).
			Updates(map[string]any{"lease_token": nil, "lease_until": nil}).Error
		return true, insert.Error
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	rows, runErr := runSafely(runCtx, handler)
	cancel()
	finished := time.Now().UTC()
	status := "success"
	message := any(nil)
	failures := uint32(0)
	if runErr != nil {
		status = "failed"
		text := runErr.Error()
		if len(text) > 512 {
			text = text[:512]
		}
		message = text
		failures = task.ConsecutiveFailCount + 1
	}
	// 用一个短生命周期的独立 context，
	// 这样即使执行被中断、进程正在关闭也能把结果记下来，
	// 同时那个有界的租约仍然可以恢复。
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	logErr := s.DB.WithContext(finishCtx).Table("task_execution_log").
		Where("id = ? AND created_month = ?", log.ID, month).
		Updates(map[string]any{"finished_at": finished, "status": status, "affected_rows": rows, "error_msg": message}).Error
	updates := map[string]any{"last_run_at": finished, "next_run_at": spec.Next(finished), "consecutive_fail_count": failures,
		"lease_token": nil, "lease_until": nil}
	if failures >= 5 {
		updates["enabled"] = false
	}
	updated := s.DB.WithContext(finishCtx).Table("scheduled_task").Where("id = ? AND lease_token = ?", task.ID, lease).Updates(updates)
	taskErr := updated.Error
	if taskErr == nil && updated.RowsAffected == 0 {
		taskErr = errors.New("scheduled task lease lost before completion")
	}
	return true, errors.Join(runErr, logErr, taskErr)
}

func parseSpec(raw string) (spec cron.Schedule, err error) {
	// 所有 worker 计划都按 UTC 跑。
	// 拒绝逐行的时区前缀，
	// 这样一次手滑的数据库改动既不会让 cron 解析器 panic，
	// 也不会改变某个任务的时基。
	if strings.Contains(raw, "TZ=") || len(raw) > 64 {
		return nil, errors.New("timezone override or oversized cron expression")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("cron parser panic: %v", recovered)
		}
	}()
	return parser.Parse(raw)
}

func runSafely(ctx context.Context, handler Handler) (rows uint64, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("scheduled task panic: %v", recovered)
		}
	}()
	return handler(ctx)
}
