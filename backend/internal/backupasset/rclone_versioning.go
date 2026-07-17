package backupasset

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const RcloneTaskConfigSchemaVersion = 1

type RcloneVersioningMigrationChoice string

const (
	RcloneImportedBaseline RcloneVersioningMigrationChoice = "imported_baseline"
	RcloneFirstNewPoint    RcloneVersioningMigrationChoice = "first_new_point"
)

type RcloneVersioningState string

const (
	RcloneStateLegacy                  RcloneVersioningState = "legacy"
	RcloneStatePreflightRequired       RcloneVersioningState = "preflight_required"
	RcloneStateCredentialSetupRequired RcloneVersioningState = "credential_setup_required"
	RcloneStateCapabilitySettling      RcloneVersioningState = "capability_settling"
	RcloneStateReady                   RcloneVersioningState = "ready"
	RcloneStatePreparing               RcloneVersioningState = "preparing"
	RcloneStateVerifying               RcloneVersioningState = "verifying"
	RcloneStateCommitted               RcloneVersioningState = "committed"
	RcloneStateDegraded                RcloneVersioningState = "degraded"
	RcloneStateAtRisk                  RcloneVersioningState = "at_risk"
	RcloneStateFailed                  RcloneVersioningState = "failed"
	RcloneStateBlocked                 RcloneVersioningState = "blocked"
	RcloneStateRollbackPrepared        RcloneVersioningState = "rollback_prepared"
)

type RcloneEncryptionProfile string

const (
	RcloneEncryptionNone   RcloneEncryptionProfile = "none"
	RcloneEncryptionSSES3  RcloneEncryptionProfile = "sse_s3"
	RcloneEncryptionSSEKMS RcloneEncryptionProfile = "sse_kms_cmk"
)

type RcloneKMSKeyStatus string

const (
	RcloneKMSNotApplicable RcloneKMSKeyStatus = "not_applicable"
	RcloneKMSReady         RcloneKMSKeyStatus = "ready"
	RcloneKMSDegraded      RcloneKMSKeyStatus = "degraded"
	RcloneKMSAtRisk        RcloneKMSKeyStatus = "at_risk"
	RcloneKMSBlocked       RcloneKMSKeyStatus = "blocked"
)

type RcloneRollbackCapability string

const (
	RcloneRollbackCleanAvailable  RcloneRollbackCapability = "clean_available"
	RcloneRollbackPreparationOnly RcloneRollbackCapability = "preparation_only"
	RcloneRollbackPrepared        RcloneRollbackCapability = "prepared"
)

type RcloneVersioningReasonCode string

