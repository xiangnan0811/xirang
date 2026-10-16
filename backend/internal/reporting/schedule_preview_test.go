package reporting

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"xirang/backend/internal/model"
)

func TestSchedulePreviewWindows(t *testing.T) {
	shanghai := schedulePreviewLocation(t, "Asia/Shanghai")
	newYork := schedulePreviewLocation(t, "America/New_York")
	lordHowe := schedulePreviewLocation(t, "Australia/Lord_Howe")
	tests := []struct {
		name  string
		spec  string
		now   string
		loc   *time.Location
		start string
	}{
		{
			name:  "half_hour_fall_fold_does_not_skip_earliest_hour",
			spec:  "0 2 * * *",
			now:   "2027-04-04T01:15:00+11:00",
			loc:   lordHowe,
			start: "2027-04-04T02:00:00+10:30",
		},
		{
			name:  "half_hour_fall_fold_returns_second_0145",
			spec:  "45 1 * * *",
			now:   "2027-04-04T01:46:00+11:00",
			loc:   lordHowe,
			start: "2027-04-04T01:45:00+10:30",
		},
		{
			name:  "half_hour_fall_boundary_uses_new_offset",
			spec:  "30 1 * * *",
			now:   "2027-04-04T01:59:59.999999999+11:00",
			loc:   lordHowe,
			start: "2027-04-04T01:30:00+10:30",
		},
		{
			name:  "half_hour_fall_exact_boundary_is_current",
			spec:  "30 1 * * *",
			now:   "2027-04-04T01:30:00+10:30",
			loc:   lordHowe,
			start: "2027-04-04T01:30:00+10:30",
		},
		{
			name:  "half_hour_fall_boundary_future_minute",
			spec:  "31 1 * * *",
			now:   "2027-04-04T01:30:00+10:30",
			loc:   lordHowe,
			start: "2027-04-04T01:31:00+10:30",
		},
		{
			name:  "half_hour_fall_and_intersection",
			spec:  "0 2 4 * 0",
			now:   "2027-04-04T01:15:00+11:00",
			loc:   lordHowe,
			start: "2027-04-04T02:00:00+10:30",
		},
		{
			name:  "half_hour_fall_and_boundary_inclusive",
			spec:  "30 1 4 * 0",
			now:   "2027-04-04T01:59:59.999999999+11:00",
			loc:   lordHowe,
			start: "2027-04-04T01:30:00+10:30",
		},
		{
			name:  "half_hour_spring_gap_does_not_invent_0215",
			spec:  "15 2 * * *",
			now:   "2027-10-03T01:59:30+10:30",
			loc:   lordHowe,
			start: "2027-10-04T02:15:00+11:00",
		},
		{
			name:  "half_hour_spring_gap_keeps_earliest_0230",
			spec:  "30 2 * * *",
			now:   "2027-10-03T01:59:30+10:30",
			loc:   lordHowe,
			start: "2027-10-03T02:30:00+11:00",
		},
		{
			name:  "half_hour_spring_boundary_is_inclusive",
			spec:  "30 2 * * *",
			now:   "2027-10-03T01:59:59.999999999+10:30",
			loc:   lordHowe,
			start: "2027-10-03T02:30:00+11:00",
		},
		{
			name:  "half_hour_spring_exact_boundary_is_current",
			spec:  "30 2 * * *",
			now:   "2027-10-03T02:30:00+11:00",
			loc:   lordHowe,
			start: "2027-10-03T02:30:00+11:00",
		},
		{
			name:  "half_hour_spring_boundary_future_minute",
			spec:  "31 2 * * *",
			now:   "2027-10-03T02:30:00+11:00",
			loc:   lordHowe,
			start: "2027-10-03T02:31:00+11:00",
		},
		{
			name:  "half_hour_spring_and_intersection",
			spec:  "30 2 3 * 0",
			now:   "2027-10-03T01:59:30+10:30",
			loc:   lordHowe,
			start: "2027-10-03T02:30:00+11:00",
		},
		{
			name:  "server_local_weekly_window",
			spec:  "0 8 * * 1",
			now:   "2026-10-18T23:59:30+08:00",
			loc:   shanghai,
			start: "2026-10-19T08:00:00+08:00",
		},
		{
			name:  "current_minute_not_future_tick",
			spec:  "* * * * *",
			now:   "2026-10-15T12:34:45.123456789+08:00",
			loc:   shanghai,
			start: "2026-10-15T12:34:00+08:00",
		},
		{
			name:  "exact_now_is_current_window",
			spec:  "34 12 * * *",
			now:   "2026-10-15T12:34:00Z",
			loc:   time.UTC,
			start: "2026-10-15T12:34:00Z",
		},
		{
			name:  "window_last_nanosecond",
			spec:  "34 12 * * *",
			now:   "2026-10-15T12:34:59.999999999Z",
			loc:   time.UTC,
			start: "2026-10-15T12:34:00Z",
		},
		{
			name:  "window_end_is_exclusive",
			spec:  "34 12 * * *",
			now:   "2026-10-15T12:35:00Z",
			loc:   time.UTC,
			start: "2026-10-16T12:34:00Z",
		},
		{
			name:  "dom_and_dow_not_standard_or",
			spec:  "0 8 1 * 1",
			now:   "2026-10-02T00:00:00Z",
			loc:   time.UTC,
			start: "2027-02-01T08:00:00Z",
		},
		{
			name:  "dom_only_skips_missing_date",
			spec:  "0 8 31 * *",
			now:   "2026-04-30T10:00:00Z",
			loc:   time.UTC,
			start: "2026-05-31T08:00:00Z",
		},
		{
			name:  "spring_gap_does_not_invent_0230",
			spec:  "30 2 * * *",
			now:   "2026-03-08T01:59:30-05:00",
			loc:   newYork,
			start: "2026-03-09T02:30:00-04:00",
		},
		{
			name:  "fall_fold_returns_second_0130",
			spec:  "30 1 * * *",
			now:   "2026-11-01T01:31:00-04:00",
			loc:   newYork,
			start: "2026-11-01T01:30:00-05:00",
		},
		{
			name:  "fall_fold_current_first_0130",
			spec:  "30 1 * * *",
			now:   "2026-11-01T01:30:45-04:00",
			loc:   newYork,
			start: "2026-11-01T01:30:00-04:00",
		},
		{
			name:  "fall_fold_first_window_expired",
			spec:  "30 1 * * *",
			now:   "2026-11-01T01:31:00-05:00",
			loc:   newYork,
			start: "2026-11-02T01:30:00-05:00",
		},
		{
			name:  "intersection_preserves_fold_instant",
			spec:  "30 1 1 * 0",
			now:   "2026-11-01T01:31:00-04:00",
			loc:   newYork,
			start: "2026-11-01T01:30:00-05:00",
		},
		{
			name:  "numeric_prefix_not_standard_list",
			spec:  "5,10 8 * * *",
			now:   "2026-10-15T08:06:00Z",
			loc:   time.UTC,
			start: "2026-10-16T08:05:00Z",
		},
		{
			name:  "signed_zero_padded_and_partial_tokens",
			spec:  "+05junk 008tail 019/2 * 01,2",
			now:   "2026-10-18T23:59:30+08:00",
			loc:   shanghai,
			start: "2026-10-19T08:05:00+08:00",
		},
		{
			name:  "numeric_start_step_is_just_prefix",
			spec:  "5/15 8 * * *",
			now:   "2026-10-15T08:04:30Z",
			loc:   time.UTC,
			start: "2026-10-15T08:05:00Z",
		},
		{
			name:  "negative_zero_keeps_old_numeric_semantics",
			spec:  "-0 8 * * *",
			now:   "2026-10-15T07:59:30Z",
			loc:   time.UTC,
			start: "2026-10-15T08:00:00Z",
		},
		{
			name:  "whitespace_only_separates_fields",
			spec:  " \t5\n8  *\t* *  ",
			now:   "2026-10-15T08:04:30Z",
			loc:   time.UTC,
			start: "2026-10-15T08:05:00Z",
		},
		{
			name:  "non_whole_hour_timezone",
			spec:  "0 8 * * *",
			now:   "2026-10-15T07:59:45+05:45",
			loc:   time.FixedZone("quarter-hour", 5*3600+45*60),
			start: "2026-10-15T08:00:00+05:45",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := schedulePreviewTime(t, tt.now, tt.loc)
			preview, err := PreviewSchedule(context.Background(), tt.spec, now)
			if err != nil {
				t.Fatal(err)
			}
			assertSchedulePreviewMetadata(t, preview, now)
			want := schedulePreviewTime(t, tt.start, tt.loc)
			if preview.Status != "match" || preview.Reason != "" || preview.WindowStart == nil || preview.WindowEnd == nil {
				t.Fatalf("expected match, got %+v", preview)
			}
			if !preview.WindowStart.Equal(want) || !preview.WindowEnd.Equal(want.Add(time.Minute)) {
				t.Fatalf("window = [%s, %s), want [%s, %s)", preview.WindowStart, preview.WindowEnd, want, want.Add(time.Minute))
			}
			if preview.WindowEnd.Sub(*preview.WindowStart) != time.Minute || !preview.WindowEnd.After(now) || !preview.WindowStart.Before(preview.SearchUntil) {
				t.Fatalf("invalid matching window: %+v", preview)
			}
			cfg := model.ReportConfig{Cron: tt.spec}
			for _, instant := range []time.Time{*preview.WindowStart, preview.WindowStart.Add(time.Minute - time.Nanosecond)} {
				if !shouldGenerate(cfg, instant) {
					t.Fatalf("real matcher rejects returned window at %s", instant)
				}
			}
			if preview.WindowStart.Location() != tt.loc || preview.WindowEnd.Location() != tt.loc {
				t.Fatalf("window lost caller location: %+v", preview)
			}
		})
	}
}

