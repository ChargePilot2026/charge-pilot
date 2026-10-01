package snowflake

import (
	"testing"
	"time"
)

func TestAdvanceSurvivesSameMillisecondRollbackAndRestart(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	first, persisted, err := advance(state{}, now)
	if err != nil || first <= 1<<53 || first >= 1<<63 {
		t.Fatalf("id=%d err=%v", first, err)
	}
	second, persisted, err := advance(persisted, now)
	if err != nil || second != first+1 {
		t.Fatalf("same millisecond id=%d err=%v", second, err)
	}
	third, _, err := advance(persisted, now-1000)
	if err != nil || third != second+1 {
		t.Fatalf("persisted state after clock rollback id=%d err=%v", third, err)
	}
}

func TestAdvanceSequenceOverflowAndTimeAdvance(t *testing.T) {
	current := state{ID: 1, LastMillisecond: 123, Sequence: maxSequence}
	id, next, err := advance(current, epochMillis+123)
	if err != nil || next.LastMillisecond != 124 || next.Sequence != 0 || id != 124<<(nodeBits+sequenceBits) {
		t.Fatalf("overflow id=%d state=%+v err=%v", id, next, err)
	}
	_, next, err = advance(next, epochMillis+125)
	if err != nil || next.LastMillisecond != 125 || next.Sequence != 0 {
		t.Fatalf("later clock state=%+v err=%v", next, err)
	}
}

func TestAdvanceRejectsInvalidAndExhaustedState(t *testing.T) {
	for _, tc := range []struct {
		current state
		now     int64
	}{
		{state{}, epochMillis},
		{state{}, epochMillis - 1},
		{state{LastMillisecond: maxMillis + 1}, epochMillis + 1},
		{state{Sequence: maxSequence + 1}, epochMillis + 1},
		{state{LastMillisecond: maxMillis, Sequence: maxSequence}, epochMillis + maxMillis},
		{state{}, epochMillis + maxMillis + 1},
	} {
		if _, _, err := advance(tc.current, tc.now); err == nil {
			t.Fatalf("accepted state=%+v clock=%d", tc.current, tc.now)
		}
	}
}
