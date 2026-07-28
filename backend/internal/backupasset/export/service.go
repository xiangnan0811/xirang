package export

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	exportCreateEndpoint   = "export_create"
	itemCursorSchemaV1     = 1
	itemCursorMaxTokenSize = 4096
	itemCursorDomain       = "xirang.backup_asset.export.status_cursor.v1"
)

type LeaseAcquirer interface {
	AcquireTx(context.Context, *gorm.DB, backupasset.AcquireLeaseRequest) (backupasset.Lease, error)
}

type ExportKeySource interface {
	Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error)
	ByVersion(context.Context, backupasset.KeyDomain, int) (backupasset.DomainKeyMaterial, error)
	LockActiveTx(context.Context, *gorm.DB, backupasset.DomainKeyMaterial) (backupasset.DomainKeyMaterial, error)
}

type ServiceConfig struct {
	Selection              SelectionLimits
	Quota                  QuotaLimits
	ChunkBytes             int64
	MaxItemBytes           int64
	MaxProviderBytes       int64
	MaxCiphertextBytes     int64
	MaxOpenReaders         int
	MaxDuration            time.Duration
	MaxAttempts            int
	RetryBase              time.Duration
	RetryMaxDelay          time.Duration
	LeaseTTL               time.Duration
	LeaseRenewMargin       time.Duration
	ReadyTTL               time.Duration
	IdempotencyTTL         time.Duration
	IdempotencyKeyMaxBytes int
}

type ServiceDependencies struct {
	DB       *gorm.DB
	Now      func() time.Time
	Leases   LeaseAcquirer
	Keys     ExportKeySource
	Resolver SelectionResolver
	Config   ServiceConfig
}

type Service struct {
	db       *gorm.DB
	now      func() time.Time
	leases   LeaseAcquirer
	keys     ExportKeySource
	resolver SelectionResolver
	config   ServiceConfig
}

type CommitCreateRequest struct {
	Actor          SelectionActor
	Selection      FrozenSelection
	IdempotencyKey string
	ArchiveFormat  ArchiveFormat
	ArchiveProfile string

	requestIntentDigest string
}

type CommitCreateResult struct {
	JobID           string
	SelectionDigest string
	Replay          bool
}

type CreateRequest struct {
	Actor          SelectionActor
	Selection      CreateSelectionV1
	IdempotencyKey string
	ArchiveFormat  ArchiveFormat
	ArchiveProfile string
}

type CreateResult struct {
	Job    JobStatus `json:"job"`
	Replay bool      `json:"replay"`
}

type StatusRequest struct {
	Actor       SelectionActor
	JobID       string
	ItemsCursor string
	ItemsLimit  int
}

type JobItemStatus struct {
	ID            string    `json:"id"`
	Ordinal       int       `json:"ordinal"`
	State         ItemState `json:"state"`
	LogicalBytes  int64     `json:"logical_bytes"`
	ProviderBytes int64     `json:"provider_bytes"`
	ErrorCategory string    `json:"error_category,omitempty"`
}

type AttemptProgress struct {
	AttemptNumber  int       `json:"attempt_number"`
	State          string    `json:"state"`
	ItemCount      int64     `json:"item_count"`
	LogicalBytes   int64     `json:"logical_bytes"`
	ProviderBytes  int64     `json:"provider_bytes"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

type JobStatus struct {
	SchemaVersion      int              `json:"schema_version"`
	ID                 string           `json:"id"`
	SelectionDigest    string           `json:"selection_digest"`
	ArchiveFormat      ArchiveFormat    `json:"archive_format"`
	ArchiveProfile     string           `json:"archive_profile"`
	ExecutionState     ExecutionState   `json:"execution_state"`
	ResultKind         ResultKind       `json:"result_kind,omitempty"`
	CleanupState       CleanupState     `json:"cleanup_state"`
	ItemCount          int64            `json:"item_count"`
	PackedCount        int64            `json:"packed_count"`
	SkippedCount       int64            `json:"skipped_count"`
	FailedCount        int64            `json:"failed_count"`
	LogicalBytes       int64            `json:"logical_bytes"`
	ProviderBytes      int64            `json:"provider_bytes"`
	ArtifactBytes      int64            `json:"artifact_bytes"`
	ErrorCategory      string           `json:"error_category,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	AbsoluteDeadline   time.Time        `json:"absolute_deadline"`
	ReadyAt            *time.Time       `json:"ready_at,omitempty"`
	ExpiresAt          *time.Time       `json:"expires_at,omitempty"`
	Attempt            *AttemptProgress `json:"attempt,omitempty"`
	Items              []JobItemStatus  `json:"items"`
	NextCursor         string           `json:"next_cursor,omitempty"`
	PollAfterSeconds   int              `json:"poll_after_seconds"`
	CanCancel          bool             `json:"can_cancel"`
	CanDownload        bool             `json:"can_download"`
	TransitionRevision int64            `json:"-"`
}

type itemCursorV1 struct {
	SchemaVersion   int    `json:"schema_version"`
	JobID           string `json:"job_id"`
	SelectionDigest string `json:"selection_digest"`
	NextOrdinal     int    `json:"next_ordinal"`
}

