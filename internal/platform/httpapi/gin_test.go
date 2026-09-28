package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGinTraceRejectsUnsafeRequestID(t *testing.T) {
	router := NewRouter()
	router.GET("/test", func(c *gin.Context) { OK(c, nil) })
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Request-ID", "bad id")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code)
	}
	var body Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.RequestID) != 32 || body.TraceID != body.RequestID || response.Header().Get("X-Request-ID") != body.RequestID {
		t.Fatal(body)
	}
}