func TestSchedulePreviewNoMatch(t *testing.T) {
	tests := []struct {
		spec   string
		reason string
	}{
		{"0 8 * 1 1", "unsupported_month"},
		{"0 8 * */1 1", "unsupported_month"},
		{"0 8 * *,1 1", "unsupported_month"},
		{"bad bad bad bad bad", "unsupported_month"},
		{"*/15 * * * *", "unmatchable_field"},
		{"* */2 * * *", "unmatchable_field"},
		{"0 8 */1 * *", "unmatchable_field"},
		{"0 8 * * MON", "unmatchable_field"},
		{"*suffix * * * *", "unmatchable_field"},
		{"60 * * * *", "unmatchable_field"},
		{"-1 * * * *", "unmatchable_field"},
		{"0 24 * * *", "unmatchable_field"},
		{"0 -1 * * *", "unmatchable_field"},
		{"0 8 0 * *", "unmatchable_field"},
		{"0 8 32 * *", "unmatchable_field"},
		{"0 8 * * 7", "unmatchable_field"},
		{"0 8 * * -1", "unmatchable_field"},
		{strings.Repeat("9", 100) + " * * * *", "unmatchable_field"},
	}
	now := schedulePreviewTime(t, "2026-10-15T12:34:45+08:00", schedulePreviewLocation(t, "Asia/Shanghai"))
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			preview, err := PreviewSchedule(context.Background(), tt.spec, now)
			if err != nil {
				t.Fatal(err)
			}
			assertSchedulePreviewMetadata(t, preview, now)
			if preview.Status != "no_match" || preview.Reason != tt.reason || preview.WindowStart != nil || preview.WindowEnd != nil {
				t.Fatalf("expected no_match/%s with null windows, got %+v", tt.reason, preview)
			}
			if shouldGenerate(model.ReportConfig{Cron: tt.spec}, now) {
				t.Fatal("real matcher unexpectedly accepts unmatchable expression")
			}
		})
	}
}

