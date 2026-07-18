package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

const (
	runtimeProviderMaxPageSize = 200
	runtimeProviderCursorTTL   = 15 * time.Minute
)

// Dependencies contain the one process-wide asset runtime graph. Supplying
// only one transport facet, or two different instances, would split command
// admission/concurrency evidence and is rejected.
type Dependencies struct {
	DB              *gorm.DB
	Settings        *settings.Service
	Now             func() time.Time
	Transport       provider.CommandTransport
	StreamTransport provider.CommandStreamTransport
	StagedPayload   provider.StagedPayloadTransport
	ToolBinaries    provider.ToolBinaries
	Metrics         publication.Metrics
	CatalogMetrics  catalog.Metrics
	SearchMetrics   search.Metrics
	Tombstones      repository.ManagedHistoryTombstoneSource
}

// Runtime is the single composition root for Repository reads, publication,
// admission, and guarded legacy Restic callers. It does not own Task Manager;
// callback ports are set explicitly before StartupPass.
type Runtime struct {
	foundation     *backupasset.FoundationService
	repository     *repository.Service
	publication    *repository.PublicationService
	resticStrategy provider.PublicationStrategy
	rsyncStrategy  provider.PublicationStrategy
	rcloneStrategy provider.PublicationStrategy
	admission      *AdmissionController
	worker         *PublicationWorker
	healthWorker   *RcloneHealthWorker
	catalogService *catalog.Service
	catalogIndexer *catalog.Indexer
	catalogWorker  *CatalogWorker
	catalogAudit   repository.AssetAuditSink
	keyring        *backupasset.Keyring
	searchService  *search.Service
	searchIndexer  *search.Indexer
	searchIngest   *search.ContentIngestService
	searchWorker   *SearchWorker
	overlayService *overlay.Service
	searchReady    *atomic.Bool
	metrics        publication.Metrics

	mu          sync.Mutex
	searchKeyMu sync.Mutex
	starting    bool
	observer    publication.CommitObserver
	reporter    publication.InterruptedRunReporter
	readiness   publication.InterruptedRunReadiness
}

