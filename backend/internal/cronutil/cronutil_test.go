package cronutil

import (
	"testing"
	"time"
)

func TestNextAfterReturnsUTCActivation(t *testing.T) {
	after := time.Date(2026, 9, 14, 2, 3, 4, 0, time.FixedZone("test-zone", 8*60*60))
	got := NextAfter("0 * * * *", after)
	if got == nil {
		t.Fatal("NextAfter returned nil for valid expression")
	}
	want := time.Date(2026, 9, 13, 19, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("NextAfter = %s, want %s", got, want)
	}
	if got.Location() != time.UTC {
		t.Fatalf("NextAfter location = %v, want UTC", got.Location())
	}
}

func TestNextRejectsEmptyAndInvalidExpressions(t *testing.T) {
	if got := Next(""); got != nil {
		t.Fatalf("Next(empty) = %v, want nil", got)
	}
	if got := Next("not a cron expression"); got != nil {
		t.Fatalf("Next(invalid) = %v, want nil", got)
	}
	if got := NextAfter("", time.Now()); got != nil {
		t.Fatalf("NextAfter(empty) = %v, want nil", got)
	}
}
