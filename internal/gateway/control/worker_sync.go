package control

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registerWorkerSync 把 worker 结果同步需要的 gateway_db 读写暴露为内部
// HTTP：启动回执上报、结束事件处理、端口释放、计量证据、结束回执冻结与
// 刷卡事件投递。语义逐项对齐原 worker 直读实现（第 5 批⑤），
// worker 侧不再持有 gateway 库句柄。
func (a TelemetryAPI) registerWorkerSync(r *gin.Engine) {
	r.GET("/api/v1/internal/start-results", a.startResults)
	r.POST("/api/v1/internal/start-results/mark-reported", a.markStartResultsReported)
	r.GET("/api/v1/internal/end-events", a.endEvents)
	r.GET("/api/v1/internal/charge-commands/by-order/:order_id", a.chargeCommandByOrder)
	r.POST("/api/v1/internal/device-events/mark-processed", a.markDeviceEventsProcessed)
	r.POST("/api/v1/internal/ports/release", a.releasePort)
	r.GET("/api/v1/internal/devices/:device_id/meter-samples", a.meterSamples)
	r.POST("/api/v1/internal/charge-end-deliveries/freeze", a.freezeEndDelivery)
	r.GET("/api/v1/internal/card-events", a.cardEvents)
	r.POST("/api/v1/internal/card-events/decide-begin", a.cardDecideBegin)
	r.POST("/api/v1/internal/card-events/decide-finish", a.cardDecideFinish)
	r.POST("/api/v1/internal/card-events/advance", a.cardEventAdvance)
	r.GET("/api/v1/internal/ports/resolve", a.resolvePort)
}

// startResults 返回已回执未上报的启动命令，按 updated_at 升序，最多 50 条。
func (a TelemetryAPI) startResults(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	query := a.DB.WithContext(c.Request.Context()).Model(&workerSyncCommandRow{}).
		Where("status IN ('acked','rejected') AND result_reported = FALSE AND result_code IS NOT NULL AND ack_at IS NOT NULL")
	if filter := c.Query("command_id"); filter != "" {
		if len(filter) > 64 {
			httpapi.BadRequest(c, "command_id 无效")
			return
		}
		query = query.Where("command_id = ?", filter)
	}
	var rows []workerSyncCommandRow
	if err := query.Order("updated_at").Limit(50).Find(&rows).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "启动回执暂不可读取", nil)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{
			"command_id": row.CommandID, "stop_command_id": row.StopCommandID,
			"charge_order_id": row.ChargeOrderID, "order_no": row.OrderNo,
			"device_id": row.DeviceID, "port_no": row.PortNo,
			"port_id": nullableInt(row.PortID), "status": row.Status,
			"result_code": nullableInt16(row.ResultCode), "ack_at": nullableTime(row.AckAt),
		})
	}
	httpapi.OK(c, gin.H{"items": items})
}