func (service *Service) Create(ctx context.Context, request CreateRequest) (CreateResult, error) {
	if service == nil || validateActor(request.Actor) != nil || request.Actor.Role != "admin" ||
		ValidateCreateSelection(request.Selection) != nil ||
		!ValidArchiveProfilePair(request.ArchiveFormat, request.ArchiveProfile) {
		return CreateResult{}, ErrInvalidSelection
	}
	ctx = nonNilServiceContext(ctx)
	keyDigest, err := IdempotencyKeyDigestWithMaxBytes(
		IdempotencyDomainExportCreate, request.Actor.UserID, request.IdempotencyKey, service.config.IdempotencyKeyMaxBytes,
	)
	if err != nil {
		return CreateResult{}, err
	}
	intentDigest, err := createRequestIntentDigest(request)
	if err != nil {
		return CreateResult{}, err
	}
	committed, found, err := service.lookupCreateReplay(ctx, request.Actor.UserID, keyDigest, intentDigest)
	if err != nil {
		return CreateResult{}, err
	}
	if !found {
		var selection FrozenSelection
		switch request.Selection.Kind {
		case SelectionExplicit:
			selection, err = service.resolver.ResolveExplicit(ctx, request.Actor, request.Selection.Refs, service.config.Selection)
		case SelectionSavedSearch:
			selection, err = service.resolver.ResolveSavedSearch(
				ctx, request.Actor, request.Selection.SavedSearchID, int64(request.Selection.SavedSearchVersion), service.config.Selection,
			)
		default:
			err = ErrInvalidSelection
		}
		if err != nil {
			return CreateResult{}, err
		}
		committed, err = service.CommitCreate(ctx, CommitCreateRequest{
			Actor: request.Actor, Selection: selection, IdempotencyKey: request.IdempotencyKey,
			ArchiveFormat: request.ArchiveFormat, ArchiveProfile: request.ArchiveProfile,
			requestIntentDigest: intentDigest,
		})
	}
	if err != nil {
		return CreateResult{}, err
	}
	job, err := service.Status(ctx, StatusRequest{Actor: request.Actor, JobID: committed.JobID})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Job: job, Replay: committed.Replay}, nil
}

func (service *Service) Status(ctx context.Context, request StatusRequest) (JobStatus, error) {
	if service == nil || request.Actor.UserID == 0 || request.Actor.Role != "admin" ||
		backupasset.ValidateOpaqueID(request.JobID) != nil || request.ItemsLimit < 0 || request.ItemsLimit > 200 {
		return JobStatus{}, ErrInvalidSelection
	}
	ctx = nonNilServiceContext(ctx)
	limit := request.ItemsLimit
	if limit == 0 {
		limit = 100
	}
	var job model.BackupAssetExportJob
	result := service.db.WithContext(ctx).Where("id = ? AND owner_user_id = ?", request.JobID, request.Actor.UserID).Limit(1).Find(&job)
	if result.Error != nil {
		return JobStatus{}, fmt.Errorf("load export status: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return JobStatus{}, ErrNotFound
	}
	if err := validateStatusJob(job); err != nil {
		return JobStatus{}, err
	}
	nextOrdinal := 0
	if request.ItemsCursor != "" {
		cursor, err := service.decodeItemCursor(ctx, request.ItemsCursor)
		if err != nil || cursor.JobID != job.ID || cursor.SelectionDigest != job.SelectionDigest {
			return JobStatus{}, ErrInvalidSelection
		}
		nextOrdinal = cursor.NextOrdinal
	}
	var rows []model.BackupAssetExportItem
	if err := service.db.WithContext(ctx).Where("job_id = ? AND ordinal >= ?", job.ID, nextOrdinal).
		Order("ordinal ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return JobStatus{}, fmt.Errorf("load export status items: %w", err)
	}
	hasNext := len(rows) > limit
	if hasNext {
		rows = rows[:limit]
	}
	items := make([]JobItemStatus, 0, len(rows))
	for _, row := range rows {
		state := ItemState(row.State)
		if !validItemStates[state] || row.Ordinal < nextOrdinal || row.LogicalBytes < 0 || row.ProviderBytes < 0 ||
			!validPersistedStatusCategory(row.ErrorCategory) {
			return JobStatus{}, ErrUnavailable
		}
		items = append(items, JobItemStatus{
			ID: row.ID, Ordinal: row.Ordinal, State: state, LogicalBytes: row.LogicalBytes,
			ProviderBytes: row.ProviderBytes, ErrorCategory: publicStatusCategory(row.ErrorCategory),
		})
	}
	status := jobStatusFromModel(job, items, service.now().UTC())
	if hasNext {
		cursor, err := service.encodeItemCursor(ctx, itemCursorV1{
			SchemaVersion: 1, JobID: job.ID, SelectionDigest: job.SelectionDigest, NextOrdinal: rows[len(rows)-1].Ordinal + 1,
		})
		if err != nil {
			return JobStatus{}, err
		}
		status.NextCursor = cursor
	}
	if job.CurrentAttemptID != nil {
		var attempt model.BackupAssetExportAttempt
		attemptResult := service.db.WithContext(ctx).Where("id = ? AND job_id = ?", *job.CurrentAttemptID, job.ID).Limit(1).Find(&attempt)
		if attemptResult.Error != nil || attemptResult.RowsAffected != 1 || attempt.AttemptNumber <= 0 ||
			attempt.CheckpointItemCount < 0 || attempt.CheckpointLogicalBytes < 0 || attempt.CheckpointProviderBytes < 0 {
			return JobStatus{}, ErrUnavailable
		}
		status.Attempt = &AttemptProgress{
			AttemptNumber: attempt.AttemptNumber, State: attempt.State, ItemCount: attempt.CheckpointItemCount,
			LogicalBytes: attempt.CheckpointLogicalBytes, ProviderBytes: attempt.CheckpointProviderBytes,
			LeaseExpiresAt: attempt.LeaseExpiresAt.UTC(),
		}
	}
	return status, nil
}

func (service *Service) Cancel(ctx context.Context, actor SelectionActor, jobID string) (JobStatus, error) {
	if service == nil || actor.UserID == 0 || actor.Role != "admin" || backupasset.ValidateOpaqueID(jobID) != nil {
		return JobStatus{}, ErrNotFound
	}
	ctx = nonNilServiceContext(ctx)
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND owner_user_id = ?", jobID, actor.UserID).Limit(1).Find(&job)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrNotFound
		}
		state := ExecutionState(job.ExecutionState)
		if !validExecutionStates[state] {
			return ErrUnavailable
		}
		if state == ExecutionCancelRequested || state == ExecutionCanceled || state == ExecutionFailed ||
			state == ExecutionSourceExpired || state == ExecutionExpiring || state == ExecutionExpired {
			return nil
		}
		if ValidateExecutionTransition(state, ExecutionCancelRequested) != nil {
			return ErrInvalidTransition
		}
		now := service.now().UTC()
		result = tx.Model(&model.BackupAssetExportJob{}).
			Where("id = ? AND owner_user_id = ? AND execution_state = ? AND transition_revision = ?", job.ID, actor.UserID, state, job.TransitionRevision).
			Updates(map[string]any{
				"execution_state": string(ExecutionCancelRequested), "transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("request export cancellation: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrInvalidTransition
		}
		return nil
	})
	if err != nil {
		return JobStatus{}, err
	}
	return service.Status(ctx, StatusRequest{Actor: actor, JobID: jobID})
}

