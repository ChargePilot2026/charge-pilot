package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
)

var ErrUnpaid = errors.New("charge order is not fully paid or has no device limit")
var ErrNotCharging = errors.New("charge order is not charging for this user")

type PaidOrderAuthorizer interface {
	PaidOrder(context.Context, string) (store.PaidOrder, error)
}

type CentralAuthorizer struct {
	BaseURL      string
	ServiceToken string
	Client       *http.Client
}

func (a CentralAuthorizer) PaidOrder(ctx context.Context, orderNo string) (store.PaidOrder, error) {
	base, err := url.Parse(a.BaseURL)
	if err != nil || base.Scheme != "http" && base.Scheme != "https" || base.Host == "" || base.User != nil || a.ServiceToken == "" {
		return store.PaidOrder{}, errors.New("invalid central authorization configuration")
	}
	if orderNo == "" || strings.ContainsAny(orderNo, "/?#") {
		return store.PaidOrder{}, ErrUnpaid
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/internal/charge-orders/" + url.PathEscape(orderNo) + "/start-authorization"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return store.PaidOrder{}, err
	}
	request.Header.Set("X-Service-Token", a.ServiceToken)
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return store.PaidOrder{}, fmt.Errorf("central start authorization: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusNotFound {
		return store.PaidOrder{}, ErrUnpaid
	}
	if response.StatusCode != http.StatusOK {
		return store.PaidOrder{}, fmt.Errorf("central start authorization returned %d", response.StatusCode)
	}
	var envelope struct {
		Code int             `json:"code"`
		Data store.PaidOrder `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&envelope); err != nil {
		return store.PaidOrder{}, err
	}
	if envelope.Code != 0 || envelope.Data.OrderNo != orderNo || envelope.Data.ChargeOrderID == 0 {
		return store.PaidOrder{}, ErrUnpaid
	}
	return envelope.Data, nil
}

func (a CentralAuthorizer) ActiveStop(ctx context.Context, orderNo string, userID uint64) (store.ActiveOrder, error) {
	base, err := url.Parse(a.BaseURL)
	if err != nil || base.Scheme != "http" && base.Scheme != "https" || base.Host == "" || base.User != nil || a.ServiceToken == "" {
		return store.ActiveOrder{}, errors.New("invalid central authorization configuration")
	}
	if orderNo == "" || strings.ContainsAny(orderNo, "/?#") || userID == 0 {
		return store.ActiveOrder{}, ErrNotCharging
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/internal/charge-orders/" + url.PathEscape(orderNo) + "/stop-authorization"
	query := base.Query()
	query.Set("user_id", strconv.FormatUint(userID, 10))
	base.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return store.ActiveOrder{}, err
	}
	request.Header.Set("X-Service-Token", a.ServiceToken)
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return store.ActiveOrder{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusNotFound {
		return store.ActiveOrder{}, ErrNotCharging
	}
	if response.StatusCode != http.StatusOK {
		return store.ActiveOrder{}, fmt.Errorf("central stop authorization returned %d", response.StatusCode)
	}
	var envelope struct {
		Code int               `json:"code"`
		Data store.ActiveOrder `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&envelope); err != nil {
		return store.ActiveOrder{}, err
	}
	if envelope.Code != 0 || envelope.Data.OrderNo != orderNo || envelope.Data.UserID != userID || envelope.Data.ChargeOrderID == 0 {
		return store.ActiveOrder{}, ErrNotCharging
	}
	return envelope.Data, nil
}