func TestSchedulePreviewInvalidAndCanceled(t *testing.T) {
	for _, spec := range []string{"", " \t\n", "0 8 * *", "0 0 8 * * *", "@daily", "TZ=UTC 0 8 * * *"} {
		t.Run("invalid/"+spec, func(t *testing.T) {
			_, err := PreviewSchedule(context.Background(), spec, reportingTimeAnchor)
			if !errors.Is(err, ErrInvalidSchedulePreview) {
				t.Fatalf("error = %v, want ErrInvalidSchedulePreview", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, spec := range []string{"", "* * * * *", "*/15 * * * *", "0 8 * 1 *"} {
		_, err := PreviewSchedule(ctx, spec, reportingTimeAnchor)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled preview %q: %v", spec, err)
		}
	}
	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer deadlineCancel()
	if _, err := PreviewSchedule(deadlineCtx, "* * * * *", reportingTimeAnchor); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired context error = %v", err)
	}

	// Cancel deterministically on the second intersection-loop check, after
	// the first unequal DOM/DOW candidates have been advanced. No timer race.
	searchCtx, searchCancel := context.WithCancel(context.Background())
	defer searchCancel()
	checked := &schedulePreviewCancelContext{Context: searchCtx, cancel: searchCancel, cancelAt: 5}
	_, err := PreviewSchedule(checked, "0 8 1 * 1", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	if !errors.Is(err, context.Canceled) || checked.checks != checked.cancelAt {
		t.Fatalf("intersection cancellation: error=%v checks=%d", err, checked.checks)
	}
}

func TestSchedulePreviewNumericPrefixCompatibility(t *testing.T) {
	for _, expr := range []string{"*", "5", "+5", "005", "5,10", "5/10", "5-10", "5junk", "5.9", "0x10", "-0", "-1", "abc", "*/15", "*junk", "++5", "", strings.Repeat("9", 100)} {
		t.Run(expr, func(t *testing.T) {
			n, wildcard, ok := parseReportField(expr)
			var original int
			_, originalErr := fmt.Sscanf(expr, "%d", &original)
			if expr == "*" {
				if !wildcard || !ok {
					t.Fatalf("wildcard parse = (%d, %v, %v)", n, wildcard, ok)
				}
			} else if wildcard || ok != (originalErr == nil) || (ok && n != original) {
				t.Fatalf("parse = (%d, %v, %v), original = (%d, %v)", n, wildcard, ok, original, originalErr)
			}
			for value := -1; value <= 60; value++ {
				want := expr == "*" || (originalErr == nil && original == value)
				if got := matchField(expr, value); got != want {
					t.Fatalf("matchField(%q, %d) = %v, original = %v", expr, value, got, want)
				}
			}
		})
	}
}

func TestSchedulePreviewEarliestRealMatch(t *testing.T) {
	newYork := schedulePreviewLocation(t, "America/New_York")
	lordHowe := schedulePreviewLocation(t, "Australia/Lord_Howe")
	tests := []struct {
		spec string
		now  string
		loc  *time.Location
	}{
		{"5,10 8 * * *", "2026-10-15T08:06:00Z", time.UTC},
		{"* 8 * * *", "2026-10-15T07:59:45Z", time.UTC},
		{"30 * * * *", "2026-10-15T12:30:01Z", time.UTC},
		{"0 8 * * 1", "2026-10-18T23:59:30Z", time.UTC},
		{"30 2 * * *", "2026-03-08T01:59:30-05:00", newYork},
		{"30 1 * * *", "2026-11-01T01:31:00-04:00", newYork},
		{"30 1 1 * 0", "2026-11-01T01:31:00-04:00", newYork},
		{"0 2 * * *", "2027-04-04T01:15:00+11:00", lordHowe},
		{"45 1 * * *", "2027-04-04T01:46:00+11:00", lordHowe},
		{"30 1 * * *", "2027-04-04T01:59:59.999999999+11:00", lordHowe},
		{"31 1 * * *", "2027-04-04T01:30:00+10:30", lordHowe},
		{"0 2 4 * 0", "2027-04-04T01:15:00+11:00", lordHowe},
		{"30 1 4 * 0", "2027-04-04T01:59:59.999999999+11:00", lordHowe},
		{"15 2 * * *", "2027-10-03T01:59:30+10:30", lordHowe},
		{"30 2 * * *", "2027-10-03T01:59:59.999999999+10:30", lordHowe},
		{"31 2 * * *", "2027-10-03T02:30:00+11:00", lordHowe},
		{"30 2 3 * 0", "2027-10-03T01:59:30+10:30", lordHowe},
		{"0 8 * * *", "2026-10-15T07:58:45+05:45", time.FixedZone("seconds-east", 5*3600+45*60+17)},
		{"0 8 15 * 4", "2026-10-15T07:58:45-03:30", time.FixedZone("seconds-west", -3*3600-30*60-23)},
	}
	for _, tt := range tests {
		t.Run(tt.spec+"/"+tt.now, func(t *testing.T) {
			now := schedulePreviewTime(t, tt.now, tt.loc)
			preview, err := PreviewSchedule(context.Background(), tt.spec, now)
			if err != nil || preview.WindowStart == nil {
				t.Fatalf("preview=%+v error=%v", preview, err)
			}
			cfg := model.ReportConfig{Cron: tt.spec}
			// Bounded test oracle only; production jumps with robfig. Walking
			// instants, not local dates, visits both sides of a DST fold.
			minute := now.Add(-time.Duration(now.Second())*time.Second - time.Duration(now.Nanosecond()))
			limit := now.Add(48 * time.Hour)
			for ; minute.Before(*preview.WindowStart) && minute.Before(limit); minute = minute.Add(time.Minute) {
				if shouldGenerate(cfg, minute) {
					t.Fatalf("preview skipped earlier real match %s before %s", minute, preview.WindowStart)
				}
			}
			if !minute.Equal(*preview.WindowStart) || !shouldGenerate(cfg, minute) {
				t.Fatalf("first real match was not reached within oracle bound: %+v", preview)
			}
		})
	}

	// The longer AND case only permits 08:00, so checking that instant on
	// each calendar date covers every possible earlier match without a
	// months-long minute scan.
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	cfg := model.ReportConfig{Cron: "0 8 1 * 1"}
	preview, err := PreviewSchedule(context.Background(), cfg.Cron, now)
	if err != nil || preview.WindowStart == nil {
		t.Fatalf("AND preview=%+v error=%v", preview, err)
	}
	for day := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC); day.Before(*preview.WindowStart); day = day.AddDate(0, 0, 1) {
		if shouldGenerate(cfg, day) {
			t.Fatalf("AND preview skipped earlier real match %s", day)
		}
	}
}

func TestSchedulePreviewExclusiveHorizon(t *testing.T) {
	// Ordinary supported fields always find matches well inside five years.
	// Use a real, test-owned TZif Location that skips midnight on every first
	// of the month from February 2026 through December 2030. This exercises
	// horizon exhaustion without replacing robfig or the report matcher.
	// Its final transition leaves January 2031 in the original UTC offset.
	var transitions []int64
	for month := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC); month.Before(time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)); month = month.AddDate(0, 1, 0) {
		transitions = append(transitions, month.Unix(), month.Add(time.Hour).Unix())
	}
	loc := schedulePreviewTZif(t, "preview/skipped-midnights", transitions, "")

	// January 1, 2026 is Thursday; January 1, 2031 is Wednesday.
	// All intervening DOM=1/DOW=3 midnight candidates are skipped by the
	// fixture's transitions. The first real match is exactly five years away.
	cfg := model.ReportConfig{Cron: "0 0 1 * 3"}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	boundary := now.AddDate(5, 0, 0)
	if shouldGenerate(cfg, now) || !shouldGenerate(cfg, boundary) {
		t.Fatal("fixture does not isolate the matching horizon boundary")
	}
	for day := now.AddDate(0, 0, 1); day.Before(boundary); day = day.AddDate(0, 0, 1) {
		// A skipped midnight normalizes to the preceding 23:00 or following
		// 01:00 depending on time.Date's choice; neither may match 00:00.
		midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
		if shouldGenerate(cfg, midnight) {
			t.Fatalf("fixture has an earlier matching midnight: %s", midnight)
		}
	}
	preview, err := PreviewSchedule(context.Background(), cfg.Cron, now)
	if err != nil {
		t.Fatal(err)
	}
	assertSchedulePreviewMetadata(t, preview, now)
	if preview.Status != "no_match" || preview.Reason != "horizon_exhausted" || preview.WindowStart != nil || preview.WindowEnd != nil {
		t.Fatalf("exclusive horizon admitted its boundary: %+v", preview)
	}

	// Moving only the evaluation seconds makes the same candidate fall
	// strictly inside the horizon. It must now be returned, not discarded.
	now = now.Add(30 * time.Second)
	preview, err = PreviewSchedule(context.Background(), cfg.Cron, now)
	if err != nil {
		t.Fatal(err)
	}
	assertSchedulePreviewMetadata(t, preview, now)
	if preview.Status != "match" || preview.Reason != "" || preview.WindowStart == nil || preview.WindowEnd == nil || !preview.WindowStart.Equal(boundary) || !preview.WindowEnd.Equal(boundary.Add(time.Minute)) {
		t.Fatalf("matching minute before horizon was discarded: %+v", preview)
	}
}

func TestSchedulePreviewHorizonAndJSON(t *testing.T) {
	loc := time.FixedZone("Local", 8*3600)
	now := time.Date(2028, 2, 29, 12, 34, 45, 123456789, loc)
	preview, err := PreviewSchedule(context.Background(), "* * * * *", now)
	if err != nil {
		t.Fatal(err)
	}
	assertSchedulePreviewMetadata(t, preview, now)
	wantHorizon := time.Date(2033, 3, 1, 12, 34, 45, 123456789, loc)
	if !preview.SearchUntil.Equal(wantHorizon) {
		t.Fatalf("horizon must use AddDate normalization, got %s want %s", preview.SearchUntil, wantHorizon)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for key, instant := range map[string]time.Time{
		"evaluated_at": now,
		"expires_at":   preview.ExpiresAt,
		"search_until": wantHorizon,
		"window_start": *preview.WindowStart,
		"window_end":   *preview.WindowEnd,
	} {
		var value string
		if err := json.Unmarshal(fields[key], &value); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if value != instant.Format(time.RFC3339Nano) || !strings.HasSuffix(value, "+08:00") {
			t.Fatalf("%s = %q, expected original RFC3339Nano offset", key, value)
		}
	}
	if string(fields["timezone"]) != `"Local"` || string(fields["reason"]) != `""` {
		t.Fatalf("lost Local label or empty match reason: %s", encoded)
	}
	noMatch, err := PreviewSchedule(context.Background(), "*/15 * * * *", now)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(noMatch)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["window_start"]) != "null" || string(fields["window_end"]) != "null" {
		t.Fatalf("no_match windows must serialize as null: %s", encoded)
	}
}

func TestSchedulePreviewSecondOffset(t *testing.T) {
	for _, offset := range []int{5*3600 + 45*60 + 17, -3*3600 - 30*60 - 23} {
		loc := time.FixedZone(fmt.Sprintf("seconds/%d", offset), offset)
		for _, spec := range []string{"0 8 * * *", "0 8 15 * 4"} {
			for _, current := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/current=%t", offset, spec, current), func(t *testing.T) {
					want := time.Date(2026, 10, 15, 8, 0, 0, 0, loc)
					now := want.Add(-15 * time.Second)
					if current {
						now = want.Add(45*time.Second + 123*time.Nanosecond)
					}
					preview, err := PreviewSchedule(context.Background(), spec, now)
					if err != nil {
						t.Fatal(err)
					}
					assertSchedulePreviewMetadata(t, preview, now)
					if preview.Status != "match" || preview.Reason != "" || preview.WindowStart == nil || preview.WindowEnd == nil ||
						!preview.WindowStart.Equal(want) || !preview.WindowEnd.Equal(want.Add(time.Minute)) {
						t.Fatalf("local minute must not be UTC-truncated: preview=%+v want=%s", preview, want)
					}
					if preview.WindowStart.Location() != loc || preview.WindowStart.Second() != 0 ||
						!shouldGenerate(model.ReportConfig{Cron: spec}, *preview.WindowStart) {
						t.Fatalf("returned minute lost local civil fields: %+v", preview)
					}
				})
			}
		}
	}
}

