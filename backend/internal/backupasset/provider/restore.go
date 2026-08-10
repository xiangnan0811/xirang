package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

const (
	RestoreRequestSchemaV1  = 1
	RestoreSchemaUseLatchID = "schema_use_latch"

	maxRestoreEntries       = 100_000
	maxRestoreIncludes      = 100_000
	maxRestorePrivateLength = 4096
)

var (
	ErrInvalidRestoreRequest   = errors.New("invalid exact restore request")
	ErrRsyncRestoreSourceDrift = fmt.Errorf("%w: rsync restore source changed", ErrInvalidRestoreRequest)
	ErrRsyncRestoreUnavailable = fmt.Errorf("%w: rsync restore unavailable", backupasset.ErrCapabilityUnavailable)
)

// RsyncRestoreSourceRef is the portable scalar binding that identifies a
// managed Rsync source without carrying a locator or filesystem capability.
type RsyncRestoreSourceRef struct {
	PlanID               string `json:"plan_id"`
	PlanBindingDigest    string `json:"plan_binding_digest"`
	RepositoryID         string `json:"repository_id"`
	RecoveryPointID      string `json:"recovery_point_id"`
	CatalogGenerationID  string `json:"catalog_generation_id"`
	SelectionDigest      string `json:"selection_digest"`
	SourceRevisionDigest string `json:"source_revision_digest"`
	ManifestDigest       string `json:"manifest_digest"`
}

