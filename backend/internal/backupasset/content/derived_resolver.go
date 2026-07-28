package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	workerCapabilities "xirang/backend/internal/backupasset/processing/capabilities"
	"xirang/backend/internal/model"

	"golang.org/x/text/unicode/norm"

	"gorm.io/gorm"
)

var ErrDerivedRepresentationUnavailable = errors.New("derived representation unavailable")

type DerivedArtifactRead struct {
	ArtifactID          string `json:"-"`
	RecoveryPointID     string `json:"-"`
	CatalogGenerationID string `json:"-"`
	EntryID             string `json:"-"`
	SourceFingerprint   string `json:"-"`
}

type DerivedArtifactReadFunc func(context.Context, DerivedArtifactRead, io.Writer) error

type DerivedPipelineFingerprintSource func(context.Context, string, string) (string, error)

type DerivedMalwareSafetySource func(context.Context, AuthorizedAsset) (bool, error)

type derivedDeliveryIntent uint8

const (
	derivedIntentNone derivedDeliveryIntent = iota
	derivedIntentExtractedText
	derivedIntentImageThumbnail
	derivedIntentDocumentPage
	derivedIntentAudioPreview
	derivedIntentVideoPreview
	derivedIntentArchiveIndex
	maximumDerivedArchiveIndexBytes = 16 << 20
	maximumDerivedArchiveEntries    = 100_000
	maximumDerivedArchiveExpanded   = 8 << 30
	maximumDerivedArchiveMember     = 256 << 20
)

type derivedArtifactContract struct {
	capability       string
	capabilitySchema string
	outputProfile    string
	ordinal          int
	role             string
	mediaTypes       []string
}

type DerivedRepresentationRequest struct {
	Ref                        backupasset.AssetRef
	CatalogGenerationID        string
	SourceFingerprint          string
	SourceEntryFingerprint     string
	FingerprintStrength        string
	ProviderCapabilityRevision int64
	SourceSize                 int64
	SourceMediaType            string
	ExpectedEntryFingerprint   string
	SecurityPolicyRevision     string
	Provider                   backupasset.ProviderKind
	Renderer                   Renderer
	Profile                    RendererProfile
	intent                     derivedDeliveryIntent
}

type DerivedRepresentation struct {
	artifactID                 string                   `json:"-"`
	artifactSetID              string                   `json:"-"`
	blobID                     string                   `json:"-"`
	setCompleteness            string                   `json:"-"`
	Ref                        backupasset.AssetRef     `json:"-"`
	CatalogGenerationID        string                   `json:"-"`
	SourceFingerprint          string                   `json:"-"`
	SecurityPolicyRevision     string                   `json:"-"`
	Provider                   backupasset.ProviderKind `json:"-"`
	Renderer                   Renderer                 `json:"-"`
	Profile                    RendererProfile          `json:"-"`
	Role                       string                   `json:"-"`
	MediaType                  string                   `json:"-"`
	Size                       int64                    `json:"-"`
	EntryFingerprint           string                   `json:"-"`
	Completeness               string                   `json:"-"`
	ModifiedAt                 *time.Time               `json:"-"`
	capability                 string                   `json:"-"`
	capabilitySchema           string                   `json:"-"`
	pipelineFingerprint        string                   `json:"-"`
	outputProfile              string                   `json:"-"`
	sourceEntryFingerprint     string                   `json:"-"`
	fingerprintStrength        string                   `json:"-"`
	providerCapabilityRevision int64                    `json:"-"`
	sourceSize                 int64                    `json:"-"`
	sourceMediaType            string                   `json:"-"`
	intent                     derivedDeliveryIntent    `json:"-"`
	ordinal                    int                      `json:"-"`
}

type DerivedRepresentationResolver struct {
	db             *gorm.DB
	read           DerivedArtifactReadFunc
	activePipeline DerivedPipelineFingerprintSource
	malwareSafety  DerivedMalwareSafetySource
}

type ArchiveIndexRequest struct {
	Asset                  AuthorizedAsset
	SecurityPolicyRevision string
	ExpectedRevision       string
}

type ArchiveIndexEntry struct {
	ID          string `json:"id"`
	ParentID    string `json:"parent_id,omitempty"`
	DisplayName string `json:"display_name"`
	Size        int64  `json:"size"`
	MediaType   string `json:"media_type"`
}

type ResolvedArchiveMember struct {
	Ordinal   int
	Digest    string
	Size      int64
	MediaType string
}

type ResolvedArchiveIndex struct {
	SchemaVersion int                 `json:"schema_version"`
	IndexRevision string              `json:"index_revision"`
	Entries       []ArchiveIndexEntry `json:"entries"`

	artifactID             string
	artifactSetID          string
	blobID                 string
	pipelineFingerprint    string
	securityPolicyRevision string
	ref                    backupasset.AssetRef
	ordinals               map[string]int
}

type ArchiveMemberArtifactRequest struct {
	RequestID   string
	OwnerUserID uint
	Asset       AuthorizedAsset
}

type ResolvedArchiveMemberArtifact struct {
	MemberRequestID            string                   `json:"-"`
	OwnerUserID                uint                     `json:"-"`
	Ref                        backupasset.AssetRef     `json:"-"`
	CatalogGenerationID        string                   `json:"-"`
	SourceFingerprint          string                   `json:"-"`
	EntryFingerprint           string                   `json:"-"`
	MemberChainDigest          string                   `json:"-"`
	ProcessingJobID            string                   `json:"-"`
	ProcessingAttemptID        string                   `json:"-"`
	DerivedArtifactSetID       string                   `json:"-"`
	DerivedArtifactID          string                   `json:"-"`
	DerivedBlobID              string                   `json:"-"`
	DerivedDigest              string                   `json:"-"`
	DerivedSize                int64                    `json:"-"`
	MediaType                  string                   `json:"-"`
	AbsoluteExpiresAt          time.Time                `json:"-"`
	Provider                   backupasset.ProviderKind `json:"-"`
	ProviderCapabilityRevision int64                    `json:"-"`
	FingerprintStrength        string                   `json:"-"`
	SourceSize                 int64                    `json:"-"`
	SourceMediaType            string                   `json:"-"`
	SecurityPolicyRevision     string                   `json:"-"`
}

func (index ResolvedArchiveIndex) ArtifactID() string { return index.artifactID }

func (index ResolvedArchiveIndex) PipelineFingerprint() string { return index.pipelineFingerprint }

func (index ResolvedArchiveIndex) SecurityPolicyRevision() string {
	return index.securityPolicyRevision
}

func (index ResolvedArchiveIndex) ResolveMember(memberID string) (ResolvedArchiveMember, bool) {
	ordinal, ok := index.ordinals[memberID]
	if !ok || ordinal < 0 || ordinal >= len(index.Entries) || index.Entries[ordinal].ID != memberID {
		return ResolvedArchiveMember{}, false
	}
	entry := index.Entries[ordinal]
	return ResolvedArchiveMember{
		Ordinal: ordinal, Digest: ArchiveMemberChainDigest(index.ref, index.IndexRevision, memberID),
		Size: entry.Size, MediaType: entry.MediaType,
	}, true
}

func ArchiveMemberChainDigest(ref backupasset.AssetRef, indexRevision, memberID string) string {
	if backupasset.ValidateAssetRef(ref) != nil || !lowerHexContent(indexRevision, 64) || backupasset.ValidateOpaqueID(memberID) != nil {
		return ""
	}
	digest := sha256.New()
	digest.Write([]byte("xirang.backup_asset.archive_member.chain.v1\x00"))
	digest.Write([]byte(ref.RecoveryPointID))
	digest.Write([]byte{0})
	digest.Write([]byte(ref.EntryID))
	digest.Write([]byte{0})
	digest.Write([]byte(indexRevision))
	digest.Write([]byte{0})
	digest.Write([]byte(memberID))
	return hex.EncodeToString(digest.Sum(nil))
}