func NewService(dependencies ServiceDependencies) (*Service, error) {
	if dependencies.DB == nil || dependencies.Leases == nil || dependencies.Keys == nil || dependencies.Resolver == nil ||
		!validServiceConfig(dependencies.Config) {
		return nil, ErrUnavailable
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		db: dependencies.DB, now: dependencies.Now, leases: dependencies.Leases,
		keys: dependencies.Keys, resolver: dependencies.Resolver, config: dependencies.Config,
	}, nil
}

func (service *Service) CommitCreate(ctx context.Context, request CommitCreateRequest) (CommitCreateResult, error) {
	if service == nil || request.Actor.UserID == 0 || request.Actor.Role != "admin" ||
		!ValidArchiveProfilePair(request.ArchiveFormat, request.ArchiveProfile) {
		return CommitCreateResult{}, ErrInvalidSelection
	}
	frozen, err := FreezeSelection(request.Selection.Items, request.Selection.SavedSearch, service.config.Selection)
	if err != nil || frozen.Digest != request.Selection.Digest {
		return CommitCreateResult{}, ErrInvalidSelection
	}
	minimumCiphertextBytes, err := minimumArchiveCiphertextBytesV1(
		service.config.Selection.MaxLogicalBytes, service.config.Selection.MaxItems, service.config.ChunkBytes,
	)
	if err != nil || service.config.MaxCiphertextBytes < minimumCiphertextBytes {
		return CommitCreateResult{}, ErrUnavailable
	}
	keyDigest, err := IdempotencyKeyDigestWithMaxBytes(
		IdempotencyDomainExportCreate, request.Actor.UserID, request.IdempotencyKey, service.config.IdempotencyKeyMaxBytes,
	)
	if err != nil {
		return CommitCreateResult{}, err
	}
	intentDigest := request.requestIntentDigest
	if intentDigest == "" {
		intentDigest, err = CreateIntentDigest(CreateIntentV1{
			SchemaVersion: 1, OwnerUserID: request.Actor.UserID, SelectionDigest: frozen.Digest,
			ArchiveFormat: string(request.ArchiveFormat), ArchiveProfile: request.ArchiveProfile,
			ChunkBytes: service.config.ChunkBytes,
		})
		if err != nil {
			return CommitCreateResult{}, err
		}
	} else if !lowerHex(intentDigest, 64) {
		return CommitCreateResult{}, ErrInvalidIdempotency
	}
	if replay, found, err := service.lookupCreateReplay(ctx, request.Actor.UserID, keyDigest, intentDigest); err != nil || found {
		return replay, err
	}
	peakStoreBytes, err := createPeakStoreBytes(frozen.Items, service.config)
	if err != nil {
		return CommitCreateResult{}, err
	}

	jobID, err := backupasset.NewOpaqueID()
	if err != nil {
		return CommitCreateResult{}, err
	}
	keyID, err := backupasset.NewOpaqueID()
	if err != nil {
		return CommitCreateResult{}, err
	}
	dek := make([]byte, 32)
	defer clear(dek)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return CommitCreateResult{}, err
	}
	exportKey, err := service.keys.Active(ctx, backupasset.KeyDomainExportStore)
	defer clear(exportKey.Key)
	if err != nil || exportKey.Domain != backupasset.KeyDomainExportStore ||
		exportKey.State != backupasset.DomainKeyActive || exportKey.Version <= 0 || len(exportKey.Key) != 32 {
		return CommitCreateResult{}, ErrUnavailable
	}
	jobKeyBinding := JobKeyBinding{
		ExportID: jobID, SelectionDigest: frozen.Digest, KEKVersion: exportKey.Version,
		WrapAlgorithm: JobKeyWrapAlgorithmV1,
	}
	envelope, err := WrapJobDEK(jobKeyBinding, exportKey.Key, dek)
	if err != nil {
		return CommitCreateResult{}, err
	}

	var result CommitCreateResult
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if replay, found, err := service.lookupCreateReplayTx(ctx, tx, request.Actor.UserID, keyDigest, intentDigest, true); err != nil {
			return err
		} else if found {
			result = replay
			return nil
		}
		verifiedExportKey, err := service.keys.LockActiveTx(ctx, tx, exportKey)
		defer clear(verifiedExportKey.Key)
		if err != nil {
			return ErrUnavailable
		}
		if err := service.resolver.RevalidateFrozenTx(ctx, tx, request.Actor, frozen); err != nil {
			return err
		}

		now := service.now().UTC()
		sourceItems := groupSelectionSources(frozen.Items)
		leases := make([]backupasset.Lease, 0, len(sourceItems))
		deadlines := make([]SourceDeadline, 0, len(sourceItems))
		for _, source := range sourceItems {
			lease, err := service.leases.AcquireTx(ctx, tx, backupasset.AcquireLeaseRequest{
				RecoveryPointID:  source.recoveryPointID,
				HolderType:       backupasset.LeaseHolderExportJob,
				OwnerID:          jobID,
				AbsoluteDeadline: time.Time{},
			})
			if err != nil {
				return err
			}
			leases = append(leases, lease)
			deadlines = append(deadlines, SourceDeadline{AbsoluteDeadline: lease.AbsoluteDeadline, RetentionUntil: source.retentionUntil})
		}
		absoluteDeadline, err := ComputeExecutionDeadline(
			now, service.config.MaxDuration, service.config.LeaseTTL+service.config.LeaseRenewMargin, deadlines,
		)
		if err != nil {
			return err
		}
		buckets, err := ensureAndLockQuotaBucketPairTx(tx, request.Actor.UserID, now)
		if err != nil {
			return err
		}
		lifecycleSequence, err := allocateLifecycleEnqueueSequenceTx(tx, buckets.Global)
		if err != nil {
			return err
		}
		if err := reserveQuotaBucket(tx, buckets.Global.ID, service.config.Quota.GlobalActiveJobs,
			service.config.Quota.GlobalStoreBytes, peakStoreBytes, now); err != nil {
			return err
		}
		if err := reserveQuotaBucket(tx, buckets.User.ID, service.config.Quota.UserActiveJobs,
			service.config.Quota.UserStoreBytes, peakStoreBytes, now); err != nil {
			return err
		}

		job := model.BackupAssetExportJob{
			ID: jobID, OwnerUserID: request.Actor.UserID, LifecycleEnqueueSequence: lifecycleSequence,
			SelectionDigest: frozen.Digest, SelectionSchemaVersion: 1,
			ArchiveFormat: string(request.ArchiveFormat), ArchiveProfile: request.ArchiveProfile,
			LimitsSchemaVersion: 1, ChunkBytes: service.config.ChunkBytes,
			MaxItems: service.config.Selection.MaxItems, MaxSourcePoints: service.config.Selection.MaxSourcePoints,
			MaxItemBytes: service.config.MaxItemBytes, MaxLogicalBytes: service.config.Selection.MaxLogicalBytes,
			MaxProviderBytes: service.config.MaxProviderBytes, MaxCiphertextBytes: service.config.MaxCiphertextBytes,
			MaxOpenReaders: service.config.MaxOpenReaders, MaxDurationSeconds: int64(service.config.MaxDuration / time.Second),
			MaxAttempts: service.config.MaxAttempts, RetryBaseSeconds: int64(service.config.RetryBase / time.Second),
			RetryMaxDelaySeconds:    int64(service.config.RetryMaxDelay / time.Second),
			LeaseTTLSeconds:         int64(service.config.LeaseTTL / time.Second),
			LeaseRenewMarginSeconds: int64(service.config.LeaseRenewMargin / time.Second),
			ReadyTTLSeconds:         int64(service.config.ReadyTTL / time.Second),
			ExecutionState:          string(ExecutionQueued), ResultKind: "", CleanupState: string(CleanupNone),
			AbsoluteDeadline: absoluteDeadline, ItemCount: int64(len(frozen.Items)), TransitionRevision: 1,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&job).Error; err != nil {
			return fmt.Errorf("create export job: %w", err)
		}
		keyRow := model.BackupAssetExportKey{
			ID: keyID, JobID: jobID, State: "active", WrappedDEK: envelope.Ciphertext,
			EnvelopeNonce: envelope.Nonce, KEKVersion: exportKey.Version, WrapAlgorithm: JobKeyWrapAlgorithmV1,
			KeyRevision: 1, CreatedAt: now,
		}
		if err := tx.Create(&keyRow).Error; err != nil {
			return fmt.Errorf("create export job key: %w", err)
		}
		for ordinal, item := range frozen.Items {
			itemID, err := backupasset.NewOpaqueID()
			if err != nil {
				return err
			}
			pathNonce, pathCiphertext, err := encryptSelectionPath(dek, jobID, itemID, frozen.Digest, item.ArchiveComponents)
			if err != nil {
				return err
			}
			row := model.BackupAssetExportItem{
				ID: itemID, JobID: jobID, Ordinal: ordinal, RecoveryPointID: item.Ref.RecoveryPointID,
				EntryID: item.Ref.EntryID, CatalogGenerationID: item.CatalogGenerationID,
				SourceFingerprint: item.SourceFingerprint, EntryFingerprint: item.EntryFingerprint,
				FingerprintStrength: item.FingerprintStrength, ProviderCapabilityRevision: item.ProviderCapabilityRevision,
				EntryType: string(item.EntryType), LogicalSize: item.LogicalSize, MediaType: item.MediaType,
				RetentionUntil: item.RetentionUntil, SelectionRootOrdinal: item.SelectionRootOrdinal,
				PathNonce: pathNonce, PathCiphertext: pathCiphertext, State: string(ItemPending), CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("create export item: %w", err)
			}
		}
		for index, lease := range leases {
			sourceID, err := backupasset.NewOpaqueID()
			if err != nil {
				return err
			}
			fenceHash := sha256.Sum256([]byte(lease.Fence.FenceToken))
			row := model.BackupAssetExportSourceLease{
				ID: sourceID, JobID: jobID, RecoveryPointID: lease.RecoveryPointID, LeaseID: lease.ID,
				LeaseAttemptID: lease.Fence.AttemptID, FenceHash: hex.EncodeToString(fenceHash[:]),
				AbsoluteDeadline: lease.AbsoluteDeadline, RetentionUntil: deadlines[index].RetentionUntil,
				State: "active", AcquiredAt: lease.LastHeartbeatAt, RenewedAt: lease.LastHeartbeatAt,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("create export source lease: %w", err)
			}
		}
		quotaRequest := QuotaJobRequest{
			UserID: request.Actor.UserID, JobID: jobID, StoreBytes: peakStoreBytes, ExpiresAt: absoluteDeadline,
		}
		for _, reservation := range []struct {
			bucketID string
			kind     string
		}{
			{bucketID: buckets.Global.ID, kind: "job"},
			{bucketID: buckets.User.ID, kind: "job"},
			{bucketID: buckets.Global.ID, kind: "store"},
			{bucketID: buckets.User.ID, kind: "store"},
		} {
			if _, err := createQuotaReservation(tx, reservation.bucketID, quotaRequest, reservation.kind, now); err != nil {
				return fmt.Errorf("create %s export reservation: %w", reservation.kind, err)
			}
		}
		receiptID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		receipt := model.BackupAssetExportIdempotency{
			ID: receiptID, OwnerUserID: request.Actor.UserID, Endpoint: exportCreateEndpoint,
			KeyDigest: keyDigest, RequestIntentDigest: intentDigest, State: "committed", ResultJobID: &jobID,
			ExpiresAt: now.Add(service.config.IdempotencyTTL), CreatedAt: now, UpdatedAt: now,
		}
		// Export receipts have no delivery references. A locked expired row is
		// discarded before the fresh job's receipt claims the unique slot.
		expired := tx.WithContext(ctx).
			Where("owner_user_id = ? AND endpoint = ? AND key_digest = ? AND expires_at <= ?",
				request.Actor.UserID, exportCreateEndpoint, keyDigest, now).
			Delete(&model.BackupAssetExportIdempotency{})
		if expired.Error != nil {
			return fmt.Errorf("discard expired export idempotency receipt: %w", expired.Error)
		}
		if err := tx.Create(&receipt).Error; err != nil {
			return fmt.Errorf("create export idempotency receipt: %w", err)
		}
		if err := validatePersistedCreateKeyTx(tx, job, keyRow, verifiedExportKey, envelope, dek); err != nil {
			return err
		}
		result = CommitCreateResult{JobID: jobID, SelectionDigest: frozen.Digest}
		return nil
	})
	if err != nil {
		replay, found, replayErr := service.lookupCreateReplay(ctx, request.Actor.UserID, keyDigest, intentDigest)
		if found {
			return replay, replayErr
		}
		if replayErr != nil {
			if ctx != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return CommitCreateResult{}, ctxErr
				}
			}
			return CommitCreateResult{}, ErrUnavailable
		}
		return CommitCreateResult{}, err
	}
	return result, nil
}

