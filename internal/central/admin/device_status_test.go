package admin

import (
	"bytes"
	"context"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeviceStatusRejectsInvalidRequestsBeforeWriting(t *testing.T) {
	for _, body := range []string{`{}`, `{"status":"fault"}`, `{"status":"enabled","station_id":9}`, `{"status":"disabled"} {}`} {
		r := httpapi.NewRouter()
		r.PUT("/devices/:id/status", (ResourceAPI{}).setDeviceStatus)
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest("PUT", "/devices/board/status", bytes.NewBufferString(body)))
		if response.Code != 400 {
			t.Fatalf("%s: %d", body, response.Code)
		}
	}
}

func TestDeviceEnrichmentUsesOnlyVisibleDevicesAndPreservesUnknown(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query()["device_id"]
		if r.URL.Path != "/api/v1/internal/device-summaries" || len(ids) != 1 || ids[0] != "visible-board" || r.Header.Get("X-Service-Token") != "service" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"device_id":"visible-board","vendor_name":"测试厂商","last_heartbeat_at":"2026-10-01T03:12:34Z"},{"device_id":"hidden-board","vendor_name":"其他厂商"}]}}`))
	}))
	defer gateway.Close()
	rows := []Device{{DeviceID: "visible-board"}}
	(ResourceAPI{GatewayURL: gateway.URL, ServiceToken: "service"}).enrichDevices(context.Background(), rows)
	if !rows[0].RuntimeAvailable || rows[0].VendorName == nil || *rows[0].VendorName != "测试厂商" || rows[0].LastHeartbeatAt == nil {
		t.Fatalf("missing enrichment: %+v", rows)
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer failing.Close()
	rows = []Device{{DeviceID: "visible-board"}}
	(ResourceAPI{GatewayURL: failing.URL, ServiceToken: "service"}).enrichDevices(context.Background(), rows)
	if rows[0].RuntimeAvailable || rows[0].LastHeartbeatAt != nil {
		t.Fatal("outage fabricated known runtime")
	}
}
