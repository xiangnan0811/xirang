package processing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrDerivedUnauthorized  = errors.New("derived artifact is not authorized")
	ErrDerivedFenceRequired = errors.New("derived projection revocation requires a processing fence")
)

type DerivedRevokeReason string

const (
	DerivedRevokeExplicit      DerivedRevokeReason = "explicit"
	DerivedRevokeExpired       DerivedRevokeReason = "expired"
	DerivedRevokeSourceChanged DerivedRevokeReason = "source_changed"
	DerivedRevokePolicyChanged DerivedRevokeReason = "policy_changed"
	DerivedRevokeKeyLoss       DerivedRevokeReason = "key_loss"
	DerivedRevokeRollback      DerivedRevokeReason = "rollback"
)

type DerivedArtifactAuthorization struct {
	ArtifactID          string
	RecoveryPointID     string
	CatalogGenerationID string
	EntryID             string
	SourceFingerprint   string
}

type DerivedProjectionPublish struct {
	ArtifactSetID       string
	RecoveryPointID     string
	CatalogGenerationID string
	EntryID             string
	SourceFingerprint   string
	RecoveryPointFence  backupasset.LeaseFence
	Fields              []DerivedProjectionField
	Classification      *DerivedClassificationEvidence
}

type DerivedSensitivity string

const (
	DerivedSensitivityPublic  DerivedSensitivity = "public"
	DerivedSensitivitySecret  DerivedSensitivity = "secret"
	DerivedSensitivityUnknown DerivedSensitivity = "unknown"
)

type DerivedClassificationEvidence struct {
	ArtifactID  string
	Sensitivity DerivedSensitivity
	Categories  []string
}

type DerivedProjectionTerm struct {
	Term      string
	Frequency int
}

type DerivedProjectionField struct {
	ExcerptArtifactID string
	Role              ArtifactRole
	Completeness      ArtifactCompleteness
	Terms             []DerivedProjectionTerm
}

type DerivedProjectionPublication struct {
	ArtifactSetID string
	Revision      int64
}

type DerivedProjectionRevoke struct {
	ArtifactSetID       string
	RecoveryPointID     string
	CatalogGenerationID string
	EntryID             string
	SourceFingerprint   string
	ProjectionRevision  int64
	Reason              DerivedRevokeReason
	RecoveryPointFence  backupasset.LeaseFence
}

type DerivedProjectionPort interface {
	PreparePublish(context.Context, DerivedProjectionPublish) (PreparedDerivedProjection, error)
	PrepareRevoke(context.Context, DerivedProjectionRevoke) (PreparedDerivedRevocation, error)
}

type PreparedDerivedProjection interface {
	// PublishTx is idempotent by ArtifactSetID and must use only the supplied
	// caller transaction for durable writes.
	PublishTx(context.Context, *gorm.DB) (DerivedProjectionPublication, error)
}

type PreparedDerivedRevocation interface {
	RevokeTx(context.Context, *gorm.DB) error
}

type DerivedFenceLease interface {
	Acquire(context.Context, backupasset.AcquireLeaseRequest) (backupasset.Lease, error)
	Release(context.Context, backupasset.LeaseFence) error
}

type DerivedLifecycle struct {
	db         *gorm.DB
	store      *DerivedStore
	projection DerivedProjectionPort
	fenceLease DerivedFenceLease
	now        func() time.Time
}

type preparedStaleDerivedSet struct {
	initial model.BackupAssetDerivedArtifactSet
	revoke  PreparedDerivedRevocation
	fence   backupasset.LeaseFence
}

