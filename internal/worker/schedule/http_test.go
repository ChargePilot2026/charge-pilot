package schedule

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRetiredTaskEndpointsReturnNotFoundBeforeDatabaseAccess(t *testing.T) {
	for _, registration := range []struct {
		name     string
		handlers map[string]Handler
	}{
		{name: "unregistered"},
		{name: "nil handler", handlers: map[string]Handler{"alert_evaluate": nil}},
	} {
		t.Run(registration.name, func(t *testing.T) {
			router := gin.New()
			API{Scheduler: Scheduler{Handlers: registration.handlers}, ServiceToken: "schedule-test"}.Register(router)
			for _, endpoint := range []struct{ method, suffix string }{
				{http.MethodGet, "last-run"},
				{http.MethodPost, "trigger"},
			} {
				t.Run(endpoint.method, func(t *testing.T) {
					request := httptest.NewRequest(endpoint.method, "/api/v1/internal/scheduled-tasks/alert_evaluate/"+endpoint.suffix,
						strings.NewReader(`{"trigger_reason":"manual retry","force":true}`))
					request.Header.Set("X-Service-Token", "schedule-test")
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					router.ServeHTTP(response, request)
					if response.Code != http.StatusNotFound {
						t.Fatalf("retired task returned %d: %s", response.Code, response.Body.String())
					}
				})
			}
		})
	}
}
