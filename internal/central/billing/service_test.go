package billing

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDispatchRejectsInvalidServiceCredentialsBeforeStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ configured, supplied string }{
		{"", ""}, {"billing-test-secret", ""}, {"billing-test-secret", "wrong-secret"},
	} {
		router := gin.New()
		// 未通过鉴权的请求不得访问数据库或订单服务。
		(Service{ServiceToken: tc.configured}).Register(router)
		req := httptest.NewRequest("POST", "/api/v1/internal/billing/dispatch", nil)
		req.Header.Set("X-Service-Token", tc.supplied)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != 401 || !strings.Contains(response.Body.String(), `"code":1001`) {
			t.Fatalf("unauthorized billing dispatch: %d %s", response.Code, response.Body.String())
		}
	}
}
