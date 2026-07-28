package processing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	assetexport "xirang/backend/internal/backupasset/export"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/model"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	archiveMemberCreateEndpoint        = "archive_member_create"
	archiveMemberConflictAttempts      = 20
	defaultArchiveMemberIdempotencyTTL = 24 * time.Hour
)

var (
	ErrArchiveMemberUnavailable = errors.New("archive member unavailable")
	ErrArchiveNestedUnsupported = errors.New("archive nested retrieval unsupported")

	errArchiveMemberTerminalCleanupPending = errors.New("archive member terminal cleanup pending")
)

type ArchiveMemberIndexEntry struct {
	OpaqueID    string
	Ordinal     int
	ParentID    string
	DisplayName string
	Size        int64
	MediaType   string
	Warning     ArchiveMemberWarning
}

// ArchiveMemberWarning is intentionally closed so archive-index responses
// cannot surface worker diagnostics or raw archive paths.
type ArchiveMemberWarning string

const ArchiveMemberWarningNone ArchiveMemberWarning = "none"

type ArchiveMemberIndexBinding struct {
	ArtifactID             string
	Revision               string
	PipelineFingerprint    string
	SecurityPolicyRevision string
	AbsoluteExpiresAt      time.Time
	Members                []ArchiveMemberIndexEntry
}

type ArchiveMemberIndexLookup struct {
	Actor content.DeliveryActor
	Ref   backupasset.AssetRef
}

type ArchiveMemberIndexViewEntry struct {
	ID          string               `json:"id"`
	ParentID    string               `json:"parent_id,omitempty"`
	DisplayName string               `json:"display_name"`
	Type        string               `json:"type"`
	Size        int64                `json:"size"`
	MediaType   string               `json:"media_type"`
	Warning     ArchiveMemberWarning `json:"warning"`
}

type ArchiveMemberIndexView struct {
	SchemaVersion int                           `json:"schema_version"`
	IndexRevision string                        `json:"index_revision"`
	ExpiresAt     time.Time                     `json:"expires_at"`
	Entries       []ArchiveMemberIndexViewEntry `json:"entries"`
}

type ArchiveMemberIndexResolver func(
	context.Context,
	content.AuthorizedAsset,
	string,
) (ArchiveMemberIndexBinding, error)

type ArchiveMemberIndexRevalidator func(
	context.Context,
	model.BackupAssetArchiveMemberRequest,
) (ArchiveMemberIndexBinding, error)

type ArchiveMemberProcessingAuthority struct {
	ProviderCapabilityRevision int64
	SecurityPolicyRevision     string
}

type ArchiveMemberAuthorityResolver func(
	context.Context,
	model.BackupAssetArchiveMemberRequest,
) (ArchiveMemberProcessingAuthority, error)

// ArchiveMemberRuntimeAssetResolver resolves the owner-bound outer asset for
// background reconciliation. It is a runtime-only port: unlike Poll, it does
// not produce a client status or fallback product.
type ArchiveMemberRuntimeAssetResolver func(
	context.Context,
	model.BackupAssetArchiveMemberRequest,
) (content.AuthorizedAsset, error)

type ArchiveMemberExtractCapabilityResolver func(context.Context) (CapabilityAdvertisement, error)

type ArchiveMemberOutputResolver func(
	context.Context,
	content.ArchiveMemberArtifactRequest,
) (content.ResolvedArchiveMemberArtifact, error)

type ArchiveMemberDeliveryRevoker func(context.Context, string, string) error

type ArchiveMemberOutputRevoker func(context.Context, string, DerivedRevokeReason) error

type ArchiveMemberCreateRequest struct {
	Actor          content.DeliveryActor
	Ref            backupasset.AssetRef
	IdempotencyKey string
	IndexRevision  string
	MemberChain    []string
}

type ArchiveMemberCreateResult struct {
	SchemaVersion int                  `json:"schema_version"`
	RequestID     string               `json:"request_id"`
	AssetRef      backupasset.AssetRef `json:"asset_ref"`
	IndexRevision string               `json:"index_revision"`
	State         string               `json:"state"`
	Replayed      bool                 `json:"-"`
}

type ArchiveMemberState string

const (
	ArchiveMemberQueued   ArchiveMemberState = "queued"
	ArchiveMemberRunning  ArchiveMemberState = "running"
	ArchiveMemberReady    ArchiveMemberState = "ready"
	ArchiveMemberFailed   ArchiveMemberState = "failed"
	ArchiveMemberCanceled ArchiveMemberState = "canceled"
	ArchiveMemberExpired  ArchiveMemberState = "expired"
)

type ArchiveFailureProduct string

const (
	ArchiveFailureNone        ArchiveFailureProduct = ""
	ArchiveFailureEncrypted   ArchiveFailureProduct = "encrypted"
	ArchiveFailureUnsupported ArchiveFailureProduct = "unsupported"
	ArchiveFailureLimit       ArchiveFailureProduct = "limit"
	ArchiveFailureUnsafe      ArchiveFailureProduct = "unsafe"
	ArchiveFailureUnavailable ArchiveFailureProduct = "unavailable"
)

type ArchiveFallbackAction string

const (
	ArchiveFallbackDownloadOriginal ArchiveFallbackAction = "download_original"
)

type ArchiveFallbackReason string

const (
	ArchiveFallbackOriginalUnavailable ArchiveFallbackReason = "original_download_unavailable"
)

type ArchiveFallbackProduct struct {
	Action ArchiveFallbackAction `json:"action,omitempty"`
	Reason ArchiveFallbackReason `json:"reason,omitempty"`
}

type ArchiveMemberLookup struct {
	Actor         content.DeliveryActor
	Ref           backupasset.AssetRef
	RequestID     string
	IndexRevision string
}

type ArchiveMemberStatusResult struct {
	SchemaVersion  int                    `json:"schema_version"`
	RequestID      string                 `json:"request_id"`
	AssetRef       backupasset.AssetRef   `json:"asset_ref"`
	IndexRevision  string                 `json:"index_revision"`
	State          ArchiveMemberState     `json:"state"`
	FailureProduct ArchiveFailureProduct  `json:"failure_product,omitempty"`
	Fallback       ArchiveFallbackProduct `json:"fallback"`
	Retryable      bool                   `json:"retryable"`
	Terminal       bool                   `json:"terminal"`
}

type ArchiveMemberServiceDependencies struct {
	DB                       *gorm.DB
	Coordinator              capabilityCoordinator
	Authorize                capabilityAssetAuthorizer
	ResolveIndex             ArchiveMemberIndexResolver
	RevalidateIndex          ArchiveMemberIndexRevalidator
	ResolveAuthority         ArchiveMemberAuthorityResolver
	ResolveRuntimeAsset      ArchiveMemberRuntimeAssetResolver
	ResolveExtractCapability ArchiveMemberExtractCapabilityResolver
	ResolveOutput            ArchiveMemberOutputResolver
	ResolveReadyOutput       ArchiveMemberOutputResolver
	RevokeDeliveries         ArchiveMemberDeliveryRevoker
	RevokeOutput             ArchiveMemberOutputRevoker
	Now                      func() time.Time
	IdempotencyTTL           time.Duration
	IdempotencyKeyMaxBytes   int
}

type ArchiveMemberService struct {
	db                       *gorm.DB
	coordinator              capabilityCoordinator
	authorize                capabilityAssetAuthorizer
	resolveIndex             ArchiveMemberIndexResolver
	revalidateIndex          ArchiveMemberIndexRevalidator
	resolveAuthority         ArchiveMemberAuthorityResolver
	resolveRuntimeAsset      ArchiveMemberRuntimeAssetResolver
	resolveExtractCapability ArchiveMemberExtractCapabilityResolver
	resolveOutput            ArchiveMemberOutputResolver
	resolveReadyOutput       ArchiveMemberOutputResolver
	revokeDeliveries         ArchiveMemberDeliveryRevoker
	revokeOutput             ArchiveMemberOutputRevoker
	now                      func() time.Time
	idempotencyTTL           time.Duration
	idempotencyKeyMaxBytes   int

	maintenanceMu     sync.Mutex
	maintenanceCursor string
}

