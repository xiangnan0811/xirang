package task

import (
	"context"
	"testing"
	"time"
)

// waitWallClockPast waits until the wall clock is strictly past deadline, even
// if the clock moves backwards while a monotonic timer is running.
func waitWallClockPast(t *testing.T, ctx context.Context, deadline time.Time, slack time.Duration) {
	t.Helper()
	if !waitWallClockPastWith(deadline, slack, time.Now, func(d time.Duration) bool {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			return true
		case <-ctx.Done():
			return false
		}
	}) {
		t.Fatalf("waiting for wall-clock deadline: %v", ctx.Err())
	}
}

func waitWallClockPastWith(deadline time.Time, slack time.Duration, now func() time.Time, wait func(time.Duration) bool) bool {
	deadline = deadline.UTC()
	for {
		remaining := deadline.Sub(now().UTC()) + slack
		if remaining > 0 && !wait(remaining) {
			return false
		}
		if now().UTC().After(deadline) {
			return true
		}
	}
}

func TestWaitWallClockPast(t *testing.T) {
	deadline := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	current := deadline.Add(-time.Second)
	waits := 0
	if !waitWallClockPastWith(deadline, 10*time.Millisecond, func() time.Time { return current }, func(d time.Duration) bool {
		waits++
		switch waits {
		case 1:
			if d != 1010*time.Millisecond {
				t.Fatalf("first wait = %s", d)
			}
			current = deadline.Add(-100 * time.Millisecond)
		case 2:
			if d != 110*time.Millisecond {
				t.Fatalf("second wait = %s", d)
			}
			current = deadline
		case 3:
			if d != 10*time.Millisecond {
				t.Fatalf("third wait = %s", d)
			}
			current = deadline.Add(time.Nanosecond)
		default:
			t.Fatal("unexpected extra wait")
		}
		return true
	}) || waits != 3 {
		t.Fatalf("returned before wall clock passed deadline, waits=%d", waits)
	}
	if waitWallClockPastWith(deadline, 0, func() time.Time { return deadline.Add(-time.Second) }, func(time.Duration) bool { return false }) {
		t.Fatal("canceled wait reported success")
	}
}