func NewDerivedLifecycle(
	db *gorm.DB,
	store *DerivedStore,
	projection DerivedProjectionPort,
	now func() time.Time,
	fenceLeases ...DerivedFenceLease,
) (*DerivedLifecycle, error) {
	if db == nil || store == nil || projection == nil {
		return nil, ErrDerivedStoreUnavailable
	}
	if len(fenceLeases) > 1 || len(fenceLeases) == 1 && fenceLeases[0] == nil {
		return nil, ErrDerivedStoreUnavailable
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	lifecycle := &DerivedLifecycle{db: db, store: store, projection: projection, now: now}
	if len(fenceLeases) == 1 {
		lifecycle.fenceLease = fenceLeases[0]
	}
	return lifecycle, nil
}

func (lifecycle *DerivedLifecycle) ReadAuthorized(ctx context.Context, authorization DerivedArtifactAuthorization, destination io.Writer) error {
	if lifecycle == nil || destination == nil || !validDerivedAuthorization(authorization) {
		return ErrDerivedUnauthorized
	}
	var reference model.BackupAssetDerivedBlobReference
	result := lifecycle.db.WithContext(ctx).Where(
		"artifact_id = ? AND recovery_point_id = ? AND catalog_generation_id = ? AND entry_id = ? AND source_fingerprint = ? AND state = ?",
		authorization.ArtifactID, authorization.RecoveryPointID, authorization.CatalogGenerationID,
		authorization.EntryID, authorization.SourceFingerprint, "active",
	).Limit(1).Find(&reference)
	if result.Error != nil {
		return fmt.Errorf("load Derived artifact authorization: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrDerivedUnauthorized
	}
	var artifact model.BackupAssetDerivedArtifact
	result = lifecycle.db.WithContext(ctx).Where("id = ? AND blob_id = ?", authorization.ArtifactID, reference.BlobID).Limit(1).Find(&artifact)
	if result.Error != nil || result.RowsAffected != 1 {
		return ErrDerivedUnauthorized
	}
	var set model.BackupAssetDerivedArtifactSet
	result = lifecycle.db.WithContext(ctx).Where(
		"id = ? AND recovery_point_id = ? AND catalog_generation_id = ? AND entry_id = ? AND source_fingerprint = ? AND state IN ? AND (projection_required = ? OR projection_published = ?)",
		artifact.ArtifactSetID, authorization.RecoveryPointID, authorization.CatalogGenerationID,
		authorization.EntryID, authorization.SourceFingerprint, []string{"active", "stale"}, false, true,
	).Limit(1).Find(&set)
	if result.Error != nil || result.RowsAffected != 1 {
		return ErrDerivedUnauthorized
	}
	if err := lifecycle.store.readBlob(ctx, reference.BlobID, destination); err != nil {
		return err
	}
	return nil
}

func (lifecycle *DerivedLifecycle) RevokeSet(ctx context.Context, artifactSetID string, reason DerivedRevokeReason) error {
	return lifecycle.revokeSet(ctx, artifactSetID, reason, nil)
}

func (lifecycle *DerivedLifecycle) RevokeSetFenced(
	ctx context.Context,
	artifactSetID string,
	reason DerivedRevokeReason,
	fence backupasset.LeaseFence,
) error {
	if !validDerivedFence(fence, "") {
		return ErrDerivedFenceRequired
	}
	return lifecycle.revokeSet(ctx, artifactSetID, reason, &fence)
}

func (lifecycle *DerivedLifecycle) revokeSet(
	ctx context.Context,
	artifactSetID string,
	reason DerivedRevokeReason,
	fence *backupasset.LeaseFence,
) error {
	if lifecycle == nil || backupasset.ValidateOpaqueID(artifactSetID) != nil || !validDerivedRevokeReason(reason) {
		return ErrDerivedUnauthorized
	}
	var initial model.BackupAssetDerivedArtifactSet
	result := lifecycle.db.WithContext(ctx).Where("id = ?", artifactSetID).Limit(1).Find(&initial)
	if result.Error != nil {
		return fmt.Errorf("load Derived artifact set: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrDerivedUnauthorized
	}
	if derivedSetUnavailable(initial.State) {
		return nil
	}
	if initial.ProjectionPublished && (fence == nil || !validDerivedFence(*fence, initial.RecoveryPointID)) {
		return ErrDerivedFenceRequired
	}
	var preparedRevoke PreparedDerivedRevocation
	if initial.ProjectionPublished {
		projectionFence := backupasset.LeaseFence{}
		if fence != nil {
			projectionFence = *fence
		}
		prepared, err := lifecycle.projection.PrepareRevoke(ctx, DerivedProjectionRevoke{
			ArtifactSetID: initial.ID, RecoveryPointID: initial.RecoveryPointID,
			CatalogGenerationID: initial.CatalogGenerationID, EntryID: initial.EntryID,
			SourceFingerprint: initial.SourceFingerprint, ProjectionRevision: initial.ProjectionRevision, Reason: reason,
			RecoveryPointFence: projectionFence,
		})
		if err != nil {
			return fmt.Errorf("revoke Derived Search projection: %w", err)
		}
		preparedRevoke = prepared
	}

	type doomedBlob struct {
		id      string
		locator string
	}
	var doomed []doomedBlob
	err := lifecycle.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var set model.BackupAssetDerivedArtifactSet
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", artifactSetID).Limit(1).Find(&set)
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrDerivedUnauthorized
		}
		if derivedSetUnavailable(set.State) {
			return nil
		}
		if set.ProjectionPublished {
			if preparedRevoke == nil {
				return ErrDerivedUnauthorized
			}
			if err := preparedRevoke.RevokeTx(ctx, tx); err != nil {
				return fmt.Errorf("revoke Derived Search projection: %w", err)
			}
		}
		now := lifecycle.utcNow()
		state := derivedSetRevokeState(reason)
		if err := tx.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", set.ID).
			Updates(map[string]any{
				"state": state, "revocation_reason": string(reason), "projection_published": false,
				"revoked_at": now, "updated_at": now,
			}).Error; err != nil {
			return fmt.Errorf("mark Derived artifact set unavailable: %w", err)
		}
		var artifacts []model.BackupAssetDerivedArtifact
		if err := tx.Where("artifact_set_id = ?", set.ID).Find(&artifacts).Error; err != nil {
			return fmt.Errorf("load Derived artifacts for revoke: %w", err)
		}
		artifactIDs := make([]string, 0, len(artifacts))
		blobIDs := make(map[string]struct{}, len(artifacts))
		for _, artifact := range artifacts {
			artifactIDs = append(artifactIDs, artifact.ID)
			blobIDs[artifact.BlobID] = struct{}{}
		}
		if len(artifactIDs) > 0 {
			if err := tx.Model(&model.BackupAssetDerivedBlobReference{}).
				Where("artifact_id IN ? AND state = ?", artifactIDs, "active").
				Updates(map[string]any{"state": derivedReferenceRevokeState(reason), "revoked_at": now, "updated_at": now}).Error; err != nil {
				return fmt.Errorf("revoke Derived blob references: %w", err)
			}
		}
		for blobID := range blobIDs {
			var activeCount int64
			if err := tx.Model(&model.BackupAssetDerivedBlobReference{}).
				Where("blob_id = ? AND state = ?", blobID, "active").Count(&activeCount).Error; err != nil {
				return fmt.Errorf("count live Derived references: %w", err)
			}
			if activeCount > 0 {
				if err := tx.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", blobID).Update("ref_count", activeCount).Error; err != nil {
					return fmt.Errorf("reconcile Derived reference count: %w", err)
				}
				continue
			}
			var blob model.BackupAssetDerivedBlob
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", blobID).Limit(1).Find(&blob)
			if result.Error != nil || result.RowsAffected != 1 {
				return ErrDerivedBlobUnavailable
			}
			if blob.State == "active" || blob.State == "staged" {
				updated := tx.Model(&model.BackupAssetDerivedBlob{}).Where("id = ? AND state IN ?", blob.ID, []string{"active", "staged"}).
					Updates(map[string]any{
						"state": "unavailable", "ref_count": 0, "wrapped_dek": []byte{},
						"unavailable_at": now, "updated_at": now,
					})
				if updated.Error != nil || updated.RowsAffected != 1 {
					return errors.Join(ErrDerivedBlobUnavailable, updated.Error)
				}
				doomed = append(doomed, doomedBlob{id: blob.ID, locator: blob.OpaqueLocator})
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, blob := range doomed {
		if err := lifecycle.removeCiphertext(blob.locator); err != nil {
			_ = lifecycle.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).Where("id = ? AND state = ?", blob.id, "unavailable").
				Update("state", "purge_failed").Error
			return err
		}
	}
	return nil
}

func (lifecycle *DerivedLifecycle) removeCiphertext(locator string) error {
	if lifecycle == nil || lifecycle.store == nil || !safeOpaqueLocator(locator) {
		return ErrDerivedBlobUnavailable
	}
	path := filepath.Join(lifecycle.store.config.Root, locator)
	if err := lifecycle.store.removeFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrDerivedBlobUnavailable
	}
	return nil
}

func (lifecycle *DerivedLifecycle) MarkSetStaleFenced(
	ctx context.Context,
	artifactSetID string,
	fence backupasset.LeaseFence,
) error {
	if lifecycle == nil || backupasset.ValidateOpaqueID(artifactSetID) != nil || !validDerivedFence(fence, "") {
		return ErrDerivedUnauthorized
	}
	prepared, err := lifecycle.prepareMarkSetStaleFenced(ctx, artifactSetID, fence)
	if err != nil {
		return err
	}
	return lifecycle.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return lifecycle.markSetStaleFencedTx(ctx, tx, prepared)
	})
}