func NewArchiveMemberService(dependencies ArchiveMemberServiceDependencies) (*ArchiveMemberService, error) {
	if dependencies.DB == nil || dependencies.Coordinator == nil || dependencies.Authorize == nil || dependencies.ResolveIndex == nil {
		return nil, fmt.Errorf("%w: archive member dependencies are unavailable", ErrInvalidContract)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.IdempotencyTTL == 0 {
		dependencies.IdempotencyTTL = defaultArchiveMemberIdempotencyTTL
	}
	if dependencies.IdempotencyKeyMaxBytes == 0 {
		dependencies.IdempotencyKeyMaxBytes = assetexport.MaxIdempotencyKeyBytes
	}
	if dependencies.IdempotencyTTL <= 0 || !assetexport.ValidIdempotencyKeyMaxBytes(dependencies.IdempotencyKeyMaxBytes) {
		return nil, fmt.Errorf("%w: archive member idempotency settings are invalid", ErrInvalidContract)
	}
	return &ArchiveMemberService{
		db: dependencies.DB, coordinator: dependencies.Coordinator, authorize: dependencies.Authorize,
		resolveIndex: dependencies.ResolveIndex, revalidateIndex: dependencies.RevalidateIndex,
		resolveAuthority: dependencies.ResolveAuthority, resolveRuntimeAsset: dependencies.ResolveRuntimeAsset,
		resolveExtractCapability: dependencies.ResolveExtractCapability, resolveOutput: dependencies.ResolveOutput,
		resolveReadyOutput: dependencies.ResolveReadyOutput,
		revokeDeliveries:   dependencies.RevokeDeliveries, revokeOutput: dependencies.RevokeOutput,
		now: dependencies.Now, idempotencyTTL: dependencies.IdempotencyTTL,
		idempotencyKeyMaxBytes: dependencies.IdempotencyKeyMaxBytes,
	}, nil
}

func (service *ArchiveMemberService) ListIndex(
	ctx context.Context,
	lookup ArchiveMemberIndexLookup,
) (ArchiveMemberIndexView, error) {
	if service == nil || service.authorize == nil || service.resolveIndex == nil || lookup.Actor.UserID == 0 ||
		backupasset.ValidateAssetRef(lookup.Ref) != nil {
		return ArchiveMemberIndexView{}, ErrArchiveMemberUnavailable
	}
	ctx = nonNilProcessingContext(ctx)
	asset, err := service.authorize.Authorize(ctx, lookup.Actor, lookup.Ref, content.DeliveryPreview)
	if err != nil || !validArchiveMemberAsset(asset, lookup.Ref) {
		return ArchiveMemberIndexView{}, errors.Join(ErrArchiveMemberUnavailable, err)
	}
	index, err := service.resolveIndex(ctx, asset, "")
	if err != nil || !validArchiveMemberIndex(index, index.Revision, service.utcNow()) {
		return ArchiveMemberIndexView{}, errors.Join(ErrArchiveMemberUnavailable, err)
	}
	entries := make([]ArchiveMemberIndexViewEntry, len(index.Members))
	for position, member := range index.Members {
		warning, _ := normalizeArchiveMemberWarning(member.Warning)
		entries[position] = ArchiveMemberIndexViewEntry{
			ID: member.OpaqueID, ParentID: member.ParentID, DisplayName: member.DisplayName,
			Type: "file", Size: member.Size, MediaType: member.MediaType, Warning: warning,
		}
	}
	return ArchiveMemberIndexView{
		SchemaVersion: 1, IndexRevision: index.Revision,
		ExpiresAt: index.AbsoluteExpiresAt.UTC(), Entries: entries,
	}, nil
}

func (service *ArchiveMemberService) AuthorizeReadyDelivery(
	ctx context.Context,
	lookup ArchiveMemberLookup,
) (content.AuthorizedAsset, error) {
	if service == nil || service.db == nil || service.authorize == nil || lookup.Actor.UserID == 0 ||
		backupasset.ValidateAssetRef(lookup.Ref) != nil || backupasset.ValidateOpaqueID(lookup.RequestID) != nil {
		return content.AuthorizedAsset{}, ErrArchiveMemberUnavailable
	}
	ctx = nonNilProcessingContext(ctx)
	var row model.BackupAssetArchiveMemberRequest
	result := service.db.WithContext(ctx).
		Where("id = ? AND owner_user_id = ?", lookup.RequestID, lookup.Actor.UserID).
		Limit(1).Find(&row)
	if result.Error != nil {
		return content.AuthorizedAsset{}, fmt.Errorf("load ready archive member delivery: %w", result.Error)
	}
	if result.RowsAffected != 1 || row.State != string(ArchiveMemberReady) ||
		row.RecoveryPointID != lookup.Ref.RecoveryPointID || row.EntryID != lookup.Ref.EntryID ||
		!service.utcNow().Before(row.AbsoluteExpiresAt.UTC()) {
		return content.AuthorizedAsset{}, ErrArchiveMemberUnavailable
	}
	asset, err := service.authorize.Authorize(ctx, lookup.Actor, lookup.Ref, content.DeliveryDownload)
	if err != nil || !validArchiveMemberAsset(asset, lookup.Ref) ||
		row.CatalogGenerationID != asset.CatalogGenerationID || row.SourceFingerprint != asset.SourceFingerprint ||
		row.EntryFingerprint != asset.EntryFingerprint {
		return content.AuthorizedAsset{}, errors.Join(ErrArchiveMemberUnavailable, err)
	}
	return asset, nil
}

func (service *ArchiveMemberService) Create(
	ctx context.Context,
	request ArchiveMemberCreateRequest,
) (ArchiveMemberCreateResult, error) {
	if service == nil || service.db == nil {
		return ArchiveMemberCreateResult{}, ErrArchiveMemberUnavailable
	}
	keyDigest, intentDigest, memberDigest, memberID, err := archiveMemberRequestDigests(request, service.idempotencyKeyMaxBytes)
	if errors.Is(err, ErrArchiveNestedUnsupported) {
		return ArchiveMemberCreateResult{}, err
	}
	if err != nil {
		if errors.Is(err, assetexport.ErrInvalidIdempotency) {
			return ArchiveMemberCreateResult{}, err
		}
		return ArchiveMemberCreateResult{}, ErrArchiveMemberUnavailable
	}
	ctx = nonNilProcessingContext(ctx)
	if replay, found, replayErr := service.loadReplay(ctx, request.Actor.UserID, keyDigest, intentDigest); found || replayErr != nil {
		return replay, replayErr
	}
	asset, err := service.authorize.Authorize(ctx, request.Actor, request.Ref, content.DeliveryPreview)
	if err != nil {
		return ArchiveMemberCreateResult{}, err
	}
	if !validArchiveMemberAsset(asset, request.Ref) {
		return ArchiveMemberCreateResult{}, ErrArchiveMemberUnavailable
	}
	index, err := service.resolveIndex(ctx, asset, request.IndexRevision)
	if err != nil || !validArchiveMemberIndex(index, request.IndexRevision, service.utcNow()) {
		return ArchiveMemberCreateResult{}, errors.Join(ErrArchiveMemberUnavailable, err)
	}
	ordinal, found := resolveArchiveMemberOrdinal(index.Members, memberID)
	if !found {
		return ArchiveMemberCreateResult{}, ErrArchiveMemberUnavailable
	}
	now := service.utcNow()
	requestID, err := backupasset.NewOpaqueID()
	if err != nil {
		return ArchiveMemberCreateResult{}, fmt.Errorf("create archive member request identity: %w", err)
	}
	row := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: request.Actor.UserID, Endpoint: archiveMemberCreateEndpoint,
		KeyDigest: keyDigest, RequestIntentDigest: intentDigest,
		RecoveryPointID: request.Ref.RecoveryPointID, EntryID: request.Ref.EntryID,
		CatalogGenerationID: asset.CatalogGenerationID, SourceFingerprint: asset.SourceFingerprint,
		EntryFingerprint: asset.EntryFingerprint, IndexArtifactID: index.ArtifactID,
		IndexRevision: index.Revision, MemberChainDigest: memberDigest, ResolvedOrdinal: ordinal,
		State: "queued", AbsoluteExpiresAt: index.AbsoluteExpiresAt.UTC(), CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	var committed ArchiveMemberCreateResult
	err = retryArchiveMemberConflicts(ctx, func() error {
		return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			transactionNow := service.utcNow()
			if !transactionNow.Before(index.AbsoluteExpiresAt.UTC()) {
				return ErrArchiveMemberUnavailable
			}
			existing, found, err := service.loadReplayRowTx(ctx, tx, request.Actor.UserID, keyDigest, true)
			if err != nil {
				return err
			}
			if found {
				if !validArchiveMemberIdempotencyExpiry(existing.IdempotencyExpiresAt) {
					return ErrArchiveMemberUnavailable
				}
				if transactionNow.Before(existing.IdempotencyExpiresAt.UTC()) {
					if existing.RequestIntentDigest != intentDigest {
						return backupasset.ErrConflict
					}
					committed = archiveMemberResult(existing, true)
					return nil
				}
				tombstoneDigest, err := archiveMemberExpiredReceiptTombstoneDigest(existing.KeyDigest, existing.ID)
				if err != nil {
					return err
				}
				rotated := tx.WithContext(ctx).Model(&model.BackupAssetArchiveMemberRequest{}).
					Where("id = ? AND owner_user_id = ? AND endpoint = ? AND key_digest = ? AND idempotency_expires_at <= ?",
						existing.ID, request.Actor.UserID, archiveMemberCreateEndpoint, keyDigest, transactionNow).
					Updates(map[string]any{"key_digest": tombstoneDigest, "updated_at": transactionNow})
				if rotated.Error != nil {
					return fmt.Errorf("rotate expired archive member idempotency receipt: %w", rotated.Error)
				}
				if rotated.RowsAffected != 1 {
					return ErrRevisionConflict
				}
			}
			if err := ensureArchiveMemberUseLatchTx(ctx, tx, transactionNow); err != nil {
				return err
			}
			row.IdempotencyExpiresAt = transactionNow.Add(service.idempotencyTTL)
			row.UpdatedAt = transactionNow
			if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
				return fmt.Errorf("create archive member request: %w", err)
			}
			committed = archiveMemberResult(row, false)
			return nil
		})
	})
	if err == nil {
		return committed, nil
	}
	if replay, found, replayErr := service.loadReplay(ctx, request.Actor.UserID, keyDigest, intentDigest); found || replayErr != nil {
		return replay, replayErr
	}
	return ArchiveMemberCreateResult{}, err
}

func (service *ArchiveMemberService) loadReplay(
	ctx context.Context,
	ownerUserID uint,
	keyDigest string,
	intentDigest string,
) (ArchiveMemberCreateResult, bool, error) {
	row, found, err := service.loadReplayRowTx(ctx, service.db.WithContext(ctx), ownerUserID, keyDigest, false)
	if err != nil {
		return ArchiveMemberCreateResult{}, false, fmt.Errorf("load archive member replay: %w", err)
	}
	if !found {
		return ArchiveMemberCreateResult{}, false, nil
	}
	if !validArchiveMemberIdempotencyExpiry(row.IdempotencyExpiresAt) {
		return ArchiveMemberCreateResult{}, false, ErrArchiveMemberUnavailable
	}
	if !service.utcNow().Before(row.IdempotencyExpiresAt.UTC()) {
		return ArchiveMemberCreateResult{}, false, nil
	}
	if row.RequestIntentDigest != intentDigest {
		return ArchiveMemberCreateResult{}, true, backupasset.ErrConflict
	}
	return archiveMemberResult(row, true), true, nil
}

