package charge

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

func measuredSegments(start time.Time, end protocol.Event, samples []protocol.Event) []pricing.MeterSegment {
	return pricing.MeasuredSegments(start, end, samples)
}

// meterSegments 经 gateway 拉取计量证据：心跳在 [ack_at, end.ReceivedAt]
// 内且 id 不超过结束事件的行，与旧直读实现同界。返回超过 10080 行时放弃
// 分段（与旧实现一致，交由无分段路径处理）。
func (s EndSynchronizer) meterSegments(ctx context.Context, gateway gatewaySyncAPI, command gatewayCommand, endID uint64, end protocol.Event) ([]pricing.MeterSegment, error) {
	if command.AckAt == nil || end.StartedAt.IsZero() {
		return nil, nil
	}
	// 以 BB 事件 ID 固定心跳证据范围，排除结束后落库的延迟心跳，保证重放结果一致。
	path := "/api/v1/internal/devices/" + url.PathEscape(end.DeviceID) + "/meter-samples?ack_at=" +
		url.QueryEscape(command.AckAt.UTC().Format(time.RFC3339Nano)) + "&end_at=" +
		url.QueryEscape(end.ReceivedAt.UTC().Format(time.RFC3339Nano)) + "&end_id=" +
		strconv.FormatUint(endID, 10)
	var reply struct {
		Items []struct {
			ID      uint64 `json:"id"`
			Payload string `json:"payload"`
		} `json:"items"`
	}
	if err := gateway.get(ctx, path, &reply); err != nil {
		return nil, err
	}
	if len(reply.Items) > 10080 {
		return nil, nil
	}
	samples := make([]protocol.Event, 0, len(reply.Items))
	for _, row := range reply.Items {
		var event protocol.Event
		if err := json.Unmarshal([]byte(row.Payload), &event); err != nil {
			return nil, err
		}
		samples = append(samples, event)
	}
	return measuredSegments(*command.AckAt, end, samples), nil
}

// freezeEndResult 在第一次 HTTP 尝试之前就把整份内部请求冻结。
// 无论是延迟到达的遥测还是保留期清理，
// 都不允许改动一份重放出去的结束回执。
// 冻结与身份校验在 gateway 单库内完成（忽略 segments 后语义一致即可）；
// 本组件每次重放都重新计算候选 segments，冻结端点保证首写优先。
func (s EndSynchronizer) freezeEndResult(ctx context.Context, gateway gatewaySyncAPI, command gatewayCommand, id uint64, event protocol.Event, candidate endResult) (endResult, error) {
	segments, err := s.meterSegments(ctx, gateway, command, id, event)
	if err != nil {
		return candidate, err
	}
	candidate.Meter.Segments = segments
	payload, err := json.Marshal(candidate)
	if err != nil {
		return candidate, err
	}
	var reply struct {
		Payload string `json:"payload"`
		Frozen  bool   `json:"frozen"`
	}
	if err := gateway.post(ctx, "/api/v1/internal/charge-end-deliveries/freeze", map[string]any{
		"device_event_id": id, "charge_order_id": candidate.ChargeOrderID, "payload": string(payload),
	}, &reply); err != nil {
		return candidate, err
	}
	var frozen endResult
	if err := json.Unmarshal([]byte(reply.Payload), &frozen); err != nil {
		return candidate, err
	}
	return frozen, nil
}
