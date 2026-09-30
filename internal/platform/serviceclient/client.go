// Package serviceclient 提供 central 各服务之间、以及它们与 gateway 之间
// 那些用 service token 鉴权的 HTTP 调用。
// 把它放在 platform，是为了让每个模块都套用同一套
// 超时、请求头与错误处理，而不是各自复制一遍。
package serviceclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnauthorized 表示被调用方拒收了共享的 service token。
var ErrUnauthorized = errors.New("service token rejected")

// ErrNotFound 表示被调用方有应答，但资源不存在。
var ErrNotFound = errors.New("resource not found")

// Client 执行无需签名、仅以 service token 鉴权的内部调用。
type Client struct {
	HTTP    *http.Client
	Timeout time.Duration
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

// Get 发出一个带鉴权的 GET 并返回原始响应体。404 会被转成 ErrNotFound，
// 这样调用方能区分"不存在"与"调用失败"，
// 而不是把一条缺失的记录当成空结果。
func (c Client) Get(ctx context.Context, baseURL, serviceToken, path string) ([]byte, error) {
	target := strings.TrimRight(baseURL, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Service-Token", serviceToken)
	response, err := c.httpClient().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case response.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return body, nil
	default:
		return nil, fmt.Errorf("service call %s returned %d", path, response.StatusCode)
	}
}

// GetJSON 把一次成功的 GET 解码进 out。
func (c Client) GetJSON(ctx context.Context, baseURL, serviceToken, path string, out any) error {
	body, err := c.Get(ctx, baseURL, serviceToken, path)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// Post 发出一个带鉴权的 JSON POST，并把应答解码进 out。
func (c Client) Post(ctx context.Context, baseURL, serviceToken, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	target := strings.TrimRight(baseURL, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Service-Token", serviceToken)
	response, err := c.httpClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case response.StatusCode >= 200 && response.StatusCode < 300:
		if out == nil || len(reply) == 0 {
			return nil
		}
		return json.Unmarshal(reply, out)
	default:
		return fmt.Errorf("service call %s returned %d", path, response.StatusCode)
	}
}