// markStartResultsReported 只推进仍处于未上报状态的行，重放安全。
func (a TelemetryAPI) markStartResultsReported(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		CommandIDs []string `json:"command_ids" binding:"required,min=1,max=50"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "上报标记请求无效")
		return
	}
	marked := 0
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for _, id := range in.CommandIDs {
			res := tx.Model(&workerSyncCommandRow{}).
				Where("command_id = ? AND status IN ('acked','rejected') AND result_reported = FALSE", id).
				Update("result_reported", true)
			if res.Error != nil {
				return res.Error
			}
			marked += int(res.RowsAffected)
		}
		return nil
	})
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "上报标记暂不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"marked": marked})
}

// endEvents 返回未处理的刷卡/扫码结束事件（ConsumerType 2/3），按 id 升序，最多 50 条。
func (a TelemetryAPI) endEvents(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var rows []workerSyncEventRow
	err := a.DB.WithContext(c.Request.Context()).
		Select("id, event_json").
		Where("event_type = 'charge_end' AND processed_at IS NULL AND JSON_EXTRACT(event_json, '$.ConsumerType') IN (2,3)").
		Order("id").Limit(50).Find(&rows).Error
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束事件暂不可读取", nil)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{"id": row.ID, "payload": string(row.EventJSON)})
	}
	httpapi.OK(c, gin.H{"items": items})
}

// chargeCommandByOrder 按订单取一条启动命令（原 Take 语义：不保证多行时的选择），不存在返回 found:false。
func (a TelemetryAPI) chargeCommandByOrder(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	orderID, err := strconv.ParseUint(c.Param("order_id"), 10, 64)
	if err != nil || orderID == 0 {
		httpapi.BadRequest(c, "订单 ID 无效")
		return
	}
	var row workerSyncCommandRow
	res := a.DB.WithContext(c.Request.Context()).Model(&workerSyncCommandRow{}).Where("charge_order_id = ?", orderID).Limit(1).Find(&row)
	if res.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "启动命令暂不可读取", nil)
		return
	}
	if res.RowsAffected == 0 {
		httpapi.OK(c, gin.H{"found": false})
		return
	}
	httpapi.OK(c, gin.H{"found": true, "command": gin.H{
		"command_id": row.CommandID, "stop_command_id": row.StopCommandID,
		"charge_order_id": row.ChargeOrderID, "order_no": row.OrderNo,
		"device_id": row.DeviceID, "port_no": row.PortNo,
		"port_id": nullableInt(row.PortID), "status": row.Status,
		"result_code": nullableInt16(row.ResultCode), "ack_at": nullableTime(row.AckAt),
	}})
}

// markDeviceEventsProcessed 只推进未处理的行，返回推进条数。
func (a TelemetryAPI) markDeviceEventsProcessed(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		IDs []uint64 `json:"ids" binding:"required,min=1,max=100"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "处理标记请求无效")
		return
	}
	marked := 0
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for _, id := range in.IDs {
			res := tx.Model(&workerSyncEventRow{}).Where("id = ? AND processed_at IS NULL", id).
				Update("processed_at", gorm.Expr("CURRENT_TIMESTAMP(3)"))
			if res.Error != nil {
				return res.Error
			}
			marked += int(res.RowsAffected)
		}
		return nil
	})
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "处理标记暂不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"marked": marked})
}