func (lifecycle *DerivedLifecycle) prepareMarkSetStaleFenced(
	ctx context.Context,
	artifactSetID string,
	fence backupasset.LeaseFence,
) (preparedStaleDerivedSet, error) {
	if lifecycle == nil || backupasset.ValidateOpaqueID(artifactSetID) != nil || !validDerivedFence(fence, "") {
		return preparedStaleDerivedSet{}, ErrDerivedUnauthorized
	}
	var initial model.BackupAssetDerivedArtifactSet
	result := lifecycle.db.WithContext(ctx).Where("id = ?", artifactSetID).Limit(1).Find(&initial)
	if result.Error != nil {
		return preparedStaleDerivedSet{}, fmt.Errorf("load Derived artifact set for invalidation: %w", result.Error)
	}
	if result.RowsAffected != 1 || !validDerivedFence(fence, initial.RecoveryPointID) {
		return preparedStaleDerivedSet{}, ErrDerivedUnauthorized
	}
	if initial.State == "stale" {
		return preparedStaleDerivedSet{initial: initial, fence: fence}, nil
	}
	if initial.State != "active" {
		return preparedStaleDerivedSet{}, ErrDerivedUnauthorized
	}
	var preparedRevoke PreparedDerivedRevocation
	if initial.ProjectionPublished {
		prepared, err := lifecycle.projection.PrepareRevoke(ctx, DerivedProjectionRevoke{
			ArtifactSetID: initial.ID, RecoveryPointID: initial.RecoveryPointID,
			CatalogGenerationID: initial.CatalogGenerationID, EntryID: initial.EntryID,
			SourceFingerprint: initial.SourceFingerprint, ProjectionRevision: initial.ProjectionRevision,
			Reason: DerivedRevokeRollback, RecoveryPointFence: fence,
		})
		if err != nil {
			return preparedStaleDerivedSet{}, fmt.Errorf("prepare stale Derived Search projection revoke: %w", err)
		}
		preparedRevoke = prepared
	}
	return preparedStaleDerivedSet{initial: initial, revoke: preparedRevoke, fence: fence}, nil
}