// Validate accepts only the durable scalar facts that Repository revalidates
// before every Rsync phase. It deliberately contains no locator or task input.
func (ref RsyncRestoreSourceRef) Validate() error {
	if backupasset.ValidateOpaqueID(ref.PlanID) != nil ||
		backupasset.ValidateOpaqueID(ref.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(ref.RecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(ref.CatalogGenerationID) != nil ||
		!validRestoreDigest(ref.PlanBindingDigest) || !validRestoreDigest(ref.SelectionDigest) ||
		!validRestoreDigest(ref.SourceRevisionDigest) || !validRestoreDigest(ref.ManifestDigest) {
		return invalidRestoreRequest("invalid Rsync restore source ref")
	}
	return nil
}

// RestoreTargetPurpose is intentionally closed so a credential session cannot
// be reused between read-only and mutating recovery stages.
type RestoreTargetPurpose string

const (
	TargetPurposePreflight RestoreTargetPurpose = "recovery_preflight"
	TargetPurposeWrite     RestoreTargetPurpose = "recovery_write"
	TargetPurposeVerify    RestoreTargetPurpose = "recovery_verify"
	TargetPurposeReconcile RestoreTargetPurpose = "recovery_reconcile"
)

func (purpose RestoreTargetPurpose) valid() bool {
	switch purpose {
	case TargetPurposePreflight, TargetPurposeWrite, TargetPurposeVerify, TargetPurposeReconcile:
		return true
	default:
		return false
	}
}

// RestoreSource is the sole provider-owned raw source locator handoff. The
// Repository boundary constructs it only after revalidating its point binding.
// Locator and LocatorDigest must never leave this process boundary.
type RestoreSource struct {
	Provider        backupasset.ProviderKind `json:"provider"`
	RepositoryID    string                   `json:"repository_id"`
	RecoveryPointID string                   `json:"recovery_point_id"`
	Locator         string                   `json:"-"`
	LocatorDigest   string                   `json:"-"`

	capability *restoreSourceCapability
}

// restoreSourceCapability seals a locator to the trusted recovery handoff
// that issued it. A raw RestoreSource literal, including one with a matching
// locator digest, has no capability and therefore cannot reach a Provider.
type restoreSourceCapability struct {
	provider        backupasset.ProviderKind
	repositoryID    string
	recoveryPointID string
	locator         string
	locatorDigest   string
}

// NewRestoreSource deliberately rejects raw-locator construction. Callers
// must use the Recovery-owned validated handoff constructors below; this
// compatibility-shaped entry point remains only so old direct callers fail
// closed instead of silently receiving a Provider-capable source.
func NewRestoreSource(provider backupasset.ProviderKind, repositoryID, recoveryPointID, locator string) (RestoreSource, error) {
	return RestoreSource{}, invalidRestoreRequest("raw restore source construction denied")
}

// NewValidatedRestoreSource mints a sealed non-Rsync source after the
// Recovery/Repository boundary has already validated its exact locator. Rsync
// intentionally has no generic arm because it additionally requires managed
// local-root containment and filesystem identity facts.
func NewValidatedRestoreSource(provider backupasset.ProviderKind, repositoryID, recoveryPointID, locator string) (RestoreSource, error) {
	if provider == backupasset.ProviderRsync {
		return RestoreSource{}, invalidRestoreRequest("Rsync restore source requires managed capability")
	}
	return newValidatedRestoreSource(provider, repositoryID, recoveryPointID, locator)
}

func newValidatedRestoreSource(
	provider backupasset.ProviderKind,
	repositoryID, recoveryPointID, locator string,
) (RestoreSource, error) {
	source := RestoreSource{
		Provider: provider, RepositoryID: repositoryID, RecoveryPointID: recoveryPointID, Locator: locator,
	}
	if !source.validShape() || provider == backupasset.ProviderRsync {
		return RestoreSource{}, invalidRestoreRequest("invalid restore source")
	}
	digest, err := restoreSourceLocatorDigest(source)
	if err != nil {
		return RestoreSource{}, err
	}
	source.LocatorDigest = digest
	source.capability = &restoreSourceCapability{
		provider: provider, repositoryID: repositoryID, recoveryPointID: recoveryPointID,
		locator: locator, locatorDigest: digest,
	}
	return source, nil
}

func (source RestoreSource) Validate() error {
	if !source.validShape() {
		return invalidRestoreRequest("invalid restore source")
	}
	digest, err := restoreSourceLocatorDigest(source)
	if err != nil || source.LocatorDigest != digest || !source.hasValidCapability() {
		return invalidRestoreRequest("restore source binding mismatch")
	}
	return nil
}

func (source RestoreSource) hasValidCapability() bool {
	capability := source.capability
	if capability == nil || capability.provider != source.Provider || capability.repositoryID != source.RepositoryID ||
		capability.recoveryPointID != source.RecoveryPointID || capability.locator != source.Locator ||
		capability.locatorDigest != source.LocatorDigest {
		return false
	}
	return source.Provider != backupasset.ProviderRsync
}

func (source RestoreSource) empty() bool {
	return source.Provider == "" && source.RepositoryID == "" && source.RecoveryPointID == "" &&
		source.Locator == "" && source.LocatorDigest == "" && source.capability == nil
}

func (source RestoreSource) validShape() bool {
	return validRestoreProvider(source.Provider) && backupasset.ValidateOpaqueID(source.RepositoryID) == nil &&
		backupasset.ValidateOpaqueID(source.RecoveryPointID) == nil && validPrivateRestoreValue(source.Locator)
}

func restoreSourceLocatorDigest(source RestoreSource) (string, error) {
	if !source.validShape() {
		return "", invalidRestoreRequest("invalid restore source")
	}
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang/backup-asset/restore-source/v1")
	writer.String(string(source.Provider))
	writer.String(source.RepositoryID)
	writer.String(source.RecoveryPointID)
	writer.String(source.Locator)
	digest, err := writer.HexDigest()
	if err != nil {
		return "", fmt.Errorf("%w: restore source binding", ErrInvalidRestoreRequest)
	}
	return digest, nil
}

// RestoreTarget carries only opaque target bindings. Raw root locators and
// relative paths remain owned by the target boundary, not this Provider port.
type RestoreTarget struct {
	NodeID            uint   `json:"node_id"`
	RootID            string `json:"root_id"`
	RootLocatorDigest string `json:"-"`
	TargetPathDigest  string `json:"target_path_digest"`
	RootRevision      string `json:"root_revision"`
	TargetRevision    string `json:"target_revision"`
	BindingDigest     string `json:"binding_digest"`
}

func NewRestoreTarget(
	nodeID uint,
	rootID, rootLocatorDigest, targetPathDigest, rootRevision, targetRevision string,
) (RestoreTarget, error) {
	target := RestoreTarget{
		NodeID: nodeID, RootID: rootID, RootLocatorDigest: rootLocatorDigest, TargetPathDigest: targetPathDigest,
		RootRevision: rootRevision, TargetRevision: targetRevision,
	}
	if !target.validShape() {
		return RestoreTarget{}, invalidRestoreRequest("invalid restore target")
	}
	digest, err := restoreTargetBindingDigest(target)
	if err != nil {
		return RestoreTarget{}, err
	}
	target.BindingDigest = digest
	return target, nil
}

func (target RestoreTarget) Validate() error {
	if !target.validShape() {
		return invalidRestoreRequest("invalid restore target")
	}
	digest, err := restoreTargetBindingDigest(target)
	if err != nil || target.BindingDigest != digest {
		return invalidRestoreRequest("restore target binding mismatch")
	}
	return nil
}

func (target RestoreTarget) validShape() bool {
	return target.NodeID != 0 && validRestoreLabel(target.RootID) && validRestoreDigest(target.RootLocatorDigest) &&
		validRestoreDigest(target.TargetPathDigest) && validRestoreRevision(target.RootRevision) &&
		validRestoreRevision(target.TargetRevision)
}

func restoreTargetBindingDigest(target RestoreTarget) (string, error) {
	if !target.validShape() {
		return "", invalidRestoreRequest("invalid restore target")
	}
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang/backup-asset/restore-target/v1")
	writer.Uint64(uint64(target.NodeID))
	writer.String(target.RootID)
	writer.String(target.RootLocatorDigest)
	writer.String(target.TargetPathDigest)
	writer.String(target.RootRevision)
	writer.String(target.TargetRevision)
	digest, err := writer.HexDigest()
	if err != nil {
		return "", fmt.Errorf("%w: restore target binding", ErrInvalidRestoreRequest)
	}
	return digest, nil
}

// TargetSession is an opaque, purpose-scoped credential session reference. It
// deliberately cannot carry credential material.
type TargetSession struct {
	ID                 string               `json:"-"`
	Purpose            RestoreTargetPurpose `json:"purpose"`
	CredentialRevision string               `json:"credential_revision"`
	ExpiresAt          time.Time            `json:"expires_at"`
}

func (session TargetSession) ValidateAt(now time.Time, purpose RestoreTargetPurpose) error {
	if now.IsZero() || !purpose.valid() || session.Purpose != purpose ||
		backupasset.ValidateOpaqueID(session.ID) != nil || !validRestoreRevision(session.CredentialRevision) ||
		!session.ExpiresAt.After(now) {
		return invalidRestoreRequest("invalid target credential session")
	}
	return nil
}

// TargetObservationPermit authorizes a read-only target observation. Its
// purpose-specific wrappers below prevent a caller from reusing a preflight
// session for verification or reconciliation.
type TargetObservationPermit struct {
	TargetBindingDigest string        `json:"target_binding_digest"`
	Session             TargetSession `json:"-"`
}

func (permit TargetObservationPermit) ValidateAt(now time.Time, target RestoreTarget, purpose RestoreTargetPurpose) error {
	if target.Validate() != nil || permit.TargetBindingDigest != target.BindingDigest ||
		permit.Session.ValidateAt(now, purpose) != nil {
		return invalidRestoreRequest("invalid target observation permit")
	}
	return nil
}

type TargetPreflightPermit struct {
	permit TargetObservationPermit
}

func NewTargetPreflightPermit(permit TargetObservationPermit, target RestoreTarget, now time.Time) (TargetPreflightPermit, error) {
	if permit.ValidateAt(now, target, TargetPurposePreflight) != nil {
		return TargetPreflightPermit{}, invalidRestoreRequest("invalid target preflight permit")
	}
	return TargetPreflightPermit{permit: permit}, nil
}

func (permit TargetPreflightPermit) ValidateAt(now time.Time, target RestoreTarget) error {
	return permit.permit.ValidateAt(now, target, TargetPurposePreflight)
}

type TargetVerifyPermit struct {
	permit TargetObservationPermit
}

func NewTargetVerifyPermit(permit TargetObservationPermit, target RestoreTarget, now time.Time) (TargetVerifyPermit, error) {
	if permit.ValidateAt(now, target, TargetPurposeVerify) != nil {
		return TargetVerifyPermit{}, invalidRestoreRequest("invalid target verify permit")
	}
	return TargetVerifyPermit{permit: permit}, nil
}

func (permit TargetVerifyPermit) ValidateAt(now time.Time, target RestoreTarget) error {
	return permit.permit.ValidateAt(now, target, TargetPurposeVerify)
}

type TargetReconcilePermit struct {
	permit TargetObservationPermit
}

func NewTargetReconcilePermit(permit TargetObservationPermit, target RestoreTarget, now time.Time) (TargetReconcilePermit, error) {
	if permit.ValidateAt(now, target, TargetPurposeReconcile) != nil {
		return TargetReconcilePermit{}, invalidRestoreRequest("invalid target reconcile permit")
	}
	return TargetReconcilePermit{permit: permit}, nil
}

func (permit TargetReconcilePermit) ValidateAt(now time.Time, target RestoreTarget) error {
	return permit.permit.ValidateAt(now, target, TargetPurposeReconcile)
}

// TargetMutationPermit is required for every target mutation. The permanent
// use-latch, all active fences, expected target-chain revision, and the
// purpose-scoped credential session must be current before I/O and rechecked
// after each bounded mutation by the target adapter.
type TargetMutationPermit struct {
	TargetBindingDigest    string        `json:"target_binding_digest"`
	UseLatchID             string        `json:"use_latch_id"`
	JobID                  string        `json:"job_id"`
	AttemptID              string        `json:"attempt_id"`
	NodeLeaseID            string        `json:"node_lease_id"`
	AttemptFence           uint64        `json:"attempt_fence"`
	NodeFence              uint64        `json:"node_fence"`
	ExpectedTargetRevision string        `json:"expected_target_revision"`
	Session                TargetSession `json:"-"`
}

func (permit TargetMutationPermit) ValidateAt(now time.Time, target RestoreTarget, fence RestoreFence) error {
	if target.Validate() != nil || fence.Validate() != nil || permit.TargetBindingDigest != target.BindingDigest ||
		permit.UseLatchID != RestoreSchemaUseLatchID || permit.JobID != fence.JobID || permit.AttemptID != fence.AttemptID ||
		permit.NodeLeaseID != fence.NodeLeaseID || permit.AttemptFence != fence.AttemptFence || permit.NodeFence != fence.NodeFence ||
		permit.ExpectedTargetRevision != target.TargetRevision || permit.ExpectedTargetRevision != fence.ExpectedTargetRevision ||
		permit.Session.ValidateAt(now, TargetPurposeWrite) != nil {
		return invalidRestoreRequest("invalid target mutation permit")
	}
	return nil
}

type RestoreFence struct {
	JobID                  string `json:"job_id"`
	AttemptID              string `json:"attempt_id"`
	NodeLeaseID            string `json:"node_lease_id"`
	AttemptFence           uint64 `json:"attempt_fence"`
	NodeFence              uint64 `json:"node_fence"`
	ExpectedTargetRevision string `json:"expected_target_revision"`
}

func (fence RestoreFence) Validate() error {
	if backupasset.ValidateOpaqueID(fence.JobID) != nil || backupasset.ValidateOpaqueID(fence.AttemptID) != nil ||
		backupasset.ValidateOpaqueID(fence.NodeLeaseID) != nil || fence.AttemptFence == 0 || fence.NodeFence == 0 ||
		!validRestoreRevision(fence.ExpectedTargetRevision) {
		return invalidRestoreRequest("invalid restore fence")
	}
	return nil
}

// RestoreCheckpoint is a typed, independently verified target-chain fact.
// Process output is intentionally absent: an adapter cannot convert a command
// exit or stdout/stderr into a successful checkpoint.
type RestoreCheckpoint struct {
	ID                           string `json:"id"`
	OperationDigest              string `json:"operation_digest"`
	PriorTargetRevision          string `json:"prior_target_revision"`
	VerifiedTargetIdentityDigest string `json:"verified_target_identity_digest"`
	VerifiedTargetRevision       string `json:"verified_target_revision"`
	VerifiedBytes                int64  `json:"verified_bytes"`
	AttemptFence                 uint64 `json:"attempt_fence"`
	NodeFence                    uint64 `json:"node_fence"`
}

func (checkpoint RestoreCheckpoint) Validate() error {
	if backupasset.ValidateOpaqueID(checkpoint.ID) != nil || !validRestoreDigest(checkpoint.OperationDigest) ||
		!validRestoreRevision(checkpoint.PriorTargetRevision) || !validRestoreDigest(checkpoint.VerifiedTargetIdentityDigest) ||
		!validRestoreRevision(checkpoint.VerifiedTargetRevision) || checkpoint.VerifiedBytes < 0 || checkpoint.AttemptFence == 0 ||
		checkpoint.NodeFence == 0 {
		return invalidRestoreRequest("invalid restore checkpoint")
	}
	return nil
}

type RestoreEntry struct {
	AssetRef           backupasset.AssetRef         `json:"asset_ref"`
	Type               backupasset.CatalogEntryType `json:"type"`
	ExpectedSize       int64                        `json:"expected_size"`
	ExpectedDigest     string                       `json:"expected_digest"`
	TargetObjectDigest string                       `json:"target_object_digest"`
}

func (entry RestoreEntry) Validate(recoveryPointID string) error {
	if entry.validateShape(recoveryPointID) != nil || !validRestoreDigest(entry.ExpectedDigest) {
		return invalidRestoreRequest("invalid frozen restore entry")
	}
	return nil
}

func (entry RestoreEntry) validateShape(recoveryPointID string) error {
	if backupasset.ValidateAssetRef(entry.AssetRef) != nil || entry.AssetRef.RecoveryPointID != recoveryPointID ||
		(entry.Type != backupasset.CatalogEntryFile && entry.Type != backupasset.CatalogEntryDirectory) || entry.ExpectedSize < 0 ||
		!validRestoreDigest(entry.TargetObjectDigest) {
		return invalidRestoreRequest("invalid restore entry shape")
	}
	return nil
}

type RestoreLimits struct {
	MaxEntries    int   `json:"max_entries"`
	MaxBytes      int64 `json:"max_bytes"`
	MaxEntryBytes int64 `json:"max_entry_bytes"`
}

func (limits RestoreLimits) Validate() error {
	if limits.MaxEntries <= 0 || limits.MaxEntries > maxRestoreEntries || limits.MaxBytes <= 0 || limits.MaxEntryBytes <= 0 ||
		limits.MaxEntryBytes > limits.MaxBytes {
		return invalidRestoreRequest("invalid restore limits")
	}
	return nil
}

type RestoreConflictPolicy string

const (
	RestoreConflictFailOnConflict    RestoreConflictPolicy = "fail_on_conflict"
	RestoreConflictSkipExisting      RestoreConflictPolicy = "skip_existing"
	RestoreConflictOverwriteSelected RestoreConflictPolicy = "overwrite_selected"
	RestoreConflictExactMirror       RestoreConflictPolicy = "exact_mirror"
)

func (policy RestoreConflictPolicy) valid() bool {
	switch policy {
	case RestoreConflictFailOnConflict, RestoreConflictSkipExisting, RestoreConflictOverwriteSelected, RestoreConflictExactMirror:
		return true
	default:
		return false
	}
}

// RestoreRequest is a closed tagged union. The provider tag selects exactly
// one typed arm; there is no executor string, command field, credential field,
// arbitrary source path, or arbitrary target path escape hatch.
type RestoreRequest struct {
	Version        int                      `json:"version"`
	Provider       backupasset.ProviderKind `json:"provider"`
	Source         RestoreSource            `json:"source"`
	Entries        []RestoreEntry           `json:"entries"`
	Target         RestoreTarget            `json:"target"`
	Limits         RestoreLimits            `json:"limits"`
	ConflictPolicy RestoreConflictPolicy    `json:"conflict_policy"`
	Fence          RestoreFence             `json:"fence"`
	Checkpoint     RestoreCheckpoint        `json:"checkpoint"`
	MutationPermit TargetMutationPermit     `json:"-"`
	Rsync          *RsyncRestoreRequest     `json:"rsync,omitempty"`
	Restic         *ResticRestoreRequest    `json:"restic,omitempty"`
	Rclone         *RcloneRestoreRequest    `json:"rclone,omitempty"`
}

type RsyncRestoreRequest struct {
	ManifestDigest string                `json:"manifest_digest"`
	SourceRef      RsyncRestoreSourceRef `json:"source_ref"`
}

func (request RsyncRestoreRequest) Validate() error {
	if !validRestoreDigest(request.ManifestDigest) || request.SourceRef.Validate() != nil ||
		request.ManifestDigest != request.SourceRef.ManifestDigest {
		return invalidRestoreRequest("invalid Rsync restore request")
	}
	return nil
}

type ResticRestoreRequest struct {
	SnapshotID string   `json:"-"`
	Includes   []string `json:"-"`
}

func (request ResticRestoreRequest) Validate() error {
	if !validRestoreDigest(request.SnapshotID) || request.SnapshotID == "latest" || len(request.Includes) == 0 ||
		len(request.Includes) > maxRestoreIncludes {
		return invalidRestoreRequest("invalid Restic restore request")
	}
	for _, include := range request.Includes {
		if !validResticRestoreInclude(include) {
			return invalidRestoreRequest("invalid Restic restore include")
		}
	}
	return nil
}

type RcloneRestoreRequest struct {
	ManifestDigest        string `json:"manifest_digest"`
	CommittedPrefixDigest string `json:"committed_prefix_digest"`
}

func (request RcloneRestoreRequest) Validate() error {
	if !validRestoreDigest(request.ManifestDigest) || !validRestoreDigest(request.CommittedPrefixDigest) {
		return invalidRestoreRequest("invalid Rclone restore request")
	}
	return nil
}

func (request RestoreRequest) Validate() error {
	return request.ValidateAt(time.Now().UTC())
}

// ValidateIntent validates the immutable provider-neutral request arm without
// requiring a purpose-specific target permit. Repository-owned ports use it
// before resolving an Rsync scalar source for every phase.
func (request RestoreRequest) ValidateIntent() error {
	return request.validateIntent()
}

// ValidateRsyncResolutionIntent validates the closed caller-side facts that
// may reach Repository before its opaque source capability materializes
// per-entry content digests. Caller-supplied digests are rejected rather than
// trusted; Repository must replace these declarations with strict entries and
// then use the ordinary ValidateIntent contract.
func (request RestoreRequest) ValidateRsyncResolutionIntent() error {
	if err := request.validateCommonIntent(); err != nil {
		return err
	}
	if request.Provider != backupasset.ProviderRsync || !request.Source.empty() || request.Rsync == nil ||
		request.Restic != nil || request.Rclone != nil {
		return invalidRestoreRequest("invalid Rsync restore union")
	}
	if err := request.Rsync.Validate(); err != nil {
		return err
	}
	return validateRsyncResolutionEntries(request.Entries, request.Rsync.SourceRef.RecoveryPointID, request.Limits)
}

func (request RestoreRequest) ValidateAt(now time.Time) error {
	if err := request.validateIntent(); err != nil {
		return err
	}
	if err := request.MutationPermit.ValidateAt(now, request.Target, request.Fence); err != nil {
		return err
	}
	return nil
}

func (request RestoreRequest) validateIntent() error {
	if err := request.validateCommonIntent(); err != nil {
		return err
	}
	switch request.Provider {
	case backupasset.ProviderRsync:
		if !request.Source.empty() || request.Rsync == nil || request.Restic != nil || request.Rclone != nil {
			return invalidRestoreRequest("invalid Rsync restore union")
		}
		if err := request.Rsync.Validate(); err != nil {
			return err
		}
		return validateRestoreEntries(request.Entries, request.Rsync.SourceRef.RecoveryPointID, request.Limits)
	case backupasset.ProviderRestic:
		if request.Restic == nil || request.Rsync != nil || request.Rclone != nil {
			return invalidRestoreRequest("invalid Restic restore union")
		}
		if request.Source.Validate() != nil || request.Source.Provider != request.Provider {
			return invalidRestoreRequest("invalid Restic restore source")
		}
		if err := request.Restic.Validate(); err != nil {
			return err
		}
		return validateRestoreEntries(request.Entries, request.Source.RecoveryPointID, request.Limits)
	case backupasset.ProviderRclone:
		if request.Rclone == nil || request.Rsync != nil || request.Restic != nil {
			return invalidRestoreRequest("invalid Rclone restore union")
		}
		if request.Source.Validate() != nil || request.Source.Provider != request.Provider {
			return invalidRestoreRequest("invalid Rclone restore source")
		}
		if err := request.Rclone.Validate(); err != nil {
			return err
		}
		return validateRestoreEntries(request.Entries, request.Source.RecoveryPointID, request.Limits)
	default:
		return invalidRestoreRequest("unsupported restore provider")
	}
}

func (request RestoreRequest) validateCommonIntent() error {
	if request.Version != RestoreRequestSchemaV1 || request.Target.Validate() != nil || request.Limits.Validate() != nil || !request.ConflictPolicy.valid() ||
		request.Fence.Validate() != nil || request.Checkpoint.Validate() != nil ||
		request.Fence.ExpectedTargetRevision != request.Target.TargetRevision ||
		request.Checkpoint.PriorTargetRevision != request.Target.TargetRevision || request.Checkpoint.VerifiedTargetRevision != request.Target.TargetRevision ||
		request.Checkpoint.AttemptFence != request.Fence.AttemptFence || request.Checkpoint.NodeFence != request.Fence.NodeFence {
		return invalidRestoreRequest("invalid restore intent")
	}
	return nil
}

func validateRestoreEntries(entries []RestoreEntry, recoveryPointID string, limits RestoreLimits) error {
	return validateRestoreEntriesWithDigestMode(entries, recoveryPointID, limits, true)
}

func validateRsyncResolutionEntries(entries []RestoreEntry, recoveryPointID string, limits RestoreLimits) error {
	return validateRestoreEntriesWithDigestMode(entries, recoveryPointID, limits, false)
}

func validateRestoreEntriesWithDigestMode(entries []RestoreEntry, recoveryPointID string, limits RestoreLimits, requireDigest bool) error {
	if len(entries) == 0 || len(entries) > limits.MaxEntries {
		return invalidRestoreRequest("restore entries exceed bounds")
	}
	totalBytes := int64(0)
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.validateShape(recoveryPointID) != nil || entry.ExpectedSize > limits.MaxEntryBytes ||
			(requireDigest && !validRestoreDigest(entry.ExpectedDigest)) || (!requireDigest && entry.ExpectedDigest != "") {
			return invalidRestoreRequest("invalid restore entry")
		}
		key := entry.AssetRef.RecoveryPointID + "\x00" + entry.AssetRef.EntryID
		if _, exists := seen[key]; exists {
			return invalidRestoreRequest("duplicate restore entry")
		}
		seen[key] = struct{}{}
		if entry.ExpectedSize > limits.MaxBytes-totalBytes {
			return invalidRestoreRequest("restore entries exceed byte bound")
		}
		totalBytes += entry.ExpectedSize
	}
	return nil
}

// RestorePreflightRequest is read-only and intentionally accepts only a
// purpose-specific observation permit. It never accepts a mutation permit as
// a substitute for target observation.
type RestorePreflightRequest struct {
	Request RestoreRequest        `json:"request"`
	Permit  TargetPreflightPermit `json:"-"`
}

func (request RestorePreflightRequest) ValidateAt(now time.Time) error {
	if request.Request.validateIntent() != nil || request.Permit.ValidateAt(now, request.Request.Target) != nil {
		return invalidRestoreRequest("invalid restore preflight request")
	}
	return nil
}

type RestoreVerifyRequest struct {
	Request RestoreRequest     `json:"request"`
	Permit  TargetVerifyPermit `json:"-"`
}

func (request RestoreVerifyRequest) ValidateAt(now time.Time) error {
	if request.Request.validateIntent() != nil || request.Permit.ValidateAt(now, request.Request.Target) != nil {
		return invalidRestoreRequest("invalid restore verify request")
	}
	return nil
}

type RestoreReconcileRequest struct {
	Request RestoreRequest        `json:"request"`
	Permit  TargetReconcilePermit `json:"-"`
}

func (request RestoreReconcileRequest) ValidateAt(now time.Time) error {
	if request.Request.validateIntent() != nil || request.Permit.ValidateAt(now, request.Request.Target) != nil {
		return invalidRestoreRequest("invalid restore reconcile request")
	}
	return nil
}

type RestorePreflightEvidence struct {
	TargetBindingDigest string            `json:"target_binding_digest"`
	TargetRevision      string            `json:"target_revision"`
	Checkpoint          RestoreCheckpoint `json:"checkpoint"`
	Operations          []RestoreEvidence `json:"operations"`
}

func (evidence RestorePreflightEvidence) ValidateFor(request RestoreRequest) error {
	if evidence.TargetBindingDigest != request.Target.BindingDigest || evidence.TargetRevision != request.Target.TargetRevision ||
		evidence.Checkpoint != request.Checkpoint {
		return invalidRestoreRequest("invalid restore preflight evidence")
	}
	for _, operation := range evidence.Operations {
		if operation.Validate() != nil {
			return invalidRestoreRequest("invalid restore preflight operation evidence")
		}
	}
	return nil
}

type RestoreProgressStage string

const (
	RestoreProgressPrepared RestoreProgressStage = "prepared"
	RestoreProgressCopied   RestoreProgressStage = "copied"
	RestoreProgressVerified RestoreProgressStage = "verified"
)

type RestoreProgressEvent struct {
	EntryID        string               `json:"entry_id"`
	Stage          RestoreProgressStage `json:"stage"`
	CompletedBytes int64                `json:"completed_bytes"`
}

func (event RestoreProgressEvent) Validate() error {
	if len(event.EntryID) != 64 || !validRestoreDigest(event.EntryID) || event.CompletedBytes < 0 {
		return invalidRestoreRequest("invalid restore progress")
	}
	switch event.Stage {
	case RestoreProgressPrepared, RestoreProgressCopied, RestoreProgressVerified:
		return nil
	default:
		return invalidRestoreRequest("invalid restore progress stage")
	}
}

type RestoreProgress struct {
	Report func(RestoreProgressEvent) error `json:"-"`
}

func (progress RestoreProgress) Emit(event RestoreProgressEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if progress.Report == nil {
		return nil
	}
	return progress.Report(event)
}

// RestoreProcessEvidenceInput may contain raw process streams only while an
// adapter is deriving sanitized evidence. It is not an authorization or
// checkpoint input and cannot be serialized.
type RestoreProcessEvidenceInput struct {
	Stdout   []byte `json:"-"`
	Stderr   []byte `json:"-"`
	ExitCode int    `json:"-"`
}

type RestoreEvidence struct {
	OutputDigest string `json:"output_digest"`
	OutputBytes  int64  `json:"output_bytes"`
	ExitCode     int    `json:"exit_code"`
}

func (evidence RestoreEvidence) Validate() error {
	if !validRestoreDigest(evidence.OutputDigest) || evidence.OutputBytes < 0 {
		return invalidRestoreRequest("invalid restore evidence")
	}
	return nil
}

func NewRestoreEvidence(input RestoreProcessEvidenceInput) (RestoreEvidence, error) {
	if len(input.Stdout)+len(input.Stderr) > int(^uint(0)>>1) {
		return RestoreEvidence{}, invalidRestoreRequest("restore process evidence exceeds bounds")
	}
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang/backup-asset/restore-process-evidence/v1")
	writer.Int64(int64(input.ExitCode))
	writer.String(string(input.Stdout))
	writer.String(string(input.Stderr))
	digest, err := writer.HexDigest()
	if err != nil {
		return RestoreEvidence{}, fmt.Errorf("%w: restore process evidence", ErrInvalidRestoreRequest)
	}
	return RestoreEvidence{OutputDigest: digest, OutputBytes: int64(len(input.Stdout) + len(input.Stderr)), ExitCode: input.ExitCode}, nil
}

type RestoreResult struct {
	Checkpoint RestoreCheckpoint `json:"checkpoint"`
	Evidence   []RestoreEvidence `json:"evidence"`
}

func (result RestoreResult) ValidateFor(request RestoreRequest) error {
	if result.Checkpoint.Validate() != nil || result.Checkpoint.PriorTargetRevision != request.Target.TargetRevision ||
		result.Checkpoint.AttemptFence != request.Fence.AttemptFence || result.Checkpoint.NodeFence != request.Fence.NodeFence {
		return invalidRestoreRequest("invalid restore result checkpoint")
	}
	for _, evidence := range result.Evidence {
		if evidence.Validate() != nil {
			return invalidRestoreRequest("invalid restore result evidence")
		}
	}
	return nil
}

type RestoreVerifyResult struct {
	Checkpoint RestoreCheckpoint `json:"checkpoint"`
	Evidence   []RestoreEvidence `json:"evidence"`
}

func (result RestoreVerifyResult) ValidateFor(request RestoreRequest) error {
	return RestoreResult(result).ValidateFor(request)
}

type RestoreReconcileResult struct {
	Checkpoint RestoreCheckpoint `json:"checkpoint"`
	Evidence   []RestoreEvidence `json:"evidence"`
}

func (result RestoreReconcileResult) ValidateFor(request RestoreRequest) error {
	return RestoreResult(result).ValidateFor(request)
}

// DecodeRestoreRequest is intentionally strict. Restore inputs are not a
// generic command wire format: unknown fields, including executor, command,
// and credential extensions, are rejected before an adapter can run.
func DecodeRestoreRequest(payload []byte) (RestoreRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request RestoreRequest
	if err := decoder.Decode(&request); err != nil {
		return RestoreRequest{}, invalidRestoreRequest("decode restore request")
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return RestoreRequest{}, invalidRestoreRequest("trailing restore request data")
	}
	if err := request.Validate(); err != nil {
		return RestoreRequest{}, err
	}
	return request, nil
}

func ExecuteRestore(ctx context.Context, port RestorePort, request RestoreRequest, progress RestoreProgress) (RestoreResult, error) {
	if interfaceNil(port) {
		return RestoreResult{}, invalidRestoreRequest("restore port unavailable")
	}
	if err := validateRestoreExecutionPortRequest(request, time.Now().UTC()); err != nil {
		return RestoreResult{}, err
	}
	result, err := port.Execute(ctx, request, progress)
	if err != nil {
		return result, err
	}
	if err := result.ValidateFor(request); err != nil {
		return RestoreResult{}, err
	}
	return result, nil
}

func PreflightRestore(ctx context.Context, port RestorePort, request RestorePreflightRequest) (RestorePreflightEvidence, error) {
	if interfaceNil(port) {
		return RestorePreflightEvidence{}, invalidRestoreRequest("restore port unavailable")
	}
	if err := validateRestorePreflightPortRequest(request, time.Now().UTC()); err != nil {
		return RestorePreflightEvidence{}, err
	}
	evidence, err := port.Preflight(ctx, request)
	if err != nil {
		return evidence, err
	}
	if err := evidence.ValidateFor(request.Request); err != nil {
		return RestorePreflightEvidence{}, err
	}
	return evidence, nil
}

func VerifyRestore(ctx context.Context, port RestorePort, request RestoreVerifyRequest) (RestoreVerifyResult, error) {
	if interfaceNil(port) {
		return RestoreVerifyResult{}, invalidRestoreRequest("restore port unavailable")
	}
	if err := validateRestoreVerifyPortRequest(request, time.Now().UTC()); err != nil {
		return RestoreVerifyResult{}, err
	}
	result, err := port.Verify(ctx, request)
	if err != nil {
		return result, err
	}
	if err := result.ValidateFor(request.Request); err != nil {
		return RestoreVerifyResult{}, err
	}
	return result, nil
}

func ReconcileRestore(ctx context.Context, port RestorePort, request RestoreReconcileRequest) (RestoreReconcileResult, error) {
	if interfaceNil(port) {
		return RestoreReconcileResult{}, invalidRestoreRequest("restore port unavailable")
	}
	if err := validateRestoreReconcilePortRequest(request, time.Now().UTC()); err != nil {
		return RestoreReconcileResult{}, err
	}
	result, err := port.Reconcile(ctx, request)
	if err != nil {
		return result, err
	}
	if err := result.ValidateFor(request.Request); err != nil {
		return RestoreReconcileResult{}, err
	}
	return result, nil
}

func validateRestoreExecutionPortRequest(request RestoreRequest, now time.Time) error {
	if request.Provider != backupasset.ProviderRsync {
		return request.ValidateAt(now)
	}
	if err := request.ValidateAt(now); err == nil {
		return nil
	}
	if err := request.ValidateRsyncResolutionIntent(); err != nil {
		return err
	}
	return request.MutationPermit.ValidateAt(now, request.Target, request.Fence)
}

func validateRestorePreflightPortRequest(request RestorePreflightRequest, now time.Time) error {
	if request.Request.Provider != backupasset.ProviderRsync {
		return request.ValidateAt(now)
	}
	if err := request.ValidateAt(now); err == nil {
		return nil
	}
	if err := request.Request.ValidateRsyncResolutionIntent(); err != nil {
		return err
	}
	return request.Permit.ValidateAt(now, request.Request.Target)
}

func validateRestoreVerifyPortRequest(request RestoreVerifyRequest, now time.Time) error {
	if request.Request.Provider != backupasset.ProviderRsync {
		return request.ValidateAt(now)
	}
	if err := request.ValidateAt(now); err == nil {
		return nil
	}
	if err := request.Request.ValidateRsyncResolutionIntent(); err != nil {
		return err
	}
	return request.Permit.ValidateAt(now, request.Request.Target)
}

func validateRestoreReconcilePortRequest(request RestoreReconcileRequest, now time.Time) error {
	if request.Request.Provider != backupasset.ProviderRsync {
		return request.ValidateAt(now)
	}
	if err := request.ValidateAt(now); err == nil {
		return nil
	}
	if err := request.Request.ValidateRsyncResolutionIntent(); err != nil {
		return err
	}
	return request.Permit.ValidateAt(now, request.Request.Target)
}

func validRestoreProvider(provider backupasset.ProviderKind) bool {
	switch provider {
	case backupasset.ProviderRsync, backupasset.ProviderRestic, backupasset.ProviderRclone:
		return true
	default:
		return false
	}
}

func validRestoreDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validPrivateRestoreValue(value string) bool {
	return value != "" && len(value) <= maxRestorePrivateLength && strings.TrimSpace(value) != "" && !strings.ContainsRune(value, '\x00')
}

func validRestoreLabel(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func validRestoreRevision(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func validResticRestoreInclude(value string) bool {
	if !validPrivateRestoreValue(value) || !strings.HasPrefix(value, "/") || strings.Contains(value, "..") {
		return false
	}
	return !strings.ContainsAny(value, "*?[]{}$;|&`'\"\r\n")
}

func invalidRestoreRequest(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidRestoreRequest, message)
}
