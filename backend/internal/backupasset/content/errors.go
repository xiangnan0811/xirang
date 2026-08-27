package content

import (
	"errors"
	"fmt"

	"xirang/backend/internal/backupasset"
)

// CapabilityError preserves the closed capability reason for an Issue failure.
// It intentionally carries no provider evidence or resource locator.
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

func contentCapabilityError(code backupasset.CapabilityCode) error {
	return &CapabilityError{Reason: backupasset.CapabilityReason{Code: code}}
}

func CapabilityFromError(err error) (backupasset.CapabilityReason, bool) {
	var capabilityErr *CapabilityError
	if errors.As(err, &capabilityErr) && capabilityErr != nil && capabilityErr.Reason.Params == nil &&
		(capabilityErr.Reason.Code == backupasset.CapabilitySequentialReadUnavailable ||
			capabilityErr.Reason.Code == backupasset.CapabilityRangeUnavailable) &&
		backupasset.ValidateCapabilityReason(capabilityErr.Reason) == nil {
		return capabilityErr.Reason, true
	}
	return backupasset.CapabilityReason{}, false
}