func (service *ArchiveMemberService) loadReplayRowTx(
	ctx context.Context,
	tx *gorm.DB,
	ownerUserID uint,
	keyDigest string,
	lock bool,
) (model.BackupAssetArchiveMemberRequest, bool, error) {
	if tx == nil {
		return model.BackupAssetArchiveMemberRequest{}, false, ErrArchiveMemberUnavailable
	}
	var row model.BackupAssetArchiveMemberRequest
	err := retryArchiveMemberConflicts(ctx, func() error {
		query := tx.WithContext(ctx).Where("owner_user_id = ? AND endpoint = ? AND key_digest = ?", ownerUserID, archiveMemberCreateEndpoint, keyDigest)
		if lock {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		err := query.Limit(1).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	})
	if err != nil {
		return model.BackupAssetArchiveMemberRequest{}, false, err
	}
	return row, row.ID != "", nil
}

func (service *ArchiveMemberService) Reconcile(ctx context.Context, requestID string) error {
	if service == nil || service.db == nil || service.coordinator == nil || service.revalidateIndex == nil ||
		service.resolveAuthority == nil ||
		service.resolveExtractCapability == nil || backupasset.ValidateOpaqueID(requestID) != nil {
		return ErrArchiveMemberUnavailable
	}
	ctx = nonNilProcessingContext(ctx)
	var row model.BackupAssetArchiveMemberRequest
	result := service.db.WithContext(ctx).Where("id = ?", requestID).Limit(1).Find(&row)
	if result.Error != nil {
		return fmt.Errorf("load archive member reconciliation: %w", result.Error)
	}
	if result.RowsAffected != 1 || row.ID != requestID || !service.utcNow().Before(row.AbsoluteExpiresAt.UTC()) {
		return ErrArchiveMemberUnavailable
	}
	if row.ProcessingInterestID != nil || row.ProcessingJobID != nil {
		if row.ProcessingInterestID == nil || row.ProcessingJobID == nil ||
			backupasset.ValidateOpaqueID(*row.ProcessingInterestID) != nil || backupasset.ValidateOpaqueID(*row.ProcessingJobID) != nil ||
			(row.State != "running" && row.State != "ready") {
			return ErrArchiveMemberUnavailable
		}
		return nil
	}
	if row.State != "queued" {
		return ErrArchiveMemberUnavailable
	}
	index, err := service.revalidateIndex(ctx, row)
	if err != nil || !validArchiveMemberIndex(index, row.IndexRevision, service.utcNow()) ||
		!archiveMemberIndexStillBound(row, index) {
		return errors.Join(ErrArchiveMemberUnavailable, err)
	}
	authority, err := service.resolveAuthority(ctx, row)
	if err != nil || authority.ProviderCapabilityRevision <= 0 ||
		strings.TrimSpace(authority.SecurityPolicyRevision) == "" || len(authority.SecurityPolicyRevision) > 128 {
		return errors.Join(ErrArchiveMemberUnavailable, err)
	}
	advertisement, err := service.resolveExtractCapability(ctx)
	if err != nil {
		return errors.Join(ErrArchiveMemberUnavailable, err)
	}
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilityArchiveExtractEntry,
		capabilityspec.ProfileArchiveMemberV1,
		false,
	)
	if !ok || advertisement.SchemaVersion != 1 || advertisement.Capability != profile.Capability ||
		advertisement.CapabilitySchema != profile.CapabilitySchema || advertisement.OutputProfile != profile.OutputProfile ||
		strings.TrimSpace(advertisement.PipelineFingerprint) == "" || len(advertisement.PipelineFingerprint) > 128 {
		return ErrArchiveMemberUnavailable
	}
	parameters := CanonicalProductionParameters(profile)
	parameters.MemberStart = row.ResolvedOrdinal
	parameters.MemberEnd = row.ResolvedOrdinal
	parameters.TruncationPolicy = "reject"
	descriptor := WorkDescriptorV1{
		SchemaVersion:       1,
		Source:              backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID},
		CatalogGenerationID: row.CatalogGenerationID, SourceFingerprint: row.SourceFingerprint,
		EntryFingerprint: row.EntryFingerprint, ProviderCapabilityRevision: authority.ProviderCapabilityRevision,
		Capability: advertisement.Capability, CapabilitySchema: advertisement.CapabilitySchema,
		PipelineFingerprint: advertisement.PipelineFingerprint, OutputProfile: advertisement.OutputProfile,
		SecurityPolicyRevision: authority.SecurityPolicyRevision, Parameters: parameters,
	}
	if err := ValidateProductionWorkDescriptorV1(descriptor, false); err != nil {
		return errors.Join(ErrArchiveMemberUnavailable, err)
	}
	work, err := service.coordinator.RequestWork(ctx, WorkRequest{
		Descriptor: descriptor,
		Interest: InterestRequest{
			OwnerKind: InterestSystem, OwnerKey: archiveMemberInterestOwnerKey(row.ID),
			PriorityClass: PriorityInteractive, Priority: 900,
		},
	})
	if err != nil {
		return err
	}
	if backupasset.ValidateOpaqueID(work.JobID) != nil || backupasset.ValidateOpaqueID(work.InterestID) != nil || !lowerHex(work.WorkKey, 64) {
		return ErrArchiveMemberUnavailable
	}
	now := service.utcNow()
	updated := service.db.WithContext(ctx).Model(&model.BackupAssetArchiveMemberRequest{}).
		Where("id = ? AND state = ? AND version = ? AND processing_interest_id IS NULL AND processing_job_id IS NULL",
			row.ID, "queued", row.Version).
		Updates(map[string]any{
			"processing_interest_id": work.InterestID, "processing_job_id": work.JobID, "state": "running",
			"updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if updated.Error != nil {
		return fmt.Errorf("bind archive member Processing interest: %w", updated.Error)
	}
	if updated.RowsAffected == 1 {
		return nil
	}
	var winner model.BackupAssetArchiveMemberRequest
	winnerResult := service.db.WithContext(ctx).Where("id = ?", row.ID).Limit(1).Find(&winner)
	if winnerResult.Error != nil || winnerResult.RowsAffected != 1 || winner.State != "running" ||
		winner.ProcessingInterestID == nil || *winner.ProcessingInterestID != work.InterestID ||
		winner.ProcessingJobID == nil || *winner.ProcessingJobID != work.JobID {
		return ErrRevisionConflict
	}
	return nil
}

// ReconcilePending performs bounded, runtime-owned maintenance for durable
// archive-member requests. It binds committed queued requests and projects
// running terminal/expiry state without using the HTTP Poll surface or
// returning a client-visible status/fallback product.
func (service *ArchiveMemberService) ReconcilePending(ctx context.Context, limit int) (int, error) {
	if service == nil || service.db == nil || service.coordinator == nil || limit <= 0 {
		return 0, ErrArchiveMemberUnavailable
	}
	ctx = nonNilProcessingContext(ctx)
	service.maintenanceMu.Lock()
	defer service.maintenanceMu.Unlock()

	requestIDs, err := service.nextMaintenanceRequestIDs(ctx, limit)
	if err != nil {
		return 0, err
	}
	var reconciliationErrors []error
	for _, requestID := range requestIDs {
		if err := service.reconcilePendingRequest(ctx, requestID); err != nil &&
			(errors.Is(err, errArchiveMemberTerminalCleanupPending) || !archiveMemberMaintenanceRetryable(err)) {
			reconciliationErrors = append(reconciliationErrors, err)
		}
	}
	return len(requestIDs), errors.Join(reconciliationErrors...)
}

func (service *ArchiveMemberService) nextMaintenanceRequestIDs(ctx context.Context, limit int) ([]string, error) {
	if service == nil || service.db == nil || limit <= 0 {
		return nil, ErrArchiveMemberUnavailable
	}
	load := func(cursor string, after bool, remaining int) ([]string, error) {
		query := service.archiveMemberMaintenanceQuery(ctx)
		if cursor != "" {
			if after {
				query = query.Where("id > ?", cursor)
			} else {
				query = query.Where("id <= ?", cursor)
			}
		}
		var requestIDs []string
		if err := query.Order("id ASC").Limit(remaining).Pluck("id", &requestIDs).Error; err != nil {
			return nil, fmt.Errorf("load archive member maintenance requests: %w", err)
		}
		return requestIDs, nil
	}

	requestIDs, err := load(service.maintenanceCursor, true, limit)
	if err != nil {
		return nil, err
	}
	if len(requestIDs) < limit && service.maintenanceCursor != "" {
		wrapped, wrapErr := load(service.maintenanceCursor, false, limit-len(requestIDs))
		if wrapErr != nil {
			return nil, wrapErr
		}
		requestIDs = append(requestIDs, wrapped...)
	}
	if len(requestIDs) > 0 {
		service.maintenanceCursor = requestIDs[len(requestIDs)-1]
	}
	return requestIDs, nil
}

func (service *ArchiveMemberService) archiveMemberMaintenanceQuery(ctx context.Context) *gorm.DB {
	requestTable := model.BackupAssetArchiveMemberRequest{}.TableName()
	jobTable := model.BackupAssetProcessingJob{}.TableName()
	artifactSetTable := model.BackupAssetDerivedArtifactSet{}.TableName()
	artifactTable := model.BackupAssetDerivedArtifact{}.TableName()
	blobTable := model.BackupAssetDerivedBlob{}.TableName()
	deliveryGrantTable := model.BackupAssetExportDeliveryGrant{}.TableName()
	interestTable := model.BackupAssetProcessingInterest{}.TableName()
	return service.db.WithContext(ctx).Model(&model.BackupAssetArchiveMemberRequest{}).Where(
		fmt.Sprintf(`state IN ? OR (
			((state = ? AND error_category = ?) OR state IN ?) AND (
				EXISTS (
					SELECT 1 FROM %s AS jobs
					INNER JOIN %s AS artifact_sets ON artifact_sets.id = jobs.current_artifact_set_id
					WHERE jobs.id = %s.processing_job_id AND (
						artifact_sets.state IN ?
						OR EXISTS (
							SELECT 1 FROM %s AS artifacts
							INNER JOIN %s AS blobs ON blobs.id = artifacts.blob_id
							WHERE artifacts.artifact_set_id = artifact_sets.id AND blobs.state = ?
						)
					)
				)
				OR EXISTS (
					SELECT 1 FROM %s AS delivery_grants
					WHERE delivery_grants.resource_kind = ? AND delivery_grants.member_request_id = %s.id
						AND (
							delivery_grants.state IN ?
							OR (delivery_grants.state = ? AND delivery_grants.audit_state IN ?)
						)
				)
				OR EXISTS (
					SELECT 1 FROM %s AS interests
					WHERE interests.id = %s.processing_interest_id
						AND interests.job_id = %s.processing_job_id
						AND interests.owner_kind = ? AND interests.owner_key LIKE ? AND interests.active = ?
				)
			)
		)`, jobTable, artifactSetTable, requestTable, artifactTable, blobTable, deliveryGrantTable, requestTable,
			interestTable, requestTable, requestTable),
		[]string{string(ArchiveMemberQueued), string(ArchiveMemberRunning), string(ArchiveMemberReady)},
		string(ArchiveMemberFailed), string(ArchiveFailureUnavailable),
		[]string{string(ArchiveMemberCanceled), string(ArchiveMemberExpired)}, []string{"active", "purge_failed"}, "purge_failed",
		"archive_member", []string{"issued", "active", "draining"}, "revoked", []string{"pending", "retry_wait", "failed"},
		string(InterestSystem), "archive-member:%", true,
	)
}

