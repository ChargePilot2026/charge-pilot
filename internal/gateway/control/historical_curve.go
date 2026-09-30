package control

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

// historicalCurve 接受一个由 central 从它自己拥有的充电订单推导出来的时间窗。
// gateway 拿不到 user_db，所以订单归属必须在 central 那一侧先证明，
// 才能调到这个内部接口。
func (a TelemetryAPI) historicalCurve(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	deviceID := c.Param("device_id")
	orderID := strings.TrimSpace(c.Query("order_id"))
	portNo, portErr := strconv.Atoi(c.Query("port_no"))
	if deviceID == "" || len(deviceID) > 64 || orderID == "" || len(orderID) > 64 || portErr != nil || portNo < 1 || portNo > 255 {
		httpapi.BadRequest(c, "设备、订单或端口参数无效")
		return
	}
	from, fromErr := time.Parse(time.RFC3339, c.Query("started_at"))
	to := time.Now().UTC()
	var toErr error
	if c.Query("ended_at") != "" {
		to, toErr = time.Parse(time.RFC3339, c.Query("ended_at"))
	}
	if fromErr != nil || toErr != nil || !from.Before(to) || to.After(time.Now().UTC().Add(5*time.Minute)) {
		httpapi.BadRequest(c, "订单时间窗无效")
		return
	}
	if from.Before(time.Now().UTC().AddDate(-3, 0, 0)) {
		httpapi.Write(c, http.StatusUnprocessableEntity, 2018, "历史遥测已超过三年保留期", nil)
		return
	}
	var bucket time.Duration
	var table string
	switch c.Query("granularity") {
	case "15min":
		bucket, table = 15*time.Minute, "telemetry_aggregate_15min"
	case "hourly":
		bucket, table = time.Hour, "telemetry_aggregate_hourly"
	default:
		httpapi.BadRequest(c, "granularity 须为 15min 或 hourly")
		return
	}
	startBucket, endBucket := from.UTC().Truncate(bucket), to.UTC().Truncate(bucket)
	limit := a.maxPoints()
	if int(endBucket.Sub(startBucket)/bucket)+1 > limit {
		httpapi.BadRequest(c, "订单时间窗超过当前粒度的最大点数，请使用更粗粒度")
		return
	}
	series, err := a.readPort(c.Request.Context(), deviceID, portNo, startBucket, endBucket, limit, table)
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "历史遥测暂时无法读取", nil)
		return
	}
	var peaks []struct {
		Metric string `gorm:"column:metric"`
		Peak   string `gorm:"column:peak"`
		Avg    string `gorm:"column:average_value"`
	}
	if err := a.DB.WithContext(c.Request.Context()).Table(table).
		Select("metric, CAST(MAX(max_value) AS CHAR) AS peak, CAST(SUM(avg_value * count) / NULLIF(SUM(count), 0) AS CHAR) AS average_value").
		Where("device_id = ? AND port_no = ? AND bucket_start >= ? AND bucket_start <= ? AND metric IN ?", deviceID, portNo, startBucket, endBucket, []string{"power_w", "temperature_c"}).
		Group("metric").Find(&peaks).Error; err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5003, "历史遥测摘要暂时无法读取", nil)
		return
	}
	summary := gin.H{}
	for _, peak := range peaks {
		switch peak.Metric {
		case "power_w":
			summary["max_power_w"] = peak.Peak
			summary["avg_power_w"] = peak.Avg
		case "temperature_c":
			summary["max_temperature_c"] = peak.Peak
		}
	}
	httpapi.OK(c, gin.H{
		"order_id": orderID, "device_id": deviceID, "port_no": portNo,
		"started_at": from.UTC(), "ended_at": to.UTC(), "granularity": c.Query("granularity"),
		"series": series, "count": len(series), "summary": summary,
		"boundary_approximate": !from.Equal(startBucket) || !to.Equal(endBucket),
	})
}
