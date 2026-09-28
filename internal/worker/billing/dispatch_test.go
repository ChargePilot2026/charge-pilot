package billing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBillingDispatchCallsProtectedRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/internal/billing/dispatch" || r.Header.Get("X-Service-Token") != "local-billing-test" {
			t.Error("wrong internal billing request")
		}
		w.Write([]byte(`{"code":0,"data":{"completed":1}}`))
	}))
	defer server.Close()
	if err := (Dispatcher{CentralURL: server.URL, ServiceToken: "local-billing-test"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}
