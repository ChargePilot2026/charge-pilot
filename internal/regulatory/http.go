package regulatory

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type API struct {
	Queue        Queue
	ServiceToken string
}

func (a API) Register(router *gin.Engine) {
	router.POST("/api/v1/internal/regulatory/events", a.authorized(), a.enqueue)
	router.GET("/api/v1/internal/regulatory/events/:event_id", a.authorized(), a.status)
}

func (a API) authorized() gin.HandlerFunc {
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

func (a API) enqueue(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 70*1024)
	var event Event
	if err := c.ShouldBindJSON(&event); err != nil {
		httpapi.BadRequest(c, "监管对象格式无效")
		return
	}
	created, err := a.Queue.Enqueue(c.Request.Context(), event)
	if err != nil {
		if _, validationErr := ValidateEvent(event); validationErr != nil {
			httpapi.BadRequest(c, "监管对象字段无效: "+validationErr.Error())
			return
		}
		if errors.Is(err, ErrEventConflict) {
			httpapi.Write(c, http.StatusConflict, httpapi.CodeConflict, err.Error(), nil)
			return
		}
		httpapi.Write(c, http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "监管报送队列暂时不可用", nil)
		return
	}
	httpapi.OK(c, gin.H{"event_id": event.EventID, "queued": created})
}

func (a API) status(c *gin.Context) {
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
	err := a.Queue.DB.QueryRowContext(c.Request.Context(), `SELECT object_type,object_key,status,attempts,next_attempt_at,delivered_at,delivered_mode,last_error FROM regulatory_report WHERE event_id = ?`, eventID).
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