func New(dependencies Dependencies) (*Runtime, error) {
	if dependencies.DB == nil || dependencies.Settings == nil {
		return nil, fmt.Errorf("%w: backup asset runtime requires database and settings", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	foundation := backupasset.NewFoundationService(dependencies.Settings)
	if _, err := foundation.ProviderConfig(); err != nil {
		return nil, err
	}
	leaseConfig, err := foundation.LeaseConfig()
	if err != nil {
		return nil, err
	}
	if _, err := foundation.PublicationConfig(); err != nil {
		return nil, err
	}
	catalogConfig, err := foundation.CatalogConfig()
	if err != nil {
		return nil, err
	}
	searchConfig, overlayConfig, err := foundation.SearchOverlayConfig()
	if err != nil {
		return nil, err
	}

	metricsSink := dependencies.Metrics
	if metricsSink == nil {
		prometheusMetrics, err := publication.NewPrometheusMetrics(prometheus.DefaultRegisterer)
		if err != nil {
			return nil, err
		}
		metricsSink = prometheusMetrics
	}
	catalogMetrics := dependencies.CatalogMetrics
	if catalogMetrics == nil {
		if dependencies.Metrics == nil {
			catalogMetrics, err = catalog.NewPrometheusMetrics(prometheus.DefaultRegisterer)
			if err != nil {
				return nil, err
			}
		} else {
			catalogMetrics = catalog.NoopMetrics{}
		}
	}
	keyring := backupasset.NewKeyring(dependencies.DB, dependencies.Now)
	auditWriter, err := backupasset.NewAuditWriterWithConfigSource(dependencies.DB, keyring, dependencies.Now, foundation.AuditConfig)
	if err != nil {
		return nil, err
	}
	auditSink := repository.NewAssetAuditSink(auditWriter)
	catalogOwnership, err := catalog.NewOwnership(dependencies.DB)
	if err != nil {
		return nil, err
	}
	catalogService, err := catalog.NewService(catalog.ServiceDependencies{
		DB: dependencies.DB, Ownership: catalogOwnership,
		Cursor: catalog.NewCursorCodec(keyring, dependencies.Now, runtimeProviderCursorTTL), Now: dependencies.Now,
		ReconcileInterval: catalogConfig.ReconcileInterval, FeatureEnabled: foundation.FeatureEnabled,
	})
	if err != nil {
		return nil, err
	}
	history, err := repository.NewManagedHistoryResolver(repository.ManagedHistoryResolverDependencies{DB: dependencies.DB, Tombstones: dependencies.Tombstones})
	if err != nil {
		return nil, err
	}
	admission, err := NewAdmissionController(AdmissionControllerDependencies{Foundation: foundation, History: history})
	if err != nil {
		return nil, err
	}
	transport, streamTransport, err := runtimeTransport(dependencies, foundation)
	if err != nil {
		return nil, err
	}
	limitsSource := func() (provider.OperationLimits, error) {
		config, err := foundation.ProviderConfig()
		if err != nil {
			return provider.OperationLimits{}, err
		}
		return provider.NewMetadataOperationLimits(config.OperationTimeout, config.MetadataLimitBytes)
	}
	cursorCodec := provider.NewCursorCodec(keyring, dependencies.Now, runtimeProviderCursorTTL)
	rsyncAdapter, err := provider.NewRsyncAdapterWithLimitsSource(cursorCodec, limitsSource, runtimeProviderMaxPageSize, dependencies.Now)
	if err != nil {
		return nil, err
	}
	resticAdapter, err := provider.NewResticAdapterWithPublication(transport, streamTransport, cursorCodec, limitsSource, foundation.PublicationConfig, runtimeProviderMaxPageSize, dependencies.Now)
	if err != nil {
		return nil, err
	}
	resticStrategy, err := provider.NewResticPublicationStrategy(resticAdapter, resticAdapter)
	if err != nil {
		return nil, err
	}
	rsyncStrategy, err := provider.NewLocalRsyncTreePublicationStrategy(dependencies.Now)
	if err != nil {
		return nil, err
	}
	rcloneAdapter, err := provider.NewRcloneAdapterWithLimitsSource(transport, cursorCodec, limitsSource, runtimeProviderMaxPageSize, dependencies.Now)
	if err != nil {
		return nil, err
	}
	stagedPayload := dependencies.StagedPayload
	if stagedPayload == nil {
		ownership, ownershipErr := keyring.Ensure(context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership)
		if ownershipErr != nil {
			return nil, ownershipErr
		}
		stagedPayload, err = provider.NewRcloneStagedPayloadTransport(sshutil.NewNodeDialer(dependencies.DB), ownership.Key, dependencies.Now)
		if err != nil {
			return nil, err
		}
	}
	portableRemote, err := provider.NewCommandRclonePortableRemote(transport, stagedPayload, limitsSource)
	if err != nil {
		return nil, err
	}
	nativeDataPlane, err := provider.NewCommandRcloneNativeDataPlane(transport, limitsSource)
	if err != nil {
		return nil, err
	}
	rcloneStrategy, err := provider.NewRclonePublicationStrategy(
		provider.NewRclonePortablePublisher(portableRemote, nil, dependencies.Now),
		provider.NewRcloneNativePublisher(nativeDataPlane, dependencies.Now),
	)
	if err != nil {
		return nil, err
	}
	rcloneCatalogReader, err := provider.NewRcloneCatalogReader(rcloneAdapter, rcloneStrategy)
	if err != nil {
		return nil, err
	}
	registry := provider.NewRegistry()
	for _, registration := range []struct {
		kind  backupasset.ProviderKind
		value provider.Registration
	}{
		{backupasset.ProviderRsync, provider.Registration{Prober: rsyncAdapter, PointLister: rsyncAdapter, EntryLister: rsyncAdapter, EntryStatter: rsyncAdapter, SequentialReader: rsyncAdapter, RangeReader: rsyncAdapter, CatalogReader: rsyncAdapter, PublicationStrategy: rsyncStrategy}},
		{backupasset.ProviderRestic, provider.Registration{Prober: resticAdapter, PointLister: resticAdapter, EntryLister: resticAdapter, EntryStatter: resticAdapter, SequentialReader: resticAdapter, CatalogReader: resticAdapter, PublicationStrategy: resticStrategy}},
		{backupasset.ProviderRclone, provider.Registration{Prober: rcloneAdapter, PointLister: rcloneAdapter, EntryLister: rcloneAdapter, EntryStatter: rcloneAdapter, SequentialReader: rcloneAdapter, RangeReader: rcloneAdapter, CatalogReader: rcloneCatalogReader, PublicationStrategy: rcloneStrategy}},
	} {
		if err := registry.Register(registration.kind, registration.value); err != nil {
			return nil, err
		}
	}
	lease, err := backupasset.NewLeaseService(dependencies.DB, dependencies.Now, leaseConfig)
	if err != nil {
		return nil, err
	}
	searchQueryLimits := runtimeSearchQueryLimits(searchConfig)
	overlayAuthorizer := &runtimeOverlayAuthorizer{catalog: catalogService, ownership: catalogOwnership}
	overlayService, err := overlay.NewService(overlay.ServiceDependencies{
		DB: dependencies.DB, Keys: keyring, Assets: overlayAuthorizer, Points: overlayAuthorizer, Now: dependencies.Now,
		Audit: auditSink, FeatureEnabled: foundation.FeatureEnabled,
		Config: overlay.Config{
			SavedSearchQuota: overlayConfig.SavedSearchQuota, FavoriteQuota: overlayConfig.FavoriteQuota,
			TagDefinitionQuota: overlayConfig.TagDefinitionQuota, TagAssignmentQuota: overlayConfig.TagAssignmentQuota,
			RecentQuota: overlayConfig.RecentQuota, RecentWritesPerMinute: overlayConfig.RecentWritesPerMinute,
			RecentTTL: overlayConfig.RecentRetention, IdempotencyTTL: overlayConfig.IdempotencyTTL,
			MaxBulk: overlayConfig.BulkMaxItems, LabelMaxBytes: overlayConfig.LabelMaxBytes,
			IdempotencyKeyMaxBytes: overlayConfig.IdempotencyKeyMaxBytes, QueryLimits: searchQueryLimits,
		},
	})
	if err != nil {
		return nil, err
	}
	searchScope, err := search.NewScopeResolver(dependencies.DB, catalogOwnership, search.ScopeResolverLimits{MaxCandidates: searchConfig.CandidateLimit})
	if err != nil {
		return nil, err
	}
	searchService, err := search.NewService(search.ServiceDependencies{
		DB: dependencies.DB, Scope: searchScope, Keys: keyring,
		Cursor: search.NewCursorCodec(keyring, dependencies.Now, runtimeProviderCursorTTL), Tags: overlayService, Now: dependencies.Now,
		Limits:         search.ServiceLimits{Query: searchQueryLimits, MaxCandidates: searchConfig.CandidateLimit, ExecutionTimeout: searchConfig.QueryTimeout},
		FeatureEnabled: foundation.FeatureEnabled,
	})
	if err != nil {
		return nil, err
	}
	searchIndexer, err := search.NewIndexer(search.IndexerDependencies{
		DB: dependencies.DB, Lease: lease, Keys: keyring, Now: dependencies.Now,
		Config: search.IndexerConfig{BatchSize: searchConfig.BatchSize, BuildTimeout: searchConfig.BuildTimeout, MaxDocuments: searchConfig.MaxDocuments},
	})
	if err != nil {
		return nil, err
	}
	contentLimits := search.DefaultContentIngestLimits()
	contentLimits.MaxTermBytes = searchConfig.ValueMaxBytes
	contentLimits.MaxTermRunes = searchConfig.ValueMaxBytes
	searchIngest, err := search.NewContentIngestService(search.ContentIngestDependencies{
		DB: dependencies.DB, Keys: keyring, Lease: lease, Now: dependencies.Now, Limits: contentLimits,
	})
	if err != nil {
		return nil, err
	}
	searchMetrics := dependencies.SearchMetrics
	if searchMetrics == nil {
		searchMetrics = search.NoopMetrics{}
	}
	searchReady := &atomic.Bool{}
	searchWorker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			config, err := foundation.SearchConfig()
			if err != nil {
				return SearchWorkerConfig{}, err
			}
			return SearchWorkerConfig{
				Enabled: config.Enabled && searchReady.Load(), ReconcileInterval: config.ReconcileInterval,
				ReconcileBatchSize: config.BatchSize, WorkerConcurrency: config.MaxConcurrency, AbandonedAfter: config.BuildTimeout,
			}, nil
		},
		Backend: searchIndexerWorkerBackend{indexer: searchIndexer, overlays: overlayService}, Metrics: searchMetrics, Now: dependencies.Now,
	})
	if err != nil {
		return nil, err
	}
	var worker *PublicationWorker
	publicationService, err := repository.NewPublicationService(repository.PublicationDependencies{
		DB: dependencies.DB, Foundation: foundation, Registry: registry, Keyring: keyring, Lease: lease, Admission: admission, Metrics: metricsSink,
		Audit: auditSink, History: history, Now: dependencies.Now,
		TryWake: func(pointID string) bool {
			if worker == nil {
				return false
			}
			return worker.TryWake(pointID)
		},
	})
	if err != nil {
		return nil, err
	}
	worker, err = NewPublicationWorker(PublicationWorkerDependencies{Foundation: foundation, Reconciler: publicationService, Metrics: metricsSink, Now: dependencies.Now})
	if err != nil {
		return nil, err
	}
	healthWorker, err := NewRcloneHealthWorker(RcloneHealthWorkerDependencies{Foundation: foundation, Health: publicationService})
	if err != nil {
		return nil, err
	}
	repositoryService, err := repository.NewService(repository.Dependencies{
		DB: dependencies.DB, Foundation: foundation, Registry: registry, Keyring: keyring, Now: dependencies.Now,
		Audit: auditSink, Admission: admission, History: history, Metrics: metricsSink, Publication: publicationService,
		CatalogOwnership: catalogOwnership, CatalogSummary: catalogService,
	})
	if err != nil {
		return nil, err
	}
	catalogIndexer, err := catalog.NewIndexer(catalog.IndexerDependencies{
		DB: dependencies.DB, Factory: repositoryService, Lease: lease, IdentityKeys: keyring, Now: dependencies.Now,
		Config: catalog.IndexerConfig{
			BatchSize: catalogConfig.BatchSize, BuildTimeout: catalogConfig.BuildTimeout, MaxEntries: catalogConfig.MaxEntries,
			HeartbeatInterval: catalogConfig.Lease.Heartbeat,
		},
	})
	if err != nil {
		return nil, err
	}
	catalogWorker, err := NewCatalogWorker(CatalogWorkerDependencies{
		Foundation: foundation, Backend: catalogIndexer, Metrics: catalogMetrics, Now: dependencies.Now,
	})
	if err != nil {
		return nil, err
	}
	return &Runtime{
		foundation: foundation, repository: repositoryService, publication: publicationService,
		resticStrategy: resticStrategy, rsyncStrategy: rsyncStrategy, rcloneStrategy: rcloneStrategy,
		admission: admission, worker: worker, healthWorker: healthWorker,
		catalogService: catalogService, catalogIndexer: catalogIndexer, catalogWorker: catalogWorker, catalogAudit: auditSink,
		keyring: keyring, searchService: searchService, searchIndexer: searchIndexer, searchIngest: searchIngest,
		searchWorker: searchWorker, overlayService: overlayService, searchReady: searchReady,
		metrics: metricsSink,
	}, nil
}