func NewDerivedRepresentationResolver(
	db *gorm.DB,
	read DerivedArtifactReadFunc,
	activePipeline DerivedPipelineFingerprintSource,
	malwareSafety DerivedMalwareSafetySource,
) (*DerivedRepresentationResolver, error) {
	if db == nil || read == nil || activePipeline == nil || malwareSafety == nil {
		return nil, ErrDerivedRepresentationUnavailable
	}
	return &DerivedRepresentationResolver{
		db: db, read: read, activePipeline: activePipeline, malwareSafety: malwareSafety,
	}, nil
}

func (resolver *DerivedRepresentationResolver) ResolveArchiveIndex(
	ctx context.Context,
	request ArchiveIndexRequest,
) (ResolvedArchiveIndex, error) {
	asset := request.Asset
	derivedRequest := DerivedRepresentationRequest{
		Ref: asset.Ref, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, SourceEntryFingerprint: asset.EntryFingerprint,
		FingerprintStrength: asset.FingerprintStrength, ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		SourceSize: asset.Size, SourceMediaType: asset.MediaType,
		ExpectedEntryFingerprint: request.ExpectedRevision,
		SecurityPolicyRevision:   request.SecurityPolicyRevision, Provider: asset.Provider,
		Renderer: RendererEscapedText, Profile: ProfileTextV1, intent: derivedIntentArchiveIndex,
	}
	binding, err := resolver.Resolve(nonNilContext(ctx), derivedRequest)
	if err != nil || binding.intent != derivedIntentArchiveIndex {
		return ResolvedArchiveIndex{}, errors.Join(ErrDerivedRepresentationUnavailable, err)
	}
	payload, err := resolver.loadArchiveIndexPayload(nonNilContext(ctx), binding)
	if err != nil {
		return ResolvedArchiveIndex{}, err
	}
	defer zeroBytes(payload)
	var decoded derivedArchiveIndex
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return ResolvedArchiveIndex{}, ErrDerivedRepresentationUnavailable
	}
	entries := make([]ArchiveIndexEntry, len(decoded.Entries))
	ordinals := make(map[string]int, len(decoded.Entries))
	for ordinal, entry := range decoded.Entries {
		entries[ordinal] = ArchiveIndexEntry(entry)
		ordinals[entry.ID] = ordinal
	}
	return ResolvedArchiveIndex{
		SchemaVersion: 1, IndexRevision: binding.EntryFingerprint, Entries: entries,
		artifactID: binding.artifactID, artifactSetID: binding.artifactSetID, blobID: binding.blobID,
		pipelineFingerprint:    binding.pipelineFingerprint,
		securityPolicyRevision: binding.SecurityPolicyRevision, ref: binding.Ref, ordinals: ordinals,
	}, nil
}

func (resolver *DerivedRepresentationResolver) ResolveArchiveMember(
	ctx context.Context,
	request ArchiveMemberArtifactRequest,
) (ResolvedArchiveMemberArtifact, error) {
	return resolver.resolveArchiveMember(ctx, request, "ready")
}

// ValidateArchiveMemberOutput resolves the exact succeeded Derived product
// while its durable member request is still running. Delivery must continue to
// use ResolveArchiveMember, which accepts only an already-ready request.
func (resolver *DerivedRepresentationResolver) ValidateArchiveMemberOutput(
	ctx context.Context,
	request ArchiveMemberArtifactRequest,
) (ResolvedArchiveMemberArtifact, error) {
	return resolver.resolveArchiveMember(ctx, request, "running")
}

func (resolver *DerivedRepresentationResolver) resolveArchiveMember(
	ctx context.Context,
	request ArchiveMemberArtifactRequest,
	requiredRequestState string,
) (ResolvedArchiveMemberArtifact, error) {
	if resolver == nil || resolver.db == nil || resolver.read == nil || request.OwnerUserID == 0 ||
		backupasset.ValidateOpaqueID(request.RequestID) != nil || !validArchiveMemberAuthorizedAsset(request.Asset) ||
		(requiredRequestState != "running" && requiredRequestState != "ready") {
		return ResolvedArchiveMemberArtifact{}, ErrDerivedRepresentationUnavailable
	}
	ctx = nonNilContext(ctx)
	var memberRequest model.BackupAssetArchiveMemberRequest
	requestResult := resolver.db.WithContext(ctx).
		Where("id = ? AND owner_user_id = ?", request.RequestID, request.OwnerUserID).
		Limit(1).Find(&memberRequest)
	if requestResult.Error != nil {
		return ResolvedArchiveMemberArtifact{}, fmt.Errorf("load archive member request: %w", requestResult.Error)
	}
	asset := request.Asset
	if requestResult.RowsAffected != 1 || memberRequest.State != requiredRequestState || memberRequest.ProcessingJobID == nil ||
		memberRequest.ProcessingInterestID == nil || memberRequest.RecoveryPointID != asset.Ref.RecoveryPointID ||
		memberRequest.EntryID != asset.Ref.EntryID || memberRequest.CatalogGenerationID != asset.CatalogGenerationID ||
		memberRequest.SourceFingerprint != asset.SourceFingerprint || memberRequest.EntryFingerprint != asset.EntryFingerprint ||
		!lowerHexContent(memberRequest.IndexRevision, 64) || !lowerHexContent(memberRequest.MemberChainDigest, 64) {
		return ResolvedArchiveMemberArtifact{}, ErrDerivedRepresentationUnavailable
	}
	var rows []derivedRepresentationRow
	rowsResult := resolver.baseQuery(ctx).
		Where("sets.job_id = ?", *memberRequest.ProcessingJobID).
		Order("artifacts.ordinal ASC").Limit(3).Scan(&rows)
	if rowsResult.Error != nil {
		return ResolvedArchiveMemberArtifact{}, fmt.Errorf("load archive member artifacts: %w", rowsResult.Error)
	}
	if len(rows) != 2 {
		return ResolvedArchiveMemberArtifact{}, ErrDerivedRepresentationUnavailable
	}
	validationRequest := DerivedRepresentationRequest{
		Ref: asset.Ref, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, SourceEntryFingerprint: asset.EntryFingerprint,
		FingerprintStrength: asset.FingerprintStrength, ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		SourceSize: asset.Size, SourceMediaType: asset.MediaType,
		SecurityPolicyRevision: rows[0].JobSecurityPolicyRevision, Provider: asset.Provider,
	}
	for _, row := range rows {
		active, activeErr := resolver.rowUsesActivePipeline(ctx, row)
		if activeErr != nil {
			return ResolvedArchiveMemberArtifact{}, activeErr
		}
		if !active || !validDerivedRepresentationRow(row, validationRequest) ||
			row.Capability != "archive.extract_entry" || row.CapabilitySchema != "archive.extract_entry.v1" ||
			row.OutputProfile != "archive_member_v1" || row.SetCompleteness != "complete" || row.Completeness != "complete" {
			return ResolvedArchiveMemberArtifact{}, ErrDerivedRepresentationUnavailable
		}
	}
	contentRow, metadataRow := rows[0], rows[1]
	if contentRow.Ordinal != 0 || contentRow.Role != "content" || metadataRow.Ordinal != 1 ||
		metadataRow.Role != "metadata" || metadataRow.MediaType != "application/json" ||
		contentRow.ArtifactSetID != metadataRow.ArtifactSetID || contentRow.AttemptID != metadataRow.AttemptID ||
		contentRow.PlaintextSize < 0 || contentRow.PlaintextSize > maximumDerivedArchiveMember {
		return ResolvedArchiveMemberArtifact{}, ErrDerivedRepresentationUnavailable
	}
	var job model.BackupAssetProcessingJob
	jobResult := resolver.db.WithContext(ctx).Where("id = ?", *memberRequest.ProcessingJobID).Limit(1).Find(&job)
	if jobResult.Error != nil || jobResult.RowsAffected != 1 || !archiveMemberDescriptorOrdinal(job.DescriptorCanonical, memberRequest.ResolvedOrdinal) {
		return ResolvedArchiveMemberArtifact{}, errors.Join(ErrDerivedRepresentationUnavailable, jobResult.Error)
	}
	metadata, err := resolver.loadArchiveMemberMetadata(ctx, metadataRow)
	if err != nil || metadata.Size != contentRow.PlaintextSize || metadata.MediaType != contentRow.MediaType ||
		ArchiveMemberChainDigest(asset.Ref, memberRequest.IndexRevision, metadata.MemberID) != memberRequest.MemberChainDigest {
		return ResolvedArchiveMemberArtifact{}, errors.Join(ErrDerivedRepresentationUnavailable, err)
	}
	return ResolvedArchiveMemberArtifact{
		MemberRequestID: memberRequest.ID, OwnerUserID: memberRequest.OwnerUserID,
		Ref: asset.Ref, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		MemberChainDigest: memberRequest.MemberChainDigest,
		ProcessingJobID:   *memberRequest.ProcessingJobID, ProcessingAttemptID: contentRow.AttemptID,
		DerivedArtifactSetID: contentRow.ArtifactSetID, DerivedArtifactID: contentRow.ArtifactID,
		DerivedBlobID: contentRow.BlobID, DerivedDigest: contentRow.PlaintextDigest,
		DerivedSize: contentRow.PlaintextSize, MediaType: contentRow.MediaType,
		AbsoluteExpiresAt: memberRequest.AbsoluteExpiresAt.UTC(), Provider: asset.Provider,
		ProviderCapabilityRevision: asset.ProviderCapabilityRevision, FingerprintStrength: asset.FingerprintStrength,
		SourceSize: asset.Size, SourceMediaType: asset.MediaType,
		SecurityPolicyRevision: contentRow.JobSecurityPolicyRevision,
	}, nil
}