const (
	RcloneReasonLegacy                  RcloneVersioningReasonCode = "legacy"
	RcloneReasonPreflightRequired       RcloneVersioningReasonCode = "preflight_required"
	RcloneReasonReady                   RcloneVersioningReasonCode = "ready"
	RcloneReasonCredentialSetupRequired RcloneVersioningReasonCode = "credential_setup_required"
	RcloneReasonCapabilitySettling      RcloneVersioningReasonCode = "capability_settling"
	RcloneReasonPreflightExpired        RcloneVersioningReasonCode = "preflight_expired"
	RcloneReasonTaskRevisionChanged     RcloneVersioningReasonCode = "task_revision_changed"
	RcloneReasonBindingRevisionChanged  RcloneVersioningReasonCode = "binding_revision_changed"
	RcloneReasonPreflightMismatch       RcloneVersioningReasonCode = "preflight_mismatch"
	RcloneReasonFeatureDisabled         RcloneVersioningReasonCode = "feature_disabled"
	RcloneReasonUnsupportedProfile      RcloneVersioningReasonCode = "unsupported_profile"
	RcloneReasonRepositoryOffline       RcloneVersioningReasonCode = "repository_offline"
	RcloneReasonProviderUnavailable     RcloneVersioningReasonCode = "provider_unavailable"
	RcloneReasonProviderTimeout         RcloneVersioningReasonCode = "provider_timeout"
	RcloneReasonProviderResourceLimit   RcloneVersioningReasonCode = "provider_resource_limit"
	RcloneReasonSessionTooShort         RcloneVersioningReasonCode = "session_too_short"
	RcloneReasonVersioningDisabled      RcloneVersioningReasonCode = "versioning_disabled"
	RcloneReasonLifecycleConflict       RcloneVersioningReasonCode = "lifecycle_conflict"
	RcloneReasonEncryptionUnsupported   RcloneVersioningReasonCode = "encryption_unsupported"
	RcloneReasonKMSKeyUnavailable       RcloneVersioningReasonCode = "kms_key_unavailable"
	RcloneReasonKMSPermissionDenied     RcloneVersioningReasonCode = "kms_permission_denied"
	RcloneReasonKMSKeyRingLimit         RcloneVersioningReasonCode = "kms_key_ring_limit"
	RcloneReasonIdentityMismatch        RcloneVersioningReasonCode = "identity_mismatch"
	RcloneReasonCredentialInvalid       RcloneVersioningReasonCode = "credential_invalid"
	RcloneReasonVerificationCostLimit   RcloneVersioningReasonCode = "verification_cost_limit"
	RcloneReasonSourceDrift             RcloneVersioningReasonCode = "source_drift"
	RcloneReasonExternalWriterDetected  RcloneVersioningReasonCode = "external_writer_detected"
	RcloneReasonUnexpectedVersion       RcloneVersioningReasonCode = "unexpected_version"
	RcloneReasonManifestMismatch        RcloneVersioningReasonCode = "manifest_mismatch"
	RcloneReasonMarkerMismatch          RcloneVersioningReasonCode = "marker_mismatch"
	RcloneReasonAdmissionBlocked        RcloneVersioningReasonCode = "admission_blocked"
	RcloneReasonOutcomeUnknown          RcloneVersioningReasonCode = "outcome_unknown"
	RcloneReasonRollbackPrepared        RcloneVersioningReasonCode = "rollback_prepared"
)

type RcloneConsistencyClass string

const (
	RcloneConsistencyNotEvaluated          RcloneConsistencyClass = "not_evaluated"
	RcloneConsistencyObservationallyStable RcloneConsistencyClass = "observationally_stable"
	RcloneConsistencyProviderStrong        RcloneConsistencyClass = "provider_strong"
)

type RcloneHashFidelity string

const (
	RcloneHashNotEvaluated           RcloneHashFidelity = "not_evaluated"
	RcloneHashProviderStrongChecksum RcloneHashFidelity = "provider_strong_checksum"
	RcloneHashDownloadVerifiedBytes  RcloneHashFidelity = "download_verified_bytes"
)

type RcloneCostClass string

const (
	RcloneCostNotEvaluated RcloneCostClass = "not_evaluated"
	RcloneCostNone         RcloneCostClass = "none"
	RcloneCostLow          RcloneCostClass = "low"
	RcloneCostModerate     RcloneCostClass = "moderate"
	RcloneCostHigh         RcloneCostClass = "high"
)

type RclonePublicationSummary struct {
	Mode                   TaskPublicationMode        `json:"mode"`
	State                  RcloneVersioningState      `json:"state"`
	ReasonCode             RcloneVersioningReasonCode `json:"reason_code"`
	TaskRevision           string                     `json:"task_revision"`
	BindingRevision        string                     `json:"binding_revision"`
	CapabilityRevision     string                     `json:"capability_revision"`
	ConsistencyClass       RcloneConsistencyClass     `json:"consistency_class"`
	HashFidelity           RcloneHashFidelity         `json:"hash_fidelity"`
	EstimatedReadBytes     string                     `json:"estimated_read_bytes"`
	APICostClass           RcloneCostClass            `json:"api_cost_class"`
	StorageCostClass       RcloneCostClass            `json:"storage_cost_class"`
	EgressCostClass        RcloneCostClass            `json:"egress_cost_class"`
	CredentialExpiresAt    *time.Time                 `json:"credential_expires_at,omitempty"`
	EncryptionProfile      RcloneEncryptionProfile    `json:"encryption_profile"`
	KMSKeyStatus           RcloneKMSKeyStatus         `json:"kms_key_status"`
	KMSReadKeyCount        uint32                     `json:"kms_read_key_count"`
	RollbackLocatorPresent bool                       `json:"rollback_locator_present"`
	RollbackCapability     RcloneRollbackCapability   `json:"rollback_capability"`
}