func TestSchedulePreviewCanceledBetweenTimezoneSegments(t *testing.T) {
	loc := schedulePreviewLocation(t, "Australia/Lord_Howe")
	now := schedulePreviewTime(t, "2027-10-03T01:59:30+10:30", loc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The old-offset segment cannot contain 02:15. Cancel before searching
	// the new-offset segment, rather than depending on a wall-clock race.
	checked := &schedulePreviewCancelContext{Context: ctx, cancel: cancel, cancelAt: 5}
	if _, err := PreviewSchedule(checked, "15 2 * * *", now); !errors.Is(err, context.Canceled) || checked.checks != checked.cancelAt {
		t.Fatalf("segment cancellation: error=%v checks=%d", err, checked.checks)
	}
}

func TestSchedulePreviewPOSIXTailLeapYearBound(t *testing.T) {
	// Explicit transitions end in 2026. Later DST behavior must come from
	// the POSIX footer, including Go's synthetic post-transition year bound.
	transitions := []int64{
		time.Date(2026, 3, 8, 2, 0, 0, 0, time.UTC).Unix(),
		time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC).Unix(),
	}
	loc := schedulePreviewTZif(t, "preview/posix-tail", transitions, "STD0DST,M3.2.0/2,M11.1.0/2")
	for _, tt := range []struct {
		month time.Month
		want  int
	}{{time.July, 3600}, {time.December, 0}} {
		_, offset := time.Date(2028, tt.month, 31, 12, 0, 0, 0, loc).Zone()
		if offset != tt.want {
			t.Fatalf("fixture footer offset in %s = %d, want %d", tt.month, offset, tt.want)
		}
	}
	staleEnd := time.Date(2028, 12, 31, 0, 0, 0, 0, time.UTC)
	nextYear := time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)
	_, end := staleEnd.Add(time.Hour).In(loc).ZoneBounds()
	// Also accept a future Go release correcting its 365-day bound.
	if !end.Equal(staleEnd) && !end.Equal(nextYear) {
		t.Fatalf("fixture did not reach the POSIX year-tail bound: %s", end)
	}
	tests := []struct {
		name  string
		spec  string
		now   string
		start string
	}{
		{"enter_leap_final_day", "0 0 * * *", "2028-12-30T23:59:30Z", "2028-12-31T00:00:00Z"},
		{"at_stale_bound", "0 8 * * *", "2028-12-31T00:00:00Z", "2028-12-31T08:00:00Z"},
		{"inside_leap_final_day", "0 8 * * *", "2028-12-31T07:59:30Z", "2028-12-31T08:00:00Z"},
		{"leap_year_rollover", "0 0 * * *", "2028-12-31T23:59:30Z", "2029-01-01T00:00:00Z"},
		{"and_at_rollover", "0 0 1 * 1", "2028-12-31T23:59:30Z", "2029-01-01T00:00:00Z"},
		{"rollover_current_window", "0 0 * * *", "2029-01-01T00:00:00Z", "2029-01-01T00:00:00Z"},
		{"ordinary_year_rollover", "0 0 * * *", "2027-12-31T23:59:30Z", "2028-01-01T00:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := schedulePreviewTime(t, tt.now, loc)
			want := schedulePreviewTime(t, tt.start, loc)
			preview, err := PreviewSchedule(context.Background(), tt.spec, now)
			if err != nil {
				t.Fatal(err)
			}
			assertSchedulePreviewMetadata(t, preview, now)
			if preview.Status != "match" || preview.Reason != "" || preview.WindowStart == nil || preview.WindowEnd == nil ||
				!preview.WindowStart.Equal(want) || !preview.WindowEnd.Equal(want.Add(time.Minute)) {
				t.Fatalf("POSIX tail window=%+v, want %s", preview, want)
			}
			cfg := model.ReportConfig{Cron: tt.spec}
			minute := now.Add(-time.Duration(now.Second())*time.Second - time.Duration(now.Nanosecond()))
			for ; minute.Before(want); minute = minute.Add(time.Minute) {
				if shouldGenerate(cfg, minute) {
					t.Fatalf("POSIX tail skipped earlier real match at %s", minute)
				}
			}
			if !shouldGenerate(cfg, want) || !shouldGenerate(cfg, want.Add(time.Minute-time.Nanosecond)) {
				t.Fatalf("real matcher rejects POSIX tail window at %s", want)
			}
		})
	}
}