func allocateLifecycleEnqueueSequenceTx(
	tx *gorm.DB,
	globalBucket model.BackupAssetExportQuotaBucket,
) (int64, error) {
	sequence := globalBucket.LifecycleNextSequence
	if tx == nil || globalBucket.Scope != "global" || globalBucket.Subject != "global" ||
		backupasset.ValidateOpaqueID(globalBucket.ID) != nil || sequence <= 0 || sequence == math.MaxInt64 ||
		globalBucket.LifecycleSweepCursor < 0 || globalBucket.LifecycleSweepHighWater < 0 ||
		globalBucket.LifecycleSweepCursor > globalBucket.LifecycleSweepHighWater ||
		globalBucket.LifecycleSweepHighWater >= sequence {
		return 0, ErrUnavailable
	}
	result := tx.Model(&model.BackupAssetExportQuotaBucket{}).
		Where("id = ? AND scope = ? AND subject = ? AND lifecycle_next_sequence = ?",
			globalBucket.ID, "global", "global", sequence).
		UpdateColumn("lifecycle_next_sequence", sequence+1)
	if result.Error != nil {
		return 0, fmt.Errorf("allocate export lifecycle sequence: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return 0, ErrUnavailable
	}
	return sequence, nil
}

func validatePersistedCreateKeyTx(
	tx *gorm.DB,
	wantJob model.BackupAssetExportJob,
	wantKey model.BackupAssetExportKey,
	exportKey backupasset.DomainKeyMaterial,
	wantEnvelope JobKeyEnvelope,
	wantDEK []byte,
) error {
	if tx == nil || len(exportKey.Key) != 32 || len(wantDEK) != 32 {
		return ErrCipherTampered
	}
	var persistedJob model.BackupAssetExportJob
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", wantJob.ID).Limit(1).Find(&persistedJob)
	if result.Error != nil {
		return fmt.Errorf("reload persisted export job: %w", result.Error)
	}
	if result.RowsAffected != 1 || persistedJob.ID != wantJob.ID ||
		persistedJob.SelectionDigest != wantJob.SelectionDigest ||
		persistedJob.SelectionSchemaVersion != wantJob.SelectionSchemaVersion {
		return ErrCipherTampered
	}

	var persistedKey model.BackupAssetExportKey
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_id = ?", wantKey.ID, wantJob.ID).
		Limit(1).Find(&persistedKey)
	if result.Error != nil {
		return fmt.Errorf("reload persisted export job key: %w", result.Error)
	}
	if result.RowsAffected != 1 || persistedKey.ID != wantKey.ID || persistedKey.JobID != wantJob.ID ||
		persistedKey.State != "active" || persistedKey.KEKVersion != exportKey.Version ||
		persistedKey.WrapAlgorithm != JobKeyWrapAlgorithmV1 || persistedKey.KeyRevision != wantKey.KeyRevision ||
		!persistedKey.CreatedAt.Equal(persistedJob.CreatedAt) || persistedKey.RewrappedAt != nil || persistedKey.DestroyedAt != nil ||
		subtle.ConstantTimeCompare(persistedKey.EnvelopeNonce, wantEnvelope.Nonce) != 1 ||
		subtle.ConstantTimeCompare(persistedKey.WrappedDEK, wantEnvelope.Ciphertext) != 1 {
		return ErrCipherTampered
	}
	unwrapped, err := UnwrapJobDEK(JobKeyBinding{
		ExportID: persistedJob.ID, SelectionDigest: persistedJob.SelectionDigest,
		KEKVersion: persistedKey.KEKVersion, WrapAlgorithm: persistedKey.WrapAlgorithm,
	}, exportKey.Key, JobKeyEnvelope{Nonce: persistedKey.EnvelopeNonce, Ciphertext: persistedKey.WrappedDEK})
	defer clear(unwrapped)
	if err != nil || subtle.ConstantTimeCompare(unwrapped, wantDEK) != 1 {
		return ErrCipherTampered
	}
	return nil
}

func createPeakStoreBytes(items []FrozenItem, config ServiceConfig) (int64, error) {
	regularLogicalBytes := make([]int64, 0, len(items))
	for _, item := range items {
		if item.EntryType != backupasset.CatalogEntryFile {
			continue
		}
		if item.LogicalSize > config.MaxItemBytes {
			return 0, ErrSelectionLimit
		}
		regularLogicalBytes = append(regularLogicalBytes, item.LogicalSize)
	}
	return peakStoreBytesV1(config.MaxCiphertextBytes, regularLogicalBytes, config.ChunkBytes)
}

func peakStoreBytesV1(finalCiphertextBytes int64, regularLogicalBytes []int64, chunkBytes int64) (int64, error) {
	peakStoreBytes := finalCiphertextBytes
	for _, logicalBytes := range regularLogicalBytes {
		spoolBytes, err := ciphertextSizeV1(logicalBytes, chunkBytes)
		if err != nil {
			return 0, err
		}
		if spoolBytes > math.MaxInt64-peakStoreBytes {
			return 0, ErrArchiveLimit
		}
		peakStoreBytes += spoolBytes
	}
	return peakStoreBytes, nil
}

func maximumRegularSpoolPeakStoreBytesV1(config ServiceConfig) (int64, error) {
	peakStoreBytes, ok := settings.BackupAssetExportMaximumStorePeakV1(
		config.MaxCiphertextBytes, int64(config.Selection.MaxItems), config.MaxItemBytes,
		config.Selection.MaxLogicalBytes, config.ChunkBytes,
	)
	if !ok {
		return 0, ErrArchiveLimit
	}
	return peakStoreBytes, nil
}

func minimumArchiveCiphertextBytesV1(logicalBytes int64, maxItems int, chunkBytes int64) (int64, error) {
	const (
		archiveFixedOverheadBytes   int64 = 64 << 20
		archiveMemberPathBytes      int64 = 4096
		archivePerItemOverheadBytes int64 = 16 * archiveMemberPathBytes
	)
	if logicalBytes <= 0 || maxItems <= 0 {
		return 0, ErrArchiveLimit
	}
	itemCount := int64(maxItems)
	if itemCount > math.MaxInt64/archivePerItemOverheadBytes {
		return 0, ErrArchiveLimit
	}
	compressionSlack := logicalBytes / 8
	if logicalBytes%8 != 0 {
		compressionSlack++
	}
	if logicalBytes > math.MaxInt64-compressionSlack {
		return 0, ErrArchiveLimit
	}
	archivePlaintextBytes := logicalBytes + compressionSlack
	perItemOverheadBytes := itemCount * archivePerItemOverheadBytes
	if perItemOverheadBytes > math.MaxInt64-archivePlaintextBytes {
		return 0, ErrArchiveLimit
	}
	archivePlaintextBytes += perItemOverheadBytes
	if archiveFixedOverheadBytes > math.MaxInt64-archivePlaintextBytes {
		return 0, ErrArchiveLimit
	}
	return ciphertextSizeV1(archivePlaintextBytes+archiveFixedOverheadBytes, chunkBytes)
}

func (service *Service) lookupCreateReplay(
	ctx context.Context, ownerID uint, keyDigest, intentDigest string,
) (CommitCreateResult, bool, error) {
	return service.lookupCreateReplayTx(ctx, service.db.WithContext(ctx), ownerID, keyDigest, intentDigest, false)
}

func (service *Service) lookupCreateReplayTx(
	ctx context.Context,
	tx *gorm.DB,
	ownerID uint,
	keyDigest, intentDigest string,
	lock bool,
) (CommitCreateResult, bool, error) {
	query := tx.WithContext(ctx).Where("owner_user_id = ? AND endpoint = ? AND key_digest = ?", ownerID, exportCreateEndpoint, keyDigest)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row model.BackupAssetExportIdempotency
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CommitCreateResult{}, false, nil
	}
	if err != nil {
		return CommitCreateResult{}, false, err
	}
	if !service.now().UTC().Before(row.ExpiresAt.UTC()) {
		return CommitCreateResult{}, false, nil
	}
	if row.RequestIntentDigest != intentDigest {
		return CommitCreateResult{}, true, ErrConflict
	}
	if row.State != "committed" || row.ResultJobID == nil || backupasset.ValidateOpaqueID(*row.ResultJobID) != nil {
		return CommitCreateResult{}, true, ErrUnavailable
	}
	var job model.BackupAssetExportJob
	if err := tx.WithContext(ctx).Where("id = ? AND owner_user_id = ?", *row.ResultJobID, ownerID).Take(&job).Error; err != nil {
		return CommitCreateResult{}, true, ErrUnavailable
	}
	return CommitCreateResult{JobID: job.ID, SelectionDigest: job.SelectionDigest, Replay: true}, true, nil
}

