package backupasset

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type ProviderCompletionClass string

const (
	CompletionKnownExitZero  ProviderCompletionClass = "known_exit_zero"
	CompletionKnownNonzero   ProviderCompletionClass = "known_nonzero"
	CompletionOutcomeUnknown ProviderCompletionClass = "outcome_unknown"
)

type PublicationFailureCode string

const (
	FailurePublicationPreconditionMissing PublicationFailureCode = "publication_precondition_missing"
	FailurePublicationInProgress          PublicationFailureCode = "publication_in_progress"
	FailurePublicationSessionAbandoned    PublicationFailureCode = "publication_session_abandoned"
	FailureEvidenceMissingSummary         PublicationFailureCode = "evidence_missing_summary"
	FailureEvidenceMalformedStream        PublicationFailureCode = "evidence_malformed_stream"
	FailureEvidenceDuplicateSummary       PublicationFailureCode = "evidence_duplicate_summary"
	FailureEvidenceNonFinalSummary        PublicationFailureCode = "evidence_non_final_summary"
	FailureEvidenceInvalidNativeID        PublicationFailureCode = "evidence_invalid_native_id"
	FailureProviderNonzeroExit            PublicationFailureCode = "provider_nonzero_exit"
	FailureProviderTimeout                PublicationFailureCode = "provider_timeout"
	FailureProviderCanceled               PublicationFailureCode = "provider_canceled"
	FailureProviderResourceLimit          PublicationFailureCode = "provider_resource_limit"
	FailureProviderOutcomeUnknown         PublicationFailureCode = "provider_outcome_unknown"
	FailureProviderCompletionUnproven     PublicationFailureCode = "provider_completion_unproven"
	FailureProviderSnapshotRewritten      PublicationFailureCode = "provider_snapshot_rewritten"
	FailureRepositoryIdentityDrift        PublicationFailureCode = "repository_identity_drift"
	FailureRunTagMissing                  PublicationFailureCode = "run_tag_missing"
	FailureAmbiguousRunTags               PublicationFailureCode = "ambiguous_run_tags"
	FailureNativePointConflict            PublicationFailureCode = "native_point_conflict"
	FailureManifestPartial                PublicationFailureCode = "manifest_partial"
	FailureManifestUnavailable            PublicationFailureCode = "manifest_unavailable"
	FailureLeaseFenceLost                 PublicationFailureCode = "lease_fence_lost"
	FailurePublicationDeadlineExceeded    PublicationFailureCode = "publication_deadline_exceeded"
	FailureSnapshotMissingAtDeadline      PublicationFailureCode = "snapshot_missing_at_deadline"
	FailureLegacyFallbackBlocked          PublicationFailureCode = "legacy_fallback_blocked"
	FailureLegacyOperationBlocked         PublicationFailureCode = "legacy_operation_blocked"
)

type PublicationOutcomeCode string

const PublicationOutcomeSuccess PublicationOutcomeCode = "success"

type PublicationLineageV1 struct {
	Version              int       `json:"version"`
	TaskRepositoryLinkID string    `json:"task_repository_link_id"`
	TaskID               uint      `json:"task_id"`
	TaskRunID            uint      `json:"task_run_id"`
	Trigger              string    `json:"trigger"`
	ChainRunIDPresent    bool      `json:"chain_run_id_present"`
	ChainRunIDDigest     string    `json:"chain_run_id_digest,omitempty"`
	PublicationMode      string    `json:"publication_mode"`
	PointCodecVersion    int       `json:"point_codec_version"`
	TagCodecVersion      int       `json:"tag_codec_version"`
	StartedAt            time.Time `json:"started_at"`
	PreparedAt           time.Time `json:"prepared_at"`
	PointDeadlineAt      time.Time `json:"point_deadline_at"`
}