var (
	validRclonePublicationModes = setOf(PublicationLegacyMutable, PublicationVersionedPrefix, PublicationNativeObjectVersions)
	validRcloneStates           = setOf(
		RcloneStateLegacy, RcloneStatePreflightRequired, RcloneStateCredentialSetupRequired,
		RcloneStateCapabilitySettling, RcloneStateReady, RcloneStatePreparing, RcloneStateVerifying,
		RcloneStateCommitted, RcloneStateDegraded, RcloneStateAtRisk, RcloneStateFailed,
		RcloneStateBlocked, RcloneStateRollbackPrepared,
	)
	validRcloneReasons = setOf(
		RcloneReasonLegacy, RcloneReasonPreflightRequired, RcloneReasonReady, RcloneReasonCredentialSetupRequired,
		RcloneReasonCapabilitySettling, RcloneReasonPreflightExpired, RcloneReasonTaskRevisionChanged,
		RcloneReasonBindingRevisionChanged, RcloneReasonPreflightMismatch, RcloneReasonFeatureDisabled,
		RcloneReasonUnsupportedProfile, RcloneReasonRepositoryOffline, RcloneReasonProviderUnavailable,
		RcloneReasonProviderTimeout, RcloneReasonProviderResourceLimit, RcloneReasonSessionTooShort,
		RcloneReasonVersioningDisabled, RcloneReasonLifecycleConflict, RcloneReasonEncryptionUnsupported,
		RcloneReasonKMSKeyUnavailable, RcloneReasonKMSPermissionDenied, RcloneReasonKMSKeyRingLimit,
		RcloneReasonIdentityMismatch, RcloneReasonCredentialInvalid, RcloneReasonVerificationCostLimit,
		RcloneReasonSourceDrift, RcloneReasonExternalWriterDetected, RcloneReasonUnexpectedVersion,
		RcloneReasonManifestMismatch, RcloneReasonMarkerMismatch, RcloneReasonAdmissionBlocked,
		RcloneReasonOutcomeUnknown, RcloneReasonRollbackPrepared,
	)
	validRcloneConsistency = setOf(RcloneConsistencyNotEvaluated, RcloneConsistencyObservationallyStable, RcloneConsistencyProviderStrong)
	validRcloneHash        = setOf(RcloneHashNotEvaluated, RcloneHashProviderStrongChecksum, RcloneHashDownloadVerifiedBytes)
	validRcloneCosts       = setOf(RcloneCostNotEvaluated, RcloneCostNone, RcloneCostLow, RcloneCostModerate, RcloneCostHigh)
	validRcloneEncryption  = setOf(RcloneEncryptionNone, RcloneEncryptionSSES3, RcloneEncryptionSSEKMS)
	validRcloneKMSStatus   = setOf(RcloneKMSNotApplicable, RcloneKMSReady, RcloneKMSDegraded, RcloneKMSAtRisk, RcloneKMSBlocked)
	validRcloneRollback    = setOf(RcloneRollbackCleanAvailable, RcloneRollbackPreparationOnly, RcloneRollbackPrepared)
)

func (value RclonePublicationSummary) Validate() error {
	if !validRclonePublicationModes[value.Mode] || !validRcloneStates[value.State] || !validRcloneReasons[value.ReasonCode] ||
		!validRcloneConsistency[value.ConsistencyClass] || !validRcloneHash[value.HashFidelity] ||
		!validRcloneCosts[value.APICostClass] || !validRcloneCosts[value.StorageCostClass] || !validRcloneCosts[value.EgressCostClass] ||
		!validRcloneEncryption[value.EncryptionProfile] || !validRcloneKMSStatus[value.KMSKeyStatus] || !validRcloneRollback[value.RollbackCapability] ||
		!canonicalUnsignedDecimal(value.TaskRevision) || !canonicalUnsignedDecimal(value.BindingRevision) ||
		!canonicalUnsignedDecimal(value.CapabilityRevision) || !canonicalUnsignedDecimal(value.EstimatedReadBytes) ||
		!validRcloneEncryptionAndKMS(value) {
		return fmt.Errorf("%w: invalid Rclone publication summary", ErrInvalidState)
	}
	if value.CredentialExpiresAt != nil && (!isUTCPublicationTime(*value.CredentialExpiresAt)) {
		return fmt.Errorf("%w: invalid Rclone credential expiry", ErrInvalidState)
	}
	return nil
}

