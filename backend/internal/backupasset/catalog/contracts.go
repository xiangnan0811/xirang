package catalog

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

var (
	ErrUnknownInternalState     = errors.New("unknown catalog internal state")
	ErrInvalidCatalogContract   = errors.New("invalid catalog contract")
	ErrInvalidAssetReference    = errors.New("invalid catalog asset reference")
	ErrUnsafeEntryPath          = errors.New("unsafe catalog entry path")
	ErrIdentityKeyUnavailable   = errors.New("catalog identity key unavailable")
	ErrDuplicateEntry           = errors.New("duplicate catalog entry")
	ErrIdentityCollision        = errors.New("catalog entry identity collision")
	ErrInvalidCursor            = errors.New("invalid catalog cursor")
	ErrStaleCursor              = errors.New("stale catalog cursor")
	ErrCatalogProofMismatch     = errors.New("catalog proof mismatch")
	ErrCatalogSourceChanged     = errors.New("catalog source changed")
	ErrCatalogBuildLimit        = errors.New("catalog build limit reached")
	ErrOwnershipProjectionLimit = errors.New("catalog ownership projection limit reached")
	ErrCatalogUnavailable       = errors.New("catalog unavailable")
	ErrFeatureDisabled          = errors.New("catalog feature disabled")
)

type GenerationState string

const (
	GenerationBuilding   GenerationState = "building"
	GenerationComplete   GenerationState = "complete"
	GenerationPartial    GenerationState = "partial"
	GenerationFailed     GenerationState = "failed"
	GenerationSuperseded GenerationState = "superseded"
)

type GenerationErrorCode string

const (
	GenerationErrorNone                   GenerationErrorCode = ""
	GenerationErrorBuildAbandoned         GenerationErrorCode = "catalog_build_abandoned"
	GenerationErrorBuildFailed            GenerationErrorCode = "catalog_build_failed"
	GenerationErrorBuildIncomplete        GenerationErrorCode = "catalog_build_incomplete"
	GenerationErrorBuildLimit             GenerationErrorCode = "catalog_build_limit"
	GenerationErrorBuildTimeout           GenerationErrorCode = "catalog_build_timeout"
	GenerationErrorIdentityKeyUnavailable GenerationErrorCode = "catalog_identity_key_unavailable"
	GenerationErrorInvalidRecord          GenerationErrorCode = "catalog_invalid_record"
	GenerationErrorProjectionMismatch     GenerationErrorCode = "catalog_projection_mismatch"
	GenerationErrorProofMismatch          GenerationErrorCode = "catalog_proof_mismatch"
	GenerationErrorProviderResourceLimit  GenerationErrorCode = "catalog_provider_resource_limit"
	GenerationErrorProviderTimeout        GenerationErrorCode = "catalog_provider_timeout"
	GenerationErrorProviderUnavailable    GenerationErrorCode = "catalog_provider_unavailable"
	GenerationErrorSourceChanged          GenerationErrorCode = "catalog_source_changed"
)

type CoverageStatus string

const (
	CoverageBuilding    CoverageStatus = "building"
	CoverageComplete    CoverageStatus = "complete"
	CoveragePartial     CoverageStatus = "partial"
	CoverageFailed      CoverageStatus = "failed"
	CoverageUnavailable CoverageStatus = "unavailable"
)

type StalenessStatus string

const (
	StalenessFresh   StalenessStatus = "fresh"
	StalenessStale   StalenessStatus = "stale"
	StalenessUnknown StalenessStatus = "unknown"
)

type FingerprintStrength string

const (
	FingerprintStrong FingerprintStrength = "strong"
	FingerprintWeak   FingerprintStrength = "weak"
	FingerprintNone   FingerprintStrength = "none"
)

type EvidenceLayerStatus string

const (
	EvidenceRecorded    EvidenceLayerStatus = "recorded"
	EvidenceUnavailable EvidenceLayerStatus = "unavailable"
	EvidenceNotRecorded EvidenceLayerStatus = "not_recorded"
	EvidenceInvalid     EvidenceLayerStatus = "invalid"
)

type DiffChangeKind string

const (
	DiffAdded       DiffChangeKind = "added"
	DiffRemoved     DiffChangeKind = "removed"
	DiffModified    DiffChangeKind = "modified"
	DiffTypeChanged DiffChangeKind = "type_changed"
)

