package backupasset

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	opaqueIDBytes       = 16
	opaqueIDEncodedSize = opaqueIDBytes * 2
	entryIDEncodedSize  = 64
)

type RepositoryStatus string

const (
	RepositoryConnecting   RepositoryStatus = "connecting"
	RepositoryOnline       RepositoryStatus = "online"
	RepositoryDegraded     RepositoryStatus = "degraded"
	RepositoryOffline      RepositoryStatus = "offline"
	RepositoryDisconnected RepositoryStatus = "disconnected"
	RepositoryPurging      RepositoryStatus = "purging"
	RepositoryPurgeBlocked RepositoryStatus = "purge_blocked"
)

type ProviderKind string

const (
	ProviderRestic         ProviderKind = "restic"
	ProviderRsync          ProviderKind = "rsync"
	ProviderRclone         ProviderKind = "rclone"
	ProviderCommand        ProviderKind = "command"
	ProviderVerifiedImport ProviderKind = "verified_import"
)

type TaskPublicationMode string

const (
	PublicationLegacyMutable        TaskPublicationMode = "legacy_mutable"
	PublicationVersionedHardlink    TaskPublicationMode = "versioned_hardlink"
	PublicationVersionedFullCopy    TaskPublicationMode = "versioned_full_copy"
	PublicationVersionedPrefix      TaskPublicationMode = "versioned_prefix"
	PublicationNativeObjectVersions TaskPublicationMode = "native_object_versions"
	PublicationNativeSnapshot       TaskPublicationMode = "native_snapshot"
)

type VersionMode string

const (
	VersionNativeSnapshot       VersionMode = "native_snapshot"
	VersionHardlinkTree         VersionMode = "hardlink_tree"
	VersionFullCopyTree         VersionMode = "full_copy_tree"
	VersionVersionedPrefix      VersionMode = "versioned_prefix"
	VersionNativeObjectVersions VersionMode = "native_object_versions"
	VersionMutableHead          VersionMode = "mutable_head"
)

type PointVersionSemantics string

const (
	PointNativeSnapshot   PointVersionSemantics = "native_snapshot"
	PointXirangManifest   PointVersionSemantics = "xirang_manifest"
	PointImportedBaseline PointVersionSemantics = "imported_baseline"
	PointMutableHead      PointVersionSemantics = "mutable_head"
)

type RecoveryPointState string

const (
	RecoveryPointObserved     RecoveryPointState = "observed"
	RecoveryPointRetired      RecoveryPointState = "retired"
	RecoveryPointPreparing    RecoveryPointState = "preparing"
	RecoveryPointVerifying    RecoveryPointState = "verifying"
	RecoveryPointCommitted    RecoveryPointState = "committed"
	RecoveryPointDegraded     RecoveryPointState = "degraded"
	RecoveryPointExpiring     RecoveryPointState = "expiring"
	RecoveryPointExpired      RecoveryPointState = "expired"
	RecoveryPointFailed       RecoveryPointState = "failed"
	RecoveryPointPurgeBlocked RecoveryPointState = "purge_blocked"
)

type RetirementReason string

const (
	RetirementCutover   RetirementReason = "cutover"
	RetirementWithdrawn RetirementReason = "withdrawn"
)

type ImmutabilityLevel string

const (
	ImmutabilityMutable          ImmutabilityLevel = "mutable"
	ImmutabilityXirangManaged    ImmutabilityLevel = "xirang_managed"
	ImmutabilityBackendVersioned ImmutabilityLevel = "backend_versioned"
	ImmutabilityStorageWORM      ImmutabilityLevel = "storage_worm"
)

type PhysicalAvailability string

const (
	PhysicalOnline  PhysicalAvailability = "online"
	PhysicalOffline PhysicalAvailability = "offline"
	PhysicalMissing PhysicalAvailability = "missing"
	PhysicalUnknown PhysicalAvailability = "unknown"
)

type HoldState string

const (
	HoldNone     HoldState = "none"
	HoldActive   HoldState = "active"
	HoldReleased HoldState = "released"
)

type RetentionPolicyScopeKind string

const (
	RetentionPolicyScopeRepository RetentionPolicyScopeKind = "repository"
	RetentionPolicyScopeTaskLink   RetentionPolicyScopeKind = "task_link"
)

type RetentionPolicyStatus string

const (
	RetentionPolicyActive  RetentionPolicyStatus = "active"
	RetentionPolicyDeleted RetentionPolicyStatus = "deleted"
)

type RecoveryPointHoldType string

const (
	RecoveryPointHoldOperational RecoveryPointHoldType = "operational"
	RecoveryPointHoldLegal       RecoveryPointHoldType = "legal"
)

