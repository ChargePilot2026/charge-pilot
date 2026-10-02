package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestChargeUserRechargesRejectInvalidPathAndPagination(t *testing.T) {
	router := gin.New()
	router.GET("/users/:id/recharges", (ResourceAPI{}).chargeUserRecharges)
	for _, path := range []string{
		"/users/0/recharges", "/users/-1/recharges", "/users/invalid/recharges",
		"/users/18446744073709551616/recharges", "/users/1/recharges?page=0",
		"/users/1/recharges?page=1000001", "/users/1/recharges?page=invalid",
		"/users/1/recharges?page_size=0", "/users/1/recharges?page_size=101",
		"/users/1/recharges?page=", "/users/1/recharges?page_size=",
		"/users/1/recharges?page=1&page=2", "/users/1/recharges?page_size=1&page_size=2",
		"/users/1/recharges?user_id=2", "/users/1/recharges?biz_type=charge",
		"/users/1/recharges?status=paid", "/users/1/recharges?page=%zz",
		"/users/1/recharges?page=1;page_size=2", "/users/1/recharges?page=1&page_size=%zz",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid query returned %d", response.Code)
			}
		})
	}
}

func TestChargeUserRechargesRequireAuthentication(t *testing.T) {
	router := gin.New()
	(ResourceAPI{}).registerChargeUsers(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/users/1/recharges", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous recharge query returned %d", response.Code)
	}
}