func runtimeTransport(dependencies Dependencies, foundation *backupasset.FoundationService) (provider.CommandTransport, provider.CommandStreamTransport, error) {
	if (dependencies.Transport == nil) != (dependencies.StreamTransport == nil) {
		return nil, nil, fmt.Errorf("%w: both provider transport facets are required together", backupasset.ErrInvalidState)
	}
	if dependencies.Transport != nil {
		if !sameTransportInstance(dependencies.Transport, dependencies.StreamTransport) {
			return nil, nil, fmt.Errorf("%w: provider transport facets must share one instance", backupasset.ErrInvalidState)
		}
		return dependencies.Transport, dependencies.StreamTransport, nil
	}
	if foundation == nil {
		return nil, nil, fmt.Errorf("%w: provider foundation unavailable", backupasset.ErrInvalidState)
	}
	transport, err := provider.NewSSHCommandTransportWithConcurrencySource(sshutil.NewNodeDialer(dependencies.DB), func() (int, error) {
		config, err := foundation.ProviderConfig()
		if err != nil {
			return 0, err
		}
		return config.MaxConcurrency, nil
	}, dependencies.ToolBinaries)
	if err != nil {
		return nil, nil, err
	}
	return transport, transport, nil
}

func sameTransportInstance(command provider.CommandTransport, stream provider.CommandStreamTransport) bool {
	left := reflect.ValueOf(command)
	right := reflect.ValueOf(stream)
	if !left.IsValid() || !right.IsValid() || left.Type() != right.Type() {
		return false
	}
	switch left.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return left.Pointer() == right.Pointer()
	default:
		return false
	}
}

