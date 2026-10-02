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

// TelemetryAPI 提供设备读数查询；telemetry 按 ts 分区，所有查询必须包含有界时间窗口。
type TelemetryAPI struct {
	DB           *gorm.DB
	ServiceToken string
	MaxWindow    time.Duration
	MaxPoints    int
}

func (a TelemetryAPI) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/devices/:device_id/telemetry", a.curve)
	router.GET("/api/v1/internal/devices/:device_id/charging-samples", a.chargingSamples)
	// 批量取证挂在 /internal 根下：/devices 下已存在 :device_id 通配，
	// 静态段 charging-evidence 会与其在 gin 基数树上冲突。
	router.POST("/api/v1/internal/charging-evidence", a.chargingEvidence)
	router.GET("/api/v1/internal/charge-orders/:order_no/process", a.chargeProcess)
	router.GET("/api/v1/internal/devices/:device_id/historical-curve", a.historicalCurve)
	router.POST("/api/v1/internal/devices/:device_id/backfill", a.backfill)
	a.registerDeviceFaults(router)
	a.registerWorkerSync(router)
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

// CurvePoint 的指标允许 NULL，表示设备未上报；调用方应保留曲线缺口，不补零。
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
	// 调用方不能对着分区历史要一段没有上界的切片。
	if !from.Before(to) {
		httpapi.BadRequest(c, "时间窗口无效")
		return
	}
	if to.Sub(from) > window {
		from = to.Add(-window)
	}

	ctx := c.Request.Context()
	portNo := 0
	if raw := c.Query("port_no"); raw != "" {
		var err error
		portNo, err = strconv.Atoi(raw)
		if err != nil || portNo < 1 || portNo > 255 {
			httpapi.BadRequest(c, "port_no 须为有效端口号")
			return
		}
	}
	granularity, table := chooseSource(from, to, limit)
	series, err := a.readPort(ctx, deviceID, portNo, from, to, limit, table)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "telemetry unavailable", nil)
		return
	}
	// 返回实际查询分辨率，便于调用方识别原始读数与聚合曲线。
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

// read 把长表形式的 telemetry 行转成按时间戳对齐的曲线点。读数在 SQL 里
// 分组，这样无论采样率多高，响应体积都是有界的。
func (a TelemetryAPI) read(ctx context.Context, deviceID string, from, to time.Time, limit int, table string) ([]CurvePoint, error) {
	return a.readPort(ctx, deviceID, 0, from, to, limit, table)
}

func (a TelemetryAPI) readPort(ctx context.Context, deviceID string, portNo int, from, to time.Time, limit int, table string) ([]CurvePoint, error) {
	// 列名和 Go 字段名对不上；没有这些 tag 的话 GORM 会扫出零值，
	// 每个点都是空的。
	type reading struct {
		TS   time.Time `gorm:"column:ts"`
		Name string    `gorm:"column:metric"`
		Raw  []byte    `gorm:"column:value_num"`
	}
	// 长窗口使用汇总表，避免原始样本超过行数预算后仅返回窗口尾部。
	rows := []reading{}
	if table == "telemetry" {
		query := a.DB.WithContext(ctx).Table("telemetry").
			Select("ts, metric, value_num").
			Where("device_id = ? AND ts >= ? AND ts <= ? AND value_num IS NOT NULL", deviceID, from, to)
		if portNo > 0 {
			query = query.Where("port_no = ? OR (port_no IS NULL AND metric IN ('voltage_v','temperature_c'))", portNo)
		}
		if err := query.Order("ts DESC").Limit(limit * 8).Find(&rows).Error; err != nil {
			return nil, err
		}
	} else {
		query := a.DB.WithContext(ctx).Table(table).
			Select("bucket_start AS ts, metric, avg_value AS value_num").
			Where("device_id = ? AND bucket_start >= ? AND bucket_start <= ?", deviceID, from, to)
		if portNo > 0 {
			query = query.Where("port_no = ? OR (port_no IS NULL AND metric IN ('voltage_v','temperature_c'))", portNo)
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
		// 同一秒内采到的样本算同一个点。
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
	// 先取最新的，再整体反转，曲线就能从左往右读。
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

// chooseSource 根据窗口和点数预算选择查询表。
// 预算可覆盖整个窗口时使用原始样本；超限时多天窗口使用小时汇总，其余使用 15 分钟汇总。
func chooseSource(from, to time.Time, limit int) (time.Duration, string) {
	buckets := int(to.Sub(from) / time.Second)
	if buckets <= 0 || limit <= 0 {
		return 0, "telemetry"
	}
	// 一秒四行对一台忙碌的设备来说是相当宽松的上限估计。
	if buckets*4 <= limit*8 {
		return 0, "telemetry"
	}
	if to.Sub(from) > 6*time.Hour {
		return time.Hour, "telemetry_aggregate_hourly"
	}
	return 15 * time.Minute, "telemetry_aggregate_15min"
}
