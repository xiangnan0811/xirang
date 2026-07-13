package repository

import (
	"errors"
	"fmt"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
)

type CapabilityError struct {
	Reason        backupasset.CapabilityReason
	CorrelationID string
}

func (err *CapabilityError) Error() string {
	if err == nil {
		return backupasset.ErrCapabilityUnavailable.Error()
	}
	return fmt.Sprintf("%s: %s", backupasset.ErrCapabilityUnavailable, err.Reason.Code)
}

func (*CapabilityError) Unwrap() error { return backupasset.ErrCapabilityUnavailable }

func capabilityError(code backupasset.CapabilityCode, correlationID string) error {
	return &CapabilityError{Reason: backupasset.CapabilityReason{Code: code}, CorrelationID: correlationID}
}

func CapabilityFromError(err error) (backupasset.CapabilityReason, string, bool) {
	var repositoryError *CapabilityError
	if errors.As(err, &repositoryError) && repositoryError != nil && backupasset.ValidateCapabilityReason(repositoryError.Reason) == nil {
		return repositoryError.Reason, repositoryError.CorrelationID, true
	}
	var providerError *provider.CapabilityError
	if errors.As(err, &providerError) && providerError != nil && backupasset.ValidateCapabilityReason(providerError.Reason) == nil {
		return providerError.Reason, "", true
	}
	if errors.Is(err, backupasset.ErrProviderUnavailable) {
		return backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable}, "", true
	}
	return backupasset.CapabilityReason{}, "", false
}