type RepositorySort string

const RepositorySortCreatedDesc = "created_desc"

type RecoveryPointSort string

const (
	RecoveryPointSortCapturedDesc = "captured_desc"
	RecoveryPointSortCapturedAsc  = "captured_asc"
	RecoveryPointSortCreatedDesc  = "created_desc"
)

type EntrySort string

const (
	EntrySortNameAsc      = "name_asc"
	EntrySortNameDesc     = "name_desc"
	EntrySortSizeDesc     = "size_desc"
	EntrySortModifiedDesc = "modified_desc"
)

type DiffSort string

const DiffSortPathAsc = "path_asc"

func ParseGenerationState(value string) (GenerationState, error) {
	parsed := GenerationState(value)
	if !validGenerationStates[parsed] {
		return "", fmt.Errorf("%w: generation state", ErrUnknownInternalState)
	}
	return parsed, nil
}

func ParseGenerationErrorCode(value string) (GenerationErrorCode, error) {
	parsed := GenerationErrorCode(value)
	if !validGenerationErrorCodes[parsed] {
		return "", fmt.Errorf("%w: generation error code", ErrUnknownInternalState)
	}
	return parsed, nil
}

func ParseCoverageStatus(value string) (CoverageStatus, error) {
	parsed := CoverageStatus(value)
	if !validCoverageStatuses[parsed] {
		return "", fmt.Errorf("%w: coverage status", ErrUnknownInternalState)
	}
	return parsed, nil
}

func ParseStalenessStatus(value string) (StalenessStatus, error) {
	parsed := StalenessStatus(value)
	if !validStalenessStatuses[parsed] {
		return "", fmt.Errorf("%w: staleness status", ErrUnknownInternalState)
	}
	return parsed, nil
}

func ParseFingerprintStrength(value string) (FingerprintStrength, error) {
	parsed := FingerprintStrength(value)
	if !validFingerprintStrengths[parsed] {
		return "", fmt.Errorf("%w: fingerprint strength", ErrUnknownInternalState)
	}
	return parsed, nil
}

func ParseEvidenceLayerStatus(value string) (EvidenceLayerStatus, error) {
	parsed := EvidenceLayerStatus(value)
	if !validEvidenceLayerStatuses[parsed] {
		return "", fmt.Errorf("%w: evidence layer status", ErrUnknownInternalState)
	}
	return parsed, nil
}

func ParseDiffChangeKind(value string) (DiffChangeKind, error) {
	parsed := DiffChangeKind(value)
	if !validDiffChangeKinds[parsed] {
		return "", fmt.Errorf("%w: diff change kind", ErrUnknownInternalState)
	}
	return parsed, nil
}

type GenerationDTO struct {
	ID            string              `json:"id"`
	Sequence      int                 `json:"sequence"`
	State         GenerationState     `json:"state"`
	StartedAt     time.Time           `json:"started_at"`
	FinishedAt    *time.Time          `json:"finished_at"`
	ErrorCode     GenerationErrorCode `json:"error_code"`
	CorrelationID string              `json:"correlation_id"`
}

type CoverageDTO struct {
	Status          CoverageStatus `json:"status"`
	IndexedEntries  int64          `json:"indexed_entries"`
	ExpectedEntries *int64         `json:"expected_entries"`
	ManifestDigest  string         `json:"manifest_digest"`
	ObservedAt      time.Time      `json:"observed_at"`
}

type StalenessDTO struct {
	Status     StalenessStatus               `json:"status"`
	ObservedAt *time.Time                    `json:"observed_at"`
	Reason     *backupasset.CapabilityReason `json:"reason"`
}

type ContentAvailabilityDTO struct {
	Available bool                          `json:"available"`
	Reason    *backupasset.CapabilityReason `json:"reason"`
}

type PermissionsDTO struct {
	List     bool `json:"list"`
	Preview  bool `json:"preview"`
	Download bool `json:"download"`
}

type StatusDTO struct {
	Generation          *GenerationDTO         `json:"generation"`
	LatestBuild         *GenerationDTO         `json:"latest_build"`
	Coverage            CoverageDTO            `json:"coverage"`
	Staleness           StalenessDTO           `json:"staleness"`
	ContentAvailability ContentAvailabilityDTO `json:"content_availability"`
	Permissions         PermissionsDTO         `json:"permissions"`
}

