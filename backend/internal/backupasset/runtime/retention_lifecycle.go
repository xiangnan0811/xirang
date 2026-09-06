package runtime

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	assetexport "xirang/backend/internal/backupasset/export"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/recovery"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/backupasset/retention"
	assetsearch "xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

type ContentSourceLifecycle interface {
	RevokeAndDrainRecoveryPoint(context.Context, backupasset.SourceLifecycleRequest) error
}

type CatalogSourceLifecycle interface {
	RetireRecoveryPoint(context.Context, backupasset.SourceLifecycleRequest) error
}

type SearchSourceLifecycle interface {
	RevokeRecoveryPoint(context.Context, backupasset.SourceLifecycleRequest) error
}

type ProcessingSourceLifecycle interface {
	RevokeRecoveryPoint(context.Context, backupasset.SourceLifecycleRequest) error
}

type ExportSourceLifecycle interface {
	ExpireRecoveryPoint(context.Context, backupasset.SourceLifecycleRequest) error
}

type RecoverySourceLifecycle interface {
	CancelRecoveryPointInterests(context.Context, backupasset.SourceLifecycleRequest) error
}

type OverlaySourceLifecycle interface {
	ReconcileSourceLifecycle(
		context.Context,
		backupasset.SourceLifecycleRequest,
		overlay.SourceLifecycle,
		int,
	) (overlay.LifecycleResult, error)
}

type RetentionLifecycleDependencies struct {
	Content    ContentSourceLifecycle
	Catalog    CatalogSourceLifecycle
	Search     SearchSourceLifecycle
	Processing ProcessingSourceLifecycle
	Export     ExportSourceLifecycle
	Recovery   RecoverySourceLifecycle
	Overlay    OverlaySourceLifecycle

	OverlayBatchSize int
	OverlayMaxPasses int
}

// RetentionLifecycle is a pure adapter over owner-scoped ports. Runtime
// startup/run/shutdown composition intentionally remains outside this type.
type RetentionLifecycle struct {
	content    ContentSourceLifecycle
	catalog    CatalogSourceLifecycle
	search     SearchSourceLifecycle
	processing ProcessingSourceLifecycle
	export     ExportSourceLifecycle
	recovery   RecoverySourceLifecycle
	overlay    OverlaySourceLifecycle

	overlayBatchSize int
	overlayMaxPasses int
}

var (
	_ retention.LifecycleAdmission           = (*RetentionLifecycle)(nil)
	_ retention.LifecycleCleanup             = (*RetentionLifecycle)(nil)
	_ processing.SearchSourceRevocationProof = (*ProcessingSearchRevocationProof)(nil)
)

// ProcessingSearchRevocationProof is the concrete runtime bridge from
// Processing cleanup to Search's exact zero-result proof. Constructing the
// bridge does not install it into the process Runtime graph.
type ProcessingSearchRevocationProof struct {
	search *assetsearch.SourceLifecycle
}

func NewProcessingSearchRevocationProof(search *assetsearch.SourceLifecycle) (*ProcessingSearchRevocationProof, error) {
	if search == nil {
		return nil, fmt.Errorf("%w: Search source lifecycle proof is unavailable", backupasset.ErrInvalidState)
	}
	return &ProcessingSearchRevocationProof{search: search}, nil
}

func (proof *ProcessingSearchRevocationProof) ProveRecoveryPointRevoked(
	ctx context.Context,
	request backupasset.SourceLifecycleRequest,
) error {
	if proof == nil || proof.search == nil {
		return fmt.Errorf("%w: Search source lifecycle proof is unavailable", backupasset.ErrInvalidState)
	}
	return proof.search.ProveRecoveryPointRevoked(ctx, request)
}