type LifecycleOperation string

const (
	LifecycleRetentionExpire LifecycleOperation = "retention_expire"
	LifecycleExplicitPurge   LifecycleOperation = "explicit_purge"
	LifecycleMutableRetire   LifecycleOperation = "mutable_retire"
)

type SourceLifecycleStage string

const (
	SourceLifecyclePrepare SourceLifecycleStage = "prepare"
	SourceLifecycleCleanup SourceLifecycleStage = "cleanup"
)

type SourceLifecycleRequest struct {
	RecoveryPointID    string
	LifecycleAttemptID string
	Operation          LifecycleOperation
	Stage              SourceLifecycleStage
}

type LifecyclePhase string

const (
	LifecyclePhaseSelected       LifecyclePhase = "selected"
	LifecyclePhaseRevoking       LifecyclePhase = "revoking"
	LifecyclePhaseDraining       LifecyclePhase = "draining"
	LifecyclePhaseCleaning       LifecyclePhase = "cleaning"
	LifecyclePhaseProviderDelete LifecyclePhase = "provider_delete"
	LifecyclePhaseTombstoning    LifecyclePhase = "tombstoning"
	LifecyclePhaseBlocked        LifecyclePhase = "blocked"
	LifecyclePhaseComplete       LifecyclePhase = "complete"
)

type LifecycleBlockedReason string

const (
	LifecycleBlockedActiveHold               LifecycleBlockedReason = "active_hold"
	LifecycleBlockedLeaseLive                LifecycleBlockedReason = "lease_live"
	LifecycleBlockedLeaseDrainUnproven       LifecycleBlockedReason = "lease_drain_unproven"
	LifecycleBlockedOwnerCleanupUnproven     LifecycleBlockedReason = "owner_cleanup_unproven"
	LifecycleBlockedProviderWORM             LifecycleBlockedReason = "provider_worm"
	LifecycleBlockedProviderUnavailable      LifecycleBlockedReason = "provider_unavailable"
	LifecycleBlockedProviderIdentityConflict LifecycleBlockedReason = "provider_identity_conflict"
	LifecycleBlockedProviderDeleteUnproven   LifecycleBlockedReason = "provider_delete_unproven"
	LifecycleBlockedDeletionUnavailable      LifecycleBlockedReason = "deletion_unavailable"
	LifecycleBlockedFenceLost                LifecycleBlockedReason = "fence_lost"
)

type ImportCandidateKind string

const (
	ImportCandidateNativeSnapshot   ImportCandidateKind = "native_snapshot"
	ImportCandidateXirangManifest   ImportCandidateKind = "xirang_manifest"
	ImportCandidateImportedBaseline ImportCandidateKind = "imported_baseline"
	ImportCandidateMutableHead      ImportCandidateKind = "mutable_head"
)

type ImportReviewState string

const (
	ImportReviewPending  ImportReviewState = "pending"
	ImportReviewAccepted ImportReviewState = "accepted"
	ImportReviewRejected ImportReviewState = "rejected"
)

type PurgePlanStatus string

const (
	PurgePlanReady       PurgePlanStatus = "ready"
	PurgePlanBound       PurgePlanStatus = "bound"
	PurgePlanExecuting   PurgePlanStatus = "executing"
	PurgePlanConsumed    PurgePlanStatus = "consumed"
	PurgePlanInvalidated PurgePlanStatus = "invalidated"
)

type ConfigImportEntityKind string

const (
	ConfigImportRepository      ConfigImportEntityKind = "repository"
	ConfigImportTaskLink        ConfigImportEntityKind = "task_link"
	ConfigImportRetentionPolicy ConfigImportEntityKind = "retention_policy"
	ConfigImportHold            ConfigImportEntityKind = "hold"
)

type ManifestCompleteness string

const (
	ManifestComplete    ManifestCompleteness = "complete"
	ManifestPartial     ManifestCompleteness = "partial"
	ManifestUnavailable ManifestCompleteness = "unavailable"
)

type CatalogGenerationState string

const (
	CatalogGenerationBuilding   CatalogGenerationState = "building"
	CatalogGenerationComplete   CatalogGenerationState = "complete"
	CatalogGenerationPartial    CatalogGenerationState = "partial"
	CatalogGenerationFailed     CatalogGenerationState = "failed"
	CatalogGenerationSuperseded CatalogGenerationState = "superseded"
)

type CatalogEntryType string

