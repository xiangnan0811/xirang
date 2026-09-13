package cronutil

import (
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Next returns the next UTC activation for spec. Invalid or empty expressions
// return nil.
func Next(spec string) *time.Time {
	if strings.TrimSpace(spec) == "" {
		return nil
	}
	schedule, err := parser.Parse(spec)
	if err != nil {
		return nil
	}
	next := schedule.Next(time.Now().UTC())
	if next.IsZero() {
		return nil
	}
	next = next.UTC()
	return &next
}

// NextAfter returns the first UTC activation strictly after after. Invalid or
// empty expressions return nil.
func NextAfter(spec string, after time.Time) *time.Time {
	if strings.TrimSpace(spec) == "" {
		return nil
	}
	schedule, err := parser.Parse(spec)
	if err != nil {
		return nil
	}
	next := schedule.Next(after.UTC())
	if next.IsZero() {
		return nil
	}
	next = next.UTC()
	return &next
}