func validRcloneEncryptionAndKMS(value RclonePublicationSummary) bool {
	switch value.Mode {
	case PublicationLegacyMutable, PublicationVersionedPrefix:
		return value.EncryptionProfile == RcloneEncryptionNone && value.KMSKeyStatus == RcloneKMSNotApplicable && value.KMSReadKeyCount == 0
	case PublicationNativeObjectVersions:
		switch value.EncryptionProfile {
		case RcloneEncryptionSSES3:
			return value.KMSKeyStatus == RcloneKMSNotApplicable && value.KMSReadKeyCount == 0
		case RcloneEncryptionSSEKMS:
			return value.KMSKeyStatus != RcloneKMSNotApplicable
		default:
			return false
		}
	default:
		return false
	}
}

func SafeRclonePublicationSummary(value RclonePublicationSummary) RclonePublicationSummary {
	if value.Validate() == nil {
		return value
	}
	return RclonePublicationSummary{
		Mode: PublicationNativeObjectVersions, State: RcloneStateBlocked, ReasonCode: RcloneReasonUnsupportedProfile,
		TaskRevision: safeCanonicalDecimal(value.TaskRevision), BindingRevision: safeCanonicalDecimal(value.BindingRevision),
		CapabilityRevision: safeCanonicalDecimal(value.CapabilityRevision), EstimatedReadBytes: safeCanonicalDecimal(value.EstimatedReadBytes),
		ConsistencyClass: RcloneConsistencyNotEvaluated, HashFidelity: RcloneHashNotEvaluated,
		APICostClass: RcloneCostHigh, StorageCostClass: RcloneCostHigh, EgressCostClass: RcloneCostHigh,
		EncryptionProfile: RcloneEncryptionSSEKMS, KMSKeyStatus: RcloneKMSBlocked,
		RollbackLocatorPresent: false, RollbackCapability: RcloneRollbackPreparationOnly,
	}
}

