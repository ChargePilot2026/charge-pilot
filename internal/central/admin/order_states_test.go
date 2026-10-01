package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOrderQueriesRejectInvalidIndependentFilters(t *testing.T) {
	router := gin.New()
	api := ResourceAPI{}
	router.GET("/orders", api.orders)
	router.GET("/payment-orders", api.paymentOrders)
	for _, path := range []string{
		"/orders?business_status=paid", "/orders?payment_status=charging", "/orders?start_source=wallet",
		"/payment-orders?biz_type=charge_debt", "/payment-orders?pay_method=card",
		"/payment-orders?payment_status=charging", "/payment-orders?page_size=101",
		"/payment-orders?order_no=" + strings.Repeat("x", 65),
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBothOrderListsRequireAuthentication(t *testing.T) {
	router := gin.New()
	(ResourceAPI{}).Register(router)
	for _, path := range []string{"/api/v1/admin/orders", "/api/v1/admin/payment-orders"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}
