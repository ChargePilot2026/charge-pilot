package charge

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol/dc589"
	"github.com/google/uuid"
)

// CardDispatcher 把原生刷卡事件决策后回复设备；事件清单、决策冻结、
// 投递推进与端口解析经 gateway 内部端点，本组件不持有 gateway 库句柄。
type CardDispatcher struct {
	Gateway                              serviceclient.Client
	CentralURL, GatewayURL, ServiceToken string
	Client                               *http.Client
}

type cardReply struct {
	Accepted     bool                 `json:"accepted"`
	DeviceID     string               `json:"device_id"`
	Session      string               `json:"session"`
	Kind         protocol.CommandKind `json:"kind"`
	CardNumber   uint32               `json:"card_number"`
	BalanceUnits uint16               `json:"balance_units"`
	Invalid      bool                 `json:"invalid"`
}

func (d CardDispatcher) Run(ctx context.Context) (int, error) {
	if d.GatewayURL == "" || d.ServiceToken == "" {
		return 0, errors.New("card dispatcher is not configured")
	}
	gateway := newGatewaySyncAPI(d.Gateway, d.GatewayURL, d.ServiceToken)
	var list struct {
		Items []struct {
			EventKey string `json:"event_key"`
			Payload  string `json:"payload"`
			Response string `json:"response"`
		} `json:"items"`
	}
	if err := gateway.get(ctx, "/api/v1/internal/card-events", &list); err != nil {
		return 0, err
	}
	done := 0
	var first error
	for _, item := range list.Items {
		reply, err := d.decisionFor(ctx, gateway, item.EventKey, item.Payload, item.Response)
		if err == nil && !reply.Accepted {
			var ack struct {
				Accepted bool `json:"accepted"`
			}
			err = d.call(ctx, d.GatewayURL, "POST", "/api/v1/internal/cards/reply", reply, &ack)
			if err == nil && !ack.Accepted {
				err = errors.New("card reply not accepted")
			}
		}
		if err != nil {
			if first == nil {
				first = err
			}
			message := err.Error()
			if len(message) > 255 {
				message = message[:255]
			}
			var ignored struct{}
			if aerr := gateway.post(ctx, "/api/v1/internal/card-events/advance", map[string]any{"event_key": item.EventKey, "outcome": "retry", "error": message}, &ignored); aerr != nil {
				return done, aerr
			}
			continue
		}
		var ignored struct{}
		if err := gateway.post(ctx, "/api/v1/internal/card-events/advance", map[string]any{"event_key": item.EventKey, "outcome": "done"}, &ignored); err != nil {
			return done, err
		}
		done++
	}
	return done, first
}

// decisionFor 返回冻结后的决策回复：清单里已有冻结回复时直接使用；
// 否则解析事件、决策并经 decide-finish 首写冻结，竞争输掉时采用胜出方回复。
// 决策是事件的纯函数，首写冻结保证同一事件只有一个回复事实。
func (d CardDispatcher) decisionFor(ctx context.Context, gateway gatewaySyncAPI, key, payload, frozen string) (cardReply, error) {
	var reply cardReply
	if frozen != "" {
		return reply, json.Unmarshal([]byte(frozen), &reply)
	}
	var e protocol.Event
	if json.Unmarshal([]byte(payload), &e) != nil || e.EventID != key || uuid.Validate(e.EventID) != nil {
		return reply, errors.New("card event identity mismatch")
	}
	reply, err := d.decide(ctx, gateway, e)
	if err != nil {
		return reply, err
	}
	raw, _ := json.Marshal(reply)
	var finish struct {
		Stored       bool   `json:"stored"`
		ResponseJSON string `json:"response_json"`
	}
	if err := gateway.post(ctx, "/api/v1/internal/card-events/decide-finish", map[string]any{"event_key": key, "response_json": string(raw)}, &finish); err != nil {
		return reply, err
	}
	if !finish.Stored {
		// 并发决策已先冻结：以胜出方回复为准，二者本应是同一事实。
		if err := json.Unmarshal([]byte(finish.ResponseJSON), &reply); err != nil {
			return reply, err
		}
	}
	return reply, nil
}

func (d CardDispatcher) decide(ctx context.Context, gateway gatewaySyncAPI, e protocol.Event) (cardReply, error) {
	r := cardReply{DeviceID: e.DeviceID, Session: hex.EncodeToString(e.SessionID[:]), CardNumber: e.CardNumber}
	card := strconv.FormatUint(uint64(e.CardNumber), 10)
	if e.Type == protocol.CardSwipe {
		if e.Port == 0xFF {
			r.Kind, r.Invalid = protocol.CommandCardDenied, true
			return r, nil
		}
		var resolved struct {
			Found    bool   `json:"found"`
			PortCode string `json:"port_code"`
		}
		if err := gateway.get(ctx, "/api/v1/internal/ports/resolve?device_id="+url.QueryEscape(e.DeviceID)+"&port_no="+strconv.Itoa(int(e.Port)), &resolved); err != nil {
			return r, err
		}
		if !resolved.Found {
			return r, errors.New("card port not provisioned")
		}
		var op struct {
			OperationID string `json:"operation_id"`
		}
		err := d.call(ctx, d.CentralURL, "POST", "/api/v1/internal/cards/swipe", map[string]any{"event_id": e.EventID, "card_no": card, "port_id": resolved.PortCode, "occurred_at": e.ReceivedAt}, &op)
		if err == nil {
			if uuid.Validate(op.OperationID) != nil {
				return r, errors.New("card operation response missing identity")
			}
			r.Accepted = true
			return r, nil
		}
		if !errors.Is(err, ErrCardDeclined) {
			return r, err
		}
		r.Kind = protocol.CommandCardDenied
		var declined *cardDecline
		r.Invalid = !errors.As(err, &declined) || !declined.InsufficientBalance
	} else if e.Type == protocol.CardBalanceQuery {
		r.Kind = protocol.CommandCardBalance
		if e.CardNumber == 0 {
			r.Invalid = true
			return r, nil
		}
	} else {
		return r, errors.New("unexpected card event type")
	}
	var balance struct {
		Valid        bool  `json:"valid"`
		BalanceCents int64 `json:"balance_cents"`
	}
	if err := d.call(ctx, d.CentralURL, "GET", "/api/v1/internal/cards/balance?card_no="+card, nil, &balance); err != nil {
		return r, err
	}
	r.Invalid = r.Invalid || !balance.Valid
	r.BalanceUnits = dc589.CardBalanceUnits(balance.BalanceCents)
	return r, nil
}

var ErrCardDeclined = errors.New("card operation declined")

type cardDecline struct{ InsufficientBalance bool }

func (e *cardDecline) Error() string { return ErrCardDeclined.Error() }
func (e *cardDecline) Unwrap() error { return ErrCardDeclined }

func (d CardDispatcher) call(ctx context.Context, base, method, path string, body, out any) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("invalid card service URL")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Token", d.ServiceToken)
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 409 {
		var rejected struct {
			Data struct {
				InsufficientBalance bool `json:"insufficient_balance"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&rejected); err != nil {
			return err
		}
		return &cardDecline{InsufficientBalance: rejected.Data.InsufficientBalance}
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("card service returned %d", resp.StatusCode)
	}
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Code != 0 || len(envelope.Data) == 0 {
		return errors.New("invalid card service envelope")
	}
	return json.Unmarshal(envelope.Data, out)
}
