package runtime

import (
	"context"
	"errors"
	"fmt"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// PreparePreviewSource proves the exact source tuple used by a preview before
// the delivery broker is asked to issue a ticket. Immutable points only return
// the existing Catalog status. A legacy mutable Rsync point gets a stat-only
// source session; only a closed mutable-source drift result can enter the
// bounded repository refresh/invalidation path.
func (runtime *Runtime) PreparePreviewSource(
	ctx context.Context,
	actor content.DeliveryActor,
	ref backupasset.AssetRef,
) (catalog.StatusDTO, error) {
	if runtime == nil || runtime.foundation == nil || runtime.repository == nil ||
		runtime.catalogService == nil || runtime.contentAuthorizer == nil {
		return catalog.StatusDTO{}, fmt.Errorf("%w: preview source preparation unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if backupasset.ValidateAssetRef(ref) != nil {
		return catalog.StatusDTO{}, fmt.Errorf("%w: preview source reference", backupasset.ErrForbidden)
	}
	if err := ctx.Err(); err != nil {
		return catalog.StatusDTO{}, err
	}
	live, err := runtime.FeatureLive()
	if err != nil {
		return catalog.StatusDTO{}, err
	}
	if !live {
		return catalog.StatusDTO{}, &backuprepository.CapabilityError{
			Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityFeatureDisabled},
		}
	}

	asset, err := runtime.contentAuthorizer.Authorize(ctx, actor, ref, content.DeliveryPreview)
	if err != nil {
		if isPendingMutablePreviewAuthorization(err) {
			if pendingErr := runtime.rearmPendingMutablePreviewSource(ctx, ref); pendingErr != nil {
				return catalog.StatusDTO{}, pendingErr
			}
			return runtime.previewAfterPendingAuthorization(ctx, actor, ref)
		}
		return catalog.StatusDTO{}, err
	}
	if asset.Provider != backupasset.ProviderRsync || asset.CatalogGenerationID == "" {
		return runtime.previewCatalogStatus(ctx, actor, ref)
	}
	mutableRsync, err := runtime.mutableRsyncPreviewAsset(ctx, asset)
	if err != nil {
		return catalog.StatusDTO{}, err
	}
	if !mutableRsync {
		return runtime.previewCatalogStatus(ctx, actor, ref)
	}
	stat, err := runtime.statPreviewSource(ctx, asset)
	if err == nil && !previewSourceStatMatches(stat, asset) {
		err = &backuprepository.CapabilityError{
			Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityMutableSourceChanged},
		}
	}
	if err == nil {
		return runtime.previewMutableStatusAfterStat(ctx, actor, ref, asset)
	}
	if !previewSourceDriftCandidate(err) {
		if status, joined, joinErr := runtime.previewStatusAfterRace(ctx, actor, ref, asset, err); joined {
			return status, joinErr
		}
		return catalog.StatusDTO{}, err
	}

	preparation, prepareErr := runtime.repository.PrepareMutablePreviewSource(ctx,
		catalog.PointReadRequest{RepositoryID: asset.RepositoryID, RecoveryPointID: ref.RecoveryPointID},
		asset.CatalogGenerationID)
	if prepareErr != nil {
		return catalog.StatusDTO{}, prepareErr
	}
	if !preparation.Invalidated && !preparation.Joined {
		return catalog.StatusDTO{}, fmt.Errorf("%w: mutable preview preparation did not establish a Catalog outcome", backupasset.ErrConflict)
	}
	return runtime.previewCatalogStatusAndWake(ctx, actor, ref)
}
func (runtime *Runtime) previewMutableStatusAfterStat(
	ctx context.Context,
	actor content.DeliveryActor,
	ref backupasset.AssetRef,
	asset content.AuthorizedAsset,
) (catalog.StatusDTO, error) {
	status, err := runtime.previewCatalogStatus(ctx, actor, ref)
	if err != nil {
		return catalog.StatusDTO{}, err
	}
	if previewStatusNeedsMutableRearm(status) {
		preparation, prepareErr := runtime.repository.PrepareMutablePreviewSource(
			ctx,
			catalog.PointReadRequest{RepositoryID: asset.RepositoryID, RecoveryPointID: ref.RecoveryPointID},
			asset.CatalogGenerationID,
		)
		if prepareErr != nil {
			return catalog.StatusDTO{}, prepareErr
		}
		if !preparation.Invalidated && !preparation.Joined {
			return catalog.StatusDTO{}, fmt.Errorf("%w: mutable preview preparation did not establish a Catalog outcome", backupasset.ErrConflict)
		}
		return runtime.previewCatalogStatusAndWake(ctx, actor, ref)
	}
	if previewStatusNeedsCatalogWake(status) {
		runtime.repository.RequestCatalogWake()
	}
	return status, nil
}
func (runtime *Runtime) previewAfterPendingAuthorization(
	ctx context.Context,
	actor content.DeliveryActor,
	ref backupasset.AssetRef,
) (catalog.StatusDTO, error) {
	asset, err := runtime.contentAuthorizer.Authorize(ctx, actor, ref, content.DeliveryPreview)
	if err != nil {
		if isPendingMutablePreviewAuthorization(err) {
			return runtime.previewCatalogStatusAndWake(ctx, actor, ref)
		}
		return catalog.StatusDTO{}, err
	}
	if asset.Provider != backupasset.ProviderRsync || asset.CatalogGenerationID == "" {
		return runtime.previewCatalogStatus(ctx, actor, ref)
	}
	mutableRsync, err := runtime.mutableRsyncPreviewAsset(ctx, asset)
	if err != nil {
		return catalog.StatusDTO{}, err
	}
	if !mutableRsync {
		return runtime.previewCatalogStatus(ctx, actor, ref)
	}
	stat, err := runtime.statPreviewSource(ctx, asset)
	if err == nil && !previewSourceStatMatches(stat, asset) {
		err = &backuprepository.CapabilityError{
			Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityMutableSourceChanged},
		}
	}
	if err != nil {
		if !previewSourceDriftCandidate(err) {
			return catalog.StatusDTO{}, err
		}
		preparation, prepareErr := runtime.repository.PrepareMutablePreviewSource(
			ctx,
			catalog.PointReadRequest{RepositoryID: asset.RepositoryID, RecoveryPointID: ref.RecoveryPointID},
			asset.CatalogGenerationID,
		)
		if prepareErr != nil {
			return catalog.StatusDTO{}, prepareErr
		}
		if !preparation.Invalidated && !preparation.Joined {
			return catalog.StatusDTO{}, fmt.Errorf("%w: pending mutable preview preparation did not establish a Catalog outcome", backupasset.ErrConflict)
		}
		return runtime.previewCatalogStatusAndWake(ctx, actor, ref)
	}
	return runtime.previewMutableStatusAfterStat(ctx, actor, ref, asset)
}