type PublicationConsistencyV1 struct {
	Version                     int                     `json:"version"`
	PublicationRevision         uint64                  `json:"publication_revision"`
	AttemptCount                uint64                  `json:"attempt_count"`
	Completion                  ProviderCompletionClass `json:"completion,omitempty"`
	Code                        PublicationFailureCode  `json:"code,omitempty"`
	CaptureStartedAt            *time.Time              `json:"capture_started_at,omitempty"`
	CaptureFinishedAt           *time.Time              `json:"capture_finished_at,omitempty"`
	FilesProcessed              uint64                  `json:"files_processed,omitempty"`
	LogicalBytes                uint64                  `json:"logical_bytes,omitempty"`
	Provider                    ProviderKind            `json:"provider,omitempty"`
	RepositoryIdentityDigest    string                  `json:"repository_identity_digest,omitempty"`
	RequestedTagDigest          string                  `json:"requested_tag_digest,omitempty"`
	ProviderCommitDigest        string                  `json:"provider_commit_digest,omitempty"`
	AdapterRevision             string                  `json:"adapter_revision,omitempty"`
	CapabilityRevision          int                     `json:"capability_revision,omitempty"`
	FirstMissingObservedAt      *time.Time              `json:"first_missing_observed_at,omitempty"`
	MissingGraceReportedAt      *time.Time              `json:"missing_grace_reported_at,omitempty"`
	LastAttemptAt               *time.Time              `json:"last_attempt_at,omitempty"`
	QuarantineObservationDigest string                  `json:"quarantine_observation_digest,omitempty"`
}

type PublicationConfig struct {
	ReconcileInterval      time.Duration
	ReconcileBatchSize     int
	WorkerConcurrency      int
	MissingGrace           time.Duration
	BackupStreamMaxBytes   int64
	ManifestTimeout        time.Duration
	ManifestMaxBytes       int64
	ManifestMaxEntries     int64
	ManifestMaxRecordBytes int
	ManifestMaxDepth       int
}

type PublicationAuditContext struct {
	Actor         AuditActor
	CorrelationID string
}

var validProviderCompletionClasses = setOf(
	CompletionKnownExitZero,
	CompletionKnownNonzero,
	CompletionOutcomeUnknown,
)

var validPublicationFailureCodes = setOf(
	FailurePublicationPreconditionMissing,
	FailurePublicationInProgress,
	FailurePublicationSessionAbandoned,
	FailureEvidenceMissingSummary,
	FailureEvidenceMalformedStream,
	FailureEvidenceDuplicateSummary,
	FailureEvidenceNonFinalSummary,
	FailureEvidenceInvalidNativeID,
	FailureProviderNonzeroExit,
	FailureProviderTimeout,
	FailureProviderCanceled,
	FailureProviderResourceLimit,
	FailureProviderOutcomeUnknown,
	FailureProviderCompletionUnproven,
	FailureProviderSnapshotRewritten,
	FailureRepositoryIdentityDrift,
	FailureRunTagMissing,
	FailureAmbiguousRunTags,
	FailureNativePointConflict,
	FailureManifestPartial,
	FailureManifestUnavailable,
	FailureLeaseFenceLost,
	FailurePublicationDeadlineExceeded,
	FailureSnapshotMissingAtDeadline,
	FailureLegacyFallbackBlocked,
	FailureLegacyOperationBlocked,
)

var knownExitZeroEvidenceFailureCodes = setOf(
	FailureEvidenceMissingSummary,
	FailureEvidenceMalformedStream,
	FailureEvidenceDuplicateSummary,
	FailureEvidenceNonFinalSummary,
	FailureEvidenceInvalidNativeID,
)

var unknownOutcomeDeferralCodes = setOf(
	FailureProviderTimeout,
	FailureProviderCanceled,
	FailureProviderResourceLimit,
	FailureProviderOutcomeUnknown,
	FailurePublicationSessionAbandoned,
)

func ValidateProviderCompletionClass(value ProviderCompletionClass) error {
	if !validProviderCompletionClasses[value] {
		return fmt.Errorf("%w: unknown provider completion class", ErrInvalidState)
	}
	return nil
}

func ValidatePublicationFailureCode(value PublicationFailureCode) error {
	if !validPublicationFailureCodes[value] {
		return fmt.Errorf("%w: unknown publication failure code", ErrInvalidState)
	}
	return nil
}

func ValidatePublicationOutcomeCode(value PublicationOutcomeCode) error {
	if value == PublicationOutcomeSuccess {
		return nil
	}
	return ValidatePublicationFailureCode(PublicationFailureCode(value))
}