func (runtime *Runtime) FoundationService() *backupasset.FoundationService { return runtime.foundation }
func (runtime *Runtime) RepositoryService() *repository.Service            { return runtime.repository }
func (runtime *Runtime) CatalogService() *catalog.Service {
	if runtime == nil {
		return nil
	}
	return runtime.catalogService
}
func (runtime *Runtime) CatalogAuditSink() repository.AssetAuditSink {
	if runtime == nil {
		return nil
	}
	return runtime.catalogAudit
}
func (runtime *Runtime) SearchService() *search.Service {
	if runtime == nil {
		return nil
	}
	return runtime.searchService
}
func (runtime *Runtime) OverlayService() *overlay.Service {
	if runtime == nil {
		return nil
	}
	return runtime.overlayService
}
func (runtime *Runtime) ContentIndexIngest() search.ContentIndexIngest {
	if runtime == nil {
		return nil
	}
	return runtime.searchIngest
}
func (runtime *Runtime) PublicationCoordinator() publication.Coordinator { return runtime.publication }
func (runtime *Runtime) PublicationReconciler() publication.Reconciler   { return runtime.publication }
func (runtime *Runtime) ResticPublicationStrategy() provider.PublicationStrategy {
	if runtime == nil {
		return nil
	}
	return runtime.resticStrategy
}
func (runtime *Runtime) RsyncTreePublicationStrategy() provider.PublicationStrategy {
	if runtime == nil {
		return nil
	}
	return runtime.rsyncStrategy
}
func (runtime *Runtime) RclonePublicationStrategy() provider.PublicationStrategy {
	if runtime == nil {
		return nil
	}
	return runtime.rcloneStrategy
}
func (runtime *Runtime) LineageGuard() publication.LineageGuard { return runtime.repository }
func (runtime *Runtime) LegacyBlockRecorder() publication.LegacyBlockRecorder {
	return runtime.publication
}
func (runtime *Runtime) FeatureTransitioner() publication.FeatureTransitioner {
	return runtime.admission
}

