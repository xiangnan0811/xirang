package recovery

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
)

const (
	sha256DigestLength = 64
	opaqueRevisionMax  = 64
	targetRootIDMax    = 32

	exactSelectionMaxItems            = 10_000
	recoveryOperationSnapshotMaxBytes = 16 << 20
	targetLocatorEnvelopeMaxBytes     = 16 << 10
	recoveryOperationMaxTotalBytes    = int64(1<<63 - 1)

	recoveryOperationSnapshotSchemaVersion = 2
	targetLocatorEnvelopeSchemaVersion     = 1
	targetLocatorCipherVersion             = 1

	sourceLocatorDigestDomain         = "xirang/recovery/source-locator/v1"
	sourceRevisionDigestDomain        = "xirang.backup_asset.recovery.source_revision.v1"
	privateSourceRevisionDomain       = "xirang.backup_asset.recovery.private_source_revision.v1"
	exactSelectionDigestDomain        = "xirang.backup_asset.recovery.exact_selection.v1"
	planIdempotencyDigestDomain       = "xirang.backup_asset.recovery.plan_idempotency.v1"
	planIntentDigestDomain            = "xirang.backup_asset.recovery.plan_intent.v1"
	operationSetDigestDomain          = "xirang/recovery/operation-set/v1"
	deleteSetDigestDomain             = "xirang/recovery/delete-set/v1"
	targetPathDigestDomain            = "xirang/recovery/target-path/v1"
	semanticTargetDigestDomain        = "xirang/recovery/semantic-target/v1"
	securityDecisionDigestDomain      = "xirang/recovery/security-decision/v1"
	securityOverrideCandidateDomain   = "xirang/recovery/security-override-candidate/v1"
	targetLocatorEnvelopeDigestDomain = "xirang/recovery/job-item-target-locator-envelope/v1"
	targetLocatorAEADDomain           = "xirang/recovery/job-item-target-locator/aead/v1"
	targetLocatorCiphertextPrefix     = "recovery:aead:v1:"

	// EmptyDeleteSetDigest is the SHA-256 digest of the raw delete-set domain
	// followed by its canonical big-endian uint32 zero-row count.
	EmptyDeleteSetDigest = "3f5a5d5213612b170da6ce2f2f90775a31d4e40269bb785042589af64011b7cf"
)

var (
	ErrInvalidSourceRevision         = errors.New("invalid recovery source revision")
	ErrInvalidTargetBinding          = errors.New("invalid recovery target binding")
	ErrInvalidPlanBinding            = errors.New("invalid recovery plan binding")
	ErrInvalidRecoveryPlan           = errors.New("invalid recovery plan")
	ErrInvalidAuthority              = errors.New("invalid recovery authority category")
	ErrInvalidSecurityDecision       = errors.New("invalid recovery security decision")
	ErrInvalidPreflightBinding       = errors.New("invalid recovery preflight binding")
	ErrInvalidAuthorityBinding       = errors.New("invalid recovery authority binding")
	ErrInvalidDeleteAuthorityBinding = errors.New("invalid exact-mirror delete authority binding")
	ErrInvalidFrozenJobBinding       = errors.New("invalid frozen recovery job binding")
	ErrInvalidCheckpointBinding      = errors.New("invalid frozen recovery checkpoint binding")
	ErrInvalidResultPublication      = errors.New("invalid recovery result publication")
	ErrInvalidResultContent          = errors.New("invalid recovery result content binding")
	ErrInvalidExactSelection         = errors.New("invalid exact recovery selection")
	ErrExactSelectionLimit           = errors.New("exact recovery selection limit exceeded")
	ErrRecoverySourceUnavailable     = errors.New("recovery source is unavailable")
	ErrRecoverySourceChanged         = errors.New("recovery source changed")
	ErrInvalidPlanIdempotency        = errors.New("invalid recovery plan idempotency")
	ErrPlanIdempotencyConflict       = errors.New("recovery plan idempotency conflict")
	ErrRecoveryPlanUnavailable       = errors.New("recovery plan is unavailable")
	ErrInvalidRecoveryOperation      = errors.New("invalid recovery operation")
	ErrRecoveryOperationLimit        = errors.New("recovery operation limit exceeded")
	ErrRecoveryImpactLimit           = errors.New("recovery impact limit exceeded")
	ErrRecoveryPreflightConflict     = errors.New("recovery preflight binding conflict")
	ErrInvalidTargetVerification     = errors.New("invalid recovery target verification")
	ErrInvalidTargetLocatorEnvelope  = errors.New("invalid recovery target locator envelope")
)

type RecoveryReconciliationCategory string

const (
	RecoveryReconciliationKnownHealthy    RecoveryReconciliationCategory = "known_healthy"
	RecoveryReconciliationKnownDrift      RecoveryReconciliationCategory = "known_drift"
	RecoveryReconciliationDBUnmatched     RecoveryReconciliationCategory = "db_unmatched"
	RecoveryReconciliationForgedOrUnknown RecoveryReconciliationCategory = "forged_or_unknown"
	RecoveryReconciliationScanIncomplete  RecoveryReconciliationCategory = "scan_incomplete"
)

type RecoveryReconciliationState string

const (
	RecoveryReconciliationClear   RecoveryReconciliationState = "clear"
	RecoveryReconciliationBlocked RecoveryReconciliationState = "blocked"
)

type RecoveryReconciliationFinding struct {
	Category    RecoveryReconciliationCategory `json:"category"`
	Fingerprint string                         `json:"fingerprint"`
	EntryKind   TargetEntryKind                `json:"entry_kind"`
	JobID       string                         `json:"job_id,omitempty"`
}

type RecoveryReconciliationCounts struct {
	Scanned         int `json:"scanned"`
	KnownHealthy    int `json:"known_healthy"`
	KnownDrift      int `json:"known_drift"`
	DBUnmatched     int `json:"db_unmatched"`
	ForgedOrUnknown int `json:"forged_or_unknown"`
	ScanIncomplete  int `json:"scan_incomplete"`
}

type RecoveryReconciliationResult struct {
	State      RecoveryReconciliationState     `json:"state"`
	Complete   bool                            `json:"complete"`
	NextCursor string                          `json:"next_cursor,omitempty"`
	Counts     RecoveryReconciliationCounts    `json:"counts"`
	Findings   []RecoveryReconciliationFinding `json:"findings"`
}

func (RecoveryReconciliationResult) String() string {
	return redactedRecoveryTargetProduct("RecoveryReconciliationResult")
}

func (RecoveryReconciliationResult) GoString() string {
	return redactedRecoveryTargetProduct("RecoveryReconciliationResult")
}

type ReconcileRecoveryRootRequest struct {
	NodeID uint
	RootID string
	Cursor string `json:"-"`
}

type RecoveryDowngradeReconciliationRequest struct {
	AdmissionGeneration string `json:"-"`
}

type SourceRevisionKind string

const (
	SourceRevisionImmutable   SourceRevisionKind = "immutable"
	SourceRevisionObservation SourceRevisionKind = "observation"
)

type ObservationRevision struct {
	SourceFingerprint   string    `json:"source_fingerprint"`
	CatalogGenerationID string    `json:"catalog_generation_id"`
	ObservedAt          time.Time `json:"observed_at"`
}

type ImmutableSourceRevision struct {
	// LocatorDigest is a private source binding. It must never be serialized
	// into an authority, plan response, audit payload, or log-facing DTO.
	LocatorDigest  string `json:"-"`
	ManifestDigest string `json:"manifest_digest"`
}

// SourceRevision is a closed union. Exactly one arm must be set.
type SourceRevision struct {
	Kind               SourceRevisionKind       `json:"kind"`
	Immutable          *ImmutableSourceRevision `json:"immutable,omitempty"`
	MutableObservation *ObservationRevision     `json:"mutable_observation,omitempty"`
}

// frozenSourceBinding is a private companion to the public source-revision
// union. Mutable observations deliberately expose only their observation
// tuple, so the recovery boundary retains the Provider + exact-locator digest
// here to reject substitution before a consumer receives any locator. It is
// intentionally unexported and never participates in SelectionDigest or JSON.
type frozenSourceBinding struct {
	Provider      backupasset.ProviderKind
	LocatorDigest string
}

func (binding frozenSourceBinding) valid() bool {
	return validRecoveryProvider(binding.Provider) && validDigest(binding.LocatorDigest)
}

func (revision SourceRevision) Validate() error {
	hasImmutable := revision.Immutable != nil
	hasObservation := revision.MutableObservation != nil
	if hasImmutable == hasObservation {
		return ErrInvalidSourceRevision
	}

	switch revision.Kind {
	case SourceRevisionImmutable:
		if !hasImmutable || !validDigest(revision.Immutable.LocatorDigest) || !validDigest(revision.Immutable.ManifestDigest) {
			return ErrInvalidSourceRevision
		}
	case SourceRevisionObservation:
		if !hasObservation || !validDigest(revision.MutableObservation.SourceFingerprint) ||
			!validOpaqueID(revision.MutableObservation.CatalogGenerationID) || revision.MutableObservation.ObservedAt.IsZero() {
			return ErrInvalidSourceRevision
		}
	default:
		return ErrInvalidSourceRevision
	}

	return nil
}

// ExactSelectionInput is the fully explicit, single-source input accepted at
// the recovery boundary. Directory expansion happens before this contract is
// created, so AssetRefs always describes the exact non-directory entries that
// will be recovered.
type ExactSelectionInput struct {
	RepositoryID        string
	RecoveryPointID     string
	CatalogGenerationID string
	AssetRefs           []backupasset.AssetRef
	SourceRevision      SourceRevision
}

// ExactSelection is an immutable source-selection product. SourceRevision is
// deliberately excluded from JSON because an immutable revision contains a
// private locator-derived binding. Use Authority for safe outward-facing
// identity data instead.
type ExactSelection struct {
	RepositoryID         string                 `json:"repository_id"`
	RecoveryPointID      string                 `json:"recovery_point_id"`
	CatalogGenerationID  string                 `json:"catalog_generation_id"`
	AssetRefs            []backupasset.AssetRef `json:"asset_refs"`
	SourceRevision       SourceRevision         `json:"-"`
	SelectionDigest      string                 `json:"selection_digest"`
	SourceRevisionDigest string                 `json:"-"`

	privateSourceBinding *frozenSourceBinding
}

// ExactSelectionAuthority is the only portable authority projection of an
// exact selection. It contains stable opaque identifiers and digests, but not
// the locator or its locator-specific digest.
type ExactSelectionAuthority struct {
	RepositoryID        string `json:"repository_id"`
	RecoveryPointID     string `json:"recovery_point_id"`
	CatalogGenerationID string `json:"catalog_generation_id"`
	SelectionDigest     string `json:"selection_digest"`
}

// NewExactSelection canonicalizes explicit AssetRefs and freezes the stable
// source-revision and selection digests. It accepts exactly one Repository,
// RecoveryPoint, and Catalog generation.
func NewExactSelection(input ExactSelectionInput) (ExactSelection, error) {
	return newExactSelection(input, nil)
}

func newExactSelectionWithPrivateSourceBinding(
	input ExactSelectionInput,
	binding frozenSourceBinding,
) (ExactSelection, error) {
	return newExactSelection(input, &binding)
}

