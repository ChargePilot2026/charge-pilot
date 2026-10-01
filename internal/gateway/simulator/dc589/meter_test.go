package dc589sim

import (
	"testing"
	"time"
)

func TestMeterTicksRetainFractionalTimeAndCatchUp(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		ticks   []time.Duration
		seconds []int
		total   int
		last    time.Duration
	}{
		{"nanosecond jitter", []time.Duration{time.Second, 2*time.Second - time.Nanosecond, 3 * time.Second}, []int{1, 0, 2}, 3, 3 * time.Second},
		{"fractional carry", []time.Duration{time.Second, 2250 * time.Millisecond, 3*time.Second - time.Nanosecond, 4 * time.Second}, []int{1, 1, 0, 2}, 4, 4 * time.Second},
		{"missed ticks", []time.Duration{time.Second, 5750 * time.Millisecond, 6 * time.Second}, []int{1, 4, 1}, 6, 6 * time.Second},
		{"duplicate and early ticks", []time.Duration{time.Second, time.Second, 0, 500 * time.Millisecond, 2 * time.Second}, []int{1, 0, 0, 0, 1}, 2, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &board{}
			total := 0
			for i, tick := range tc.ticks {
				seconds := b.meterSeconds(base.Add(tick))
				if seconds != tc.seconds[i] {
					t.Fatalf("tick %d advanced %d seconds, want %d", i, seconds, tc.seconds[i])
				}
				total += seconds
			}
			if total != tc.total || !b.lastMeter.Equal(base.Add(tc.last)) {
				t.Fatalf("advanced %d seconds through %s, want %d through %s", total, b.lastMeter, tc.total, base.Add(tc.last))
			}
		})
	}
}
