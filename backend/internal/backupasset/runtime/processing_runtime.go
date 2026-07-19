package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const processingSecurityPolicyRevision = "security-policy-v1"

type processingRuntimeDependencies struct {
	DB               *gorm.DB
	Foundation       *backupasset.FoundationService
	Keyring          *backupasset.Keyring
	Lease            *backupasset.LeaseService
	Source           content.SourceResolver
	ValidateRoot     func(context.Context, string) error
	RevalidateSource processing.ProcessingSourceRevalidator
	Projection       processing.DerivedProjectionPort
	Metrics          processing.Metrics
	Now              func() time.Time
}

type ProcessingWorkerCounts struct {
	Active      int64 `json:"active"`
	Draining    int64 `json:"draining"`
	Degraded    int64 `json:"degraded"`
	Quarantined int64 `json:"quarantined"`
}

type ProcessingSlotSummary struct {
	InteractiveUsed  int64 `json:"interactive_used"`
	InteractiveTotal int   `json:"interactive_total"`
	BackgroundUsed   int64 `json:"background_used"`
	BackgroundTotal  int   `json:"background_total"`
}

type ProcessingQueueSummary struct {
	Total               int64            `json:"total"`
	ByState             map[string]int64 `json:"by_state"`
	ByPriority          map[string]int64 `json:"by_priority"`
	OldestQueuedSeconds int64            `json:"oldest_queued_seconds"`
}

type ProcessingOutcomeSummary struct {
	ByErrorCategory map[string]int64 `json:"by_error_category"`
}

type ProcessingDerivedSummary struct {
	ByState       map[string]int64 `json:"by_state"`
	LogicalBytes  int64            `json:"logical_bytes"`
	PhysicalBytes int64            `json:"physical_bytes"`
	OrphanBytes   int64            `json:"orphan_bytes"`
	QuotaBytes    int64            `json:"quota_bytes"`
}

// ProcessingAdminSummary deliberately contains only bounded aggregates. It is
// safe for the internal Admin adapter and must never grow identity, source,
// path, grant, fence, certificate, or raw-error fields.
type ProcessingAdminSummary struct {
	SchemaVersion int                      `json:"schema_version"`
	Configured    bool                     `json:"configured"`
	LocalEnabled  bool                     `json:"local_enabled"`
	RemoteEnabled bool                     `json:"remote_enabled"`
	Workers       ProcessingWorkerCounts   `json:"worker_counts"`
	Slots         ProcessingSlotSummary    `json:"slots"`
	Queue         ProcessingQueueSummary   `json:"queue"`
	Outcomes      ProcessingOutcomeSummary `json:"outcomes"`
	Derived       ProcessingDerivedSummary `json:"derived"`
	ReconciledAt  *time.Time               `json:"reconciled_at"`
}

type managedProcessingRuntime struct {
	db               *gorm.DB
	foundation       *backupasset.FoundationService
	keyring          *backupasset.Keyring
	lease            *backupasset.LeaseService
	source           content.SourceResolver
	validateRoot     func(context.Context, string) error
	revalidateSource processing.ProcessingSourceRevalidator
	projection       processing.DerivedProjectionPort
	metrics          processing.Metrics
	now              func() time.Time

	coordinator       *processing.Coordinator
	grants            *processing.GrantService
	attemptBroker     *content.AttemptBroker
	store             *processing.DerivedStore
	lifecycle         *processing.DerivedLifecycle
	sink              *processing.ArtifactSink
	reconciler        *processing.Reconciler
	derivedReconciler *processing.DerivedReconciler
	protocol          *processing.ProtocolService
	workerProtocol    *processing.WorkerProtocolService

	mu             sync.RWMutex
	config         backupasset.ProcessingConfig
	lastReconciled *time.Time
	runCancel      context.CancelFunc
	runDone        chan struct{}
	ready          atomic.Bool
	stopped        atomic.Bool
}