func (service *ArchiveMemberService) reconcilePendingRequest(ctx context.Context, requestID string) error {
	var row model.BackupAssetArchiveMemberRequest
	result := service.db.WithContext(ctx).Where("id = ?", requestID).Limit(1).Find(&row)
	if result.Error != nil {
		return fmt.Errorf("load archive member maintenance request: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return nil
	}
	if row.State == string(ArchiveMemberFailed) || row.State == string(ArchiveMemberCanceled) ||
		row.State == string(ArchiveMemberExpired) {
		return service.reconcileTerminalArchiveMemberCleanup(ctx, row)
	}
	if row.State != string(ArchiveMemberQueued) && row.State != string(ArchiveMemberRunning) &&
		row.State != string(ArchiveMemberReady) {
		return nil
	}
	now := service.utcNow()
	if !now.Before(row.AbsoluteExpiresAt.UTC()) {
		_, err := service.expireArchiveMember(ctx, row, now)
		return err
	}
	if row.State == string(ArchiveMemberQueued) {
		return service.finishArchiveMemberMaintenance(ctx, row, service.Reconcile(ctx, row.ID))
	}
	if service.resolveRuntimeAsset == nil {
		return ErrArchiveMemberUnavailable
	}
	asset, err := service.resolveRuntimeAsset(ctx, row)
	if err != nil {
		return service.finishArchiveMemberMaintenance(ctx, row, errors.Join(ErrArchiveMemberUnavailable, err))
	}
	ref := backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID}
	if !validArchiveMemberAsset(asset, ref) || row.CatalogGenerationID != asset.CatalogGenerationID ||
		row.SourceFingerprint != asset.SourceFingerprint || row.EntryFingerprint != asset.EntryFingerprint {
		return service.invalidateArchiveMember(ctx, row, "source_changed")
	}
	_, err = service.reconcileActiveArchiveMember(ctx, row, asset)
	return service.finishArchiveMemberMaintenance(ctx, row, err)
}

func (service *ArchiveMemberService) finishArchiveMemberMaintenance(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
	err error,
) error {
	if err == nil {
		return nil
	}
	reason, invalidates := archiveMemberMaintenanceInvalidationReason(err)
	if !invalidates {
		return err
	}
	if service.archiveMemberMaintenanceAlreadyInvalidated(ctx, row.ID) {
		pending, pendingErr := service.archiveMemberTerminalCleanupPending(ctx, row.ID)
		if pendingErr != nil {
			return errors.Join(err, pendingErr)
		}
		if pending {
			return archiveMemberTerminalCleanupError(err)
		}
		return nil
	}
	if invalidateErr := service.invalidateArchiveMember(ctx, row, reason); invalidateErr != nil {
		return errors.Join(err, invalidateErr)
	}
	return nil
}

func (service *ArchiveMemberService) archiveMemberMaintenanceAlreadyInvalidated(ctx context.Context, requestID string) bool {
	if service == nil || service.db == nil {
		return false
	}
	var current struct {
		State string
	}
	result := service.db.WithContext(ctx).Model(&model.BackupAssetArchiveMemberRequest{}).
		Select("state").Where("id = ?", requestID).Limit(1).Scan(&current)
	return result.Error == nil && result.RowsAffected == 1 && current.State == string(ArchiveMemberFailed)
}

func (service *ArchiveMemberService) archiveMemberTerminalCleanupPending(ctx context.Context, requestID string) (bool, error) {
	requestTable := model.BackupAssetArchiveMemberRequest{}.TableName()
	var count int64
	result := service.archiveMemberMaintenanceQuery(ctx).
		Where(requestTable+".id = ?", requestID).
		Count(&count)
	if result.Error != nil {
		return false, fmt.Errorf("load terminal archive member cleanup state: %w", result.Error)
	}
	return count > 0, nil
}

func archiveMemberMaintenanceInvalidationReason(err error) (string, bool) {
	switch {
	case errors.Is(err, backupasset.ErrForbidden):
		return "policy_changed", true
	case errors.Is(err, backupasset.ErrNotFound):
		return "source_changed", true
	default:
		return "", false
	}
}

func archiveMemberMaintenanceRetryable(err error) bool {
	if errors.Is(err, errArchiveMemberTerminalCleanupPending) {
		return true
	}
	if _, invalidates := archiveMemberMaintenanceInvalidationReason(err); invalidates {
		return false
	}
	return errors.Is(err, ErrArchiveMemberUnavailable) || errors.Is(err, ErrNotDeployed) ||
		errors.Is(err, ErrQueueFull) || errors.Is(err, ErrRevisionConflict) || errors.Is(err, gorm.ErrRecordNotFound)
}

// reconcileTerminalArchiveMemberCleanup uses downstream durable state to retry
// revocations that did not finish before terminalization or expiry.
func (service *ArchiveMemberService) reconcileTerminalArchiveMemberCleanup(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
) error {
	if (row.State != string(ArchiveMemberFailed) || row.ErrorCategory != string(ArchiveFailureUnavailable)) &&
		row.State != string(ArchiveMemberCanceled) && row.State != string(ArchiveMemberExpired) {
		return nil
	}
	interestPending, interestErr := service.archiveMemberTerminalInterestPending(ctx, row)
	if interestErr != nil {
		return archiveMemberTerminalCleanupError(interestErr)
	}
	artifactSetID, outputPending, outputErr := service.archiveMemberTerminalOutputPending(ctx, row)
	if outputErr != nil {
		return archiveMemberTerminalCleanupError(outputErr)
	}
	deliveryPending, deliveryErr := service.archiveMemberTerminalDeliveryPending(ctx, row.ID)
	if deliveryErr != nil {
		return archiveMemberTerminalCleanupError(deliveryErr)
	}
	if !interestPending && !outputPending && !deliveryPending {
		return nil
	}
	reason, derivedReason := "member_canceled", DerivedRevokeExplicit
	if row.State == string(ArchiveMemberExpired) {
		reason, derivedReason = "member_expired", DerivedRevokeExpired
	} else if row.State != string(ArchiveMemberCanceled) {
		reason, derivedReason = service.archiveMemberTerminalCleanupReason(ctx, row)
	}
	var cleanupErr error
	if interestPending {
		cleanupErr = errors.Join(cleanupErr, service.removeTerminalArchiveMemberInterest(ctx, row, ArchiveMemberState(row.State)))
	}
	if outputPending {
		cleanupErr = errors.Join(cleanupErr, service.revokeReadyArchiveMember(ctx, row, artifactSetID, reason, derivedReason))
	} else if service.revokeDeliveries == nil {
		if deliveryPending {
			cleanupErr = errors.Join(cleanupErr, ErrArchiveMemberUnavailable)
		}
	} else {
		if deliveryPending {
			deliveryCleanupErr := service.revokeDeliveries(ctx, row.ID, reason)
			if deliveryCleanupErr != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("revoke archive member deliveries: %w", deliveryCleanupErr))
			}
		}
	}
	if cleanupErr != nil {
		return archiveMemberTerminalCleanupError(cleanupErr)
	}
	return nil
}

func archiveMemberTerminalCleanupError(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(errArchiveMemberTerminalCleanupPending, err)
}

func (service *ArchiveMemberService) archiveMemberTerminalOutputPending(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
) (string, bool, error) {
	if row.ProcessingJobID == nil {
		return "", false, nil
	}
	var job struct {
		CurrentArtifactSetID *string
	}
	jobResult := service.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Select("current_artifact_set_id").Where("id = ?", *row.ProcessingJobID).Limit(1).Find(&job)
	if jobResult.Error != nil {
		return "", false, fmt.Errorf("load terminal archive member Processing job: %w", jobResult.Error)
	}
	if jobResult.RowsAffected != 1 || job.CurrentArtifactSetID == nil {
		return "", false, nil
	}
	artifactSetID := *job.CurrentArtifactSetID
	if backupasset.ValidateOpaqueID(artifactSetID) != nil {
		return "", false, ErrArchiveMemberUnavailable
	}
	artifactSetTable := model.BackupAssetDerivedArtifactSet{}.TableName()
	artifactTable := model.BackupAssetDerivedArtifact{}.TableName()
	blobTable := model.BackupAssetDerivedBlob{}.TableName()
	var pendingSet struct{ ID string }
	result := service.db.WithContext(ctx).Model(&model.BackupAssetDerivedArtifactSet{}).
		Select("id").Where(fmt.Sprintf(`id = ? AND job_id = ? AND (
			state IN ? OR EXISTS (
				SELECT 1 FROM %s AS artifacts
				INNER JOIN %s AS blobs ON blobs.id = artifacts.blob_id
				WHERE artifacts.artifact_set_id = %s.id AND blobs.state = ?
			)
		)`, artifactTable, blobTable, artifactSetTable), artifactSetID, *row.ProcessingJobID,
		[]string{"active", "purge_failed"}, "purge_failed").
		Limit(1).Find(&pendingSet)
	if result.Error != nil {
		return "", false, fmt.Errorf("load terminal archive member Derived output: %w", result.Error)
	}
	return artifactSetID, result.RowsAffected == 1, nil
}

func (service *ArchiveMemberService) archiveMemberTerminalInterestPending(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
) (bool, error) {
	if service == nil || service.db == nil || row.ProcessingInterestID == nil || row.ProcessingJobID == nil {
		return false, nil
	}
	var count int64
	result := service.db.WithContext(ctx).Model(&model.BackupAssetProcessingInterest{}).
		Where("id = ? AND job_id = ? AND owner_kind = ? AND owner_key = ? AND active = ?", *row.ProcessingInterestID,
			*row.ProcessingJobID, InterestSystem, archiveMemberInterestOwnerKey(row.ID), true).Count(&count)
	if result.Error != nil {
		return false, fmt.Errorf("load terminal archive member Processing interest: %w", result.Error)
	}
	return count > 0, nil
}

func archiveMemberTerminalInterestRemovalReason(state ArchiveMemberState) (InterestRemovedReason, bool) {
	switch state {
	case ArchiveMemberFailed:
		return InterestRemovedSuperseded, true
	case ArchiveMemberExpired:
		return InterestRemovedExpired, true
	case ArchiveMemberCanceled:
		return InterestRemovedCanceled, true
	default:
		return "", false
	}
}

func (service *ArchiveMemberService) removeTerminalArchiveMemberInterest(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
	state ArchiveMemberState,
) error {
	reason, ok := archiveMemberTerminalInterestRemovalReason(state)
	if !ok || service == nil || service.coordinator == nil || row.ProcessingJobID == nil || row.ProcessingInterestID == nil {
		return ErrArchiveMemberUnavailable
	}
	if err := service.coordinator.RemoveInterest(
		ctx, *row.ProcessingJobID, InterestSystem, archiveMemberInterestOwnerKey(row.ID), reason,
	); err != nil && !service.archiveMemberInterestWasRemoved(ctx, row, reason) {
		return fmt.Errorf("remove terminal archive member Processing interest: %w", err)
	}
	return nil
}