func (runtime *Runtime) SetCommitObserver(observer publication.CommitObserver) error {
	if runtime == nil {
		return fmt.Errorf("%w: backup asset runtime unavailable", backupasset.ErrInvalidState)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.starting {
		return fmt.Errorf("%w: backup asset runtime callback wiring is closed", backupasset.ErrConflict)
	}
	runtime.observer = observer
	runtime.worker.observer = observer
	return nil
}

func (runtime *Runtime) SetInterruptedRunReporter(reporter publication.InterruptedRunReporter) error {
	if runtime == nil {
		return fmt.Errorf("%w: backup asset runtime unavailable", backupasset.ErrInvalidState)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.starting {
		return fmt.Errorf("%w: backup asset runtime callback wiring is closed", backupasset.ErrConflict)
	}
	runtime.reporter = reporter
	runtime.worker.reporter = reporter
	return nil
}

func (runtime *Runtime) SetInterruptedRunReadiness(readiness publication.InterruptedRunReadiness) error {
	if runtime == nil {
		return fmt.Errorf("%w: backup asset runtime unavailable", backupasset.ErrInvalidState)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.starting {
		return fmt.Errorf("%w: backup asset runtime callback wiring is closed", backupasset.ErrConflict)
	}
	runtime.readiness = readiness
	return nil
}

// StartupPass initializes admission before any command may be admitted, runs a
// bounded worker pass, then uses unfiltered publication/TaskRun readiness.
func (runtime *Runtime) StartupPass(ctx context.Context) error {
	if runtime == nil || runtime.admission == nil || runtime.worker == nil || runtime.healthWorker == nil || runtime.publication == nil {
		return fmt.Errorf("%w: backup asset runtime unavailable", backupasset.ErrInvalidState)
	}
	runtime.mu.Lock()
	runtime.starting = true
	readiness := runtime.readiness
	runtime.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := runtime.admission.Initialize(ctx); err != nil {
		return err
	}
	mode, err := runtime.admission.CurrentMode()
	if err != nil {
		return err
	}
	if err := runtime.worker.StartupPass(ctx); err != nil {
		return err
	}
	if err := runtime.healthWorker.StartupPass(ctx); err != nil {
		return err
	}
	unresolved, err := runtime.publication.HasUnresolvedPublication(ctx)
	if err != nil {
		return err
	}
	if unresolved {
		return fmt.Errorf("%w: unresolved backup asset publication", backupasset.ErrConflict)
	}
	if mode != publication.AdmissionPristineLegacy {
		if readiness == nil {
			return fmt.Errorf("%w: managed backup asset runtime requires interrupted TaskRun readiness", backupasset.ErrInvalidState)
		}
		unresolved, err := readiness.ReconcileInterruptedRuns(ctx, publicationStartupBatchSize(runtime.foundation))
		if err != nil {
			return err
		}
		if unresolved {
			return fmt.Errorf("%w: unresolved backup publication TaskRun", backupasset.ErrConflict)
		}
	}
	return runtime.startupSearch(ctx)
}

func (runtime *Runtime) startupSearch(ctx context.Context) error {
	if runtime == nil || runtime.foundation == nil || runtime.keyring == nil || runtime.searchWorker == nil {
		return fmt.Errorf("%w: Search runtime unavailable", backupasset.ErrInvalidState)
	}
	enabled, err := runtime.foundation.FeatureEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		runtime.setSearchReady(false)
		return nil
	}
	if _, err := runtime.keyring.RewrapAll(ctx); err != nil {
		runtime.setSearchReady(false)
		return err
	}
	if _, err := runtime.keyring.Ensure(ctx, backupasset.KeyDomainSearchToken); errors.Is(err, backupasset.ErrKeyLost) {
		runtime.setSearchReady(false)
		return nil
	} else if err != nil {
		runtime.setSearchReady(false)
		return err
	}
	runtime.setSearchReady(true)
	if err := runtime.searchWorker.StartupPass(ctx); err != nil {
		runtime.setSearchReady(false)
		return err
	}
	return nil
}

func (runtime *Runtime) setSearchReady(ready bool) {
	if runtime != nil && runtime.searchReady != nil {
		runtime.searchReady.Store(ready)
	}
}

func (runtime *Runtime) ReplaceSearchTokenForReindex(ctx context.Context) (backupasset.DomainKeyMaterial, error) {
	if runtime == nil || runtime.foundation == nil || runtime.keyring == nil || runtime.overlayService == nil || runtime.searchReady == nil {
		return backupasset.DomainKeyMaterial{}, fmt.Errorf("%w: Search Token replacement unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.searchKeyMu.Lock()
	defer runtime.searchKeyMu.Unlock()
	enabled, err := runtime.foundation.FeatureEnabled()
	if err != nil {
		return backupasset.DomainKeyMaterial{}, err
	}
	wasReady := runtime.searchReady.Load()
	runtime.setSearchReady(false)
	material, err := runtime.keyring.ReplaceRebuildable(
		ctx, backupasset.KeyDomainSearchToken, runtime.overlayService.InvalidateSearchKey,
	)
	if err != nil {
		runtime.setSearchReady(wasReady)
		return backupasset.DomainKeyMaterial{}, err
	}
	runtime.setSearchReady(enabled)
	return material, nil
}

func (runtime *Runtime) MarkSearchTokenLost(ctx context.Context, version int) error {
	if runtime == nil || runtime.keyring == nil || runtime.overlayService == nil || runtime.searchReady == nil || version <= 0 {
		return fmt.Errorf("%w: Search Token loss transition unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.searchKeyMu.Lock()
	defer runtime.searchKeyMu.Unlock()
	active, err := runtime.keyring.Active(ctx, backupasset.KeyDomainSearchToken)
	if err != nil {
		return err
	}
	if active.Version != version {
		return fmt.Errorf("%w: Search Token version is not active", backupasset.ErrKeyUnavailable)
	}
	wasReady := runtime.searchReady.Load()
	runtime.setSearchReady(false)
	if err := runtime.keyring.MarkRebuildableLost(
		ctx, backupasset.KeyDomainSearchToken, version, runtime.overlayService.InvalidateSearchKey,
	); err != nil {
		runtime.setSearchReady(wasReady)
		return err
	}
	return nil
}

func publicationStartupBatchSize(foundation *backupasset.FoundationService) int {
	if foundation != nil {
		if config, err := foundation.PublicationConfig(); err == nil && config.ReconcileBatchSize > 0 {
			return config.ReconcileBatchSize
		}
	}
	return 1
}

func (runtime *Runtime) StopAccepting() {
	if runtime == nil || runtime.admission == nil {
		return
	}
	runtime.admission.StopAccepting()
}

func (runtime *Runtime) Run(ctx context.Context) {
	if runtime == nil || runtime.worker == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var health sync.WaitGroup
	if runtime.healthWorker != nil {
		health.Add(1)
		go func() {
			defer health.Done()
			runtime.healthWorker.Run(runCtx)
		}()
	}
	if runtime.catalogWorker != nil {
		health.Add(1)
		go func() {
			defer health.Done()
			runtime.catalogWorker.Run(runCtx)
		}()
	}
	if runtime.searchWorker != nil {
		health.Add(1)
		go func() {
			defer health.Done()
			runtime.searchWorker.Run(runCtx)
		}()
	}
	runtime.worker.Run(runCtx)
	cancel()
	health.Wait()
}

func (runtime *Runtime) Shutdown(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if runtime.admission != nil {
		runtime.admission.StopAccepting()
	}
	if runtime.searchWorker != nil {
		if err := runtime.searchWorker.Shutdown(ctx); err != nil {
			return err
		}
	}
	if runtime.catalogWorker != nil {
		if err := runtime.catalogWorker.Shutdown(ctx); err != nil {
			return err
		}
	}
	if runtime.worker != nil {
		if err := runtime.worker.Shutdown(ctx); err != nil {
			return err
		}
	}
	if runtime.healthWorker != nil {
		if err := runtime.healthWorker.Shutdown(ctx); err != nil {
			return err
		}
	}
	if runtime.admission != nil {
		if err := runtime.admission.Stop(ctx); err != nil && err != ErrAdmissionNotInitialized {
			return err
		}
	}
	return nil
}

func runtimeSearchQueryLimits(config backupasset.SearchConfig) search.QueryLimits {
	return search.QueryLimits{
		MaxBodyBytes: config.BodyMaxBytes, MaxDepth: config.ASTMaxDepth, MaxNodes: config.ASTMaxNodes,
		MaxValuesPerNode: config.ValuesPerNode, MaxValueBytes: config.ValueMaxBytes, MaxValueRunes: config.ValueMaxBytes,
		MaxPageSize: config.PageSizeMax, MaxCandidates: config.CandidateLimit,
		MaxExecutionTime: config.QueryTimeout, MaxSuggestions: config.SuggestionLimit,
	}
}

type runtimeOverlayAuthorizer struct {
	catalog   *catalog.Service
	ownership *catalog.Ownership
}

type searchOverlayReconciler interface {
	ReconcileTagKeys(context.Context, int) (int64, error)
	ReconcileInvalidSources(context.Context, int) (int64, error)
	CleanupExpiredRecent(context.Context, int) (int64, error)
	CleanupIdempotency(context.Context, int) (int64, error)
}

type searchIndexerWorkerBackend struct {
	indexer  *search.Indexer
	overlays searchOverlayReconciler
}

func (backend searchIndexerWorkerBackend) ListCandidates(ctx context.Context, limit int) ([]search.BuildCandidate, error) {
	return backend.indexer.ListCandidates(ctx, limit)
}

func (backend searchIndexerWorkerBackend) Build(ctx context.Context, request search.BuildRequest) error {
	_, err := backend.indexer.Build(ctx, request)
	return err
}

func (backend searchIndexerWorkerBackend) ReconcileAbandoned(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	return backend.indexer.ReconcileAbandoned(ctx, cutoff, limit)
}

func (backend searchIndexerWorkerBackend) ReconcileOverlays(ctx context.Context, limit int) (int64, error) {
	if backend.overlays == nil {
		return 0, fmt.Errorf("%w: overlay reconciler unavailable", backupasset.ErrInvalidState)
	}
	rekeyed, err := backend.overlays.ReconcileTagKeys(ctx, limit)
	if err != nil {
		return 0, err
	}
	reconciled, err := backend.overlays.ReconcileInvalidSources(ctx, limit)
	if err != nil {
		return 0, err
	}
	expiredRecent, err := backend.overlays.CleanupExpiredRecent(ctx, limit)
	if err != nil {
		return 0, err
	}
	expiredIdempotency, err := backend.overlays.CleanupIdempotency(ctx, limit)
	if err != nil {
		return 0, err
	}
	return rekeyed + reconciled + expiredRecent + expiredIdempotency, nil
}

func (authorizer *runtimeOverlayAuthorizer) AuthorizeAsset(
	ctx context.Context,
	tx *gorm.DB,
	actor overlay.Actor,
	ref backupasset.AssetRef,
) error {
	if authorizer == nil || authorizer.catalog == nil || tx == nil || actor.UserID == 0 ||
		(actor.Role != "admin" && actor.Role != "operator") || backupasset.ValidateAssetRef(ref) != nil {
		return backupasset.ErrForbidden
	}
	ownership, err := catalog.NewOwnership(tx)
	if err != nil {
		return err
	}
	visible, err := ownership.AuthorizedPointIDs(ctx, catalog.AuthorizationScope{Role: actor.Role, UserID: actor.UserID}, []string{ref.RecoveryPointID})
	if err != nil {
		return err
	}
	if len(visible) != 1 {
		return fmt.Errorf("%w: overlay asset", backupasset.ErrNotFound)
	}
	var point model.RecoveryPoint
	if err := tx.WithContext(ctx).Where("id = ?", ref.RecoveryPointID).Take(&point).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: overlay asset", backupasset.ErrNotFound)
		}
		return fmt.Errorf("load overlay RecoveryPoint authorization: %w", err)
	}
	if !runtimeOverlayPointVisible(point) {
		return fmt.Errorf("%w: overlay asset", backupasset.ErrNotFound)
	}
	var count int64
	if err := tx.WithContext(ctx).Table("catalog_entries AS entries").
		Joins("JOIN catalog_generations AS generations ON generations.id = entries.generation_id AND generations.recovery_point_id = entries.recovery_point_id").
		Where(`entries.recovery_point_id = ? AND entries.entry_id = ?
			AND generations.state = ? AND generations.is_active = ?`,
			ref.RecoveryPointID, ref.EntryID, catalog.GenerationComplete, true).Count(&count).Error; err != nil {
		return fmt.Errorf("load overlay Catalog entry authorization: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("%w: overlay asset", backupasset.ErrNotFound)
	}
	return nil
}

func runtimeOverlayPointVisible(point model.RecoveryPoint) bool {
	switch backupasset.PointVersionSemantics(point.Semantics) {
	case backupasset.PointMutableHead:
		return backupasset.RecoveryPointState(point.State) == backupasset.RecoveryPointObserved
	case backupasset.PointNativeSnapshot, backupasset.PointXirangManifest, backupasset.PointImportedBaseline:
		state := backupasset.RecoveryPointState(point.State)
		return state == backupasset.RecoveryPointCommitted || state == backupasset.RecoveryPointDegraded
	default:
		return false
	}
}

func (authorizer *runtimeOverlayAuthorizer) AuthorizePoints(ctx context.Context, actor overlay.Actor, pointIDs []string) error {
	if authorizer == nil || authorizer.ownership == nil || actor.UserID == 0 || (actor.Role != "admin" && actor.Role != "operator") {
		return backupasset.ErrForbidden
	}
	const batchSize = 2000
	for start := 0; start < len(pointIDs); start += batchSize {
		end := min(start+batchSize, len(pointIDs))
		batch := pointIDs[start:end]
		authorized, err := authorizer.ownership.AuthorizedPointIDs(ctx, catalog.AuthorizationScope{Role: actor.Role, UserID: actor.UserID}, batch)
		if err != nil {
			return err
		}
		if len(authorized) != len(batch) {
			return backupasset.ErrForbidden
		}
	}
	return nil
}
