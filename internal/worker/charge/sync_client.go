package charge

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

// gatewaySyncAPI 封装对 gateway worker-sync 内部端点的调用，
// 统一解开 code/data 应答包并校验 code == 0。worker 经此访问
// gateway_db，不再持有 gateway 库句柄（第 5 批⑤）。
type gatewaySyncAPI struct {
	client serviceclient.Client
	base   string
	token  string
}

func newGatewaySyncAPI(client serviceclient.Client, base, token string) gatewaySyncAPI {
	return gatewaySyncAPI{client: client, base: base, token: token}
}

func (g gatewaySyncAPI) get(ctx context.Context, path string, out any) error {
	body, err := g.client.Get(ctx, g.base, g.token, path)
	if err != nil {
		return err
	}
	return unwrapSyncEnvelope(body, out, path)
}

func (g gatewaySyncAPI) post(ctx context.Context, path string, payload, out any) error {
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := g.client.Post(ctx, g.base, g.token, path, payload, &envelope); err != nil {
		return err
	}
	if envelope.Code != 0 {
		return fmt.Errorf("gateway %s rejected: code %d", path, envelope.Code)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

func unwrapSyncEnvelope(body []byte, out any, path string) error {
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if envelope.Code != 0 {
		return fmt.Errorf("gateway %s rejected: code %d", path, envelope.Code)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}
