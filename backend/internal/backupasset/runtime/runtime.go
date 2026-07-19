package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/logger"
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
	DB                 *gorm.DB
	Settings           *settings.Service
	Now                func() time.Time
	Transport          provider.CommandTransport
	StreamTransport    provider.CommandStreamTransport
	StagedPayload      provider.StagedPayloadTransport
	ToolBinaries       provider.ToolBinaries
	Metrics            publication.Metrics
	CatalogMetrics     catalog.Metrics
	SearchMetrics      search.Metrics
	ContentMetrics     content.Metrics
	SessionRevocations ContentSessionRevocationSource
	Tombstones         repository.ManagedHistoryTombstoneSource
}

type ContentSessionRevocationSource interface {
	IsSessionRevoked(string) (bool, error)
}

type contentRuntimeManager interface {
	Startup(context.Context) error
	PrepareEnable(context.Context) error
	PrepareDisable(context.Context) error
	SetReady(bool)
	StopAccepting()
	Run(context.Context)
	Shutdown(context.Context) error
	PrepareSchemaDown(context.Context, func() error) error
}

// Runtime is the single composition root for Repository reads, publication,
// admission, and guarded legacy Restic callers. It does not own Task Manager;
// callback ports are set explicitly before StartupPass.
type Runtime struct {
	foundation        *backupasset.FoundationService
	repository        *repository.Service
	publication       *repository.PublicationService
	resticStrategy    provider.PublicationStrategy
	rsyncStrategy     provider.PublicationStrategy
	rcloneStrategy    provider.PublicationStrategy
	admission         *AdmissionController
	worker            *PublicationWorker
	healthWorker      *RcloneHealthWorker
	catalogService    *catalog.Service
	catalogIndexer    *catalog.Indexer
	catalogWorker     *CatalogWorker
	catalogAudit      repository.AssetAuditSink
	keyring           *backupasset.Keyring
	searchService     *search.Service
	searchIndexer     *search.Indexer
	searchIngest      *search.ContentIngestService
	searchWorker      *SearchWorker
	overlayService    *overlay.Service
	searchReady       *atomic.Bool
	contentBroker     *content.Broker
	contentBudget     *content.BudgetService
	contentAudit      *content.ContentAuditService
	contentReconciler *content.Reconciler
	contentReady      *atomic.Bool
	contentManager    contentRuntimeManager
	transitioner      publication.FeatureTransitioner
	metrics           publication.Metrics

	mu          sync.Mutex
	searchKeyMu sync.Mutex
	starting    bool
	observer    publication.CommitObserver
	reporter    publication.InterruptedRunReporter
	readiness   publication.InterruptedRunReadiness
}

