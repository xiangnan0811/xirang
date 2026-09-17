package cronutil

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// InvalidSpecError identifies a malformed persisted/user cron expression.
// Callers may isolate this typed configuration error while continuing to
// surface infrastructure failures.
type InvalidSpecError struct {
	Spec string
	Err  error
}

func (e *InvalidSpecError) Error() string {
	if e == nil {
		return "invalid cron expression"
	}
	return fmt.Sprintf("invalid cron expression %q: %v", e.Spec, e.Err)
}

func (e *InvalidSpecError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsInvalidSpecError reports whether err is a parser-level configuration
// error, including wrappers from scheduler registration.
func IsInvalidSpecError(err error) bool {
	var invalid *InvalidSpecError
	return errors.As(err, &invalid)
}

// Parse returns the same five-field/descriptor schedule used by runtime
// registration. Empty expressions represent manual execution and return a
// nil schedule without an error.
func Parse(spec string) (cron.Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	schedule, err := parser.Parse(spec)
	if err != nil {
		return nil, &InvalidSpecError{Spec: spec, Err: err}
	}
	return schedule, nil
}

// Validate checks a cron expression using the runtime parser.
func Validate(spec string) error {
	_, err := Parse(spec)
	return err
}

// Next returns the next UTC activation for spec. Invalid or empty expressions
// return nil.
func Next(spec string) *time.Time {
	schedule, err := Parse(spec)
	if err != nil || schedule == nil {
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
	schedule, err := Parse(spec)
	if err != nil || schedule == nil {
		return nil
	}
	next := schedule.Next(after.UTC())
	if next.IsZero() {
		return nil
	}
	next = next.UTC()
	return &next
}