func NewRetentionLifecycle(dependencies RetentionLifecycleDependencies) (*RetentionLifecycle, error) {
	if nilLifecyclePort(dependencies.Content) || nilLifecyclePort(dependencies.Catalog) ||
		nilLifecyclePort(dependencies.Search) || nilLifecyclePort(dependencies.Processing) ||
		nilLifecyclePort(dependencies.Export) || nilLifecyclePort(dependencies.Recovery) ||
		nilLifecyclePort(dependencies.Overlay) ||
		dependencies.OverlayBatchSize <= 0 || dependencies.OverlayBatchSize > 100000 ||
		dependencies.OverlayMaxPasses <= 0 || dependencies.OverlayMaxPasses > 100000 {
		return nil, fmt.Errorf("%w: invalid retention lifecycle aggregate dependencies", backupasset.ErrInvalidState)
	}
	return &RetentionLifecycle{
		content: dependencies.Content, catalog: dependencies.Catalog, search: dependencies.Search,
		processing: dependencies.Processing, export: dependencies.Export, recovery: dependencies.Recovery,
		overlay: dependencies.Overlay, overlayBatchSize: dependencies.OverlayBatchSize,
		overlayMaxPasses: dependencies.OverlayMaxPasses,
	}, nil
}

func (lifecycle *RetentionLifecycle) RevokeRecoveryPoint(
	ctx context.Context,
	request retention.LifecyclePointRequest,
) error {
	ownerRequest, err := lifecycle.ownerRequest(request, backupasset.SourceLifecyclePrepare)
	if err != nil {
		return err
	}
	return lifecycle.runOwners(ctx, ownerRequest)
}

func (lifecycle *RetentionLifecycle) CleanupRecoveryPoint(
	ctx context.Context,
	request retention.LifecyclePointRequest,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ownerRequest, err := lifecycle.ownerRequest(request, backupasset.SourceLifecycleCleanup)
	if err != nil {
		return err
	}
	if err := lifecycle.runOwners(ctx, ownerRequest); err != nil {
		return err
	}
	reason, err := overlayReason(request.Operation)
	if err != nil {
		return err
	}
	for pass := 0; pass < lifecycle.overlayMaxPasses; pass++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := lifecycle.overlay.ReconcileSourceLifecycle(ctx, ownerRequest, overlay.SourceLifecycle{
			RecoveryPointID: request.RecoveryPointID, Reason: reason,
		}, lifecycle.overlayBatchSize)
		if err != nil {
			return fmt.Errorf("clean Overlay source lifecycle: %w", err)
		}
		if result == (overlay.LifecycleResult{}) {
			return nil
		}
	}
	return fmt.Errorf("%w: Overlay lifecycle zero-result completion is unproven", backupasset.ErrConflict)
}

func (lifecycle *RetentionLifecycle) ownerRequest(
	request retention.LifecyclePointRequest,
	stage backupasset.SourceLifecycleStage,
) (backupasset.SourceLifecycleRequest, error) {
	if lifecycle == nil {
		return backupasset.SourceLifecycleRequest{}, fmt.Errorf("%w: retention lifecycle aggregate is unavailable", backupasset.ErrInvalidState)
	}
	ownerRequest := backupasset.SourceLifecycleRequest{
		RecoveryPointID: request.RecoveryPointID, LifecycleAttemptID: request.AttemptID,
		Operation: request.Operation, Stage: stage,
	}
	if err := backupasset.ValidateSourceLifecycleRequest(ownerRequest); err != nil {
		return backupasset.SourceLifecycleRequest{}, err
	}
	return ownerRequest, nil
}

func (lifecycle *RetentionLifecycle) runOwners(
	ctx context.Context,
	request backupasset.SourceLifecycleRequest,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	steps := []struct {
		name string
		run  func(context.Context, backupasset.SourceLifecycleRequest) error
	}{
		{name: "Content", run: lifecycle.content.RevokeAndDrainRecoveryPoint},
		{name: "Catalog", run: lifecycle.catalog.RetireRecoveryPoint},
		{name: "Search", run: lifecycle.search.RevokeRecoveryPoint},
		{name: "Processing", run: lifecycle.processing.RevokeRecoveryPoint},
		{name: "Export", run: lifecycle.export.ExpireRecoveryPoint},
		{name: "Recovery", run: lifecycle.recovery.CancelRecoveryPointInterests},
	}
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step.run(ctx, request); err != nil {
			return fmt.Errorf("%s source lifecycle: %w", step.name, err)
		}
	}
	return nil
}