func newProcessingRuntime(dependencies processingRuntimeDependencies) (*managedProcessingRuntime, error) {
	if dependencies.DB == nil || dependencies.Foundation == nil || dependencies.Keyring == nil || dependencies.Lease == nil ||
		dependencies.Source == nil || dependencies.ValidateRoot == nil || dependencies.RevalidateSource == nil || dependencies.Projection == nil {
		return nil, fmt.Errorf("%w: Processing runtime dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.Metrics == nil {
		dependencies.Metrics = processing.NoopMetrics{}
	}
	return &managedProcessingRuntime{
		db: dependencies.DB, foundation: dependencies.Foundation, keyring: dependencies.Keyring,
		lease: dependencies.Lease, source: dependencies.Source, validateRoot: dependencies.ValidateRoot,
		revalidateSource: dependencies.RevalidateSource, projection: dependencies.Projection, metrics: dependencies.Metrics, now: dependencies.Now,
	}, nil
}

func (runtime *managedProcessingRuntime) Startup(ctx context.Context) error {
	if runtime == nil || runtime.foundation == nil {
		return fmt.Errorf("%w: Processing runtime unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := runtime.foundation.ProcessingConfig()
	if err != nil {
		return err
	}
	runtime.mu.Lock()
	runtime.config = config
	runtime.mu.Unlock()
	if !config.Enabled || (!config.LocalWorker.Enabled && !config.RemoteWorker.Enabled) {
		runtime.ready.Store(false)
		return nil
	}
	if err := runtime.initialize(ctx, config); err != nil {
		runtime.ready.Store(false)
		return err
	}
	if err := runtime.reconcile(ctx); err != nil {
		runtime.ready.Store(false)
		return err
	}
	runtime.ready.Store(true)
	return nil
}

func (runtime *managedProcessingRuntime) initialize(ctx context.Context, config backupasset.ProcessingConfig) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.workerProtocol != nil {
		return nil
	}
	if err := runtime.validateRoot(ctx, config.DerivedStore.Root); err != nil {
		return fmt.Errorf("validate Derived Store root: %w", err)
	}
	if _, err := runtime.keyring.RewrapDomains(ctx, backupasset.KeyDomainDerivedStore); err != nil {
		return runtime.invalidateUnreadableDerivedKey(ctx, config, err)
	}
	if _, err := runtime.keyring.Ensure(ctx, backupasset.KeyDomainDerivedStore); err != nil {
		if errors.Is(err, backupasset.ErrKeyLost) || errors.Is(err, backupasset.ErrKeyUnavailable) {
			return runtime.invalidateUnreadableDerivedKey(ctx, config, err)
		}
		return fmt.Errorf("ensure Derived Store key: %w", err)
	}
	coordinator, err := processing.NewCoordinator(runtime.db, runtime.lease, runtime.now, processing.CoordinatorConfig{
		QueueMax: config.QueueMax, InteractiveReservedSlots: config.InteractiveSlots,
		BackgroundSlots: config.BackgroundSlots, PullLease: config.PullLease,
		AttemptTimeout: config.AttemptTimeout, RetryMax: config.RetryMax,
	})
	if err != nil {
		return err
	}
	grants, err := processing.NewGrantService(runtime.db, runtime.lease, runtime.now, processing.GrantConfig{
		TTL: config.AttemptTimeout,
		InputLimits: processing.GrantLimits{
			MaxRequests: config.Input.MaxRequests, MaxBytesPerRequest: config.Input.RequestMaxBytes,
			MaxCumulativeBytes: config.Input.CumulativeMaxBytes, MaxInFlight: config.Input.MaxInFlight,
		},
		SinkLimits: processing.GrantLimits{
			MaxRequests: int64(config.Sink.MaxArtifacts), MaxBytesPerRequest: config.Sink.ArtifactMaxBytes,
			MaxCumulativeBytes: config.Sink.TotalMaxBytes, MaxInFlight: int64(config.Sink.MaxArtifacts),
		},
	})
	if err != nil {
		return err
	}
	attemptBroker, err := content.NewAttemptBroker(runtime.source, grants, runtime.now)
	if err != nil {
		return err
	}
	store, err := processing.NewDerivedStore(ctx, runtime.db, runtime.keyring, processing.DerivedStoreConfig{
		Root: config.DerivedStore.Root, ChunkSize: config.DerivedStore.ChunkBytes,
		BlobMaxBytes: config.DerivedStore.BlobMaxBytes, GlobalMaxBytes: config.DerivedStore.GlobalMaxBytes,
		ValidateRoot: runtime.validateRoot,
	}, runtime.now)
	if err != nil {
		return err
	}
	lifecycle, err := processing.NewDerivedLifecycle(runtime.db, store, runtime.projection, runtime.now)
	if err != nil {
		return err
	}
	sink, err := processing.NewArtifactSink(
		runtime.db, runtime.lease, grants, store, lifecycle, runtime.revalidateSource,
		func(context.Context) (string, error) { return processingSecurityPolicyRevision, nil },
		runtime.now, processing.ArtifactSinkConfig{
			MaxArtifacts: config.Sink.MaxArtifacts, MaxArtifactBytes: config.Sink.ArtifactMaxBytes,
			MaxTotalBytes: config.Sink.TotalMaxBytes,
		},
	)
	if err != nil {
		return err
	}
	sink.SetMetrics(runtime.metrics)
	reconciler, err := processing.NewReconciler(coordinator, grants, runtime.now, processing.ReconcilerConfig{
		BatchSize: config.DerivedStore.ReconcileBatchSize, RetryBase: config.RetryBase,
	})
	if err != nil {
		return err
	}
	derivedReconciler, err := processing.NewDerivedReconciler(store, lifecycle, config.DerivedStore.ReconcileBatchSize)
	if err != nil {
		return err
	}
	registry, err := processing.NewCapabilityRegistry(nil)
	if err != nil {
		return err
	}
	protocol, err := processing.NewProtocolService(runtime.db, registry, runtime.now)
	if err != nil {
		return err
	}
	workerProtocol, err := processing.NewWorkerProtocolService(protocol, coordinator, grants, attemptBroker, sink)
	if err != nil {
		return err
	}
	runtime.coordinator = coordinator
	runtime.grants = grants
	runtime.attemptBroker = attemptBroker
	runtime.store = store
	runtime.lifecycle = lifecycle
	runtime.sink = sink
	runtime.reconciler = reconciler
	runtime.derivedReconciler = derivedReconciler
	runtime.protocol = protocol
	runtime.workerProtocol = workerProtocol
	return nil
}

func (runtime *managedProcessingRuntime) invalidateUnreadableDerivedKey(
	ctx context.Context,
	config backupasset.ProcessingConfig,
	cause error,
) error {
	var key model.WrappedDomainKey
	result := runtime.db.WithContext(ctx).
		Where("domain = ? AND state IN ?", backupasset.KeyDomainDerivedStore, []string{
			string(backupasset.DomainKeyActive), string(backupasset.DomainKeyLost),
		}).
		Order("version DESC").Limit(1).Find(&key)
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, result.Error)
	}
	store, err := processing.NewDerivedStore(ctx, runtime.db, runtime.keyring, processing.DerivedStoreConfig{
		Root: config.DerivedStore.Root, ChunkSize: config.DerivedStore.ChunkBytes,
		BlobMaxBytes: config.DerivedStore.BlobMaxBytes, GlobalMaxBytes: config.DerivedStore.GlobalMaxBytes,
		ValidateRoot: runtime.validateRoot,
	}, runtime.now)
	if err != nil {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, err)
	}
	lifecycle, err := processing.NewDerivedLifecycle(runtime.db, store, runtime.projection, runtime.now)
	if err != nil {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, err)
	}
	if err := lifecycle.MarkActiveKeyLost(ctx, key.Version, config.DerivedStore.ReconcileBatchSize); err != nil {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, err)
	}
	derivedReconciler, err := processing.NewDerivedReconciler(store, lifecycle, config.DerivedStore.ReconcileBatchSize)
	if err != nil {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, err)
	}
	if _, err := derivedReconciler.Reconcile(ctx); err != nil {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, err)
	}
	runtime.store = store
	runtime.lifecycle = lifecycle
	runtime.derivedReconciler = derivedReconciler
	return errors.Join(processing.ErrDerivedStoreUnavailable, cause)
}

