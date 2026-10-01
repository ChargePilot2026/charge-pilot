package schedule

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type API struct {
	Scheduler    Scheduler
	ServiceToken string
}

func (a API) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/scheduled-tasks/:task_code/last-run", a.authorized(), a.lastRun)
	router.POST("/api/v1/internal/scheduled-tasks/:task_code/trigger", a.authorized(), a.trigger)
}

func (a API) authorized() gin.HandlerFunc {
	return func(c *gin.Context) {
		provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
		expected := sha256.Sum256([]byte(a.ServiceToken))
		if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}

func (a API) task(c *gin.Context) (Task, bool) {
	code := strings.TrimSpace(c.Param("task_code"))
	if code == "" || len(code) > 64 {
		httpapi.BadRequest(c, "task_code 无效")
		return Task{}, false
	}
	if a.Scheduler.Handlers[code] == nil {
		httpapi.Write(c, http.StatusNotFound, 1004, "定时任务不存在", nil)
		return Task{}, false
	}
	var task Task
	err := a.Scheduler.DB.WithContext(c.Request.Context()).Table("scheduled_task").Where("task_code = ?", code).Take(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusNotFound, 1004, "定时任务不存在", nil)
		return Task{}, false
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "定时任务暂时无法读取", nil)
		return Task{}, false
	}
	return task, true
}

func (a API) lastRun(c *gin.Context) {
	task, ok := a.task(c)
	if !ok {
		return
	}
	var row struct {
		StartedAt  time.Time  `gorm:"column:started_at"`
		FinishedAt *time.Time `gorm:"column:finished_at"`
		Status     string     `gorm:"column:status"`
		ErrorMsg   *string    `gorm:"column:error_msg"`
	}
	err := a.Scheduler.DB.WithContext(c.Request.Context()).Table("task_execution_log").
		Where("task_code = ?", task.Code).Order("started_at DESC").Take(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "执行记录暂时无法读取", nil)
		return
	}
	var duration any
	var status any
	var lastRun any
	var errorMessage any
	if err == nil {
		status, lastRun, errorMessage = row.Status, row.StartedAt.UTC(), row.ErrorMsg
		if row.FinishedAt != nil {
			duration = row.FinishedAt.Sub(row.StartedAt).Milliseconds()
		}
	}
	httpapi.OK(c, gin.H{"task_code": task.Code, "enabled": task.Enabled, "last_run_at": lastRun,
		"last_run_status": status, "last_run_duration_ms": duration, "error_message": errorMessage,
		"consecutive_fail_count": task.ConsecutiveFailCount, "next_run_at": task.NextRunAt})
}

func (a API) trigger(c *gin.Context) {
	task, ok := a.task(c)
	if !ok {
		return
	}
	var input struct {
		TriggerReason string `json:"trigger_reason"`
		Force         bool   `json:"force"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || len(strings.TrimSpace(input.TriggerReason)) == 0 || len(input.TriggerReason) > 256 {
		httpapi.BadRequest(c, "trigger_reason 须为 1 至 256 字符")
		return
	}
	if !task.Enabled && !input.Force {
		httpapi.Write(c, http.StatusConflict, 1005, "任务已暂停", nil)
		return
	}
	completed, err := a.Scheduler.execute(c.Request.Context(), task, input.Force, "admin_api", strings.TrimSpace(input.TriggerReason))
	if !completed && err == nil {
		httpapi.Write(c, http.StatusConflict, 1005, "任务正在执行", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "任务执行失败: "+err.Error(), nil)
		return
	}
	httpapi.OK(c, gin.H{"task_code": task.Code, "status": "success"})
}