func overlayReason(operation backupasset.LifecycleOperation) (overlay.SourceReason, error) {
	switch operation {
	case backupasset.LifecycleMutableRetire:
		return overlay.SourceRetired, nil
	case backupasset.LifecycleRetentionExpire, backupasset.LifecycleExplicitPurge:
		return overlay.SourceExpiring, nil
	default:
		return "", fmt.Errorf("%w: invalid Overlay lifecycle operation", backupasset.ErrInvalidState)
	}
}

func nilLifecyclePort(port any) bool {
	if port == nil {
		return true
	}
	value := reflect.ValueOf(port)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

type retentionRuntimeInput struct {
	DB             *gorm.DB
	Foundation     *backupasset.FoundationService
	Now            func() time.Time
	Lease          *backupasset.LeaseService
	Registry       *provider.Registry
	Repository     *repository.Service
	CatalogIndexer *catalog.Indexer
	SearchIndexer  *assetsearch.Indexer
	Overlay        *overlay.Service
	ContentBroker  *content.Broker
	ContentManager *managedContentRuntime
	Processing     *managedProcessingRuntime
	Export         exportRuntimeManager
	Recovery       recoveryRuntimeManager
	AuditWriter    *backupasset.AuditWriter
	OverlayBatch   int
	OverlayPasses  int
	OwnerBatch     int
	Metrics        retention.Metrics
}

func composeRetentionRuntime(input retentionRuntimeInput) (*retention.Worker, *retention.PolicyService, *retention.HoldService, *retention.PurgeService, *retention.ManagedTaskRetentionFacade, error) {
	if input.DB == nil || input.Foundation == nil || input.Lease == nil || input.Registry == nil ||
		input.Repository == nil || input.CatalogIndexer == nil || input.SearchIndexer == nil ||
		input.Overlay == nil || input.ContentBroker == nil || input.Metrics == nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("%w: retention runtime composition is unavailable", backupasset.ErrInvalidState)
	}
	if input.Now == nil {
		input.Now = func() time.Time { return time.Now().UTC() }
	}
	if input.OwnerBatch <= 0 || input.OwnerBatch > 1000 {
		input.OwnerBatch = 100
	}
	if input.OverlayBatch <= 0 || input.OverlayBatch > 100000 {
		input.OverlayBatch = 200
	}
	if input.OverlayPasses <= 0 || input.OverlayPasses > 100000 {
		input.OverlayPasses = 32
	}
	catalogOwner, err := catalog.NewSourceLifecycle(input.DB, input.CatalogIndexer, input.Now, input.OwnerBatch)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	searchOwner, err := assetsearch.NewSourceLifecycle(input.DB, input.SearchIndexer, input.Now, input.OwnerBatch)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	lifecycle, err := NewRetentionLifecycle(RetentionLifecycleDependencies{
		Content: &runtimeContentSourceLifecycle{
			db: input.DB, broker: input.ContentBroker, manager: input.ContentManager, now: input.Now, batchSize: input.OwnerBatch,
		},
		Catalog: catalogOwner,
		Search:  searchOwner,
		Processing: &runtimeProcessingSourceLifecycle{
			db: input.DB, processing: input.Processing, search: searchOwner, batchSize: input.OwnerBatch,
		},
		Export: &runtimeExportSourceLifecycle{
			db: input.DB, manager: input.Export, now: input.Now, batchSize: input.OwnerBatch,
		},
		Recovery: &runtimeRecoverySourceLifecycle{
			db: input.DB, manager: input.Recovery, batchSize: input.OwnerBatch,
		},
		Overlay:          input.Overlay,
		OverlayBatchSize: input.OverlayBatch,
		OverlayMaxPasses: input.OverlayPasses,
	})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	var settledAudit retention.AssetAuditSink
	var mutationAudit retention.MutationAuditor
	if input.AuditWriter != nil {
		adapter := retentionAssetAuditAdapter{writer: input.AuditWriter}
		settledAudit = adapter
		mutationAudit = adapter
	}
	holds, err := retention.NewHoldService(retention.HoldServiceDependencies{DB: input.DB, Now: input.Now, Audit: mutationAudit})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	policies, err := retention.NewPolicyService(retention.PolicyServiceDependencies{DB: input.DB, Now: input.Now, Audit: mutationAudit})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	deleter, err := retention.NewRegistryPointDeletion(input.DB, input.Registry, &runtimePointDeletionResolver{repository: input.Repository})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	deleter.SetNow(input.Now)
	coordinator, err := retention.NewCoordinator(retention.CoordinatorDependencies{
		DB: input.DB, Leases: input.Lease, Holds: holds, Now: input.Now,
		LeaseOwnerID: retention.RetentionWorkerLeaseOwnerID, Admissions: lifecycle, Cleanup: lifecycle,
		Deleter: deleter, RetryDelay: time.Minute, Audit: settledAudit,
	})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	purge, err := retention.NewPurgeService(coordinator)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	audit, err := retention.NewAuditRetention(retention.AuditRetentionDependencies{
		DB: input.DB, Writer: input.AuditWriter, Now: input.Now,
		Config: func() (retention.AuditRetentionConfig, error) {
			detailDays, checkpointDays, configErr := input.Foundation.AuditRetentionConfig()
			if configErr != nil {
				return retention.AuditRetentionConfig{}, configErr
			}
			return retention.AuditRetentionConfig{DetailRetentionDays: detailDays, CheckpointRetentionDays: checkpointDays}, nil
		},
	})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	worker, err := retention.NewWorker(retention.WorkerDependencies{
		Foundation: input.Foundation, Coordinator: coordinator, Policies: policies, Holds: holds,
		Audit: audit, ImportRebuild: &runtimeImportRebuildReconciler{repository: input.Repository},
		Metrics: input.Metrics, Now: input.Now,
	})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	facade, err := retention.NewManagedTaskRetentionFacade(retention.ManagedTaskRetentionFacadeDependencies{
		DB: input.DB, Policies: policies, Coordinator: coordinator, Now: input.Now,
	})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return worker, policies, holds, purge, facade, nil
}

