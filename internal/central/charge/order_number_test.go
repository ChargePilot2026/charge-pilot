package charge

import (
	"strings"
	"testing"
	"time"
)

func TestChargeOrderNumberUsesBeijingTimeAndPaddedPort(t *testing.T) {
	at := time.Date(2026, 10, 1, 2, 42, 0, 123000000, time.UTC)
	for _, tc := range []struct {
		port uint8
		want string
	}{{1, "C20261001104200534824051408265201"}, {12, "C20261001104200534824051408265212"}, {99, "C20261001104200534824051408265299"}} {
		got, err := chargeOrderNumber(at, "5348240514082652", tc.port)
		if err != nil || got != tc.want {
			t.Fatal(got, err, tc.want)
		}
	}
	for _, port := range []uint8{0, 100, 255} {
		if _, err := chargeOrderNumber(at, "5348240514082652", port); err == nil {
			t.Fatalf("port outside two-digit range accepted: %d", port)
		}
	}
	if _, err := chargeOrderNumber(time.Time{}, "5348240514082652", 1); err == nil {
		t.Fatal("zero start time accepted")
	}
}

func TestChargeOrderNumberKeepsLengthAndIdentityBoundaries(t *testing.T) {
	at := time.Date(2026, 10, 1, 16, 0, 0, 0, time.UTC)
	device := strings.Repeat("A", 47)
	got, err := chargeOrderNumber(at, device, 1)
	if err != nil || len(got) != 64 || got != "C20261002000000"+device+"01" {
		t.Fatalf("maximum length or Beijing date rollover: %q %v", got, err)
	}
	for _, device := range []string{"", strings.Repeat("A", 48), "board/1", "board 1", "board?1"} {
		if _, err := chargeOrderNumber(at, device, 1); err == nil {
			t.Fatalf("invalid device accepted: %q", device)
		}
	}
	if _, err := chargeOrderNumber(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), "board", 1); err == nil {
		t.Fatal("timestamp with more than 14 digits accepted")
	}
}
