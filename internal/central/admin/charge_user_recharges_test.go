package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestChargeUserRechargesRejectInvalidPathAndPagination(t *testing.T) {
	router := gin.New()
	router.GET("/charge-users/:id/recharges", (ResourceAPI{}).chargeUserRecharges)
	for _, path := range []string{
		"/charge-users/0/recharges", "/charge-users/-1/recharges", "/charge-users/invalid/recharges",
		"/charge-users/18446744073709551616/recharges", "/charge-users/1/recharges?page=0",
		"/charge-users/1/recharges?page=1000001", "/charge-users/1/recharges?page=invalid",
		"/charge-users/1/recharges?page_size=0", "/charge-users/1/recharges?page_size=101",
		"/charge-users/1/recharges?page=", "/charge-users/1/recharges?page_size=",
		"/charge-users/1/recharges?page=1&page=2", "/charge-users/1/recharges?page_size=1&page_size=2",
		"/charge-users/1/recharges?user_id=2", "/charge-users/1/recharges?biz_type=charge",
		"/charge-users/1/recharges?status=paid", "/charge-users/1/recharges?page=%zz",
		"/charge-users/1/recharges?page=1;page_size=2", "/charge-users/1/recharges?page=1&page_size=%zz",
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
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/charge-users/1/recharges", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous recharge query returned %d", response.Code)
	}
}
