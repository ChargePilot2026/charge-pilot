package admin

import (
	"bytes"
	"context"
	"encoding/json"
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
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"device_id":"visible-board","vendor_name":"测试厂商","last_heartbeat_at":"2026-10-01T03:12:34Z","signal_strength":0,"signal_at":"2026-10-01T03:12:34Z","ports":[{"port_no":1,"status_code":0,"status_at":"2026-10-01T03:12:34Z"},{"port_no":2,"status_code":null,"status_at":null}]},{"device_id":"hidden-board","vendor_name":"其他厂商","signal_strength":99,"ports":[{"port_no":9,"status_code":9}]}]}}`))
	}))
	defer gateway.Close()
	rows := []Device{{DeviceID: "visible-board"}}
	(ResourceAPI{GatewayURL: gateway.URL, ServiceToken: "service"}).enrichDevices(context.Background(), rows)
	if !rows[0].RuntimeAvailable || rows[0].VendorName == nil || *rows[0].VendorName != "测试厂商" || rows[0].LastHeartbeatAt == nil {
		t.Fatalf("missing enrichment: %+v", rows)
	}
	if rows[0].SignalStrength == nil || *rows[0].SignalStrength != 0 || rows[0].SignalAt == nil || len(rows[0].Ports) != 2 || rows[0].Ports[0].StatusCode == nil || *rows[0].Ports[0].StatusCode != 0 || rows[0].Ports[0].StatusAt == nil || rows[0].Ports[1].StatusCode != nil || rows[0].Ports[1].StatusAt != nil {
		t.Fatalf("zero/unknown device readings were changed: %+v", rows[0])
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer failing.Close()
	rows = []Device{{DeviceID: "visible-board"}}
	(ResourceAPI{GatewayURL: failing.URL, ServiceToken: "service"}).enrichDevices(context.Background(), rows)
	if rows[0].RuntimeAvailable || rows[0].LastHeartbeatAt != nil || rows[0].SignalStrength != nil || rows[0].SignalAt != nil || rows[0].Ports == nil || len(rows[0].Ports) != 0 {
		t.Fatal("outage fabricated known runtime")
	}
}

func TestDeviceEnrichmentNormalizesMissingPortArrays(t *testing.T) {
	for _, body := range []string{`{"code":0,"data":{"items":[{"device_id":"board","ports":null}]}}`, `{"code":0,"data":{"items":[{"device_id":"board"}]}}`} {
		gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		rows := []Device{{DeviceID: "board"}}
		(ResourceAPI{GatewayURL: gateway.URL, ServiceToken: "service"}).enrichDevices(context.Background(), rows)
		gateway.Close()
		if !rows[0].RuntimeAvailable || rows[0].Ports == nil || len(rows[0].Ports) != 0 {
			t.Fatalf("missing ports must stay unknown in an empty array: %+v", rows[0])
		}
	}
	rows := []Device{{DeviceID: "board"}}
	(ResourceAPI{}).enrichDevices(context.Background(), rows)
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Ports []DevicePortStatus `json:"ports"`
	}
	if err := json.Unmarshal(encoded, &out); err != nil || out.Ports == nil || rows[0].RuntimeAvailable {
		t.Fatalf("unconfigured runtime must expose [] without fabricating availability: %s %v", encoded, err)
	}
}