func (resolver *DerivedRepresentationResolver) ReadArchiveMember(
	ctx context.Context,
	binding ResolvedArchiveMemberArtifact,
	destination io.Writer,
) error {
	if destination == nil || !validResolvedArchiveMemberArtifact(binding) {
		return ErrDerivedRepresentationUnavailable
	}
	current, err := resolver.ResolveArchiveMember(nonNilContext(ctx), ArchiveMemberArtifactRequest{
		RequestID: binding.MemberRequestID, OwnerUserID: binding.OwnerUserID,
		Asset: AuthorizedAsset{
			Ref: binding.Ref, CatalogGenerationID: binding.CatalogGenerationID,
			Provider: binding.Provider, ProviderCapabilityRevision: binding.ProviderCapabilityRevision,
			SourceFingerprint: binding.SourceFingerprint, EntryFingerprint: binding.EntryFingerprint,
			FingerprintStrength: binding.FingerprintStrength, Size: binding.SourceSize, MediaType: binding.SourceMediaType,
		},
	})
	if err != nil || !sameResolvedArchiveMemberArtifact(current, binding) {
		return errors.Join(ErrDerivedRepresentationUnavailable, err)
	}
	return resolver.read(nonNilContext(ctx), DerivedArtifactRead{
		ArtifactID: binding.DerivedArtifactID, RecoveryPointID: binding.Ref.RecoveryPointID,
		CatalogGenerationID: binding.CatalogGenerationID, EntryID: binding.Ref.EntryID,
		SourceFingerprint: binding.SourceFingerprint,
	}, destination)
}

type archiveMemberOutputMetadata struct {
	SchemaVersion int    `json:"schema_version"`
	MemberID      string `json:"member_id"`
	DisplayName   string `json:"display_name"`
	Size          int64  `json:"size"`
	MediaType     string `json:"media_type"`
}

func (resolver *DerivedRepresentationResolver) loadArchiveMemberMetadata(
	ctx context.Context,
	row derivedRepresentationRow,
) (archiveMemberOutputMetadata, error) {
	if resolver == nil || resolver.read == nil || row.Role != "metadata" || row.MediaType != "application/json" ||
		row.PlaintextSize <= 0 || row.PlaintextSize > 4096 || !lowerHexContent(row.PlaintextDigest, 64) {
		return archiveMemberOutputMetadata{}, ErrDerivedRepresentationUnavailable
	}
	buffer := &derivedBoundedBuffer{maximum: 4096}
	err := resolver.read(nonNilContext(ctx), DerivedArtifactRead{
		ArtifactID: row.ArtifactID, RecoveryPointID: row.RecoveryPointID,
		CatalogGenerationID: row.CatalogGenerationID, EntryID: row.EntryID,
		SourceFingerprint: row.SourceFingerprint,
	}, buffer)
	if err != nil || buffer.exceeded || int64(buffer.buffer.Len()) != row.PlaintextSize {
		zeroBytes(buffer.buffer.Bytes())
		return archiveMemberOutputMetadata{}, errors.Join(ErrDerivedRepresentationUnavailable, err)
	}
	payload := buffer.buffer.Bytes()
	defer zeroBytes(payload)
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != row.PlaintextDigest || !json.Valid(payload) {
		return archiveMemberOutputMetadata{}, ErrDerivedRepresentationUnavailable
	}
	var metadata archiveMemberOutputMetadata
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&metadata) != nil {
		return archiveMemberOutputMetadata{}, ErrDerivedRepresentationUnavailable
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return archiveMemberOutputMetadata{}, ErrDerivedRepresentationUnavailable
	}
	canonical, canonicalErr := json.Marshal(metadata)
	if canonicalErr != nil || !bytes.Equal(canonical, payload) || metadata.SchemaVersion != 1 ||
		backupasset.ValidateOpaqueID(metadata.MemberID) != nil || !safeDerivedArchiveDisplayName(metadata.DisplayName) ||
		metadata.Size < 0 || metadata.Size > maximumDerivedArchiveMember ||
		!oneOfContent(metadata.MediaType, "text/plain", "image/png", "image/jpeg", "application/pdf", "application/octet-stream") {
		return archiveMemberOutputMetadata{}, ErrDerivedRepresentationUnavailable
	}
	return metadata, nil
}

func archiveMemberDescriptorOrdinal(canonical []byte, ordinal int) bool {
	if len(canonical) == 0 || ordinal < 0 || !json.Valid(canonical) {
		return false
	}
	var descriptor struct {
		SchemaVersion int `json:"schema_version"`
		Parameters    struct {
			MemberStart int `json:"member_start"`
			MemberEnd   int `json:"member_end"`
		} `json:"parameters"`
	}
	if json.Unmarshal(canonical, &descriptor) != nil {
		return false
	}
	return descriptor.SchemaVersion == 1 && descriptor.Parameters.MemberStart == ordinal && descriptor.Parameters.MemberEnd == ordinal
}

func validArchiveMemberAuthorizedAsset(asset AuthorizedAsset) bool {
	return backupasset.ValidateAssetRef(asset.Ref) == nil && backupasset.ValidateOpaqueID(asset.CatalogGenerationID) == nil &&
		asset.SourceFingerprint != "" && len(asset.SourceFingerprint) <= 128 &&
		asset.EntryFingerprint != "" && len(asset.EntryFingerprint) <= 128 &&
		asset.ProviderCapabilityRevision > 0 && asset.Size >= 0 &&
		oneOfContent(asset.FingerprintStrength, "strong", "weak", "none") &&
		strings.TrimSpace(asset.MediaType) != "" && len(asset.MediaType) <= 255 &&
		(asset.Provider == backupasset.ProviderRestic || asset.Provider == backupasset.ProviderRsync || asset.Provider == backupasset.ProviderRclone)
}

