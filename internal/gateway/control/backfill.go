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

// backfillMetrics 是 telemetry 表文档约定的封闭指标集合。设备上报此集合之外的
// 字段会被当场拒绝而不是先存下来：静默丢弃只会把固件不匹配的真相藏到一条看起来
// 稀疏的曲线背后，而这些数值之后还要进入计费对账。
var backfillMetrics = map[string]struct{}{
	"voltage_v": {}, "current_a": {}, "temperature_c": {},
	"battery_soc": {}, "power_w": {}, "meter_kwh": {},
}

const (
	backfillMaxFrames  = 1000
	backfillMaxBody    = 1 << 20
	backfillMaxClockSk = 5 * time.Minute
	// 设备离线期间会在本地缓存，但缓存一年的断线并不现实，放进来只会让
	// 垃圾时间戳落进兜底分区里。比这更老的数据直接拒绝写入，而不是
	// 无限期地存下去。
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

// backfill 接收设备离线期间缓存下来的遥测。它复用 TCP 链路写入的那张表，
// 所以曲线能直接取到这些数据，不需要单独的聚合步骤。
//
// 这个接口是幂等的：设备没收到明确响应时会重发同一批数据，
// 这些读数如果再写一遍，
// 不仅会把表撑大，还会把窗口的点数预算顶满。所以同一个
// (device， port， metric， ts) 上已经存过的读数会被跳过，而不是再插一条。
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

// parseBackfill 在写入任何一条之前先校验整批数据，这样一帧格式不对就会让
// 整批被拒，而不是在曲线上留下一段缺口——那种缺口和真实断线根本分辨
// 不出来。
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

// decodeBackfillValue 同时接受协议在线上使用的字符串形式和裸 JSON 数字，
// 并全程用 decimal 承载数值，这样一个读数永远不会经过 float64 被四舍
// 五入一次。
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

// validDeviceID 与开通接口强制的 device id 规则保持一致
// （8-32 个 A-Z a-z 0-9 _ - 字符）。这里放宽范围，就等于允许一次补传
// 指向一个开通接口当初会拒绝创建的 id。
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
	// 补传不能替一个从未开通的设备、或一个硬件上并不存在的端口凭空造出
	// 历史：这两类读数一旦落进表里，平台就永远解释不了它们的来源。
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
	// 已停用的设备可能还在补传停用前缓存的内容；已退役的设备已经下线，
	// 不能再凭空多出历史。
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
	// 一次覆盖查询胜过逐条判断是否已存在：一整批最多 6000 行，把整个分区
	// 问一次，重试路径才够便宜。
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
	// 补传进来的读数同样要进入汇总表。否则设备在窗口内离线的那段时间，
	// 在任何走 aggregates 的曲线上都会留一段缺口，
	// 尽管原始行其实都在。
	if err := gatewaystore.RefreshAggregates(ctx, a.DB, newSamples); err != nil {
		return 0, 0, err
	}
	return len(rows), skipped, nil
}

func sampleKey(deviceID string, port sql.NullInt16, metric string, ts time.Time) string {
	return fmt.Sprintf("%s|%d|%s|%d", deviceID, port.Int16, metric, ts.UnixMilli())
}
