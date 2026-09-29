package admin

import (
	"fmt"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Pushing a tariff onto a device is a job with a result, not a transaction.
//
// The commercial back office this was modelled on keeps a switch log with a
// status, an operator, the mode before and after, and a snapshot of the
// packages that were actually sent — and it contains a record that sat "in
// progress" for days. A synchronous apply cannot express a board that never
// acknowledged, which is exactly the case an operator most needs to see, so a
// switch is recorded as a task with one row per device before anything is sent.

func (a ResourceAPI) registerSwitchTasks(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/switch-tasks", a.Auth.Require("pricing.read"), a.switchTasks)
	r.GET("/api/v1/admin/settings/switch-tasks/:id", a.Auth.Require("pricing.read"), a.switchTaskDetail)
}

// createSwitchTask records a planned rollout. The downlink itself is not driven
// here: a task exists so the attempt is visible, and a device that fails keeps
// its own row rather than vanishing into a rolled-back transaction.
func (a ResourceAPI) planSwitchTask(tx *gorm.DB, actor Profile, stationID uint64, templateID uint64, mode pricing.ChargeMode, targets []switchTarget, c *gin.Context) (uint64, error) {
	number := "SW" + time.Now().UTC().Format("20060102150405") + fmt.Sprintf("%04d", stationID%10000)
	row := map[string]any{
		"task_no": number, "station_id": stationID, "template_id": templateID,
		"mode_before": commonMode(targets, true), "mode_after": string(mode),
		"device_count": len(targets), "status": "pending", "requested_by": actor.ID,
	}
	if err := tx.Table("pricing_switch_task").Create(row).Error; err != nil {
		return 0, err
	}
	var taskID uint64
	if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&taskID).Error; err != nil {
		return 0, err
	}
	for _, target := range targets {
		item := map[string]any{
			"task_id": taskID, "device_id": target.DeviceID,
			"mode_before": target.Before, "mode_after": string(mode), "status": "pending",
		}
		if err := tx.Table("pricing_switch_task_item").Create(item).Error; err != nil {
			return 0, err
		}
	}
	if err := resourceAudit(tx, actor, "pricing.switch.plan", "pricing_switch_task", taskID, nil, row, c.ClientIP(), httpapi.RequestID(c)); err != nil {
		return 0, err
	}
	return taskID, nil
}

// commonMode collapses the per-board before-modes into the one figure the task
// header carries. A yard where the boards did not all agree gets a marker, not
// the mode of whichever row was read first: a summary that is wrong for most of
// the task is worse than no summary, and the per-device rows are always there.
func commonMode(targets []switchTarget, before bool) any {
	mode := ""
	for _, target := range targets {
		value := target.Before
		if !before {
			continue
		}
		if value == "" {
			return nil
		}
		if mode == "" {
			mode = value
			continue
		}
		if mode != value {
			return "mixed"
		}
	}
	if mode == "" {
		return nil
	}
	return mode
}

// The JSON tags are not decoration. Every other admin endpoint answers in
// snake_case, and a screen that has to special-case one response is a screen
// that gets one of the two spellings wrong.
type switchTaskRow struct {
	ID          uint64     `gorm:"column:id" json:"id"`
	TaskNo      string     `gorm:"column:task_no" json:"task_no"`
	StationID   uint64     `gorm:"column:station_id" json:"station_id"`
	StationName string     `gorm:"column:station_name" json:"station_name"`
	TemplateID  uint64     `gorm:"column:template_id" json:"template_id"`
	ModeBefore  *string    `gorm:"column:mode_before" json:"mode_before"`
	ModeAfter   string     `gorm:"column:mode_after" json:"mode_after"`
	DeviceCount int        `gorm:"column:device_count" json:"device_count"`
	Status      string     `gorm:"column:status" json:"status"`
	RequestedBy uint64     `gorm:"column:requested_by" json:"requested_by"`
	CreatedAt   time.Time  `gorm:"column:created_at" json:"created_at"`
	CompletedAt *time.Time `gorm:"column:completed_at" json:"completed_at"`
}

type switchTaskItemRow struct {
	ID       uint64  `gorm:"column:id" json:"id"`
	DeviceID string  `gorm:"column:device_id" json:"device_id"`
	Before   *string `gorm:"column:mode_before" json:"mode_before"`
	After    string  `gorm:"column:mode_after" json:"mode_after"`
	Status   string  `gorm:"column:status" json:"status"`
	Snapshot []byte  `gorm:"column:offered_snapshot" json:"offered_snapshot"`
	ErrorMsg *string `gorm:"column:error_msg" json:"error_msg"`
}

func (a ResourceAPI) switchTasks(c *gin.Context) {
	rows := []switchTaskRow{}
	query := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_switch_task t").
		Select("t.id,t.task_no,t.station_id,s.name AS station_name,t.template_id,t.mode_before,t.mode_after,t.device_count,t.status,t.requested_by,t.created_at,t.completed_at").
		Joins("LEFT JOIN station s ON s.id=t.station_id AND s.deleted_at IS NULL")
	if id := c.Query("station_id"); id != "" {
		query = query.Where("t.station_id=?", id)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("t.status=?", status)
	}
	if err := query.Order("t.id DESC").Limit(200).Find(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, rows)
}

// switchTaskDetail returns the per-device outcome, which is the reason the
// screen exists: "the switch was applied" is not a useful answer on its own.
func (a ResourceAPI) switchTaskDetail(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var task switchTaskRow
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_switch_task t").
		Select("t.id,t.task_no,t.station_id,s.name AS station_name,t.template_id,t.mode_before,t.mode_after,t.device_count,t.status,t.requested_by,t.created_at,t.completed_at").
		Joins("LEFT JOIN station s ON s.id=t.station_id AND s.deleted_at IS NULL").
		Where("t.id=?", id).Take(&task).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	items := []switchTaskItemRow{}
	if err := a.Store.AdminDB.WithContext(c.Request.Context()).Table("pricing_switch_task_item").
		Select("id,device_id,mode_before,mode_after,status,offered_snapshot,error_msg").
		Where("task_id=?", id).Order("device_id").Find(&items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"task": task, "items": items})
}

// recordSwitchResult is called by the downlink once a device has been written
// to. It is deliberately separate from planning: the two happen at different
// times, and a device that never answers must keep a row saying so.
func recordSwitchResult(tx *gorm.DB, taskID, itemID uint64, ok bool, snapshot any, message string) error {
	status := "succeeded"
	if !ok {
		status = "failed"
	}
	return tx.Table("pricing_switch_task_item").Where("id=?", itemID).
		Updates(map[string]any{
			"status": status, "offered_snapshot": snapshot,
			"error_msg": nullableText(message), "completed_at": time.Now().UTC(),
		}).Error
}

func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
