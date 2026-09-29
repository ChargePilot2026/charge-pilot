package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestHistoricalCurveUsesOrderWindowAndPort(t *testing.T) {
	if os.Getenv("TEST_GATEWAY_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, os.Getenv("TEST_GATEWAY_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := "HIST-" + uuid.NewString()[:8]
	first := time.Now().UTC().Add(-45 * time.Minute).Truncate(15 * time.Minute)
	second := first.Add(15 * time.Minute)
	defer db.ExecContext(ctx, "DELETE FROM telemetry_aggregate_15min WHERE device_id = ?", deviceID)
	for _, sample := range []struct {
		port int
		at   time.Time
		avg  int
	}{{1, first, 100}, {1, second, 200}, {2, first, 900}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO telemetry_aggregate_15min(device_id,port_no,metric,bucket_start,bucket_month,avg_value,min_value,max_value,count) VALUES(?,?, 'power_w',?,?,?,?,?,1)`,
			deviceID, sample.port, sample.at, sample.at.Format("2006-01-01"), sample.avg, sample.avg, sample.avg); err != nil {
			t.Fatal(err)
		}
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	TelemetryAPI{DB: orm, ServiceToken: "curve-secret"}.Register(router)
	requestURL := fmt.Sprintf("/api/v1/internal/devices/%s/historical-curve?order_id=ORD-1&port_no=1&started_at=%s&ended_at=%s&granularity=15min",
		deviceID, url.QueryEscape(first.Add(time.Minute).Format(time.RFC3339)), url.QueryEscape(second.Add(time.Minute).Format(time.RFC3339)))
	request := httptest.NewRequest("GET", requestURL, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("missing token returned %d", response.Code)
	}
	request.Header.Set("X-Service-Token", "curve-secret")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("curve returned %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			Series  []CurvePoint   `json:"series"`
			Summary map[string]any `json:"summary"`
			Approx  bool           `json:"boundary_approximate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Series) != 2 || body.Data.Series[0].PowerW == nil || *body.Data.Series[0].PowerW != 100 || *body.Data.Series[1].PowerW != 200 || !body.Data.Approx {
		t.Fatalf("port/time filter failed: %+v", body.Data)
	}
	if body.Data.Summary["max_power_w"] != "200.000000" {
		t.Fatalf("peak value missing: %v", body.Data.Summary)
	}
}