func New(dependencies Dependencies) (*Runtime, error) {
	if dependencies.DB == nil || dependencies.Settings == nil || dependencies.SessionRevocations == nil {
		return nil, fmt.Errorf("%w: backup asset runtime requires database, settings, and session revocation state", backupasset.ErrInvalidState)
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
	contentConfig, err := foundation.ContentConfig()
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
	contentSession, err := newRuntimeContentSessionValidator(dependencies.DB, dependencies.SessionRevocations, dependencies.Now)
	if err != nil {
		return nil, err
	}
	contentAuthorizer, err := newRuntimeContentAuthorizer(dependencies.DB, catalogOwnership)
	if err != nil {
		return nil, err
	}
	contentBudget, err := content.NewBudgetService(content.BudgetDependencies{
		DB: dependencies.DB, Now: dependencies.Now,
		Limits: func(context.Context) (content.BudgetLimits, error) {
			config, configErr := foundation.ContentConfig()
			if configErr != nil {
				return content.BudgetLimits{}, configErr
			}
			return runtimeContentBudgetLimits(config), nil
		},
	})
	if err != nil {
		return nil, err
	}
	contentMetrics := dependencies.ContentMetrics
	if contentMetrics == nil {
		if dependencies.Metrics != nil {
			contentMetrics = content.NoopMetrics{}
		} else {
			contentMetrics, err = content.NewPrometheusMetrics(prometheus.DefaultRegisterer)
			if err != nil {
				return nil, err
			}
		}
	}
	contentAudit, err := content.NewContentAuditService(content.ContentAuditDependencies{
		DB: dependencies.DB, Writer: auditWriter, Now: dependencies.Now,
		BacklogMax: contentConfig.AuditBacklogMax, Metrics: contentMetrics,
	})
	if err != nil {
		return nil, err
	}
	contentReady := &atomic.Bool{}
	contentBroker, err := content.NewBroker(content.BrokerDependencies{
		DB: dependencies.DB, Now: dependencies.Now,
		FeatureEnabled: func(context.Context) (bool, error) {
			enabled, enabledErr := foundation.FeatureEnabled()
			return enabled && contentReady.Load(), enabledErr
		},
		Authorize: contentAuthorizer, Session: contentSession, Lease: lease, Source: repositoryService,
		Audit: contentAudit, ReadAudit: contentAudit, Budget: contentBudget, Metrics: contentMetrics,
		Config: func(context.Context) (content.BrokerConfig, error) {
			config, configErr := foundation.ContentConfig()
			if configErr != nil {
				return content.BrokerConfig{}, configErr
			}
			return runtimeContentBrokerConfig(config), nil
		},
	})
	if err != nil {
		return nil, err
	}
	contentReconciler, err := content.NewReconciler(content.ReconcilerDependencies{
		DB: dependencies.DB, Budget: contentBudget, Audit: contentAudit, Lease: lease,
		Now: dependencies.Now, BatchSize: contentConfig.ReconcileBatchSize, Metrics: contentMetrics,
	})
	if err != nil {
		return nil, err
	}
	contentManager := &managedContentRuntime{
		db: dependencies.DB, foundation: foundation, sourceRoots: repositoryService,
		broker: contentBroker, reconciler: contentReconciler, ready: contentReady, now: dependencies.Now,
		metrics: contentMetrics,
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
		contentBroker: contentBroker, contentBudget: contentBudget, contentAudit: contentAudit,
		contentReconciler: contentReconciler, contentReady: contentReady, contentManager: contentManager,
		transitioner: admission,
		metrics:      metricsSink,
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
func (runtime *Runtime) ContentBroker() *content.Broker {
	if runtime == nil {
		return nil
	}
	return runtime.contentBroker
}
func (runtime *Runtime) ContentConfig() (backupasset.ContentConfig, error) {
	if runtime == nil || runtime.foundation == nil {
		return backupasset.ContentConfig{}, fmt.Errorf("%w: Content config unavailable", backupasset.ErrInvalidState)
	}
	return runtime.foundation.ContentConfig()
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
	if runtime == nil {
		return nil
	}
	return runtime
}

func (runtime *Runtime) TransitionFeature(ctx context.Context, enabled bool, persist func() error) error {
	if runtime == nil || runtime.transitioner == nil || runtime.contentManager == nil {
		return fmt.Errorf("%w: backup asset feature transition unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if enabled {
		if err := runtime.contentManager.PrepareEnable(ctx); err != nil {
			return err
		}
		err := runtime.transitioner.TransitionFeature(ctx, true, persist)
		if err != nil {
			runtime.contentManager.SetReady(false)
			return errors.Join(err, runtime.contentManager.PrepareDisable(ctx))
		}
		runtime.contentManager.SetReady(true)
		return nil
	}
	runtime.contentManager.SetReady(false)
	if err := runtime.contentManager.PrepareDisable(ctx); err != nil {
		return err
	}
	err := runtime.transitioner.TransitionFeature(ctx, false, persist)
	if err != nil {
		restoreErr := runtime.contentManager.PrepareEnable(ctx)
		runtime.contentManager.SetReady(restoreErr == nil)
		return errors.Join(err, restoreErr)
	}
	return nil
}

func (runtime *Runtime) PrepareApplicationDowngrade(ctx context.Context, callback func() error) error {
	if runtime == nil || runtime.transitioner == nil || runtime.contentManager == nil {
		return fmt.Errorf("%w: backup asset application downgrade unavailable", backupasset.ErrInvalidState)
	}
	runtime.contentManager.SetReady(false)
	return runtime.contentManager.PrepareSchemaDown(ctx, func() error {
		return runtime.transitioner.PrepareApplicationDowngrade(ctx, callback)
	})
}

func (runtime *Runtime) PrepareSchemaDown(ctx context.Context, callback func() error) error {
	if runtime == nil || runtime.transitioner == nil || runtime.contentManager == nil {
		return fmt.Errorf("%w: backup asset schema down unavailable", backupasset.ErrInvalidState)
	}
	runtime.contentManager.SetReady(false)
	return runtime.contentManager.PrepareSchemaDown(ctx, func() error {
		return runtime.transitioner.PrepareSchemaDown(ctx, callback)
	})
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
	if runtime == nil || runtime.admission == nil || runtime.worker == nil || runtime.healthWorker == nil || runtime.publication == nil || runtime.contentManager == nil {
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
	if err := runtime.contentManager.Startup(ctx); err != nil {
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
	if runtime == nil {
		return
	}
	if runtime.contentManager != nil {
		runtime.contentManager.StopAccepting()
	}
	if runtime.admission != nil {
		runtime.admission.StopAccepting()
	}
}

func (runtime *Runtime) Run(ctx context.Context) {
	if runtime == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var health sync.WaitGroup
	if runtime.contentManager != nil {
		health.Add(1)
		go func() {
			defer health.Done()
			runtime.contentManager.Run(runCtx)
		}()
	}
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
	if runtime.worker != nil {
		runtime.worker.Run(runCtx)
	} else {
		<-runCtx.Done()
	}
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
	var shutdownErrors []error
	runtime.StopAccepting()
	if runtime.contentManager != nil {
		if err := runtime.contentManager.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if runtime.searchWorker != nil {
		if err := runtime.searchWorker.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if runtime.catalogWorker != nil {
		if err := runtime.catalogWorker.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if runtime.worker != nil {
		if err := runtime.worker.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if runtime.healthWorker != nil {
		if err := runtime.healthWorker.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if runtime.admission != nil {
		if err := runtime.admission.Stop(ctx); err != nil && err != ErrAdmissionNotInitialized {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	return errors.Join(shutdownErrors...)
}

type managedContentRuntime struct {
	db          *gorm.DB
	foundation  *backupasset.FoundationService
	sourceRoots content.CacheRootSourceValidator
	broker      *content.Broker
	reconciler  *content.Reconciler
	ready       *atomic.Bool
	now         func() time.Time
	metrics     content.Metrics

	mu            sync.Mutex
	cache         *content.AuthenticatedCache
	cacheAttached bool
}

func (runtime *managedContentRuntime) Startup(ctx context.Context) error {
	if runtime == nil || runtime.foundation == nil || runtime.reconciler == nil || runtime.ready == nil {
		return fmt.Errorf("%w: Content runtime unavailable", backupasset.ErrInvalidState)
	}
	runtime.ready.Store(false)
	if ctx == nil {
		ctx = context.Background()
	}
	if err := runtime.reconciler.Startup(ctx); err != nil {
		return err
	}
	config, err := runtime.foundation.ContentConfig()
	if err != nil {
		return err
	}
	if config.Enabled {
		if err := runtime.ensureCache(ctx, config); err != nil {
			return err
		}
		runtime.ready.Store(true)
	}
	return nil
}

func (runtime *managedContentRuntime) PrepareEnable(ctx context.Context) error {
	if runtime == nil || runtime.reconciler == nil || runtime.foundation == nil {
		return fmt.Errorf("%w: Content runtime unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.ready.Store(false)
	if err := runtime.reconciler.Startup(ctx); err != nil {
		return err
	}
	config, err := runtime.foundation.ContentConfig()
	if err != nil {
		return err
	}
	if err := runtime.ensureCache(ctx, config); err != nil {
		return err
	}
	return runtime.broker.Resume()
}

func (runtime *managedContentRuntime) PrepareDisable(ctx context.Context) error {
	if runtime == nil || runtime.broker == nil || runtime.ready == nil {
		return fmt.Errorf("%w: Content runtime unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.ready.Store(false)
	if err := runtime.broker.Drain(ctx, "feature_disabled"); err != nil {
		return err
	}
	runtime.mu.Lock()
	cache := runtime.cache
	attached := runtime.cacheAttached
	runtime.mu.Unlock()
	if cache == nil {
		return nil
	}
	if attached {
		if err := runtime.broker.ClearCache(cache); err != nil {
			return err
		}
		runtime.mu.Lock()
		runtime.cacheAttached = false
		runtime.mu.Unlock()
	}
	if err := cache.Shutdown(ctx); err != nil {
		return err
	}
	runtime.mu.Lock()
	if runtime.cache == cache {
		runtime.cache = nil
	}
	runtime.mu.Unlock()
	return nil
}

func (runtime *managedContentRuntime) SetReady(ready bool) {
	if runtime != nil && runtime.ready != nil {
		runtime.ready.Store(ready)
	}
}

func (runtime *managedContentRuntime) StopAccepting() {
	runtime.SetReady(false)
}

func (runtime *managedContentRuntime) Run(ctx context.Context) {
	if runtime == nil || runtime.foundation == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		config, err := runtime.foundation.ContentConfig()
		interval := time.Minute
		if err == nil && config.ReconcileInterval > 0 {
			interval = config.ReconcileInterval
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		if !runtime.ready.Load() {
			continue
		}
		if runtime.reconciler != nil {
			if err := runtime.reconciler.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Module("backup_asset_content").Warn().Str("stage", "state_reconcile").Msg("备份内容状态对账失败")
			}
		}
		runtime.mu.Lock()
		cache := runtime.cache
		runtime.mu.Unlock()
		if cache != nil {
			if err := cache.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Module("backup_asset_content").Warn().Str("stage", "cache_reconcile").Msg("备份内容缓存对账失败")
			}
		}
	}
}

func (runtime *managedContentRuntime) Shutdown(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	runtime.ready.Store(false)
	if ctx == nil {
		ctx = context.Background()
	}
	var shutdownErrors []error
	if runtime.broker != nil {
		if err := runtime.broker.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if runtime.reconciler != nil {
		if err := runtime.reconciler.Startup(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	runtime.mu.Lock()
	cache := runtime.cache
	runtime.cacheAttached = false
	runtime.mu.Unlock()
	if cache != nil {
		if err := cache.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	return errors.Join(shutdownErrors...)
}

func (runtime *managedContentRuntime) PrepareSchemaDown(ctx context.Context, down func() error) error {
	if runtime == nil || runtime.db == nil || down == nil {
		return fmt.Errorf("%w: Content schema drain unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := runtime.Shutdown(ctx); err != nil {
		return err
	}
	if err := drainContentSchemaState(ctx, runtime.db); err != nil {
		return err
	}
	return down()
}

func (runtime *managedContentRuntime) ensureCache(ctx context.Context, config backupasset.ContentConfig) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.cache != nil && runtime.cacheAttached {
		return nil
	}
	if runtime.cache != nil {
		if err := runtime.cache.Shutdown(ctx); err != nil {
			return err
		}
		runtime.cache = nil
	}
	cache, err := content.NewAuthenticatedCache(ctx, content.CacheDependencies{
		Config: content.CacheConfig{
			DiskEnabled: config.Cache.Enabled, Root: config.Cache.Root, ChunkBytes: config.Cache.ChunkBytes,
			ObjectBytes: config.Cache.ObjectBytes, UserBytes: config.Cache.UserBytes,
			ProviderBytes: config.Cache.ProviderBytes, GlobalBytes: config.Cache.GlobalBytes,
			ObjectFiles: config.Cache.ObjectFiles, UserFiles: config.Cache.UserFiles,
			ProviderFiles: config.Cache.ProviderFiles, GlobalFiles: config.Cache.GlobalFiles,
			MemoryObjectBytes: config.Memory.ObjectBytes, MemoryUserBytes: config.Memory.UserBytes,
			MemoryProviderBytes: config.Memory.ProviderBytes, MemoryGlobalBytes: config.Memory.GlobalBytes,
			IdleTTL: config.Cache.IdleTTL, AbsoluteTTL: config.Cache.AbsoluteTTL,
			ReconcileBatchSize: config.ReconcileBatchSize,
		},
		Now: runtime.now, Random: rand.Reader, SourceRoots: runtime.sourceRoots, Metrics: runtime.metrics,
	})
	if err != nil {
		return err
	}
	if runtime.broker == nil {
		_ = cache.Shutdown(ctx)
		return fmt.Errorf("%w: Content Broker cache binding unavailable", backupasset.ErrInvalidState)
	}
	if err := runtime.broker.SetCache(cache); err != nil {
		_ = cache.Shutdown(ctx)
		return err
	}
	runtime.cache = cache
	runtime.cacheAttached = true
	return nil
}

func drainContentSchemaState(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var activeRequests int64
		if err := tx.Model(&model.BackupAssetDeliveryRequest{}).
			Where("state IN ?", []string{string(content.RequestReserved), string(content.RequestStreaming)}).
			Count(&activeRequests).Error; err != nil {
			return err
		}
		var activeGrants int64
		if err := tx.Model(&model.BackupAssetDeliveryGrant{}).
			Where("state IN ?", []string{string(content.DeliveryIssued), string(content.DeliveryActive), string(content.DeliveryDraining)}).
			Count(&activeGrants).Error; err != nil {
			return err
		}
		var unsafeUsage int64
		if err := tx.Model(&model.BackupAssetDeliveryUsage{}).
			Where("reserved_bytes <> 0 OR in_flight <> 0").Count(&unsafeUsage).Error; err != nil {
			return err
		}
		var activeLeases int64
		if err := tx.Model(&model.RecoveryPointLease{}).
			Where("holder_type = ? AND status = ?", backupasset.LeaseHolderContentSession, backupasset.LeaseActive).
			Count(&activeLeases).Error; err != nil {
			return err
		}
		if activeRequests != 0 || activeGrants != 0 || unsafeUsage != 0 || activeLeases != 0 {
			return fmt.Errorf("%w: Content schema state is not safely drained", backupasset.ErrConflict)
		}
		global := tx.Session(&gorm.Session{AllowGlobalUpdate: true})
		if err := global.Delete(&model.BackupAssetDeliveryRequest{}).Error; err != nil {
			return err
		}
		if err := global.Delete(&model.BackupAssetDeliveryGrant{}).Error; err != nil {
			return err
		}
		if err := global.Delete(&model.BackupAssetDeliveryUsage{}).Error; err != nil {
			return err
		}
		if err := tx.Where("holder_type = ? AND status IN ?", backupasset.LeaseHolderContentSession,
			[]string{string(backupasset.LeaseReleased), string(backupasset.LeaseExpired)}).
			Delete(&model.RecoveryPointLease{}).Error; err != nil {
			return err
		}
		return nil
	})
}

func runtimeContentBudgetLimits(config backupasset.ContentConfig) content.BudgetLimits {
	return content.BudgetLimits{
		Window: config.RateWindow,
		Global: content.BudgetScopeLimits{
			WindowBytes: config.Global.WindowBytes, WindowRequests: config.Global.WindowRequests,
			MaxInFlight: config.Global.MaxConcurrency,
		},
		Provider: content.BudgetScopeLimits{
			WindowBytes: config.Provider.WindowBytes, WindowRequests: config.Provider.WindowRequests,
			MaxInFlight: config.Provider.MaxConcurrency,
		},
		User: content.BudgetScopeLimits{
			WindowBytes: config.User.WindowBytes, WindowRequests: config.User.WindowRequests,
			MaxInFlight: config.User.MaxConcurrency,
		},
	}
}

func runtimeContentBrokerConfig(config backupasset.ContentConfig) content.BrokerConfig {
	return content.BrokerConfig{
		TicketTimeout: config.TicketTimeout, WriteIdleTimeout: config.WriteIdleTimeout,
		LeaseHeartbeat: config.LeaseHeartbeat,
		PreviewTTL:     config.PreviewTTL, MediaTTL: config.MediaTTL, IdleTTL: config.IdleTTL,
		MaxBytesPerRequest: config.RequestMaxBytes, MaxCumulativeBytes: config.CumulativeMaxBytes,
		MaxRequests: config.MaxRequests, MaxInFlight: config.GrantMaxInFlight,
		Classification: content.ClassificationConfig{ScanBytes: config.ClassificationScanBytes},
		Renderer: content.RendererConfig{
			TextBytes: config.TextPreviewBytes, HexBytes: config.HexPreviewBytes,
			RasterMaxPixels: config.RasterMaxPixels, PDFMaxBytes: config.RequestMaxBytes,
			MediaMaxBytes: config.Cache.ObjectBytes,
		},
	}
}

type runtimeContentSessionValidator struct {
	db          *gorm.DB
	revocations ContentSessionRevocationSource
	now         func() time.Time
}

func newRuntimeContentSessionValidator(
	db *gorm.DB,
	revocations ContentSessionRevocationSource,
	now func() time.Time,
) (*runtimeContentSessionValidator, error) {
	if db == nil || revocations == nil || now == nil {
		return nil, fmt.Errorf("%w: Content session validator dependencies unavailable", backupasset.ErrInvalidState)
	}
	return &runtimeContentSessionValidator{db: db, revocations: revocations, now: now}, nil
}

func (validator *runtimeContentSessionValidator) Validate(ctx context.Context, session content.DeliverySession) error {
	if validator == nil || validator.db == nil || validator.revocations == nil || session.UserID == 0 ||
		backupasset.ValidateOpaqueID(session.JTI) != nil || (session.Role != "admin" && session.Role != "operator") ||
		!session.ExpiresAt.UTC().After(validator.now().UTC()) {
		return fmt.Errorf("%w: invalid Content session", backupasset.ErrForbidden)
	}
	revoked, err := validator.revocations.IsSessionRevoked(session.JTI)
	if err != nil || revoked {
		return fmt.Errorf("%w: revoked Content session", backupasset.ErrForbidden)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var current struct {
		ID           uint
		Role         string
		TokenVersion uint
	}
	result := validator.db.WithContext(ctx).Model(&model.User{}).
		Select("id", "role", "token_version").Where("id = ?", session.UserID).Limit(1).Scan(&current)
	if result.Error != nil || result.RowsAffected != 1 || current.ID != session.UserID ||
		current.Role != session.Role || current.TokenVersion != session.TokenVersion {
		return fmt.Errorf("%w: Content session binding changed", backupasset.ErrForbidden)
	}
	return nil
}

type runtimeContentAuthorizer struct {
	db        *gorm.DB
	ownership *catalog.Ownership
}

type runtimeContentAssetRecord struct {
	CatalogGenerationID          string
	GenerationSourceFingerprint  string
	RepositoryID                 string
	RepositoryProvider           string
	RepositoryStatus             string
	RepositoryCapability         int
	RepositoryCapabilitiesJSON   string
	PointSemantics               string
	PointState                   string
	PointSourceFingerprint       string
	PointCapability              int
	PointPhysicalAvailability    string
	PointRetiredAt               *time.Time
	EntryType                    string
	EntrySize                    int64
	EntryModifiedAt              *time.Time
	EntryMediaType               string
	EntryPath                    string
	EntryName                    string
	EntryFingerprint             string
	EntryFingerprintStrength     string
	EntrySecurityState           string
	SearchClassification         *string
	SearchClassificationRevision *int64
}

func newRuntimeContentAuthorizer(db *gorm.DB, ownership *catalog.Ownership) (*runtimeContentAuthorizer, error) {
	if db == nil || ownership == nil {
		return nil, fmt.Errorf("%w: Content authorizer dependencies unavailable", backupasset.ErrInvalidState)
	}
	return &runtimeContentAuthorizer{db: db, ownership: ownership}, nil
}

func (authorizer *runtimeContentAuthorizer) Authorize(
	ctx context.Context,
	actor content.DeliveryActor,
	ref backupasset.AssetRef,
	action content.DeliveryAction,
) (content.AuthorizedAsset, error) {
	if !runtimeContentActionAllowed(actor, action) || backupasset.ValidateAssetRef(ref) != nil {
		return content.AuthorizedAsset{}, fmt.Errorf("%w: Content action", backupasset.ErrForbidden)
	}
	return authorizer.load(ctx, actor, ref, "")
}

func (authorizer *runtimeContentAuthorizer) Reauthorize(
	ctx context.Context,
	actor content.DeliveryActor,
	expected content.AuthorizedAsset,
	action content.DeliveryAction,
) error {
	if !runtimeContentActionAllowed(actor, action) || backupasset.ValidateAssetRef(expected.Ref) != nil ||
		backupasset.ValidateOpaqueID(expected.CatalogGenerationID) != nil {
		return fmt.Errorf("%w: Content reauthorization", backupasset.ErrForbidden)
	}
	current, err := authorizer.load(ctx, actor, expected.Ref, expected.CatalogGenerationID)
	if err != nil {
		return err
	}
	if current.Ref != expected.Ref || current.CatalogGenerationID != expected.CatalogGenerationID ||
		current.RepositoryID != expected.RepositoryID || current.Provider != expected.Provider ||
		current.SourceFingerprint != expected.SourceFingerprint ||
		current.EntryFingerprint != expected.EntryFingerprint || current.FingerprintStrength != expected.FingerprintStrength ||
		current.Size != expected.Size || !sameRuntimeContentTime(current.ModifiedAt, expected.ModifiedAt) ||
		current.Path != expected.Path || current.Name != expected.Name || current.MediaType != expected.MediaType ||
		current.SearchClassification != expected.SearchClassification ||
		current.SearchClassificationRevision != expected.SearchClassificationRevision ||
		expected.RangeProven && !current.RangeProven {
		return fmt.Errorf("%w: Content asset binding changed", backupasset.ErrConflict)
	}
	return nil
}

func (authorizer *runtimeContentAuthorizer) load(
	ctx context.Context,
	actor content.DeliveryActor,
	ref backupasset.AssetRef,
	expectedGeneration string,
) (content.AuthorizedAsset, error) {
	if authorizer == nil || authorizer.db == nil || authorizer.ownership == nil {
		return content.AuthorizedAsset{}, fmt.Errorf("%w: Content authorizer unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	authorized, err := authorizer.ownership.AuthorizedPointIDs(ctx,
		catalog.AuthorizationScope{Role: actor.Role, UserID: actor.UserID}, []string{ref.RecoveryPointID})
	if err != nil {
		return content.AuthorizedAsset{}, err
	}
	if len(authorized) != 1 || authorized[0] != ref.RecoveryPointID {
		return content.AuthorizedAsset{}, fmt.Errorf("%w: Content asset", backupasset.ErrNotFound)
	}
	query := authorizer.db.WithContext(ctx).Table("catalog_entries AS entries").
		Select(`entries.generation_id AS catalog_generation_id,
			generations.source_fingerprint AS generation_source_fingerprint,
			points.repository_id AS repository_id, points.semantics AS point_semantics,
			points.state AS point_state, points.source_fingerprint AS point_source_fingerprint,
			points.capability_revision AS point_capability, points.physical_availability AS point_physical_availability,
			points.retired_at AS point_retired_at,
			repositories.provider_kind AS repository_provider, repositories.status AS repository_status,
			repositories.capability_revision AS repository_capability,
			repositories.capabilities_json AS repository_capabilities_json,
			entries.entry_type AS entry_type, entries.size AS entry_size,
			entries.modified_at AS entry_modified_at, entries.mime_type AS entry_media_type,
			entries.normalized_path AS entry_path, entries.name AS entry_name,
			entries.fingerprint AS entry_fingerprint,
			entries.fingerprint_strength AS entry_fingerprint_strength,
			entries.security_state AS entry_security_state,
			search_documents.sensitivity AS search_classification,
			search_documents.classification_revision AS search_classification_revision`).
		Joins("JOIN catalog_generations AS generations ON generations.id = entries.generation_id AND generations.recovery_point_id = entries.recovery_point_id").
		Joins("JOIN recovery_points AS points ON points.id = entries.recovery_point_id").
		Joins("JOIN backup_repositories AS repositories ON repositories.id = points.repository_id").
		Joins(`LEFT JOIN backup_asset_search_generations AS search_generations
			ON search_generations.recovery_point_id = entries.recovery_point_id
			AND search_generations.catalog_generation_id = entries.generation_id
			AND search_generations.source_fingerprint = generations.source_fingerprint
			AND search_generations.state = ? AND search_generations.is_active = ?
			AND search_generations.finished_at IS NOT NULL
			AND search_generations.expected_document_count = search_generations.written_document_count`,
			search.SearchGenerationComplete, true).
		Joins(`LEFT JOIN backup_asset_search_documents AS search_documents
			ON search_documents.search_generation_id = search_generations.id
			AND search_documents.recovery_point_id = entries.recovery_point_id
			AND search_documents.catalog_generation_id = entries.generation_id
			AND search_documents.entry_id = entries.entry_id
			AND search_documents.document_id = entries.entry_id`).
		Where("entries.recovery_point_id = ? AND entries.entry_id = ? AND generations.state = ? AND generations.is_active = ?",
			ref.RecoveryPointID, ref.EntryID, catalog.GenerationComplete, true)
	if expectedGeneration != "" {
		query = query.Where("generations.id = ?", expectedGeneration)
	}
	var rows []runtimeContentAssetRecord
	if err := query.Limit(2).Scan(&rows).Error; err != nil {
		return content.AuthorizedAsset{}, fmt.Errorf("load Content authorization binding: %w", err)
	}
	if len(rows) != 1 {
		return content.AuthorizedAsset{}, fmt.Errorf("%w: Content asset", backupasset.ErrNotFound)
	}
	record := rows[0]
	providerKind := backupasset.ProviderKind(record.RepositoryProvider)
	strength, strengthErr := catalog.ParseFingerprintStrength(record.EntryFingerprintStrength)
	if strengthErr != nil || !runtimeContentPointVisible(record) || record.PointRetiredAt != nil ||
		record.RepositoryStatus != string(backupasset.RepositoryOnline) ||
		record.RepositoryCapability <= 0 || record.RepositoryCapability != record.PointCapability ||
		providerKind == backupasset.ProviderCommand ||
		(providerKind != backupasset.ProviderRestic && providerKind != backupasset.ProviderRsync && providerKind != backupasset.ProviderRclone) ||
		record.GenerationSourceFingerprint == "" || record.GenerationSourceFingerprint != record.PointSourceFingerprint ||
		record.EntryType != string(backupasset.CatalogEntryFile) || record.EntrySize < 0 ||
		strings.TrimSpace(record.EntryPath) == "" || strings.TrimSpace(record.EntryName) == "" ||
		record.EntrySecurityState != "sealed" {
		return content.AuthorizedAsset{}, fmt.Errorf("%w: Content asset binding", backupasset.ErrConflict)
	}
	var capabilities backupasset.CapabilitySet
	if err := json.Unmarshal([]byte(record.RepositoryCapabilitiesJSON), &capabilities); err != nil || !capabilities.OpenSequential {
		return content.AuthorizedAsset{}, fmt.Errorf("%w: Content source capability", backupasset.ErrCapabilityUnavailable)
	}
	modifiedAt := record.EntryModifiedAt
	if modifiedAt != nil {
		value := modifiedAt.UTC()
		modifiedAt = &value
	}
	searchClassification, searchClassificationRevision, err := runtimeSearchClassification(record)
	if err != nil {
		return content.AuthorizedAsset{}, err
	}
	return content.AuthorizedAsset{
		Ref: ref, CatalogGenerationID: record.CatalogGenerationID, RepositoryID: record.RepositoryID,
		Provider: providerKind, SourceFingerprint: record.GenerationSourceFingerprint,
		EntryFingerprint: record.EntryFingerprint, FingerprintStrength: string(strength),
		Size: record.EntrySize, ModifiedAt: modifiedAt, MediaType: record.EntryMediaType,
		Path: record.EntryPath, Name: record.EntryName, RangeProven: capabilities.OpenRange,
		SearchClassification: searchClassification, SearchClassificationRevision: searchClassificationRevision,
	}, nil
}

func runtimeSearchClassification(record runtimeContentAssetRecord) (content.Classification, int64, error) {
	if record.SearchClassification == nil && record.SearchClassificationRevision == nil {
		return "", 0, nil
	}
	if record.SearchClassification == nil || record.SearchClassificationRevision == nil || *record.SearchClassificationRevision <= 0 {
		return "", 0, fmt.Errorf("%w: Content Search classification binding", backupasset.ErrConflict)
	}
	classification := content.Classification(*record.SearchClassification)
	switch classification {
	case content.ClassificationNonSecret, content.ClassificationSecret, content.ClassificationUnknown:
		return classification, *record.SearchClassificationRevision, nil
	default:
		return "", 0, fmt.Errorf("%w: Content Search classification binding", backupasset.ErrConflict)
	}
}

func runtimeContentActionAllowed(actor content.DeliveryActor, action content.DeliveryAction) bool {
	if actor.UserID == 0 || (actor.Role != "admin" && actor.Role != "operator") {
		return false
	}
	return action == content.DeliveryPreview || action == content.DeliveryDownload && actor.Role == "admin"
}

func runtimeContentPointVisible(record runtimeContentAssetRecord) bool {
	if record.PointPhysicalAvailability != string(backupasset.PhysicalOnline) {
		return false
	}
	switch backupasset.PointVersionSemantics(record.PointSemantics) {
	case backupasset.PointMutableHead:
		return backupasset.RecoveryPointState(record.PointState) == backupasset.RecoveryPointObserved
	case backupasset.PointNativeSnapshot, backupasset.PointXirangManifest, backupasset.PointImportedBaseline:
		state := backupasset.RecoveryPointState(record.PointState)
		return state == backupasset.RecoveryPointCommitted || state == backupasset.RecoveryPointDegraded
	default:
		return false
	}
}

func sameRuntimeContentTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.UTC().Equal(right.UTC())
}

var _ content.DeliverySessionValidator = (*runtimeContentSessionValidator)(nil)
var _ content.AssetAuthorizer = (*runtimeContentAuthorizer)(nil)
var _ publication.FeatureTransitioner = (*Runtime)(nil)

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