func validResolvedArchiveMemberArtifact(binding ResolvedArchiveMemberArtifact) bool {
	return backupasset.ValidateOpaqueID(binding.MemberRequestID) == nil && binding.OwnerUserID > 0 &&
		backupasset.ValidateAssetRef(binding.Ref) == nil && backupasset.ValidateOpaqueID(binding.CatalogGenerationID) == nil &&
		binding.SourceFingerprint != "" && binding.EntryFingerprint != "" && lowerHexContent(binding.MemberChainDigest, 64) &&
		backupasset.ValidateOpaqueID(binding.ProcessingJobID) == nil && backupasset.ValidateOpaqueID(binding.ProcessingAttemptID) == nil &&
		backupasset.ValidateOpaqueID(binding.DerivedArtifactSetID) == nil && backupasset.ValidateOpaqueID(binding.DerivedArtifactID) == nil &&
		backupasset.ValidateOpaqueID(binding.DerivedBlobID) == nil && lowerHexContent(binding.DerivedDigest, 64) &&
		binding.DerivedSize >= 0 && binding.DerivedSize <= maximumDerivedArchiveMember &&
		strings.TrimSpace(binding.MediaType) != "" && binding.AbsoluteExpiresAt.Location() == time.UTC &&
		validArchiveMemberAuthorizedAsset(AuthorizedAsset{
			Ref: binding.Ref, CatalogGenerationID: binding.CatalogGenerationID,
			Provider: binding.Provider, ProviderCapabilityRevision: binding.ProviderCapabilityRevision,
			SourceFingerprint: binding.SourceFingerprint, EntryFingerprint: binding.EntryFingerprint,
			FingerprintStrength: binding.FingerprintStrength, Size: binding.SourceSize, MediaType: binding.SourceMediaType,
		}) && strings.TrimSpace(binding.SecurityPolicyRevision) != ""
}

func sameResolvedArchiveMemberArtifact(left, right ResolvedArchiveMemberArtifact) bool {
	return left == right
}

func (resolver *DerivedRepresentationResolver) Resolve(
	ctx context.Context,
	request DerivedRepresentationRequest,
) (DerivedRepresentation, error) {
	if resolver == nil || resolver.db == nil || resolver.read == nil || !validDerivedRepresentationRequest(request) {
		return DerivedRepresentation{}, ErrDerivedRepresentationUnavailable
	}
	if err := resolver.requireMalwareSafety(nonNilContext(ctx), derivedSafetyAsset(request)); err != nil {
		return DerivedRepresentation{}, err
	}
	intent, ok := resolveDerivedDeliveryIntent(request)
	if !ok {
		return DerivedRepresentation{}, ErrDerivedRepresentationUnavailable
	}
	var rows []derivedRepresentationRow
	query := resolver.baseQuery(nonNilContext(ctx)).
		Where(`sets.recovery_point_id = ? AND sets.catalog_generation_id = ? AND sets.entry_id = ?
			AND sets.source_fingerprint = ? AND sets.security_policy_revision = ?`,
			request.Ref.RecoveryPointID, request.CatalogGenerationID, request.Ref.EntryID,
			request.SourceFingerprint, request.SecurityPolicyRevision)
	if request.ExpectedEntryFingerprint != "" {
		query = query.Where("artifacts.plaintext_digest = ?", request.ExpectedEntryFingerprint)
	}
	query = queryForDerivedIntent(query, intent)
	if err := query.Order("sets.updated_at DESC, sets.id ASC, artifacts.ordinal ASC").Limit(257).Scan(&rows).Error; err != nil {
		return DerivedRepresentation{}, fmt.Errorf("load Derived representation: %w", err)
	}
	if len(rows) == 257 {
		return DerivedRepresentation{}, backupasset.ErrConflict
	}
	var resolved *DerivedRepresentation
	for _, row := range rows {
		if !derivedArtifactMatchesIntent(row, intent) || !validDerivedRepresentationRow(row, request) {
			continue
		}
		active, err := resolver.rowUsesActivePipeline(nonNilContext(ctx), row)
		if err != nil {
			return DerivedRepresentation{}, err
		}
		if !active {
			continue
		}
		if resolved != nil {
			return DerivedRepresentation{}, backupasset.ErrConflict
		}
		modified := row.UpdatedAt.UTC()
		candidate := DerivedRepresentation{
			artifactID: row.ArtifactID, artifactSetID: row.ArtifactSetID, blobID: row.BlobID,
			setCompleteness: row.SetCompleteness,
			Ref:             request.Ref, CatalogGenerationID: request.CatalogGenerationID,
			SourceFingerprint: request.SourceFingerprint, SecurityPolicyRevision: request.SecurityPolicyRevision,
			Provider: request.Provider, Renderer: request.Renderer, Profile: request.Profile,
			Role: row.Role, MediaType: row.MediaType, Size: row.PlaintextSize,
			EntryFingerprint: row.PlaintextDigest, Completeness: row.Completeness, ModifiedAt: &modified,
			capability: row.Capability, capabilitySchema: row.CapabilitySchema,
			pipelineFingerprint: row.PipelineFingerprint, outputProfile: row.OutputProfile,
			sourceEntryFingerprint:     request.SourceEntryFingerprint,
			fingerprintStrength:        request.FingerprintStrength,
			providerCapabilityRevision: request.ProviderCapabilityRevision,
			sourceSize:                 request.SourceSize, sourceMediaType: request.SourceMediaType,
			intent: intent, ordinal: row.Ordinal,
		}
		resolved = &candidate
	}
	if resolved != nil {
		if err := resolver.validateDerivedPayload(nonNilContext(ctx), *resolved); err != nil {
			return DerivedRepresentation{}, err
		}
		return *resolved, nil
	}
	return DerivedRepresentation{}, ErrDerivedRepresentationUnavailable
}

func (resolver *DerivedRepresentationResolver) Revalidate(ctx context.Context, binding DerivedRepresentation) error {
	if resolver == nil || !validDerivedBinding(binding) {
		return ErrDerivedRepresentationUnavailable
	}
	var row derivedRepresentationRow
	result := resolver.baseQuery(nonNilContext(ctx)).Where(
		"artifacts.id = ? AND sets.id = ? AND blobs.id = ?", binding.artifactID, binding.artifactSetID, binding.blobID,
	).Limit(1).Scan(&row)
	if result.Error != nil {
		return fmt.Errorf("revalidate Derived representation: %w", result.Error)
	}
	request := DerivedRepresentationRequest{
		Ref: binding.Ref, CatalogGenerationID: binding.CatalogGenerationID,
		SourceFingerprint: binding.SourceFingerprint, SourceEntryFingerprint: binding.sourceEntryFingerprint,
		FingerprintStrength:        binding.fingerprintStrength,
		ProviderCapabilityRevision: binding.providerCapabilityRevision,
		SourceSize:                 binding.sourceSize, SourceMediaType: binding.sourceMediaType,
		SecurityPolicyRevision: binding.SecurityPolicyRevision,
		Provider:               binding.Provider, Renderer: binding.Renderer, Profile: binding.Profile, intent: binding.intent,
	}
	if err := resolver.requireMalwareSafety(nonNilContext(ctx), derivedSafetyAsset(request)); err != nil {
		return err
	}
	active, activeErr := resolver.rowUsesActivePipeline(nonNilContext(ctx), row)
	if activeErr != nil {
		return activeErr
	}
	if result.RowsAffected != 1 || !active || !validDerivedRepresentationRow(row, request) ||
		row.ArtifactID != binding.artifactID || row.ArtifactSetID != binding.artifactSetID || row.BlobID != binding.blobID ||
		row.SetCompleteness != binding.setCompleteness ||
		row.Role != binding.Role || row.MediaType != binding.MediaType || row.PlaintextSize != binding.Size ||
		row.PlaintextDigest != binding.EntryFingerprint || row.Completeness != binding.Completeness ||
		row.Capability != binding.capability || row.CapabilitySchema != binding.capabilitySchema ||
		row.PipelineFingerprint != binding.pipelineFingerprint || row.OutputProfile != binding.outputProfile ||
		row.Ordinal != binding.ordinal || !derivedArtifactMatchesIntent(row, binding.intent) {
		return ErrDerivedRepresentationUnavailable
	}
	return nil
}