func (runtime *Runtime) statPreviewSource(
	ctx context.Context,
	asset content.AuthorizedAsset,
) (content.SourceStat, error) {
	source, err := runtime.repository.OpenContentSource(ctx, content.SourceRequest{
		Ref:                 asset.Ref,
		CatalogGenerationID: asset.CatalogGenerationID,
		ExpectedSource:      asset.SourceFingerprint,
		ExpectedEntry:       asset.EntryFingerprint,
		Mode:                content.SourceModeStat,
	})
	if err != nil {
		return content.SourceStat{}, err
	}
	if source == nil {
		return content.SourceStat{}, fmt.Errorf("%w: nil preview source session", backupasset.ErrInvalidState)
	}
	stat := source.Stat()
	if err := source.Close(); err != nil {
		return content.SourceStat{}, err
	}
	return stat, nil
}

func (runtime *Runtime) mutableRsyncPreviewAsset(
	ctx context.Context,
	asset content.AuthorizedAsset,
) (bool, error) {
	if runtime == nil || runtime.contentAuthorizer == nil || runtime.contentAuthorizer.db == nil {
		return false, fmt.Errorf("%w: preview source authorizer unavailable", backupasset.ErrInvalidState)
	}
	var point model.RecoveryPoint
	result := runtime.contentAuthorizer.db.WithContext(ctx).
		Where("id = ? AND repository_id = ?", asset.Ref.RecoveryPointID, asset.RepositoryID).
		Take(&point)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return false, fmt.Errorf("%w: preview source recovery point", backupasset.ErrNotFound)
	}
	if result.Error != nil {
		return false, fmt.Errorf("load preview source recovery point: %w", result.Error)
	}
	if point.Semantics != string(backupasset.PointMutableHead) ||
		point.State != string(backupasset.RecoveryPointObserved) {
		return false, nil
	}
	var repository model.BackupRepository
	result = runtime.contentAuthorizer.db.WithContext(ctx).
		Where("id = ?", asset.RepositoryID).
		Take(&repository)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return false, fmt.Errorf("%w: preview source repository", backupasset.ErrNotFound)
	}
	if result.Error != nil {
		return false, fmt.Errorf("load preview source repository: %w", result.Error)
	}
	return repository.ProviderKind == string(backupasset.ProviderRsync) &&
		repository.VersionMode == string(backupasset.VersionMutableHead), nil
}

