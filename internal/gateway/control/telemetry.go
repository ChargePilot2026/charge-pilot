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
	router.GET("/api/v1/internal/devices/:device_id/historical-curve", a.historicalCurve)
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
	granularity, table := chooseSource(from, to, limit)
	series, err := a.read(ctx, deviceID, from, to, limit, table)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "telemetry unavailable", nil)
		return
	}
	// The caller is told which resolution they are looking at. Without this a
	// 24-hour curve served from hourly rollups is indistinguishable from a
	// per-second one, and nobody can tell that the detail was reduced.
	label := "raw"
	switch table {
	case "telemetry_aggregate_15min":
		label = "15min"
	case "telemetry_aggregate_hourly":
		label = "hourly"
	}
	httpapi.OK(c, gin.H{
		"device_id": deviceID, "from": from, "to": to, "series": series, "count": len(series),
		"granularity": label, "bucket": granularity.String(),
	})
}

// read pivots the long telemetry rows into per-timestamp points. Readings are
// grouped in SQL so the response size stays bounded regardless of sample rate.
func (a TelemetryAPI) read(ctx context.Context, deviceID string, from, to time.Time, limit int, table string) ([]CurvePoint, error) {
	return a.readPort(ctx, deviceID, 0, from, to, limit, table)
}

func (a TelemetryAPI) readPort(ctx context.Context, deviceID string, portNo int, from, to time.Time, limit int, table string) ([]CurvePoint, error) {
	// Column names differ from the Go field names; without the tags GORM scans
	// zero values and every point comes back empty.
	type reading struct {
		TS   time.Time `gorm:"column:ts"`
		Name string    `gorm:"column:metric"`
		Raw  []byte    `gorm:"column:value_num"`
	}
	// A long window cannot be served from the raw table: a device reporting every
	// few seconds produces tens of thousands of rows, and the row budget below
	// would silently return only the newest slice while the response claimed to
	// cover the whole window. When the window is wide enough that this would
	// happen, the request is served from the rollups instead.
	rows := []reading{}
	if table == "telemetry" {
		query := a.DB.WithContext(ctx).Table("telemetry").
			Select("ts, metric, value_num").
			Where("device_id = ? AND ts >= ? AND ts <= ? AND value_num IS NOT NULL", deviceID, from, to)
		if portNo > 0 {
			query = query.Where("port_no = ?", portNo)
		}
		if err := query.Order("ts DESC").Limit(limit * 8).Find(&rows).Error; err != nil {
			return nil, err
		}
	} else {
		query := a.DB.WithContext(ctx).Table(table).
			Select("bucket_start AS ts, metric, avg_value AS value_num").
			Where("device_id = ? AND bucket_start >= ? AND bucket_start <= ?", deviceID, from, to)
		if portNo > 0 {
			query = query.Where("port_no = ?", portNo)
		}
		if err := query.Order("bucket_start DESC").Limit(limit * 8).Find(&rows).Error; err != nil {
			return nil, err
		}
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

// chooseSource picks the table that can actually answer the request.
//
// The raw table is preferred whenever the point budget comfortably covers the
// window, because per-second detail is more useful than a rollup whenever it is
// affordable. Beyond that the hourly rollup is used for multi-day windows and
// the 15-minute one otherwise.
//
// The estimate is deliberately generous: each raw sample becomes one row, and a
// second of readings can hold several metrics. Choosing the rollup too eagerly
// would throw away detail an operator can still afford; choosing the raw table
// too eagerly is the silent-truncation bug this guards against.
func chooseSource(from, to time.Time, limit int) (time.Duration, string) {
	buckets := int(to.Sub(from) / time.Second)
	if buckets <= 0 || limit <= 0 {
		return 0, "telemetry"
	}
	// Four rows per second is a comfortable upper estimate for a busy device.
	if buckets*4 <= limit*8 {
		return 0, "telemetry"
	}
	if to.Sub(from) > 6*time.Hour {
		return time.Hour, "telemetry_aggregate_hourly"
	}
	return 15 * time.Minute, "telemetry_aggregate_15min"
}
