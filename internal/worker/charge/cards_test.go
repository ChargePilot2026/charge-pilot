package charge

import (
	"context"
	"errors"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCardServiceFailuresDoNotBecomeBusinessDeclines(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		body              string
		declined, balance bool
	}{
		{"insufficient", 409, `{"code":2009,"data":{"insufficient_balance":true}}`, true, true},
		{"invalid operation", 409, `{"code":2009,"data":null}`, true, false},
		{"server unavailable", 503, `{"code":5001}`, false, false},
		{"malformed rejection", 409, `bad response`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Service-Token") != "token" {
					t.Error("missing authentication")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			var out any
			err := (CardDispatcher{ServiceToken: "token"}).call(context.Background(), server.URL, "POST", "/api/v1/internal/cards/swipe", map[string]string{"event_id": "stable"}, &out)
			if err == nil || errors.Is(err, ErrCardDeclined) != tc.declined {
				t.Fatalf("%v", err)
			}
			var declined *cardDecline
			if tc.declined && (!errors.As(err, &declined) || declined.InsufficientBalance != tc.balance) {
				t.Fatalf("%+v", err)
			}
		})
	}
}

func TestBalanceQueryAndInvalidPortProduceNativeReplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("card_no") != "17564161" {
			t.Error(r.URL)
		}
		w.Write([]byte(`{"code":0,"data":{"valid":true,"balance_cents":889}}`))
	}))
	defer server.Close()
	d := CardDispatcher{CentralURL: server.URL, ServiceToken: "token"}
	gateway := gatewaySyncAPI{}
	r, err := d.decide(context.Background(), gateway, protocol.Event{Type: protocol.CardBalanceQuery, DeviceID: "device", CardNumber: 17564161})
	if err != nil || r.Kind != protocol.CommandCardBalance || r.Invalid || r.BalanceUnits != 88 || r.Session != "000000000000" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = d.decide(context.Background(), gateway, protocol.Event{Type: protocol.CardSwipe, Port: 255, CardNumber: 17564161})
	if err != nil || r.Kind != protocol.CommandCardDenied || !r.Invalid {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = d.decide(context.Background(), gateway, protocol.Event{Type: protocol.CardBalanceQuery, CardNumber: 0})
	if err != nil || !r.Invalid || r.BalanceUnits != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}