func canonicalUnsignedDecimal(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || value[0] == '0' {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func safeCanonicalDecimal(value string) string {
	if canonicalUnsignedDecimal(value) {
		return value
	}
	return "0"
}

type RcloneVersioningPreflightRequest struct {
	TaskID               uint                `json:"-"`
	ExpectedTaskRevision uint64              `json:"expected_task_revision"`
	RequestedMode        TaskPublicationMode `json:"requested_mode"`
}

func (value RcloneVersioningPreflightRequest) Validate() error {
	if value.TaskID == 0 || value.ExpectedTaskRevision == 0 ||
		(value.RequestedMode != PublicationVersionedPrefix && value.RequestedMode != PublicationNativeObjectVersions) {
		return fmt.Errorf("%w: invalid Rclone versioning preflight request", ErrInvalidState)
	}
	return nil
}

type RcloneVersioningActivationRequest struct {
	TaskID               uint                            `json:"-"`
	ExpectedTaskRevision uint64                          `json:"expected_task_revision"`
	PreflightID          string                          `json:"preflight_id"`
	MigrationChoice      RcloneVersioningMigrationChoice `json:"migration_choice"`
}

func (value RcloneVersioningActivationRequest) Validate() error {
	if value.TaskID == 0 || value.ExpectedTaskRevision == 0 || ValidateOpaqueID(value.PreflightID) != nil ||
		(value.MigrationChoice != RcloneImportedBaseline && value.MigrationChoice != RcloneFirstNewPoint) {
		return fmt.Errorf("%w: invalid Rclone versioning activation request", ErrInvalidState)
	}
	return nil
}

// RcloneBindingSetupRequest creates one short-lived, Task-revision-bound
// write-only binding setup. Provider identities and credentials are supplied
// only when the setup is consumed.
type RcloneBindingSetupRequest struct {
	TaskID               uint   `json:"-"`
	ExpectedTaskRevision uint64 `json:"expected_task_revision"`
}

func (value RcloneBindingSetupRequest) Validate() error {
	if value.TaskID == 0 || value.ExpectedTaskRevision == 0 {
		return fmt.Errorf("%w: invalid Rclone binding setup request", ErrInvalidState)
	}
	return nil
}

type RcloneBindingSetupResult struct {
	SetupID   string    `json:"setup_id"`
	ExpiresAt time.Time `json:"expires_at"`
	// ExternalID is returned only by the native setup endpoint. It is not
	// included in ordinary Task or Repository projections.
	ExternalID string `json:"external_id,omitempty"`
}

func (value RcloneBindingSetupResult) Validate(native bool) error {
	if ValidateOpaqueID(value.SetupID) != nil || !isUTCPublicationTime(value.ExpiresAt) ||
		(native && !validRclonePrivateInput(value.ExternalID, 1024, true)) || (!native && value.ExternalID != "") {
		return fmt.Errorf("%w: invalid Rclone binding setup result", ErrInvalidState)
	}
	return nil
}

// RclonePortableBindingRequest is a write-only service input. The private
// target, root and exact config bytes must never be serialized in a response,
// log, audit record or generic Task DTO.
type RclonePortableBindingRequest struct {
	TaskID                  uint   `json:"-"`
	ExpectedTaskRevision    uint64 `json:"expected_task_revision"`
	ExpectedBindingRevision uint64 `json:"expected_binding_revision"`
	SetupID                 string `json:"setup_id"`
	TargetRemote            string `json:"-"`
	ManagedRootLocator      string `json:"-"`
	BoundConfig             string `json:"-"`
}

func (value RclonePortableBindingRequest) Validate() error {
	if value.TaskID == 0 || value.ExpectedTaskRevision == 0 || ValidateOpaqueID(value.SetupID) != nil ||
		!validRclonePrivateInput(value.TargetRemote, 256, true) ||
		!validRclonePrivateInput(value.ManagedRootLocator, 16<<10, true) ||
		!validRclonePrivateInput(value.BoundConfig, 64<<10, false) {
		return fmt.Errorf("%w: invalid portable Rclone binding request", ErrInvalidState)
	}
	return nil
}

type RcloneNativeBootstrapMode string

const (
	RcloneBootstrapWorkloadChain RcloneNativeBootstrapMode = "workload_chain"
	RcloneBootstrapStaticSTS     RcloneNativeBootstrapMode = "static_sts_bootstrap"
)

// RcloneNativeBootstrapInput is intentionally write-only. Workload mode has
// no static fields; static mode requires both values and is still restricted
// to STS AssumeRole by the provider admission contract.
type RcloneNativeBootstrapInput struct {
	Mode            RcloneNativeBootstrapMode `json:"mode"`
	AccessKeyID     string                    `json:"-"`
	SecretAccessKey string                    `json:"-"`
}

func (value RcloneNativeBootstrapInput) Validate() error {
	switch value.Mode {
	case RcloneBootstrapWorkloadChain:
		if value.AccessKeyID != "" || value.SecretAccessKey != "" {
			return fmt.Errorf("%w: workload Rclone bootstrap cannot contain static credentials", ErrInvalidState)
		}
	case RcloneBootstrapStaticSTS:
		if !validRclonePrivateInput(value.AccessKeyID, 256, true) ||
			!validRclonePrivateInput(value.SecretAccessKey, 4096, true) {
			return fmt.Errorf("%w: invalid static STS Rclone bootstrap", ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: unsupported Rclone bootstrap mode", ErrInvalidState)
	}
	return nil
}

// RcloneNativeBindingRequest contains only write-only provider identity and
// bootstrap input. External ID is taken from the one-time setup record rather
// than accepted from the caller.
type RcloneNativeBindingRequest struct {
	TaskID                  uint                       `json:"-"`
	ExpectedTaskRevision    uint64                     `json:"expected_task_revision"`
	ExpectedBindingRevision uint64                     `json:"expected_binding_revision"`
	SetupID                 string                     `json:"setup_id"`
	Region                  string                     `json:"-"`
	Bucket                  string                     `json:"-"`
	ManagedPrefix           string                     `json:"-"`
	RoleARN                 string                     `json:"-"`
	Bootstrap               RcloneNativeBootstrapInput `json:"-"`
	EncryptionProfile       RcloneEncryptionProfile    `json:"encryption_profile"`
	KMSKeyARN               string                     `json:"-"`
}

func (value RcloneNativeBindingRequest) Validate() error {
	if value.TaskID == 0 || value.ExpectedTaskRevision == 0 || ValidateOpaqueID(value.SetupID) != nil ||
		!validRclonePrivateInput(value.Region, 256, true) || !validRclonePrivateInput(value.Bucket, 1024, true) ||
		!validRclonePrivateInput(value.ManagedPrefix, 16<<10, true) || !validRclonePrivateInput(value.RoleARN, 2048, true) ||
		value.Bootstrap.Validate() != nil {
		return fmt.Errorf("%w: invalid native Rclone binding request", ErrInvalidState)
	}
	switch value.EncryptionProfile {
	case RcloneEncryptionSSES3:
		if value.KMSKeyARN != "" {
			return fmt.Errorf("%w: SSE-S3 Rclone binding cannot contain a KMS key", ErrInvalidState)
		}
	case RcloneEncryptionSSEKMS:
		if !validRclonePrivateInput(value.KMSKeyARN, 2048, true) {
			return fmt.Errorf("%w: SSE-KMS Rclone binding requires a key ARN", ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: unsupported native Rclone encryption profile", ErrInvalidState)
	}
	return nil
}

type RcloneVersioningPreflightResult struct {
	PreflightID string                   `json:"preflight_id"`
	ExpiresAt   time.Time                `json:"expires_at"`
	Summary     RclonePublicationSummary `json:"summary"`
}

func (value RcloneVersioningPreflightResult) Validate() error {
	if ValidateOpaqueID(value.PreflightID) != nil || !isUTCPublicationTime(value.ExpiresAt) || value.Summary.Validate() != nil {
		return fmt.Errorf("%w: invalid Rclone versioning preflight result", ErrInvalidState)
	}
	return nil
}

type RcloneVersioningActivationResult struct {
	Summary         RclonePublicationSummary        `json:"summary"`
	MigrationChoice RcloneVersioningMigrationChoice `json:"migration_choice"`
}

func (value RcloneVersioningActivationResult) Validate() error {
	if value.Summary.Validate() != nil ||
		(value.MigrationChoice != RcloneImportedBaseline && value.MigrationChoice != RcloneFirstNewPoint) {
		return fmt.Errorf("%w: invalid Rclone versioning activation result", ErrInvalidState)
	}
	return nil
}

type RcloneVersioningCleanRollbackRequest struct {
	TaskID                  uint   `json:"-"`
	ExpectedTaskRevision    uint64 `json:"expected_task_revision"`
	ExpectedBindingRevision uint64 `json:"expected_binding_revision"`
}

func (value RcloneVersioningCleanRollbackRequest) Validate() error {
	if value.TaskID == 0 || value.ExpectedTaskRevision == 0 || value.ExpectedBindingRevision == 0 {
		return fmt.Errorf("%w: invalid Rclone clean rollback request", ErrInvalidState)
	}
	return nil
}

type RcloneVersioningRollbackPreparationRequest struct {
	TaskID                  uint   `json:"-"`
	ExpectedTaskRevision    uint64 `json:"expected_task_revision"`
	ExpectedBindingRevision uint64 `json:"expected_binding_revision"`
}

func (value RcloneVersioningRollbackPreparationRequest) Validate() error {
	if value.TaskID == 0 || value.ExpectedTaskRevision == 0 || value.ExpectedBindingRevision == 0 {
		return fmt.Errorf("%w: invalid Rclone rollback preparation request", ErrInvalidState)
	}
	return nil
}

type RcloneVersioningRollbackResult struct {
	Summary RclonePublicationSummary `json:"summary"`
}

func (value RcloneVersioningRollbackResult) Validate() error {
	if value.Summary.Validate() != nil {
		return fmt.Errorf("%w: invalid Rclone rollback result", ErrInvalidState)
	}
	return nil
}

func validRclonePrivateInput(value string, maximum int, trim bool) bool {
	if value == "" || maximum <= 0 || len(value) > maximum || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return false
	}
	return !trim || strings.TrimSpace(value) == value
}
