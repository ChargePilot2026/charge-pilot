package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
)

func deviceEditPayload() map[string]any {
	return map[string]any{"model": nil, "serial_no": nil, "install_at": nil, "warranty_until": nil, "tags": []string{}, "expected_updated_at": "2026-10-01T01:02:03.456Z"}
}

func TestDeviceEditingRejectsInvalidRequestsBeforeWriting(t *testing.T) {
	router := httpapi.NewRouter()
	router.PUT("/devices/:id", (ResourceAPI{}).updateDevice)
	invalid := map[string]map[string]any{}
	for _, field := range []string{"model", "serial_no", "install_at", "warranty_until", "tags", "expected_updated_at"} {
		body := deviceEditPayload()
		delete(body, field)
		invalid["missing "+field] = body
	}
	for name, value := range map[string]any{
		"model": 1, "serial_no": strings.Repeat("序", 129), "install_at": "2026-10-01",
		"warranty_until": "0999-12-31T23:59:59Z", "tags": nil, "expected_updated_at": nil,
	} {
		body := deviceEditPayload()
		body[name] = value
		invalid["invalid "+name] = body
	}
	for name, tags := range map[string]any{
		"too many tags": strings.Split(strings.Repeat("tag,", 20)+"end", ","),
		"long tag":      []string{strings.Repeat("标", 33)}, "empty tag": []string{" "},
		"duplicate tag": []string{" 烟感 ", "烟感"}, "null tag": []any{nil}, "wrong tag type": "abc",
	} {
		body := deviceEditPayload()
		body["tags"] = tags
		invalid[name] = body
	}
	body := deviceEditPayload()
	body["install_at"], body["warranty_until"] = "2026-10-01T09:00:00+08:00", "2026-10-01T00:59:59Z"
	invalid["warranty before installation"] = body
	for _, field := range []string{"status", "station_id", "vendor_id", "device_id", "protocol_adapter", "charge_mode", "tags_json"} {
		body := deviceEditPayload()
		body[field] = "must not change"
		invalid["protected field "+field] = body
	}
	for name, body := range invalid {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/devices/board", bytes.NewReader(encoded)))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":1005`) {
				t.Fatalf("invalid request returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
	for _, body := range []string{`null`, `{} {}`, `{"tags":[]} {}`} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/devices/board", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("malformed body returned %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestDeviceEditingNormalizesMetadataAndMilliseconds(t *testing.T) {
	body := deviceEditPayload()
	body["model"], body["serial_no"] = "  ", "  "+strings.Repeat("序", 128)+"  "
	body["install_at"], body["warranty_until"] = "2026-10-01T09:00:00.1239+08:00", "2026-10-01T01:00:00.123Z"
	body["tags"] = []string{"  " + strings.Repeat("标", 32) + "  ", "烟感"}
	body["expected_updated_at"] = "2026-10-01T09:02:03.4569+08:00"
	encoded, _ := json.Marshal(body)
	var input DeviceInput
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	row, expected, err := input.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if row.Model != nil || row.SerialNo == nil || *row.SerialNo != strings.Repeat("序", 128) || row.Tags[0] != strings.Repeat("标", 32) {
		t.Fatalf("text normalization failed: %+v", row)
	}
	if row.InstallAt.Format(time.RFC3339Nano) != "2026-10-01T01:00:00.123Z" || !row.InstallAt.Equal(*row.WarrantyUntil) || expected.Format(time.RFC3339Nano) != "2026-10-01T01:02:03.456Z" {
		t.Fatalf("timestamp normalization failed: install=%s warranty=%s expected=%s", row.InstallAt, row.WarrantyUntil, expected)
	}
	for _, raw := range []*string{nil, stringPointer("null"), stringPointer("[]")} {
		device := Device{TagsJSON: raw, UpdatedAt: expected.In(time.FixedZone("UTC+8", 8*3600))}
		if err := device.normalizeMetadata(); err != nil {
			t.Fatal(err)
		}
		result, _ := json.Marshal(device)
		if device.Tags == nil || !strings.Contains(string(result), `"tags":[]`) || strings.Contains(string(result), "tags_json") || device.UpdatedAt.Location() != time.UTC {
			t.Fatalf("device response normalization: %s", result)
		}
	}
	previous := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	if next := nextDeviceUpdateTime(previous); !next.Equal(previous.Add(time.Millisecond)) {
		t.Fatalf("update timestamp did not advance: %s -> %s", previous, next)
	}
}

func stringPointer(value string) *string { return &value }

func TestDeviceEditingRouteRequiresAuthentication(t *testing.T) {
	router := httpapi.NewRouter()
	(ResourceAPI{}).Register(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/admin/devices/board", strings.NewReader(`{}`)))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("device editing route is missing its auth guard: %d %s", response.Code, response.Body.String())
	}
}
