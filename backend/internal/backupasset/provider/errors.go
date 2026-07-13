package provider

import (
	"errors"
	"fmt"

	"xirang/backend/internal/backupasset"
)

var (
	ErrInvalidCursor = errors.New("invalid provider cursor")
	ErrStaleCursor   = errors.New("stale provider cursor")
)

type CapabilityError struct {
	Reason backupasset.CapabilityReason
}

func (err *CapabilityError) Error() string {
	if err == nil {
		return backupasset.ErrCapabilityUnavailable.Error()
	}
	return fmt.Sprintf("%s: %s", backupasset.ErrCapabilityUnavailable, err.Reason.Code)
}

func (*CapabilityError) Unwrap() error { return backupasset.ErrCapabilityUnavailable }

func newCapabilityError(code backupasset.CapabilityCode) error {
	return &CapabilityError{Reason: backupasset.CapabilityReason{Code: code}}
}