func (service *ArchiveMemberService) archiveMemberTerminalDeliveryPending(ctx context.Context, requestID string) (bool, error) {
	var count int64
	result := service.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where(`resource_kind = ? AND member_request_id = ? AND (
			state IN ? OR (state = ? AND audit_state IN ?)
		)`, "archive_member", requestID, []string{"issued", "active", "draining"}, "revoked",
			[]string{"pending", "retry_wait", "failed"}).Count(&count)
	if result.Error != nil {
		return false, fmt.Errorf("load terminal archive member deliveries: %w", result.Error)
	}
	return count > 0, nil
}

func (service *ArchiveMemberService) archiveMemberTerminalCleanupReason(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
) (string, DerivedRevokeReason) {
	if service.resolveRuntimeAsset != nil {
		asset, err := service.resolveRuntimeAsset(ctx, row)
		ref := backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID}
		if errors.Is(err, backupasset.ErrNotFound) ||
			(err == nil && (!validArchiveMemberAsset(asset, ref) || row.CatalogGenerationID != asset.CatalogGenerationID ||
				row.SourceFingerprint != asset.SourceFingerprint || row.EntryFingerprint != asset.EntryFingerprint)) {
			return "source_changed", DerivedRevokeSourceChanged
		}
	}
	if service.resolveAuthority != nil {
		_, err := service.resolveAuthority(ctx, row)
		if errors.Is(err, backupasset.ErrNotFound) {
			return "source_changed", DerivedRevokeSourceChanged
		}
	}
	return "policy_changed", DerivedRevokePolicyChanged
}

func (service *ArchiveMemberService) expireArchiveMember(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
	now time.Time,
) (model.BackupAssetArchiveMemberRequest, error) {
	state := ArchiveMemberState(row.State)
	if state != ArchiveMemberQueued && state != ArchiveMemberRunning && state != ArchiveMemberReady {
		return row, nil
	}
	var revokeErr error
	if state == ArchiveMemberReady {
		artifactSetID, artifactErr := service.readyArchiveMemberArtifactSet(ctx, row)
		revokeErr = errors.Join(revokeErr, artifactErr, service.revokeReadyArchiveMember(
			ctx, row, artifactSetID, "member_expired", DerivedRevokeExpired,
		))
	}
	updated := service.db.WithContext(ctx).Model(&model.BackupAssetArchiveMemberRequest{}).
		Where("id = ? AND state = ? AND version = ?", row.ID, row.State, row.Version).
		Updates(map[string]any{
			"state": string(ArchiveMemberExpired), "error_category": "",
			"finished_at": now, "updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if updated.Error != nil {
		return row, errors.Join(revokeErr, fmt.Errorf("persist archive member expiry: %w", updated.Error))
	}
	if updated.RowsAffected != 1 {
		return row, errors.Join(revokeErr, ErrRevisionConflict)
	}
	row.State = string(ArchiveMemberExpired)
	row.ErrorCategory = ""
	row.FinishedAt = &now
	row.Version++
	if state == ArchiveMemberRunning {
		revokeErr = errors.Join(revokeErr, service.removeTerminalArchiveMemberInterest(ctx, row, ArchiveMemberExpired))
	}
	return row, archiveMemberTerminalCleanupError(revokeErr)
}

func (service *ArchiveMemberService) reconcileActiveArchiveMember(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
	asset content.AuthorizedAsset,
) (model.BackupAssetArchiveMemberRequest, error) {
	ref := backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID}
	if !validArchiveMemberAsset(asset, ref) || row.CatalogGenerationID != asset.CatalogGenerationID ||
		row.SourceFingerprint != asset.SourceFingerprint || row.EntryFingerprint != asset.EntryFingerprint {
		return row, ErrArchiveMemberUnavailable
	}
	now := service.utcNow()
	if !now.Before(row.AbsoluteExpiresAt.UTC()) &&
		(row.State == string(ArchiveMemberQueued) || row.State == string(ArchiveMemberRunning) || row.State == string(ArchiveMemberReady)) {
		return service.expireArchiveMember(ctx, row, now)
	}
	if ArchiveMemberState(row.State) != ArchiveMemberReady && terminalArchiveMemberState(ArchiveMemberState(row.State)) {
		return row, nil
	}
	if row.ProcessingJobID == nil {
		if row.State == string(ArchiveMemberQueued) {
			return row, nil
		}
		return row, ErrArchiveMemberUnavailable
	}
	var job model.BackupAssetProcessingJob
	jobResult := service.db.WithContext(ctx).Where("id = ?", *row.ProcessingJobID).Limit(1).Find(&job)
	if jobResult.Error != nil {
		return row, fmt.Errorf("load archive member Processing status: %w", jobResult.Error)
	}
	if jobResult.RowsAffected != 1 || !validArchiveMemberProcessingJob(row, job) {
		return row, ErrArchiveMemberUnavailable
	}
	if err := service.validateArchiveMemberProcessingAuthority(ctx, row, job); err != nil {
		return row, err
	}
	if ArchiveMemberState(row.State) == ArchiveMemberReady {
		return row, nil
	}
	return service.projectArchiveMemberProcessingState(ctx, row, job, asset)
}

func validArchiveMemberProcessingJob(
	row model.BackupAssetArchiveMemberRequest,
	job model.BackupAssetProcessingJob,
) bool {
	return row.ProcessingInterestID != nil && row.ProcessingJobID != nil && job.ID == *row.ProcessingJobID &&
		job.RecoveryPointID == row.RecoveryPointID && job.CatalogGenerationID == row.CatalogGenerationID &&
		job.EntryID == row.EntryID && job.SourceFingerprint == row.SourceFingerprint &&
		job.EntryFingerprint == row.EntryFingerprint &&
		job.Capability == capabilityspec.CapabilityArchiveExtractEntry && job.CapabilitySchema == "archive.extract_entry.v1" &&
		job.OutputProfile == capabilityspec.ProfileArchiveMemberV1 &&
		archiveMemberDescriptorOrdinal(job.DescriptorCanonical, row.ResolvedOrdinal)
}

func (service *ArchiveMemberService) validateArchiveMemberProcessingAuthority(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
	job model.BackupAssetProcessingJob,
) error {
	if service.resolveAuthority == nil {
		return nil
	}
	authority, authorityErr := service.resolveAuthority(ctx, row)
	if authorityErr != nil {
		reason, invalidates := archiveMemberMaintenanceInvalidationReason(authorityErr)
		if !invalidates {
			return errors.Join(ErrArchiveMemberUnavailable, authorityErr)
		}
		invalidateErr := service.invalidateArchiveMember(ctx, row, reason)
		return errors.Join(ErrArchiveMemberUnavailable, authorityErr, invalidateErr)
	}
	if authority.ProviderCapabilityRevision == job.ProviderCapabilityRevision &&
		authority.SecurityPolicyRevision == job.SecurityPolicyRevision {
		return nil
	}
	reason := "policy_changed"
	if authority.ProviderCapabilityRevision != job.ProviderCapabilityRevision {
		reason = "source_changed"
	}
	invalidateErr := service.invalidateArchiveMember(ctx, row, reason)
	return errors.Join(ErrArchiveMemberUnavailable, invalidateErr)
}