func (runtime *Runtime) rearmPendingMutablePreviewSource(
	ctx context.Context,
	ref backupasset.AssetRef,
) error {
	var point model.RecoveryPoint
	result := runtime.contentAuthorizer.db.WithContext(ctx).
		Where("id = ?", ref.RecoveryPointID).
		Take(&point)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: preview source recovery point", backupasset.ErrNotFound)
	}
	if result.Error != nil {
		return fmt.Errorf("load preview source recovery point: %w", result.Error)
	}
	preparation, err := runtime.repository.PrepareMutablePreviewSource(
		ctx,
		catalog.PointReadRequest{RepositoryID: point.RepositoryID, RecoveryPointID: ref.RecoveryPointID},
		"",
	)
	if err != nil {
		return err
	}
	if !preparation.Invalidated && !preparation.Joined {
		return fmt.Errorf("%w: pending mutable preview preparation did not establish a Catalog outcome", backupasset.ErrConflict)
	}
	return nil
}

func previewStatusNeedsCatalogWake(status catalog.StatusDTO) bool {
	return status.Generation == nil ||
		status.Coverage.Status != catalog.CoverageComplete ||
		status.Staleness.Status != catalog.StalenessFresh
}
func previewStatusNeedsMutableRearm(status catalog.StatusDTO) bool {
	if status.Generation == nil || status.Coverage.Status != catalog.CoverageComplete ||
		status.Staleness.Status != catalog.StalenessFresh || !status.ContentAvailability.Available {
		return true
	}
	if status.LatestBuild == nil {
		return false
	}
	switch status.LatestBuild.State {
	case catalog.GenerationPartial, catalog.GenerationFailed, catalog.GenerationSuperseded:
		return true
	default:
		return false
	}
}

func (runtime *Runtime) previewCatalogStatusAndWake(
	ctx context.Context,
	actor content.DeliveryActor,
	ref backupasset.AssetRef,
) (catalog.StatusDTO, error) {
	status, err := runtime.previewCatalogStatus(ctx, actor, ref)
	if err == nil && previewStatusNeedsCatalogWake(status) {
		runtime.repository.RequestCatalogWake()
	}
	return status, err
}

func (runtime *Runtime) previewStatusAfterRace(
	ctx context.Context,
	actor content.DeliveryActor,
	ref backupasset.AssetRef,
	asset content.AuthorizedAsset,
	original error,
) (catalog.StatusDTO, bool, error) {
	current, err := runtime.contentAuthorizer.Authorize(ctx, actor, ref, content.DeliveryPreview)
	if err != nil {
		if isPendingMutablePreviewAuthorization(err) {
			if pendingErr := runtime.rearmPendingMutablePreviewSource(ctx, ref); pendingErr != nil {
				return catalog.StatusDTO{}, true, pendingErr
			}
			status, statusErr := runtime.previewAfterPendingAuthorization(ctx, actor, ref)
			return status, true, statusErr
		}
		return catalog.StatusDTO{}, false, original
	}
	if current.CatalogGenerationID == asset.CatalogGenerationID {
		return catalog.StatusDTO{}, false, original
	}
	if current.Provider != backupasset.ProviderRsync || current.CatalogGenerationID == "" {
		status, statusErr := runtime.previewCatalogStatus(ctx, actor, ref)
		return status, true, statusErr
	}
	mutableRsync, mutableErr := runtime.mutableRsyncPreviewAsset(ctx, current)
	if mutableErr != nil {
		return catalog.StatusDTO{}, false, original
	}
	if !mutableRsync {
		status, statusErr := runtime.previewCatalogStatus(ctx, actor, ref)
		return status, true, statusErr
	}
	stat, statErr := runtime.statPreviewSource(ctx, current)
	if statErr == nil && !previewSourceStatMatches(stat, current) {
		statErr = &backuprepository.CapabilityError{
			Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityMutableSourceChanged},
		}
	}
	if statErr != nil {
		if !previewSourceDriftCandidate(statErr) {
			return catalog.StatusDTO{}, false, original
		}
	}
	if statErr == nil {
		status, statusErr := runtime.previewMutableStatusAfterStat(ctx, actor, ref, current)
		return status, true, statusErr
	}
	preparation, prepareErr := runtime.repository.PrepareMutablePreviewSource(
		ctx,
		catalog.PointReadRequest{RepositoryID: current.RepositoryID, RecoveryPointID: ref.RecoveryPointID},
		current.CatalogGenerationID,
	)
	if prepareErr != nil {
		return catalog.StatusDTO{}, true, prepareErr
	}
	if !preparation.Invalidated && !preparation.Joined {
		return catalog.StatusDTO{}, true,
			fmt.Errorf("%w: raced mutable preview preparation did not establish a Catalog outcome", backupasset.ErrConflict)
	}
	status, statusErr := runtime.previewCatalogStatusAndWake(ctx, actor, ref)
	return status, true, statusErr
}