// releasePort 在 central 确认结算后释放端口：条件更新占用中的端口；
// 已被释放过的端口返回 already，其余状态视为释放失败（409）。
func (a TelemetryAPI) releasePort(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		PortID  uint64 `json:"port_id" binding:"required"`
		OrderNo string `json:"order_no" binding:"required,max=64"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "端口释放请求无效")
		return
	}
	res := a.DB.WithContext(c.Request.Context()).Model(&workerSyncPortRow{}).
		Where("id = ? AND current_order_id = ? AND status = 'charging'", in.PortID, in.OrderNo).
		Updates(map[string]any{"status": "idle", "current_order_id": nil})
	if res.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "端口释放暂不可用", nil)
		return
	}
	if res.RowsAffected == 1 {
		httpapi.OK(c, gin.H{"released": true, "already": false})
		return
	}
	var port workerSyncPortRow
	if err := a.DB.WithContext(c.Request.Context()).Select("status, current_order_id").Where("id = ?", in.PortID).Take(&port).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "端口状态暂不可读取", nil)
		return
	}
	if port.Status == "idle" && !port.CurrentOrderID.Valid {
		httpapi.OK(c, gin.H{"released": true, "already": true})
		return
	}
	httpapi.Write(c, http.StatusConflict, 2009, "端口在结算后无法释放", nil)
}

// meterSamples 返回计量证据：设备心跳在 [ack_at, end_at] 内且 id 不超过
// end_id 的全部原始事件，按 id 升序，上限 10081（末行为哨兵，与旧实现一致）。
func (a TelemetryAPI) meterSamples(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	device := c.Param("device_id")
	if device == "" || len(device) > 64 {
		httpapi.BadRequest(c, "设备无效")
		return
	}
	ackAt, err1 := time.Parse(time.RFC3339Nano, c.Query("ack_at"))
	endAt, err2 := time.Parse(time.RFC3339Nano, c.Query("end_at"))
	endID, err3 := strconv.ParseUint(c.Query("end_id"), 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || endID == 0 || ackAt.After(endAt) {
		httpapi.BadRequest(c, "计量窗口无效")
		return
	}
	var rows []workerSyncEventRow
	err := a.DB.WithContext(c.Request.Context()).Select("id, event_json").
		Where("device_id=? AND event_type='heartbeat' AND id<=? AND received_at>=? AND received_at<=?", device, endID, ackAt.UTC(), endAt.UTC()).
		Order("id").Limit(10081).Find(&rows).Error
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "计量证据暂不可读取", nil)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{"id": row.ID, "payload": string(row.EventJSON)})
	}
	httpapi.OK(c, gin.H{"items": items})
}

// freezeEndDelivery 冻结结束回执：已有冻结时校验身份（忽略 segments 后
// 逐字节一致）并返回存量；否则落库首写。并发首写冲突按错误重读校验，
// 不做静默空更新（B19 约束）。
func (a TelemetryAPI) freezeEndDelivery(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		DeviceEventID uint64 `json:"device_event_id" binding:"required"`
		ChargeOrderID uint64 `json:"charge_order_id" binding:"required"`
		Payload       string `json:"payload" binding:"required,max=65535"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpapi.BadRequest(c, "冻结请求无效")
		return
	}
	stored, err := a.frozenEndPayload(c, in.DeviceEventID)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束回执暂不可读取", nil)
		return
	}
	if stored == "" {
		err = a.DB.WithContext(c.Request.Context()).Table("charge_end_delivery").Create(map[string]any{
			"device_event_id": in.DeviceEventID, "charge_order_id": in.ChargeOrderID, "payload_json": in.Payload,
		}).Error
		if err == nil {
			httpapi.OK(c, gin.H{"payload": in.Payload, "frozen": false})
			return
		}
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束回执冻结失败", nil)
			return
		}
		// 主键冲突：并发首写已落库，重读并校验身份。
		stored, err = a.frozenEndPayload(c, in.DeviceEventID)
		if err != nil || stored == "" {
			httpapi.Write(c, http.StatusServiceUnavailable, 5003, "结束回执冻结失败", nil)
			return
		}
	}
	if !sameEndIdentity(stored, in.Payload) {
		httpapi.Write(c, http.StatusConflict, 2009, "frozen end result identity conflict", nil)
		return
	}
	httpapi.OK(c, gin.H{"payload": stored, "frozen": true})
}

func (a TelemetryAPI) frozenEndPayload(c *gin.Context, deviceEventID uint64) (string, error) {
	var row struct {
		PayloadJSON []byte `gorm:"column:payload_json"`
	}
	res := a.DB.WithContext(c.Request.Context()).Table("charge_end_delivery").Where("device_event_id=?", deviceEventID).Find(&row)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", nil
	}
	return string(row.PayloadJSON), nil
}

// sameEndIdentity 比较两份结束回执是否同一事实：忽略 meter.segments 后
// 语义一致即可（重放时 segments 允许为空，其余字段必须逐字节相同）。
func sameEndIdentity(stored, candidate string) bool {
	strip := func(raw string) string {
		var value map[string]any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return raw
		}
		if meter, ok := value["meter"].(map[string]any); ok {
			delete(meter, "segments")
		}
		normalized, err := json.Marshal(value)
		if err != nil {
			return raw
		}
		return string(normalized)
	}
	return strip(stored) == strip(candidate)
}

