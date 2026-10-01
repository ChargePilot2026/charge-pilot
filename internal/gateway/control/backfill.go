package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	gatewaystore "github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// backfillMetrics 限定 telemetry 支持的指标集合；未知指标在写入前拒绝，避免形成无法解释的计量。
var backfillMetrics = map[string]struct{}{
	"voltage_v": {}, "current_a": {}, "temperature_c": {},
	"battery_soc": {}, "power_w": {}, "meter_kwh": {},
}

const (
	backfillMaxFrames  = 1000
	backfillMaxBody    = 1 << 20
	backfillMaxClockSk = 5 * time.Minute
	// 补传时间限定在允许的历史窗口内；超期读数拒绝写入，避免进入兜底分区。
	backfillMaxAge = 365 * 24 * time.Hour
)

// sample 是一条准备写入 telemetry 表的指标读数。
type sample struct {
	deviceID string
	portNo   sql.NullInt16
	metric   string
	value    string
	ts       time.Time
}

type backfillFrame struct {
	DeviceID string                     `json:"device_id"`
	PortNo   *int16                     `json:"port_no"`
	MsgType  string                     `json:"msg_type"`
	TS       string                     `json:"ts"`
	Payload  map[string]json.RawMessage `json:"payload"`
}

// backfill 将离线遥测补入在线链路使用的 telemetry 表，曲线接口可直接读取。
// (device, port, metric, ts) 标识重复样本，已存在时跳过，保持整批重发幂等。
func (a TelemetryAPI) backfill(c *gin.Context) {
	if !a.authorized(c) {
		return
	}
	deviceID := c.Param("device_id")
	var in struct {
		Frames []backfillFrame `json:"frames"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, backfillMaxBody)
	if c.ShouldBindJSON(&in) != nil || len(in.Frames) == 0 || len(in.Frames) > backfillMaxFrames {
		httpapi.BadRequest(c, "invalid backfill batch")
		return
	}
	samples, err := parseBackfill(deviceID, in.Frames, time.Now().UTC())
	if err != nil {
		httpapi.BadRequest(c, err.Error())
		return
	}
	inserted, skipped, err := a.persistBackfill(c.Request.Context(), deviceID, samples)
	if errors.Is(err, errUnknownDevice) {
		httpapi.Write(c, 404, 1004, "设备不存在或已退役", nil)
		return
	}
	if errors.Is(err, errUnknownPort) {
		httpapi.BadRequest(c, "frame port_no is not a port of this device")
		return
	}
	if err != nil {
		httpapi.Write(c, 503, 5003, "断线补传写入失败", nil)
		return
	}
	httpapi.OK(c, gin.H{"inserted": inserted, "skipped": skipped})
}

// parseBackfill 在写入前校验整批数据；任一帧无效时拒绝整批，避免部分写入。
func parseBackfill(deviceID string, frames []backfillFrame, now time.Time) ([]sample, error) {
	if !validDeviceID(deviceID) {
		return nil, fmt.Errorf("invalid device id")
	}
	out := make([]sample, 0, len(frames))
	seen := make(map[string]struct{}, len(frames))
	for _, frame := range frames {
		if frame.DeviceID != deviceID {
			return nil, fmt.Errorf("frame device_id must match the path parameter")
		}
		if frame.MsgType != "telemetry" {
			return nil, fmt.Errorf("only telemetry frames can be backfilled")
		}
		if len(frame.Payload) == 0 {
			return nil, fmt.Errorf("frame payload is empty")
		}
		ts, err := time.Parse(time.RFC3339, frame.TS)
		if err != nil {
			return nil, fmt.Errorf("frame ts is not RFC3339")
		}
		ts = ts.UTC()
		if ts.After(now.Add(backfillMaxClockSk)) || ts.Before(now.Add(-backfillMaxAge)) {
			return nil, fmt.Errorf("frame ts is outside the accepted window")
		}
		port := sql.NullInt16{}
		if frame.PortNo != nil {
			if *frame.PortNo < 1 || *frame.PortNo > 255 {
				return nil, fmt.Errorf("frame port_no is out of range")
			}
			port = sql.NullInt16{Int16: *frame.PortNo, Valid: true}
		}
		for metric, raw := range frame.Payload {
			if _, ok := backfillMetrics[metric]; !ok {
				return nil, fmt.Errorf("unsupported metric %q", metric)
			}
			value, err := decodeBackfillValue(raw)
			if err != nil {
				return nil, fmt.Errorf("metric %q is not a decimal number", metric)
			}
			key := sampleKey(deviceID, port, metric, ts)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, sample{deviceID: deviceID, portNo: port, metric: metric, value: value, ts: ts})
		}
	}
	return out, nil
}

// decodeBackfillValue 接受字符串及 JSON 数字，使用 decimal 解析，避免 float64 精度损失。
func decodeBackfillValue(raw json.RawMessage) (string, error) {
	text := strings.TrimSpace(string(raw))
	if unquoted, err := strconv.Unquote(text); err == nil {
		text = strings.TrimSpace(unquoted)
	}
	value, err := decimal.NewFromString(text)
	if err != nil {
		return "", err
	}
	return value.StringFixed(6), nil
}

// validDeviceID 校验 8–32 位字母、数字、下划线或短横线，与设备开通接口一致。
func validDeviceID(id string) bool {
	if len(id) < 8 || len(id) > 32 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

var (
	errUnknownDevice = errors.New("backfill device is unknown or retired")
	errUnknownPort   = errors.New("backfill port does not belong to the device")
)

func (a TelemetryAPI) persistBackfill(ctx context.Context, deviceID string, samples []sample) (int, int, error) {
	if len(samples) == 0 {
		return 0, 0, nil
	}
	// 仅接受已开通设备及其有效端口的补传，防止写入无法关联硬件来源的历史读数。
	var device struct {
		Status string
	}
	if err := a.DB.WithContext(ctx).Table("device").
		Select("status").Where("device_id = ? AND deleted_at IS NULL", deviceID).Take(&device).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, 0, errUnknownDevice
		}
		return 0, 0, err
	}
	// 停用设备可补传停用前缓存的读数；退役设备不接受补传。
	if device.Status == "retired" {
		return 0, 0, errUnknownDevice
	}
	var ports []struct {
		PortNo int16 `gorm:"column:port_no"`
	}
	if err := a.DB.WithContext(ctx).Table("device_port").
		Select("port_no").Where("device_id = ? AND deleted_at IS NULL", deviceID).Find(&ports).Error; err != nil {
		return 0, 0, err
	}
	known := make(map[int16]struct{}, len(ports))
	for _, p := range ports {
		known[p.PortNo] = struct{}{}
	}
	allowed := samples[:0]
	for _, s := range samples {
		if !s.portNo.Valid {
			allowed = append(allowed, s)
			continue
		}
		if _, ok := known[s.portNo.Int16]; !ok {
			return 0, 0, errUnknownPort
		}
		allowed = append(allowed, s)
	}
	samples = allowed
	metrics := make([]string, 0, len(backfillMetrics))
	for metric := range backfillMetrics {
		metrics = append(metrics, metric)
	}
	from, to := samples[0].ts, samples[0].ts
	for _, s := range samples[1:] {
		if s.ts.Before(from) {
			from = s.ts
		}
		if s.ts.After(to) {
			to = s.ts
		}
	}
	// 批量查询现有读数，避免对最多 6000 条补传逐行查重。
	type existing struct {
		PortNo sql.NullInt16 `gorm:"column:port_no"`
		Metric string        `gorm:"column:metric"`
		TS     time.Time     `gorm:"column:ts"`
	}
	var stored []existing
	err := a.DB.WithContext(ctx).Table("telemetry").
		Select("port_no, metric, ts").
		Where("device_id = ? AND metric IN ? AND ts >= ? AND ts <= ?", deviceID, metrics, from, to).
		Find(&stored).Error
	if err != nil {
		return 0, 0, err
	}
	present := make(map[string]struct{}, len(stored))
	for _, row := range stored {
		present[sampleKey(deviceID, row.PortNo, row.Metric, row.TS)] = struct{}{}
	}
	rows := make([]map[string]any, 0, len(samples))
	newSamples := make([]gatewaystore.AggregateSample, 0, len(samples))
	skipped := 0
	for _, s := range samples {
		if _, dup := present[sampleKey(s.deviceID, s.portNo, s.metric, s.ts)]; dup {
			skipped++
			continue
		}
		rows = append(rows, map[string]any{
			"device_id": s.deviceID, "port_no": s.portNo, "metric": s.metric,
			"value_num": s.value, "ts": s.ts,
		})
		newSamples = append(newSamples, gatewaystore.AggregateSample{
			DeviceID: s.deviceID, Port: s.portNo, Metric: s.metric, Value: s.value, TS: s.ts,
		})
	}
	if len(rows) == 0 {
		return 0, skipped, nil
	}
	if err := a.DB.WithContext(ctx).Table("telemetry").CreateInBatches(rows, 500).Error; err != nil {
		return 0, 0, err
	}
	// 补传读数同步更新汇总表，保持原始遥测与聚合曲线一致。
	if err := gatewaystore.RefreshAggregates(ctx, a.DB, newSamples); err != nil {
		return 0, 0, err
	}
	return len(rows), skipped, nil
}

func sampleKey(deviceID string, port sql.NullInt16, metric string, ts time.Time) string {
	return fmt.Sprintf("%s|%d|%s|%d", deviceID, port.Int16, metric, ts.UnixMilli())
}