func newExactSelection(input ExactSelectionInput, privateBinding *frozenSourceBinding) (ExactSelection, error) {
	if !validOpaqueID(input.RepositoryID) || !validOpaqueID(input.RecoveryPointID) ||
		!validOpaqueID(input.CatalogGenerationID) || input.SourceRevision.Validate() != nil {
		return ExactSelection{}, ErrInvalidExactSelection
	}
	if privateBinding != nil && !privateBinding.valid() {
		return ExactSelection{}, ErrInvalidExactSelection
	}
	if observation := input.SourceRevision.MutableObservation; observation != nil &&
		observation.CatalogGenerationID != input.CatalogGenerationID {
		return ExactSelection{}, ErrInvalidExactSelection
	}

	refs, err := canonicalExactAssetRefs(input.RecoveryPointID, input.AssetRefs)
	if err != nil {
		return ExactSelection{}, err
	}
	sourceRevision := cloneSourceRevision(input.SourceRevision)
	sourceRevisionDigest, err := privateSourceRevisionDigest(sourceRevision, privateBinding)
	if err != nil {
		return ExactSelection{}, err
	}
	selectionDigest := exactSelectionDigest(input.RepositoryID, input.RecoveryPointID, input.CatalogGenerationID, refs)
	return ExactSelection{
		RepositoryID:         input.RepositoryID,
		RecoveryPointID:      input.RecoveryPointID,
		CatalogGenerationID:  input.CatalogGenerationID,
		AssetRefs:            refs,
		SourceRevision:       sourceRevision,
		SelectionDigest:      selectionDigest,
		SourceRevisionDigest: sourceRevisionDigest,
		privateSourceBinding: cloneFrozenSourceBinding(privateBinding),
	}, nil
}

// Validate proves that the selection remains a canonical, untampered product.
func (selection ExactSelection) Validate() error {
	rebuilt, err := newExactSelection(ExactSelectionInput{
		RepositoryID:        selection.RepositoryID,
		RecoveryPointID:     selection.RecoveryPointID,
		CatalogGenerationID: selection.CatalogGenerationID,
		AssetRefs:           selection.AssetRefs,
		SourceRevision:      selection.SourceRevision,
	}, selection.privateSourceBinding)
	if err != nil || selection.SelectionDigest != rebuilt.SelectionDigest ||
		selection.SourceRevisionDigest != rebuilt.SourceRevisionDigest ||
		!sameAssetRefs(selection.AssetRefs, rebuilt.AssetRefs) ||
		!sameSourceRevision(selection.SourceRevision, rebuilt.SourceRevision) ||
		!sameFrozenSourceBinding(selection.privateSourceBinding, rebuilt.privateSourceBinding) {
		return ErrInvalidExactSelection
	}
	return nil
}

func (selection ExactSelection) hasPrivateSourceBinding() bool {
	return selection.privateSourceBinding != nil && selection.privateSourceBinding.valid()
}

// Authority returns the safe, opaque authority projection. A malformed
// selection intentionally has no authority projection.
func (selection ExactSelection) Authority() ExactSelectionAuthority {
	if selection.Validate() != nil {
		return ExactSelectionAuthority{}
	}
	return ExactSelectionAuthority{
		RepositoryID:        selection.RepositoryID,
		RecoveryPointID:     selection.RecoveryPointID,
		CatalogGenerationID: selection.CatalogGenerationID,
		SelectionDigest:     selection.SelectionDigest,
	}
}

// SourceLocatorDigest derives a domain-separated private locator binding. The
// digest is useful only inside trusted source boundaries and must not be sent
// over JSON or copied into an authority projection.
func SourceLocatorDigest(repositoryID string, provider backupasset.ProviderKind, recoveryPointID, locator string) (string, error) {
	digest, err := publication.ImmutableLocatorDigest(repositoryID, provider, recoveryPointID, locator)
	if err != nil {
		return "", ErrInvalidExactSelection
	}
	return digest, nil
}

// SourceRevisionDigest derives a stable digest for the closed source-revision
// union without exposing its private members.
func SourceRevisionDigest(revision SourceRevision) (string, error) {
	if revision.Validate() != nil {
		return "", ErrInvalidSourceRevision
	}
	switch revision.Kind {
	case SourceRevisionImmutable:
		return framedDigest(
			sourceRevisionDigestDomain, string(revision.Kind), revision.Immutable.LocatorDigest, revision.Immutable.ManifestDigest,
		), nil
	case SourceRevisionObservation:
		return framedDigest(
			sourceRevisionDigestDomain, string(revision.Kind), revision.MutableObservation.SourceFingerprint,
			revision.MutableObservation.CatalogGenerationID, revision.MutableObservation.ObservedAt.UTC().Format(time.RFC3339Nano),
		), nil
	default:
		return "", ErrInvalidSourceRevision
	}
}

func privateSourceRevisionDigest(revision SourceRevision, binding *frozenSourceBinding) (string, error) {
	revisionDigest, err := SourceRevisionDigest(revision)
	if err != nil || binding == nil {
		return revisionDigest, err
	}
	if !binding.valid() {
		return "", ErrInvalidSourceRevision
	}
	return framedDigest(
		privateSourceRevisionDomain,
		revisionDigest,
		string(binding.Provider),
		binding.LocatorDigest,
	), nil
}

func canonicalExactAssetRefs(recoveryPointID string, refs []backupasset.AssetRef) ([]backupasset.AssetRef, error) {
	if len(refs) == 0 || len(refs) > exactSelectionMaxItems {
		return nil, ErrInvalidExactSelection
	}
	unique := make(map[string]backupasset.AssetRef, len(refs))
	for _, ref := range refs {
		if backupasset.ValidateAssetRef(ref) != nil || ref.RecoveryPointID != recoveryPointID {
			return nil, ErrInvalidExactSelection
		}
		if _, exists := unique[ref.EntryID]; exists {
			return nil, ErrInvalidExactSelection
		}
		unique[ref.EntryID] = backupasset.AssetRef{RecoveryPointID: recoveryPointID, EntryID: ref.EntryID}
	}
	ordered := make([]backupasset.AssetRef, 0, len(unique))
	for _, ref := range unique {
		ordered = append(ordered, ref)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].RecoveryPointID != ordered[right].RecoveryPointID {
			return ordered[left].RecoveryPointID < ordered[right].RecoveryPointID
		}
		return ordered[left].EntryID < ordered[right].EntryID
	})
	return ordered, nil
}

func exactSelectionDigest(repositoryID, recoveryPointID, catalogGenerationID string, refs []backupasset.AssetRef) string {
	buffer := bytes.NewBuffer(nil)
	writeRecoveryDigestString(buffer, exactSelectionDigestDomain)
	writeRecoveryDigestString(buffer, repositoryID)
	writeRecoveryDigestString(buffer, recoveryPointID)
	writeRecoveryDigestString(buffer, catalogGenerationID)
	writeRecoveryDigestUint64(buffer, uint64(len(refs)))
	for _, ref := range refs {
		writeRecoveryDigestString(buffer, ref.RecoveryPointID)
		writeRecoveryDigestString(buffer, ref.EntryID)
	}
	sum := sha256.Sum256(buffer.Bytes())
	return hex.EncodeToString(sum[:])
}

func framedDigest(domain string, values ...string) string {
	buffer := bytes.NewBuffer(nil)
	writeRecoveryDigestString(buffer, domain)
	writeRecoveryDigestUint64(buffer, uint64(len(values)))
	for _, value := range values {
		writeRecoveryDigestString(buffer, value)
	}
	sum := sha256.Sum256(buffer.Bytes())
	return hex.EncodeToString(sum[:])
}

func writeRecoveryDigestString(buffer *bytes.Buffer, value string) {
	writeRecoveryDigestUint64(buffer, uint64(len(value)))
	buffer.WriteString(value)
}

func writeRecoveryDigestUint64(buffer *bytes.Buffer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	buffer.Write(encoded[:])
}

func writeRecoveryDigestInt64(buffer *bytes.Buffer, value int64) {
	writeRecoveryDigestUint64(buffer, uint64(value))
}

func cloneSourceRevision(revision SourceRevision) SourceRevision {
	result := SourceRevision{Kind: revision.Kind}
	if revision.Immutable != nil {
		value := *revision.Immutable
		result.Immutable = &value
	}
	if revision.MutableObservation != nil {
		value := *revision.MutableObservation
		result.MutableObservation = &value
	}
	return result
}

func cloneFrozenSourceBinding(binding *frozenSourceBinding) *frozenSourceBinding {
	if binding == nil {
		return nil
	}
	value := *binding
	return &value
}

func sameSourceRevision(left, right SourceRevision) bool {
	if left.Kind != right.Kind || (left.Immutable == nil) != (right.Immutable == nil) ||
		(left.MutableObservation == nil) != (right.MutableObservation == nil) {
		return false
	}
	if left.Immutable != nil && *left.Immutable != *right.Immutable {
		return false
	}
	return left.MutableObservation == nil ||
		left.MutableObservation.SourceFingerprint == right.MutableObservation.SourceFingerprint &&
			left.MutableObservation.CatalogGenerationID == right.MutableObservation.CatalogGenerationID &&
			left.MutableObservation.ObservedAt.Equal(right.MutableObservation.ObservedAt)
}

func sameFrozenSourceBinding(left, right *frozenSourceBinding) bool {
	if (left == nil) != (right == nil) {
		return false
	}
	return left == nil || *left == *right
}

