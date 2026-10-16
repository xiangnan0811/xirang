package reporting

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"

	"github.com/robfig/cron/v3"
)

// ErrInvalidSchedulePreview identifies an empty or non-five-field expression.
// It does not change validation of saved report configurations.
var ErrInvalidSchedulePreview = errors.New("invalid report schedule preview expression")

// SchedulePreview describes a matching minute, not a scheduled execution or a
// guarantee that a report will be generated or delivered. All times retain the
// location supplied to PreviewSchedule.
type SchedulePreview struct {
	Status      string     `json:"status"`
	Reason      string     `json:"reason"`
	EvaluatedAt time.Time  `json:"evaluated_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	Timezone    string     `json:"timezone"`
	SearchUntil time.Time  `json:"search_until"`
	WindowStart *time.Time `json:"window_start" extensions:"x-nullable"`
	WindowEnd   *time.Time `json:"window_end" extensions:"x-nullable"`
}

// PreviewSchedule returns the current or next minute accepted by the existing
// report matcher, using now.Location() and a five-calendar-year exclusive search
// horizon. Unsupported matcher fields are a no_match result, not a save error.
// It performs no I/O and does not inspect configuration state or generate reports.
func PreviewSchedule(ctx context.Context, spec string, now time.Time) (SchedulePreview, error) {
	if err := ctx.Err(); err != nil {
		return SchedulePreview{}, err
	}
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return SchedulePreview{}, ErrInvalidSchedulePreview
	}

	minuteStart := now.Add(-time.Duration(now.Second())*time.Second - time.Duration(now.Nanosecond()))
	preview := SchedulePreview{
		Status:      "no_match",
		Reason:      "horizon_exhausted",
		EvaluatedAt: now,
		ExpiresAt:   minuteStart.Add(time.Minute),
		Timezone:    now.Location().String(),
		SearchUntil: now.AddDate(5, 0, 0),
	}
	if parts[3] != "*" {
		preview.Reason = "unsupported_month"
		return preview, nil
	}

	// Never parse the original expression with robfig: the real matcher accepts
	// numeric prefixes, rejects steps, and requires DOM AND DOW rather than OR.
	normalized := [5]string{"", "", "", "*", ""}
	for _, field := range [...]struct {
		index int
		min   int
		max   int
	}{
		{0, 0, 59},
		{1, 0, 23},
		{2, 1, 31},
		{4, 0, 6},
	} {
		value, wildcard, ok := parseReportField(parts[field.index])
		if !ok || (!wildcard && (value < field.min || value > field.max)) {
			preview.Reason = "unmatchable_field"
			return preview, nil
		}
		if wildcard {
			normalized[field.index] = "*"
		} else {
			normalized[field.index] = strconv.Itoa(value)
		}
	}

	cfg := model.ReportConfig{Cron: spec}
	candidate := minuteStart
	if !shouldGenerate(cfg, now) {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		bothDays := normalized[2] != "*" && normalized[4] != "*"
		firstFields := normalized
		if bothDays {
			firstFields[4] = "*"
		}
		first, err := parser.Parse(strings.Join(firstFields[:], " "))
		if err != nil {
			return SchedulePreview{}, fmt.Errorf("parse normalized report preview schedule: %w", err)
		}
		var second cron.Schedule
		if bothDays {
			secondFields := normalized
			secondFields[2] = "*"
			second, err = parser.Parse(strings.Join(secondFields[:], " "))
			if err != nil {
				return SchedulePreview{}, fmt.Errorf("parse normalized report preview weekday schedule: %w", err)
			}
		}

		candidate, err = nextReportPreviewMatch(ctx, first, second, now, preview.SearchUntil)
		if err != nil {
			return SchedulePreview{}, err
		}
		if candidate.IsZero() {
			return preview, nil
		}
	}

	if err := ctx.Err(); err != nil {
		return SchedulePreview{}, err
	}
	end := candidate.Add(time.Minute)
	if !shouldGenerate(cfg, candidate) || !end.After(now) || !candidate.Before(preview.SearchUntil) {
		return SchedulePreview{}, errors.New("normalized report preview disagrees with report matcher")
	}
	preview.Status = "match"
	preview.Reason = ""
	preview.WindowStart = &candidate
	preview.WindowEnd = &end
	return preview, nil
}

// Search each constant-offset interval independently. robfig's fixed hour jumps
// can skip matches across half-hour DST changes; UTC civil surrogates have no
// transitions and also align minutes correctly for offsets containing seconds.
func nextReportPreviewMatch(ctx context.Context, first, second cron.Schedule, now, horizon time.Time) (time.Time, error) {
	loc := now.Location()
	for cursor := now; cursor.Before(horizon); {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		_, offset := cursor.Zone()
		zoneStart, zoneEnd := cursor.ZoneBounds()
		if !zoneEnd.IsZero() && !zoneEnd.After(cursor) {
			// Go's POSIX-tail lookup uses Jan 1 + 365 days as the upper
			// bound after the year's last transition, even in leap years.
			// Only that exact stale bound is safe to extend to next Jan 1.
			year := cursor.UTC().Year()
			yearStart := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
			nextYear := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)
			staleEnd := yearStart.Add(365 * 24 * time.Hour)
			if nextYear.Sub(yearStart) != 366*24*time.Hour || !zoneEnd.Equal(staleEnd) ||
				zoneStart.Before(yearStart) || !zoneStart.Before(staleEnd) || !cursor.Before(nextYear) {
				return time.Time{}, errors.New("nonadvancing report preview timezone bound")
			}
			zoneEnd = nextYear
		}
		segmentEnd := horizon
		if !zoneEnd.IsZero() && zoneEnd.Before(segmentEnd) {
			segmentEnd = zoneEnd
		}
		shift := time.Duration(offset) * time.Second
		after := cursor.UTC().Add(shift)
		if !cursor.Equal(now) {
			// Later segments include their first instant, using the NEW offset.
			after = after.Add(-time.Nanosecond)
		}
		civilEnd := segmentEnd.UTC().Add(shift)
		civil, err := reportPreviewSegmentMatch(ctx, first, second, after, civilEnd)
		if err != nil {
			return time.Time{}, err
		}
		if !civil.IsZero() {
			candidate := civil.Add(-shift).In(loc)
			if candidate.Before(cursor) || !candidate.After(now) || !candidate.Before(segmentEnd) {
				return time.Time{}, errors.New("report preview candidate outside timezone segment")
			}
			return candidate, nil
		}
		cursor = segmentEnd
	}
	return time.Time{}, nil
}

// The cap is exclusive: no intersection may jump past this offset's interval.
func reportPreviewSegmentMatch(ctx context.Context, first, second cron.Schedule, after, cap time.Time) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	a, err := advanceReportPreviewSchedule(first, after)
	if err != nil {
		return time.Time{}, err
	}
	var b time.Time
	if second != nil {
		b, err = advanceReportPreviewSchedule(second, after)
		if err != nil {
			return time.Time{}, err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		if a.IsZero() || !a.Before(cap) || (second != nil && (b.IsZero() || !b.Before(cap))) {
			return time.Time{}, nil
		}
		if second == nil || a.Equal(b) {
			return a, nil
		}
		if a.Before(b) {
			a, err = advanceReportPreviewSchedule(first, b.Add(-time.Nanosecond))
		} else {
			b, err = advanceReportPreviewSchedule(second, a.Add(-time.Nanosecond))
		}
		if err != nil {
			return time.Time{}, err
		}
	}
}

func advanceReportPreviewSchedule(schedule cron.Schedule, after time.Time) (time.Time, error) {
	next := schedule.Next(after)
	if !next.IsZero() && !next.After(after) {
		return time.Time{}, errors.New("nonadvancing normalized report preview schedule")
	}
	return next, nil
}