// Both fixtures use alternating standard/DST transitions. The second TZif
// block carries 64-bit instants; a nonempty footer supplies recurring rules.
func schedulePreviewTZif(t *testing.T, name string, transitions []int64, footer string) *time.Location {
	t.Helper()
	var data []byte
	for _, width := range []int{4, 8} {
		header := make([]byte, 44)
		copy(header, "TZif2")
		binary.BigEndian.PutUint32(header[32:36], uint32(len(transitions)))
		binary.BigEndian.PutUint32(header[36:40], 2)
		binary.BigEndian.PutUint32(header[40:44], 8)
		data = append(data, header...)
		for _, transition := range transitions {
			if width == 4 {
				data = binary.BigEndian.AppendUint32(data, uint32(transition))
			} else {
				data = binary.BigEndian.AppendUint64(data, uint64(transition))
			}
		}
		for i := range transitions {
			data = append(data, byte(1-i%2))
		}
		data = binary.BigEndian.AppendUint32(data, 0)
		data = append(data, 0, 0) // Standard offset, abbreviation STD.
		data = binary.BigEndian.AppendUint32(data, 3600)
		data = append(data, 1, 4) // DST offset, abbreviation DST.
		data = append(data, "STD\x00DST\x00"...)
	}
	data = append(data, '\n')
	data = append(data, footer...)
	data = append(data, '\n')
	loc, err := time.LoadLocationFromTZData(name, data)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func schedulePreviewLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func schedulePreviewTime(t *testing.T, value string, loc *time.Location) time.Time {
	t.Helper()
	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return instant.In(loc)
}

func assertSchedulePreviewMetadata(t *testing.T, preview SchedulePreview, now time.Time) {
	t.Helper()
	minuteStart := now.Add(-time.Duration(now.Second())*time.Second - time.Duration(now.Nanosecond()))
	if !preview.EvaluatedAt.Equal(now) || preview.EvaluatedAt.Location() != now.Location() {
		t.Fatalf("evaluated_at lost the supplied time or location: %+v", preview)
	}
	if !preview.ExpiresAt.Equal(minuteStart.Add(time.Minute)) || !preview.ExpiresAt.After(now) || preview.ExpiresAt.Sub(now) > time.Minute {
		t.Fatalf("expires_at is not the next local minute boundary: %+v", preview)
	}
	if !preview.SearchUntil.Equal(now.AddDate(5, 0, 0)) || preview.SearchUntil.Location() != now.Location() {
		t.Fatalf("search_until is not five local calendar years: %+v", preview)
	}
	if preview.Timezone != now.Location().String() {
		t.Fatalf("timezone = %q, want %q", preview.Timezone, now.Location().String())
	}
}

type schedulePreviewCancelContext struct {
	context.Context
	cancel   context.CancelFunc
	cancelAt int
	checks   int
}

func (c *schedulePreviewCancelContext) Err() error {
	c.checks++
	if c.checks == c.cancelAt {
		c.cancel()
	}
	return c.Context.Err()
}
