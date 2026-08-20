package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidContentProjection = errors.New("invalid backup asset content projection")
	ErrContentProjectionStale   = errors.New("backup asset content projection stale")
)

type TermFrequency struct {
	Term      string
	Frequency int
}

type ContentProjection struct {
	Ref                            backupasset.AssetRef
	Field                          SearchField
	Terms                          []TermFrequency
	SourceFingerprint              string
	CatalogGenerationID            string
	SearchGenerationID             string
	ProcessingLeaseID              string
	AttemptID                      string
	FenceToken                     string
	ExpectedClassificationRevision int
	Classification                 Sensitivity
	ClassificationRevision         int
	CoverageRevision               int
	PipelineRevision               int
	IndexRevision                  int
	ExcerptRef                     *string
	Coverage                       FieldCoverage
}

type RevokeProjection struct {
	Ref                            backupasset.AssetRef
	Field                          SearchField
	SourceFingerprint              string
	CatalogGenerationID            string
	SearchGenerationID             string
	ProcessingLeaseID              string
	AttemptID                      string
	FenceToken                     string
	ExpectedClassificationRevision int
	CoverageRevision               int
	PipelineRevision               int
	IndexRevision                  int
}

type ClassificationProjection struct {
	Ref                            backupasset.AssetRef
	SourceFingerprint              string
	CatalogGenerationID            string
	SearchGenerationID             string
	ProcessingLeaseID              string
	AttemptID                      string
	FenceToken                     string
	ExpectedClassificationRevision int
	Classification                 Sensitivity
	ClassificationRevision         int
	EvidenceArtifactID             string
}

type ContentIndexIngest interface {
	PublishContentProjection(context.Context, ContentProjection) error
	RevokeContentProjection(context.Context, RevokeProjection) error
}

type ContentIndexIngestTx interface {
	PrepareContentProjection(context.Context, ContentProjection) (PreparedContentProjection, error)
	PublishContentProjectionTx(context.Context, *gorm.DB, PreparedContentProjection) error
	RevokeContentProjectionTx(context.Context, *gorm.DB, RevokeProjection) error
}

type ClassificationIndexIngestTx interface {
	PrepareClassificationProjection(context.Context, ClassificationProjection) (PreparedClassificationProjection, error)
	PublishClassificationProjectionTx(context.Context, *gorm.DB, PreparedClassificationProjection) error
}

type PreparedContentProjection struct {
	owner      *ContentIngestService
	projection ContentProjection
	postings   []model.BackupAssetSearchPosting
	keyVersion int
}

type PreparedClassificationProjection struct {
	owner      *ContentIngestService
	projection ClassificationProjection
	keyVersion int
}

type ContentIngestLimits struct {
	MaxTerms     int
	MaxTermBytes int
	MaxTermRunes int
	MaxFrequency int
}

func DefaultContentIngestLimits() ContentIngestLimits {
	return ContentIngestLimits{MaxTerms: 2048, MaxTermBytes: 4096, MaxTermRunes: 2048, MaxFrequency: 1_000_000}
}

type ContentIngestDependencies struct {
	DB     *gorm.DB
	Keys   SearchKeySource
	Lease  SearchLease
	Now    func() time.Time
	Limits ContentIngestLimits
}

type ContentIngestService struct {
	db     *gorm.DB
	keys   SearchKeySource
	lease  SearchLease
	now    func() time.Time
	limits ContentIngestLimits
}

type contentProjectionControl struct {
	document   model.BackupAssetSearchDocument
	field      model.BackupAssetSearchDocumentField
	generation model.BackupAssetSearchGeneration
}