func (status StatusDTO) Validate() error {
	if status.Generation != nil {
		if err := status.Generation.validate(); err != nil {
			return err
		}
		if status.Generation.State != GenerationComplete {
			return fmt.Errorf("%w: active generation must be complete", ErrInvalidCatalogContract)
		}
	}
	if status.LatestBuild != nil {
		if err := status.LatestBuild.validate(); err != nil {
			return err
		}
	}
	if !validCoverageStatuses[status.Coverage.Status] || status.Coverage.IndexedEntries < 0 ||
		(status.Coverage.ExpectedEntries != nil && *status.Coverage.ExpectedEntries < 0) ||
		(!status.Coverage.ObservedAt.IsZero() && !isUTC(status.Coverage.ObservedAt)) ||
		(status.Coverage.ManifestDigest != "" && !lowerHexLength(status.Coverage.ManifestDigest, 64, 128)) {
		return fmt.Errorf("%w: invalid coverage", ErrInvalidCatalogContract)
	}
	if status.Coverage.Status == CoverageComplete && status.Generation == nil {
		return fmt.Errorf("%w: complete coverage requires an active generation", ErrInvalidCatalogContract)
	}
	if !validStalenessStatuses[status.Staleness.Status] ||
		(status.Staleness.ObservedAt != nil && !isUTC(*status.Staleness.ObservedAt)) {
		return fmt.Errorf("%w: invalid staleness", ErrInvalidCatalogContract)
	}
	if status.Staleness.Reason != nil {
		if err := backupasset.ValidateCapabilityReason(*status.Staleness.Reason); err != nil {
			return fmt.Errorf("%w: invalid staleness reason", ErrInvalidCatalogContract)
		}
	}
	if status.ContentAvailability.Reason != nil {
		if err := backupasset.ValidateCapabilityReason(*status.ContentAvailability.Reason); err != nil {
			return fmt.Errorf("%w: invalid content availability reason", ErrInvalidCatalogContract)
		}
	}
	if status.ContentAvailability.Available && status.ContentAvailability.Reason != nil {
		return fmt.Errorf("%w: available content cannot carry an unavailable reason", ErrInvalidCatalogContract)
	}
	return nil
}

func (generation GenerationDTO) validate() error {
	if backupasset.ValidateOpaqueID(generation.ID) != nil || generation.Sequence <= 0 ||
		!validGenerationStates[generation.State] || generation.StartedAt.IsZero() || !isUTC(generation.StartedAt) ||
		(generation.FinishedAt != nil && !isUTC(*generation.FinishedAt)) ||
		!validGenerationErrorCodes[generation.ErrorCode] || strings.ContainsAny(generation.CorrelationID, "\r\n\x00") {
		return fmt.Errorf("%w: invalid generation", ErrInvalidCatalogContract)
	}
	return nil
}

type BreadcrumbDTO struct {
	RecoveryPointID string `json:"recovery_point_id"`
	EntryID         string `json:"entry_id"`
	Name            string `json:"name"`
}

type EntryVersionDTO struct {
	RecoveryPointID string                       `json:"recovery_point_id"`
	EntryID         string                       `json:"entry_id"`
	CapturedAt      *time.Time                   `json:"captured_at"`
	Size            int64                        `json:"size"`
	EntryType       backupasset.CatalogEntryType `json:"entry_type"`
}

func (version EntryVersionDTO) Validate() error {
	if backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: version.RecoveryPointID, EntryID: version.EntryID}) != nil ||
		version.Size < 0 || !validCatalogEntryType(version.EntryType) ||
		(version.CapturedAt != nil && !isUTC(*version.CapturedAt)) {
		return fmt.Errorf("%w: invalid entry version DTO", ErrInvalidCatalogContract)
	}
	return nil
}

type EntryVersionPage struct {
	Items []EntryVersionDTO `json:"items"`
}