func retentionOwnerBatch(foundation *backupasset.FoundationService) int {
	if foundation != nil {
		if config, err := foundation.RetentionConfig(); err == nil && config.BatchSize > 0 {
			return config.BatchSize
		}
	}
	return 100
}

func retentionRuntimeMetrics(publicationMetrics publication.Metrics) retention.Metrics {
	if publicationMetrics == nil {
		metrics, err := retention.NewPrometheusMetrics(prometheus.DefaultRegisterer)
		if err == nil {
			return metrics
		}
	}
	return retention.NoopMetrics{}
}

type runtimePointDeletionResolver struct {
	repository *repository.Service
}

func (resolver *runtimePointDeletionResolver) ResolveDeletePoint(
	ctx context.Context,
	tx *gorm.DB,
	request retention.LifecyclePointRequest,
	point model.RecoveryPoint,
	repositoryRow model.BackupRepository,
) (provider.DeletePointRequest, error) {
	if resolver == nil || resolver.repository == nil {
		return provider.DeletePointRequest{}, fmt.Errorf("%w: point deletion resolver is unavailable", backupasset.ErrInvalidState)
	}
	return resolver.repository.ResolveLifecycleDeletePointTx(ctx, tx, request.AttemptID, point, repositoryRow)
}

type runtimeImportRebuildReconciler struct {
	repository *repository.Service
}

func (reconciler *runtimeImportRebuildReconciler) ReconcileImports(ctx context.Context, limit int) (int, error) {
	if reconciler == nil || reconciler.repository == nil {
		return 0, fmt.Errorf("%w: import reconciliation is unavailable", backupasset.ErrInvalidState)
	}
	return reconciler.repository.ReconcileImports(ctx, limit)
}

func (reconciler *runtimeImportRebuildReconciler) ReconcileRebuilds(ctx context.Context, limit int) (int, error) {
	if reconciler == nil || reconciler.repository == nil {
		return 0, fmt.Errorf("%w: rebuild reconciliation is unavailable", backupasset.ErrInvalidState)
	}
	return reconciler.repository.ReconcileRebuilds(ctx, limit)
}

type runtimeContentSourceLifecycle struct {
	db        *gorm.DB
	broker    *content.Broker
	manager   *managedContentRuntime
	now       func() time.Time
	batchSize int
}

