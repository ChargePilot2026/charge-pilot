package control

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// TelemetryAPI serves device readings to central. Telemetry is partitioned by
// ts, so the window is always bounded and the partition key is part of the query.
type TelemetryAPI struct {
	DB           *gorm.DB
	ServiceToken string
	MaxWindow    time.Duration
	MaxPoints    int
}

func (a TelemetryAPI) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/devices/:device_id/telemetry", a.curve)
	router.POST("/api/v1/internal/devices/:device_id/backfill", a.backfill)
}

func (a TelemetryAPI) authorized(c *gin.Context) bool {
	provided := sha256.Sum256([]byte(c.GetHeader("X-Service-Token")))
	expected := sha256.Sum256([]byte(a.ServiceToken))
	if a.ServiceToken == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		httpapi.Write(c, http.StatusUnauthorized, 1001, "service token invalid", nil)
		return false
	}
	return true
}

// CurvePoint keeps every metric nullable: a device reports whichever values it
// measures, and the caller renders gaps rather than zeros.
type CurvePoint struct {
	TS           string   `json:"ts"`
	PowerW       *float64 `json:"power_w"`
	CurrentA     *float64 `json:"current_a"`
	VoltageV     *float64 `json:"voltage_v"`
	TemperatureC *float64 `json:"temperature_c"`
	BatterySOC   *float64 `json:"battery_soc"`
	MeterKWh     *float64 `json:"meter_kwh"`
}

func (a TelemetryAPI) curve(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	deviceID := c.Param("device_id")
	if deviceID == "" || len(deviceID) > 64 {
		httpapi.BadRequest(c, "device id invalid")
		return
	}
	window := a.maxWindow()
	if window <= 0 {
		window = 24 * time.Hour
	}
	limit := a.maxPoints()
	if limit <= 0 {
		limit = 2000
	}
	to := time.Now().UTC()
	from := to.Add(-window)
	if raw := c.Query("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpapi.BadRequest(c, "from 须为 RFC 3339 时间")
			return
		}
		from = parsed.UTC()
	}
	if raw := c.Query("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpapi.BadRequest(c, "to 须为 RFC 3339 时间")
			return
		}
		to = parsed.UTC()
	}
	// A caller cannot ask for an unbounded slice of a partitioned history.
	if !from.Before(to) {
		httpapi.BadRequest(c, "时间窗口无效")
		return
	}
	if to.Sub(from) > window {
		from = to.Add(-window)
	}

	ctx := c.Request.Context()
	series, err := a.read(ctx, deviceID, from, to, limit)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "telemetry unavailable", nil)
		return
	}
	httpapi.OK(c, gin.H{
		"device_id": deviceID, "from": from, "to": to, "series": series, "count": len(series),
	})
}

// read pivots the long telemetry rows into per-timestamp points. Readings are
// grouped in SQL so the response size stays bounded regardless of sample rate.
func (a TelemetryAPI) read(ctx context.Context, deviceID string, from, to time.Time, limit int) ([]CurvePoint, error) {
	// Column names differ from the Go field names; without the tags GORM scans
	// zero values and every point comes back empty.
	type reading struct {
		TS   time.Time `gorm:"column:ts"`
		Name string    `gorm:"column:metric"`
		Raw  []byte    `gorm:"column:value_num"`
	}
	rows := []reading{}
	if err := a.DB.WithContext(ctx).Table("telemetry").
		Select("ts, metric, value_num").
		Where("device_id = ? AND ts >= ? AND ts <= ? AND value_num IS NOT NULL", deviceID, from, to).
		Order("ts DESC").Limit(limit * 8).Find(&rows).Error; err != nil {
		return nil, err
	}
	type bucket struct {
		ts     time.Time
		values map[string]float64
	}
	order := []time.Time{}
	buckets := map[time.Time]*bucket{}
	for _, row := range rows {
		// Samples taken within the same second belong to one point.
		key := row.TS.UTC().Truncate(time.Second)
		existing, present := buckets[key]
		if !present {
			existing = &bucket{ts: key, values: map[string]float64{}}
			buckets[key] = existing
			order = append(order, key)
		}
		value, err := strconv.ParseFloat(string(row.Raw), 64)
		if err != nil {
			continue
		}
		existing.values[row.Name] = value
	}
	// Newest first, then reversed so the curve reads left to right.
	sortDesc(order)
	out := make([]CurvePoint, 0, len(order))
	for i, key := range order {
		if i >= limit {
			break
		}
		b := buckets[key]
		point := CurvePoint{TS: b.ts.Format(time.RFC3339)}
		point.PowerW = value(b.values, "power_w")
		point.CurrentA = value(b.values, "current_a")
		point.VoltageV = value(b.values, "voltage_v")
		point.TemperatureC = value(b.values, "temperature_c")
		point.BatterySOC = value(b.values, "battery_soc")
		point.MeterKWh = value(b.values, "meter_kwh")
		out = append(out, point)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func value(values map[string]float64, key string) *float64 {
	if v, present := values[key]; present {
		return &v
	}
	return nil
}

func sortDesc(times []time.Time) {
	for i := 1; i < len(times); i++ {
		for j := i; j > 0 && times[j].After(times[j-1]); j-- {
			times[j], times[j-1] = times[j-1], times[j]
		}
	}
}

func (a TelemetryAPI) maxWindow() time.Duration { return a.MaxWindow }

func (a TelemetryAPI) maxPoints() int {
	if a.MaxPoints > 0 {
		return a.MaxPoints
	}
	return 2000
}
