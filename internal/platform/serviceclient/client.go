// Package serviceclient provides the service-token authenticated HTTP calls the
// central services make to each other and to the gateway. Keeping it in
// platform means every module applies the same timeout, header and error
// handling instead of repeating it.
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

// ErrUnauthorized means the callee rejected the shared service token.
var ErrUnauthorized = errors.New("service token rejected")

// ErrNotFound means the callee answered but the resource does not exist.
var ErrNotFound = errors.New("resource not found")

// Client performs signed-free internal calls authenticated by service token.
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

// Get performs an authenticated GET and returns the raw body. A 404 is surfaced
// as ErrNotFound so callers can distinguish absence from failure instead of
// turning a missing record into an empty result.
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

// GetJSON decodes a successful GET into out.
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

// Post sends an authenticated JSON POST and decodes the reply into out.
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
