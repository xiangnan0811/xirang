package runtime

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/repository"
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
	metrics        publication.Metrics

	mu        sync.Mutex
	starting  bool
	observer  publication.CommitObserver
	reporter  publication.InterruptedRunReporter
	readiness publication.InterruptedRunReadiness
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

	metricsSink := dependencies.Metrics
	if metricsSink == nil {
		prometheusMetrics, err := publication.NewPrometheusMetrics(prometheus.DefaultRegisterer)
		if err != nil {
			return nil, err
		}
		metricsSink = prometheusMetrics
	}
	keyring := backupasset.NewKeyring(dependencies.DB, dependencies.Now)
	auditWriter, err := backupasset.NewAuditWriterWithConfigSource(dependencies.DB, keyring, dependencies.Now, foundation.AuditConfig)
	if err != nil {
		return nil, err
	}
	auditSink := repository.NewAssetAuditSink(auditWriter)
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
	registry := provider.NewRegistry()
	for _, registration := range []struct {
		kind  backupasset.ProviderKind
		value provider.Registration
	}{
		{backupasset.ProviderRsync, provider.Registration{Prober: rsyncAdapter, PointLister: rsyncAdapter, EntryLister: rsyncAdapter, EntryStatter: rsyncAdapter, SequentialReader: rsyncAdapter, RangeReader: rsyncAdapter, PublicationStrategy: rsyncStrategy}},
		{backupasset.ProviderRestic, provider.Registration{Prober: resticAdapter, PointLister: resticAdapter, EntryLister: resticAdapter, EntryStatter: resticAdapter, SequentialReader: resticAdapter, PublicationStrategy: resticStrategy}},
		{backupasset.ProviderRclone, provider.Registration{Prober: rcloneAdapter, PointLister: rcloneAdapter, EntryLister: rcloneAdapter, EntryStatter: rcloneAdapter, SequentialReader: rcloneAdapter, RangeReader: rcloneAdapter, PublicationStrategy: rcloneStrategy}},
	} {
		if err := registry.Register(registration.kind, registration.value); err != nil {
			return nil, err
		}
	}
	lease, err := backupasset.NewLeaseService(dependencies.DB, dependencies.Now, leaseConfig)
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
	})
	if err != nil {
		return nil, err
	}
	return &Runtime{foundation: foundation, repository: repositoryService, publication: publicationService, resticStrategy: resticStrategy, rsyncStrategy: rsyncStrategy, rcloneStrategy: rcloneStrategy, admission: admission, worker: worker, healthWorker: healthWorker, metrics: metricsSink}, nil
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
func (runtime *Runtime) PublicationCoordinator() publication.Coordinator   { return runtime.publication }
func (runtime *Runtime) PublicationReconciler() publication.Reconciler     { return runtime.publication }
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