// cardEvents 返回待投递的刷卡事件（含原始事件与已冻结的回复），按事件 id 升序，最多 100 条。
func (a TelemetryAPI) cardEvents(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var rows []struct {
		EventKey     string `gorm:"column:event_key"`
		EventJSON    []byte `gorm:"column:event_json"`
		ResponseJSON []byte `gorm:"column:response_json"`
	}
	err := a.DB.WithContext(c.Request.Context()).Table("card_event_delivery x").
		Select("x.event_key,e.event_json,x.response_json").
		Joins("JOIN device_event e ON e.event_key=x.event_key").
		Where("x.status='pending' AND x.next_attempt_at<=UTC_TIMESTAMP(3)").
		Order("e.id").Limit(100).Find(&rows).Error
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡事件暂不可读取", nil)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{
			"event_key": row.EventKey, "payload": string(row.EventJSON),
			"response": string(row.ResponseJSON),
		})
	}
	httpapi.OK(c, gin.H{"items": items})
}

// cardDecideBegin 锁定投递行并返回决策输入：已有冻结回复时只回回复；
// 否则校验事件身份后返回原始事件。锁在事务内释放，回复由 decide-finish 首写冻结。
func (a TelemetryAPI) cardDecideBegin(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		EventKey string `json:"event_key" binding:"required,max=64"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "event_key 无效")
		return
	}
	var eventJSON string
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var stored struct {
			ResponseJSON []byte `gorm:"column:response_json"`
		}
		if err := tx.Table("card_event_delivery").Clauses(clause.Locking{Strength: "UPDATE"}).Where("event_key=?", in.EventKey).Take(&stored).Error; err != nil {
			return err
		}
		if len(stored.ResponseJSON) > 0 {
			eventJSON = ""
			return errResponseFrozen(string(stored.ResponseJSON))
		}
		var row struct {
			EventJSON []byte `gorm:"column:event_json"`
		}
		if err := tx.Table("device_event").Where("event_key=?", in.EventKey).Take(&row).Error; err != nil {
			return err
		}
		var e protocol.Event
		if json.Unmarshal(row.EventJSON, &e) != nil || e.EventID != in.EventKey || uuid.Validate(e.EventID) != nil {
			return errCardIdentity
		}
		eventJSON = string(row.EventJSON)
		return nil
	})
	var frozen *frozenResponse
	if errors.As(err, &frozen) {
		httpapi.OK(c, gin.H{"event_json": "", "response_json": frozen.response})
		return
	}
	if errors.Is(err, errCardIdentity) {
		httpapi.Write(c, http.StatusConflict, 2009, "card event identity mismatch", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡决策输入暂不可读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"event_json": eventJSON, "response_json": ""})
}

var errCardIdentity = errors.New("card event identity mismatch")

// frozenResponse 是 decide-begin 发现已有冻结回复时的正常出口。
type frozenResponse struct{ response string }

func (e *frozenResponse) Error() string { return "card response already frozen" }

func errResponseFrozen(response string) error { return &frozenResponse{response: response} }

// cardDecideFinish 首写冻结决策回复；并发输掉时返回已存回复，调用方使用存量。
func (a TelemetryAPI) cardDecideFinish(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		EventKey     string `json:"event_key" binding:"required,max=64"`
		ResponseJSON string `json:"response_json" binding:"required,max=65535"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "冻结请求无效")
		return
	}
	res := a.DB.WithContext(c.Request.Context()).Table("card_event_delivery").
		Where("event_key=? AND (response_json IS NULL OR response_json='')", in.EventKey).
		Update("response_json", in.ResponseJSON)
	if res.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡回复冻结暂不可用", nil)
		return
	}
	if res.RowsAffected == 1 {
		httpapi.OK(c, gin.H{"stored": true, "response_json": in.ResponseJSON})
		return
	}
	var stored string
	if err := a.DB.WithContext(c.Request.Context()).Table("card_event_delivery").Select("response_json").Where("event_key=?", in.EventKey).Take(&stored).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡回复暂不可读取", nil)
		return
	}
	httpapi.OK(c, gin.H{"stored": false, "response_json": stored})
}

