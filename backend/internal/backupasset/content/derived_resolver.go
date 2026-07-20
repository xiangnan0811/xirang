package content

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"

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

type DerivedRepresentationRequest struct {
	Ref                      backupasset.AssetRef
	CatalogGenerationID      string
	SourceFingerprint        string
	ExpectedEntryFingerprint string
	SecurityPolicyRevision   string
	Provider                 backupasset.ProviderKind
	Renderer                 Renderer
	Profile                  RendererProfile
}

type DerivedRepresentation struct {
	artifactID             string                   `json:"-"`
	artifactSetID          string                   `json:"-"`
	blobID                 string                   `json:"-"`
	setCompleteness        string                   `json:"-"`
	Ref                    backupasset.AssetRef     `json:"-"`
	CatalogGenerationID    string                   `json:"-"`
	SourceFingerprint      string                   `json:"-"`
	SecurityPolicyRevision string                   `json:"-"`
	Provider               backupasset.ProviderKind `json:"-"`
	Renderer               Renderer                 `json:"-"`
	Profile                RendererProfile          `json:"-"`
	Role                   string                   `json:"-"`
	MediaType              string                   `json:"-"`
	Size                   int64                    `json:"-"`
	EntryFingerprint       string                   `json:"-"`
	Completeness           string                   `json:"-"`
	ModifiedAt             *time.Time               `json:"-"`
}

type DerivedRepresentationResolver struct {
	db   *gorm.DB
	read DerivedArtifactReadFunc
}

func NewDerivedRepresentationResolver(db *gorm.DB, read DerivedArtifactReadFunc) (*DerivedRepresentationResolver, error) {
	if db == nil || read == nil {
		return nil, ErrDerivedRepresentationUnavailable
	}
	return &DerivedRepresentationResolver{db: db, read: read}, nil
}