func (lifecycle *DerivedLifecycle) markSetStaleFencedTx(
	ctx context.Context,
	tx *gorm.DB,
	prepared preparedStaleDerivedSet,
) error {
	if lifecycle == nil || tx == nil || !validDerivedFence(prepared.fence, prepared.initial.RecoveryPointID) {
		return ErrDerivedUnauthorized
	}
	var set model.BackupAssetDerivedArtifactSet
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", prepared.initial.ID).Limit(1).Find(&set)
	if result.Error != nil || result.RowsAffected != 1 || set.RecoveryPointID != prepared.fence.RecoveryPointID {
		return ErrDerivedUnauthorized
	}
	if set.State == "stale" {
		return nil
	}
	if set.State != "active" || set.ProjectionPublished != prepared.initial.ProjectionPublished ||
		set.ProjectionRevision != prepared.initial.ProjectionRevision {
		return ErrDerivedUnauthorized
	}
	if set.ProjectionPublished {
		if prepared.revoke == nil {
			return ErrDerivedFenceRequired
		}
		if err := prepared.revoke.RevokeTx(ctx, tx); err != nil {
			return fmt.Errorf("revoke stale Derived Search projection: %w", err)
		}
	}
	updated := tx.Model(&model.BackupAssetDerivedArtifactSet{}).
		Where("id = ? AND state = ? AND projection_published = ? AND projection_revision = ?",
			set.ID, "active", set.ProjectionPublished, set.ProjectionRevision).
		Updates(map[string]any{"state": "stale", "projection_published": false, "updated_at": lifecycle.utcNow()})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return errors.Join(ErrDerivedUnauthorized, updated.Error)
	}
	return nil
}

