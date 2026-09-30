package charge

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol/dc589"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type CardDispatcher struct {
	GatewayDB                            *gorm.DB
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
	if d.GatewayDB == nil || d.ServiceToken == "" {
		return 0, errors.New("card dispatcher is not configured")
	}
	var rows []struct {
		EventKey                string
		EventJSON, ResponseJSON []byte
	}
	err := d.GatewayDB.WithContext(ctx).Table("card_event_delivery x").Select("x.event_key,e.event_json,x.response_json").Joins("JOIN device_event e ON e.event_key=x.event_key").Where("x.status='pending' AND x.next_attempt_at<=UTC_TIMESTAMP(3)").Order("e.id").Limit(100).Find(&rows).Error
	if err != nil {
		return 0, err
	}
	done := 0
	var first error
	for _, r := range rows {
		reply, decisionErr := d.decisionFor(ctx, r.EventKey)
		err = decisionErr
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
			if updateErr := d.GatewayDB.WithContext(ctx).Table("card_event_delivery").Where("event_key=?", r.EventKey).Updates(map[string]any{"attempts": gorm.Expr("attempts+1"), "next_attempt_at": time.Now().UTC().Add(5 * time.Second), "last_error": message}).Error; updateErr != nil {
				return done, updateErr
			}
			continue
		}
		if err := d.GatewayDB.WithContext(ctx).Table("card_event_delivery").Where("event_key=?", r.EventKey).Updates(map[string]any{"status": "done", "last_error": nil}).Error; err != nil {
			return done, err
		}
		done++
	}
	return done, first
}

// Serialize the decision across workers and freeze it before a socket reply.
// A lost central response is retried with the same committed event UUID.
func (d CardDispatcher) decisionFor(ctx context.Context, key string) (cardReply, error) {
	var reply cardReply
	err := d.GatewayDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored struct {
			ResponseJSON []byte
			Status       string
		}
		if err := tx.Table("card_event_delivery").Clauses(clause.Locking{Strength: "UPDATE"}).Where("event_key=?", key).Take(&stored).Error; err != nil {
			return err
		}
		if len(stored.ResponseJSON) > 0 {
			return json.Unmarshal(stored.ResponseJSON, &reply)
		}
		var row struct{ EventJSON []byte }
		if err := tx.Table("device_event").Where("event_key=?", key).Take(&row).Error; err != nil {
			return err
		}
		var e protocol.Event
		if json.Unmarshal(row.EventJSON, &e) != nil || e.EventID != key || uuid.Validate(e.EventID) != nil {
			return errors.New("card event identity mismatch")
		}
		var err error
		reply, err = d.decide(ctx, e)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(reply)
		return tx.Table("card_event_delivery").Where("event_key=?", key).Update("response_json", string(raw)).Error
	})
	return reply, err
}

func (d CardDispatcher) decide(ctx context.Context, e protocol.Event) (cardReply, error) {
	r := cardReply{DeviceID: e.DeviceID, Session: hex.EncodeToString(e.SessionID[:]), CardNumber: e.CardNumber}
	card := strconv.FormatUint(uint64(e.CardNumber), 10)
	if e.Type == protocol.CardSwipe {
		if e.Port == 0xFF {
			r.Kind, r.Invalid = protocol.CommandCardDenied, true
			return r, nil
		}
		var port string
		if err := d.GatewayDB.WithContext(ctx).Table("device_port").Where("device_id=? AND port_no=? AND deleted_at IS NULL", e.DeviceID, e.Port).Pluck("port_code", &port).Error; err != nil {
			return r, err
		}
		if port == "" {
			return r, errors.New("card port not provisioned")
		}
		var op struct {
			OperationID string `json:"operation_id"`
		}
		err := d.call(ctx, d.CentralURL, "POST", "/api/v1/internal/cards/swipe", map[string]any{"event_id": e.EventID, "card_no": card, "port_id": port, "occurred_at": e.ReceivedAt}, &op)
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