func (owner *runtimeContentSourceLifecycle) RevokeAndDrainRecoveryPoint(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	if owner == nil || owner.db == nil || owner.broker == nil {
		return fmt.Errorf("%w: Content source lifecycle is unavailable", backupasset.ErrInvalidState)
	}
	cache := owner.liveCache()
	if cache != nil {
		lifecycle, err := content.NewSourceLifecycle(owner.db, owner.broker, cache, owner.now, owner.batchSize)
		if err != nil {
			return err
		}
		return lifecycle.RevokeAndDrainRecoveryPoint(ctx, request)
	}
	return proveNoOutstandingContent(ctx, owner.db, request.RecoveryPointID)
}

func (owner *runtimeContentSourceLifecycle) liveCache() *content.AuthenticatedCache {
	if owner == nil || owner.manager == nil {
		return nil
	}
	owner.manager.mu.Lock()
	defer owner.manager.mu.Unlock()
	return owner.manager.cache
}

type runtimeProcessingSourceLifecycle struct {
	db         *gorm.DB
	processing *managedProcessingRuntime
	search     *assetsearch.SourceLifecycle
	batchSize  int
}

func (owner *runtimeProcessingSourceLifecycle) RevokeRecoveryPoint(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	if owner == nil || owner.db == nil {
		return fmt.Errorf("%w: Processing source lifecycle is unavailable", backupasset.ErrInvalidState)
	}
	if owner.processing != nil && owner.search != nil {
		proof, err := NewProcessingSearchRevocationProof(owner.search)
		if err == nil {
			lifecycle, lifecycleErr := owner.processing.sourceLifecycle(proof, owner.batchSize)
			if lifecycleErr == nil {
				return lifecycle.RevokeRecoveryPoint(ctx, request)
			}
		}
	}
	return proveNoOutstandingProcessingSource(ctx, owner.db, request.RecoveryPointID)
}

type runtimeExportSourceLifecycle struct {
	db        *gorm.DB
	manager   exportRuntimeManager
	now       func() time.Time
	batchSize int
}

func (owner *runtimeExportSourceLifecycle) ExpireRecoveryPoint(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	if owner == nil || owner.db == nil {
		return fmt.Errorf("%w: Export source lifecycle is unavailable", backupasset.ErrInvalidState)
	}
	if lifecycle := liveExportLifecycle(owner.manager); lifecycle != nil {
		source, err := assetexport.NewSourceLifecycle(owner.db, lifecycle, owner.now, owner.batchSize)
		if err != nil {
			return err
		}
		return source.ExpireRecoveryPoint(ctx, request)
	}
	return proveNoOutstandingExportSource(ctx, owner.db, request.RecoveryPointID)
}

type runtimeRecoverySourceLifecycle struct {
	db        *gorm.DB
	manager   recoveryRuntimeManager
	batchSize int
}

func (owner *runtimeRecoverySourceLifecycle) CancelRecoveryPointInterests(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	if owner == nil || owner.db == nil {
		return fmt.Errorf("%w: Recovery source lifecycle is unavailable", backupasset.ErrInvalidState)
	}
	if canceler := liveRecoveryCanceler(owner.manager); canceler != nil {
		source, err := recovery.NewSourceLifecycle(owner.db, canceler, owner.batchSize)
		if err != nil {
			return err
		}
		return source.CancelRecoveryPointInterests(ctx, request)
	}
	return proveNoOutstandingRecoverySource(ctx, owner.db, request.RecoveryPointID)
}