func NewContentIngestService(dependencies ContentIngestDependencies) (*ContentIngestService, error) {
	if dependencies.DB == nil || dependencies.Keys == nil || dependencies.Lease == nil ||
		dependencies.Limits.MaxTerms <= 0 || dependencies.Limits.MaxTerms > 100000 ||
		dependencies.Limits.MaxTermBytes <= 0 || dependencies.Limits.MaxTermRunes <= 0 ||
		dependencies.Limits.MaxTermRunes > dependencies.Limits.MaxTermBytes || dependencies.Limits.MaxFrequency <= 0 {
		return nil, fmt.Errorf("%w: content ingest dependencies", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &ContentIngestService{db: dependencies.DB, keys: dependencies.Keys, lease: dependencies.Lease, now: dependencies.Now, limits: dependencies.Limits}, nil
}

func (service *ContentIngestService) PublishContentProjection(ctx context.Context, projection ContentProjection) error {
	prepared, err := service.PrepareContentProjection(ctx, projection)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return service.PublishContentProjectionTx(ctx, tx, prepared)
	})
}

func (service *ContentIngestService) PrepareContentProjection(
	ctx context.Context,
	projection ContentProjection,
) (PreparedContentProjection, error) {
	if err := service.validateProjection(projection); err != nil {
		return PreparedContentProjection{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key, err := service.activeKey(ctx)
	if err != nil {
		return PreparedContentProjection{}, err
	}
	postings, err := service.prepareContentPostings(projection, key)
	if err != nil {
		return PreparedContentProjection{}, err
	}
	return PreparedContentProjection{
		owner: service, projection: projection, postings: postings, keyVersion: key.Version,
	}, nil
}

func (service *ContentIngestService) PublishContentProjectionTx(
	ctx context.Context,
	tx *gorm.DB,
	prepared PreparedContentProjection,
) error {
	if prepared.owner != service || !service.validContentTransaction(tx) {
		return ErrInvalidContentProjection
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx = tx.WithContext(ctx)
	projection := prepared.projection
	control, err := service.lockAndValidate(ctx, tx, projection.Ref, projection.Field, projection.SourceFingerprint,
		projection.CatalogGenerationID, projection.SearchGenerationID, projection.ProcessingLeaseID,
		projection.AttemptID, projection.FenceToken, projection.ExpectedClassificationRevision, prepared.keyVersion)
	if err != nil {
		return err
	}
	if projection.CoverageRevision <= control.field.CoverageRevision || projection.PipelineRevision < control.field.PipelineRevision ||
		projection.PipelineRevision == control.field.PipelineRevision && control.field.State != string(FieldCoverageUnavailable) ||
		projection.IndexRevision <= control.field.IndexRevision {
		return ErrContentProjectionStale
	}
	currentSensitivity := Sensitivity(control.document.Sensitivity)
	if projection.Classification == currentSensitivity {
		if projection.ClassificationRevision < control.document.ClassificationRevision {
			return ErrContentProjectionStale
		}
	} else if projection.ClassificationRevision <= control.document.ClassificationRevision {
		return ErrContentProjectionStale
	}
	now := service.utcNow()
	documentUpdate := tx.Model(&model.BackupAssetSearchDocument{}).
		Where("search_generation_id = ? AND document_id = ? AND classification_revision = ?",
			projection.SearchGenerationID, projection.Ref.EntryID, projection.ExpectedClassificationRevision).
		Updates(map[string]any{
			"sensitivity": projection.Classification, "classification_revision": projection.ClassificationRevision, "updated_at": now,
		})
	if documentUpdate.Error != nil {
		return fmt.Errorf("update content classification: %w", documentUpdate.Error)
	}
	if documentUpdate.RowsAffected != 1 {
		return ErrContentProjectionStale
	}
	classificationChanged := projection.ClassificationRevision != control.document.ClassificationRevision
	postingDelete := tx.Where("search_generation_id = ? AND document_id = ?",
		projection.SearchGenerationID, projection.Ref.EntryID)
	if classificationChanged {
		postingDelete = postingDelete.Where("field IN ?", []SearchField{SearchFieldContent, SearchFieldOCR})
	} else {
		postingDelete = postingDelete.Where("field = ?", projection.Field)
	}
	if err := postingDelete.Delete(&model.BackupAssetSearchPosting{}).Error; err != nil {
		return fmt.Errorf("replace content postings: %w", err)
	}
	if classificationChanged {
		if err := tx.Model(&model.BackupAssetSearchDocumentField{}).
			Where("search_generation_id = ? AND document_id = ? AND field IN ? AND field <> ?",
				projection.SearchGenerationID, projection.Ref.EntryID,
				[]SearchField{SearchFieldContent, SearchFieldOCR}, projection.Field).
			Updates(map[string]any{
				"state": FieldCoverageUnavailable, "classification_revision": projection.ClassificationRevision,
				"excerpt_ref": nil, "updated_at": now,
			}).Error; err != nil {
			return fmt.Errorf("invalidate sibling content fields: %w", err)
		}
	}
	if len(prepared.postings) > 0 {
		if err := tx.Create(&prepared.postings).Error; err != nil {
			return fmt.Errorf("publish content postings: %w", err)
		}
	}
	fieldUpdate := tx.Model(&model.BackupAssetSearchDocumentField{}).
		Where("search_generation_id = ? AND document_id = ? AND field = ? AND classification_revision = ?",
			projection.SearchGenerationID, projection.Ref.EntryID, projection.Field, control.field.ClassificationRevision).
		Updates(map[string]any{
			"state": projection.Coverage, "coverage_revision": projection.CoverageRevision,
			"classification_revision": projection.ClassificationRevision,
			"pipeline_revision":       projection.PipelineRevision, "index_revision": projection.IndexRevision,
			"source_fingerprint": projection.SourceFingerprint, "excerpt_ref": projection.ExcerptRef, "updated_at": now,
		})
	if fieldUpdate.Error != nil {
		return fmt.Errorf("update content field coverage: %w", fieldUpdate.Error)
	}
	if fieldUpdate.RowsAffected != 1 {
		return ErrContentProjectionStale
	}
	return service.advanceProjectionRevision(tx, projection.SearchGenerationID, control.generation.ProjectionRevision, now)
}

func (service *ContentIngestService) PrepareClassificationProjection(
	ctx context.Context,
	projection ClassificationProjection,
) (PreparedClassificationProjection, error) {
	if err := service.validateClassificationProjection(projection); err != nil {
		return PreparedClassificationProjection{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key, err := service.activeKey(ctx)
	if err != nil {
		return PreparedClassificationProjection{}, err
	}
	return PreparedClassificationProjection{owner: service, projection: projection, keyVersion: key.Version}, nil
}

func (service *ContentIngestService) PublishClassificationProjectionTx(
	ctx context.Context,
	tx *gorm.DB,
	prepared PreparedClassificationProjection,
) error {
	if prepared.owner != service || !service.validContentTransaction(tx) {
		return ErrInvalidContentProjection
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx = tx.WithContext(ctx)
	projection := prepared.projection
	contentControl, err := service.lockAndValidate(
		ctx, tx, projection.Ref, SearchFieldContent, projection.SourceFingerprint,
		projection.CatalogGenerationID, projection.SearchGenerationID, projection.ProcessingLeaseID,
		projection.AttemptID, projection.FenceToken, projection.ExpectedClassificationRevision, prepared.keyVersion,
	)
	if err != nil {
		return err
	}
	ocrControl, err := service.lockAndValidate(
		ctx, tx, projection.Ref, SearchFieldOCR, projection.SourceFingerprint,
		projection.CatalogGenerationID, projection.SearchGenerationID, projection.ProcessingLeaseID,
		projection.AttemptID, projection.FenceToken, projection.ExpectedClassificationRevision, prepared.keyVersion,
	)
	if err != nil {
		return err
	}
	if contentControl.document.SearchGenerationID != ocrControl.document.SearchGenerationID ||
		contentControl.document.DocumentID != ocrControl.document.DocumentID ||
		contentControl.document.ClassificationRevision != ocrControl.document.ClassificationRevision ||
		contentControl.generation.ID != ocrControl.generation.ID ||
		contentControl.generation.ProjectionRevision != ocrControl.generation.ProjectionRevision {
		return ErrContentProjectionStale
	}
	now := service.utcNow()
	documentUpdate := tx.Model(&model.BackupAssetSearchDocument{}).
		Where("search_generation_id = ? AND document_id = ? AND classification_revision = ?",
			projection.SearchGenerationID, projection.Ref.EntryID, projection.ExpectedClassificationRevision).
		Updates(map[string]any{
			"sensitivity": projection.Classification, "classification_revision": projection.ClassificationRevision,
			"updated_at": now,
		})
	if documentUpdate.Error != nil {
		return fmt.Errorf("update classification evidence: %w", documentUpdate.Error)
	}
	if documentUpdate.RowsAffected != 1 {
		return ErrContentProjectionStale
	}
	fields := []SearchField{SearchFieldContent, SearchFieldOCR}
	if err := tx.Where("search_generation_id = ? AND document_id = ? AND field IN ?",
		projection.SearchGenerationID, projection.Ref.EntryID, fields).
		Delete(&model.BackupAssetSearchPosting{}).Error; err != nil {
		return fmt.Errorf("remove classified content postings: %w", err)
	}
	fieldUpdate := tx.Model(&model.BackupAssetSearchDocumentField{}).
		Where(`search_generation_id = ? AND document_id = ? AND field IN ?
			AND classification_revision = ? AND source_fingerprint = ?`,
			projection.SearchGenerationID, projection.Ref.EntryID, fields,
			projection.ExpectedClassificationRevision, projection.SourceFingerprint).
		Updates(map[string]any{
			"state": FieldCoverageUnavailable, "classification_revision": projection.ClassificationRevision,
			"coverage_revision": gorm.Expr("coverage_revision + 1"),
			"index_revision":    gorm.Expr("index_revision + 1"),
			"excerpt_ref":       nil, "updated_at": now,
		})
	if fieldUpdate.Error != nil {
		return fmt.Errorf("invalidate classified content fields: %w", fieldUpdate.Error)
	}
	if fieldUpdate.RowsAffected != int64(len(fields)) {
		return ErrContentProjectionStale
	}
	return service.advanceProjectionRevision(tx, projection.SearchGenerationID, contentControl.generation.ProjectionRevision, now)
}

func (service *ContentIngestService) RevokeContentProjection(ctx context.Context, projection RevokeProjection) error {
	if err := service.validateRevoke(projection); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return service.RevokeContentProjectionTx(ctx, tx, projection)
	})
}

func (service *ContentIngestService) RevokeContentProjectionTx(
	ctx context.Context,
	tx *gorm.DB,
	projection RevokeProjection,
) error {
	if err := service.validateRevoke(projection); err != nil || !service.validContentTransaction(tx) {
		return ErrInvalidContentProjection
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx = tx.WithContext(ctx)
	var key model.WrappedDomainKey
	if err := tx.Where("domain = ? AND state = ?", backupasset.KeyDomainSearchToken, backupasset.DomainKeyActive).
		Take(&key).Error; err != nil {
		return ErrSearchKeyUnavailable
	}
	control, err := service.lockAndValidate(ctx, tx, projection.Ref, projection.Field, projection.SourceFingerprint,
		projection.CatalogGenerationID, projection.SearchGenerationID, projection.ProcessingLeaseID,
		projection.AttemptID, projection.FenceToken, projection.ExpectedClassificationRevision, key.Version)
	if err != nil {
		return err
	}
	if projection.CoverageRevision <= control.field.CoverageRevision || projection.PipelineRevision < control.field.PipelineRevision ||
		projection.IndexRevision <= control.field.IndexRevision {
		return ErrContentProjectionStale
	}
	if err := tx.Where("search_generation_id = ? AND document_id = ? AND field = ?",
		projection.SearchGenerationID, projection.Ref.EntryID, projection.Field).
		Delete(&model.BackupAssetSearchPosting{}).Error; err != nil {
		return fmt.Errorf("revoke content postings: %w", err)
	}
	now := service.utcNow()
	fieldUpdate := tx.Model(&model.BackupAssetSearchDocumentField{}).
		Where("search_generation_id = ? AND document_id = ? AND field = ? AND index_revision = ?",
			projection.SearchGenerationID, projection.Ref.EntryID, projection.Field, control.field.IndexRevision).
		Updates(map[string]any{
			"state": FieldCoverageUnavailable, "coverage_revision": projection.CoverageRevision,
			"pipeline_revision": projection.PipelineRevision, "index_revision": projection.IndexRevision,
			"source_fingerprint": projection.SourceFingerprint, "excerpt_ref": nil, "updated_at": now,
		})
	if fieldUpdate.Error != nil {
		return fmt.Errorf("revoke content field coverage: %w", fieldUpdate.Error)
	}
	if fieldUpdate.RowsAffected != 1 {
		return ErrContentProjectionStale
	}
	return service.advanceProjectionRevision(tx, projection.SearchGenerationID, control.generation.ProjectionRevision, now)
}

func (service *ContentIngestService) lockAndValidate(
	ctx context.Context,
	tx *gorm.DB,
	ref backupasset.AssetRef,
	field SearchField,
	sourceFingerprint, catalogGenerationID, searchGenerationID, leaseID, attemptID, fenceToken string,
	expectedClassificationRevision, keyVersion int,
) (contentProjectionControl, error) {
	if err := backupasset.ValidateRecoveryPointWriteAdmissionTx(ctx, tx, ref.RecoveryPointID); err != nil {
		return contentProjectionControl{}, err
	}
	var leaseRow model.RecoveryPointLease
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", leaseID).Take(&leaseRow).Error; err != nil {
		return contentProjectionControl{}, backupasset.ErrLeaseFenceLost
	}
	if leaseRow.RecoveryPointID != ref.RecoveryPointID || leaseRow.HolderType != string(backupasset.LeaseHolderProcessingJob) {
		return contentProjectionControl{}, backupasset.ErrLeaseFenceLost
	}
	fence := backupasset.LeaseFence{
		LeaseID: leaseID, RecoveryPointID: ref.RecoveryPointID, HolderType: backupasset.LeaseHolderProcessingJob,
		OwnerID: leaseRow.OwnerID, AttemptID: attemptID, FenceToken: fenceToken,
	}
	if err := service.lease.ValidateFenceTx(ctx, tx, fence); err != nil {
		return contentProjectionControl{}, err
	}
	var point model.RecoveryPoint
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", ref.RecoveryPointID).Take(&point).Error; err != nil ||
		point.SourceFingerprint != sourceFingerprint || !eligibleSearchPoint(point) {
		return contentProjectionControl{}, ErrSearchSourceChanged
	}
	var catalogGeneration model.CatalogGeneration
	if err := tx.Where("id = ?", catalogGenerationID).Take(&catalogGeneration).Error; err != nil ||
		catalogGeneration.RecoveryPointID != ref.RecoveryPointID || !catalogGeneration.IsActive || catalogGeneration.State != "complete" ||
		catalogGeneration.SourceFingerprint != sourceFingerprint {
		return contentProjectionControl{}, ErrSearchCatalogChanged
	}
	var generation model.BackupAssetSearchGeneration
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", searchGenerationID).Take(&generation).Error; err != nil ||
		generation.RecoveryPointID != ref.RecoveryPointID || generation.CatalogGenerationID != catalogGenerationID ||
		generation.SourceFingerprint != sourceFingerprint || generation.SearchKeyVersion != keyVersion ||
		!generation.IsActive || generation.State != string(SearchGenerationComplete) {
		return contentProjectionControl{}, ErrContentProjectionStale
	}
	var keyRow model.WrappedDomainKey
	if err := tx.Where("domain = ? AND state = ?", backupasset.KeyDomainSearchToken, backupasset.DomainKeyActive).Take(&keyRow).Error; err != nil || keyRow.Version != keyVersion {
		return contentProjectionControl{}, ErrSearchKeyUnavailable
	}
	var document model.BackupAssetSearchDocument
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("search_generation_id = ? AND document_id = ? AND recovery_point_id = ? AND catalog_generation_id = ? AND entry_id = ?",
			searchGenerationID, ref.EntryID, ref.RecoveryPointID, catalogGenerationID, ref.EntryID).
		Take(&document).Error; err != nil || document.ClassificationRevision != expectedClassificationRevision {
		return contentProjectionControl{}, ErrContentProjectionStale
	}
	var fieldRow model.BackupAssetSearchDocumentField
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("search_generation_id = ? AND document_id = ? AND field = ?", searchGenerationID, ref.EntryID, field).
		Take(&fieldRow).Error; err != nil {
		return contentProjectionControl{}, ErrContentProjectionStale
	}
	if fieldRow.ClassificationRevision != expectedClassificationRevision || fieldRow.SourceFingerprint != sourceFingerprint {
		return contentProjectionControl{}, ErrContentProjectionStale
	}
	return contentProjectionControl{document: document, field: fieldRow, generation: generation}, nil
}

func (service *ContentIngestService) prepareContentPostings(
	projection ContentProjection,
	key backupasset.DomainKeyMaterial,
) ([]model.BackupAssetSearchPosting, error) {
	type postingKey struct {
		kind TokenKind
		hmac string
	}
	frequencies := make(map[postingKey]int)
	for _, term := range projection.Terms {
		normalized, err := NormalizeFieldV1(projection.Field, term.Term, NormalizerLimits{
			MaxInputBytes: service.limits.MaxTermBytes, MaxRunes: service.limits.MaxTermRunes,
			MaxTokens: service.limits.MaxTerms, MaxTokenRunes: service.limits.MaxTermRunes,
		})
		if err != nil {
			return nil, ErrInvalidContentProjection
		}
		for _, token := range normalized.Tokens {
			digest, err := TokenHMAC(key.Key, key.Version, NormalizerVersion, projection.Field, token.Kind, token.Value)
			if err != nil {
				return nil, ErrInvalidContentProjection
			}
			posting := postingKey{kind: token.Kind, hmac: digest}
			frequencies[posting] += term.Frequency
			if frequencies[posting] > service.limits.MaxFrequency {
				return nil, ErrInvalidContentProjection
			}
		}
	}
	postings := make([]model.BackupAssetSearchPosting, 0, len(frequencies))
	for keyValue, frequency := range frequencies {
		postings = append(postings, model.BackupAssetSearchPosting{
			SearchGenerationID: projection.SearchGenerationID, DocumentID: projection.Ref.EntryID,
			Field: string(projection.Field), TokenKind: string(keyValue.kind), KeyVersion: key.Version,
			TokenHMAC: keyValue.hmac, TermFrequency: frequency,
		})
	}
	return postings, nil
}

func (service *ContentIngestService) validateProjection(projection ContentProjection) error {
	if service == nil || service.db == nil || !validContentField(projection.Field) ||
		backupasset.ValidateAssetRef(projection.Ref) != nil || len(projection.Terms) > service.limits.MaxTerms ||
		!validProjectionIdentity(projection.SourceFingerprint, projection.CatalogGenerationID, projection.SearchGenerationID,
			projection.ProcessingLeaseID, projection.AttemptID, projection.FenceToken) ||
		projection.ExpectedClassificationRevision <= 0 || projection.ClassificationRevision <= 0 ||
		!validSensitivity(projection.Classification) || projection.CoverageRevision <= 0 || projection.PipelineRevision <= 0 ||
		projection.IndexRevision <= 0 || (projection.Coverage != FieldCoverageComplete && projection.Coverage != FieldCoveragePartial) ||
		(projection.ExcerptRef != nil && !lowerHex(*projection.ExcerptRef, 32)) {
		return ErrInvalidContentProjection
	}
	for _, term := range projection.Terms {
		if strings.TrimSpace(term.Term) == "" || term.Term != strings.TrimSpace(term.Term) || !utf8.ValidString(term.Term) ||
			len(term.Term) > service.limits.MaxTermBytes || utf8.RuneCountInString(term.Term) > service.limits.MaxTermRunes ||
			strings.ContainsRune(term.Term, 0) || term.Frequency <= 0 || term.Frequency > service.limits.MaxFrequency {
			return ErrInvalidContentProjection
		}
	}
	return nil
}

func (service *ContentIngestService) validateRevoke(projection RevokeProjection) error {
	if service == nil || service.db == nil || !validContentField(projection.Field) || backupasset.ValidateAssetRef(projection.Ref) != nil ||
		!validProjectionIdentity(projection.SourceFingerprint, projection.CatalogGenerationID, projection.SearchGenerationID,
			projection.ProcessingLeaseID, projection.AttemptID, projection.FenceToken) ||
		projection.ExpectedClassificationRevision <= 0 || projection.CoverageRevision <= 0 ||
		projection.PipelineRevision <= 0 || projection.IndexRevision <= 0 {
		return ErrInvalidContentProjection
	}
	return nil
}

func (service *ContentIngestService) validateClassificationProjection(projection ClassificationProjection) error {
	evidenceValid := lowerHex(projection.EvidenceArtifactID, 32) ||
		projection.Classification == SensitivityUnknown && projection.EvidenceArtifactID == ""
	if service == nil || service.db == nil || backupasset.ValidateAssetRef(projection.Ref) != nil ||
		!validProjectionIdentity(projection.SourceFingerprint, projection.CatalogGenerationID, projection.SearchGenerationID,
			projection.ProcessingLeaseID, projection.AttemptID, projection.FenceToken) ||
		projection.ExpectedClassificationRevision <= 0 ||
		projection.ClassificationRevision != projection.ExpectedClassificationRevision+1 ||
		!validSensitivity(projection.Classification) || !evidenceValid {
		return ErrInvalidContentProjection
	}
	return nil
}

func validProjectionIdentity(source, catalogID, searchID, leaseID, attemptID, fenceToken string) bool {
	return source != "" && len(source) <= 128 && !strings.ContainsAny(source, "\r\n\x00") &&
		backupasset.ValidateOpaqueID(catalogID) == nil && backupasset.ValidateOpaqueID(searchID) == nil &&
		backupasset.ValidateOpaqueID(leaseID) == nil && backupasset.ValidateOpaqueID(attemptID) == nil && lowerHex(fenceToken, 64)
}

func validContentField(field SearchField) bool {
	return field == SearchFieldContent || field == SearchFieldOCR
}

func validSensitivity(value Sensitivity) bool {
	return value == SensitivityNonSecret || value == SensitivitySecret || value == SensitivityUnknown
}

func (service *ContentIngestService) activeKey(ctx context.Context) (backupasset.DomainKeyMaterial, error) {
	key, err := service.keys.Active(ctx, backupasset.KeyDomainSearchToken)
	if err != nil || key.Domain != backupasset.KeyDomainSearchToken || key.State != backupasset.DomainKeyActive || len(key.Key) != 32 {
		return backupasset.DomainKeyMaterial{}, ErrSearchKeyUnavailable
	}
	return key, nil
}

func (service *ContentIngestService) advanceProjectionRevision(tx *gorm.DB, searchID string, expected int64, now time.Time) error {
	result := tx.Model(&model.BackupAssetSearchGeneration{}).Where("id = ? AND projection_revision = ?", searchID, expected).
		Updates(map[string]any{"projection_revision": expected + 1, "updated_at": now})
	if result.Error != nil {
		return fmt.Errorf("advance content projection revision: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrContentProjectionStale
	}
	return nil
}

func (service *ContentIngestService) validContentTransaction(tx *gorm.DB) bool {
	if service == nil || service.db == nil || tx == nil || tx.Statement == nil || tx.Statement.ConnPool == nil {
		return false
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return false
	}
	serviceDB, serviceErr := service.db.DB()
	txDB, txErr := tx.DB()
	return serviceErr == nil && txErr == nil && serviceDB == txDB
}

func (service *ContentIngestService) utcNow() time.Time { return service.now().UTC() }

var _ ClassificationIndexIngestTx = (*ContentIngestService)(nil)
