package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
)

func TestOrderProcessRejectsInvalidIdentityAndPaging(t *testing.T) {
	router := httpapi.NewRouter()
	router.GET("/orders/:id/process", (ResourceAPI{}).orderProcess)
	for _, path := range []string{
		"/orders/0/process", "/orders/-1/process", "/orders/not-an-id/process",
		"/orders/1/process?after_id=-1", "/orders/1/process?after_id=", "/orders/1/process?after_id=1&after_id=2",
		"/orders/1/process?after_id=18446744073709551616", "/orders/1/process?after_id=1.2",
		"/orders/1/process?limit=0", "/orders/1/process?limit=2001", "/orders/1/process?limit=", "/orders/1/process?limit=1&limit=2",
	} {
		reply := httptest.NewRecorder()
		router.ServeHTTP(reply, httptest.NewRequest(http.MethodGet, path, nil))
		if reply.Code != http.StatusBadRequest {
			t.Fatalf("%s returned %d: %s", path, reply.Code, reply.Body.String())
		}
	}
}

func TestOrderProcessRouteRequiresAuthentication(t *testing.T) {
	router := httpapi.NewRouter()
	(ResourceAPI{}).Register(router)
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, httptest.NewRequest(http.MethodGet, "/api/v1/admin/orders/1/process", nil))
	if reply.Code != http.StatusUnauthorized {
		t.Fatalf("process route bypassed order.read authentication: %d", reply.Code)
	}
}

func TestOrderProcessProxyPreservesReadingsAndCursor(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api/v1/internal/charge-orders/CH-fixed/process" || q.Get("charge_order_id") != "17" || q.Get("device_id") != "board&+一" || q.Get("port_no") != "2" || q.Get("after_id") != "10" || q.Get("limit") != "2" || r.Header.Get("X-Service-Token") != "process-service" {
			t.Errorf("incorrect canonical proxy request: %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":13,"ts":"2026-10-01T03:12:34.123Z","power_w":148.5,"charged_kwh":0.0025,"remaining_kwh":1.25,"charged_seconds":30,"remaining_seconds":120,"signal_strength":0,"port_status":0,"voltage_v":220,"temperature_c":-2,"device_status":null}],"next_after_id":13}}`))
	}))
	defer gateway.Close()
	a := ResourceAPI{GatewayURL: gateway.URL, ServiceToken: "process-service"}
	page, err := a.readOrderProcess(context.Background(), orderProcessIdentity{ID: 17, OrderNo: "CH-fixed", DeviceID: "board&+一", PortNo: 2}, orderProcessQuery{AfterID: 10, Limit: 2})
	if err != nil || len(page.Items) != 1 || page.NextAfterID == nil || *page.NextAfterID != 13 {
		t.Fatalf("lost process cursor: %+v %v", page, err)
	}
	point := page.Items[0]
	if point.PowerW != 148.5 || point.ChargedKWh != 0.0025 || point.RemainingKWh != 1.25 || point.SignalStrength != 0 || point.PortStatus == nil || *point.PortStatus != 0 || point.VoltageV == nil || *point.VoltageV != 220 || point.TemperatureC == nil || *point.TemperatureC != -2 || point.DeviceStatus != nil || point.TS.Nanosecond() != 123000000 {
		t.Fatalf("raw readings changed: %+v", point)
	}
}

func TestOrderProcessProxyFailsClosedAndAcceptsActualEmptyPage(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"empty", `{"code":0,"data":{"items":[],"next_after_id":null}}`, 200, true},
		{"dependency", `{"code":5003}`, 503, false},
		{"missing", `{"code":1004}`, 404, false},
		{"service_auth", `{"code":1001}`, 401, false},
		{"invalid_json", `broken`, 200, false},
		{"absent_code", `{"data":{"items":[],"next_after_id":null}}`, 200, false},
		{"null_data", `{"code":0,"data":null}`, 200, false},
		{"null_items", `{"code":0,"data":{"items":null,"next_after_id":null}}`, 200, false},
		{"missing_cursor", `{"code":0,"data":{"items":[]}}`, 200, false},
		{"bad_cursor", `{"code":0,"data":{"items":[],"next_after_id":3}}`, 200, false},
		{"repeated_id", `{"code":0,"data":{"items":[{"id":10,"ts":"2026-10-01T00:00:00Z"}],"next_after_id":null}}`, 200, false},
		{"missing_timestamp", `{"code":0,"data":{"items":[{"id":11}],"next_after_id":null}}`, 200, false},
		{"cursor_skips_rows", `{"code":0,"data":{"items":[{"id":11,"ts":"2026-10-01T00:00:00Z"}],"next_after_id":12}}`, 200, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer gateway.Close()
			a := ResourceAPI{GatewayURL: gateway.URL, ServiceToken: "service"}
			page, err := a.readOrderProcess(context.Background(), orderProcessIdentity{ID: 1, OrderNo: "CH", DeviceID: "board", PortNo: 1}, orderProcessQuery{AfterID: 10, Limit: 2})
			if test.valid {
				if err != nil || page.Items == nil || len(page.Items) != 0 || page.NextAfterID != nil {
					t.Fatalf("actual empty page: %+v %v", page, err)
				}
			} else if !errors.Is(err, errOrderProcess) {
				t.Fatalf("bad dependency response became successful data: %+v %v", page, err)
			}
		})
	}
}

func TestOrderProcessProxyDoesNotRedirectServiceToken(t *testing.T) {
	forwarded := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	defer target.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer gateway.Close()
	_, err := (ResourceAPI{GatewayURL: gateway.URL, ServiceToken: "service"}).readOrderProcess(context.Background(), orderProcessIdentity{ID: 1, OrderNo: "CH", DeviceID: "board", PortNo: 1}, orderProcessQuery{Limit: 1000})
	if !errors.Is(err, errOrderProcess) || forwarded {
		t.Fatalf("redirect leaked service credentials: forwarded=%v err=%v", forwarded, err)
	}
}
