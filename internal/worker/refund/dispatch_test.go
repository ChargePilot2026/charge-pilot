package refund

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDispatcherRequiresSuccessfulEnvelope(t *testing.T) {
	for _, body := range []string{`{"code":0}`, `{"code":5001}`, `{}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/internal/refunds/dispatch" || r.Header.Get("X-Service-Token") != "test-token" {
					t.Error("request mismatch")
				}
				w.Write([]byte(body))
			}))
			defer server.Close()
			err := (Dispatcher{CentralURL: server.URL, ServiceToken: "test-token"}).Run(context.Background())
			if (err == nil) != (body == `{"code":0}`) {
				t.Fatal(err)
			}
		})
	}
}
