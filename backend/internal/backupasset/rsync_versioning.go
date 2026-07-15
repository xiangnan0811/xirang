package backupasset

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

const RsyncPublicationConfigSchemaVersion = 1

type RsyncVersioningMigrationChoice string

const (
	RsyncVersioningImportedBaseline RsyncVersioningMigrationChoice = "imported_baseline"
	RsyncVersioningFirstNewPoint    RsyncVersioningMigrationChoice = "first_new_point"
)

type RsyncVersioningState string

const (
	RsyncVersioningLegacy            RsyncVersioningState = "legacy"
	RsyncVersioningPreflightRequired RsyncVersioningState = "preflight_required"
	RsyncVersioningReady             RsyncVersioningState = "ready"
	RsyncVersioningPreparing         RsyncVersioningState = "preparing"
	RsyncVersioningVerifying         RsyncVersioningState = "verifying"
	RsyncVersioningCommitted         RsyncVersioningState = "committed"
	RsyncVersioningFailed            RsyncVersioningState = "failed"
	RsyncVersioningBlocked           RsyncVersioningState = "blocked"
	RsyncVersioningRollbackPrepared  RsyncVersioningState = "rollback_prepared"
)

type RsyncVersioningReasonCode string

const (
	RsyncVersioningReasonLegacy              RsyncVersioningReasonCode = "legacy"
	RsyncVersioningReasonPreflightRequired   RsyncVersioningReasonCode = "preflight_required"
	RsyncVersioningReasonReady               RsyncVersioningReasonCode = "ready"
	RsyncVersioningReasonPreflightExpired    RsyncVersioningReasonCode = "preflight_expired"
	RsyncVersioningReasonTaskRevisionChanged RsyncVersioningReasonCode = "task_revision_changed"
	RsyncVersioningReasonPreflightMismatch   RsyncVersioningReasonCode = "preflight_mismatch"
	RsyncVersioningReasonRootDrift           RsyncVersioningReasonCode = "root_drift"
	RsyncVersioningReasonUnsupported         RsyncVersioningReasonCode = "unsupported"
	RsyncVersioningReasonAdmissionBlocked    RsyncVersioningReasonCode = "admission_blocked"
	RsyncVersioningReasonRollbackPrepared    RsyncVersioningReasonCode = "rollback_prepared"
)

type RsyncVersioningEstimateBucket string

const (
	RsyncVersioningEstimateUnknown     RsyncVersioningEstimateBucket = "unknown"
	RsyncVersioningEstimateConstrained RsyncVersioningEstimateBucket = "constrained"
	RsyncVersioningEstimateAvailable   RsyncVersioningEstimateBucket = "available"
)

// RsyncVersioningPreflightRequest starts a local, bounded preflight for one
// existing legacy Rsync task. The managed root is derived server-side.
type RsyncVersioningPreflightRequest struct {
	TaskID               uint                `json:"task_id"`
	ExpectedTaskRevision uint64              `json:"expected_task_revision"`
	RequestedMode        TaskPublicationMode `json:"requested_mode"`
}

func (request RsyncVersioningPreflightRequest) Validate() error {
	if request.TaskID == 0 || request.ExpectedTaskRevision == 0 {
		return fmt.Errorf("%w: invalid Rsync versioning preflight request", ErrInvalidState)
	}
	switch request.RequestedMode {
	case PublicationVersionedHardlink, PublicationVersionedFullCopy:
		return nil
	default:
		return fmt.Errorf("%w: unsupported Rsync versioning preflight mode", ErrInvalidState)
	}
}

// RsyncVersioningActivationRequest consumes one exact unexpired preflight.
// The migration choice is explicit because imported_baseline and
// first_new_point have intentionally different provider effects.
type RsyncVersioningActivationRequest struct {
	TaskID               uint                           `json:"task_id"`
	ExpectedTaskRevision uint64                         `json:"expected_task_revision"`
	PreflightID          string                         `json:"preflight_id"`
	MigrationChoice      RsyncVersioningMigrationChoice `json:"migration_choice"`
}

func (request RsyncVersioningActivationRequest) Validate() error {
	if request.TaskID == 0 || request.ExpectedTaskRevision == 0 || ValidateOpaqueID(request.PreflightID) != nil {
		return fmt.Errorf("%w: invalid Rsync versioning activation request", ErrInvalidState)
	}
	switch request.MigrationChoice {
	case RsyncVersioningImportedBaseline, RsyncVersioningFirstNewPoint:
		return nil
	default:
		return fmt.Errorf("%w: unsupported Rsync versioning migration choice", ErrInvalidState)
	}
}

type RsyncVersioningRollbackPreparationRequest struct {
	TaskID               uint   `json:"task_id"`
	ExpectedTaskRevision uint64 `json:"expected_task_revision"`
}

