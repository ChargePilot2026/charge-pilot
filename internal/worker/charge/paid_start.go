package charge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
)

// PaidStarter 重试已支付订单，直到 gateway 接受持久化 START 命令；重复调用由 gateway 保证幂等。
type PaidStarter struct {
	UserDB       *gorm.DB
	GatewayURL   string
	ServiceToken string
	Client       *http.Client
	cursor       uint64
}

func (s *PaidStarter) DispatchBatch(ctx context.Context) (int, error) {
	if s == nil || s.UserDB == nil || s.ServiceToken == "" {
		return 0, errors.New("paid start dispatcher is not configured")
	}
	base, err := url.Parse(s.GatewayURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" ||
		(base.Scheme != "http" && base.Scheme != "https") {
		return 0, errors.New("invalid gateway URL")
	}
	var rows []paidChargeOrderRow
	err = s.UserDB.WithContext(ctx).Table("charge_order AS c").Select("c.id, c.order_no").
		Joins("JOIN charge_payment_intent AS i ON i.charge_order_id = c.id AND i.user_id = c.user_id AND i.status = 'paid'").
		Joins("JOIN payment_order AS p ON p.id = i.payment_order_id AND p.biz_type = 'charge' AND p.biz_id = c.id AND p.user_id = c.user_id AND p.status = 'paid' AND p.total_cents > 0 AND p.paid_cents >= p.total_cents").
		Where("c.status = 'paid' AND c.deleted_at IS NULL AND c.id > ?", s.cursor).Order("c.id").Limit(100).Find(&rows).Error
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		s.cursor = 0
		return 0, nil
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	accepted := 0
	var firstError error
	for _, row := range rows {
		s.cursor = row.ID
		if err := postPaidStart(ctx, client, *base, s.ServiceToken, row.OrderNo); err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		accepted++
	}
	return accepted, firstError
}

type paidChargeOrderRow struct {
	ID      uint64 `gorm:"column:id"`
	OrderNo string `gorm:"column:order_no"`
}

func postPaidStart(ctx context.Context, client *http.Client, base url.URL, token, orderNo string) error {
	if orderNo == "" {
		return errors.New("empty paid charge order number")
	}
	data, err := json.Marshal(map[string]string{"order_no": orderNo})
	if err != nil {
		return err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/internal/charge-orders/start"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Service-Token", token)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("gateway paid start: HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			OrderNo string `json:"order_no"`
			Status  string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Code != 0 || envelope.Data.OrderNo != orderNo || envelope.Data.Status == "" {
		return errors.New("gateway paid start response mismatch")
	}
	return nil
}