func (service *ArchiveMemberService) projectArchiveMemberProcessingState(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
	job model.BackupAssetProcessingJob,
	asset content.AuthorizedAsset,
) (model.BackupAssetArchiveMemberRequest, error) {
	switch ProcessingState(job.State) {
	case ProcessingSucceeded:
		if service.resolveOutput == nil || service.revalidateIndex == nil || job.FinishedAt == nil || job.ErrorCode != "" {
			return row, ErrArchiveMemberUnavailable
		}
		index, indexErr := service.revalidateIndex(ctx, row)
		if indexErr != nil || !validArchiveMemberIndex(index, row.IndexRevision, service.utcNow()) {
			return row, errors.Join(ErrArchiveMemberUnavailable, indexErr)
		}
		if !archiveMemberIndexStillBound(row, index) {
			invalidateErr := service.invalidateArchiveMember(ctx, row, "source_changed")
			return row, errors.Join(ErrArchiveMemberUnavailable, invalidateErr)
		}
		output, outputErr := service.resolveOutput(ctx, content.ArchiveMemberArtifactRequest{
			RequestID: row.ID, OwnerUserID: row.OwnerUserID, Asset: asset,
		})
		if outputErr != nil || !validArchiveMemberOutput(row, job, asset, output) {
			return row, errors.Join(ErrArchiveMemberUnavailable, outputErr)
		}
		finished := service.utcNow()
		if err := service.projectArchiveMemberTerminal(
			ctx, row, job, ArchiveMemberReady, ArchiveFailureNone, InterestRemovedCompleted, finished,
		); err != nil {
			return row, fmt.Errorf("project ready archive member: %w", err)
		}
		row.State = string(ArchiveMemberReady)
		row.ErrorCategory = ""
		row.FinishedAt = &finished
		row.Version++
		return row, nil
	case ProcessingCanceled:
		if job.CancelReason != string(CancelReasonInterestWithdrawn) ||
			!service.archiveMemberInterestWasRemoved(ctx, row, InterestRemovedCanceled) {
			return row, ErrArchiveMemberUnavailable
		}
		finished := service.utcNow()
		updated := service.db.WithContext(ctx).Model(&model.BackupAssetArchiveMemberRequest{}).
			Where("id = ? AND state = ? AND version = ?", row.ID, row.State, row.Version).
			Updates(map[string]any{
				"state": string(ArchiveMemberCanceled), "error_category": "",
				"finished_at": finished, "updated_at": finished, "version": gorm.Expr("version + 1"),
			})
		if updated.Error != nil {
			return row, fmt.Errorf("reconcile canceled archive member: %w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return row, ErrRevisionConflict
		}
		row.State = string(ArchiveMemberCanceled)
		row.ErrorCategory = ""
		row.FinishedAt = &finished
		row.Version++
		return row, nil
	case ProcessingFailed:
		product := archiveFailureProduct(ProcessingErrorCode(job.ErrorCode))
		if product == ArchiveFailureNone {
			return row, ErrArchiveMemberUnavailable
		}
		finished := service.utcNow()
		if err := service.projectArchiveMemberTerminal(
			ctx, row, job, ArchiveMemberFailed, product, InterestRemovedCompleted, finished,
		); err != nil {
			return row, fmt.Errorf("project archive member failure: %w", err)
		}
		row.State = string(ArchiveMemberFailed)
		row.ErrorCategory = string(product)
		row.FinishedAt = &finished
		row.Version++
		return row, nil
	case ProcessingQueued, ProcessingLeased, ProcessingFetching, ProcessingMaterializing,
		ProcessingProcessing, ProcessingUploading, ProcessingValidating, ProcessingRetryWait:
		return row, nil
	default:
		return row, ErrArchiveMemberUnavailable
	}
}

func (service *ArchiveMemberService) Poll(
	ctx context.Context,
	lookup ArchiveMemberLookup,
) (ArchiveMemberStatusResult, error) {
	if service == nil || service.db == nil || service.authorize == nil || lookup.Actor.UserID == 0 ||
		backupasset.ValidateAssetRef(lookup.Ref) != nil || backupasset.ValidateOpaqueID(lookup.RequestID) != nil ||
		!lowerHex(lookup.IndexRevision, 64) {
		return ArchiveMemberStatusResult{}, ErrArchiveMemberUnavailable
	}
	ctx = nonNilProcessingContext(ctx)
	var row model.BackupAssetArchiveMemberRequest
	result := service.db.WithContext(ctx).
		Where("id = ? AND owner_user_id = ?", lookup.RequestID, lookup.Actor.UserID).
		Limit(1).Find(&row)
	if result.Error != nil {
		return ArchiveMemberStatusResult{}, fmt.Errorf("load archive member status: %w", result.Error)
	}
	if result.RowsAffected != 1 || row.RecoveryPointID != lookup.Ref.RecoveryPointID || row.EntryID != lookup.Ref.EntryID ||
		row.IndexRevision != lookup.IndexRevision {
		return ArchiveMemberStatusResult{}, ErrArchiveMemberUnavailable
	}
	asset, err := service.authorize.Authorize(ctx, lookup.Actor, lookup.Ref, content.DeliveryPreview)
	if err != nil {
		if reason, invalidates := archiveMemberMaintenanceInvalidationReason(err); invalidates {
			_ = service.invalidateArchiveMember(ctx, row, reason)
		}
		return ArchiveMemberStatusResult{}, err
	}
	if !validArchiveMemberAsset(asset, lookup.Ref) || row.CatalogGenerationID != asset.CatalogGenerationID ||
		row.SourceFingerprint != asset.SourceFingerprint || row.EntryFingerprint != asset.EntryFingerprint {
		_ = service.invalidateArchiveMember(ctx, row, "source_changed")
		return ArchiveMemberStatusResult{}, ErrArchiveMemberUnavailable
	}
	reconciled, reconcileErr := service.reconcileActiveArchiveMember(ctx, row, asset)
	if reconcileErr != nil {
		return ArchiveMemberStatusResult{}, reconcileErr
	}
	status := archiveMemberStatus(reconciled)
	if status.Terminal {
		status.Fallback = service.archiveMemberFallback(ctx, lookup.Actor, lookup.Ref, asset, status.FailureProduct)
	}
	return status, nil
}

func (service *ArchiveMemberService) Invalidate(
	ctx context.Context,
	requestID string,
	reason DerivedRevokeReason,
) error {
	closedReason, ok := archiveMemberInvalidationReason(reason)
	if service == nil || service.db == nil || !ok || backupasset.ValidateOpaqueID(requestID) != nil {
		return ErrArchiveMemberUnavailable
	}
	ctx = nonNilProcessingContext(ctx)
	var row model.BackupAssetArchiveMemberRequest
	result := service.db.WithContext(ctx).Where("id = ?", requestID).Limit(1).Find(&row)
	if result.Error != nil {
		return fmt.Errorf("load archive member invalidation: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrArchiveMemberUnavailable
	}
	return service.invalidateArchiveMember(ctx, row, closedReason)
}

func (service *ArchiveMemberService) invalidateArchiveMember(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
	reason string,
) error {
	if service == nil || service.db == nil ||
		(reason != "source_changed" && reason != "policy_changed" && reason != "key_loss") {
		return ErrArchiveMemberUnavailable
	}
	state := ArchiveMemberState(row.State)
	if state == ArchiveMemberFailed {
		return nil
	}
	var revokeErr error
	if state == ArchiveMemberReady {
		derivedReason, ok := archiveMemberDerivedRevokeReason(reason)
		if !ok {
			return ErrArchiveMemberUnavailable
		}
		artifactSetID, artifactErr := service.readyArchiveMemberArtifactSet(ctx, row)
		revokeErr = errors.Join(revokeErr, artifactErr, service.revokeReadyArchiveMember(ctx, row, artifactSetID, reason, derivedReason))
	} else if terminalArchiveMemberState(state) {
		return ErrArchiveMemberUnavailable
	} else if state != ArchiveMemberQueued && state != ArchiveMemberRunning {
		return ErrArchiveMemberUnavailable
	}
	finished := service.utcNow()
	updated := service.db.WithContext(ctx).Model(&model.BackupAssetArchiveMemberRequest{}).
		Where("id = ? AND state = ? AND version = ?", row.ID, row.State, row.Version).
		Updates(map[string]any{
			"state": string(ArchiveMemberFailed), "error_category": string(ArchiveFailureUnavailable),
			"finished_at": finished, "updated_at": finished, "version": gorm.Expr("version + 1"),
		})
	if updated.Error != nil {
		return errors.Join(revokeErr, fmt.Errorf("persist invalid archive member: %w", updated.Error))
	}
	if updated.RowsAffected != 1 {
		return errors.Join(revokeErr, ErrRevisionConflict)
	}
	if state == ArchiveMemberRunning {
		revokeErr = errors.Join(revokeErr, service.removeTerminalArchiveMemberInterest(ctx, row, ArchiveMemberFailed))
	}
	return archiveMemberTerminalCleanupError(revokeErr)
}

func (service *ArchiveMemberService) readyArchiveMemberArtifactSet(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
) (string, error) {
	if service == nil || service.db == nil || row.ProcessingJobID == nil {
		return "", ErrArchiveMemberUnavailable
	}
	var job model.BackupAssetProcessingJob
	result := service.db.WithContext(ctx).Where("id = ?", *row.ProcessingJobID).Limit(1).Find(&job)
	if result.Error != nil {
		return "", fmt.Errorf("load ready archive member Processing job: %w", result.Error)
	}
	if result.RowsAffected != 1 || job.ID != *row.ProcessingJobID || job.CurrentArtifactSetID == nil ||
		backupasset.ValidateOpaqueID(*job.CurrentArtifactSetID) != nil || job.RecoveryPointID != row.RecoveryPointID ||
		job.CatalogGenerationID != row.CatalogGenerationID || job.EntryID != row.EntryID ||
		job.SourceFingerprint != row.SourceFingerprint || job.EntryFingerprint != row.EntryFingerprint {
		return "", ErrArchiveMemberUnavailable
	}
	return *job.CurrentArtifactSetID, nil
}

func (service *ArchiveMemberService) revokeReadyArchiveMember(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
	artifactSetID string,
	deliveryReason string,
	outputReason DerivedRevokeReason,
) error {
	if service == nil || backupasset.ValidateOpaqueID(row.ID) != nil {
		return ErrArchiveMemberUnavailable
	}
	var revokeErrors []error
	if service.revokeDeliveries == nil {
		revokeErrors = append(revokeErrors, ErrArchiveMemberUnavailable)
	} else if err := service.revokeDeliveries(ctx, row.ID, deliveryReason); err != nil {
		revokeErrors = append(revokeErrors, fmt.Errorf("revoke archive member deliveries: %w", err))
	}
	if service.revokeOutput == nil || backupasset.ValidateOpaqueID(artifactSetID) != nil {
		revokeErrors = append(revokeErrors, ErrArchiveMemberUnavailable)
	} else if err := service.revokeOutput(ctx, artifactSetID, outputReason); err != nil {
		revokeErrors = append(revokeErrors, fmt.Errorf("revoke archive member Derived output: %w", err))
	}
	return errors.Join(revokeErrors...)
}

func archiveMemberInvalidationReason(reason DerivedRevokeReason) (string, bool) {
	switch reason {
	case DerivedRevokeSourceChanged:
		return "source_changed", true
	case DerivedRevokePolicyChanged:
		return "policy_changed", true
	case DerivedRevokeKeyLoss:
		return "key_loss", true
	default:
		return "", false
	}
}

func archiveMemberDerivedRevokeReason(reason string) (DerivedRevokeReason, bool) {
	switch reason {
	case "source_changed":
		return DerivedRevokeSourceChanged, true
	case "policy_changed":
		return DerivedRevokePolicyChanged, true
	case "key_loss":
		return DerivedRevokeKeyLoss, true
	default:
		return "", false
	}
}

func (service *ArchiveMemberService) archiveMemberFallback(
	ctx context.Context,
	actor content.DeliveryActor,
	ref backupasset.AssetRef,
	expected content.AuthorizedAsset,
	product ArchiveFailureProduct,
) ArchiveFallbackProduct {
	if product != ArchiveFailureEncrypted && product != ArchiveFailureUnsupported && product != ArchiveFailureLimit {
		return ArchiveFallbackProduct{}
	}
	asset, err := service.authorize.Authorize(ctx, actor, ref, content.DeliveryDownload)
	if err != nil || !sameArchiveMemberAsset(asset, expected) {
		return ArchiveFallbackProduct{Reason: ArchiveFallbackOriginalUnavailable}
	}
	return ArchiveFallbackProduct{Action: ArchiveFallbackDownloadOriginal}
}

func sameArchiveMemberAsset(left, right content.AuthorizedAsset) bool {
	return left.Ref == right.Ref && left.CatalogGenerationID == right.CatalogGenerationID &&
		left.RepositoryID == right.RepositoryID && left.Provider == right.Provider &&
		left.ProviderCapabilityRevision == right.ProviderCapabilityRevision &&
		left.SourceFingerprint == right.SourceFingerprint && left.EntryFingerprint == right.EntryFingerprint &&
		left.FingerprintStrength == right.FingerprintStrength && left.Size == right.Size &&
		sameArchiveMemberTime(left.ModifiedAt, right.ModifiedAt) && left.MediaType == right.MediaType &&
		left.Path == right.Path && left.Name == right.Name && left.RangeProven == right.RangeProven &&
		left.SearchClassification == right.SearchClassification &&
		left.SearchClassificationRevision == right.SearchClassificationRevision
}

func sameArchiveMemberTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.UTC().Equal(right.UTC())
}

func (service *ArchiveMemberService) projectArchiveMemberTerminal(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
	job model.BackupAssetProcessingJob,
	state ArchiveMemberState,
	product ArchiveFailureProduct,
	interestReason InterestRemovedReason,
	finished time.Time,
) error {
	if service == nil || service.db == nil || row.ProcessingInterestID == nil || row.ProcessingJobID == nil ||
		!terminalArchiveMemberState(state) || !validInterestRemoval(interestReason) ||
		(state == ArchiveMemberFailed) != (product != ArchiveFailureNone) {
		return ErrArchiveMemberUnavailable
	}
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.BackupAssetArchiveMemberRequest
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND state = ? AND version = ?", row.ID, row.State, row.Version,
		).Limit(1).Find(&current)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || current.ProcessingInterestID == nil || current.ProcessingJobID == nil ||
			*current.ProcessingInterestID != *row.ProcessingInterestID || *current.ProcessingJobID != job.ID {
			return ErrRevisionConflict
		}
		removed := tx.Model(&model.BackupAssetProcessingInterest{}).Where(
			"id = ? AND job_id = ? AND owner_kind = ? AND owner_key = ? AND active = ?",
			*current.ProcessingInterestID, job.ID, InterestSystem, archiveMemberInterestOwnerKey(row.ID), true,
		).Updates(map[string]any{
			"active": false, "removed_reason": string(interestReason),
			"removed_at": finished, "updated_at": finished,
		})
		if removed.Error != nil {
			return removed.Error
		}
		if removed.RowsAffected != 1 {
			return ErrRevisionConflict
		}
		updated := tx.Model(&model.BackupAssetArchiveMemberRequest{}).
			Where("id = ? AND state = ? AND version = ?", row.ID, row.State, row.Version).
			Updates(map[string]any{
				"state": string(state), "error_category": string(product),
				"finished_at": finished, "updated_at": finished, "version": gorm.Expr("version + 1"),
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRevisionConflict
		}
		return nil
	})
}

