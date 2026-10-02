package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

// RegulatoryDeliverer 从 central 领取监管报送任务，经 Sender 签名发送后
// 把结果回执 central 推进状态。租约领取、退避计算与状态写入都在 central
// 单库事务内完成，本组件不持有数据库句柄。
type RegulatoryDeliverer struct {
	Central      serviceclient.Client
	CentralURL   string
	ServiceToken string
	Sender       Sender
}

// claimedReport 是 central 租约领取的一笔待报送记录。
type claimedReport struct {
	ID         uint64          `json:"id"`
	EventID    string          `json:"event_id"`
	ObjectType string          `json:"object_type"`
	ObjectKey  string          `json:"object_key"`
	Data       json.RawMessage `json:"data"`
	LeaseToken string          `json:"lease_token"`
}

// finishItem 是一笔报送的发送结果；central 按 lease_token 条件推进状态。
type finishItem struct {
	ID         uint64 `json:"id"`
	LeaseToken string `json:"lease_token"`
	Delivered  bool   `json:"delivered"`
	Mode       string `json:"mode,omitempty"`
	Error      string `json:"error,omitempty"`
}

// RunBatch 以租约领取持久化事件；失败事件保留，由 central 按有界指数退避重排。
func (d RegulatoryDeliverer) RunBatch(ctx context.Context) (int, error) {
	if d.CentralURL == "" || d.Sender == nil {
		return 0, errors.New("regulatory deliverer not configured")
	}
	var claim struct {
		Code int
		Data struct {
			Items []claimedReport `json:"items"`
		}
	}
	if err := d.Central.Post(ctx, d.CentralURL, d.ServiceToken, "/api/v1/internal/regulatory-reports/claim", map[string]any{"limit": 20}, &claim); err != nil {
		return 0, err
	}
	if claim.Code != 0 {
		return 0, errors.New("regulatory claim rejected")
	}
	finishes := make([]finishItem, 0, len(claim.Data.Items))
	delivered := 0
	var failures []error
	for _, report := range claim.Data.Items {
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		sendErr := d.Sender.Send(sendCtx, Event{EventID: report.EventID, ObjectType: report.ObjectType, ObjectKey: report.ObjectKey, Data: report.Data})
		cancel()
		item := finishItem{ID: report.ID, LeaseToken: report.LeaseToken, Mode: d.Sender.Mode(), Delivered: sendErr == nil}
		if sendErr == nil {
			delivered++
		} else {
			item.Error = sendErr.Error()
			failures = append(failures, sendErr)
		}
		finishes = append(finishes, item)
	}
	if len(finishes) == 0 {
		return 0, errors.Join(failures...)
	}
	var done struct {
		Code int
		Data struct {
			Finished int `json:"finished"`
		}
	}
	if err := d.Central.Post(ctx, d.CentralURL, d.ServiceToken, "/api/v1/internal/regulatory-reports/finish", map[string]any{"items": finishes}, &done); err != nil {
		// 回执未到达时租约到期由 central 重排，事件不丢。
		failures = append(failures, err)
	} else if done.Code != 0 {
		failures = append(failures, errors.New("regulatory finish rejected"))
	}
	return delivered, errors.Join(failures...)
}
