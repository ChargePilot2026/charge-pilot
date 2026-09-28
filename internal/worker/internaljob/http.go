package internaljob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func Run(ctx context.Context, centralURL, serviceToken, path string, client *http.Client) error {
	base, err := url.Parse(centralURL)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "http" && base.Scheme != "https") || base.RawQuery != "" || base.Fragment != "" || serviceToken == "" {
		return errors.New("invalid internal job dispatcher configuration")
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-Service-Token", serviceToken)
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("internal job dispatcher HTTP %d", response.StatusCode)
	}
	var body struct {
		Code *int `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body); err != nil {
		return err
	}
	if body.Code == nil || *body.Code != 0 {
		return errors.New("invalid internal job dispatcher response")
	}
	return nil
}
