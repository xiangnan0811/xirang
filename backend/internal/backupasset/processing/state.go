package processing

import (
	"fmt"
	"time"
)

type ProcessingState string

const (
	ProcessingQueued          ProcessingState = "queued"
	ProcessingLeased          ProcessingState = "leased"
	ProcessingFetching        ProcessingState = "fetching"
	ProcessingMaterializing   ProcessingState = "materializing"
	ProcessingProcessing      ProcessingState = "processing"
	ProcessingUploading       ProcessingState = "uploading"
	ProcessingValidating      ProcessingState = "validating"
	ProcessingRetryWait       ProcessingState = "retry_wait"
	ProcessingCancelRequested ProcessingState = "cancel_requested"
	ProcessingCanceled        ProcessingState = "canceled"
	ProcessingSucceeded       ProcessingState = "succeeded"
	ProcessingFailed          ProcessingState = "failed"
	ProcessingSuperseded      ProcessingState = "superseded"
	ProcessingExpired         ProcessingState = "expired"
)

var allProcessingStates = []ProcessingState{
	ProcessingQueued, ProcessingLeased, ProcessingFetching, ProcessingMaterializing,
	ProcessingProcessing, ProcessingUploading, ProcessingValidating, ProcessingRetryWait,
	ProcessingCancelRequested, ProcessingCanceled, ProcessingSucceeded, ProcessingFailed,
	ProcessingSuperseded, ProcessingExpired,
}

var validProcessingStates = func() map[ProcessingState]bool {
	result := make(map[ProcessingState]bool, len(allProcessingStates))
	for _, state := range allProcessingStates {
		result[state] = true
	}
	return result
}()

func AllProcessingStates() []ProcessingState {
	return append([]ProcessingState(nil), allProcessingStates...)
}

func (state ProcessingState) Valid() bool { return validProcessingStates[state] }

var processingTransitions = map[[2]ProcessingState]bool{
	{ProcessingQueued, ProcessingLeased}:            true,
	{ProcessingQueued, ProcessingCancelRequested}:   true,
	{ProcessingQueued, ProcessingSuperseded}:        true,
	{ProcessingQueued, ProcessingExpired}:           true,
	{ProcessingLeased, ProcessingFetching}:          true,
	{ProcessingFetching, ProcessingMaterializing}:   true,
	{ProcessingFetching, ProcessingProcessing}:      true,
	{ProcessingMaterializing, ProcessingProcessing}: true,
	{ProcessingProcessing, ProcessingUploading}:     true,
	{ProcessingUploading, ProcessingValidating}:     true,
	{ProcessingValidating, ProcessingSucceeded}:     true,
	{ProcessingRetryWait, ProcessingQueued}:         true,
	{ProcessingCancelRequested, ProcessingCanceled}: true,
}