const (
	CatalogEntryFile      CatalogEntryType = "file"
	CatalogEntryDirectory CatalogEntryType = "directory"
	CatalogEntrySymlink   CatalogEntryType = "symlink"
	CatalogEntryHardlink  CatalogEntryType = "hardlink"
	CatalogEntrySpecial   CatalogEntryType = "special"
	CatalogEntryUnknown   CatalogEntryType = "unknown"
)

type LeaseHolderType string

const (
	LeaseHolderRsyncParent      LeaseHolderType = "rsync_parent"
	LeaseHolderCatalogBuild     LeaseHolderType = "catalog_build"
	LeaseHolderContentSession   LeaseHolderType = "content_session"
	LeaseHolderProcessingJob    LeaseHolderType = "processing_job"
	LeaseHolderExportJob        LeaseHolderType = "export_job"
	LeaseHolderRecoveryJob      LeaseHolderType = "recovery_job"
	LeaseHolderPointPublication LeaseHolderType = "point_publication"
)

type AssetRef struct {
	RecoveryPointID string `json:"recovery_point_id"`
	EntryID         string `json:"entry_id"`
}

type RecoveryPointProfile struct {
	VersionMode                 VersionMode
	Semantics                   PointVersionSemantics
	State                       RecoveryPointState
	Immutability                ImmutabilityLevel
	Availability                PhysicalAvailability
	Hold                        HoldState
	ObservedAt                  *time.Time
	RetirementReason            RetirementReason
	RetiredAt                   *time.Time
	HasEncryptedRollbackLocator bool
}

type MutableHead struct {
	ID                string
	RepositoryID      string
	State             RecoveryPointState
	SourceFingerprint string
	ObservedAt        time.Time
	Availability      PhysicalAvailability
	CatalogGeneration string
}

type MutableObservation struct {
	SourceFingerprint string
	ObservedAt        time.Time
	Availability      PhysicalAvailability
	CatalogGeneration string
}

type CapabilityCode string

const (
	CapabilityFeatureDisabled               CapabilityCode = "feature_disabled"
	CapabilityTaskArtifactContractMissing   CapabilityCode = "task_artifact_contract_missing"
	CapabilityRepositoryOffline             CapabilityCode = "repository_offline"
	CapabilityRepositoryDisconnected        CapabilityCode = "repository_disconnected"
	CapabilityProviderUnavailable           CapabilityCode = "provider_unavailable"
	CapabilityRepositoryIdentityUnavailable CapabilityCode = "repository_identity_unavailable"
	CapabilityProviderProtocolIncompatible  CapabilityCode = "provider_protocol_incompatible"
	CapabilityProviderOperationTimeout      CapabilityCode = "provider_operation_timeout"
	CapabilityProviderResourceLimit         CapabilityCode = "provider_resource_limit"
	CapabilityPointNotCommitted             CapabilityCode = "point_not_committed"
	CapabilityMutableSourceChanged          CapabilityCode = "mutable_source_changed"
	CapabilityCatalogUnavailable            CapabilityCode = "catalog_unavailable"
	CapabilitySequentialReadUnavailable     CapabilityCode = "sequential_read_unavailable"
	CapabilityRangeUnavailable              CapabilityCode = "range_unavailable"
	CapabilityDownloadUnavailable           CapabilityCode = "download_unavailable"
	CapabilityRestoreUnavailable            CapabilityCode = "restore_unavailable"
	CapabilityDeletionUnavailable           CapabilityCode = "deletion_unavailable"
	CapabilityDiffUnavailable               CapabilityCode = "diff_unavailable"
)

type CapabilityReason struct {
	Code   CapabilityCode    `json:"code" enums:"feature_disabled,task_artifact_contract_missing,repository_offline,repository_disconnected,provider_unavailable,repository_identity_unavailable,provider_protocol_incompatible,provider_operation_timeout,provider_resource_limit,point_not_committed,mutable_source_changed,catalog_unavailable,sequential_read_unavailable,range_unavailable,download_unavailable,restore_unavailable,deletion_unavailable,diff_unavailable"`
	Params map[string]string `json:"params,omitempty"`
}

type CapabilitySet struct {
	List           bool              `json:"list"`
	SearchPath     bool              `json:"search_path"`
	OpenSequential bool              `json:"open_sequential"`
	OpenRange      bool              `json:"open_range"`
	Download       bool              `json:"download"`
	Restore        bool              `json:"restore"`
	Diff           bool              `json:"diff"`
	NativeHistory  bool              `json:"native_history"`
	Reason         *CapabilityReason `json:"reason,omitempty"`
}

type TaskArtifactContract struct {
	Provider            ProviderKind
	PublicationMode     TaskPublicationMode
	HasArtifactContract bool
}

const ImportedRepositoryIdentityRefPrefix = "identity_ref:v1:"