func (resolver *DerivedRepresentationResolver) Open(
	ctx context.Context,
	binding DerivedRepresentation,
	request SourceRequest,
) (SourceSession, error) {
	return resolver.open(ctx, binding, request, nil)
}

func (resolver *DerivedRepresentationResolver) open(
	ctx context.Context,
	binding DerivedRepresentation,
	request SourceRequest,
	liveSourceRevalidate func(context.Context) error,
) (SourceSession, error) {
	if ValidateSourceRequest(request) != nil || request.Ref != binding.Ref ||
		request.CatalogGenerationID != binding.CatalogGenerationID || request.ExpectedSource != binding.SourceFingerprint ||
		request.ExpectedEntry != binding.EntryFingerprint || request.Mode == SourceModeRange ||
		request.Mode == SourceModeSequential && request.MaxBytes > binding.Size {
		return nil, ErrDerivedRepresentationUnavailable
	}
	if liveSourceRevalidate != nil {
		if err := liveSourceRevalidate(nonNilContext(ctx)); err != nil {
			return nil, err
		}
	}
	if err := resolver.Revalidate(ctx, binding); err != nil {
		return nil, err
	}
	return &derivedSourceSession{
		resolver: resolver, binding: binding, request: request, ctx: nonNilContext(ctx),
		liveSourceRevalidate: liveSourceRevalidate,
	}, nil
}

type derivedRepresentationRow struct {
	ArtifactID                    string
	ArtifactSetID                 string
	BlobID                        string
	RecoveryPointID               string
	CatalogGenerationID           string
	EntryID                       string
	SourceFingerprint             string
	SecurityPolicyRevision        string
	SetState                      string
	SetCompleteness               string
	ProjectionRequired            bool
	ProjectionPublished           bool
	Role                          string
	Ordinal                       int
	MediaType                     string
	PlaintextSize                 int64
	PlaintextDigest               string
	Completeness                  string
	ReferenceState                string
	BlobState                     string
	UpdatedAt                     time.Time
	Capability                    string
	CapabilitySchema              string
	PipelineFingerprint           string
	OutputProfile                 string
	JobRecoveryPointID            string
	JobCatalogGenerationID        string
	JobEntryID                    string
	JobSourceFingerprint          string
	JobEntryFingerprint           string
	JobProviderCapabilityRevision int64
	JobSecurityPolicyRevision     string
	JobState                      string
	JobIsCurrent                  bool
	JobFinishedAt                 *time.Time
	JobCurrentAttemptID           string
	JobCurrentArtifactSetID       string
	AttemptID                     string
	AttemptState                  string
	AttemptIsCurrent              bool
	AttemptFinishedAt             *time.Time
}

func (resolver *DerivedRepresentationResolver) baseQuery(ctx context.Context) *gorm.DB {
	return resolver.db.WithContext(ctx).Table("backup_asset_derived_artifacts AS artifacts").
		Select(`artifacts.id AS artifact_id, artifacts.artifact_set_id, artifacts.blob_id,
			sets.recovery_point_id, sets.catalog_generation_id, sets.entry_id, sets.source_fingerprint,
			sets.security_policy_revision, sets.state AS set_state, sets.completeness AS set_completeness,
			sets.projection_required, sets.projection_published, sets.updated_at,
			artifacts.ordinal, artifacts.role, artifacts.media_type, artifacts.plaintext_size,
			artifacts.plaintext_digest, artifacts.completeness,
			refs.state AS reference_state, blobs.state AS blob_state,
			jobs.capability, jobs.capability_schema, jobs.pipeline_fingerprint, jobs.output_profile,
			jobs.recovery_point_id AS job_recovery_point_id,
			jobs.catalog_generation_id AS job_catalog_generation_id,
			jobs.entry_id AS job_entry_id, jobs.source_fingerprint AS job_source_fingerprint,
			jobs.entry_fingerprint AS job_entry_fingerprint,
			jobs.provider_capability_revision AS job_provider_capability_revision,
			jobs.security_policy_revision AS job_security_policy_revision,
			jobs.state AS job_state, jobs.is_current AS job_is_current,
			jobs.finished_at AS job_finished_at,
			jobs.current_attempt_id AS job_current_attempt_id,
			jobs.current_artifact_set_id AS job_current_artifact_set_id,
			attempts.id AS attempt_id, attempts.state AS attempt_state,
			attempts.is_current AS attempt_is_current, attempts.finished_at AS attempt_finished_at`).
		Joins(`JOIN backup_asset_derived_artifact_sets AS sets ON sets.id = artifacts.artifact_set_id`).
		Joins(`JOIN backup_asset_processing_jobs AS jobs ON jobs.id = sets.job_id`).
		Joins(`JOIN backup_asset_processing_attempts AS attempts
			ON attempts.id = sets.attempt_id AND attempts.job_id = jobs.id`).
		Joins(`JOIN backup_asset_derived_blob_references AS refs
			ON refs.artifact_id = artifacts.id AND refs.blob_id = artifacts.blob_id
			AND refs.recovery_point_id = sets.recovery_point_id
			AND refs.catalog_generation_id = sets.catalog_generation_id
			AND refs.entry_id = sets.entry_id AND refs.source_fingerprint = sets.source_fingerprint`).
		Joins(`JOIN backup_asset_derived_blobs AS blobs ON blobs.id = artifacts.blob_id`).
		Where("sets.state = ? AND refs.state = ? AND blobs.state = ?", "active", "active", "active")
}

func (resolver *DerivedRepresentationResolver) rowUsesActivePipeline(
	ctx context.Context,
	row derivedRepresentationRow,
) (bool, error) {
	if resolver == nil || resolver.activePipeline == nil || row.Capability == "" || row.OutputProfile == "" || row.PipelineFingerprint == "" {
		return false, ErrDerivedRepresentationUnavailable
	}
	active, err := resolver.activePipeline(nonNilContext(ctx), row.Capability, row.OutputProfile)
	if err != nil {
		return false, fmt.Errorf("load active Derived pipeline: %w", err)
	}
	return active != "" && active == row.PipelineFingerprint, nil
}

func (resolver *DerivedRepresentationResolver) requireMalwareSafety(
	ctx context.Context,
	asset AuthorizedAsset,
) error {
	if resolver == nil || resolver.malwareSafety == nil {
		return ErrDerivedRepresentationUnavailable
	}
	safe, err := resolver.malwareSafety(nonNilContext(ctx), asset)
	if err != nil || !safe {
		return errors.Join(ErrDerivedRepresentationUnavailable, err)
	}
	return nil
}

func derivedSafetyAsset(request DerivedRepresentationRequest) AuthorizedAsset {
	return AuthorizedAsset{
		Ref: request.Ref, CatalogGenerationID: request.CatalogGenerationID,
		Provider: request.Provider, ProviderCapabilityRevision: request.ProviderCapabilityRevision,
		SourceFingerprint: request.SourceFingerprint, EntryFingerprint: request.SourceEntryFingerprint,
		FingerprintStrength: request.FingerprintStrength, Size: request.SourceSize,
		MediaType: request.SourceMediaType,
	}
}

func validDerivedRepresentationRequest(request DerivedRepresentationRequest) bool {
	if backupasset.ValidateAssetRef(request.Ref) != nil || backupasset.ValidateOpaqueID(request.CatalogGenerationID) != nil ||
		request.SourceFingerprint == "" || len(request.SourceFingerprint) > 128 ||
		len(request.SourceEntryFingerprint) > 128 || request.ProviderCapabilityRevision <= 0 || request.SourceSize < 0 ||
		!oneOfContent(request.FingerprintStrength, "strong", "weak", "none") ||
		strings.TrimSpace(request.SourceMediaType) == "" || len(request.SourceMediaType) > 255 ||
		request.SecurityPolicyRevision == "" || len(request.SecurityPolicyRevision) > 128 ||
		request.ExpectedEntryFingerprint != "" && !lowerHexContent(request.ExpectedEntryFingerprint, 64) {
		return false
	}
	if request.Provider != backupasset.ProviderRestic && request.Provider != backupasset.ProviderRsync && request.Provider != backupasset.ProviderRclone {
		return false
	}
	_, ok := resolveDerivedDeliveryIntent(request)
	return ok
}