func PublicationOutcomeFromFailure(code PublicationFailureCode) (PublicationOutcomeCode, error) {
	if code == "" {
		return PublicationOutcomeSuccess, nil
	}
	if err := ValidatePublicationFailureCode(code); err != nil {
		return "", err
	}
	return PublicationOutcomeCode(code), nil
}

func ValidatePublicationDeferral(completion ProviderCompletionClass, code PublicationFailureCode) error {
	if err := ValidateProviderCompletionClass(completion); err != nil {
		return err
	}
	if err := ValidatePublicationFailureCode(code); err != nil {
		return err
	}
	switch completion {
	case CompletionKnownExitZero:
		if knownExitZeroEvidenceFailureCodes[code] {
			return nil
		}
	case CompletionOutcomeUnknown:
		if unknownOutcomeDeferralCodes[code] {
			return nil
		}
	}
	return fmt.Errorf("%w: invalid publication deferral pairing", ErrInvalidState)
}

func ValidatePublicationAuditContext(value PublicationAuditContext) error {
	if !validPublicationCorrelationID(value.CorrelationID) {
		return fmt.Errorf("%w: invalid publication correlation ID", ErrInvalidState)
	}
	actor := value.Actor
	if actor.UserID == 0 {
		if actor.Username == "system" && actor.Role == "system" {
			return nil
		}
		return fmt.Errorf("%w: invalid system publication actor", ErrInvalidState)
	}
	if actor.Username == "system" || actor.Role == "system" {
		return fmt.Errorf("%w: reserved system publication actor", ErrInvalidState)
	}
	if safeAuditLabel(actor.Username, 64) != actor.Username || safeAuditLabel(actor.Role, 32) != actor.Role {
		return fmt.Errorf("%w: invalid publication actor", ErrInvalidState)
	}
	return nil
}

func NewSystemPublicationAuditContext(correlationID string) (PublicationAuditContext, error) {
	context := PublicationAuditContext{
		Actor:         AuditActor{Username: "system", Role: "system"},
		CorrelationID: correlationID,
	}
	if err := ValidatePublicationAuditContext(context); err != nil {
		return PublicationAuditContext{}, err
	}
	return context, nil
}

func EncodePublicationLineage(value PublicationLineageV1) (string, error) {
	if err := validatePublicationLineage(value); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode publication lineage: %w", err)
	}
	return string(encoded), nil
}

func DecodePublicationLineage(raw string) (PublicationLineageV1, error) {
	var value PublicationLineageV1
	if err := decodeStrictPublicationJSON(raw, &value); err != nil {
		return PublicationLineageV1{}, err
	}
	if err := validatePublicationLineage(value); err != nil {
		return PublicationLineageV1{}, err
	}
	return value, nil
}

func EncodePublicationConsistency(value PublicationConsistencyV1) (string, error) {
	if err := validatePublicationConsistency(value); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode publication consistency: %w", err)
	}
	return string(encoded), nil
}

func DecodePublicationConsistency(raw string) (PublicationConsistencyV1, error) {
	var value PublicationConsistencyV1
	if err := decodeStrictPublicationJSON(raw, &value); err != nil {
		return PublicationConsistencyV1{}, err
	}
	if err := validatePublicationConsistency(value); err != nil {
		return PublicationConsistencyV1{}, err
	}
	return value, nil
}

func decodeStrictPublicationJSON(raw string, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: invalid publication JSON", ErrInvalidState)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: publication JSON has trailing data", ErrInvalidState)
	}
	return nil
}

