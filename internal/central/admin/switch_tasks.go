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

// 把一套计费规则推到设备上是一件有结果的事，不是一个事务。
//
// 对标的商业后台维护着一份切换日志：状态、操作人、切换前后的方式，
// 以及实际发出去的套餐快照——里面就有一条"进行中"挂了好几天的记录。
// 同步下发没法表达"板子一直不回执"这种情况，
// 而运营最需要看到的恰恰就是这种情况，
// 所以切换在发出任何东西之前，
// 先记成一个任务，每台设备一行。

// registerSwitchTasks 挂载切换任务的列表与详情两个只读接口，权限均为 pricing.read。
// 创建任务由计费模板变更流程内部调用 planSwitchTask 完成，不对外暴露。
func (a ResourceAPI) registerSwitchTasks(r *gin.Engine) {
	r.GET("/api/v1/admin/settings/switch-tasks", a.Auth.Require("pricing.read"), a.switchTasks)
	r.GET("/api/v1/admin/settings/switch-tasks/:id", a.Auth.Require("pricing.read"), a.switchTaskDetail)
}

// createSwitchTask 登记一次计划中的下发。真正的下发不在这里驱动：
// 任务的作用就是让这次尝试可见，某台设备失败时它自己那行会留着，
// 而不是随事务回滚一起消失。
// planSwitchTask 登记一次计划中的计费方式下发：写一条任务头，再为每台目标设备写一条明细，
// 明细初始都是 pending。必须用调用方传入的 tx，与业务改动同事务；
// 真正下发不归这里管，因此一台设备没响应时它那条明细会一直留在表里可见。
// 任务号由 UTC 时间加站点 ID 后四位拼成，唯一性由数据库唯一索引兜底。
//
// createSwitchTask 登记一次计划中的下发。真正的下发不在这里驱动：
// 任务的作用就是让这次尝试可见，某台设备失败时它自己那行会留着，
// 而不是随事务回滚一起消失。
func (a ResourceAPI) planSwitchTask(tx *gorm.DB, actor Profile, stationID uint64, templateID uint64, mode pricing.ChargeMode, targets []switchTarget, c *gin.Context) (uint64, error) {
	// 任务号：SW + UTC 时间戳（秒）+ 站点 ID 后四位，人可读且便于按时间检索。
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

// commonMode 把各设备原本的计费方式收敛成任务头上那一个值：
// 全部一致就给该值，有缺失或不一致返回 nil / "mixed"。
// before 为 false 时不取任何值（此时任务的"切换前"是空），返回 nil。
//
// commonMode 把各板子切换前的方式收敛成任务头上那一个值。
// 各板子本来就不一致的站点拿到的是一个标记，而不是恰好先读到的那一行的方式：
// 一个对任务里大多数设备都错了的汇总，比没有汇总更糟，
// 而逐台明细一直都在。
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

// 这些 JSON tag 不是装饰。其它后台接口一律用 snake_case 应答，
// 而一个要为某一个响应单独写特例的页面，
// 迟早会把两种写法里的一种用错。
//
// switchTaskRow 是切换任务表头的一行，其中 StationName 由联表查出，并不是表里的列。
type switchTaskRow struct {
	ID          uint64     `gorm:"column:id" json:"id"`                     // 任务主键
	TaskNo      string     `gorm:"column:task_no" json:"task_no"`           // 任务号：SW + UTC 时间戳 + 站点 ID 后四位
	StationID   uint64     `gorm:"column:station_id" json:"station_id"`     // 所属站点 ID
	StationName string     `gorm:"column:station_name" json:"station_name"` // 站点名称，来自 station 表的联查结果
	TemplateID  uint64     `gorm:"column:template_id" json:"template_id"`   // 目标计费模板 ID
	ModeBefore  *string    `gorm:"column:mode_before" json:"mode_before"`   // 切换前计费方式；指针，NULL 表示各设备本来就没有独立规则
	ModeAfter   string     `gorm:"column:mode_after" json:"mode_after"`     // 切换后计费方式
	DeviceCount int        `gorm:"column:device_count" json:"device_count"` // 本次任务覆盖的设备数
	Status      string     `gorm:"column:status" json:"status"`             // 任务状态：pending/running/completed/failed
	RequestedBy uint64     `gorm:"column:requested_by" json:"requested_by"` // 发起操作的后台账号 ID
	CreatedAt   time.Time  `gorm:"column:created_at" json:"created_at"`     // 任务创建时间
	CompletedAt *time.Time `gorm:"column:completed_at" json:"completed_at"` // 任务完成时间；指针，未完成为 null
}

// switchTaskItemRow 是切换任务的一条设备明细：每台设备各自成败，互不影响。
type switchTaskItemRow struct {
	ID       uint64  `gorm:"column:id" json:"id"`                             // 明细主键
	DeviceID string  `gorm:"column:device_id" json:"device_id"`               // 设备 ID
	Before   *string `gorm:"column:mode_before" json:"mode_before"`           // 该设备切换前的计费方式；指针，为空表示此前没有独立规则
	After    string  `gorm:"column:mode_after" json:"mode_after"`             // 该设备切换后的计费方式
	Status   string  `gorm:"column:status" json:"status"`                     // 明细状态：pending/running/succeeded/failed
	Snapshot []byte  `gorm:"column:offered_snapshot" json:"offered_snapshot"` // 实际下发给该设备的套餐内容快照
	ErrorMsg *string `gorm:"column:error_msg" json:"error_msg"`               // 失败原因；指针，成功为 null
}

// switchTasks 是 GET /api/v1/admin/settings/switch-tasks 的处理函数：
// 列出切换任务，可按站点和状态过滤，按 ID 倒序，最多返回 200 条，不分页。
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

// switchTaskDetail 返回逐台设备的结果，这正是这个页面存在的理由：
// 只回一句"已下发"，对运营没有任何用处。
//
// switchTaskDetail 是 GET /api/v1/admin/settings/switch-tasks/：id 的处理函数：
// 返回任务头加逐台设备的成败明细，按设备 ID 排序。明细才是这个页面存在的理由：
// 只回一句"已下发"对运营没有任何用处。
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

// recordSwitchResult 由下发链路在写完某台设备后回调。
// 刻意与登记计划分开：两者发生在不同时刻，
// 一直没回应的设备也必须留下一条记录说明这件事。
//
// recordSwitchResult 由下发链路在写完某台设备后回调：按结果把该条明细置为
// succeeded 或 failed，并记下实际下发的套餐快照与失败原因。
// 与登记计划分开是因为两者发生在不同时刻，从不回滚，所以一直没响应的设备也会留下一条记录。
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

// nullableText 把空白字符串转成 NULL 落库，成功时不想在 error_msg 里留一个空串。
func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