type DerivedSourceAssetResolver func(
	context.Context,
	backupasset.AssetRef,
	string,
	string,
) (AuthorizedAsset, error)

// DerivedAttemptSourceResolver routes a Worker Input binding to an exact active
// complete text/OCR Derived representation when the descriptor carries its
// plaintext digest. Other bindings remain on the original Provider resolver.
type DerivedAttemptSourceResolver struct {
	primary                SourceResolver
	derived                *DerivedRepresentationResolver
	securityPolicyRevision string
	asset                  DerivedSourceAssetResolver
}

func NewDerivedAttemptSourceResolver(
	primary SourceResolver,
	derived *DerivedRepresentationResolver,
	securityPolicyRevision string,
	asset DerivedSourceAssetResolver,
) (*DerivedAttemptSourceResolver, error) {
	if primary == nil || derived == nil || strings.TrimSpace(securityPolicyRevision) == "" ||
		len(securityPolicyRevision) > 128 || asset == nil {
		return nil, ErrDerivedRepresentationUnavailable
	}
	return &DerivedAttemptSourceResolver{
		primary: primary, derived: derived, securityPolicyRevision: securityPolicyRevision, asset: asset,
	}, nil
}

func (resolver *DerivedAttemptSourceResolver) OpenContentSource(
	ctx context.Context,
	request SourceRequest,
) (SourceSession, error) {
	if resolver == nil || resolver.primary == nil || resolver.derived == nil || resolver.asset == nil ||
		ValidateSourceRequest(request) != nil {
		return nil, ErrDerivedRepresentationUnavailable
	}
	ctx = nonNilContext(ctx)
	known, err := resolver.derived.hasArtifactIdentity(ctx, request)
	if err != nil {
		return nil, err
	}
	if !known {
		return resolver.primary.OpenContentSource(ctx, request)
	}
	if request.Mode == SourceModeRange {
		return nil, ErrDerivedRepresentationUnavailable
	}
	asset, err := resolver.asset(ctx, request.Ref, request.CatalogGenerationID, request.ExpectedSource)
	if err != nil || asset.Ref != request.Ref || asset.CatalogGenerationID != request.CatalogGenerationID ||
		asset.SourceFingerprint != request.ExpectedSource || !validDerivedProvider(asset.Provider) {
		return nil, errors.Join(ErrDerivedRepresentationUnavailable, err)
	}
	binding, err := resolver.derived.Resolve(ctx, DerivedRepresentationRequest{
		Ref: request.Ref, CatalogGenerationID: request.CatalogGenerationID,
		SourceFingerprint: request.ExpectedSource, SourceEntryFingerprint: asset.EntryFingerprint,
		FingerprintStrength:        asset.FingerprintStrength,
		ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		SourceSize:                 asset.Size, SourceMediaType: asset.MediaType,
		ExpectedEntryFingerprint: request.ExpectedEntry,
		SecurityPolicyRevision:   resolver.securityPolicyRevision, Provider: asset.Provider,
		Renderer: RendererEscapedText, Profile: ProfileTextV1, intent: derivedIntentExtractedText,
	})
	if err != nil || binding.setCompleteness != "complete" || binding.Completeness != "complete" ||
		(binding.Role != "content" && binding.Role != "ocr") || binding.MediaType != "text/plain" {
		return nil, errors.Join(ErrDerivedRepresentationUnavailable, err)
	}
	expectedAsset := asset
	return resolver.derived.open(ctx, binding, request, func(revalidateCtx context.Context) error {
		current, resolveErr := resolver.asset(
			nonNilContext(revalidateCtx), request.Ref, request.CatalogGenerationID, request.ExpectedSource,
		)
		if resolveErr != nil || !sameDerivedSourceAsset(current, expectedAsset) {
			return errors.Join(ErrDerivedRepresentationUnavailable, resolveErr)
		}
		return nil
	})
}

func (resolver *DerivedAttemptSourceResolver) ValidateContentCacheRoot(ctx context.Context, root string) error {
	if resolver == nil || resolver.primary == nil {
		return ErrDerivedRepresentationUnavailable
	}
	return resolver.primary.ValidateContentCacheRoot(ctx, root)
}

func (resolver *DerivedRepresentationResolver) hasArtifactIdentity(ctx context.Context, request SourceRequest) (bool, error) {
	if resolver == nil || resolver.db == nil || ValidateSourceRequest(request) != nil ||
		!lowerHexContent(request.ExpectedEntry, 64) {
		return false, nil
	}
	var count int64
	err := resolver.db.WithContext(nonNilContext(ctx)).Table("backup_asset_derived_artifacts AS artifacts").
		Joins("JOIN backup_asset_derived_artifact_sets AS sets ON sets.id = artifacts.artifact_set_id").
		Where(`sets.recovery_point_id = ? AND sets.catalog_generation_id = ? AND sets.entry_id = ?
			AND sets.source_fingerprint = ? AND artifacts.plaintext_digest = ?`,
			request.Ref.RecoveryPointID, request.CatalogGenerationID, request.Ref.EntryID,
			request.ExpectedSource, request.ExpectedEntry).
		Limit(1).Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("load Derived attempt identity: %w", err)
	}
	return count > 0, nil
}

func validDerivedProvider(provider backupasset.ProviderKind) bool {
	return provider == backupasset.ProviderRestic || provider == backupasset.ProviderRsync || provider == backupasset.ProviderRclone
}

func sameDerivedSourceAsset(current, expected AuthorizedAsset) bool {
	return current.Ref == expected.Ref && current.CatalogGenerationID == expected.CatalogGenerationID &&
		current.RepositoryID == expected.RepositoryID && current.Provider == expected.Provider &&
		current.ProviderCapabilityRevision == expected.ProviderCapabilityRevision &&
		current.SourceFingerprint == expected.SourceFingerprint && current.EntryFingerprint == expected.EntryFingerprint &&
		current.FingerprintStrength == expected.FingerprintStrength && current.Size == expected.Size &&
		current.MediaType == expected.MediaType
}

func validDerivedRepresentationRow(row derivedRepresentationRow, request DerivedRepresentationRequest) bool {
	return backupasset.ValidateOpaqueID(row.ArtifactID) == nil && backupasset.ValidateOpaqueID(row.ArtifactSetID) == nil &&
		backupasset.ValidateOpaqueID(row.BlobID) == nil && row.RecoveryPointID == request.Ref.RecoveryPointID &&
		row.CatalogGenerationID == request.CatalogGenerationID && row.EntryID == request.Ref.EntryID &&
		row.SourceFingerprint == request.SourceFingerprint && row.SecurityPolicyRevision == request.SecurityPolicyRevision &&
		row.JobRecoveryPointID == request.Ref.RecoveryPointID && row.JobCatalogGenerationID == request.CatalogGenerationID &&
		row.JobEntryID == request.Ref.EntryID && row.JobSourceFingerprint == request.SourceFingerprint &&
		row.JobEntryFingerprint == request.SourceEntryFingerprint &&
		row.JobProviderCapabilityRevision == request.ProviderCapabilityRevision &&
		row.JobSecurityPolicyRevision == request.SecurityPolicyRevision && row.JobState == "succeeded" &&
		!row.JobIsCurrent && row.JobFinishedAt != nil && row.JobCurrentArtifactSetID == row.ArtifactSetID &&
		row.JobCurrentAttemptID == row.AttemptID && row.AttemptState == "succeeded" &&
		!row.AttemptIsCurrent && row.AttemptFinishedAt != nil &&
		row.Capability != "" && len(row.Capability) <= 64 && row.CapabilitySchema != "" && len(row.CapabilitySchema) <= 64 &&
		row.PipelineFingerprint != "" && len(row.PipelineFingerprint) <= 128 && row.OutputProfile != "" && len(row.OutputProfile) <= 64 &&
		row.SetState == "active" && row.ReferenceState == "active" && row.BlobState == "active" &&
		(row.SetCompleteness == "complete" || row.SetCompleteness == "partial") &&
		(!row.ProjectionRequired || row.ProjectionPublished) && row.PlaintextSize >= 0 &&
		lowerHexContent(row.PlaintextDigest, 64) &&
		(row.Completeness == "complete" || row.Completeness == "partial")
}