// cardEventAdvance 推进投递状态：retry 增加尝试次数并 5 秒后重排；done 标记完成。
func (a TelemetryAPI) cardEventAdvance(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	var in struct {
		EventKey string `json:"event_key" binding:"required,max=64"`
		Outcome  string `json:"outcome" binding:"required,oneof=retry done"`
		Error    string `json:"error" binding:"omitempty,max=255"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || uuid.Validate(in.EventKey) != nil {
		httpapi.BadRequest(c, "推进请求无效")
		return
	}
	var res *gorm.DB
	if in.Outcome == "retry" {
		res = a.DB.WithContext(c.Request.Context()).Table("card_event_delivery").Where("event_key=?", in.EventKey).
			Updates(map[string]any{
				"attempts":        gorm.Expr("attempts+1"),
				"next_attempt_at": time.Now().UTC().Add(5 * time.Second),
				"last_error":      in.Error,
			})
	} else {
		res = a.DB.WithContext(c.Request.Context()).Table("card_event_delivery").Where("event_key=?", in.EventKey).
			Updates(map[string]any{"status": "done", "last_error": nil})
	}
	if res.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "刷卡投递状态暂不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"advanced": res.RowsAffected == 1})
}

// resolvePort 按设备与端口序号取二维码，未开通返回 found:false。
func (a TelemetryAPI) resolvePort(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	device := c.Query("device_id")
	port, err := strconv.ParseUint(c.Query("port_no"), 10, 8)
	if err != nil || device == "" || len(device) > 64 || port == 0 {
		httpapi.BadRequest(c, "设备或端口无效")
		return
	}
	var code string
	res := a.DB.WithContext(c.Request.Context()).Table("device_port").
		Where("device_id=? AND port_no=? AND deleted_at IS NULL", device, uint8(port)).
		Pluck("port_code", &code)
	if res.Error != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "端口暂不可解析", nil)
		return
	}
	if code == "" {
		httpapi.OK(c, gin.H{"found": false})
		return
	}
	httpapi.OK(c, gin.H{"found": true, "port_code": code})
}

func nullableInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// workerSyncCommandRow 是 start-results / by-order 端点返回的启动命令列子集。
type workerSyncCommandRow struct {
	CommandID     string        `gorm:"column:command_id;primaryKey"`
	StopCommandID string        `gorm:"column:stop_command_id"`
	ChargeOrderID uint64        `gorm:"column:charge_order_id"`
	OrderNo       string        `gorm:"column:order_no"`
	DeviceID      string        `gorm:"column:device_id"`
	PortNo        uint8         `gorm:"column:port_no"`
	PortID        sql.NullInt64 `gorm:"column:port_id"`
	Status        string        `gorm:"column:status"`
	ResultCode    sql.NullInt16 `gorm:"column:result_code"`
	AckAt         sql.NullTime  `gorm:"column:ack_at"`
}

func (workerSyncCommandRow) TableName() string { return "charge_command" }

// workerSyncEventRow 是 end-events / mark-processed / meter-samples 的事件列子集。
type workerSyncEventRow struct {
	ID          uint64       `gorm:"column:id;primaryKey"`
	EventType   string       `gorm:"column:event_type"`
	EventJSON   []byte       `gorm:"column:event_json"`
	ProcessedAt sql.NullTime `gorm:"column:processed_at"`
}

func (workerSyncEventRow) TableName() string { return "device_event" }

// workerSyncPortRow 是 ports/release 的端口列子集。
type workerSyncPortRow struct {
	ID             uint64         `gorm:"column:id;primaryKey"`
	Status         string         `gorm:"column:status"`
	CurrentOrderID sql.NullString `gorm:"column:current_order_id"`
}

func (workerSyncPortRow) TableName() string { return "device_port" }

func nullableInt16(v sql.NullInt16) any {
	if !v.Valid {
		return nil
	}
	return v.Int16
}

func nullableTime(v sql.NullTime) any {
	if !v.Valid {
		return nil
	}
	return v.Time.UTC()
}
