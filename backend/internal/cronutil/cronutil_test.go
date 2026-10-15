package cronutil

import (
	"testing"
	"time"
	_ "time/tzdata"
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

// Task cron explanations in the web UI promise that unprefixed expressions are
// scheduled in UTC and that TZ=/CRON_TZ= prefixes select their own timezone,
// regardless of the caller's local zone.
func TestNextAfterTimezoneBasis(t *testing.T) {
	after := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load America/New_York: %v", err)
	}
	cases := []struct {
		name string
		spec string
		from time.Time
		want time.Time
	}{
		{"unprefixed uses UTC", "0 2 * * *", after, time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC)},
		{"unprefixed ignores caller zone", "0 2 * * *", after.In(newYork), time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC)},
		{"CRON_TZ prefix selects zone", "CRON_TZ=Asia/Shanghai 0 2 * * *", after, time.Date(2026, 9, 14, 18, 0, 0, 0, time.UTC)},
		{"TZ prefix selects zone", "TZ=America/New_York 0 2 * * *", after, time.Date(2026, 9, 14, 6, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NextAfter(tc.spec, tc.from)
			if got == nil {
				t.Fatalf("NextAfter(%q) returned nil", tc.spec)
			}
			if !got.Equal(tc.want) || got.Location() != time.UTC {
				t.Fatalf("NextAfter(%q) = %s, want %s (UTC)", tc.spec, got, tc.want)
			}
		})
	}
}