func ValidateStateTransition(from, to ProcessingState) error {
	if !from.Valid() || !to.Valid() || from == to {
		return fmt.Errorf("%w: unknown or unchanged state", ErrInvalidTransition)
	}
	if processingTransitions[[2]ProcessingState{from, to}] {
		return nil
	}
	if !isTerminalState(from) {
		switch to {
		case ProcessingCancelRequested:
			if from != ProcessingCancelRequested {
				return nil
			}
		case ProcessingSuperseded, ProcessingExpired:
			return nil
		case ProcessingRetryWait:
			if isAttemptOwnedState(from) {
				return nil
			}
		case ProcessingFailed:
			if isAttemptOwnedState(from) || from == ProcessingRetryWait {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, from, to)
}

func isAttemptOwnedState(state ProcessingState) bool {
	switch state {
	case ProcessingLeased, ProcessingFetching, ProcessingMaterializing, ProcessingProcessing, ProcessingUploading, ProcessingValidating:
		return true
	default:
		return false
	}
}

func isTerminalState(state ProcessingState) bool {
	switch state {
	case ProcessingCanceled, ProcessingSucceeded, ProcessingFailed, ProcessingSuperseded, ProcessingExpired:
		return true
	default:
		return false
	}
}

type ProcessingErrorCategory string

const (
	PermanentError        ProcessingErrorCategory = "permanent"
	TransientError        ProcessingErrorCategory = "transient"
	ContractSecurityError ProcessingErrorCategory = "contract_security"
)

type ProcessingErrorCode string

const (
	ProcessingErrorUnsupportedFormat       ProcessingErrorCode = "unsupported_format"
	ProcessingErrorEncryptedArchive        ProcessingErrorCode = "encrypted_archive"
	ProcessingErrorInputTooLarge           ProcessingErrorCode = "input_too_large"
	ProcessingErrorMaterializationDisabled ProcessingErrorCode = "materialization_disabled"
	ProcessingErrorSourceChanged           ProcessingErrorCode = "source_changed"
	ProcessingErrorSourceExpired           ProcessingErrorCode = "source_expired"
	ProcessingErrorWorkerUnavailable       ProcessingErrorCode = "worker_unavailable"
	ProcessingErrorProviderUnavailable     ProcessingErrorCode = "provider_unavailable"
	ProcessingErrorQuotaBusy               ProcessingErrorCode = "quota_busy"
	ProcessingErrorTimeout                 ProcessingErrorCode = "timeout"
	ProcessingErrorWorkerCrash             ProcessingErrorCode = "worker_crash"
	ProcessingErrorLeaseLost               ProcessingErrorCode = "lease_lost"
	ProcessingErrorProtocolIncompatible    ProcessingErrorCode = "protocol_incompatible"
	ProcessingErrorInvalidOutput           ProcessingErrorCode = "invalid_output"
	ProcessingErrorDigestMismatch          ProcessingErrorCode = "digest_mismatch"
	ProcessingErrorSandboxViolation        ProcessingErrorCode = "sandbox_violation"
	ProcessingErrorNetworkViolation        ProcessingErrorCode = "network_violation"
)

func (code ProcessingErrorCode) Category() (ProcessingErrorCategory, error) {
	switch code {
	case ProcessingErrorUnsupportedFormat, ProcessingErrorEncryptedArchive, ProcessingErrorInputTooLarge,
		ProcessingErrorMaterializationDisabled, ProcessingErrorSourceChanged, ProcessingErrorSourceExpired:
		return PermanentError, nil
	case ProcessingErrorWorkerUnavailable, ProcessingErrorProviderUnavailable, ProcessingErrorQuotaBusy,
		ProcessingErrorTimeout, ProcessingErrorWorkerCrash, ProcessingErrorLeaseLost:
		return TransientError, nil
	case ProcessingErrorProtocolIncompatible, ProcessingErrorInvalidOutput, ProcessingErrorDigestMismatch,
		ProcessingErrorSandboxViolation, ProcessingErrorNetworkViolation:
		return ContractSecurityError, nil
	default:
		return "", fmt.Errorf("%w: unknown processing error code", ErrInvalidContract)
	}
}

type CancelReason string

const (
	CancelReasonInterestWithdrawn CancelReason = "interest_withdrawn"
	CancelReasonAdminRequested    CancelReason = "admin_requested"
	CancelReasonShutdown          CancelReason = "shutdown"
)

type SupersedeReason string

const (
	SupersedeReasonSourceChanged   SupersedeReason = "source_changed"
	SupersedeReasonPipelineChanged SupersedeReason = "pipeline_changed"
	SupersedeReasonPolicyChanged   SupersedeReason = "policy_changed"
)

type ExpiryReason string

const (
	ExpiryReasonSource        ExpiryReason = "source_expired"
	ExpiryReasonRecoveryPoint ExpiryReason = "recovery_point_expired"
	ExpiryReasonDeadline      ExpiryReason = "deadline_expired"
)

type TransitionRequest struct {
	From             ProcessingState
	To               ProcessingState
	CurrentRevision  int64
	ExpectedRevision int64
	ErrorCode        ProcessingErrorCode
	RetryAt          *time.Time
	RetryExhausted   bool
	CancelReason     CancelReason
	SupersedeReason  SupersedeReason
	ExpiryReason     ExpiryReason
}

func ValidateTransition(request TransitionRequest) (int64, error) {
	if request.CurrentRevision <= 0 || request.ExpectedRevision != request.CurrentRevision {
		return 0, fmt.Errorf("%w: got %d want %d", ErrRevisionConflict, request.ExpectedRevision, request.CurrentRevision)
	}
	if err := ValidateStateTransition(request.From, request.To); err != nil {
		return 0, err
	}
	if err := validateTransitionProduct(request); err != nil {
		return 0, err
	}
	return request.CurrentRevision + 1, nil
}

func validateTransitionProduct(request TransitionRequest) error {
	if request.To == ProcessingRetryWait {
		category, err := request.ErrorCode.Category()
		if err != nil || category != TransientError || request.RetryAt == nil || request.RetryExhausted {
			return fmt.Errorf("%w: retry_wait requires transient error and schedule", ErrInvalidContract)
		}
	} else if request.RetryAt != nil {
		return fmt.Errorf("%w: retry schedule outside retry_wait", ErrInvalidContract)
	}
	if request.To == ProcessingFailed {
		category, err := request.ErrorCode.Category()
		if err != nil || category == TransientError && !request.RetryExhausted || category != TransientError && request.RetryExhausted {
			return fmt.Errorf("%w: failed requires permanent or contract/security error", ErrInvalidContract)
		}
	} else if request.To != ProcessingRetryWait && request.ErrorCode != "" || request.To != ProcessingFailed && request.RetryExhausted {
		return fmt.Errorf("%w: error code outside error transition", ErrInvalidContract)
	}
	if (request.To == ProcessingCancelRequested || request.To == ProcessingCanceled) != (request.CancelReason != "") {
		return fmt.Errorf("%w: cancellation reason product", ErrInvalidContract)
	}
	if (request.To == ProcessingSuperseded) != (request.SupersedeReason != "") {
		return fmt.Errorf("%w: supersede reason product", ErrInvalidContract)
	}
	if (request.To == ProcessingExpired) != (request.ExpiryReason != "") {
		return fmt.Errorf("%w: expiry reason product", ErrInvalidContract)
	}
	return nil
}