func (resolver *DerivedRepresentationResolver) Resolve(
	ctx context.Context,
	request DerivedRepresentationRequest,
) (DerivedRepresentation, error) {
	if resolver == nil || resolver.db == nil || resolver.read == nil || !validDerivedRepresentationRequest(request) {
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
	if err := query.Order("artifacts.ordinal ASC").Limit(64).Scan(&rows).Error; err != nil {
		return DerivedRepresentation{}, fmt.Errorf("load Derived representation: %w", err)
	}
	for _, row := range rows {
		if !derivedArtifactMatchesRenderer(row.Role, row.MediaType, request.Renderer, request.Profile) ||
			!validDerivedRepresentationRow(row, request) {
			continue
		}
		modified := row.UpdatedAt.UTC()
		return DerivedRepresentation{
			artifactID: row.ArtifactID, artifactSetID: row.ArtifactSetID, blobID: row.BlobID,
			setCompleteness: row.SetCompleteness,
			Ref:             request.Ref, CatalogGenerationID: request.CatalogGenerationID,
			SourceFingerprint: request.SourceFingerprint, SecurityPolicyRevision: request.SecurityPolicyRevision,
			Provider: request.Provider, Renderer: request.Renderer, Profile: request.Profile,
			Role: row.Role, MediaType: row.MediaType, Size: row.PlaintextSize,
			EntryFingerprint: row.PlaintextDigest, Completeness: row.Completeness, ModifiedAt: &modified,
		}, nil
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
		SourceFingerprint: binding.SourceFingerprint, SecurityPolicyRevision: binding.SecurityPolicyRevision,
		Provider: binding.Provider, Renderer: binding.Renderer, Profile: binding.Profile,
	}
	if result.RowsAffected != 1 || !validDerivedRepresentationRow(row, request) ||
		row.ArtifactID != binding.artifactID || row.ArtifactSetID != binding.artifactSetID || row.BlobID != binding.blobID ||
		row.SetCompleteness != binding.setCompleteness ||
		row.Role != binding.Role || row.MediaType != binding.MediaType || row.PlaintextSize != binding.Size ||
		row.PlaintextDigest != binding.EntryFingerprint || row.Completeness != binding.Completeness ||
		!derivedArtifactMatchesRenderer(row.Role, row.MediaType, binding.Renderer, binding.Profile) {
		return ErrDerivedRepresentationUnavailable
	}
	return nil
}

func (resolver *DerivedRepresentationResolver) Open(
	ctx context.Context,
	binding DerivedRepresentation,
	request SourceRequest,
) (SourceSession, error) {
	if ValidateSourceRequest(request) != nil || request.Ref != binding.Ref ||
		request.CatalogGenerationID != binding.CatalogGenerationID || request.ExpectedSource != binding.SourceFingerprint ||
		request.ExpectedEntry != binding.EntryFingerprint || request.Mode == SourceModeRange ||
		request.Mode == SourceModeSequential && request.MaxBytes > binding.Size {
		return nil, ErrDerivedRepresentationUnavailable
	}
	if err := resolver.Revalidate(ctx, binding); err != nil {
		return nil, err
	}
	return &derivedSourceSession{resolver: resolver, binding: binding, request: request, ctx: nonNilContext(ctx)}, nil
}

type derivedRepresentationRow struct {
	ArtifactID             string
	ArtifactSetID          string
	BlobID                 string
	RecoveryPointID        string
	CatalogGenerationID    string
	EntryID                string
	SourceFingerprint      string
	SecurityPolicyRevision string
	SetState               string
	SetCompleteness        string
	ProjectionRequired     bool
	ProjectionPublished    bool
	Role                   string
	MediaType              string
	PlaintextSize          int64
	PlaintextDigest        string
	Completeness           string
	ReferenceState         string
	BlobState              string
	UpdatedAt              time.Time
}

func (resolver *DerivedRepresentationResolver) baseQuery(ctx context.Context) *gorm.DB {
	return resolver.db.WithContext(ctx).Table("backup_asset_derived_artifacts AS artifacts").
		Select(`artifacts.id AS artifact_id, artifacts.artifact_set_id, artifacts.blob_id,
			sets.recovery_point_id, sets.catalog_generation_id, sets.entry_id, sets.source_fingerprint,
			sets.security_policy_revision, sets.state AS set_state, sets.completeness AS set_completeness,
			sets.projection_required, sets.projection_published, sets.updated_at,
			artifacts.role, artifacts.media_type, artifacts.plaintext_size,
			artifacts.plaintext_digest, artifacts.completeness,
			refs.state AS reference_state, blobs.state AS blob_state`).
		Joins(`JOIN backup_asset_derived_artifact_sets AS sets ON sets.id = artifacts.artifact_set_id`).
		Joins(`JOIN backup_asset_derived_blob_references AS refs
			ON refs.artifact_id = artifacts.id AND refs.blob_id = artifacts.blob_id
			AND refs.recovery_point_id = sets.recovery_point_id
			AND refs.catalog_generation_id = sets.catalog_generation_id
			AND refs.entry_id = sets.entry_id AND refs.source_fingerprint = sets.source_fingerprint`).
		Joins(`JOIN backup_asset_derived_blobs AS blobs ON blobs.id = artifacts.blob_id`).
		Where("sets.state = ? AND refs.state = ? AND blobs.state = ?", "active", "active", "active")
}

func validDerivedRepresentationRequest(request DerivedRepresentationRequest) bool {
	if backupasset.ValidateAssetRef(request.Ref) != nil || backupasset.ValidateOpaqueID(request.CatalogGenerationID) != nil ||
		request.SourceFingerprint == "" || len(request.SourceFingerprint) > 128 ||
		request.SecurityPolicyRevision == "" || len(request.SecurityPolicyRevision) > 128 ||
		request.ExpectedEntryFingerprint != "" && !lowerHexContent(request.ExpectedEntryFingerprint, 64) {
		return false
	}
	if request.Provider != backupasset.ProviderRestic && request.Provider != backupasset.ProviderRsync && request.Provider != backupasset.ProviderRclone {
		return false
	}
	switch request.Renderer {
	case RendererEscapedText:
		return request.Profile == ProfileTextV1
	case RendererSafeRaster:
		return request.Profile == ProfileRasterV1
	case RendererSameOriginPDF:
		return request.Profile == ProfilePDFV1
	case RendererNativeAudio:
		return request.Profile == ProfileAudioV1
	case RendererNativeVideo:
		return request.Profile == ProfileVideoV1
	default:
		return false
	}
}

type DerivedProviderResolver func(
	context.Context,
	backupasset.AssetRef,
	string,
	string,
) (backupasset.ProviderKind, error)

// DerivedAttemptSourceResolver routes a Worker Input binding to an exact active
// complete text/OCR Derived representation when the descriptor carries its
// plaintext digest. Other bindings remain on the original Provider resolver.
type DerivedAttemptSourceResolver struct {
	primary                SourceResolver
	derived                *DerivedRepresentationResolver
	securityPolicyRevision string
	provider               DerivedProviderResolver
}

func NewDerivedAttemptSourceResolver(
	primary SourceResolver,
	derived *DerivedRepresentationResolver,
	securityPolicyRevision string,
	provider DerivedProviderResolver,
) (*DerivedAttemptSourceResolver, error) {
	if primary == nil || derived == nil || strings.TrimSpace(securityPolicyRevision) == "" ||
		len(securityPolicyRevision) > 128 || provider == nil {
		return nil, ErrDerivedRepresentationUnavailable
	}
	return &DerivedAttemptSourceResolver{
		primary: primary, derived: derived, securityPolicyRevision: securityPolicyRevision, provider: provider,
	}, nil
}

func (resolver *DerivedAttemptSourceResolver) OpenContentSource(
	ctx context.Context,
	request SourceRequest,
) (SourceSession, error) {
	if resolver == nil || resolver.primary == nil || resolver.derived == nil || resolver.provider == nil ||
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
	provider, err := resolver.provider(ctx, request.Ref, request.CatalogGenerationID, request.ExpectedSource)
	if err != nil || !validDerivedProvider(provider) {
		return nil, errors.Join(ErrDerivedRepresentationUnavailable, err)
	}
	binding, err := resolver.derived.Resolve(ctx, DerivedRepresentationRequest{
		Ref: request.Ref, CatalogGenerationID: request.CatalogGenerationID,
		SourceFingerprint: request.ExpectedSource, ExpectedEntryFingerprint: request.ExpectedEntry,
		SecurityPolicyRevision: resolver.securityPolicyRevision, Provider: provider,
		Renderer: RendererEscapedText, Profile: ProfileTextV1,
	})
	if err != nil || binding.setCompleteness != "complete" || binding.Completeness != "complete" ||
		(binding.Role != "content" && binding.Role != "ocr") || binding.MediaType != "text/plain" {
		return nil, errors.Join(ErrDerivedRepresentationUnavailable, err)
	}
	return resolver.derived.Open(ctx, binding, request)
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

func validDerivedRepresentationRow(row derivedRepresentationRow, request DerivedRepresentationRequest) bool {
	return backupasset.ValidateOpaqueID(row.ArtifactID) == nil && backupasset.ValidateOpaqueID(row.ArtifactSetID) == nil &&
		backupasset.ValidateOpaqueID(row.BlobID) == nil && row.RecoveryPointID == request.Ref.RecoveryPointID &&
		row.CatalogGenerationID == request.CatalogGenerationID && row.EntryID == request.Ref.EntryID &&
		row.SourceFingerprint == request.SourceFingerprint && row.SecurityPolicyRevision == request.SecurityPolicyRevision &&
		row.SetState == "active" && row.ReferenceState == "active" && row.BlobState == "active" &&
		(row.SetCompleteness == "complete" || row.SetCompleteness == "partial") &&
		(!row.ProjectionRequired || row.ProjectionPublished) && row.PlaintextSize >= 0 &&
		lowerHexContent(row.PlaintextDigest, 64) &&
		(row.Completeness == "complete" || row.Completeness == "partial")
}

func validDerivedBinding(binding DerivedRepresentation) bool {
	return validDerivedRepresentationRequest(DerivedRepresentationRequest{
		Ref: binding.Ref, CatalogGenerationID: binding.CatalogGenerationID,
		SourceFingerprint: binding.SourceFingerprint, SecurityPolicyRevision: binding.SecurityPolicyRevision,
		Provider: binding.Provider, Renderer: binding.Renderer, Profile: binding.Profile,
	}) && backupasset.ValidateOpaqueID(binding.artifactID) == nil && backupasset.ValidateOpaqueID(binding.artifactSetID) == nil &&
		backupasset.ValidateOpaqueID(binding.blobID) == nil &&
		(binding.setCompleteness == "complete" || binding.setCompleteness == "partial") &&
		binding.Size >= 0 && lowerHexContent(binding.EntryFingerprint, 64)
}

func derivedArtifactMatchesRenderer(role, mediaType string, renderer Renderer, profile RendererProfile) bool {
	switch renderer {
	case RendererEscapedText:
		return profile == ProfileTextV1 && (role == "content" || role == "ocr") && mediaType == "text/plain"
	case RendererSafeRaster:
		return profile == ProfileRasterV1 && role == "thumbnail" &&
			(mediaType == "image/png" || mediaType == "image/jpeg" || mediaType == "image/webp")
	case RendererSameOriginPDF:
		return profile == ProfilePDFV1 && role == "content" && mediaType == "application/pdf"
	case RendererNativeAudio:
		return profile == ProfileAudioV1 && role == "content" && strings.HasPrefix(mediaType, "audio/")
	case RendererNativeVideo:
		return profile == ProfileVideoV1 && role == "content" && strings.HasPrefix(mediaType, "video/")
	default:
		return false
	}
}

type derivedSourceSession struct {
	resolver *DerivedRepresentationResolver
	binding  DerivedRepresentation
	request  SourceRequest
	ctx      context.Context
	once     sync.Once
	reader   *derivedSourceReader
	closed   bool
	mu       sync.Mutex
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
			err := session.resolver.Revalidate(session.ctx, binding)
			if err == nil {
				err = session.resolver.read(session.ctx, DerivedArtifactRead{
					ArtifactID: binding.artifactID, RecoveryPointID: binding.Ref.RecoveryPointID,
					CatalogGenerationID: binding.CatalogGenerationID, EntryID: binding.Ref.EntryID,
					SourceFingerprint: binding.SourceFingerprint,
				}, pipeWriter)
			}
			_ = pipeWriter.CloseWithError(err)
		}()
	})
	return session.reader
}

func (session *derivedSourceSession) Revalidate(ctx context.Context) error {
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
