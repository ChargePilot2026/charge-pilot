package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/delivery"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// RegulatoryQueue 把监管报送事件按 event_id 幂等写入 regulatory_report。
// 同 ID 的 type、key 或 data 不一致时拒绝，避免重写事件事实。
type RegulatoryQueue struct {
	AdminDB *sql.DB
}

var errRegulatoryConflict = errors.New("event_id already exists with different content")

// Enqueue 返回是否新创建；重复且内容一致时返回 (false, nil)。
func (q RegulatoryQueue) Enqueue(ctx context.Context, event delivery.Event) (bool, error) {
	data, err := delivery.ValidateEvent(event)
	if err != nil {
		return false, err
	}
	_, err = q.AdminDB.ExecContext(ctx, `INSERT INTO regulatory_report(event_id,object_type,object_key,payload_json) VALUES(?,?,?,?)`,
		event.EventID, event.ObjectType, event.ObjectKey, string(data))
	if err == nil {
		return true, nil
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return false, err
	}
	var existingType, existingKey, existingData string
	if err := q.AdminDB.QueryRowContext(ctx, `SELECT object_type,object_key,CAST(payload_json AS CHAR) FROM regulatory_report WHERE event_id = ?`, event.EventID).
		Scan(&existingType, &existingKey, &existingData); err != nil {
		return false, err
	}
	var normalized any
	if err := json.Unmarshal([]byte(existingData), &normalized); err != nil {
		return false, err
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return false, err
	}
	if existingType != event.ObjectType || existingKey != event.ObjectKey || !bytes.Equal(canonical, data) {
		return false, errRegulatoryConflict
	}
	return false, nil
}

// RegulatoryEventsAPI 暴露监管报送的入队与状态查询；表在 central_db，
// 投递由 worker 的 delivery 组件触发，经内部端点回执状态。
type RegulatoryEventsAPI struct {
	Queue        RegulatoryQueue
	ServiceToken string
}

func (a RegulatoryEventsAPI) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/regulatory/events", a.authorized(), a.enqueue)
	router.GET("/api/v1/internal/regulatory/events/:event_id", a.authorized(), a.status)
}

func (a RegulatoryEventsAPI) authorized() gin.HandlerFunc {
	return func(c *gin.Context) {
		provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
		expected := sha256.Sum256([]byte(a.ServiceToken))
		if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}

func (a RegulatoryEventsAPI) enqueue(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 70*1024)
	var event delivery.Event
	if err := c.ShouldBindJSON(&event); err != nil {
		httpapi.BadRequest(c, "监管对象格式无效")
		return
	}
	created, err := a.Queue.Enqueue(c.Request.Context(), event)
	if err != nil {
		if _, validationErr := delivery.ValidateEvent(event); validationErr != nil {
			httpapi.BadRequest(c, "监管对象字段无效: "+validationErr.Error())
			return
		}
		if errors.Is(err, errRegulatoryConflict) {
			httpapi.Write(c, http.StatusConflict, httpapi.CodeConflict, err.Error(), nil)
			return
		}
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "监管报送队列暂时不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"event_id": event.EventID, "queued": created})
}

func (a RegulatoryEventsAPI) status(c *gin.Context) {
	eventID := c.Param("event_id")
	if uuid.Validate(eventID) != nil {
		httpapi.BadRequest(c, "event_id 无效")
		return
	}
	var objectType, objectKey, state string
	var attempts int
	var next time.Time
	var delivered sql.NullTime
	var deliveredMode sql.NullString
	var lastError sql.NullString
	err := a.Queue.AdminDB.QueryRowContext(c.Request.Context(), `SELECT object_type,object_key,status,attempts,next_attempt_at,delivered_at,delivered_mode,last_error FROM regulatory_report WHERE event_id = ?`, eventID).
		Scan(&objectType, &objectKey, &state, &attempts, &next, &delivered, &deliveredMode, &lastError)
	if err == sql.ErrNoRows {
		httpapi.Write(c, http.StatusNotFound, httpapi.CodeNotFound, "监管报送事件不存在", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "监管报送状态暂时无法读取", nil)
		return
	}
	var deliveredAt any
	var mode any
	var message any
	if delivered.Valid {
		deliveredAt = delivered.Time.UTC()
	}
	if lastError.Valid {
		message = lastError.String
	}
	if deliveredMode.Valid {
		mode = deliveredMode.String
	}
	httpapi.OK(c, gin.H{"event_id": eventID, "object_type": objectType, "object_key": objectKey,
		"status": state, "attempts": attempts, "next_attempt_at": next.UTC(), "delivered_at": deliveredAt,
		"delivered_mode": mode, "last_error": message})
}