type selectionSource struct {
	recoveryPointID string
	retentionUntil  *time.Time
}

func groupSelectionSources(items []FrozenItem) []selectionSource {
	byID := make(map[string]*time.Time)
	for _, item := range items {
		current, exists := byID[item.Ref.RecoveryPointID]
		if !exists || (item.RetentionUntil != nil && (current == nil || item.RetentionUntil.Before(*current))) {
			byID[item.Ref.RecoveryPointID] = item.RetentionUntil
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]selectionSource, 0, len(ids))
	for _, id := range ids {
		result = append(result, selectionSource{recoveryPointID: id, retentionUntil: byID[id]})
	}
	return result
}

func ensureQuotaBucketTx(tx *gorm.DB, scope, subject string, now time.Time) (string, error) {
	digest := sha256.Sum256([]byte("xirang.backup_asset.export.quota.v1\x00" + scope + "\x00" + subject))
	id := hex.EncodeToString(digest[:16])
	row := model.BackupAssetExportQuotaBucket{
		ID: id, Scope: scope, Subject: subject, TransitionRevision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope"}, {Name: "subject"}}, DoNothing: true}).
		Create(&row).Error; err != nil {
		return "", fmt.Errorf("ensure export quota bucket: %w", err)
	}
	var persisted model.BackupAssetExportQuotaBucket
	if err := tx.Where("scope = ? AND subject = ?", scope, subject).Take(&persisted).Error; err != nil {
		return "", fmt.Errorf("load export quota bucket: %w", err)
	}
	return persisted.ID, nil
}

func EnsurePermanentUseLatchTx(tx *gorm.DB, now time.Time) (string, error) {
	if tx == nil || now.IsZero() || now.Location() != time.UTC {
		return "", ErrUnavailable
	}
	return ensureQuotaBucketTx(tx, "global", "global", now)
}

func validServiceConfig(config ServiceConfig) bool {
	if !validSelectionLimits(config.Selection) || !validQuotaLimits(config.Quota) ||
		config.ChunkBytes <= 0 || config.MaxItemBytes <= 0 ||
		config.MaxProviderBytes <= 0 || config.MaxCiphertextBytes <= 0 || config.MaxOpenReaders <= 0 ||
		config.MaxDuration <= 0 || config.MaxAttempts <= 0 || config.RetryBase <= 0 ||
		config.RetryMaxDelay < config.RetryBase || config.LeaseTTL <= 0 || config.LeaseRenewMargin <= 0 ||
		config.LeaseRenewMargin >= config.LeaseTTL || config.ReadyTTL <= 0 || config.IdempotencyTTL <= 0 ||
		!ValidIdempotencyKeyMaxBytes(config.IdempotencyKeyMaxBytes) {
		return false
	}
	minimumCiphertextBytes, err := minimumArchiveCiphertextBytesV1(
		config.Selection.MaxLogicalBytes, config.Selection.MaxItems, config.ChunkBytes,
	)
	if err != nil || config.MaxCiphertextBytes < minimumCiphertextBytes {
		return false
	}
	maximumPeakStoreBytes, err := maximumRegularSpoolPeakStoreBytesV1(config)
	if err != nil {
		return false
	}
	return maximumPeakStoreBytes <= config.Quota.UserStoreBytes
}

func nonNilServiceContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func validateStatusJob(job model.BackupAssetExportJob) error {
	state := ExecutionState(job.ExecutionState)
	cleanup := CleanupState(job.CleanupState)
	result := ResultKind(job.ResultKind)
	if !lowerHex(job.ID, 32) || !lowerHex(job.SelectionDigest, 64) || job.SelectionSchemaVersion != 1 ||
		!ValidArchiveProfilePair(ArchiveFormat(job.ArchiveFormat), job.ArchiveProfile) || job.LimitsSchemaVersion != 1 ||
		!validExecutionStates[state] || !validCleanupStates[cleanup] ||
		(job.ResultKind != "" && result != ResultComplete && result != ResultPartial) ||
		job.CreatedAt.IsZero() || job.AbsoluteDeadline.IsZero() || job.CreatedAt.After(job.AbsoluteDeadline) ||
		job.ItemCount < 0 || job.PackedCount < 0 || job.SkippedCount < 0 ||
		job.FailedCount < 0 || job.PackedCount+job.SkippedCount+job.FailedCount > job.ItemCount ||
		job.LogicalBytes < 0 || job.ProviderBytes < 0 || job.ArtifactBytes < 0 ||
		job.TransitionRevision <= 0 || !validPersistedStatusCategory(job.ErrorCategory) {
		return ErrUnavailable
	}
	if job.CurrentAttemptID != nil && !lowerHex(*job.CurrentAttemptID, 32) {
		return ErrUnavailable
	}
	if (state == ExecutionReady || state == ExecutionExpiring || state == ExecutionExpired) &&
		(job.ReadyAt == nil || job.ExpiresAt == nil || job.ResultKind == "" || job.PackedCount == 0) {
		return ErrUnavailable
	}
	if job.ReadyAt != nil {
		readyAt := job.ReadyAt.UTC()
		if readyAt.IsZero() || job.ExpiresAt == nil || job.ExpiresAt.UTC().IsZero() ||
			!job.ExpiresAt.UTC().After(readyAt) {
			return ErrUnavailable
		}
	}
	if result == ResultComplete &&
		(job.PackedCount != job.ItemCount || job.SkippedCount != 0 || job.FailedCount != 0) {
		return ErrUnavailable
	}
	if result == ResultPartial && (job.PackedCount == 0 || job.SkippedCount+job.FailedCount == 0) {
		return ErrUnavailable
	}
	return nil
}

func validPersistedStatusCategory(category string) bool {
	switch category {
	case "", "source_changed", "source_expired", ItemErrorLinkMetadataUnavailable, ItemErrorSpecialFileSkipped,
		"artifact_missing", "artifact_tampered", "key_unavailable", "quota_exceeded",
		"deadline", "canceled", "internal_failure", "worker_unavailable", "provider_unavailable",
		"archive_failed", "heartbeat_lost":
		return true
	default:
		return false
	}
}

func publicStatusCategory(category string) string {
	switch category {
	case "archive_failed", "heartbeat_lost":
		return "internal_failure"
	default:
		return category
	}
}

func jobStatusFromModel(job model.BackupAssetExportJob, items []JobItemStatus, now time.Time) JobStatus {
	state := ExecutionState(job.ExecutionState)
	status := JobStatus{
		SchemaVersion: 1, ID: job.ID, SelectionDigest: job.SelectionDigest,
		ArchiveFormat: ArchiveFormat(job.ArchiveFormat), ArchiveProfile: job.ArchiveProfile,
		ExecutionState: state, ResultKind: ResultKind(job.ResultKind), CleanupState: CleanupState(job.CleanupState),
		ItemCount: job.ItemCount, PackedCount: job.PackedCount, SkippedCount: job.SkippedCount,
		FailedCount: job.FailedCount, LogicalBytes: job.LogicalBytes, ProviderBytes: job.ProviderBytes,
		ArtifactBytes: job.ArtifactBytes, ErrorCategory: publicStatusCategory(job.ErrorCategory),
		CreatedAt: job.CreatedAt.UTC(), AbsoluteDeadline: job.AbsoluteDeadline.UTC(),
		Items: items, TransitionRevision: job.TransitionRevision,
	}
	if job.ReadyAt != nil {
		readyAt := job.ReadyAt.UTC()
		status.ReadyAt = &readyAt
	}
	if job.ExpiresAt != nil {
		expiresAt := job.ExpiresAt.UTC()
		status.ExpiresAt = &expiresAt
	}
	switch state {
	case ExecutionQueued, ExecutionRunning, ExecutionRetryWait, ExecutionSealing,
		ExecutionCancelRequested, ExecutionExpiring:
		status.PollAfterSeconds = 2
	}
	switch state {
	case ExecutionQueued, ExecutionRunning, ExecutionRetryWait, ExecutionSealing,
		ExecutionReady, ExecutionCancelRequested:
		status.CanCancel = true
	}
	status.CanDownload = state == ExecutionReady && status.CleanupState == CleanupNone &&
		status.ExpiresAt != nil && now.UTC().Before(*status.ExpiresAt)
	return status
}

func (service *Service) encodeItemCursor(ctx context.Context, cursor itemCursorV1) (string, error) {
	if service == nil || service.keys == nil || !validItemCursor(cursor) {
		return "", ErrUnavailable
	}
	material, err := service.keys.Active(nonNilServiceContext(ctx), backupasset.KeyDomainExportStore)
	defer clear(material.Key)
	if err != nil || material.Domain != backupasset.KeyDomainExportStore || material.Version <= 0 ||
		material.State != backupasset.DomainKeyActive || len(material.Key) != 32 {
		return "", ErrUnavailable
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", ErrUnavailable
	}
	block, err := aes.NewCipher(material.Key)
	if err != nil {
		return "", ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", ErrUnavailable
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", ErrUnavailable
	}
	version := make([]byte, 4)
	binary.BigEndian.PutUint32(version, uint32(material.Version))
	sealed := aead.Seal(nil, nonce, payload, itemCursorAdditionalData(version))
	tokenBytes := make([]byte, 0, len(version)+len(nonce)+len(sealed))
	tokenBytes = append(tokenBytes, version...)
	tokenBytes = append(tokenBytes, nonce...)
	tokenBytes = append(tokenBytes, sealed...)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	if len(token) > itemCursorMaxTokenSize {
		return "", ErrUnavailable
	}
	return token, nil
}

func (service *Service) decodeItemCursor(ctx context.Context, token string) (itemCursorV1, error) {
	if service == nil || service.keys == nil || token == "" || len(token) > itemCursorMaxTokenSize {
		return itemCursorV1{}, ErrInvalidSelection
	}
	tokenBytes, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(tokenBytes) < 4 {
		return itemCursorV1{}, ErrInvalidSelection
	}
	versionBytes := tokenBytes[:4]
	version := binary.BigEndian.Uint32(versionBytes)
	if version == 0 || uint64(version) > uint64(^uint(0)>>1) {
		return itemCursorV1{}, ErrInvalidSelection
	}
	material, err := service.keys.ByVersion(nonNilServiceContext(ctx), backupasset.KeyDomainExportStore, int(version))
	defer clear(material.Key)
	if err != nil || material.Domain != backupasset.KeyDomainExportStore || material.Version != int(version) ||
		len(material.Key) != 32 || (material.State != backupasset.DomainKeyActive && material.State != backupasset.DomainKeyVerifyOnly) {
		return itemCursorV1{}, ErrInvalidSelection
	}
	block, err := aes.NewCipher(material.Key)
	if err != nil {
		return itemCursorV1{}, ErrInvalidSelection
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(tokenBytes) < 4+aead.NonceSize()+aead.Overhead() {
		return itemCursorV1{}, ErrInvalidSelection
	}
	nonce := tokenBytes[4 : 4+aead.NonceSize()]
	plaintext, err := aead.Open(nil, nonce, tokenBytes[4+aead.NonceSize():], itemCursorAdditionalData(versionBytes))
	if err != nil || len(plaintext) == 0 || len(plaintext) > itemCursorMaxTokenSize {
		return itemCursorV1{}, ErrInvalidSelection
	}
	var cursor itemCursorV1
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return itemCursorV1{}, ErrInvalidSelection
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || !validItemCursor(cursor) {
		return itemCursorV1{}, ErrInvalidSelection
	}
	return cursor, nil
}

func validItemCursor(cursor itemCursorV1) bool {
	return cursor.SchemaVersion == itemCursorSchemaV1 && lowerHex(cursor.JobID, 32) &&
		lowerHex(cursor.SelectionDigest, 64) && cursor.NextOrdinal >= 0
}

func itemCursorAdditionalData(version []byte) []byte {
	additionalData := make([]byte, 0, len(itemCursorDomain)+1+len(version))
	additionalData = append(additionalData, itemCursorDomain...)
	additionalData = append(additionalData, 0)
	additionalData = append(additionalData, version...)
	return additionalData
}
