package delivery

import (
	"strings"
	"testing"
)

func TestParseStreamEntry(t *testing.T) {
	event, err := parseStreamEntry(map[string]any{
		"source":  "gateway",
		"payload": `{"event_id":"evt-1","event_type":"charge_started","occurred_at":"2026-09-29T12:00:00Z","data":{"order_id":"o-1"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.EventID != "evt-1" || event.EventType != "charge_started" || event.Source != "gateway" {
		t.Fatalf("unexpected event %+v", event)
	}
	// source 字段缺失时回退到信封内。
	event, err = parseStreamEntry(map[string]any{
		"payload": `{"event_id":"evt-2","event_type":"charge_ended","source":"user"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Source != "user" {
		t.Fatalf("source=%q want user", event.Source)
	}
}

func TestParseStreamEntryRejectsGarbage(t *testing.T) {
	if _, err := parseStreamEntry(map[string]any{"payload": ""}); err == nil {
		t.Fatal("empty payload accepted")
	}
	if _, err := parseStreamEntry(map[string]any{"payload": `{"event_id":"e"}`}); err == nil {
		t.Fatal("event without type accepted")
	}
	if _, err := parseStreamEntry(map[string]any{"payload": `not json`}); err == nil {
		t.Fatal("invalid json accepted")
	}
}

func TestSubscriptionWants(t *testing.T) {
	if !subscriptionWants([]string{"*", "charge_started"}, "charge_ended") {
		t.Fatal("wildcard should match any event")
	}
	if !subscriptionWants([]string{"charge_started"}, "charge_started") {
		t.Fatal("exact type should match")
	}
	if subscriptionWants([]string{"charge_started"}, "charge_ended") {
		t.Fatal("different type should not match")
	}
	if subscriptionWants(nil, "charge_started") {
		t.Fatal("empty subscription should match nothing")
	}
}

func TestSignPayloadStable(t *testing.T) {
	sig := signPayload("secret", 1727600000, []byte(`{"a":1}`))
	if !strings.HasPrefix(sig, "sha256=") {
		t.Fatalf("signature %q missing scheme prefix", sig)
	}
	if signPayload("secret", 1727600000, []byte(`{"a":1}`)) != sig {
		t.Fatal("signature is not deterministic")
	}
	if signPayload("other", 1727600000, []byte(`{"a":1}`)) == sig {
		t.Fatal("different secret produced the same signature")
	}
}
