package content

import (
	"context"
	"errors"
	"fmt"
)

// SourceFailureStage is the closed, non-sensitive stage vocabulary exposed by
// the delivery-ticket boundary. It deliberately carries no provider evidence,
// asset identity, locator, path, or command output.
type SourceFailureStage string

const (
	SourceFailureOpen         SourceFailureStage = "open"
	SourceFailureRead         SourceFailureStage = "read"
	SourceFailureChanged      SourceFailureStage = "changed"
	SourceFailureTimeout      SourceFailureStage = "timeout"
	SourceFailureCancellation SourceFailureStage = "cancellation"
	SourceFailureCapability   SourceFailureStage = "capability"
)

type SourceFailureError struct {
	stage SourceFailureStage
	cause error
}

func (failure *SourceFailureError) Error() string {
	if failure == nil {
		return "backup asset content source failure"
	}
	return fmt.Sprintf("backup asset content source %s failure", failure.stage)
}

func (failure *SourceFailureError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func NewSourceFailureError(stage SourceFailureStage, cause error) error {
	if !validSourceFailureStage(stage) {
		return ErrContentSourceUnavailable
	}
	if cause == nil {
		cause = ErrContentSourceUnavailable
	}
	var existing *SourceFailureError
	if errors.As(cause, &existing) {
		return cause
	}
	return &SourceFailureError{stage: stage, cause: cause}
}

func classifySourceFailure(stage SourceFailureStage, cause error) error {
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		stage = SourceFailureTimeout
	case errors.Is(cause, context.Canceled):
		stage = SourceFailureCancellation
	}
	return NewSourceFailureError(stage, cause)
}

func SourceFailureStageFromError(err error) (SourceFailureStage, bool) {
	var failure *SourceFailureError
	if !errors.As(err, &failure) || failure == nil || !validSourceFailureStage(failure.stage) {
		return "", false
	}
	return failure.stage, true
}

func validSourceFailureStage(stage SourceFailureStage) bool {
	switch stage {
	case SourceFailureOpen, SourceFailureRead, SourceFailureChanged, SourceFailureTimeout,
		SourceFailureCancellation, SourceFailureCapability:
		return true
	default:
		return false
	}
}
