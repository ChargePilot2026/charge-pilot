package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const requestIDKey = "chargepilot.request_id"

type Envelope struct {
	Code      int        `json:"code"`
	Message   string     `json:"message"`
	Data      any        `json:"data"`
	RequestID string     `json:"request_id"`
	TraceID   string     `json:"trace_id"`
	Errors    []FieldErr `json:"errors,omitempty"`
}

type FieldErr struct {
	Field string `json:"field"`
	Rule  string `json:"rule"`
}

func NewRouter(metrics ...*Metrics) *gin.Engine {
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.Use(gin.Recovery(), Trace())
	// 请求结束后读取 FullPath 路由模板；未匹配路径使用统一标签，限制指标基数。
	for _, m := range metrics {
		if m != nil {
			router.Use(m.Middleware())
		}
	}
	router.HandleMethodNotAllowed = true
	router.NoRoute(func(c *gin.Context) {
		Write(c, http.StatusNotFound, CodeNotFound, "接口尚未提供，请确认服务版本", nil)
	})
	router.NoMethod(func(c *gin.Context) {
		Write(c, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "请求方法不支持", nil)
	})
	return router
}

func Trace() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if len(id) == 0 || len(id) > 128 || !validID(id) {
			id = randomID()
		}
		c.Set(requestIDKey, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func RequestID(c *gin.Context) string { return c.GetString(requestIDKey) }

func Write(c *gin.Context, status, code int, message string, data any, fields ...FieldErr) {
	c.Header("Cache-Control", "no-store")
	id := RequestID(c)
	c.JSON(status, Envelope{Code: code, Message: message, Data: data, RequestID: id, TraceID: id, Errors: fields})
}

func OK(c *gin.Context, data any) { Write(c, http.StatusOK, 0, "ok", data) }

func BadRequest(c *gin.Context, message string, fields ...FieldErr) {
	Write(c, http.StatusBadRequest, CodeBadRequest, message, nil, fields...)
}

func validID(id string) bool {
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