func (service *ArchiveMemberService) Cancel(ctx context.Context, lookup ArchiveMemberLookup) error {
	if service == nil || service.db == nil || service.authorize == nil || service.coordinator == nil ||
		lookup.Actor.UserID == 0 || backupasset.ValidateAssetRef(lookup.Ref) != nil ||
		backupasset.ValidateOpaqueID(lookup.RequestID) != nil || !lowerHex(lookup.IndexRevision, 64) {
		return ErrArchiveMemberUnavailable
	}
	ctx = nonNilProcessingContext(ctx)
	asset, err := service.authorize.Authorize(ctx, lookup.Actor, lookup.Ref, content.DeliveryPreview)
	if err != nil {
		return err
	}
	if !validArchiveMemberAsset(asset, lookup.Ref) {
		return ErrArchiveMemberUnavailable
	}
	var row model.BackupAssetArchiveMemberRequest
	result := service.db.WithContext(ctx).
		Where("id = ? AND owner_user_id = ?", lookup.RequestID, lookup.Actor.UserID).
		Limit(1).Find(&row)
	if result.Error != nil {
		return fmt.Errorf("load archive member cancellation: %w", result.Error)
	}
	if result.RowsAffected != 1 || row.RecoveryPointID != lookup.Ref.RecoveryPointID ||
		row.EntryID != lookup.Ref.EntryID || row.IndexRevision != lookup.IndexRevision ||
		row.CatalogGenerationID != asset.CatalogGenerationID ||
		row.SourceFingerprint != asset.SourceFingerprint || row.EntryFingerprint != asset.EntryFingerprint {
		return ErrArchiveMemberUnavailable
	}
	if ArchiveMemberState(row.State) == ArchiveMemberCanceled {
		return nil
	}
	var terminalizationErr error
	if row.State == string(ArchiveMemberReady) {
		artifactSetID := ""
		if service.resolveReadyOutput == nil {
			terminalizationErr = errors.Join(terminalizationErr, ErrArchiveMemberUnavailable)
		} else {
			output, outputErr := service.resolveReadyOutput(ctx, content.ArchiveMemberArtifactRequest{
				RequestID: row.ID, OwnerUserID: row.OwnerUserID, Asset: asset,
			})
			if outputErr != nil || !validReadyArchiveMemberOutput(row, asset, output) {
				terminalizationErr = errors.Join(terminalizationErr, ErrArchiveMemberUnavailable, outputErr)
			} else {
				artifactSetID = output.DerivedArtifactSetID
			}
		}
		terminalizationErr = errors.Join(terminalizationErr, service.revokeReadyArchiveMember(
			ctx, row, artifactSetID, "member_canceled", DerivedRevokeExplicit,
		))
	} else if terminalArchiveMemberState(ArchiveMemberState(row.State)) ||
		(row.State != string(ArchiveMemberQueued) && row.State != string(ArchiveMemberRunning)) {
		return ErrArchiveMemberUnavailable
	} else if row.State == string(ArchiveMemberRunning) {
		if row.ProcessingJobID == nil || row.ProcessingInterestID == nil {
			return ErrArchiveMemberUnavailable
		}
		removeErr := service.coordinator.RemoveInterest(
			ctx, *row.ProcessingJobID, InterestSystem, archiveMemberInterestOwnerKey(row.ID), InterestRemovedCanceled,
		)
		if removeErr != nil && !service.archiveMemberInterestWasRemoved(ctx, row, InterestRemovedCanceled) {
			return fmt.Errorf("cancel archive member Processing interest: %w", removeErr)
		}
	}
	finished := service.utcNow()
	updated := service.db.WithContext(ctx).Model(&model.BackupAssetArchiveMemberRequest{}).
		Where("id = ? AND state = ? AND version = ?", row.ID, row.State, row.Version).
		Updates(map[string]any{
			"state": string(ArchiveMemberCanceled), "error_category": "",
			"finished_at": finished, "updated_at": finished, "version": gorm.Expr("version + 1"),
		})
	if updated.Error != nil {
		return errors.Join(terminalizationErr, fmt.Errorf("persist archive member cancellation: %w", updated.Error))
	}
	if updated.RowsAffected != 1 {
		var current model.BackupAssetArchiveMemberRequest
		currentResult := service.db.WithContext(ctx).Where("id = ?", row.ID).Limit(1).Find(&current)
		if currentResult.Error == nil && currentResult.RowsAffected == 1 && current.State == string(ArchiveMemberCanceled) {
			return terminalizationErr
		}
		return errors.Join(terminalizationErr, ErrRevisionConflict)
	}
	return terminalizationErr
}

func (service *ArchiveMemberService) archiveMemberInterestWasRemoved(
	ctx context.Context,
	row model.BackupAssetArchiveMemberRequest,
	reason InterestRemovedReason,
) bool {
	if service == nil || service.db == nil || row.ProcessingInterestID == nil || row.ProcessingJobID == nil {
		return false
	}
	var interest model.BackupAssetProcessingInterest
	result := service.db.WithContext(ctx).Where(
		"id = ? AND job_id = ? AND owner_kind = ? AND owner_key = ?",
		*row.ProcessingInterestID, *row.ProcessingJobID, InterestSystem, archiveMemberInterestOwnerKey(row.ID),
	).Limit(1).Find(&interest)
	return result.Error == nil && result.RowsAffected == 1 && !interest.Active && interest.RemovedReason == string(reason) &&
		interest.RemovedAt != nil
}

func validArchiveMemberOutput(
	request model.BackupAssetArchiveMemberRequest,
	job model.BackupAssetProcessingJob,
	asset content.AuthorizedAsset,
	output content.ResolvedArchiveMemberArtifact,
) bool {
	return output.MemberRequestID == request.ID && output.OwnerUserID == request.OwnerUserID &&
		output.Ref == asset.Ref && output.CatalogGenerationID == request.CatalogGenerationID &&
		output.SourceFingerprint == request.SourceFingerprint && output.EntryFingerprint == request.EntryFingerprint &&
		output.MemberChainDigest == request.MemberChainDigest && output.ProcessingJobID == job.ID &&
		backupasset.ValidateOpaqueID(output.ProcessingAttemptID) == nil &&
		backupasset.ValidateOpaqueID(output.DerivedArtifactSetID) == nil &&
		backupasset.ValidateOpaqueID(output.DerivedArtifactID) == nil &&
		backupasset.ValidateOpaqueID(output.DerivedBlobID) == nil && lowerHex(output.DerivedDigest, 64) &&
		output.DerivedSize >= 0 && output.DerivedSize <= 256<<20 &&
		strings.TrimSpace(output.MediaType) != "" && len(output.MediaType) <= 128 &&
		output.AbsoluteExpiresAt.Location() == time.UTC && output.AbsoluteExpiresAt.Equal(request.AbsoluteExpiresAt.UTC()) &&
		output.Provider == asset.Provider && output.ProviderCapabilityRevision == asset.ProviderCapabilityRevision &&
		output.FingerprintStrength == asset.FingerprintStrength && output.SourceSize == asset.Size &&
		output.SourceMediaType == asset.MediaType && output.SecurityPolicyRevision == job.SecurityPolicyRevision
}

func validReadyArchiveMemberOutput(
	request model.BackupAssetArchiveMemberRequest,
	asset content.AuthorizedAsset,
	output content.ResolvedArchiveMemberArtifact,
) bool {
	return request.ProcessingJobID != nil && output.MemberRequestID == request.ID &&
		output.OwnerUserID == request.OwnerUserID && output.Ref == asset.Ref &&
		output.CatalogGenerationID == request.CatalogGenerationID &&
		output.SourceFingerprint == request.SourceFingerprint && output.EntryFingerprint == request.EntryFingerprint &&
		output.MemberChainDigest == request.MemberChainDigest && output.ProcessingJobID == *request.ProcessingJobID &&
		backupasset.ValidateOpaqueID(output.ProcessingAttemptID) == nil &&
		backupasset.ValidateOpaqueID(output.DerivedArtifactSetID) == nil &&
		backupasset.ValidateOpaqueID(output.DerivedArtifactID) == nil &&
		backupasset.ValidateOpaqueID(output.DerivedBlobID) == nil && lowerHex(output.DerivedDigest, 64) &&
		output.DerivedSize >= 0 && output.DerivedSize <= 256<<20 && strings.TrimSpace(output.MediaType) != "" &&
		len(output.MediaType) <= 128 && output.AbsoluteExpiresAt.Location() == time.UTC &&
		output.AbsoluteExpiresAt.Equal(request.AbsoluteExpiresAt.UTC()) && output.Provider == asset.Provider &&
		output.ProviderCapabilityRevision == asset.ProviderCapabilityRevision &&
		output.FingerprintStrength == asset.FingerprintStrength && output.SourceSize == asset.Size &&
		output.SourceMediaType == asset.MediaType && strings.TrimSpace(output.SecurityPolicyRevision) != "" &&
		len(output.SecurityPolicyRevision) <= 128
}