func validDerivedBinding(binding DerivedRepresentation) bool {
	return validDerivedRepresentationRequest(DerivedRepresentationRequest{
		Ref: binding.Ref, CatalogGenerationID: binding.CatalogGenerationID,
		SourceFingerprint: binding.SourceFingerprint, SourceEntryFingerprint: binding.sourceEntryFingerprint,
		FingerprintStrength:        binding.fingerprintStrength,
		ProviderCapabilityRevision: binding.providerCapabilityRevision,
		SourceSize:                 binding.sourceSize, SourceMediaType: binding.sourceMediaType,
		SecurityPolicyRevision: binding.SecurityPolicyRevision,
		Provider:               binding.Provider, Renderer: binding.Renderer, Profile: binding.Profile, intent: binding.intent,
	}) && backupasset.ValidateOpaqueID(binding.artifactID) == nil && backupasset.ValidateOpaqueID(binding.artifactSetID) == nil &&
		backupasset.ValidateOpaqueID(binding.blobID) == nil &&
		(binding.setCompleteness == "complete" || binding.setCompleteness == "partial") &&
		binding.Size >= 0 && lowerHexContent(binding.EntryFingerprint, 64) &&
		binding.capability != "" && binding.capabilitySchema != "" && binding.pipelineFingerprint != "" && binding.outputProfile != "" &&
		binding.intent != derivedIntentNone && binding.ordinal >= 0 &&
		len(binding.sourceEntryFingerprint) <= 128 && binding.providerCapabilityRevision > 0 && binding.sourceSize >= 0 &&
		oneOfContent(binding.fingerprintStrength, "strong", "weak", "none") && binding.sourceMediaType != ""
}

func oneOfContent(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func resolveDerivedDeliveryIntent(request DerivedRepresentationRequest) (derivedDeliveryIntent, bool) {
	inferred, ok := inferDerivedDeliveryIntent(request.SourceMediaType, request.Renderer, request.Profile)
	if !ok || request.intent != derivedIntentNone && request.intent != inferred {
		return derivedIntentNone, false
	}
	return inferred, true
}

func inferDerivedDeliveryIntent(
	sourceMediaType string,
	renderer Renderer,
	profile RendererProfile,
) (derivedDeliveryIntent, bool) {
	mediaType := normalizedMediaType(sourceMediaType)
	switch {
	case renderer == RendererEscapedText && profile == ProfileTextV1 && isDerivedTextSource(mediaType):
		return derivedIntentExtractedText, true
	case renderer == RendererSafeRaster && profile == ProfileRasterV1 && isDerivedImageSource(mediaType):
		return derivedIntentImageThumbnail, true
	case renderer == RendererSafeRaster && profile == ProfileRasterV1 && isDerivedDocumentSource(mediaType):
		return derivedIntentDocumentPage, true
	case renderer == RendererNativeAudio && profile == ProfileAudioV1 && strings.HasPrefix(mediaType, "audio/"):
		return derivedIntentAudioPreview, true
	case renderer == RendererNativeVideo && profile == ProfileVideoV1 && strings.HasPrefix(mediaType, "video/"):
		return derivedIntentVideoPreview, true
	case renderer == RendererEscapedText && profile == ProfileTextV1 && isDerivedArchiveSource(mediaType):
		return derivedIntentArchiveIndex, true
	default:
		return derivedIntentNone, false
	}
}

func queryForDerivedIntent(query *gorm.DB, intent derivedDeliveryIntent) *gorm.DB {
	contract, ok := derivedContractForIntent(intent)
	if query == nil {
		return nil
	}
	if !ok {
		return query.Where("1 = 0")
	}
	return query.Where(
		"jobs.capability = ? AND jobs.capability_schema = ? AND jobs.output_profile = ? AND artifacts.ordinal = ? AND artifacts.role = ? AND artifacts.media_type IN ?",
		contract.capability, contract.capabilitySchema, contract.outputProfile,
		contract.ordinal, contract.role, contract.mediaTypes,
	)
}

func derivedArtifactMatchesIntent(row derivedRepresentationRow, intent derivedDeliveryIntent) bool {
	contract, ok := derivedContractForIntent(intent)
	if !ok || row.Capability != contract.capability || row.CapabilitySchema != contract.capabilitySchema ||
		row.OutputProfile != contract.outputProfile || row.Ordinal != contract.ordinal || row.Role != contract.role {
		return false
	}
	return oneOfContent(row.MediaType, contract.mediaTypes...)
}

func derivedContractForIntent(intent derivedDeliveryIntent) (derivedArtifactContract, bool) {
	switch intent {
	case derivedIntentExtractedText:
		return derivedArtifactContract{
			capability: "text.extract", capabilitySchema: "text.extract.v1", outputProfile: "bounded_text_v1",
			ordinal: 0, role: "content", mediaTypes: []string{"text/plain"},
		}, true
	case derivedIntentImageThumbnail:
		return derivedArtifactContract{
			capability: "image.thumbnail", capabilitySchema: "image.thumbnail.v1", outputProfile: "raster_thumbnail_v1",
			ordinal: 0, role: "thumbnail", mediaTypes: []string{"image/png", "image/jpeg", "image/webp"},
		}, true
	case derivedIntentDocumentPage:
		return derivedArtifactContract{
			capability: "document.convert", capabilitySchema: "document.convert.v1", outputProfile: "static_pages_v1",
			ordinal: 0, role: "thumbnail", mediaTypes: []string{"image/png", "image/jpeg", "image/webp"},
		}, true
	case derivedIntentAudioPreview:
		return derivedArtifactContract{
			capability: "media.transcode", capabilitySchema: "media.transcode.v1", outputProfile: "browser_preview_v1",
			ordinal: 0, role: "content", mediaTypes: []string{"audio/mpeg", "audio/mp4", "audio/ogg"},
		}, true
	case derivedIntentVideoPreview:
		return derivedArtifactContract{
			capability: "media.transcode", capabilitySchema: "media.transcode.v1", outputProfile: "browser_preview_v1",
			ordinal: 0, role: "content", mediaTypes: []string{"video/mp4", "video/webm"},
		}, true
	case derivedIntentArchiveIndex:
		return derivedArtifactContract{
			capability: "archive.inspect", capabilitySchema: "archive.inspect.v1", outputProfile: "archive_index_v1",
			ordinal: 0, role: "metadata", mediaTypes: []string{"application/json"},
		}, true
	default:
		return derivedArtifactContract{}, false
	}
}

func isDerivedTextSource(mediaType string) bool {
	return oneOfContent(mediaType, "text/plain", "text/csv", "text/markdown", "application/json", "application/xml")
}

func isDerivedImageSource(mediaType string) bool {
	return oneOfContent(mediaType, "image/jpeg", "image/png", "image/webp", "image/gif", "image/tiff", "image/bmp")
}

func isDerivedDocumentSource(mediaType string) bool {
	return oneOfContent(mediaType,
		"application/pdf",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.oasis.opendocument.text",
		"application/vnd.oasis.opendocument.spreadsheet",
		"application/vnd.oasis.opendocument.presentation",
	)
}

func isDerivedArchiveSource(mediaType string) bool {
	return oneOfContent(mediaType, "application/zip", "application/x-tar", "application/gzip", "application/x-xz", "application/zstd")
}

type derivedArchiveIndexEntry struct {
	ID          string `json:"id"`
	ParentID    string `json:"parent_id,omitempty"`
	DisplayName string `json:"display_name"`
	Size        int64  `json:"size"`
	MediaType   string `json:"media_type"`
}

type derivedArchiveIndex struct {
	SchemaVersion int                        `json:"schema_version"`
	Entries       []derivedArchiveIndexEntry `json:"entries"`
	ExpandedBytes int64                      `json:"expanded_bytes"`
	Complete      bool                       `json:"complete"`
}

func (resolver *DerivedRepresentationResolver) validateDerivedPayload(
	ctx context.Context,
	binding DerivedRepresentation,
) error {
	if binding.intent != derivedIntentArchiveIndex {
		return nil
	}
	payload, err := resolver.loadArchiveIndexPayload(ctx, binding)
	zeroBytes(payload)
	return err
}

func (resolver *DerivedRepresentationResolver) readRepresentation(
	ctx context.Context,
	binding DerivedRepresentation,
	destination io.Writer,
) error {
	if binding.intent != derivedIntentArchiveIndex {
		return resolver.read(ctx, derivedArtifactReadRequest(binding), destination)
	}
	payload, err := resolver.loadArchiveIndexPayload(ctx, binding)
	if err != nil {
		return err
	}
	defer zeroBytes(payload)
	written, err := destination.Write(payload)
	if err != nil || written != len(payload) {
		return errors.Join(ErrDerivedRepresentationUnavailable, err)
	}
	return nil
}

func (resolver *DerivedRepresentationResolver) loadArchiveIndexPayload(
	ctx context.Context,
	binding DerivedRepresentation,
) ([]byte, error) {
	if resolver == nil || resolver.read == nil || binding.intent != derivedIntentArchiveIndex ||
		binding.Size < 0 || binding.Size > maximumDerivedArchiveIndexBytes {
		return nil, ErrDerivedRepresentationUnavailable
	}
	buffer := &derivedBoundedBuffer{maximum: maximumDerivedArchiveIndexBytes}
	buffer.buffer.Grow(int(binding.Size))
	err := resolver.read(nonNilContext(ctx), derivedArtifactReadRequest(binding), buffer)
	if err != nil || buffer.exceeded || int64(buffer.buffer.Len()) != binding.Size {
		zeroBytes(buffer.buffer.Bytes())
		return nil, errors.Join(ErrDerivedRepresentationUnavailable, err)
	}
	payload := buffer.buffer.Bytes()
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != binding.EntryFingerprint || !validDerivedArchiveIndex(payload) {
		zeroBytes(payload)
		return nil, ErrDerivedRepresentationUnavailable
	}
	return payload, nil
}

func derivedArtifactReadRequest(binding DerivedRepresentation) DerivedArtifactRead {
	return DerivedArtifactRead{
		ArtifactID: binding.artifactID, RecoveryPointID: binding.Ref.RecoveryPointID,
		CatalogGenerationID: binding.CatalogGenerationID, EntryID: binding.Ref.EntryID,
		SourceFingerprint: binding.SourceFingerprint,
	}
}

type derivedBoundedBuffer struct {
	buffer   bytes.Buffer
	maximum  int64
	exceeded bool
}

func (buffer *derivedBoundedBuffer) Write(payload []byte) (int, error) {
	if buffer == nil || buffer.maximum < 0 || int64(len(payload)) > buffer.maximum-int64(buffer.buffer.Len()) {
		if buffer != nil {
			buffer.exceeded = true
		}
		return 0, ErrDerivedRepresentationUnavailable
	}
	return buffer.buffer.Write(payload)
}

func validDerivedArchiveIndex(payload []byte) bool {
	if len(payload) == 0 || len(payload) > maximumDerivedArchiveIndexBytes || !utf8.Valid(payload) || !json.Valid(payload) {
		return false
	}
	var value derivedArchiveIndex
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		return false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return false
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, payload) || value.SchemaVersion != 1 || value.Entries == nil ||
		!value.Complete || value.ExpandedBytes < 0 || value.ExpandedBytes > maximumDerivedArchiveExpanded ||
		len(value.Entries) > maximumDerivedArchiveEntries {
		return false
	}
	seen := make(map[string]struct{}, len(value.Entries))
	seenDisplayNames := make(map[string]struct{}, len(value.Entries))
	total := int64(0)
	for _, entry := range value.Entries {
		if !lowerHexContent(entry.ID, 32) || entry.ParentID != "" && !lowerHexContent(entry.ParentID, 32) ||
			!safeDerivedArchiveDisplayName(entry.DisplayName) || entry.Size < 0 || entry.Size > maximumDerivedArchiveMember ||
			entry.Size > value.ExpandedBytes-total || !oneOfContent(entry.MediaType,
			"text/plain", "image/png", "image/jpeg", "application/pdf", "application/octet-stream") {
			return false
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			return false
		}
		displayKey := derivedArchiveDisplayCollisionKey(entry.ParentID, entry.DisplayName)
		if _, duplicate := seenDisplayNames[displayKey]; duplicate {
			return false
		}
		seen[entry.ID] = struct{}{}
		seenDisplayNames[displayKey] = struct{}{}
		total += entry.Size
	}
	return total == value.ExpandedBytes
}

