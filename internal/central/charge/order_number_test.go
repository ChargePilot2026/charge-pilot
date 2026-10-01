package charge

import (
	"testing"
	"time"
)

func TestChargeOrderNumberUsesBeijingTimeAndPaddedPort(t *testing.T) {
	at := time.Date(2026, 10, 1, 2, 42, 0, 123000000, time.UTC)
	for _, tc := range []struct {
		port uint8
		want string
	}{{1, "20261001104200534824051408265201"}, {12, "20261001104200534824051408265212"}} {
		got, err := chargeOrderNumber(at, "5348240514082652", tc.port)
		if err != nil || got != tc.want {
			t.Fatal(got, err, tc.want)
		}
	}
	if _, err := chargeOrderNumber(at, "5348240514082652", 0); err == nil {
		t.Fatal("zero port accepted")
	}
	if _, err := chargeOrderNumber(time.Time{}, "5348240514082652", 1); err == nil {
		t.Fatal("zero start time accepted")
	}
}