type EntryDTO struct {
	RecoveryPointID     string                       `json:"recovery_point_id"`
	EntryID             string                       `json:"entry_id"`
	ParentEntryID       *string                      `json:"parent_entry_id"`
	Name                string                       `json:"name"`
	EntryType           backupasset.CatalogEntryType `json:"entry_type"`
	Size                int64                        `json:"size"`
	ModifiedAt          *time.Time                   `json:"modified_at"`
	Mode                string                       `json:"mode"`
	Owner               string                       `json:"owner"`
	MIMEType            string                       `json:"mime_type"`
	FingerprintStrength FingerprintStrength          `json:"fingerprint_strength"`
	Breadcrumb          []BreadcrumbDTO              `json:"breadcrumb,omitempty"`
}

func (entry EntryDTO) Validate() error {
	if backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: entry.RecoveryPointID, EntryID: entry.EntryID}) != nil ||
		(entry.ParentEntryID != nil && !lowerHexLength(*entry.ParentEntryID, 64)) ||
		strings.TrimSpace(entry.Name) == "" || strings.ContainsRune(entry.Name, '\x00') || len(entry.Name) > 4096 ||
		entry.Size < 0 || !validCatalogEntryType(entry.EntryType) || !validFingerprintStrengths[entry.FingerprintStrength] ||
		(entry.ModifiedAt != nil && !isUTC(*entry.ModifiedAt)) {
		return fmt.Errorf("%w: invalid entry DTO", ErrInvalidCatalogContract)
	}
	for _, crumb := range entry.Breadcrumb {
		if backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: crumb.RecoveryPointID, EntryID: crumb.EntryID}) != nil ||
			crumb.RecoveryPointID != entry.RecoveryPointID || strings.TrimSpace(crumb.Name) == "" || strings.ContainsRune(crumb.Name, '\x00') {
			return fmt.Errorf("%w: invalid breadcrumb", ErrInvalidCatalogContract)
		}
	}
	return nil
}

func validCatalogEntryType(value backupasset.CatalogEntryType) bool {
	switch value {
	case backupasset.CatalogEntryFile, backupasset.CatalogEntryDirectory, backupasset.CatalogEntrySymlink,
		backupasset.CatalogEntryHardlink, backupasset.CatalogEntrySpecial, backupasset.CatalogEntryUnknown:
		return true
	default:
		return false
	}
}

func isUTC(value time.Time) bool {
	return value.Location() == time.UTC
}

func lowerHexLength(value string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		if len(value) == length {
			validLength = true
			break
		}
	}
	if !validLength {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

var (
	validGenerationStates = map[GenerationState]bool{
		GenerationBuilding: true, GenerationComplete: true, GenerationPartial: true,
		GenerationFailed: true, GenerationSuperseded: true,
	}
	validGenerationErrorCodes = map[GenerationErrorCode]bool{
		GenerationErrorNone: true, GenerationErrorBuildAbandoned: true, GenerationErrorBuildFailed: true,
		GenerationErrorBuildIncomplete: true, GenerationErrorBuildLimit: true, GenerationErrorBuildTimeout: true,
		GenerationErrorIdentityKeyUnavailable: true, GenerationErrorInvalidRecord: true,
		GenerationErrorProjectionMismatch: true, GenerationErrorProofMismatch: true,
		GenerationErrorProviderResourceLimit: true, GenerationErrorProviderTimeout: true,
		GenerationErrorProviderUnavailable: true, GenerationErrorSourceChanged: true,
	}
	validCoverageStatuses = map[CoverageStatus]bool{
		CoverageBuilding: true, CoverageComplete: true, CoveragePartial: true,
		CoverageFailed: true, CoverageUnavailable: true,
	}
	validStalenessStatuses = map[StalenessStatus]bool{
		StalenessFresh: true, StalenessStale: true, StalenessUnknown: true,
	}
	validFingerprintStrengths = map[FingerprintStrength]bool{
		FingerprintStrong: true, FingerprintWeak: true, FingerprintNone: true,
	}
	validEvidenceLayerStatuses = map[EvidenceLayerStatus]bool{
		EvidenceRecorded: true, EvidenceUnavailable: true, EvidenceNotRecorded: true, EvidenceInvalid: true,
	}
	validDiffChangeKinds = map[DiffChangeKind]bool{
		DiffAdded: true, DiffRemoved: true, DiffModified: true, DiffTypeChanged: true,
	}
)
