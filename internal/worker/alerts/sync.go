// Package alerts 只保留设备告警同步的触发转发。
// 故障码翻译（协议语义）在 protocol/dc589；告警去重、写入与恢复判定
// 在 central/admin（alert_event 是 central 的表，device_event 证据经
// gateway 内部 HTTP）。本包不做任何业务判定，不接触两个库。
package alerts

import (
	"context"
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

// Sync 把每秒的同步节奏转发给 central，返回本轮新告警数。
type Sync struct {
	CentralURL, ServiceToken string
	Client                   serviceclient.Client
}

func (s Sync) Run(ctx context.Context) (int, error) {
	if s.CentralURL == "" || s.ServiceToken == "" {
		return 0, errors.New("device alert sync is not configured")
	}
	var response struct {
		Code int
		Data struct {
			Raised int
		}
	}
	if err := s.Client.Post(ctx, s.CentralURL, s.ServiceToken, "/api/v1/internal/device-alerts/sync", nil, &response); err != nil {
		return 0, err
	}
	if response.Code != 0 {
		return 0, errors.New("device alert sync rejected")
	}
	return response.Data.Raised, nil
}
