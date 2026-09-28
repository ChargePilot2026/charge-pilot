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

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// backfillMetrics is the closed set the telemetry table documents. A device
// that reports a field outside this set is rejected rather than stored: silently
// dropping it would hide a firmware mismatch behind a curve that just looks
// sparse, and these values later feed billing reconciliation.
var backfillMetrics = map[string]struct{}{
	"voltage_v": {}, "current_a": {}, "temperature_c": {},
	"battery_soc": {}, "power_w": {}, "meter_kwh": {},
}

const (
	backfillMaxFrames  = 1000
	backfillMaxBody    = 1 << 20
	backfillMaxClockSk = 5 * time.Minute
	// Devices buffer locally while offline, but a year of buffering is not a
	// plausible outage and would let garbage timestamps land in the catch-all
	// partition. Anything older is rejected instead of being stored forever.
	backfillMaxAge = 365 * 24 * time.Hour
)

// sample is one metric reading destined for the telemetry table.
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

// backfill ingests telemetry a device buffered while it was offline. It reuses
// the same table the TCP path writes to, so the curve picks the data up with no
// separate aggregation step.
//
// The endpoint is idempotent: a device that never sees a clean response will
// retry the same batch, and re-reading those samples would both inflate the
// table and shift the window limits. A sample already stored for the same
// (device, port, metric, ts) is therefore skipped, not duplicated.
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

// parseBackfill validates the whole batch before anything is written, so a
// malformed frame rejects the batch instead of leaving a partial gap in the
// curve that nobody can tell apart from a real outage.
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

// decodeBackfillValue accepts both the string form the protocol uses on the
// wire and a bare JSON number, and keeps the value in decimal the whole way so
// a reading is never rounded through float64.
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

// validDeviceID mirrors the device id rule the provisioning endpoint enforces
// (8-32 chars of A-Z a-z 0-9 _ -). Accepting a wider range here would let a
// backfill target an id that provisioning would have refused to create.
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
	// A backfill must not be able to invent history for a device that was never
	// provisioned, or for a port the hardware does not have: both would surface
	// as readings the platform can never explain.
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
	// A disabled device may still be replaying what it buffered before it was
	// switched off; a retired one is decommissioned and must not gain history.
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
	// One covering query beats a per-sample existence check: a full batch is up
	// to 6000 rows, and asking the partition once keeps the retry path cheap.
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
	}
	if len(rows) == 0 {
		return 0, skipped, nil
	}
	if err := a.DB.WithContext(ctx).Table("telemetry").CreateInBatches(rows, 500).Error; err != nil {
		return 0, 0, err
	}
	return len(rows), skipped, nil
}

func sampleKey(deviceID string, port sql.NullInt16, metric string, ts time.Time) string {
	return fmt.Sprintf("%s|%d|%s|%d", deviceID, port.Int16, metric, ts.UnixMilli())
}