func (request RsyncVersioningRollbackPreparationRequest) Validate() error {
	if request.TaskID == 0 || request.ExpectedTaskRevision == 0 {
		return fmt.Errorf("%w: invalid Rsync versioning rollback request", ErrInvalidState)
	}
	return nil
}

// RsyncVersioningSummary is the only task-facing projection of publication
// configuration and progress. It intentionally has no filesystem or provider
// execution facts.
type RsyncVersioningSummary struct {
	Mode               TaskPublicationMode       `json:"mode"`
	State              RsyncVersioningState      `json:"state"`
	ReasonCode         RsyncVersioningReasonCode `json:"reason_code"`
	CapabilityRevision uint64                    `json:"capability_revision"`
	// TaskRevision is an exact decimal CAS token, intentionally represented as
	// a string so JavaScript clients cannot lose nanosecond precision.
	TaskRevision         string `json:"task_revision"`
	SeedFullCopyRequired bool   `json:"seed_full_copy_required"`
}

func (summary RsyncVersioningSummary) Validate() error {
	switch summary.Mode {
	case PublicationLegacyMutable, PublicationVersionedHardlink, PublicationVersionedFullCopy:
	default:
		return fmt.Errorf("%w: unsupported Rsync versioning summary mode", ErrInvalidState)
	}
	if !validRsyncVersioningState(summary.State) || !validRsyncVersioningReason(summary.ReasonCode) {
		return fmt.Errorf("%w: invalid Rsync versioning summary", ErrInvalidState)
	}
	if summary.CapabilityRevision == 0 {
		return fmt.Errorf("%w: missing Rsync versioning capability revision", ErrInvalidState)
	}
	if !validRsyncVersioningTaskRevision(summary.TaskRevision) {
		return fmt.Errorf("%w: invalid Rsync versioning Task revision", ErrInvalidState)
	}
	return nil
}

func validRsyncVersioningTaskRevision(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed != 0 && strconv.FormatUint(parsed, 10) == value
}

type RsyncVersioningPreflightResult struct {
	PreflightID        string                        `json:"preflight_id"`
	Mode               TaskPublicationMode           `json:"mode"`
	State              RsyncVersioningState          `json:"state"`
	ReasonCode         RsyncVersioningReasonCode     `json:"reason_code"`
	CapabilityRevision uint64                        `json:"capability_revision"`
	ExpiresAt          time.Time                     `json:"expires_at"`
	CapacityEstimate   RsyncVersioningEstimateBucket `json:"capacity_estimate"`
	InodeEstimate      RsyncVersioningEstimateBucket `json:"inode_estimate"`
}

type RsyncVersioningActivationResult struct {
	Summary         RsyncVersioningSummary         `json:"summary"`
	MigrationChoice RsyncVersioningMigrationChoice `json:"migration_choice"`
}

type RsyncVersioningRollbackPreparationResult struct {
	Summary RsyncVersioningSummary `json:"summary"`
}

// EncodeRsyncPublicationConfig stores only the closed publication policy in a
// Task executor config. It intentionally cannot represent paths, arguments,
// preflight evidence, or credentials.
func EncodeRsyncPublicationConfig(mode TaskPublicationMode) (string, error) {
	switch mode {
	case PublicationLegacyMutable, PublicationVersionedHardlink, PublicationVersionedFullCopy:
	default:
		return "", fmt.Errorf("%w: unsupported Rsync publication config mode", ErrInvalidState)
	}
	encoded, err := json.Marshal(struct {
		Version         int                 `json:"version"`
		PublicationMode TaskPublicationMode `json:"publication_mode"`
	}{Version: RsyncPublicationConfigSchemaVersion, PublicationMode: mode})
	if err != nil {
		return "", fmt.Errorf("encode Rsync publication config: %w", err)
	}
	return string(encoded), nil
}

func validRsyncVersioningState(value RsyncVersioningState) bool {
	switch value {
	case RsyncVersioningLegacy, RsyncVersioningPreflightRequired, RsyncVersioningReady,
		RsyncVersioningPreparing, RsyncVersioningVerifying, RsyncVersioningCommitted,
		RsyncVersioningFailed, RsyncVersioningBlocked, RsyncVersioningRollbackPrepared:
		return true
	default:
		return false
	}
}

func validRsyncVersioningReason(value RsyncVersioningReasonCode) bool {
	switch value {
	case RsyncVersioningReasonLegacy, RsyncVersioningReasonPreflightRequired,
		RsyncVersioningReasonReady, RsyncVersioningReasonPreflightExpired,
		RsyncVersioningReasonTaskRevisionChanged, RsyncVersioningReasonPreflightMismatch,
		RsyncVersioningReasonRootDrift, RsyncVersioningReasonUnsupported,
		RsyncVersioningReasonAdmissionBlocked, RsyncVersioningReasonRollbackPrepared:
		return true
	default:
		return false
	}
}