func liveExportLifecycle(manager exportRuntimeManager) *assetexport.Lifecycle {
	runtime, ok := manager.(*managedExportRuntime)
	if !ok || runtime == nil {
		return nil
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.graph == nil {
		return nil
	}
	return runtime.graph.lifecycle
}

func liveRecoveryCanceler(manager recoveryRuntimeManager) recovery.RecoverySourceJobCanceler {
	runtime, ok := manager.(*managedRecoveryRuntime)
	if !ok || runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.graph == nil {
		return nil
	}
	return runtime.graph.workerCoordinator
}

func proveNoOutstandingContent(ctx context.Context, db *gorm.DB, recoveryPointID string) error {
	if db == nil || !db.Migrator().HasTable(&model.BackupAssetDeliveryGrant{}) {
		return fmt.Errorf("%w: Content source lifecycle is unproven", backupasset.ErrConflict)
	}
	var count int64
	if err := db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
		Where("recovery_point_id = ? AND state IN ?", recoveryPointID, []string{
			string(content.DeliveryIssued), string(content.DeliveryActive), string(content.DeliveryDraining),
		}).Count(&count).Error; err != nil {
		return fmt.Errorf("prove Content source settled: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("%w: Content source lifecycle is unproven", backupasset.ErrConflict)
	}
	return nil
}

func proveNoOutstandingProcessingSource(ctx context.Context, db *gorm.DB, recoveryPointID string) error {
	if db == nil || !db.Migrator().HasTable(&model.BackupAssetProcessingJob{}) {
		return fmt.Errorf("%w: Processing source lifecycle is unproven", backupasset.ErrConflict)
	}
	var count int64
	if err := db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Where("recovery_point_id = ? AND (is_current = ? OR current_attempt_id IS NOT NULL)", recoveryPointID, true).
		Limit(1).Count(&count).Error; err != nil {
		return fmt.Errorf("prove Processing source settled: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("%w: Processing source lifecycle is unproven", backupasset.ErrConflict)
	}
	return nil
}

func proveNoOutstandingExportSource(ctx context.Context, db *gorm.DB, recoveryPointID string) error {
	if db == nil || !db.Migrator().HasTable(&model.BackupAssetExportJob{}) ||
		!db.Migrator().HasTable(&model.BackupAssetExportItem{}) {
		return fmt.Errorf("%w: Export source lifecycle is unproven", backupasset.ErrConflict)
	}
	var count int64
	if err := db.WithContext(ctx).Table("backup_asset_export_jobs AS jobs").
		Where(`jobs.execution_state IN ? AND EXISTS (
			SELECT 1 FROM backup_asset_export_items AS items
			WHERE items.job_id = jobs.id AND items.recovery_point_id = ?
		)`, []string{
			string(assetexport.ExecutionQueued), string(assetexport.ExecutionRunning),
			string(assetexport.ExecutionRetryWait), string(assetexport.ExecutionSealing),
			string(assetexport.ExecutionReady),
		}, recoveryPointID).
		Limit(1).Count(&count).Error; err != nil {
		return fmt.Errorf("prove Export source settled: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("%w: Export source lifecycle is unproven", backupasset.ErrConflict)
	}
	return nil
}

func proveNoOutstandingRecoverySource(ctx context.Context, db *gorm.DB, recoveryPointID string) error {
	if db == nil || !db.Migrator().HasTable(&model.BackupAssetRecoveryJob{}) ||
		!db.Migrator().HasTable(&model.BackupAssetRecoveryPlan{}) {
		return fmt.Errorf("%w: Recovery source lifecycle is unproven", backupasset.ErrConflict)
	}
	var count int64
	if err := db.WithContext(ctx).Table("backup_asset_recovery_jobs AS jobs").
		Joins("JOIN backup_asset_recovery_plans AS plans ON plans.id = jobs.plan_id").
		Where("plans.recovery_point_id = ? AND jobs.state IN ?", recoveryPointID, []string{
			string(recovery.JobStateQueued), string(recovery.JobStateRunning),
			string(recovery.JobStateVerifying), string(recovery.JobStateCancelRequested),
		}).
		Limit(1).Count(&count).Error; err != nil {
		return fmt.Errorf("prove Recovery source settled: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("%w: Recovery source lifecycle is unproven", backupasset.ErrConflict)
	}
	return nil
}

type retentionAssetAuditAdapter struct {
	writer *backupasset.AuditWriter
}

func (adapter retentionAssetAuditAdapter) Write(ctx context.Context, input backupasset.AuditEventInput) error {
	if adapter.writer == nil {
		return nil
	}
	_, err := adapter.writer.Write(ctx, input)
	return err
}

func (adapter retentionAssetAuditAdapter) WriteTx(ctx context.Context, tx *gorm.DB, input backupasset.AuditEventInput) error {
	if adapter.writer == nil {
		return nil
	}
	return adapter.writer.WriteTx(ctx, tx, input)
}