func sameAssetRefs(left, right []backupasset.AssetRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validRecoveryProvider(provider backupasset.ProviderKind) bool {
	switch provider {
	case backupasset.ProviderRestic, backupasset.ProviderRsync, backupasset.ProviderRclone, backupasset.ProviderVerifiedImport:
		return true
	default:
		return false
	}
}

func validPrivateLocator(locator string) bool {
	return len(locator) > 0 && len(locator) <= 4096 && strings.TrimSpace(locator) != "" && !strings.ContainsRune(locator, '\x00')
}

func validTargetRelativeLocator(locator string) bool {
	if len(locator) == 0 || len(locator) > 4096 || !utf8.ValidString(locator) ||
		strings.ContainsRune(locator, '\x00') || strings.Contains(locator, `\`) ||
		strings.HasPrefix(locator, "/") || strings.HasSuffix(locator, "/") || hasWindowsVolumePrefix(locator) {
		return false
	}
	for _, component := range strings.Split(locator, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func hasWindowsVolumePrefix(locator string) bool {
	if len(locator) < 2 || locator[1] != ':' {
		return false
	}
	first := locator[0]
	return (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')
}

// TargetPathDigest derives the target object identity from the trusted root
// binding and an already-canonical relative locator.
func TargetPathDigest(rootID, rootLocatorDigest, relativeLocator string) (string, error) {
	if !validBoundedOpaque(rootID, targetRootIDMax) || !validDigest(rootLocatorDigest) ||
		!validTargetRelativeLocator(relativeLocator) {
		return "", ErrInvalidTargetBinding
	}
	return framedDigest(targetPathDigestDomain, rootID, rootLocatorDigest, relativeLocator), nil
}

// SemanticTargetDigest binds a canonical item locator to the exact target
// namespace without conflating it with the final root-relative object. For an
// isolated recovery the item locator remains workspace-relative here.
func SemanticTargetDigest(
	mode TargetMode,
	rootID,
	rootLocatorDigest,
	relativeLocator string,
) (string, error) {
	if mode.Validate() != nil || !validBoundedOpaque(rootID, targetRootIDMax) ||
		!validDigest(rootLocatorDigest) || !validTargetRelativeLocator(relativeLocator) {
		return "", ErrInvalidTargetBinding
	}
	return framedDigest(semanticTargetDigestDomain, string(mode), rootID, rootLocatorDigest, relativeLocator), nil
}

// TargetObjectDigest is the final root-relative object binding carried by a
// TargetObjectRef. It intentionally retains the established target-path digest
// domain while giving persistence a distinct field from SemanticTargetDigest.
func TargetObjectDigest(rootID, rootLocatorDigest, relativeLocator string) (string, error) {
	return TargetPathDigest(rootID, rootLocatorDigest, relativeLocator)
}

type TargetMode string

const (
	TargetModeIsolated TargetMode = "isolated"
	TargetModeInPlace  TargetMode = "in_place"
)

func (mode TargetMode) Validate() error {
	if mode != TargetModeIsolated && mode != TargetModeInPlace {
		return ErrInvalidTargetBinding
	}
	return nil
}

type TargetBinding struct {
	Mode                    TargetMode
	NodeID                  uint
	RootID                  string
	EncryptedRelativePath   string `json:"-"`
	RootLocatorDigest       string `json:"-"`
	PathDigest              string `json:"-"`
	BaseNodeRevision        string
	CredentialScopeRevision string
	RootRevision            string
	FilesystemRevision      string
}

func (binding TargetBinding) Validate() error {
	pathDigest, err := TargetPathDigest(binding.RootID, binding.RootLocatorDigest, binding.EncryptedRelativePath)
	if binding.Mode.Validate() != nil || binding.NodeID == 0 || !validBoundedOpaque(binding.RootID, targetRootIDMax) ||
		err != nil || binding.PathDigest != pathDigest ||
		!validOpaqueRevision(binding.BaseNodeRevision) || !validOpaqueRevision(binding.CredentialScopeRevision) ||
		!validOpaqueRevision(binding.RootRevision) || !validOpaqueRevision(binding.FilesystemRevision) {
		return ErrInvalidTargetBinding
	}
	return nil
}

type ConflictPolicy string

const (
	ConflictFailOnConflict    ConflictPolicy = "fail_on_conflict"
	ConflictSkipExisting      ConflictPolicy = "skip_existing"
	ConflictOverwriteSelected ConflictPolicy = "overwrite_selected"
	ConflictExactMirror       ConflictPolicy = "exact_mirror"
)

func (policy ConflictPolicy) Validate() error {
	switch policy {
	case ConflictFailOnConflict, ConflictSkipExisting, ConflictOverwriteSelected, ConflictExactMirror:
		return nil
	default:
		return ErrInvalidPlanBinding
	}
}

type RecoveryOperationKind string

const (
	RecoveryOperationCreate    RecoveryOperationKind = "create"
	RecoveryOperationOverwrite RecoveryOperationKind = "overwrite"
	RecoveryOperationSkip      RecoveryOperationKind = "skip"
	RecoveryOperationDelete    RecoveryOperationKind = "delete"
)

type ExpectedTargetIdentityKind string

const (
	ExpectedTargetAbsent  ExpectedTargetIdentityKind = "absent"
	ExpectedTargetPresent ExpectedTargetIdentityKind = "present"
)

type ExpectedTargetIdentity struct {
	Kind   ExpectedTargetIdentityKind
	Digest string
}

func (identity ExpectedTargetIdentity) valid() bool {
	switch identity.Kind {
	case ExpectedTargetAbsent:
		return identity.Digest == ""
	case ExpectedTargetPresent:
		return validDigest(identity.Digest)
	default:
		return false
	}
}

// TargetPresenceKind is closed so an ambiguous target lookup cannot be
// represented as a successful absence observation.
type TargetPresenceKind string

const (
	TargetPresencePresent TargetPresenceKind = "present"
	TargetPresenceAbsent  TargetPresenceKind = "absent"
)

type PresentExpectation struct {
	IdentityDigest string
	Bytes          int64
}

func (expectation PresentExpectation) valid() bool {
	return validDigest(expectation.IdentityDigest) && expectation.Bytes >= 0
}

// AbsentExpectation has no synthetic identity digest. Its presence as the
// single closed-union arm is the explicit request for an absence proof.
type AbsentExpectation struct{}

type TargetVerifyExpectation struct {
	Kind    TargetPresenceKind
	Present *PresentExpectation
	Absent  *AbsentExpectation
}

func (expectation TargetVerifyExpectation) Validate() error {
	hasPresent := expectation.Present != nil
	hasAbsent := expectation.Absent != nil
	if hasPresent == hasAbsent {
		return ErrInvalidTargetVerification
	}
	switch expectation.Kind {
	case TargetPresencePresent:
		if !hasPresent || !expectation.Present.valid() {
			return ErrInvalidTargetVerification
		}
	case TargetPresenceAbsent:
		if !hasAbsent {
			return ErrInvalidTargetVerification
		}
	default:
		return ErrInvalidTargetVerification
	}
	return nil
}

type PresentObservation struct {
	IdentityDigest string
	Bytes          int64
}

func (observation PresentObservation) valid() bool {
	return validDigest(observation.IdentityDigest) && observation.Bytes >= 0
}

type TargetAbsenceEvidenceKind string

const TargetAbsenceEvidenceExact TargetAbsenceEvidenceKind = "exact"

// AbsentObservation carries explicit absence evidence only. Permission,
// timeout, and ambiguous missing conditions remain errors rather than arms.
type AbsentObservation struct {
	Evidence TargetAbsenceEvidenceKind
}

func (observation AbsentObservation) valid() bool {
	return observation.Evidence == TargetAbsenceEvidenceExact
}

type TargetVerifyObservation struct {
	Kind             TargetPresenceKind
	Present          *PresentObservation
	Absent           *AbsentObservation
	ObservedRevision string
}

func (observation TargetVerifyObservation) Validate() error {
	hasPresent := observation.Present != nil
	hasAbsent := observation.Absent != nil
	if !validOpaqueRevision(observation.ObservedRevision) || sha256Shaped(observation.ObservedRevision) ||
		hasPresent == hasAbsent {
		return ErrInvalidTargetVerification
	}
	switch observation.Kind {
	case TargetPresencePresent:
		if !hasPresent || !observation.Present.valid() {
			return ErrInvalidTargetVerification
		}
	case TargetPresenceAbsent:
		if !hasAbsent || !observation.Absent.valid() {
			return ErrInvalidTargetVerification
		}
	default:
		return ErrInvalidTargetVerification
	}
	return nil
}

func (observation TargetVerifyObservation) ValidateAgainst(expectation TargetVerifyExpectation) error {
	if expectation.Validate() != nil || observation.Validate() != nil || observation.Kind != expectation.Kind {
		return ErrInvalidTargetVerification
	}
	if observation.Kind == TargetPresencePresent &&
		(observation.Present.IdentityDigest != expectation.Present.IdentityDigest ||
			observation.Present.Bytes != expectation.Present.Bytes) {
		return ErrInvalidTargetVerification
	}
	return nil
}

type RecoveryOperationSourceKind string

const (
	RecoveryOperationSourceNone     RecoveryOperationSourceKind = "none"
	RecoveryOperationSourceAssetRef RecoveryOperationSourceKind = "asset_ref"
)

type RecoveryOperationSource struct {
	Kind     RecoveryOperationSourceKind
	AssetRef *backupasset.AssetRef
}

func (source RecoveryOperationSource) valid(expectAsset bool) bool {
	if expectAsset {
		return source.Kind == RecoveryOperationSourceAssetRef && source.AssetRef != nil &&
			backupasset.ValidateAssetRef(*source.AssetRef) == nil
	}
	return source.Kind == RecoveryOperationSourceNone && source.AssetRef == nil
}

type RecoveryDisplayClass string

const (
	RecoveryDisplayClassRegular   RecoveryDisplayClass = "regular"
	RecoveryDisplayClassDirectory RecoveryDisplayClass = "directory"
	RecoveryDisplayClassLink      RecoveryDisplayClass = "link"
	RecoveryDisplayClassSpecial   RecoveryDisplayClass = "special"
)

func (class RecoveryDisplayClass) valid() bool {
	switch class {
	case RecoveryDisplayClassRegular, RecoveryDisplayClassDirectory, RecoveryDisplayClassLink, RecoveryDisplayClassSpecial:
		return true
	default:
		return false
	}
}

type RecoveryOperation struct {
	Kind                       RecoveryOperationKind
	TargetPathDigest           string
	TargetRelativeLocator      string
	SemanticTargetDigest       string
	ExpectedPrior              ExpectedTargetIdentity
	ExpectedPostIdentityDigest string
	ExpectedPostBytes          int64
	ExpectedPriorBytes         int64
	Source                     RecoveryOperationSource
	DisplayClass               RecoveryDisplayClass
	EstimatedBytes             int64
	snapshotTargetMode         TargetMode
	snapshotConflictPolicy     ConflictPolicy
}

func (operation RecoveryOperation) validExpectedPostFacts() bool {
	switch operation.Kind {
	case RecoveryOperationCreate:
		return validDigest(operation.ExpectedPostIdentityDigest) &&
			operation.ExpectedPostBytes >= 0 && operation.ExpectedPriorBytes == -1
	case RecoveryOperationOverwrite:
		return validDigest(operation.ExpectedPostIdentityDigest) &&
			operation.ExpectedPostBytes >= 0 && operation.ExpectedPriorBytes >= 0
	case RecoveryOperationSkip:
		return operation.ExpectedPostIdentityDigest == operation.ExpectedPrior.Digest &&
			operation.ExpectedPostBytes == -1 && operation.ExpectedPriorBytes >= 0
	case RecoveryOperationDelete:
		return operation.ExpectedPostIdentityDigest == "" &&
			operation.ExpectedPostBytes == -1 && operation.ExpectedPriorBytes == -1
	default:
		return false
	}
}

type RecoveryOperationLimits struct {
	MaxRows       int
	MaxItems      int
	MaxBytes      int64
	MaxImpactRows int
}

type RecoveryImpactRow struct {
	Kind             RecoveryOperationKind
	TargetPathDigest string
	DisplayClass     RecoveryDisplayClass
}

type RecoveryImpactSummary struct {
	CreateCount    int64
	OverwriteCount int64
	SkipCount      int64
	DeleteCount    int64
	EstimatedItems int64
	EstimatedBytes int64
	Rows           []RecoveryImpactRow
}

type RecoveryOperationProductsInput struct {
	TargetMode     TargetMode
	ConflictPolicy ConflictPolicy
	Operations     []RecoveryOperation
	Limits         RecoveryOperationLimits
}

type RecoveryOperationProducts struct {
	Rows               []RecoveryOperation
	OperationSetDigest string
	DeleteSetDigest    string
	Impact             RecoveryImpactSummary
}

func NewOperationProducts(input RecoveryOperationProductsInput) (RecoveryOperationProducts, error) {
	if input.TargetMode.Validate() != nil || input.ConflictPolicy.Validate() != nil ||
		input.Limits.MaxRows <= 0 || input.Limits.MaxItems <= 0 || input.Limits.MaxBytes < 0 ||
		input.Limits.MaxImpactRows <= 0 || len(input.Operations) == 0 {
		return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
	}
	if input.ConflictPolicy == ConflictExactMirror && input.TargetMode != TargetModeInPlace {
		return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
	}
	if len(input.Operations) > input.Limits.MaxRows || len(input.Operations) > input.Limits.MaxItems {
		return RecoveryOperationProducts{}, ErrRecoveryOperationLimit
	}
	if len(input.Operations) > input.Limits.MaxImpactRows {
		return RecoveryOperationProducts{}, ErrRecoveryImpactLimit
	}

	rows := make([]RecoveryOperation, len(input.Operations))
	targets := make(map[string]struct{}, len(input.Operations))
	locators := make(map[string]struct{}, len(input.Operations))
	semanticTargets := make(map[string]struct{}, len(input.Operations))
	sources := make(map[string]struct{}, len(input.Operations))
	impact := RecoveryImpactSummary{Rows: make([]RecoveryImpactRow, 0, len(input.Operations))}
	for index, operation := range input.Operations {
		if (operation.snapshotTargetMode != "" && operation.snapshotTargetMode != input.TargetMode) ||
			(operation.snapshotConflictPolicy != "" && operation.snapshotConflictPolicy != input.ConflictPolicy) {
			return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
		}
		if !validDigest(operation.TargetPathDigest) || !validTargetRelativeLocator(operation.TargetRelativeLocator) ||
			!validDigest(operation.SemanticTargetDigest) || !operation.ExpectedPrior.valid() ||
			!operation.DisplayClass.valid() || operation.EstimatedBytes < 0 {
			return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
		}
		if _, duplicate := targets[operation.TargetPathDigest]; duplicate {
			return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
		}
		targets[operation.TargetPathDigest] = struct{}{}
		if _, duplicate := locators[operation.TargetRelativeLocator]; duplicate {
			return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
		}
		locators[operation.TargetRelativeLocator] = struct{}{}
		if _, collision := semanticTargets[operation.SemanticTargetDigest]; collision {
			return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
		}
		semanticTargets[operation.SemanticTargetDigest] = struct{}{}

		expectAsset := operation.Kind != RecoveryOperationDelete
		if !operation.Source.valid(expectAsset) {
			return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
		}
		switch operation.Kind {
		case RecoveryOperationCreate:
			if operation.ExpectedPrior.Kind != ExpectedTargetAbsent {
				return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
			}
			impact.CreateCount++
		case RecoveryOperationOverwrite:
			if operation.ExpectedPrior.Kind != ExpectedTargetPresent {
				return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
			}
			impact.OverwriteCount++
		case RecoveryOperationSkip:
			if operation.ExpectedPrior.Kind != ExpectedTargetPresent {
				return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
			}
			impact.SkipCount++
		case RecoveryOperationDelete:
			if operation.ExpectedPrior.Kind != ExpectedTargetPresent || input.TargetMode != TargetModeInPlace ||
				input.ConflictPolicy != ConflictExactMirror {
				return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
			}
			impact.DeleteCount++
		default:
			return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
		}
		if !operation.validExpectedPostFacts() {
			return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
		}

		if operation.Source.AssetRef != nil {
			sourceKey := operation.Source.AssetRef.RecoveryPointID + "\x00" + operation.Source.AssetRef.EntryID
			if _, duplicate := sources[sourceKey]; duplicate {
				return RecoveryOperationProducts{}, ErrInvalidRecoveryOperation
			}
			sources[sourceKey] = struct{}{}
		}
		if operation.EstimatedBytes > input.Limits.MaxBytes-impact.EstimatedBytes {
			return RecoveryOperationProducts{}, ErrRecoveryOperationLimit
		}
		impact.EstimatedBytes += operation.EstimatedBytes
		impact.EstimatedItems++
		operation.snapshotTargetMode = input.TargetMode
		operation.snapshotConflictPolicy = input.ConflictPolicy
		rows[index] = cloneRecoveryOperation(operation)
	}

	sort.Slice(rows, func(left, right int) bool {
		return rows[left].TargetPathDigest < rows[right].TargetPathDigest
	})
	impact.Rows = impact.Rows[:0]
	deleteRows := make([]RecoveryOperation, 0, int(impact.DeleteCount))
	for _, operation := range rows {
		impact.Rows = append(impact.Rows, RecoveryImpactRow{
			Kind: operation.Kind, TargetPathDigest: operation.TargetPathDigest, DisplayClass: operation.DisplayClass,
		})
		if operation.Kind == RecoveryOperationDelete {
			deleteRows = append(deleteRows, operation)
		}
	}

	return RecoveryOperationProducts{
		Rows:               rows,
		OperationSetDigest: recoveryOperationSetDigest(operationSetDigestDomain, rows),
		DeleteSetDigest:    recoveryOperationSetDigest(deleteSetDigestDomain, deleteRows),
		Impact:             impact,
	}, nil
}

func cloneRecoveryOperation(operation RecoveryOperation) RecoveryOperation {
	clone := operation
	if operation.Source.AssetRef != nil {
		assetRef := *operation.Source.AssetRef
		clone.Source.AssetRef = &assetRef
	}
	return clone
}

type recoveryOperationRowsSnapshot struct {
	SchemaVersion      int                            `json:"schema_version"`
	TargetMode         TargetMode                     `json:"target_mode"`
	ConflictPolicy     ConflictPolicy                 `json:"conflict_policy"`
	OperationSetDigest string                         `json:"operation_set_digest"`
	DeleteSetDigest    string                         `json:"delete_set_digest"`
	Rows               []recoveryOperationSnapshotRow `json:"rows"`
}

type recoveryOperationSnapshotRow struct {
	Kind                       string `json:"kind"`
	TargetPathDigest           string `json:"target_path_digest"`
	TargetRelativeLocator      string `json:"target_relative_locator"`
	SemanticTargetDigest       string `json:"semantic_target_digest"`
	ExpectedPriorKind          string `json:"expected_prior_kind"`
	ExpectedPriorDigest        string `json:"expected_prior_digest"`
	ExpectedPostIdentityDigest string `json:"expected_post_identity_digest"`
	ExpectedPostBytes          int64  `json:"expected_post_bytes"`
	ExpectedPriorBytes         int64  `json:"expected_prior_bytes"`
	SourceKind                 string `json:"source_kind"`
	SourceRecoveryPointID      string `json:"source_recovery_point_id"`
	SourceEntryID              string `json:"source_entry_id"`
	DisplayClass               string `json:"display_class"`
	EstimatedBytes             int64  `json:"estimated_bytes"`
}

func encodeRecoveryOperationRows(rows []RecoveryOperation) (string, error) {
	targetMode, conflictPolicy, err := recoveryOperationSnapshotContext(rows)
	if err != nil {
		return "", err
	}
	products, err := rebuildRecoveryOperationSnapshotProduct(targetMode, conflictPolicy, rows)
	if err != nil {
		return "", err
	}
	snapshotRows, err := recoveryOperationSnapshotRows(products.Rows)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(recoveryOperationRowsSnapshot{
		SchemaVersion:      recoveryOperationSnapshotSchemaVersion,
		TargetMode:         targetMode,
		ConflictPolicy:     conflictPolicy,
		OperationSetDigest: products.OperationSetDigest,
		DeleteSetDigest:    products.DeleteSetDigest,
		Rows:               snapshotRows,
	})
	if err != nil || len(encoded) > recoveryOperationSnapshotMaxBytes {
		return "", ErrInvalidRecoveryOperation
	}
	return string(encoded), nil
}

func decodeRecoveryOperationRows(encoded string) ([]RecoveryOperation, error) {
	if encoded == "" || len(encoded) > recoveryOperationSnapshotMaxBytes {
		return nil, ErrInvalidRecoveryOperation
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var snapshot recoveryOperationRowsSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, ErrInvalidRecoveryOperation
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidRecoveryOperation
	}
	if snapshot.SchemaVersion != recoveryOperationSnapshotSchemaVersion || snapshot.TargetMode.Validate() != nil ||
		snapshot.ConflictPolicy.Validate() != nil || !validDigest(snapshot.OperationSetDigest) ||
		!validDigest(snapshot.DeleteSetDigest) || len(snapshot.Rows) == 0 || len(snapshot.Rows) > exactSelectionMaxItems {
		return nil, ErrInvalidRecoveryOperation
	}
	canonical, err := json.Marshal(snapshot)
	if err != nil || string(canonical) != encoded {
		return nil, ErrInvalidRecoveryOperation
	}
	rows := make([]RecoveryOperation, len(snapshot.Rows))
	for index, row := range snapshot.Rows {
		operation := RecoveryOperation{
			Kind: RecoveryOperationKind(row.Kind), TargetPathDigest: row.TargetPathDigest,
			TargetRelativeLocator: row.TargetRelativeLocator, SemanticTargetDigest: row.SemanticTargetDigest,
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetIdentityKind(row.ExpectedPriorKind), Digest: row.ExpectedPriorDigest},
			ExpectedPostIdentityDigest: row.ExpectedPostIdentityDigest,
			ExpectedPostBytes:          row.ExpectedPostBytes,
			ExpectedPriorBytes:         row.ExpectedPriorBytes,
			Source:                     RecoveryOperationSource{Kind: RecoveryOperationSourceKind(row.SourceKind)},
			DisplayClass:               RecoveryDisplayClass(row.DisplayClass), EstimatedBytes: row.EstimatedBytes,
		}
		if operation.Source.Kind == RecoveryOperationSourceAssetRef {
			operation.Source.AssetRef = &backupasset.AssetRef{
				RecoveryPointID: row.SourceRecoveryPointID,
				EntryID:         row.SourceEntryID,
			}
		} else if row.SourceRecoveryPointID != "" || row.SourceEntryID != "" {
			return nil, ErrInvalidRecoveryOperation
		}
		operation.snapshotTargetMode = snapshot.TargetMode
		operation.snapshotConflictPolicy = snapshot.ConflictPolicy
		rows[index] = operation
	}
	if _, err := recoveryOperationSnapshotRows(rows); err != nil {
		return nil, err
	}
	products, err := rebuildRecoveryOperationSnapshotProduct(snapshot.TargetMode, snapshot.ConflictPolicy, rows)
	if err != nil || snapshot.OperationSetDigest != products.OperationSetDigest ||
		snapshot.DeleteSetDigest != products.DeleteSetDigest {
		return nil, ErrInvalidRecoveryOperation
	}
	return products.Rows, nil
}

func recoveryOperationSnapshotContext(rows []RecoveryOperation) (TargetMode, ConflictPolicy, error) {
	if len(rows) == 0 {
		return "", "", ErrInvalidRecoveryOperation
	}
	targetMode := rows[0].snapshotTargetMode
	conflictPolicy := rows[0].snapshotConflictPolicy
	if targetMode.Validate() != nil || conflictPolicy.Validate() != nil {
		return "", "", ErrInvalidRecoveryOperation
	}
	for _, row := range rows[1:] {
		if row.snapshotTargetMode != targetMode || row.snapshotConflictPolicy != conflictPolicy {
			return "", "", ErrInvalidRecoveryOperation
		}
	}
	return targetMode, conflictPolicy, nil
}

func rebuildRecoveryOperationSnapshotProduct(
	targetMode TargetMode,
	conflictPolicy ConflictPolicy,
	rows []RecoveryOperation,
) (RecoveryOperationProducts, error) {
	return NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode:     targetMode,
		ConflictPolicy: conflictPolicy,
		Operations:     rows,
		Limits: RecoveryOperationLimits{
			MaxRows:       exactSelectionMaxItems,
			MaxItems:      exactSelectionMaxItems,
			MaxBytes:      recoveryOperationMaxTotalBytes,
			MaxImpactRows: exactSelectionMaxItems,
		},
	})
}

func recoveryOperationSnapshotRows(rows []RecoveryOperation) ([]recoveryOperationSnapshotRow, error) {
	if len(rows) == 0 || len(rows) > exactSelectionMaxItems {
		return nil, ErrInvalidRecoveryOperation
	}
	snapshotRows := make([]recoveryOperationSnapshotRow, len(rows))
	previousTarget := ""
	for index, operation := range rows {
		expectAsset := operation.Kind != RecoveryOperationDelete
		if !validDigest(operation.TargetPathDigest) || !validTargetRelativeLocator(operation.TargetRelativeLocator) ||
			!validDigest(operation.SemanticTargetDigest) || !operation.ExpectedPrior.valid() || !operation.validExpectedPostFacts() ||
			!operation.Source.valid(expectAsset) || !operation.DisplayClass.valid() || operation.EstimatedBytes < 0 ||
			(index > 0 && operation.TargetPathDigest <= previousTarget) {
			return nil, ErrInvalidRecoveryOperation
		}
		switch operation.Kind {
		case RecoveryOperationCreate:
			if operation.ExpectedPrior.Kind != ExpectedTargetAbsent {
				return nil, ErrInvalidRecoveryOperation
			}
		case RecoveryOperationOverwrite, RecoveryOperationSkip, RecoveryOperationDelete:
			if operation.ExpectedPrior.Kind != ExpectedTargetPresent {
				return nil, ErrInvalidRecoveryOperation
			}
		default:
			return nil, ErrInvalidRecoveryOperation
		}
		row := recoveryOperationSnapshotRow{
			Kind: string(operation.Kind), TargetPathDigest: operation.TargetPathDigest,
			TargetRelativeLocator: operation.TargetRelativeLocator, SemanticTargetDigest: operation.SemanticTargetDigest,
			ExpectedPriorKind: string(operation.ExpectedPrior.Kind), ExpectedPriorDigest: operation.ExpectedPrior.Digest,
			ExpectedPostIdentityDigest: operation.ExpectedPostIdentityDigest,
			ExpectedPostBytes:          operation.ExpectedPostBytes,
			ExpectedPriorBytes:         operation.ExpectedPriorBytes,
			SourceKind:                 string(operation.Source.Kind), DisplayClass: string(operation.DisplayClass), EstimatedBytes: operation.EstimatedBytes,
		}
		if operation.Source.AssetRef != nil {
			row.SourceRecoveryPointID = operation.Source.AssetRef.RecoveryPointID
			row.SourceEntryID = operation.Source.AssetRef.EntryID
		}
		snapshotRows[index] = row
		previousTarget = operation.TargetPathDigest
	}
	return snapshotRows, nil
}

func recoveryOperationSetDigest(domain string, rows []RecoveryOperation) string {
	buffer := bytes.NewBuffer(nil)
	buffer.WriteString(domain)
	writeRecoveryDigestUint32(buffer, uint32(len(rows)))
	for _, row := range rows {
		writeRecoveryOperationString(buffer, row.TargetPathDigest)
		writeRecoveryOperationString(buffer, row.TargetRelativeLocator)
		writeRecoveryOperationString(buffer, row.SemanticTargetDigest)
		writeRecoveryOperationString(buffer, string(row.Kind))
		writeRecoveryOperationString(buffer, string(row.ExpectedPrior.Kind))
		writeRecoveryOperationString(buffer, row.ExpectedPrior.Digest)
		writeRecoveryOperationString(buffer, row.ExpectedPostIdentityDigest)
		writeRecoveryDigestInt64(buffer, row.ExpectedPostBytes)
		writeRecoveryDigestInt64(buffer, row.ExpectedPriorBytes)
		writeRecoveryOperationString(buffer, string(row.Source.Kind))
		if row.Source.AssetRef != nil {
			writeRecoveryOperationString(buffer, row.Source.AssetRef.RecoveryPointID)
			writeRecoveryOperationString(buffer, row.Source.AssetRef.EntryID)
		} else {
			writeRecoveryOperationString(buffer, "")
			writeRecoveryOperationString(buffer, "")
		}
		writeRecoveryOperationString(buffer, string(row.DisplayClass))
		writeRecoveryDigestUint64(buffer, uint64(row.EstimatedBytes))
	}
	sum := sha256.Sum256(buffer.Bytes())
	return hex.EncodeToString(sum[:])
}

func writeRecoveryOperationString(buffer *bytes.Buffer, value string) {
	writeRecoveryDigestUint32(buffer, uint32(len(value)))
	buffer.WriteString(value)
}

func writeRecoveryDigestUint32(buffer *bytes.Buffer, value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	buffer.Write(encoded[:])
}

// TargetLocatorEnvelopeBinding is the immutable, row-bound context for a
// private canonical target locator. The ciphertext layer authenticates this
// complete envelope; this codec additionally rejects noncanonical or swapped
// plaintext before a caller can use it.
type TargetLocatorEnvelopeBinding struct {
	CodecVersion               int
	JobID                      string
	JobItemID                  string
	PlanDigest                 string
	PlanItemID                 string
	SourceRecoveryPointID      string
	SourceEntryID              string
	TargetMode                 TargetMode
	NodeID                     uint
	RootID                     string
	RootLocatorDigest          string
	SemanticTargetDigest       string
	TargetObjectDigest         string
	Operation                  RecoveryOperationKind
	WorkspaceBindingDigest     string
	WorkspaceRelativeLocator   string
	ExpectedPriorKind          ExpectedTargetIdentityKind
	ExpectedPriorDigest        string
	ExpectedPostIdentityDigest string
	ExpectedPostBytes          int64
	ExpectedPriorBytes         int64
	TargetLocatorKeyVersion    int
	TargetLocatorCipherVersion int
}

func (binding TargetLocatorEnvelopeBinding) Validate() error {
	if binding.CodecVersion != targetLocatorEnvelopeSchemaVersion ||
		!validOpaqueID(binding.JobID) || !validOpaqueID(binding.JobItemID) ||
		!validDigest(binding.PlanDigest) || binding.TargetMode.Validate() != nil || binding.NodeID == 0 ||
		!validBoundedOpaque(binding.RootID, targetRootIDMax) || !validDigest(binding.RootLocatorDigest) ||
		!validDigest(binding.SemanticTargetDigest) || !validDigest(binding.TargetObjectDigest) ||
		binding.SemanticTargetDigest == binding.TargetObjectDigest || binding.TargetLocatorKeyVersion <= 0 ||
		binding.TargetLocatorCipherVersion <= 0 {
		return ErrInvalidTargetLocatorEnvelope
	}
	switch binding.TargetMode {
	case TargetModeInPlace:
		if binding.WorkspaceBindingDigest != "" || binding.WorkspaceRelativeLocator != "" {
			return ErrInvalidTargetLocatorEnvelope
		}
	case TargetModeIsolated:
		if !validDigest(binding.WorkspaceBindingDigest) ||
			!validTargetRelativeLocator(binding.WorkspaceRelativeLocator) {
			return ErrInvalidTargetLocatorEnvelope
		}
	}
	switch binding.Operation {
	case RecoveryOperationCreate:
		if !validOpaqueID(binding.PlanItemID) || !validOpaqueID(binding.SourceRecoveryPointID) ||
			!validDigest(binding.SourceEntryID) || binding.ExpectedPriorKind != ExpectedTargetAbsent || binding.ExpectedPriorDigest != "" ||
			!validDigest(binding.ExpectedPostIdentityDigest) || binding.ExpectedPostBytes < 0 ||
			binding.ExpectedPriorBytes != -1 {
			return ErrInvalidTargetLocatorEnvelope
		}
	case RecoveryOperationOverwrite:
		if !validOpaqueID(binding.PlanItemID) || !validOpaqueID(binding.SourceRecoveryPointID) ||
			!validDigest(binding.SourceEntryID) || binding.ExpectedPriorKind != ExpectedTargetPresent || !validDigest(binding.ExpectedPriorDigest) ||
			!validDigest(binding.ExpectedPostIdentityDigest) || binding.ExpectedPostBytes < 0 ||
			binding.ExpectedPriorBytes < 0 {
			return ErrInvalidTargetLocatorEnvelope
		}
	case RecoveryOperationSkip:
		if !validOpaqueID(binding.PlanItemID) || !validOpaqueID(binding.SourceRecoveryPointID) ||
			!validDigest(binding.SourceEntryID) || binding.ExpectedPriorKind != ExpectedTargetPresent || !validDigest(binding.ExpectedPriorDigest) ||
			binding.ExpectedPostIdentityDigest != binding.ExpectedPriorDigest || binding.ExpectedPostBytes != -1 ||
			binding.ExpectedPriorBytes < 0 {
			return ErrInvalidTargetLocatorEnvelope
		}
	case RecoveryOperationDelete:
		if binding.PlanItemID != "" || binding.SourceRecoveryPointID != "" || binding.SourceEntryID != "" ||
			binding.ExpectedPriorKind != ExpectedTargetPresent || !validDigest(binding.ExpectedPriorDigest) ||
			binding.TargetMode != TargetModeInPlace || binding.ExpectedPostIdentityDigest != "" ||
			binding.ExpectedPostBytes != -1 || binding.ExpectedPriorBytes != -1 {
			return ErrInvalidTargetLocatorEnvelope
		}
	default:
		return ErrInvalidTargetLocatorEnvelope
	}
	return nil
}

type targetLocatorEnvelope struct {
	SchemaVersion              int                        `json:"schema_version"`
	JobID                      string                     `json:"job_id"`
	JobItemID                  string                     `json:"job_item_id"`
	PlanDigest                 string                     `json:"plan_digest"`
	PlanItemID                 string                     `json:"plan_item_id"`
	SourceRecoveryPointID      string                     `json:"source_recovery_point_id"`
	SourceEntryID              string                     `json:"source_entry_id"`
	TargetMode                 TargetMode                 `json:"target_mode"`
	NodeID                     uint                       `json:"node_id"`
	RootID                     string                     `json:"root_id"`
	RootLocatorDigest          string                     `json:"root_locator_digest"`
	SemanticTargetDigest       string                     `json:"semantic_target_digest"`
	TargetObjectDigest         string                     `json:"target_object_digest"`
	Operation                  RecoveryOperationKind      `json:"operation"`
	WorkspaceBindingDigest     string                     `json:"workspace_binding_digest"`
	WorkspaceRelativeLocator   string                     `json:"workspace_relative_locator"`
	ExpectedPriorKind          ExpectedTargetIdentityKind `json:"expected_prior_kind"`
	ExpectedPriorDigest        string                     `json:"expected_prior_digest"`
	ExpectedPostIdentityDigest string                     `json:"expected_post_identity_digest"`
	ExpectedPostBytes          int64                      `json:"expected_post_bytes"`
	ExpectedPriorBytes         int64                      `json:"expected_prior_bytes"`
	TargetLocatorKeyVersion    int                        `json:"target_locator_key_version"`
	TargetLocatorCipherVersion int                        `json:"target_locator_cipher_version"`
	RelativeLocator            string                     `json:"relative_locator"`
	BindingDigest              string                     `json:"binding_digest"`
}

func (envelope targetLocatorEnvelope) binding() TargetLocatorEnvelopeBinding {
	return TargetLocatorEnvelopeBinding{
		CodecVersion:               envelope.SchemaVersion,
		JobID:                      envelope.JobID,
		JobItemID:                  envelope.JobItemID,
		PlanDigest:                 envelope.PlanDigest,
		PlanItemID:                 envelope.PlanItemID,
		SourceRecoveryPointID:      envelope.SourceRecoveryPointID,
		SourceEntryID:              envelope.SourceEntryID,
		TargetMode:                 envelope.TargetMode,
		NodeID:                     envelope.NodeID,
		RootID:                     envelope.RootID,
		RootLocatorDigest:          envelope.RootLocatorDigest,
		SemanticTargetDigest:       envelope.SemanticTargetDigest,
		TargetObjectDigest:         envelope.TargetObjectDigest,
		Operation:                  envelope.Operation,
		WorkspaceBindingDigest:     envelope.WorkspaceBindingDigest,
		WorkspaceRelativeLocator:   envelope.WorkspaceRelativeLocator,
		ExpectedPriorKind:          envelope.ExpectedPriorKind,
		ExpectedPriorDigest:        envelope.ExpectedPriorDigest,
		ExpectedPostIdentityDigest: envelope.ExpectedPostIdentityDigest,
		ExpectedPostBytes:          envelope.ExpectedPostBytes,
		ExpectedPriorBytes:         envelope.ExpectedPriorBytes,
		TargetLocatorKeyVersion:    envelope.TargetLocatorKeyVersion,
		TargetLocatorCipherVersion: envelope.TargetLocatorCipherVersion,
	}
}

func (envelope targetLocatorEnvelope) Validate() error {
	binding := envelope.binding()
	if envelope.SchemaVersion != targetLocatorEnvelopeSchemaVersion || binding.Validate() != nil ||
		!validTargetRelativeLocator(envelope.RelativeLocator) {
		return ErrInvalidTargetLocatorEnvelope
	}
	targetRelativeLocator := envelope.RelativeLocator
	if binding.TargetMode == TargetModeIsolated {
		targetRelativeLocator = binding.WorkspaceRelativeLocator + "/" + envelope.RelativeLocator
		if !validTargetRelativeLocator(targetRelativeLocator) {
			return ErrInvalidTargetLocatorEnvelope
		}
	}
	semanticDigest, semanticErr := SemanticTargetDigest(
		binding.TargetMode, binding.RootID, binding.RootLocatorDigest, envelope.RelativeLocator,
	)
	objectDigest, objectErr := TargetObjectDigest(binding.RootID, binding.RootLocatorDigest, targetRelativeLocator)
	if semanticErr != nil || objectErr != nil || binding.SemanticTargetDigest != semanticDigest ||
		binding.TargetObjectDigest != objectDigest {
		return ErrInvalidTargetLocatorEnvelope
	}
	return nil
}

func EncodeTargetLocatorEnvelope(binding TargetLocatorEnvelopeBinding, relativeLocator string) (string, error) {
	envelope := targetLocatorEnvelope{
		SchemaVersion:              binding.CodecVersion,
		JobID:                      binding.JobID,
		JobItemID:                  binding.JobItemID,
		PlanDigest:                 binding.PlanDigest,
		PlanItemID:                 binding.PlanItemID,
		SourceRecoveryPointID:      binding.SourceRecoveryPointID,
		SourceEntryID:              binding.SourceEntryID,
		TargetMode:                 binding.TargetMode,
		NodeID:                     binding.NodeID,
		RootID:                     binding.RootID,
		RootLocatorDigest:          binding.RootLocatorDigest,
		SemanticTargetDigest:       binding.SemanticTargetDigest,
		TargetObjectDigest:         binding.TargetObjectDigest,
		Operation:                  binding.Operation,
		WorkspaceBindingDigest:     binding.WorkspaceBindingDigest,
		WorkspaceRelativeLocator:   binding.WorkspaceRelativeLocator,
		ExpectedPriorKind:          binding.ExpectedPriorKind,
		ExpectedPriorDigest:        binding.ExpectedPriorDigest,
		ExpectedPostIdentityDigest: binding.ExpectedPostIdentityDigest,
		ExpectedPostBytes:          binding.ExpectedPostBytes,
		ExpectedPriorBytes:         binding.ExpectedPriorBytes,
		TargetLocatorKeyVersion:    binding.TargetLocatorKeyVersion,
		TargetLocatorCipherVersion: binding.TargetLocatorCipherVersion,
		RelativeLocator:            relativeLocator,
	}
	if envelope.Validate() != nil {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	envelope.BindingDigest = targetLocatorEnvelopeDigest(envelope)
	encoded, err := json.Marshal(envelope)
	if err != nil || len(encoded) > targetLocatorEnvelopeMaxBytes {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	return string(encoded), nil
}

func DecodeTargetLocatorEnvelope(encoded string, expected TargetLocatorEnvelopeBinding) (string, error) {
	if encoded == "" || len(encoded) > targetLocatorEnvelopeMaxBytes || expected.Validate() != nil {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var envelope targetLocatorEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || string(canonical) != encoded || envelope.Validate() != nil ||
		!validDigest(envelope.BindingDigest) || envelope.BindingDigest != targetLocatorEnvelopeDigest(envelope) ||
		envelope.binding() != expected {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	return envelope.RelativeLocator, nil
}

func targetLocatorEnvelopeDigest(envelope targetLocatorEnvelope) string {
	return framedDigest(
		targetLocatorEnvelopeDigestDomain,
		strconv.Itoa(envelope.SchemaVersion),
		envelope.JobID,
		envelope.JobItemID,
		envelope.PlanDigest,
		envelope.PlanItemID,
		envelope.SourceRecoveryPointID,
		envelope.SourceEntryID,
		string(envelope.TargetMode),
		strconv.FormatUint(uint64(envelope.NodeID), 10),
		envelope.RootID,
		envelope.RootLocatorDigest,
		envelope.SemanticTargetDigest,
		envelope.TargetObjectDigest,
		string(envelope.Operation),
		envelope.WorkspaceBindingDigest,
		envelope.WorkspaceRelativeLocator,
		string(envelope.ExpectedPriorKind),
		envelope.ExpectedPriorDigest,
		envelope.ExpectedPostIdentityDigest,
		strconv.FormatInt(envelope.ExpectedPostBytes, 10),
		strconv.FormatInt(envelope.ExpectedPriorBytes, 10),
		strconv.Itoa(envelope.TargetLocatorKeyVersion),
		strconv.Itoa(envelope.TargetLocatorCipherVersion),
		envelope.RelativeLocator,
	)
}

// SealTargetLocatorEnvelope applies the recovery-local, explicitly versioned
// HKDF-SHA256/AES-256-GCM envelope. Generic model encryption must never process
// this ciphertext.
func SealTargetLocatorEnvelope(
	material backupasset.DomainKeyMaterial,
	binding TargetLocatorEnvelopeBinding,
	relativeLocator string,
) (string, error) {
	if !validTargetLocatorKey(material, binding.TargetLocatorKeyVersion) ||
		binding.TargetLocatorCipherVersion != targetLocatorCipherVersion {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	plaintext, err := EncodeTargetLocatorEnvelope(binding, relativeLocator)
	if err != nil {
		return "", err
	}
	aead, err := targetLocatorAEAD(material)
	if err != nil {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	aad := []byte(targetLocatorEnvelopeBindingDigest(binding))
	sealed := aead.Seal(nil, nonce, []byte(plaintext), aad)
	payload := append(nonce, sealed...)
	return targetLocatorCiphertextPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// OpenTargetLocatorEnvelope authenticates the complete persisted binding before
// returning the private canonical locator. Errors remain sentinel-only so raw
// locator or ciphertext material cannot enter logs or API failures.
func OpenTargetLocatorEnvelope(
	material backupasset.DomainKeyMaterial,
	binding TargetLocatorEnvelopeBinding,
	encoded string,
) (string, error) {
	if !validTargetLocatorKey(material, binding.TargetLocatorKeyVersion) ||
		binding.TargetLocatorCipherVersion != targetLocatorCipherVersion ||
		!strings.HasPrefix(encoded, targetLocatorCiphertextPrefix) {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, targetLocatorCiphertextPrefix))
	if err != nil {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	aead, err := targetLocatorAEAD(material)
	if err != nil || len(payload) < aead.NonceSize()+aead.Overhead() {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	nonce, ciphertext := payload[:aead.NonceSize()], payload[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(targetLocatorEnvelopeBindingDigest(binding)))
	if err != nil {
		return "", ErrInvalidTargetLocatorEnvelope
	}
	return DecodeTargetLocatorEnvelope(string(plaintext), binding)
}

func targetLocatorAEAD(material backupasset.DomainKeyMaterial) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, material.Key, nil, targetLocatorAEADDomain, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func validTargetLocatorKey(material backupasset.DomainKeyMaterial, version int) bool {
	return material.Domain == backupasset.KeyDomainRecoveryCleanupOwnership &&
		material.State == backupasset.DomainKeyActive && material.Version == version &&
		version > 0 && len(material.Key) == 32
}

func targetLocatorEnvelopeBindingDigest(binding TargetLocatorEnvelopeBinding) string {
	return framedDigest(
		targetLocatorAEADDomain,
		strconv.Itoa(binding.CodecVersion), binding.JobID, binding.JobItemID, binding.PlanDigest,
		binding.PlanItemID, binding.SourceRecoveryPointID, binding.SourceEntryID,
		string(binding.TargetMode), strconv.FormatUint(uint64(binding.NodeID), 10), binding.RootID,
		binding.RootLocatorDigest, binding.SemanticTargetDigest, binding.TargetObjectDigest,
		string(binding.Operation), binding.WorkspaceBindingDigest, binding.WorkspaceRelativeLocator,
		string(binding.ExpectedPriorKind), binding.ExpectedPriorDigest,
		binding.ExpectedPostIdentityDigest, strconv.FormatInt(binding.ExpectedPostBytes, 10),
		strconv.FormatInt(binding.ExpectedPriorBytes, 10), strconv.Itoa(binding.TargetLocatorKeyVersion),
		strconv.Itoa(binding.TargetLocatorCipherVersion),
	)
}

type AuthorityCategory string

const (
	AuthorityWrite             AuthorityCategory = "write"
	AuthorityExactMirrorDelete AuthorityCategory = "exact_mirror_delete"
)

func (category AuthorityCategory) Validate() error {
	if category != AuthorityWrite && category != AuthorityExactMirrorDelete {
		return ErrInvalidAuthority
	}
	return nil
}

type SecurityDecisionKind string

const (
	SecurityDecisionAllowClean    SecurityDecisionKind = "allow_clean"
	SecurityDecisionBlock         SecurityDecisionKind = "block"
	SecurityDecisionAdminOverride SecurityDecisionKind = "admin_override"
)

type SecurityDecision struct {
	Kind                  SecurityDecisionKind
	DecisionDigest        string
	FindingSetDigest      string
	PolicyRevision        string
	OverrideBindingDigest string
}

type SecurityFindingCategory string

const (
	SecurityFindingMalware       SecurityFindingCategory = "malware"
	SecurityFindingSuspicious    SecurityFindingCategory = "suspicious"
	SecurityFindingTestSignature SecurityFindingCategory = "test_signature"
	SecurityFindingUnknown       SecurityFindingCategory = "unknown"
)

func (category SecurityFindingCategory) known() bool {
	switch category {
	case SecurityFindingMalware, SecurityFindingSuspicious, SecurityFindingTestSignature:
		return true
	default:
		return false
	}
}

type SecurityFindingDisposition string

const (
	SecurityFindingDispositionClean   SecurityFindingDisposition = "clean"
	SecurityFindingDispositionBlocked SecurityFindingDisposition = "blocked"
)

func (disposition SecurityFindingDisposition) valid() bool {
	return disposition == SecurityFindingDispositionClean || disposition == SecurityFindingDispositionBlocked
}

type SecurityFinding struct {
	Category SecurityFindingCategory `json:"category"`
}

type PreflightSecurityDecisionInput struct {
	FindingSetDigest      string
	PolicyRevision        string
	Findings              []SecurityFinding
	OverridableCategories []SecurityFindingCategory
}

type SecurityOverrideCandidate struct {
	BindingDigest    string                    `json:"binding_digest"`
	FindingSetDigest string                    `json:"finding_set_digest"`
	PolicyRevision   string                    `json:"policy_revision"`
	Categories       []SecurityFindingCategory `json:"categories"`
}

type PreflightSecurityDecision struct {
	Decision          SecurityDecision           `json:"decision"`
	FindingCount      int                        `json:"finding_count"`
	OverrideCandidate *SecurityOverrideCandidate `json:"override_candidate,omitempty"`
}

func NewPreflightSecurityDecision(input PreflightSecurityDecisionInput) (PreflightSecurityDecision, error) {
	if !validDigest(input.FindingSetDigest) || !validOpaqueRevision(input.PolicyRevision) {
		return PreflightSecurityDecision{}, ErrInvalidSecurityDecision
	}

	kind := SecurityDecisionAllowClean
	if len(input.Findings) > 0 {
		kind = SecurityDecisionBlock
	}
	decision := SecurityDecision{
		Kind: kind,
		DecisionDigest: framedDigest(
			securityDecisionDigestDomain,
			string(kind),
			input.FindingSetDigest,
			input.PolicyRevision,
			strconv.Itoa(len(input.Findings)),
		),
		FindingSetDigest: input.FindingSetDigest,
		PolicyRevision:   input.PolicyRevision,
	}
	product := PreflightSecurityDecision{Decision: decision, FindingCount: len(input.Findings)}
	if len(input.Findings) == 0 {
		return product, nil
	}

	allowed := make(map[SecurityFindingCategory]struct{}, len(input.OverridableCategories))
	for _, category := range input.OverridableCategories {
		if category.known() {
			allowed[category] = struct{}{}
		}
	}
	candidateCategories := make(map[SecurityFindingCategory]struct{}, len(input.Findings))
	for _, finding := range input.Findings {
		category := finding.Category
		if !category.known() {
			category = SecurityFindingUnknown
		}
		if category == SecurityFindingUnknown {
			return product, nil
		}
		if _, ok := allowed[category]; !ok {
			return product, nil
		}
		candidateCategories[category] = struct{}{}
	}

	categories := make([]SecurityFindingCategory, 0, len(candidateCategories))
	for category := range candidateCategories {
		categories = append(categories, category)
	}
	sort.Slice(categories, func(left, right int) bool { return categories[left] < categories[right] })
	categoryValues := make([]string, len(categories))
	for index, category := range categories {
		categoryValues[index] = string(category)
	}
	candidate := &SecurityOverrideCandidate{
		FindingSetDigest: input.FindingSetDigest,
		PolicyRevision:   input.PolicyRevision,
		Categories:       categories,
	}
	candidate.BindingDigest = framedDigest(
		securityOverrideCandidateDomain,
		input.FindingSetDigest,
		input.PolicyRevision,
		strings.Join(categoryValues, ","),
	)
	product.OverrideCandidate = candidate
	return product, nil
}

func (product PreflightSecurityDecision) ValidateBinding(
	findingSetDigest,
	policyRevision string,
	disposition SecurityFindingDisposition,
) error {
	if err := product.validateBindingShape(findingSetDigest, policyRevision); err != nil {
		return err
	}
	if !disposition.valid() ||
		(disposition == SecurityFindingDispositionClean && product.Decision.Kind != SecurityDecisionAllowClean) ||
		(disposition == SecurityFindingDispositionBlocked && product.Decision.Kind != SecurityDecisionBlock) {
		return ErrInvalidSecurityDecision
	}
	return nil
}

func (product PreflightSecurityDecision) validateBindingShape(findingSetDigest, policyRevision string) error {
	if !validDigest(findingSetDigest) || !validOpaqueRevision(policyRevision) ||
		product.Decision.FindingSetDigest != findingSetDigest || product.Decision.PolicyRevision != policyRevision {
		return ErrRecoveryPreflightConflict
	}
	if product.FindingCount < 0 {
		return ErrInvalidSecurityDecision
	}
	expectedDecision := framedDigest(
		securityDecisionDigestDomain,
		string(product.Decision.Kind),
		product.Decision.FindingSetDigest,
		product.Decision.PolicyRevision,
		strconv.Itoa(product.FindingCount),
	)
	if product.Decision.Validate() != nil || product.Decision.DecisionDigest != expectedDecision ||
		product.Decision.Kind == SecurityDecisionAdminOverride {
		return ErrInvalidSecurityDecision
	}
	if product.Decision.Kind == SecurityDecisionAllowClean {
		if product.FindingCount != 0 || product.OverrideCandidate != nil {
			return ErrInvalidSecurityDecision
		}
		return nil
	}
	if product.Decision.Kind != SecurityDecisionBlock || product.FindingCount == 0 {
		return ErrInvalidSecurityDecision
	}
	if product.OverrideCandidate == nil {
		return nil
	}
	if len(product.OverrideCandidate.Categories) == 0 ||
		product.OverrideCandidate.FindingSetDigest != findingSetDigest ||
		product.OverrideCandidate.PolicyRevision != policyRevision {
		return ErrInvalidSecurityDecision
	}
	categoryValues := make([]string, len(product.OverrideCandidate.Categories))
	previous := SecurityFindingCategory("")
	for index, category := range product.OverrideCandidate.Categories {
		if !category.known() || (index > 0 && category <= previous) {
			return ErrInvalidSecurityDecision
		}
		categoryValues[index] = string(category)
		previous = category
	}
	expectedCandidate := framedDigest(
		securityOverrideCandidateDomain,
		findingSetDigest,
		policyRevision,
		strings.Join(categoryValues, ","),
	)
	if product.OverrideCandidate.BindingDigest != expectedCandidate {
		return ErrInvalidSecurityDecision
	}
	return nil
}

func (decision SecurityDecision) Validate() error {
	if !validDigest(decision.DecisionDigest) || !validDigest(decision.FindingSetDigest) ||
		!validOpaqueRevision(decision.PolicyRevision) {
		return ErrInvalidSecurityDecision
	}

	switch decision.Kind {
	case SecurityDecisionAllowClean, SecurityDecisionBlock:
		if decision.OverrideBindingDigest != "" {
			return ErrInvalidSecurityDecision
		}
	case SecurityDecisionAdminOverride:
		if !validDigest(decision.OverrideBindingDigest) {
			return ErrInvalidSecurityDecision
		}
	default:
		return ErrInvalidSecurityDecision
	}

	return nil
}

type PlanBinding struct {
	SchemaVersion        int
	PlanDigest           string
	SelectionDigest      string
	RepositoryID         string
	RecoveryPointID      string
	SourceRevisionDigest string
	SourceRevision       SourceRevision
	Target               TargetBinding
	ConflictPolicy       ConflictPolicy
	OperationSetDigest   string
	DeleteSetDigest      string
	CapabilityRevision   string
	SecurityDecision     SecurityDecision
	PreflightRevision    string
}

func (binding PlanBinding) Validate() error {
	if binding.SchemaVersion != 1 || !validDigest(binding.PlanDigest) || !validDigest(binding.SelectionDigest) ||
		!validOpaqueID(binding.RepositoryID) || !validOpaqueID(binding.RecoveryPointID) || !validDigest(binding.SourceRevisionDigest) ||
		binding.SourceRevision.Validate() != nil ||
		binding.Target.Validate() != nil || binding.ConflictPolicy.Validate() != nil ||
		!validDigest(binding.OperationSetDigest) || !validDigest(binding.DeleteSetDigest) ||
		!validOpaqueRevision(binding.CapabilityRevision) || binding.SecurityDecision.Validate() != nil ||
		!validOpaqueRevision(binding.PreflightRevision) {
		return ErrInvalidPlanBinding
	}

	if binding.ConflictPolicy == ConflictExactMirror {
		if binding.Target.Mode != TargetModeInPlace {
			return ErrInvalidPlanBinding
		}
	} else if binding.DeleteSetDigest != EmptyDeleteSetDigest {
		return ErrInvalidPlanBinding
	}

	return nil
}

// PreflightBinding is the immutable read-only snapshot copied into a durable
// job. It deliberately carries only opaque revisions and digests.
type PreflightBinding struct {
	ID                   string
	Revision             string
	SourceRevisionDigest string
	TargetNodeID         uint
	NodeRevision         string
	RootID               string
	RootLocatorDigest    string `json:"-"`
	PathDigest           string `json:"-"`
	TargetRevision       string
	CapabilityRevision   string
	PolicyRevision       string
	FindingSetDigest     string
	OperationSetDigest   string
	DeleteSetDigest      string
	EstimatedItems       int64
	EstimatedBytes       int64
	ExpiresAt            time.Time
}

func (binding PreflightBinding) ValidateAt(now time.Time) error {
	if now.IsZero() || !validOpaqueID(binding.ID) || !validOpaqueRevision(binding.Revision) ||
		!validDigest(binding.SourceRevisionDigest) || binding.TargetNodeID == 0 ||
		!validOpaqueRevision(binding.NodeRevision) || !validBoundedOpaque(binding.RootID, targetRootIDMax) ||
		!validDigest(binding.RootLocatorDigest) || !validDigest(binding.PathDigest) ||
		!validOpaqueRevision(binding.TargetRevision) ||
		!validOpaqueRevision(binding.CapabilityRevision) || !validOpaqueRevision(binding.PolicyRevision) ||
		!validDigest(binding.FindingSetDigest) || !validDigest(binding.OperationSetDigest) ||
		!validDigest(binding.DeleteSetDigest) || binding.EstimatedItems < 0 || binding.EstimatedBytes < 0 ||
		!binding.ExpiresAt.After(now) {
		return ErrInvalidPreflightBinding
	}
	return nil
}

// AuthorityBinding identifies the exact consumed one-use authority that
// admitted a job. The grant secret and encrypted reason remain in their owning
// boundary and are never copied here.
type AuthorityBinding struct {
	GrantID       string
	Category      AuthorityCategory
	BindingDigest string
	ExpiresAt     time.Time
	ConsumedAt    time.Time
}

func (binding AuthorityBinding) ValidateAt(now time.Time) error {
	if now.IsZero() || !validOpaqueID(binding.GrantID) || binding.Category.Validate() != nil ||
		!validDigest(binding.BindingDigest) || binding.ConsumedAt.IsZero() || binding.ConsumedAt.After(now) ||
		!binding.ExpiresAt.After(now) || !binding.ExpiresAt.After(binding.ConsumedAt) {
		return ErrInvalidAuthorityBinding
	}
	return nil
}

// DeleteAuthorityCheckpointBinding is the immutable pause product created
// after every non-delete exact-mirror operation has been checkpointed. Raw
// target locators and authorization material deliberately remain outside it.
type DeleteAuthorityCheckpointBinding struct {
	CheckpointID           string
	JobID                  string
	AttemptID              string
	DeleteSetDigest        string
	TargetRevision         string
	NodeRevision           string
	RootRevision           string
	AttemptFence           uint64
	NodeFence              uint64
	AuthorizationExpiresAt time.Time
}

func (binding DeleteAuthorityCheckpointBinding) ValidateAt(now time.Time) error {
	if now.IsZero() || !validOpaqueID(binding.CheckpointID) || !validOpaqueID(binding.JobID) ||
		!validOpaqueID(binding.AttemptID) || !validDigest(binding.DeleteSetDigest) ||
		!validOpaqueRevision(binding.TargetRevision) || !validOpaqueRevision(binding.NodeRevision) ||
		!validOpaqueRevision(binding.RootRevision) || binding.AttemptFence == 0 || binding.NodeFence == 0 ||
		!binding.AuthorizationExpiresAt.After(now) {
		return ErrInvalidDeleteAuthorityBinding
	}
	return nil
}

// ExactMirrorDeleteGrantBinding is the consumed, hash-only one-use grant
// product. Its checkpoint ID transitively binds the paused node/root revisions
// while the duplicated attempt, fence, delete-set, and target fields prevent a
// grant from being substituted across a changed execution boundary.
type ExactMirrorDeleteGrantBinding struct {
	GrantID         string
	Category        AuthorityCategory
	BindingDigest   string
	JobID           string
	CheckpointID    string
	AttemptID       string
	DeleteSetDigest string
	TargetRevision  string
	AttemptFence    uint64
	NodeFence       uint64
	ExpiresAt       time.Time
	ConsumedAt      time.Time
	RevokedAt       *time.Time
}

// ConsumedDeleteAuthorityBinding is the durable second-checkpoint product.
// ValidateAt requires an injected caller time so tests and consumers never
// depend on a fixed calendar date or the process wall clock.
type ConsumedDeleteAuthorityBinding struct {
	CheckpointID string
	Required     DeleteAuthorityCheckpointBinding
	Grant        ExactMirrorDeleteGrantBinding
}

func (binding ConsumedDeleteAuthorityBinding) ValidateAt(now time.Time) error {
	if now.IsZero() || !validOpaqueID(binding.CheckpointID) ||
		binding.CheckpointID == binding.Required.CheckpointID || binding.Required.ValidateAt(now) != nil ||
		!validOpaqueID(binding.Grant.GrantID) || binding.Grant.Category != AuthorityExactMirrorDelete ||
		!validDigest(binding.Grant.BindingDigest) || !validOpaqueID(binding.Grant.JobID) ||
		!validOpaqueID(binding.Grant.CheckpointID) || !validOpaqueID(binding.Grant.AttemptID) ||
		!validDigest(binding.Grant.DeleteSetDigest) || !validOpaqueRevision(binding.Grant.TargetRevision) ||
		binding.Grant.AttemptFence == 0 || binding.Grant.NodeFence == 0 || binding.Grant.RevokedAt != nil ||
		binding.Grant.ConsumedAt.IsZero() || binding.Grant.ConsumedAt.After(now) ||
		!binding.Grant.ExpiresAt.After(now) || !binding.Grant.ExpiresAt.After(binding.Grant.ConsumedAt) ||
		binding.Grant.ExpiresAt.After(binding.Required.AuthorizationExpiresAt) {
		return ErrInvalidDeleteAuthorityBinding
	}
	if binding.Grant.JobID != binding.Required.JobID ||
		binding.Grant.CheckpointID != binding.Required.CheckpointID ||
		binding.Grant.AttemptID != binding.Required.AttemptID ||
		binding.Grant.DeleteSetDigest != binding.Required.DeleteSetDigest ||
		binding.Grant.TargetRevision != binding.Required.TargetRevision ||
		binding.Grant.AttemptFence != binding.Required.AttemptFence ||
		binding.Grant.NodeFence != binding.Required.NodeFence {
		return ErrInvalidDeleteAuthorityBinding
	}
	return nil
}

// FrozenJobBinding preserves the exact plan, preflight, and consumed write
// authority products used to create a durable job.
type FrozenJobBinding struct {
	Plan      PlanBinding
	Preflight PreflightBinding
	Authority AuthorityBinding
}

func (binding FrozenJobBinding) ValidateAt(now time.Time) error {
	if binding.Plan.Validate() != nil || binding.Preflight.ValidateAt(now) != nil ||
		binding.Authority.ValidateAt(now) != nil || binding.Authority.Category != AuthorityWrite ||
		(binding.Plan.SecurityDecision.Kind != SecurityDecisionAllowClean &&
			binding.Plan.SecurityDecision.Kind != SecurityDecisionAdminOverride) {
		return ErrInvalidFrozenJobBinding
	}
	if binding.Plan.SourceRevisionDigest != binding.Preflight.SourceRevisionDigest ||
		binding.Plan.PreflightRevision != binding.Preflight.Revision ||
		binding.Plan.Target.NodeID != binding.Preflight.TargetNodeID ||
		binding.Plan.Target.BaseNodeRevision != binding.Preflight.NodeRevision ||
		binding.Plan.Target.RootID != binding.Preflight.RootID ||
		binding.Plan.Target.RootLocatorDigest != binding.Preflight.RootLocatorDigest ||
		binding.Plan.Target.PathDigest != binding.Preflight.PathDigest ||
		binding.Plan.CapabilityRevision != binding.Preflight.CapabilityRevision ||
		binding.Plan.SecurityDecision.PolicyRevision != binding.Preflight.PolicyRevision ||
		binding.Plan.SecurityDecision.FindingSetDigest != binding.Preflight.FindingSetDigest ||
		binding.Plan.OperationSetDigest != binding.Preflight.OperationSetDigest ||
		binding.Plan.DeleteSetDigest != binding.Preflight.DeleteSetDigest {
		return ErrInvalidFrozenJobBinding
	}
	if observation := binding.Plan.SourceRevision.MutableObservation; observation != nil &&
		(observation.ObservedAt.After(now) || !binding.Preflight.ExpiresAt.After(observation.ObservedAt)) {
		return ErrInvalidFrozenJobBinding
	}
	return nil
}

// FrozenCheckpointBinding is the compact immutable authority tuple copied from
// the owning job into every checkpoint.
type FrozenCheckpointBinding struct {
	PlanBindingDigest        string
	SourceRevisionDigest     string
	PreflightID              string
	PreflightRevision        string
	PreflightExpiresAt       time.Time
	SecurityDecision         SecurityDecisionKind
	SecurityDecisionDigest   string
	SecurityFindingSetDigest string
	SecurityPolicyRevision   string
	AuthorityGrantID         string
	JobAuthorityCategory     AuthorityCategory
	AuthorityBindingDigest   string
	AuthorityExpiresAt       time.Time
}

// ResultPublicationBinding is the cross-state product required before an
// isolated workspace can become visible through a ready ResultSet.
type ResultPublicationBinding struct {
	TargetMode                        TargetMode
	JobState                          JobState
	WorkspacePhase                    WorkspacePhase
	HasActiveAttempt                  bool
	WorkspaceMarkerBindingDigest      string
	ResultSetMarkerBindingDigest      string
	WorkspacePlaintextDeadline        time.Time
	InitialResultPlaintextDeadline    time.Time
	ResultPlaintextRetentionHardLimit time.Time
}

func (binding ResultPublicationBinding) ValidateAt(now time.Time) error {
	if now.IsZero() || binding.TargetMode != TargetModeIsolated ||
		(binding.JobState != JobStateSucceeded && binding.JobState != JobStateDegraded) ||
		binding.WorkspacePhase != WorkspacePhasePublished || binding.HasActiveAttempt ||
		!validDigest(binding.WorkspaceMarkerBindingDigest) ||
		binding.WorkspaceMarkerBindingDigest != binding.ResultSetMarkerBindingDigest ||
		!binding.WorkspacePlaintextDeadline.After(now) ||
		!binding.InitialResultPlaintextDeadline.Equal(binding.WorkspacePlaintextDeadline) ||
		binding.ResultPlaintextRetentionHardLimit.Before(binding.InitialResultPlaintextDeadline) {
		return ErrInvalidResultPublication
	}
	return nil
}

func (binding ResultPublicationBinding) CanRetainUntil(deadline time.Time) bool {
	return deadline.After(binding.InitialResultPlaintextDeadline) &&
		!deadline.After(binding.ResultPlaintextRetentionHardLimit)
}

type RecoveryResultClassification string

const (
	RecoveryResultClassificationNonSecret RecoveryResultClassification = "non_secret"
	RecoveryResultClassificationSecret    RecoveryResultClassification = "secret"
	RecoveryResultClassificationUnknown   RecoveryResultClassification = "unknown"
)

type RecoveryResultClassificationBinding struct {
	Kind           RecoveryResultClassification
	Revision       int64
	SourceRevision int64
}

func (binding RecoveryResultClassificationBinding) valid() bool {
	switch binding.Kind {
	case RecoveryResultClassificationNonSecret, RecoveryResultClassificationSecret,
		RecoveryResultClassificationUnknown:
		return binding.Revision > 0 && binding.SourceRevision > 0
	default:
		return false
	}
}

// RecoveryResultContentBinding freezes every authority and classification fact
// required before Content may issue a RecoveryResult download grant.
type RecoveryResultContentBinding struct {
	SessionRole          string
	OwnerMatches         bool
	StepUpAction         string
	HasStepUpProof       bool
	TargetMode           TargetMode
	JobState             JobState
	WorkspacePhase       WorkspacePhase
	ResultSetState       ResultSetState
	SecurityDecision     SecurityDecisionKind
	AuthorityCategory    AuthorityCategory
	ResultClassification RecoveryResultClassificationBinding
	GrantClassification  RecoveryResultClassificationBinding
}

func (binding RecoveryResultContentBinding) Validate() error {
	approvedSecurityDecision := binding.SecurityDecision == SecurityDecisionAllowClean ||
		binding.SecurityDecision == SecurityDecisionAdminOverride
	if binding.SessionRole != "admin" || !binding.OwnerMatches ||
		binding.StepUpAction != "recovery.result_download" || !binding.HasStepUpProof ||
		binding.TargetMode != TargetModeIsolated ||
		(binding.JobState != JobStateSucceeded && binding.JobState != JobStateDegraded) ||
		binding.WorkspacePhase != WorkspacePhasePublished || binding.ResultSetState != ResultSetStateReady ||
		!approvedSecurityDecision || binding.AuthorityCategory != AuthorityWrite ||
		!binding.ResultClassification.valid() ||
		binding.ResultClassification != binding.GrantClassification {
		return ErrInvalidResultContent
	}
	return nil
}

func (binding FrozenCheckpointBinding) ValidateAgainst(job FrozenJobBinding) error {
	if !validDigest(binding.PlanBindingDigest) || !validDigest(binding.SourceRevisionDigest) ||
		!validOpaqueID(binding.PreflightID) || !validOpaqueRevision(binding.PreflightRevision) || binding.PreflightExpiresAt.IsZero() ||
		!validDigest(binding.SecurityDecisionDigest) || !validDigest(binding.SecurityFindingSetDigest) ||
		!validOpaqueRevision(binding.SecurityPolicyRevision) || !validOpaqueID(binding.AuthorityGrantID) ||
		binding.JobAuthorityCategory.Validate() != nil || !validDigest(binding.AuthorityBindingDigest) ||
		binding.AuthorityExpiresAt.IsZero() {
		return ErrInvalidCheckpointBinding
	}
	if binding.PlanBindingDigest != job.Plan.PlanDigest ||
		binding.SourceRevisionDigest != job.Plan.SourceRevisionDigest ||
		binding.PreflightID != job.Preflight.ID || binding.PreflightRevision != job.Preflight.Revision ||
		!binding.PreflightExpiresAt.Equal(job.Preflight.ExpiresAt) ||
		binding.SecurityDecision != job.Plan.SecurityDecision.Kind ||
		binding.SecurityDecisionDigest != job.Plan.SecurityDecision.DecisionDigest ||
		binding.SecurityFindingSetDigest != job.Plan.SecurityDecision.FindingSetDigest ||
		binding.SecurityPolicyRevision != job.Plan.SecurityDecision.PolicyRevision ||
		binding.AuthorityGrantID != job.Authority.GrantID ||
		binding.JobAuthorityCategory != job.Authority.Category ||
		binding.AuthorityBindingDigest != job.Authority.BindingDigest ||
		!binding.AuthorityExpiresAt.Equal(job.Authority.ExpiresAt) {
		return ErrInvalidCheckpointBinding
	}
	return nil
}

// RecoveryPlan keeps the expiring preflight boundary separate from the frozen
// binding so callers must validate the expiry at their own clock boundary.
type RecoveryPlan struct {
	Binding            PlanBinding
	PreflightExpiresAt time.Time
}

func (plan RecoveryPlan) ValidateAt(now time.Time) error {
	if now.IsZero() || plan.Binding.Validate() != nil || !plan.PreflightExpiresAt.After(now) {
		return ErrInvalidRecoveryPlan
	}
	if observation := plan.Binding.SourceRevision.MutableObservation; observation != nil &&
		(observation.ObservedAt.After(now) || !plan.PreflightExpiresAt.After(observation.ObservedAt)) {
		return ErrInvalidRecoveryPlan
	}
	return nil
}

func nonEmpty(value string) bool {
	return strings.TrimSpace(value) != ""
}

func validOpaqueID(value string) bool {
	return backupasset.ValidateOpaqueID(value) == nil
}

func validOpaqueRevision(value string) bool {
	return validBoundedOpaque(value, opaqueRevisionMax)
}

func validBoundedOpaque(value string, maxLength int) bool {
	return maxLength > 0 && len(value) <= maxLength && utf8.ValidString(value) && nonEmpty(value)
}

func sha256Shaped(value string) bool {
	if len(value) != sha256DigestLength {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') &&
			(character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != sha256DigestLength {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