func safeDerivedArchiveDisplayName(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || strings.TrimSpace(value) != value ||
		value == "." || value == ".." || strings.ContainsAny(value, "\x00\r\n/\\") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	normalized := norm.NFKC.String(value)
	if normalized == "" || normalized == "." || normalized == ".." || strings.ContainsAny(normalized, "/\\") {
		return false
	}
	for _, character := range normalized {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func derivedArchiveDisplayCollisionKey(parentID, displayName string) string {
	return parentID + "\x00" + workerCapabilities.CanonicalNFKCCasefold(displayName)
}

type derivedSourceSession struct {
	resolver             *DerivedRepresentationResolver
	binding              DerivedRepresentation
	request              SourceRequest
	ctx                  context.Context
	liveSourceRevalidate func(context.Context) error
	once                 sync.Once
	reader               *derivedSourceReader
	closed               bool
	mu                   sync.Mutex
}

func (session *derivedSourceSession) Stat() SourceStat {
	return SourceStat{
		Size: session.binding.Size, ModifiedAt: cloneDerivedTime(session.binding.ModifiedAt),
		MediaType: session.binding.MediaType, SourceFingerprint: session.binding.SourceFingerprint,
		EntryFingerprint: session.binding.EntryFingerprint, FingerprintStrong: true,
	}
}

func (session *derivedSourceSession) Capabilities() SourceCapabilities {
	return SourceCapabilities{Provider: session.binding.Provider, Sequential: true, Range: false}
}

func (session *derivedSourceSession) Reader() SourceReader {
	if session.request.Mode == SourceModeStat {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil
	}
	session.once.Do(func() {
		pipeReader, pipeWriter := io.Pipe()
		session.reader = &derivedSourceReader{PipeReader: pipeReader}
		binding := session.binding
		go func() {
			err := session.Revalidate(session.ctx)
			if err == nil {
				err = session.resolver.readRepresentation(session.ctx, binding, pipeWriter)
			}
			_ = pipeWriter.CloseWithError(err)
		}()
	})
	return session.reader
}

func (session *derivedSourceSession) Revalidate(ctx context.Context) error {
	if session.liveSourceRevalidate != nil {
		if err := session.liveSourceRevalidate(nonNilContext(ctx)); err != nil {
			return err
		}
	}
	return session.resolver.Revalidate(ctx, session.binding)
}

func (session *derivedSourceSession) Close() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.closed = true
	if session.reader != nil {
		return session.reader.Close()
	}
	return nil
}

type derivedSourceReader struct {
	*io.PipeReader
}

func (*derivedSourceReader) ProviderBytes() int64 { return 0 }

func cloneDerivedTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}

func lowerHexContent(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