func (runtime *Runtime) previewCatalogStatus(
	ctx context.Context,
	actor content.DeliveryActor,
	ref backupasset.AssetRef,
) (catalog.StatusDTO, error) {
	status, err := runtime.catalogService.GetCatalogStatus(ctx, ref.RecoveryPointID,
		catalog.AuthorizationScope{Role: actor.Role, UserID: actor.UserID})
	if err != nil {
		return catalog.StatusDTO{}, err
	}
	if err := status.Validate(); err != nil {
		return catalog.StatusDTO{}, err
	}
	return status, nil
}

func previewSourceStatMatches(stat content.SourceStat, asset content.AuthorizedAsset) bool {
	if stat.SourceFingerprint != asset.SourceFingerprint || stat.EntryFingerprint != asset.EntryFingerprint || stat.Size != asset.Size {
		return false
	}
	if asset.ModifiedAt == nil {
		return true
	}
	return stat.ModifiedAt != nil && stat.ModifiedAt.UTC().Equal(asset.ModifiedAt.UTC())
}

func previewSourceDriftCandidate(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	reason, _, ok := backuprepository.CapabilityFromError(err)
	if !ok {
		return false
	}
	return reason.Code == backupasset.CapabilityMutableSourceChanged || reason.Code == backupasset.CapabilityRepositoryOffline
}

func isPendingMutablePreviewAuthorization(err error) bool {
	stage, ok := content.SourceFailureStageFromError(err)
	return ok && stage == content.SourceFailureChanged && errors.Is(err, content.ErrContentSourceUnavailable)
}

// pendingMutablePreviewAuthorization distinguishes a real missing entry from
// a known historical entry whose mutable projection is between generations.
// It is intentionally called only after ownership has already been checked.
func (authorizer *runtimeContentAuthorizer) pendingMutablePreviewAuthorization(
	ctx context.Context,
	ref backupasset.AssetRef,
) (bool, error) {
	if authorizer == nil || authorizer.db == nil {
		return false, fmt.Errorf("%w: Content authorizer unavailable", backupasset.ErrInvalidState)
	}
	var historicalCount int64
	if err := authorizer.db.WithContext(ctx).Model(&model.CatalogEntry{}).
		Where("recovery_point_id = ? AND entry_id = ?", ref.RecoveryPointID, ref.EntryID).
		Count(&historicalCount).Error; err != nil {
		return false, fmt.Errorf("load historical Content entry: %w", err)
	}
	if historicalCount == 0 {
		return false, nil
	}
	var point model.RecoveryPoint
	if err := authorizer.db.WithContext(ctx).Where("id = ?", ref.RecoveryPointID).Take(&point).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, fmt.Errorf("%w: Content recovery point", backupasset.ErrNotFound)
		}
		return false, fmt.Errorf("load Content recovery point: %w", err)
	}
	if point.Semantics != string(backupasset.PointMutableHead) || point.State != string(backupasset.RecoveryPointObserved) {
		return false, nil
	}
	var repository model.BackupRepository
	if err := authorizer.db.WithContext(ctx).Where("id = ?", point.RepositoryID).Take(&repository).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, fmt.Errorf("%w: Content repository", backupasset.ErrNotFound)
		}
		return false, fmt.Errorf("load Content repository: %w", err)
	}
	if repository.ProviderKind != string(backupasset.ProviderRsync) ||
		repository.VersionMode != string(backupasset.VersionMutableHead) {
		return false, nil
	}
	switch backupasset.RepositoryStatus(repository.Status) {
	case backupasset.RepositoryOnline:
	case backupasset.RepositoryOffline:
		return false, &backuprepository.CapabilityError{Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityRepositoryOffline}}
	case backupasset.RepositoryDisconnected:
		return false, &backuprepository.CapabilityError{Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityRepositoryDisconnected}}
	default:
		return false, fmt.Errorf("%w: Content repository status", backupasset.ErrConflict)
	}
	if point.PhysicalAvailability != string(backupasset.PhysicalOnline) {
		return false, &backuprepository.CapabilityError{Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable}}
	}
	var active model.CatalogGeneration
	activeResult := authorizer.db.WithContext(ctx).
		Where("recovery_point_id = ? AND state = ? AND is_active = ?", ref.RecoveryPointID, catalog.GenerationComplete, true).
		Limit(1).Find(&active)
	if activeResult.Error != nil {
		return false, fmt.Errorf("load active Content generation: %w", activeResult.Error)
	}
	if activeResult.RowsAffected == 1 && active.SourceFingerprint == point.SourceFingerprint {
		return false, nil
	}
	return true, nil
}

var _ interface {
	PreparePreviewSource(context.Context, content.DeliveryActor, backupasset.AssetRef) (catalog.StatusDTO, error)
} = (*Runtime)(nil)