func archiveFailureProduct(code ProcessingErrorCode) ArchiveFailureProduct {
	switch code {
	case ProcessingErrorEncryptedArchive:
		return ArchiveFailureEncrypted
	case ProcessingErrorUnsupportedFormat:
		return ArchiveFailureUnsupported
	case ProcessingErrorInputTooLarge:
		return ArchiveFailureLimit
	case ProcessingErrorInvalidOutput, ProcessingErrorDigestMismatch,
		ProcessingErrorSandboxViolation, ProcessingErrorNetworkViolation,
		ProcessingErrorProtocolIncompatible:
		return ArchiveFailureUnsafe
	case ProcessingErrorSourceChanged, ProcessingErrorSourceExpired,
		ProcessingErrorMaterializationDisabled, ProcessingErrorWorkerUnavailable,
		ProcessingErrorProviderUnavailable, ProcessingErrorQuotaBusy, ProcessingErrorTimeout,
		ProcessingErrorWorkerCrash, ProcessingErrorLeaseLost:
		return ArchiveFailureUnavailable
	default:
		return ArchiveFailureNone
	}
}

func archiveMemberStatus(row model.BackupAssetArchiveMemberRequest) ArchiveMemberStatusResult {
	state := ArchiveMemberState(row.State)
	return ArchiveMemberStatusResult{
		SchemaVersion: 1, RequestID: row.ID,
		AssetRef:      backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID},
		IndexRevision: row.IndexRevision, State: state,
		FailureProduct: ArchiveFailureProduct(row.ErrorCategory),
		Retryable:      false, Terminal: terminalArchiveMemberState(state),
	}
}

func terminalArchiveMemberState(state ArchiveMemberState) bool {
	return state == ArchiveMemberReady || state == ArchiveMemberFailed || state == ArchiveMemberCanceled || state == ArchiveMemberExpired
}

func archiveMemberInterestOwnerKey(requestID string) string {
	digest := sha256.Sum256([]byte("xirang.backup_asset.archive_member.interest.v1\x00" + requestID))
	return "archive-member:" + hex.EncodeToString(digest[:])
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
	return json.Unmarshal(canonical, &descriptor) == nil && descriptor.SchemaVersion == 1 &&
		descriptor.Parameters.MemberStart == ordinal && descriptor.Parameters.MemberEnd == ordinal
}

func archiveMemberResult(row model.BackupAssetArchiveMemberRequest, replayed bool) ArchiveMemberCreateResult {
	// Create always acknowledges the accepted request. Poll exposes its current lifecycle state.
	return ArchiveMemberCreateResult{
		SchemaVersion: 1, RequestID: row.ID,
		AssetRef:      backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID},
		IndexRevision: row.IndexRevision, State: string(ArchiveMemberQueued), Replayed: replayed,
	}
}

func archiveMemberRequestDigests(
	request ArchiveMemberCreateRequest,
	keyMaxBytes int,
) (keyDigest string, intentDigest string, memberDigest string, memberID string, err error) {
	if len(request.MemberChain) != 1 {
		return "", "", "", "", ErrArchiveNestedUnsupported
	}
	if request.Actor.UserID == 0 || backupasset.ValidateAssetRef(request.Ref) != nil ||
		!lowerHex(request.IndexRevision, 64) ||
		backupasset.ValidateOpaqueID(request.MemberChain[0]) != nil {
		return "", "", "", "", ErrArchiveMemberUnavailable
	}
	keyDigest, err = assetexport.IdempotencyKeyDigestWithMaxBytes(
		assetexport.IdempotencyDomainArchiveMemberCreate, request.Actor.UserID, request.IdempotencyKey, keyMaxBytes,
	)
	if err != nil {
		return "", "", "", "", err
	}
	memberID = request.MemberChain[0]
	memberDigest = content.ArchiveMemberChainDigest(request.Ref, request.IndexRevision, memberID)
	if memberDigest == "" {
		return "", "", "", "", ErrArchiveMemberUnavailable
	}
	canonical, marshalErr := json.Marshal(struct {
		SchemaVersion int                  `json:"schema_version"`
		OwnerUserID   uint                 `json:"owner_user_id"`
		Ref           backupasset.AssetRef `json:"ref"`
		IndexRevision string               `json:"index_revision"`
		MemberDigest  string               `json:"member_digest"`
	}{SchemaVersion: 1, OwnerUserID: request.Actor.UserID, Ref: request.Ref, IndexRevision: request.IndexRevision, MemberDigest: memberDigest})
	if marshalErr != nil {
		return "", "", "", "", ErrArchiveMemberUnavailable
	}
	digest := sha256.New()
	digest.Write([]byte("xirang.backup_asset.archive_member.create_intent.v1\x00"))
	digest.Write(canonical)
	return keyDigest, hex.EncodeToString(digest.Sum(nil)), memberDigest, memberID, nil
}

func archiveMemberExpiredReceiptTombstoneDigest(keyDigest, requestID string) (string, error) {
	if !lowerHex(keyDigest, 64) || backupasset.ValidateOpaqueID(requestID) != nil {
		return "", ErrArchiveMemberUnavailable
	}
	digest := sha256.New()
	digest.Write([]byte("xirang.backup_asset.idempotency.expired_tombstone.v1\x00archive_member_create\x00"))
	digest.Write([]byte(keyDigest))
	digest.Write([]byte{0})
	digest.Write([]byte(requestID))
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validArchiveMemberIdempotencyExpiry(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func validArchiveMemberAsset(asset content.AuthorizedAsset, ref backupasset.AssetRef) bool {
	return asset.Ref == ref && backupasset.ValidateOpaqueID(asset.CatalogGenerationID) == nil &&
		asset.SourceFingerprint != "" && len(asset.SourceFingerprint) <= 128 &&
		asset.EntryFingerprint != "" && len(asset.EntryFingerprint) <= 128 &&
		asset.ProviderCapabilityRevision > 0 && asset.Size >= 0 &&
		(asset.Provider == backupasset.ProviderRestic || asset.Provider == backupasset.ProviderRsync || asset.Provider == backupasset.ProviderRclone)
}

func validArchiveMemberIndex(index ArchiveMemberIndexBinding, revision string, now time.Time) bool {
	if backupasset.ValidateOpaqueID(index.ArtifactID) != nil || index.Revision != revision ||
		!lowerHex(index.Revision, 64) || strings.TrimSpace(index.PipelineFingerprint) == "" ||
		len(index.PipelineFingerprint) > 128 || strings.TrimSpace(index.SecurityPolicyRevision) == "" ||
		len(index.SecurityPolicyRevision) > 128 || index.AbsoluteExpiresAt.Location() != time.UTC ||
		!now.Before(index.AbsoluteExpiresAt) || index.Members == nil || len(index.Members) > 100_000 {
		return false
	}
	seenIDs := make(map[string]struct{}, len(index.Members))
	seenOrdinals := make(map[int]struct{}, len(index.Members))
	for _, member := range index.Members {
		_, warningValid := normalizeArchiveMemberWarning(member.Warning)
		if backupasset.ValidateOpaqueID(member.OpaqueID) != nil || member.Ordinal < 0 || member.Size < 0 ||
			member.Size > 256<<20 || strings.TrimSpace(member.DisplayName) == "" || len(member.DisplayName) > 512 ||
			strings.TrimSpace(member.MediaType) == "" || len(member.MediaType) > 128 || !warningValid {
			return false
		}
		if _, exists := seenIDs[member.OpaqueID]; exists {
			return false
		}
		if _, exists := seenOrdinals[member.Ordinal]; exists {
			return false
		}
		seenIDs[member.OpaqueID] = struct{}{}
		seenOrdinals[member.Ordinal] = struct{}{}
	}
	return true
}

// Child 11 has no per-entry advisory source for fully validated regular files.
// Empty internal projections therefore normalize to the only allowed public value.
func normalizeArchiveMemberWarning(value ArchiveMemberWarning) (ArchiveMemberWarning, bool) {
	switch value {
	case "", ArchiveMemberWarningNone:
		return ArchiveMemberWarningNone, true
	default:
		return "", false
	}
}

func resolveArchiveMemberOrdinal(members []ArchiveMemberIndexEntry, memberID string) (int, bool) {
	for _, member := range members {
		if member.OpaqueID == memberID {
			return member.Ordinal, true
		}
	}
	return 0, false
}

func archiveMemberIndexStillBound(
	request model.BackupAssetArchiveMemberRequest,
	index ArchiveMemberIndexBinding,
) bool {
	if index.ArtifactID != request.IndexArtifactID || index.Revision != request.IndexRevision ||
		!index.AbsoluteExpiresAt.UTC().Equal(request.AbsoluteExpiresAt.UTC()) {
		return false
	}
	matches := 0
	for _, member := range index.Members {
		digest := content.ArchiveMemberChainDigest(
			backupasset.AssetRef{RecoveryPointID: request.RecoveryPointID, EntryID: request.EntryID},
			request.IndexRevision,
			member.OpaqueID,
		)
		if digest != request.MemberChainDigest {
			continue
		}
		if member.Ordinal != request.ResolvedOrdinal {
			return false
		}
		matches++
	}
	return matches == 1
}

func ensureArchiveMemberUseLatchTx(ctx context.Context, tx *gorm.DB, now time.Time) error {
	if tx == nil {
		return ErrArchiveMemberUnavailable
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return fmt.Errorf("create archive member use latch identity: %w", err)
	}
	latch := model.BackupAssetExportQuotaBucket{
		ID: id, Scope: "global", Subject: "global", TransitionRevision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "subject"}},
		DoNothing: true,
	}).Create(&latch).Error; err != nil {
		return fmt.Errorf("ensure archive member use latch: %w", err)
	}
	return nil
}

func retryableArchiveMemberCreateError(err error) bool {
	if err == nil {
		return false
	}
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) {
		return sqliteError.Code == sqlite3.ErrBusy || sqliteError.Code == sqlite3.ErrLocked
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code == "40P01" || postgresError.Code == "40001"
	}
	return false
}

func retryArchiveMemberConflicts(ctx context.Context, operation func() error) error {
	var lastErr error
	for attempt := 0; attempt < archiveMemberConflictAttempts; attempt++ {
		if err := operation(); err != nil {
			lastErr = err
			if !retryableArchiveMemberCreateError(err) {
				return err
			}
			if attempt == archiveMemberConflictAttempts-1 {
				return fmt.Errorf("archive member database conflict after retries: %w", lastErr)
			}
			timer := time.NewTimer(time.Duration(attempt+1) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("archive member database conflict: %w", ctx.Err())
			case <-timer.C:
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("archive member database conflict after retries: %w", lastErr)
}

func (service *ArchiveMemberService) utcNow() time.Time {
	now := service.now()
	if now.Location() != time.UTC {
		now = now.UTC()
	}
	return now
}