func (lifecycle *DerivedLifecycle) MarkSetStale(ctx context.Context, artifactSetID string) error {
	if lifecycle == nil || lifecycle.fenceLease == nil || backupasset.ValidateOpaqueID(artifactSetID) != nil {
		return ErrDerivedFenceRequired
	}
	var set model.BackupAssetDerivedArtifactSet
	result := lifecycle.db.WithContext(ctx).Select("id", "job_id", "recovery_point_id", "state").
		Where("id = ?", artifactSetID).Limit(1).Find(&set)
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(ErrDerivedUnauthorized, result.Error)
	}
	if set.State == "stale" {
		return nil
	}
	if set.State != "active" || backupasset.ValidateOpaqueID(set.JobID) != nil {
		return ErrDerivedUnauthorized
	}
	lease, err := lifecycle.fenceLease.Acquire(ctx, backupasset.AcquireLeaseRequest{
		RecoveryPointID: set.RecoveryPointID, HolderType: backupasset.LeaseHolderProcessingJob, OwnerID: set.JobID,
	})
	if err != nil {
		return errors.Join(ErrDerivedFenceRequired, err)
	}
	staleErr := lifecycle.MarkSetStaleFenced(ctx, artifactSetID, lease.Fence)
	releaseErr := lifecycle.fenceLease.Release(ctx, lease.Fence)
	return errors.Join(staleErr, releaseErr)
}

func (lifecycle *DerivedLifecycle) revokeSetWithManagedFence(
	ctx context.Context,
	artifactSetID string,
	reason DerivedRevokeReason,
) error {
	var set model.BackupAssetDerivedArtifactSet
	result := lifecycle.db.WithContext(ctx).Select("id", "job_id", "recovery_point_id", "projection_published").
		Where("id = ?", artifactSetID).Limit(1).Find(&set)
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(ErrDerivedUnauthorized, result.Error)
	}
	if !set.ProjectionPublished {
		return lifecycle.RevokeSet(ctx, artifactSetID, reason)
	}
	if lifecycle.fenceLease == nil || backupasset.ValidateOpaqueID(set.JobID) != nil {
		return ErrDerivedFenceRequired
	}
	lease, err := lifecycle.fenceLease.Acquire(ctx, backupasset.AcquireLeaseRequest{
		RecoveryPointID: set.RecoveryPointID, HolderType: backupasset.LeaseHolderProcessingJob, OwnerID: set.JobID,
	})
	if err != nil {
		return errors.Join(ErrDerivedFenceRequired, err)
	}
	revokeErr := lifecycle.RevokeSetFenced(ctx, artifactSetID, reason, lease.Fence)
	releaseErr := lifecycle.fenceLease.Release(ctx, lease.Fence)
	return errors.Join(revokeErr, releaseErr)
}

// MarkActiveKeyLost revokes every artifact backed by the affected Derived KEK
// before allowing the keyring to persist the loss. Search revocation is an
// external atomic port, so it deliberately runs before the keyring transaction;
// the callback then rechecks the database invariant under the keyring lock.
func (lifecycle *DerivedLifecycle) MarkActiveKeyLost(ctx context.Context, version, batchSize int) error {
	if lifecycle == nil || lifecycle.db == nil || lifecycle.store == nil || lifecycle.store.keyring == nil || version <= 0 || batchSize <= 0 || batchSize > 10000 {
		return ErrDerivedStoreUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		var setIDs []string
		if err := lifecycle.db.WithContext(ctx).Table("backup_asset_derived_artifact_sets AS sets").
			Distinct("sets.id").
			Joins("JOIN backup_asset_derived_artifacts AS artifacts ON artifacts.artifact_set_id = sets.id").
			Joins("JOIN backup_asset_derived_blob_references AS refs ON refs.artifact_id = artifacts.id AND refs.blob_id = artifacts.blob_id").
			Joins("JOIN backup_asset_derived_blobs AS blobs ON blobs.id = refs.blob_id").
			Where("blobs.derived_kek_version = ? AND refs.state = ? AND sets.state IN ?", version, "active", []string{"active", "stale"}).
			Order("sets.id ASC").Limit(batchSize).Pluck("sets.id", &setIDs).Error; err != nil {
			return fmt.Errorf("load Derived sets for key loss: %w", err)
		}
		if len(setIDs) == 0 {
			break
		}
		for _, setID := range setIDs {
			if err := lifecycle.revokeSetWithManagedFence(ctx, setID, DerivedRevokeKeyLoss); err != nil {
				return err
			}
		}
	}

	err := lifecycle.store.keyring.MarkRebuildableLost(
		ctx,
		backupasset.KeyDomainDerivedStore,
		version,
		func(_ context.Context, tx *gorm.DB, transition backupasset.RebuildableKeyTransition) error {
			if transition.Domain != backupasset.KeyDomainDerivedStore || transition.PreviousVersion != version || transition.NextVersion != 0 {
				return ErrDerivedStoreUnavailable
			}
			var activeReferences int64
			if err := tx.Table("backup_asset_derived_blob_references AS refs").
				Joins("JOIN backup_asset_derived_blobs AS blobs ON blobs.id = refs.blob_id").
				Where("blobs.derived_kek_version = ? AND refs.state = ?", version, "active").
				Count(&activeReferences).Error; err != nil {
				return fmt.Errorf("count Derived references during key loss: %w", err)
			}
			if activeReferences != 0 {
				return fmt.Errorf("%w: active Derived references remain", backupasset.ErrConflict)
			}
			var publishedSets int64
			if err := tx.Table("backup_asset_derived_artifact_sets AS sets").
				Joins("JOIN backup_asset_derived_artifacts AS artifacts ON artifacts.artifact_set_id = sets.id").
				Joins("JOIN backup_asset_derived_blobs AS blobs ON blobs.id = artifacts.blob_id").
				Where("blobs.derived_kek_version = ? AND sets.projection_published = ?", version, true).
				Count(&publishedSets).Error; err != nil {
				return fmt.Errorf("count Derived projections during key loss: %w", err)
			}
			if publishedSets != 0 {
				return fmt.Errorf("%w: published Derived projections remain", backupasset.ErrConflict)
			}
			now := lifecycle.utcNow()
			if err := tx.Model(&model.BackupAssetDerivedBlob{}).
				Where("derived_kek_version = ? AND state IN ?", version, []string{"active", "staged"}).
				Updates(map[string]any{
					"state": "unavailable", "wrapped_dek": []byte{}, "ref_count": 0,
					"unavailable_at": now, "updated_at": now,
				}).Error; err != nil {
				return fmt.Errorf("invalidate Derived blobs during key loss: %w", err)
			}
			return nil
		},
	)
	if err == nil {
		return nil
	}
	var key model.WrappedDomainKey
	result := lifecycle.db.WithContext(ctx).
		Where("domain = ? AND version = ?", backupasset.KeyDomainDerivedStore, version).
		Limit(1).Find(&key)
	if result.Error == nil && result.RowsAffected == 1 && backupasset.DomainKeyState(key.State) == backupasset.DomainKeyLost {
		return nil
	}
	return err
}

