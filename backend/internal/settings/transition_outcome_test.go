package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

type transitionRetryError struct {
	cause error
	safe  bool
}

func (err *transitionRetryError) Error() string     { return "driver error: " + err.cause.Error() }
func (err *transitionRetryError) Unwrap() error     { return err.cause }
func (err *transitionRetryError) SafeToRetry() bool { return err.safe }

func TestTransitionCommitErrorClassification(t *testing.T) {
	uncertainCause := errors.New("sensitive driver detail")
	for _, tc := range []struct {
		name             string
		cause            error
		completed        bool
		commitNotApplied bool
		dialect          string
		uncertain        bool
	}{
		{name: "body error", cause: uncertainCause},
		{name: "transaction already rolled back", cause: fmt.Errorf("wrapped: %w", sql.ErrTxDone), completed: true},
		{name: "postgres commit rolled back", cause: fmt.Errorf("wrapped: %w", pgx.ErrTxCommitRollback), completed: true},
		{name: "sqlite commit not applied", cause: uncertainCause, completed: true, commitNotApplied: true},
		{name: "sqlite canceled before commit", cause: context.Canceled, completed: true, dialect: "sqlite"},
		{name: "sqlite deadline before commit", cause: context.DeadlineExceeded, completed: true, dialect: "sqlite"},
		{name: "sqlite wrapped cancellation", cause: fmt.Errorf("driver: %w", context.Canceled), completed: true, dialect: "sqlite", uncertain: true},
		{name: "sqlite wrapped deadline", cause: fmt.Errorf("driver: %w", context.DeadlineExceeded), completed: true, dialect: "sqlite", uncertain: true},
		{name: "postgres safe to retry", cause: fmt.Errorf("wrapped: %w", &transitionRetryError{cause: context.Canceled, safe: true}), completed: true, dialect: "postgres"},
		{name: "postgres unsafe retry", cause: &transitionRetryError{cause: context.Canceled}, completed: true, dialect: "postgres", uncertain: true},
		{name: "postgres canceled after dispatch", cause: context.Canceled, completed: true, dialect: "postgres", uncertain: true},
		{name: "postgres wrapped cancellation", cause: fmt.Errorf("driver: %w", context.Canceled), completed: true, dialect: "postgres", uncertain: true},
		{name: "postgres deadline before commit", cause: context.DeadlineExceeded, completed: true, dialect: "postgres"},
		{name: "postgres wrapped deadline after dispatch", cause: fmt.Errorf("driver: %w", context.DeadlineExceeded), completed: true, dialect: "postgres", uncertain: true},
		{name: "unknown commit", cause: uncertainCause, completed: true, uncertain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyTransitionError(tc.cause, tc.completed, tc.commitNotApplied, tc.dialect)
			if !errors.Is(err, tc.cause) {
				t.Fatalf("error %v does not retain its cause", err)
			}
			if got := errors.Is(err, ErrSettingOverrideCommitUncertain); got != tc.uncertain {
				t.Fatalf("uncertain=%v, want %v", got, tc.uncertain)
			}
			if err.Error() == tc.cause.Error() {
				t.Fatalf("error exposes the driver detail: %v", err)
			}
		})
	}
}