func validatePublicationLineage(value PublicationLineageV1) error {
	if value.Version != 1 || ValidateOpaqueID(value.TaskRepositoryLinkID) != nil || value.TaskID == 0 || value.TaskRunID == 0 ||
		value.PointCodecVersion != 1 || safeAuditLabel(value.Trigger, 64) != value.Trigger {
		return fmt.Errorf("%w: invalid publication lineage", ErrInvalidState)
	}
	switch TaskPublicationMode(value.PublicationMode) {
	case PublicationNativeSnapshot:
		if value.TagCodecVersion != 1 {
			return fmt.Errorf("%w: invalid native publication tag codec", ErrInvalidState)
		}
	case PublicationVersionedHardlink, PublicationVersionedFullCopy:
		// Managed trees do not use provider tags. Keeping this zero makes the
		// persisted lineage explicit instead of implying Restic tag semantics.
		if value.TagCodecVersion != 0 {
			return fmt.Errorf("%w: invalid managed-tree publication tag codec", ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: invalid publication lineage mode", ErrInvalidState)
	}
	if value.ChainRunIDPresent {
		if !isLowerHex(value.ChainRunIDDigest, 64) {
			return fmt.Errorf("%w: invalid publication chain digest", ErrInvalidState)
		}
	} else if value.ChainRunIDDigest != "" {
		return fmt.Errorf("%w: unexpected publication chain digest", ErrInvalidState)
	}
	if !isUTCPublicationTime(value.StartedAt) || !isUTCPublicationTime(value.PreparedAt) || !isUTCPublicationTime(value.PointDeadlineAt) ||
		value.PreparedAt.Before(value.StartedAt) || !value.PointDeadlineAt.After(value.PreparedAt) {
		return fmt.Errorf("%w: invalid publication lineage timestamps", ErrInvalidState)
	}
	return nil
}

func validatePublicationConsistency(value PublicationConsistencyV1) error {
	if value.Version != 1 || value.CapabilityRevision < 0 || !validOptionalPublicationDigest(value.RepositoryIdentityDigest) ||
		!validOptionalPublicationDigest(value.RequestedTagDigest) || !validOptionalPublicationDigest(value.ProviderCommitDigest) ||
		!validOptionalPublicationDigest(value.QuarantineObservationDigest) || safeAuditLabel(value.AdapterRevision, 64) != value.AdapterRevision {
		return fmt.Errorf("%w: invalid publication consistency", ErrInvalidState)
	}
	if value.Provider != "" && !validRepositoryProviderKinds[value.Provider] {
		return fmt.Errorf("%w: invalid publication provider", ErrInvalidState)
	}
	if value.Completion != "" {
		if value.Code == "" {
			return fmt.Errorf("%w: incomplete publication completion state", ErrInvalidState)
		}
		if err := ValidateProviderCompletionClass(value.Completion); err != nil {
			return err
		}
		if err := ValidatePublicationFailureCode(value.Code); err != nil {
			return err
		}
	} else if value.Code != "" && !terminalPublicationCodeWithoutCompletion(value.Code) {
		return fmt.Errorf("%w: publication code requires a completion class", ErrInvalidState)
	}
	if err := validatePublicationTimePair(value.CaptureStartedAt, value.CaptureFinishedAt); err != nil {
		return err
	}
	for _, timestamp := range []*time.Time{value.FirstMissingObservedAt, value.MissingGraceReportedAt, value.LastAttemptAt} {
		if timestamp != nil && !isUTCPublicationTime(*timestamp) {
			return fmt.Errorf("%w: non-UTC publication consistency timestamp", ErrInvalidState)
		}
	}
	return nil
}

func terminalPublicationCodeWithoutCompletion(code PublicationFailureCode) bool {
	switch code {
	case FailureProviderCompletionUnproven,
		FailureProviderSnapshotRewritten,
		FailureRepositoryIdentityDrift,
		FailureRunTagMissing,
		FailureAmbiguousRunTags,
		FailureNativePointConflict,
		FailureProviderResourceLimit,
		FailureManifestPartial,
		FailureManifestUnavailable,
		FailurePublicationDeadlineExceeded,
		FailureSnapshotMissingAtDeadline:
		return true
	default:
		return false
	}
}

func validatePublicationTimePair(startedAt, finishedAt *time.Time) error {
	if (startedAt == nil) != (finishedAt == nil) {
		return fmt.Errorf("%w: incomplete publication capture interval", ErrInvalidState)
	}
	if startedAt == nil {
		return nil
	}
	if !isUTCPublicationTime(*startedAt) || !isUTCPublicationTime(*finishedAt) || finishedAt.Before(*startedAt) {
		return fmt.Errorf("%w: invalid publication capture interval", ErrInvalidState)
	}
	return nil
}

func validOptionalPublicationDigest(value string) bool {
	return value == "" || isLowerHex(value, 64)
}

func isUTCPublicationTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func validPublicationCorrelationID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}