func (runtime *managedProcessingRuntime) WorkerProtocol() *processing.WorkerProtocolService {
	if runtime == nil || !runtime.ready.Load() || runtime.stopped.Load() {
		return nil
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.workerProtocol
}

func (runtime *managedProcessingRuntime) ProcessingConfig() (backupasset.ProcessingConfig, error) {
	if runtime == nil || runtime.foundation == nil {
		return backupasset.ProcessingConfig{}, fmt.Errorf("%w: Processing config unavailable", backupasset.ErrInvalidState)
	}
	return runtime.foundation.ProcessingConfig()
}

func (runtime *managedProcessingRuntime) AdminSummary(ctx context.Context) (ProcessingAdminSummary, error) {
	if runtime == nil || runtime.db == nil {
		return ProcessingAdminSummary{}, fmt.Errorf("%w: Processing summary unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.mu.RLock()
	config := runtime.config
	lastReconciled := cloneProcessingTime(runtime.lastReconciled)
	runtime.mu.RUnlock()
	if config.QueueMax == 0 {
		var err error
		config, err = runtime.foundation.ProcessingConfig()
		if err != nil {
			return ProcessingAdminSummary{}, err
		}
	}
	summary := ProcessingAdminSummary{
		SchemaVersion: 1,
		Configured:    config.LocalWorker.Enabled || config.RemoteWorker.Enabled,
		LocalEnabled:  config.LocalWorker.Enabled, RemoteEnabled: config.RemoteWorker.Enabled,
		Slots:        ProcessingSlotSummary{InteractiveTotal: config.InteractiveSlots, BackgroundTotal: config.BackgroundSlots},
		Queue:        ProcessingQueueSummary{ByState: make(map[string]int64), ByPriority: make(map[string]int64)},
		Outcomes:     ProcessingOutcomeSummary{ByErrorCategory: make(map[string]int64)},
		Derived:      ProcessingDerivedSummary{ByState: make(map[string]int64), QuotaBytes: config.DerivedStore.GlobalMaxBytes},
		ReconciledAt: lastReconciled,
	}
	if !summary.Configured || !config.Enabled {
		return summary, nil
	}
	if err := runtime.loadWorkerSummary(ctx, &summary); err != nil {
		return ProcessingAdminSummary{}, err
	}
	if err := runtime.loadJobSummary(ctx, &summary); err != nil {
		return ProcessingAdminSummary{}, err
	}
	if err := runtime.loadDerivedSummary(ctx, &summary); err != nil {
		return ProcessingAdminSummary{}, err
	}
	return summary, nil
}

func (runtime *managedProcessingRuntime) loadWorkerSummary(ctx context.Context, summary *ProcessingAdminSummary) error {
	var rows []struct {
		TrustState  string
		HealthState string
		Count       int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetWorkerIdentity{}).
		Select("trust_state, health_state, count(*) AS count").Group("trust_state, health_state").Scan(&rows).Error; err != nil {
		return fmt.Errorf("load Processing Worker aggregates: %w", err)
	}
	for _, row := range rows {
		switch {
		case row.TrustState == "quarantined":
			summary.Workers.Quarantined += row.Count
		case row.HealthState == "draining":
			summary.Workers.Draining += row.Count
		case row.HealthState == "ready":
			summary.Workers.Active += row.Count
		default:
			summary.Workers.Degraded += row.Count
		}
	}
	var slots []struct {
		SlotClass string
		Count     int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingAttempt{}).
		Select("slot_class, count(*) AS count").Where("state = ? AND is_current = ?", "active", true).
		Group("slot_class").Scan(&slots).Error; err != nil {
		return fmt.Errorf("load Processing slot aggregates: %w", err)
	}
	for _, row := range slots {
		switch processing.SlotClass(row.SlotClass) {
		case processing.SlotInteractive:
			summary.Slots.InteractiveUsed += row.Count
		case processing.SlotBackground, processing.SlotBackgroundBorrowed:
			summary.Slots.BackgroundUsed += row.Count
		}
	}
	return nil
}

func (runtime *managedProcessingRuntime) loadJobSummary(ctx context.Context, summary *ProcessingAdminSummary) error {
	var states []struct {
		State         string
		PriorityClass string
		Count         int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Select("state, priority_class, count(*) AS count").Where("is_current = ?", true).
		Group("state, priority_class").Scan(&states).Error; err != nil {
		return fmt.Errorf("load Processing queue aggregates: %w", err)
	}
	for _, row := range states {
		summary.Queue.Total += row.Count
		summary.Queue.ByState[row.State] += row.Count
		summary.Queue.ByPriority[row.PriorityClass] += row.Count
	}
	var oldest model.BackupAssetProcessingJob
	result := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Select("queued_at").Where("state = ? AND is_current = ?", processing.ProcessingQueued, true).
		Order("queued_at ASC").Limit(1).Find(&oldest)
	if result.Error != nil {
		return fmt.Errorf("load oldest Processing queue age: %w", result.Error)
	}
	if result.RowsAffected == 1 && !oldest.QueuedAt.IsZero() {
		age := runtime.now().UTC().Sub(oldest.QueuedAt.UTC())
		if age > 0 {
			summary.Queue.OldestQueuedSeconds = int64(age / time.Second)
		}
	}
	var outcomes []struct {
		ErrorCode string
		Count     int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Select("error_code, count(*) AS count").Where("error_code <> ?", "").Group("error_code").Scan(&outcomes).Error; err != nil {
		return fmt.Errorf("load Processing outcome aggregates: %w", err)
	}
	for _, row := range outcomes {
		summary.Outcomes.ByErrorCategory[row.ErrorCode] += row.Count
	}
	return nil
}

func (runtime *managedProcessingRuntime) loadDerivedSummary(ctx context.Context, summary *ProcessingAdminSummary) error {
	var sets []struct {
		State        string
		LogicalBytes int64
		Count        int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetDerivedArtifactSet{}).
		Select("state, coalesce(sum(total_plaintext_bytes), 0) AS logical_bytes, count(*) AS count").
		Group("state").Scan(&sets).Error; err != nil {
		return fmt.Errorf("load Derived set aggregates: %w", err)
	}
	for _, row := range sets {
		summary.Derived.ByState[row.State] += row.Count
		if row.State == "active" || row.State == "stale" {
			summary.Derived.LogicalBytes += row.LogicalBytes
		}
	}
	var blobs []struct {
		State        string
		PhysicalSize int64
		RefCount     int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
		Select("state, physical_size, ref_count").Scan(&blobs).Error; err != nil {
		return fmt.Errorf("load Derived blob aggregates: %w", err)
	}
	for _, row := range blobs {
		if row.State == "active" || row.State == "staged" {
			summary.Derived.PhysicalBytes += row.PhysicalSize
		}
		if row.RefCount == 0 {
			summary.Derived.OrphanBytes += row.PhysicalSize
		}
	}
	return nil
}

func (runtime *managedProcessingRuntime) reconcile(ctx context.Context) error {
	runtime.mu.RLock()
	reconciler := runtime.reconciler
	derivedReconciler := runtime.derivedReconciler
	sink := runtime.sink
	projectionBatchSize := runtime.config.DerivedStore.ReconcileBatchSize
	runtime.mu.RUnlock()
	if reconciler == nil {
		return nil
	}
	if sink != nil {
		if _, err := sink.ReconcilePendingProjections(ctx, projectionBatchSize); err != nil {
			return err
		}
	}
	reconcileResult, err := reconciler.Reconcile(ctx)
	if err != nil {
		return err
	}
	for index := 0; index < reconcileResult.ExpiredAttempts; index++ {
		runtime.metrics.ObserveLeaseLoss()
	}
	if _, err := reconciler.PromoteRetries(ctx); err != nil {
		return err
	}
	if derivedReconciler != nil {
		derivedResult, err := derivedReconciler.Reconcile(ctx)
		if err != nil {
			runtime.metrics.ObserveDerived(processing.DerivedEventReconcileFailure)
			return err
		}
		for index := 0; index < derivedResult.RewrappedBlobs; index++ {
			runtime.metrics.ObserveDerived(processing.DerivedEventRewrapped)
		}
		for index := 0; index < derivedResult.RepairedRefCounts; index++ {
			runtime.metrics.ObserveDerived(processing.DerivedEventRefcountRepaired)
		}
		for index := 0; index < derivedResult.PurgedBlobs; index++ {
			runtime.metrics.ObserveDerived(processing.DerivedEventPurged)
		}
		for index := 0; index < derivedResult.RemovedFileOrphans; index++ {
			runtime.metrics.ObserveDerived(processing.DerivedEventOrphanRemoved)
		}
	}
	runtime.publishMetrics(ctx)
	now := runtime.now().UTC()
	runtime.mu.Lock()
	runtime.lastReconciled = &now
	runtime.mu.Unlock()
	return nil
}

func (runtime *managedProcessingRuntime) publishMetrics(ctx context.Context) {
	if runtime == nil || runtime.metrics == nil {
		return
	}
	for _, trust := range []processing.WorkerTrustClass{processing.WorkerTrustActive, processing.WorkerTrustQuarantined, processing.WorkerTrustRevoked} {
		for _, health := range []processing.WorkerHealthClass{processing.WorkerHealthReady, processing.WorkerHealthDegraded, processing.WorkerHealthDraining} {
			runtime.metrics.SetWorkers(trust, health, 0)
		}
	}
	var workers []struct {
		TrustState  string
		HealthState string
		Count       int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetWorkerIdentity{}).
		Select("trust_state, health_state, count(*) AS count").Group("trust_state, health_state").Scan(&workers).Error; err == nil {
		for _, row := range workers {
			runtime.metrics.SetWorkers(processing.WorkerTrustClass(row.TrustState), processing.WorkerHealthClass(row.HealthState), row.Count)
		}
	}
	for _, priority := range []processing.PriorityClass{processing.PriorityInteractive, processing.PriorityBackground} {
		for _, state := range processing.AllProcessingStates() {
			runtime.metrics.SetQueue(priority, state, 0, 0)
		}
	}
	var jobs []struct {
		State         string
		PriorityClass string
		Count         int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Select("state, priority_class, count(*) AS count").
		Where("is_current = ?", true).Group("state, priority_class").Scan(&jobs).Error; err == nil {
		for _, row := range jobs {
			age := time.Duration(0)
			var oldest model.BackupAssetProcessingJob
			if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
				Select("queued_at").
				Where("state = ? AND priority_class = ? AND is_current = ?", row.State, row.PriorityClass, true).
				Order("queued_at ASC").Limit(1).Take(&oldest).Error; err == nil {
				age = runtime.now().UTC().Sub(oldest.QueuedAt.UTC())
			}
			runtime.metrics.SetQueue(processing.PriorityClass(row.PriorityClass), processing.ProcessingState(row.State), row.Count, age)
		}
	}
	for _, class := range []processing.SlotClass{processing.SlotInteractive, processing.SlotBackground, processing.SlotBackgroundBorrowed} {
		runtime.metrics.SetSlots(class, processing.SlotMetricUsed, 0)
	}
	var attempts []struct {
		SlotClass string
		Count     int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingAttempt{}).
		Select("slot_class, count(*) AS count").Where("state = ? AND is_current = ?", "active", true).
		Group("slot_class").Scan(&attempts).Error; err == nil {
		for _, row := range attempts {
			runtime.metrics.SetSlots(processing.SlotClass(row.SlotClass), processing.SlotMetricUsed, row.Count)
		}
	}
	config := runtime.config
	runtime.metrics.SetSlots(processing.SlotInteractive, processing.SlotMetricTotal, int64(config.InteractiveSlots))
	runtime.metrics.SetSlots(processing.SlotBackground, processing.SlotMetricTotal, int64(config.BackgroundSlots))
	var derived struct {
		LogicalBytes  int64
		PhysicalBytes int64
		OrphanBytes   int64
	}
	_ = runtime.db.WithContext(ctx).Model(&model.BackupAssetDerivedArtifactSet{}).
		Select("coalesce(sum(CASE WHEN state IN ('active','stale') THEN total_plaintext_bytes ELSE 0 END), 0) AS logical_bytes").Scan(&derived).Error
	_ = runtime.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
		Select("coalesce(sum(CASE WHEN state IN ('active','staged') THEN physical_size ELSE 0 END), 0) AS physical_bytes, coalesce(sum(CASE WHEN ref_count = 0 THEN physical_size ELSE 0 END), 0) AS orphan_bytes").Scan(&derived).Error
	runtime.metrics.SetDerived(processing.DerivedMetricLogicalBytes, derived.LogicalBytes)
	runtime.metrics.SetDerived(processing.DerivedMetricPhysicalBytes, derived.PhysicalBytes)
	runtime.metrics.SetDerived(processing.DerivedMetricOrphanBytes, derived.OrphanBytes)
	runtime.metrics.SetDerived(processing.DerivedMetricQuotaBytes, config.DerivedStore.GlobalMaxBytes)
}

func (runtime *managedProcessingRuntime) Run(ctx context.Context) {
	if runtime == nil || !runtime.ready.Load() || runtime.stopped.Load() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.mu.Lock()
	if runtime.runDone != nil {
		runtime.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	runtime.runCancel = cancel
	runtime.runDone = done
	interval := runtime.config.DerivedStore.ReconcileInterval
	runtime.mu.Unlock()
	defer func() {
		cancel()
		runtime.mu.Lock()
		if runtime.runDone == done {
			runtime.runCancel = nil
			runtime.runDone = nil
			close(done)
		}
		runtime.mu.Unlock()
	}()
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			_ = runtime.reconcile(runCtx)
		}
	}
}

func (runtime *managedProcessingRuntime) StopAccepting() {
	if runtime == nil {
		return
	}
	runtime.ready.Store(false)
	runtime.mu.RLock()
	protocol := runtime.workerProtocol
	runtime.mu.RUnlock()
	if protocol != nil {
		protocol.StopAccepting()
	}
}

func (runtime *managedProcessingRuntime) Shutdown(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.stopped.Store(true)
	runtime.StopAccepting()
	runtime.mu.Lock()
	cancel := runtime.runCancel
	done := runtime.runDone
	protocol := runtime.workerProtocol
	reconciler := runtime.reconciler
	derivedReconciler := runtime.derivedReconciler
	runtime.mu.Unlock()
	var shutdownErrors []error
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			shutdownErrors = append(shutdownErrors, ctx.Err())
		}
	}
	if protocol != nil {
		if err := protocol.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if reconciler != nil {
		if _, err := reconciler.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if derivedReconciler != nil {
		if _, err := derivedReconciler.Reconcile(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	return errors.Join(shutdownErrors...)
}

func cloneProcessingTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

type runtimeProcessingSourceRevalidator struct {
	source content.SourceResolver
}

func (revalidator runtimeProcessingSourceRevalidator) RevalidateProcessingSource(ctx context.Context, descriptor processing.WorkDescriptorV1) error {
	if revalidator.source == nil || processing.ValidateWorkDescriptorV1(descriptor) != nil {
		return processing.ErrManifestSourceChanged
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := revalidator.source.OpenContentSource(ctx, content.SourceRequest{
		Ref: descriptor.Source, CatalogGenerationID: descriptor.CatalogGenerationID,
		ExpectedSource: descriptor.SourceFingerprint, ExpectedEntry: descriptor.EntryFingerprint,
		Mode: content.SourceModeStat,
	})
	if err != nil || session == nil {
		return fmt.Errorf("%w: source unavailable", processing.ErrManifestSourceChanged)
	}
	stat := session.Stat()
	revalidateErr := session.Revalidate(ctx)
	closeErr := session.Close()
	if stat.SourceFingerprint != descriptor.SourceFingerprint || stat.EntryFingerprint != descriptor.EntryFingerprint ||
		revalidateErr != nil || closeErr != nil {
		return processing.ErrManifestSourceChanged
	}
	return nil
}

// Child 10 wires the existing Child 7 ingest instance into the shared graph,
// but production capabilities are intentionally empty until Child 11 defines
// the signed projection payload contract. Failing closed here prevents a
// derivative from being marked searchable without an atomic Search update.
type runtimeDerivedProjectionPort struct {
	ingest search.ContentIndexIngest
}

func (port runtimeDerivedProjectionPort) Publish(context.Context, processing.DerivedProjectionPublish) (processing.DerivedProjectionPublication, error) {
	if port.ingest == nil {
		return processing.DerivedProjectionPublication{}, fmt.Errorf("%w: Search projection port unavailable", backupasset.ErrInvalidState)
	}
	return processing.DerivedProjectionPublication{}, fmt.Errorf("%w: no production Derived projection capability is registered", backupasset.ErrInvalidState)
}

func (port runtimeDerivedProjectionPort) Revoke(context.Context, processing.DerivedProjectionRevoke) error {
	if port.ingest == nil {
		return fmt.Errorf("%w: Search projection port unavailable", backupasset.ErrInvalidState)
	}
	return fmt.Errorf("%w: no production Derived projection capability is registered", backupasset.ErrInvalidState)
}