func validDerivedAuthorization(value DerivedArtifactAuthorization) bool {
	return backupasset.ValidateOpaqueID(value.ArtifactID) == nil && backupasset.ValidateOpaqueID(value.RecoveryPointID) == nil &&
		backupasset.ValidateOpaqueID(value.CatalogGenerationID) == nil && value.EntryID != "" && len(value.EntryID) <= 64 &&
		value.SourceFingerprint != "" && len(value.SourceFingerprint) <= 128
}

func validDerivedRevokeReason(value DerivedRevokeReason) bool {
	switch value {
	case DerivedRevokeExplicit, DerivedRevokeExpired, DerivedRevokeSourceChanged, DerivedRevokePolicyChanged, DerivedRevokeKeyLoss, DerivedRevokeRollback:
		return true
	default:
		return false
	}
}

func validDerivedFence(fence backupasset.LeaseFence, recoveryPointID string) bool {
	return backupasset.ValidateOpaqueID(fence.LeaseID) == nil &&
		backupasset.ValidateOpaqueID(fence.RecoveryPointID) == nil &&
		(recoveryPointID == "" || fence.RecoveryPointID == recoveryPointID) &&
		fence.HolderType == backupasset.LeaseHolderProcessingJob &&
		backupasset.ValidateOpaqueID(fence.OwnerID) == nil &&
		backupasset.ValidateOpaqueID(fence.AttemptID) == nil && lowerHex(fence.FenceToken, 64)
}

func derivedSetUnavailable(state string) bool {
	switch state {
	case "unavailable", "superseded", "revoked", "purging", "purge_failed":
		return true
	default:
		return false
	}
}

func derivedSetRevokeState(reason DerivedRevokeReason) string {
	switch reason {
	case DerivedRevokeSourceChanged, DerivedRevokePolicyChanged:
		return "superseded"
	case DerivedRevokeExpired, DerivedRevokeKeyLoss:
		return "unavailable"
	default:
		return "revoked"
	}
}

func derivedReferenceRevokeState(reason DerivedRevokeReason) string {
	if reason == DerivedRevokeExpired || reason == DerivedRevokeKeyLoss {
		return "unavailable"
	}
	return "revoked"
}

func (lifecycle *DerivedLifecycle) utcNow() time.Time { return lifecycle.now().UTC() }