func ImportedIdentityRef(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func FormatImportedRepositoryIdentity(identityRef string) string {
	return ImportedRepositoryIdentityRefPrefix + strings.TrimSpace(identityRef)
}

func BindImportedRepositoryIdentity(stored *string, observed string) (string, error) {
	observed = strings.TrimSpace(observed)
	if observed == "" {
		return "", fmt.Errorf("%w: repository identity mismatch", ErrConflict)
	}
	if stored == nil || strings.TrimSpace(*stored) == "" {
		return observed, nil
	}
	current := strings.TrimSpace(*stored)
	if digest, ok := strings.CutPrefix(current, ImportedRepositoryIdentityRefPrefix); ok {
		if ImportedIdentityRef(observed) != digest {
			return "", fmt.Errorf("%w: repository identity mismatch", ErrConflict)
		}
		return observed, nil
	}
	if current != observed {
		return "", fmt.Errorf("%w: repository identity mismatch", ErrConflict)
	}
	return observed, nil
}

func NewOpaqueID() (string, error) {
	return newOpaqueIDFrom(rand.Reader)
}

func newOpaqueIDFrom(source io.Reader) (string, error) {
	raw := make([]byte, opaqueIDBytes)
	if _, err := io.ReadFull(source, raw); err != nil {
		return "", fmt.Errorf("generate opaque ID: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func ValidateOpaqueID(value string) error {
	if !isLowerHex(value, opaqueIDEncodedSize) {
		return fmt.Errorf("%w: opaque ID must be 32 lowercase hex characters", ErrInvalidState)
	}
	return nil
}

func ValidateRetentionPolicyScope(kind RetentionPolicyScopeKind, scopeID string) error {
	if !validRetentionPolicyScopeKinds[kind] || ValidateOpaqueID(scopeID) != nil {
		return fmt.Errorf("%w: invalid retention policy scope", ErrInvalidState)
	}
	return nil
}

func ValidateRecoveryPointHoldType(holdType RecoveryPointHoldType) error {
	if !validRecoveryPointHoldTypes[holdType] {
		return fmt.Errorf("%w: invalid recovery point hold type", ErrInvalidState)
	}
	return nil
}

func ValidateRetentionPolicyStatus(status RetentionPolicyStatus) error {
	if !validRetentionPolicyStatuses[status] {
		return fmt.Errorf("%w: invalid retention policy status", ErrInvalidState)
	}
	return nil
}

func ValidateRecoveryPointHoldRecordState(state HoldState) error {
	if !validRecoveryPointHoldRecordStates[state] {
		return fmt.Errorf("%w: invalid recovery point hold record state", ErrInvalidState)
	}
	return nil
}

func ValidateLifecycleOperation(operation LifecycleOperation) error {
	if !validLifecycleOperations[operation] {
		return fmt.Errorf("%w: invalid recovery point lifecycle operation", ErrInvalidState)
	}
	return nil
}

func ValidateSourceLifecycleRequest(request SourceLifecycleRequest) error {
	if ValidateOpaqueID(request.RecoveryPointID) != nil || ValidateOpaqueID(request.LifecycleAttemptID) != nil ||
		ValidateLifecycleOperation(request.Operation) != nil ||
		request.Stage != SourceLifecyclePrepare && request.Stage != SourceLifecycleCleanup {
		return fmt.Errorf("%w: invalid source lifecycle request", ErrInvalidState)
	}
	return nil
}

func ValidateLifecyclePhase(phase LifecyclePhase) error {
	if !validLifecyclePhases[phase] {
		return fmt.Errorf("%w: invalid recovery point lifecycle phase", ErrInvalidState)
	}
	return nil
}

func ValidateLifecycleBlockedReason(reason LifecycleBlockedReason) error {
	if !validLifecycleBlockedReasons[reason] {
		return fmt.Errorf("%w: invalid recovery point lifecycle blocked reason", ErrInvalidState)
	}
	return nil
}

func ValidateImportCandidateKind(kind ImportCandidateKind) error {
	if !validImportCandidateKinds[kind] {
		return fmt.Errorf("%w: invalid backup import candidate kind", ErrInvalidState)
	}
	return nil
}

func ValidateImportReviewState(state ImportReviewState) error {
	if !validImportReviewStates[state] {
		return fmt.Errorf("%w: invalid backup import review state", ErrInvalidState)
	}
	return nil
}

func ValidatePurgePlanStatus(status PurgePlanStatus) error {
	if !validPurgePlanStatuses[status] {
		return fmt.Errorf("%w: invalid backup asset purge plan status", ErrInvalidState)
	}
	return nil
}

func ValidateConfigImportEntityKind(kind ConfigImportEntityKind) error {
	if !validConfigImportEntityKinds[kind] {
		return fmt.Errorf("%w: invalid backup config import entity kind", ErrInvalidState)
	}
	return nil
}

func ValidateAssetRef(ref AssetRef) error {
	if ValidateOpaqueID(ref.RecoveryPointID) != nil || !isLowerHex(ref.EntryID, entryIDEncodedSize) {
		return fmt.Errorf("%w: recovery_point_id and entry_id are required", ErrInvalidAssetRef)
	}
	return nil
}

func ValidateRepositoryTransition(from, to RepositoryStatus) error {
	if !validRepositoryStatuses[from] || !validRepositoryStatuses[to] {
		return fmt.Errorf("%w: unknown repository state", ErrInvalidState)
	}
	if from == to || repositoryTransitions[[2]RepositoryStatus{from, to}] {
		return nil
	}
	return fmt.Errorf("%w: repository transition %s to %s", ErrInvalidState, from, to)
}

func ValidateRecoveryPointTransition(profile RecoveryPointProfile, to RecoveryPointState) error {
	if err := ValidateRecoveryPointProfile(profile); err != nil {
		return err
	}
	if !validRecoveryPointStates[to] {
		return fmt.Errorf("%w: unknown recovery point state", ErrInvalidState)
	}
	if profile.State == to {
		return nil
	}

	key := [2]RecoveryPointState{profile.State, to}
	if profile.Semantics == PointMutableHead {
		if mutableRecoveryPointTransitions[key] {
			return nil
		}
	} else if immutableRecoveryPointTransitions[key] {
		return nil
	}
	return fmt.Errorf("%w: recovery point transition %s to %s", ErrInvalidState, profile.State, to)
}

func ValidateRecoveryPointProfile(profile RecoveryPointProfile) error {
	if !validVersionModes[profile.VersionMode] || !validPointSemantics[profile.Semantics] ||
		!validRecoveryPointStates[profile.State] || !validImmutabilityLevels[profile.Immutability] ||
		!validPhysicalAvailability[profile.Availability] {
		return fmt.Errorf("%w: unknown recovery point profile value", ErrInvalidState)
	}
	if profile.Hold == "" {
		profile.Hold = HoldNone
	}
	if !validHoldStates[profile.Hold] {
		return fmt.Errorf("%w: unknown hold state", ErrInvalidState)
	}

	if profile.Semantics == PointMutableHead {
		if profile.VersionMode != VersionMutableHead || profile.Immutability != ImmutabilityMutable || profile.Hold != HoldNone {
			return fmt.Errorf("%w: mutable-head profile mismatch", ErrInvalidState)
		}
		if !validMutableStates[profile.State] || profile.ObservedAt == nil {
			return fmt.Errorf("%w: invalid mutable-head lifecycle", ErrInvalidState)
		}
		if profile.State == RecoveryPointRetired {
			if !validRetirementReasons[profile.RetirementReason] || profile.RetiredAt == nil || !profile.HasEncryptedRollbackLocator {
				return fmt.Errorf("%w: incomplete mutable-head retirement", ErrInvalidState)
			}
		} else if profile.State == RecoveryPointObserved && (profile.RetirementReason != "" || profile.RetiredAt != nil || profile.HasEncryptedRollbackLocator) {
			return fmt.Errorf("%w: retirement data on observed mutable head", ErrInvalidState)
		}
		return nil
	}

	if profile.VersionMode == VersionMutableHead || profile.Immutability == ImmutabilityMutable ||
		profile.ObservedAt != nil || profile.RetirementReason != "" || profile.RetiredAt != nil || profile.HasEncryptedRollbackLocator {
		return fmt.Errorf("%w: mutable fields on immutable point", ErrInvalidState)
	}
	if !validImmutableStates[profile.State] {
		return fmt.Errorf("%w: immutable point has mutable state", ErrInvalidState)
	}
	if !validImmutableModeSemantics[[2]string{string(profile.VersionMode), string(profile.Semantics)}] {
		return fmt.Errorf("%w: version mode and point semantics mismatch", ErrInvalidState)
	}
	return nil
}

func MapPublicationMode(provider ProviderKind, mode TaskPublicationMode) (VersionMode, PointVersionSemantics, RecoveryPointState, error) {
	if provider == ProviderCommand {
		return "", "", "", fmt.Errorf("%w: command task has no artifact contract", ErrCapabilityUnavailable)
	}
	if provider == ProviderRestic {
		if mode != PublicationNativeSnapshot {
			return "", "", "", fmt.Errorf("%w: Restic requires native_snapshot publication", ErrInvalidState)
		}
		return VersionNativeSnapshot, PointNativeSnapshot, RecoveryPointPreparing, nil
	}

	var version VersionMode
	switch {
	case provider == ProviderRsync && mode == PublicationLegacyMutable,
		provider == ProviderRclone && mode == PublicationLegacyMutable:
		return VersionMutableHead, PointMutableHead, RecoveryPointObserved, nil
	case provider == ProviderRsync && mode == PublicationVersionedHardlink:
		version = VersionHardlinkTree
	case provider == ProviderRsync && mode == PublicationVersionedFullCopy:
		version = VersionFullCopyTree
	case provider == ProviderRclone && mode == PublicationVersionedPrefix:
		version = VersionVersionedPrefix
	case provider == ProviderRclone && mode == PublicationNativeObjectVersions:
		version = VersionNativeObjectVersions
	case provider == ProviderVerifiedImport:
		version = versionModeForPublication(mode)
		if version == "" || version == VersionMutableHead {
			return "", "", "", fmt.Errorf("%w: unsupported verified import mode", ErrInvalidState)
		}
		return version, PointImportedBaseline, RecoveryPointPreparing, nil
	default:
		return "", "", "", fmt.Errorf("%w: unsupported provider/publication combination", ErrInvalidState)
	}
	return version, PointXirangManifest, RecoveryPointPreparing, nil
}

func CapabilitiesForTask(contract TaskArtifactContract) CapabilitySet {
	if contract.Provider == ProviderCommand && !contract.HasArtifactContract {
		return CapabilitySet{Reason: &CapabilityReason{Code: CapabilityTaskArtifactContractMissing}}
	}

	capabilities := CapabilitySet{
		List:           true,
		SearchPath:     true,
		OpenSequential: true,
		Download:       true,
		Restore:        true,
	}
	switch contract.Provider {
	case ProviderRestic:
		capabilities.Diff = true
		capabilities.NativeHistory = true
	case ProviderRsync:
		capabilities.Diff = contract.PublicationMode != PublicationLegacyMutable
	case ProviderRclone:
		capabilities.Diff = contract.PublicationMode != PublicationLegacyMutable
		capabilities.NativeHistory = contract.PublicationMode == PublicationNativeObjectVersions
	case ProviderCommand:
		capabilities.Diff = contract.PublicationMode != PublicationLegacyMutable
	default:
		return CapabilitySet{Reason: &CapabilityReason{Code: CapabilityProviderUnavailable}}
	}
	return capabilities
}

func ValidateCapabilityReason(reason CapabilityReason) error {
	if !validCapabilityCodes[reason.Code] {
		return fmt.Errorf("%w: unknown capability reason", ErrInvalidState)
	}
	for key, value := range reason.Params {
		if !allowedCapabilityParams[key] || strings.ContainsAny(value, "\r\n\x00") || len(value) > 128 {
			return fmt.Errorf("%w: unsafe capability reason parameter", ErrInvalidState)
		}
		if key == "retry_after_seconds" {
			if parsed, err := strconv.ParseUint(value, 10, 64); err != nil || parsed == 0 {
				return fmt.Errorf("%w: invalid retry_after_seconds", ErrInvalidState)
			}
		}
	}
	return nil
}

func ApplyMutableObservation(current MutableHead, observation MutableObservation) (MutableHead, error) {
	if ValidateOpaqueID(current.ID) != nil || ValidateOpaqueID(current.RepositoryID) != nil || current.State != RecoveryPointObserved {
		return MutableHead{}, fmt.Errorf("%w: mutable head is not observable", ErrInvalidState)
	}
	if observation.ObservedAt.IsZero() || !validPhysicalAvailability[observation.Availability] {
		return MutableHead{}, fmt.Errorf("%w: invalid mutable observation", ErrInvalidState)
	}
	if observation.CatalogGeneration != "" && ValidateOpaqueID(observation.CatalogGeneration) != nil {
		return MutableHead{}, fmt.Errorf("%w: invalid catalog generation", ErrInvalidState)
	}
	current.SourceFingerprint = observation.SourceFingerprint
	current.ObservedAt = observation.ObservedAt.UTC()
	current.Availability = observation.Availability
	current.CatalogGeneration = observation.CatalogGeneration
	return current, nil
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func versionModeForPublication(mode TaskPublicationMode) VersionMode {
	switch mode {
	case PublicationVersionedHardlink:
		return VersionHardlinkTree
	case PublicationVersionedFullCopy:
		return VersionFullCopyTree
	case PublicationVersionedPrefix:
		return VersionVersionedPrefix
	case PublicationNativeObjectVersions:
		return VersionNativeObjectVersions
	default:
		return ""
	}
}

var validRepositoryStatuses = setOf(
	RepositoryConnecting, RepositoryOnline, RepositoryDegraded, RepositoryOffline,
	RepositoryDisconnected, RepositoryPurging, RepositoryPurgeBlocked,
)

var repositoryTransitions = map[[2]RepositoryStatus]bool{
	{RepositoryConnecting, RepositoryOnline}:       true,
	{RepositoryConnecting, RepositoryDegraded}:     true,
	{RepositoryConnecting, RepositoryOffline}:      true,
	{RepositoryConnecting, RepositoryDisconnected}: true,
	{RepositoryOnline, RepositoryDegraded}:         true,
	{RepositoryOnline, RepositoryOffline}:          true,
	{RepositoryOnline, RepositoryDisconnected}:     true,
	{RepositoryOnline, RepositoryPurging}:          true,
	{RepositoryDegraded, RepositoryOnline}:         true,
	{RepositoryDegraded, RepositoryOffline}:        true,
	{RepositoryDegraded, RepositoryDisconnected}:   true,
	{RepositoryDegraded, RepositoryPurging}:        true,
	{RepositoryOffline, RepositoryConnecting}:      true,
	{RepositoryOffline, RepositoryOnline}:          true,
	{RepositoryOffline, RepositoryDegraded}:        true,
	{RepositoryOffline, RepositoryDisconnected}:    true,
	{RepositoryOffline, RepositoryPurging}:         true,
	{RepositoryDisconnected, RepositoryConnecting}: true,
	{RepositoryDisconnected, RepositoryPurging}:    true,
	{RepositoryPurging, RepositoryPurgeBlocked}:    true,
	{RepositoryPurgeBlocked, RepositoryPurging}:    true,
}

var validRecoveryPointStates = setOf(
	RecoveryPointObserved, RecoveryPointRetired, RecoveryPointPreparing, RecoveryPointVerifying,
	RecoveryPointCommitted, RecoveryPointDegraded, RecoveryPointExpiring, RecoveryPointExpired,
	RecoveryPointFailed, RecoveryPointPurgeBlocked,
)

var immutableRecoveryPointTransitions = map[[2]RecoveryPointState]bool{
	{RecoveryPointPreparing, RecoveryPointVerifying}:   true,
	{RecoveryPointPreparing, RecoveryPointFailed}:      true,
	{RecoveryPointVerifying, RecoveryPointCommitted}:   true,
	{RecoveryPointVerifying, RecoveryPointFailed}:      true,
	{RecoveryPointCommitted, RecoveryPointDegraded}:    true,
	{RecoveryPointCommitted, RecoveryPointExpiring}:    true,
	{RecoveryPointDegraded, RecoveryPointCommitted}:    true,
	{RecoveryPointDegraded, RecoveryPointExpiring}:     true,
	{RecoveryPointExpiring, RecoveryPointExpired}:      true,
	{RecoveryPointExpiring, RecoveryPointPurgeBlocked}: true,
	{RecoveryPointPurgeBlocked, RecoveryPointExpiring}: true,
}

var mutableRecoveryPointTransitions = map[[2]RecoveryPointState]bool{
	{RecoveryPointObserved, RecoveryPointRetired}:      true,
	{RecoveryPointObserved, RecoveryPointExpiring}:     true,
	{RecoveryPointRetired, RecoveryPointExpiring}:      true,
	{RecoveryPointExpiring, RecoveryPointExpired}:      true,
	{RecoveryPointExpiring, RecoveryPointPurgeBlocked}: true,
	{RecoveryPointPurgeBlocked, RecoveryPointExpiring}: true,
}

var (
	validVersionModes = setOf(
		VersionNativeSnapshot, VersionHardlinkTree, VersionFullCopyTree,
		VersionVersionedPrefix, VersionNativeObjectVersions, VersionMutableHead,
	)
	validPointSemantics                = setOf(PointNativeSnapshot, PointXirangManifest, PointImportedBaseline, PointMutableHead)
	validImmutabilityLevels            = setOf(ImmutabilityMutable, ImmutabilityXirangManaged, ImmutabilityBackendVersioned, ImmutabilityStorageWORM)
	validPhysicalAvailability          = setOf(PhysicalOnline, PhysicalOffline, PhysicalMissing, PhysicalUnknown)
	validHoldStates                    = setOf(HoldNone, HoldActive, HoldReleased)
	validRetirementReasons             = setOf(RetirementCutover, RetirementWithdrawn)
	validMutableStates                 = setOf(RecoveryPointObserved, RecoveryPointRetired, RecoveryPointExpiring, RecoveryPointExpired, RecoveryPointPurgeBlocked)
	validImmutableStates               = setOf(RecoveryPointPreparing, RecoveryPointVerifying, RecoveryPointCommitted, RecoveryPointDegraded, RecoveryPointExpiring, RecoveryPointExpired, RecoveryPointFailed, RecoveryPointPurgeBlocked)
	validRetentionPolicyScopeKinds     = setOf(RetentionPolicyScopeRepository, RetentionPolicyScopeTaskLink)
	validRetentionPolicyStatuses       = setOf(RetentionPolicyActive, RetentionPolicyDeleted)
	validRecoveryPointHoldTypes        = setOf(RecoveryPointHoldOperational, RecoveryPointHoldLegal)
	validRecoveryPointHoldRecordStates = setOf(HoldActive, HoldReleased)
	validLifecycleOperations           = setOf(LifecycleRetentionExpire, LifecycleExplicitPurge, LifecycleMutableRetire)
	validLifecyclePhases               = setOf(
		LifecyclePhaseSelected, LifecyclePhaseRevoking, LifecyclePhaseDraining, LifecyclePhaseCleaning,
		LifecyclePhaseProviderDelete, LifecyclePhaseTombstoning, LifecyclePhaseBlocked, LifecyclePhaseComplete,
	)
	validImportCandidateKinds = setOf(
		ImportCandidateNativeSnapshot, ImportCandidateXirangManifest, ImportCandidateImportedBaseline, ImportCandidateMutableHead,
	)
	validLifecycleBlockedReasons = setOf(
		LifecycleBlockedActiveHold, LifecycleBlockedLeaseLive, LifecycleBlockedLeaseDrainUnproven,
		LifecycleBlockedOwnerCleanupUnproven, LifecycleBlockedProviderWORM, LifecycleBlockedProviderUnavailable,
		LifecycleBlockedProviderIdentityConflict, LifecycleBlockedProviderDeleteUnproven,
		LifecycleBlockedDeletionUnavailable, LifecycleBlockedFenceLost,
	)
	validImportReviewStates      = setOf(ImportReviewPending, ImportReviewAccepted, ImportReviewRejected)
	validPurgePlanStatuses       = setOf(PurgePlanReady, PurgePlanBound, PurgePlanExecuting, PurgePlanConsumed, PurgePlanInvalidated)
	validConfigImportEntityKinds = setOf(ConfigImportRepository, ConfigImportTaskLink, ConfigImportRetentionPolicy, ConfigImportHold)
)

var validImmutableModeSemantics = map[[2]string]bool{
	{string(VersionNativeSnapshot), string(PointNativeSnapshot)}:         true,
	{string(VersionHardlinkTree), string(PointXirangManifest)}:           true,
	{string(VersionFullCopyTree), string(PointXirangManifest)}:           true,
	{string(VersionVersionedPrefix), string(PointXirangManifest)}:        true,
	{string(VersionNativeObjectVersions), string(PointXirangManifest)}:   true,
	{string(VersionNativeSnapshot), string(PointImportedBaseline)}:       true,
	{string(VersionHardlinkTree), string(PointImportedBaseline)}:         true,
	{string(VersionFullCopyTree), string(PointImportedBaseline)}:         true,
	{string(VersionVersionedPrefix), string(PointImportedBaseline)}:      true,
	{string(VersionNativeObjectVersions), string(PointImportedBaseline)}: true,
}

var validCapabilityCodes = setOf(
	CapabilityFeatureDisabled, CapabilityTaskArtifactContractMissing, CapabilityRepositoryOffline,
	CapabilityRepositoryDisconnected, CapabilityProviderUnavailable, CapabilityRepositoryIdentityUnavailable,
	CapabilityProviderProtocolIncompatible, CapabilityProviderOperationTimeout, CapabilityProviderResourceLimit,
	CapabilityPointNotCommitted,
	CapabilityMutableSourceChanged, CapabilityCatalogUnavailable, CapabilitySequentialReadUnavailable,
	CapabilityRangeUnavailable, CapabilityDownloadUnavailable, CapabilityRestoreUnavailable,
	CapabilityDeletionUnavailable, CapabilityDiffUnavailable,
)

var allowedCapabilityParams = map[string]bool{
	"provider_kind":        true,
	"repository_status":    true,
	"recovery_point_state": true,
	"capability":           true,
	"correlation_id":       true,
	"retry_after_seconds":  true,
}

func setOf[T comparable](values ...T) map[T]bool {
	result := make(map[T]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
