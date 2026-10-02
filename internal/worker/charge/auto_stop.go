package charge

import (
	"context"
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

// AutoStopper 不再做停机判定：它只把扫描触发转发给 central。
// 判定、计费截止点冻结与停机下发都在 central 完成（P0-1），
// worker 不读订单计费快照、不写 charge_billing_cutoff、不直读 gateway 库。
type AutoStopper struct {
	CentralURL, ServiceToken string
	Client                   serviceclient.Client
}

// Run 触发一轮 central 停机扫描，返回本轮下发停机的订单数。
func (s AutoStopper) Run(ctx context.Context) (int, error) {
	if s.CentralURL == "" || s.ServiceToken == "" {
		return 0, errors.New("auto stop is not configured")
	}
	var response struct {
		Code int
		Data struct {
			Stopped int
		}
	}
	if err := s.Client.Post(ctx, s.CentralURL, s.ServiceToken, "/api/v1/internal/charge-orders/auto-stop", nil, &response); err != nil {
		return 0, err
	}
	if response.Code != 0 {
		return 0, errors.New("auto stop sweep rejected")
	}
	return response.Data.Stopped, nil
}
