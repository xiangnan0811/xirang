package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	assetexport "xirang/backend/internal/backupasset/export"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	processingupdater "xirang/backend/internal/backupasset/processing/updater"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/recovery"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/backupasset/retention"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

const (
	runtimeProviderMaxPageSize   = 200
	runtimeProviderCursorTTL     = 15 * time.Minute
	runtimeSearchExcerptMaxBytes = 16 << 20
	runtimeSearchSnippetMaxBytes = 4 << 10
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
	RecoveryMetrics    recovery.Metrics
	ProcessingMetrics  processing.Metrics
	CatalogMetrics     catalog.Metrics
	SearchMetrics      search.Metrics
	GAMetrics          ga.Metrics
	ContentMetrics     content.Metrics
	SessionRevocations ContentSessionRevocationSource
	Tombstones         repository.ManagedHistoryTombstoneSource
	AlertDispatcher    *alerting.Dispatcher
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

type exportRuntimeManager interface {
	Startup(context.Context) error
	Ready() bool
	TransitionSettings(context.Context, bool, backupasset.ExportConfig, func() error) error
	Service() *managedExportServiceFacade
	Delivery() *managedExportDeliveryFacade
	StopAccepting()
	Run(context.Context)
	Shutdown(context.Context) error
	PrepareSchemaDown(context.Context, func() error) error
}

type recoveryRuntimeManager interface {
	StartupWithConfig(context.Context, backupasset.RecoveryConfig) error
	TransitionSettingsWithRestore(
		context.Context,
		backupasset.RecoveryConfig,
		func() error,
		func() error,
	) error
	DowngradeReadiness(context.Context) (RecoveryDowngradeReadiness, error)
	StopAccepting()
	Run(context.Context)
	Shutdown(context.Context) error
	PrepareSchemaDown(context.Context, func() error) error
}

type runtimeStopTerminalizer interface {
	TerminalizeForRuntimeStopPass(context.Context, int) (assetexport.RuntimeStopTerminalizationProgress, error)
}

func terminalizeExportRuntimeLifecycle(
	ctx context.Context,
	terminalizer runtimeStopTerminalizer,
	batchSize int,
	reconcileOrphans func(context.Context) (int, error),
) error {
	if terminalizer == nil || batchSize <= 0 || reconcileOrphans == nil {
		return assetexport.ErrUnavailable
	}
	var terminalizeErr error
	for {
		progress, err := terminalizer.TerminalizeForRuntimeStopPass(ctx, batchSize)
		terminalizeErr = errors.Join(terminalizeErr, err)
		if progress.Complete {
			break
		}
		if progress.Advanced == 0 {
			if terminalizeErr != nil {
				return terminalizeErr
			}
			return assetexport.ErrUnavailable
		}
	}
	_, orphanErr := reconcileOrphans(ctx)
	return errors.Join(terminalizeErr, orphanErr)
}

// Runtime is the single composition root for Repository reads, publication,
// admission, and guarded legacy Restic callers. It does not own Task Manager;
// callback ports are set explicitly before StartupPass.
type Runtime struct {
	foundation              *backupasset.FoundationService
	settings                *settings.Service
	repository              *repository.Service
	publication             *repository.PublicationService
	resticStrategy          provider.PublicationStrategy
	rsyncStrategy           provider.PublicationStrategy
	rcloneStrategy          provider.PublicationStrategy
	admission               *AdmissionController
	worker                  *PublicationWorker
	healthWorker            *RcloneHealthWorker
	catalogService          *catalog.Service
	catalogIndexer          *catalog.Indexer
	catalogWorker           *CatalogWorker
	catalogAudit            repository.AssetAuditSink
	keyring                 *backupasset.Keyring
	searchService           *search.Service
	searchIndexer           *search.Indexer
	searchIngest            *search.ContentIngestService
	searchWorker            *SearchWorker
	overlayService          *overlay.Service
	searchReady             *atomic.Bool
	contentBroker           *content.Broker
	contentService          *contentDeliveryMux
	exportDelivery          *managedExportDeliveryFacade
	contentBudget           *content.BudgetService
	contentAudit            *content.ContentAuditService
	contentReconciler       *content.Reconciler
	contentReady            *atomic.Bool
	contentManager          contentRuntimeManager
	exportManager           exportRuntimeManager
	recoveryManager         recoveryRuntimeManager
	recoverySourceNamespace recovery.RecoverySourceNamespaceAuthority
	recoveryTargetRoots     *managedRecoveryTargetRootFacade
	recoveryDowngrade       *managedRecoveryDowngradeFacade
	nodeWriteCoordinator    *NodeWriteCoordinator
	recoveryAuthorization   *managedRecoveryAuthorizationFacade
	recoveryAPI             *recovery.APIService
	recoveryOperations      *managedRecoveryAPIFacade
	recoveryResults         *managedRecoveryResultFacade
	processingManager       *managedProcessingRuntime
	archiveMemberService    *managedArchiveMemberFacade
	retentionWorker         *retention.Worker
	retentionPolicies       *retention.PolicyService
	retentionHolds          *retention.HoldService
	retentionPurge          *retention.PurgeService
	managedTaskRetention    task.ManagedRecoveryPointRetention
	inventory               *ga.InventoryService
	enablement              ga.ReadinessSource
	transitioner            publication.FeatureTransitioner
	metrics                 publication.Metrics
	gaMetrics               ga.Metrics

	mu          sync.Mutex
	searchKeyMu sync.Mutex
	starting    bool
	observer    publication.CommitObserver
	reporter    publication.InterruptedRunReporter
	readiness   publication.InterruptedRunReadiness
}

// managedRecoverySourceNamespaceAdapter keeps Repository dependent only on
// its own ownership-transfer port while Recovery retains the opaque proof and
// observed source capability. Runtime is the sole composition boundary that
// translates the scalar request between the two packages.
type managedRecoverySourceNamespaceAdapter struct {
	authority recovery.RecoverySourceNamespaceAuthority
}

func (adapter *managedRecoverySourceNamespaceAdapter) ObserveRecoverySourceNamespace(
	ctx context.Context,
	request repository.RecoverySourceNamespaceRequest,
	pinned provider.RsyncRestoreSource,
) (provider.RsyncRestoreSource, error) {
	if adapter == nil || adapter.authority == nil {
		if pinned != nil {
			_ = pinned.Close()
		}
		return nil, fmt.Errorf("%w: recovery source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	return adapter.authority.ObserveRecoverySourceNamespace(ctx, recovery.RecoverySourceNamespaceRequest{
		SourceRef:                 request.SourceRef,
		ProducingTaskID:           request.ProducingTaskID,
		RepositoryBindingRevision: request.RepositoryBindingRevision,
		ProvenanceRevision:        request.ProvenanceRevision,
	}, pinned)
}

var _ repository.RecoverySourceNamespaceAuthority = (*managedRecoverySourceNamespaceAdapter)(nil)

// RecoveryTargetRootAuthority is the narrow Task 9 composition seam. It
// exposes only the reviewed authority operations and never the registry,
// ciphertext, probe session, or private locator outside Recovery.
type RecoveryTargetRootAuthority interface {
	ReplayRegistration(
		context.Context,
		recovery.TargetRootRegistrationRequest,
	) (settings.RecoveryTargetRootSummary, bool, error)
	Register(
		context.Context,
		recovery.TargetRootRegistrationRequest,
	) (settings.RecoveryTargetRootSummary, error)
	Delete(context.Context, uint, string) error
	DeleteAuthorized(
		context.Context,
		recovery.TargetRootDeletionRequest,
	) (settings.RecoveryTargetRootSummary, error)
	ReplayDeletion(
		context.Context,
		recovery.TargetRootDeletionRequest,
	) (settings.RecoveryTargetRootSummary, bool, error)
	List(context.Context, uint, uint) ([]settings.RecoveryTargetRootSummary, error)
}

type recoveryTargetRootMutationService interface {
	ReplayRegistration(
		context.Context,
		recovery.TargetRootRegistrationRequest,
	) (settings.RecoveryTargetRootSummary, bool, error)
	ValidateRegistration(recovery.TargetRootRegistrationRequest) error
	ValidateDelete(uint, string) error
	RegisterMutation(
		context.Context,
		recovery.TargetRootRegistrationRequest,
	) (settings.RecoveryTargetRootSummary, recovery.TargetRootMutationRollback, error)
	DeleteMutation(context.Context, uint, string) (recovery.TargetRootMutationRollback, error)
	DeleteAuthorizedMutation(
		context.Context,
		recovery.TargetRootDeletionRequest,
	) (settings.RecoveryTargetRootSummary, recovery.TargetRootMutationRollback, error)
	ReplayDeletion(
		context.Context,
		recovery.TargetRootDeletionRequest,
	) (settings.RecoveryTargetRootSummary, bool, error)
	RestoreMutation(context.Context, recovery.TargetRootMutationRollback) error
	List(context.Context, uint) ([]settings.RecoveryTargetRootSummary, error)
}

type recoveryTargetRootTransitionOwner interface {
	TransitionCurrentWithRestore(context.Context, func() error, func() error) error
}

type managedRecoveryTargetRootFacade struct {
	service recoveryTargetRootMutationService
	runtime recoveryTargetRootTransitionOwner
	audit   recoveryAdministrationAuditWriter
}

func (facade *managedRecoveryTargetRootFacade) ReplayRegistration(
	ctx context.Context,
	request recovery.TargetRootRegistrationRequest,
) (settings.RecoveryTargetRootSummary, bool, error) {
	if facade == nil || facade.service == nil {
		return settings.RecoveryTargetRootSummary{}, false, recovery.ErrRecoveryTargetUnavailable
	}
	return facade.service.ReplayRegistration(ctx, request)
}

func (facade *managedRecoveryTargetRootFacade) Register(
	ctx context.Context,
	request recovery.TargetRootRegistrationRequest,
) (settings.RecoveryTargetRootSummary, error) {
	if facade == nil || facade.service == nil || facade.runtime == nil {
		return settings.RecoveryTargetRootSummary{}, recovery.ErrRecoveryTargetUnavailable
	}
	if err := facade.service.ValidateRegistration(request); err != nil {
		return settings.RecoveryTargetRootSummary{}, err
	}
	var result settings.RecoveryTargetRootSummary
	var rollback recovery.TargetRootMutationRollback
	mutationApplied := false
	err := facade.runtime.TransitionCurrentWithRestore(ctx, func() error {
		var mutationErr error
		result, rollback, mutationErr = facade.service.RegisterMutation(ctx, request)
		mutationApplied = mutationErr == nil
		return mutationErr
	}, func() error {
		if !mutationApplied {
			return nil
		}
		return facade.restoreMutation(rollback)
	})
	if err != nil {
		return settings.RecoveryTargetRootSummary{}, err
	}
	operation := "target_root_register"
	if request.Mutation == recovery.TargetRootMutationRotate {
		operation = "target_root_rotate"
	}
	if !rollback.Replay() {
		writeRecoveryAdministrationAudit(ctx, facade.audit, request.RequesterID, operation, "succeeded", 1)
	}
	return result, nil
}

func (facade *managedRecoveryTargetRootFacade) Delete(ctx context.Context, nodeID uint, rootID string) error {
	if facade == nil || facade.service == nil || facade.runtime == nil {
		return recovery.ErrRecoveryTargetUnavailable
	}
	if err := facade.service.ValidateDelete(nodeID, rootID); err != nil {
		return err
	}
	var rollback recovery.TargetRootMutationRollback
	mutationApplied := false
	return facade.runtime.TransitionCurrentWithRestore(ctx, func() error {
		var mutationErr error
		rollback, mutationErr = facade.service.DeleteMutation(ctx, nodeID, rootID)
		mutationApplied = mutationErr == nil
		return mutationErr
	}, func() error {
		if !mutationApplied {
			return nil
		}
		return facade.restoreMutation(rollback)
	})
}

func (facade *managedRecoveryTargetRootFacade) DeleteAuthorized(
	ctx context.Context,
	request recovery.TargetRootDeletionRequest,
) (settings.RecoveryTargetRootSummary, error) {
	if facade == nil || facade.service == nil || facade.runtime == nil {
		return settings.RecoveryTargetRootSummary{}, recovery.ErrRecoveryTargetUnavailable
	}
	if err := facade.service.ValidateDelete(request.NodeID, request.RootID); err != nil {
		return settings.RecoveryTargetRootSummary{}, err
	}
	var result settings.RecoveryTargetRootSummary
	var rollback recovery.TargetRootMutationRollback
	mutationApplied := false
	err := facade.runtime.TransitionCurrentWithRestore(ctx, func() error {
		var mutationErr error
		result, rollback, mutationErr = facade.service.DeleteAuthorizedMutation(ctx, request)
		mutationApplied = mutationErr == nil
		return mutationErr
	}, func() error {
		if !mutationApplied {
			return nil
		}
		return facade.restoreMutation(rollback)
	})
	if err != nil {
		return settings.RecoveryTargetRootSummary{}, err
	}
	if !rollback.Replay() {
		writeRecoveryAdministrationAudit(ctx, facade.audit, request.RequesterID, "target_root_delete", "succeeded", 1)
	}
	return result, nil
}

func (facade *managedRecoveryTargetRootFacade) ReplayDeletion(
	ctx context.Context,
	request recovery.TargetRootDeletionRequest,
) (settings.RecoveryTargetRootSummary, bool, error) {
	if facade == nil || facade.service == nil {
		return settings.RecoveryTargetRootSummary{}, false, recovery.ErrRecoveryTargetUnavailable
	}
	return facade.service.ReplayDeletion(ctx, request)
}

func (facade *managedRecoveryTargetRootFacade) restoreMutation(rollback recovery.TargetRootMutationRollback) error {
	restoreCtx, cancel := context.WithTimeout(context.Background(), recoveryRuntimeTransitionTimeout)
	defer cancel()
	return facade.service.RestoreMutation(restoreCtx, rollback)
}

func (facade *managedRecoveryTargetRootFacade) List(
	ctx context.Context,
	requesterID uint,
	nodeID uint,
) ([]settings.RecoveryTargetRootSummary, error) {
	if facade == nil || facade.service == nil {
		return nil, recovery.ErrRecoveryTargetUnavailable
	}
	items, err := facade.service.List(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	writeRecoveryAdministrationAudit(ctx, facade.audit, requesterID, "target_root_list", "succeeded", int64(len(items)))
	return items, nil
}

var _ RecoveryTargetRootAuthority = (*managedRecoveryTargetRootFacade)(nil)

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
	recoveryMetrics := dependencies.RecoveryMetrics
	if recoveryMetrics == nil {
		if dependencies.Metrics == nil {
			recoveryMetrics, err = recovery.NewPrometheusMetrics(prometheus.DefaultRegisterer)
			if err != nil {
				return nil, err
			}
		} else {
			recoveryMetrics = recovery.NoopMetrics{}
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
	tombstones := dependencies.Tombstones
	if tombstones == nil {
		constructed, tombstoneErr := repository.NewLifecycleManagedHistoryTombstones(dependencies.DB)
		if tombstoneErr != nil {
			return nil, tombstoneErr
		}
		tombstones = constructed
	}
	history, err := repository.NewManagedHistoryResolver(repository.ManagedHistoryResolverDependencies{DB: dependencies.DB, Tombstones: tombstones})
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
			return nil, reconcilePermanentCleanupKeyLossBeforeReturn(ownershipErr, dependencies, foundation)
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
	resticDeleter, err := provider.NewResticPointDeleter(transport, streamTransport, limitsSource, dependencies.Now)
	if err != nil {
		return nil, err
	}
	rsyncDeleter, err := provider.NewRsyncPointDeleter(dependencies.Now)
	if err != nil {
		return nil, err
	}
	rclonePrefixDeleter, err := provider.NewRclonePrefixPointDeleter(transport, limitsSource, dependencies.Now)
	if err != nil {
		return nil, err
	}
	rcloneDeleter, err := provider.NewRclonePointDeleter(rclonePrefixDeleter, dependencies.Now)
	if err != nil {
		return nil, err
	}
	registry := provider.NewRegistry()
	for _, registration := range []struct {
		kind  backupasset.ProviderKind
		value provider.Registration
	}{
		{backupasset.ProviderRsync, provider.Registration{Prober: rsyncAdapter, PointLister: rsyncAdapter, EntryLister: rsyncAdapter, EntryStatter: rsyncAdapter, SequentialReader: rsyncAdapter, RangeReader: rsyncAdapter, CatalogReader: rsyncAdapter, PublicationStrategy: rsyncStrategy, PointDeleter: rsyncDeleter}},
		{backupasset.ProviderRestic, provider.Registration{Prober: resticAdapter, PointLister: resticAdapter, EntryLister: resticAdapter, EntryStatter: resticAdapter, SequentialReader: resticAdapter, CatalogReader: resticAdapter, PublicationStrategy: resticStrategy, PointDeleter: resticDeleter}},
		{backupasset.ProviderRclone, provider.Registration{Prober: rcloneAdapter, PointLister: rcloneAdapter, EntryLister: rcloneAdapter, EntryStatter: rcloneAdapter, SequentialReader: rcloneAdapter, RangeReader: rcloneAdapter, CatalogReader: rcloneCatalogReader, PublicationStrategy: rcloneStrategy, PointDeleter: rcloneDeleter}},
	} {
		if err := registry.Register(registration.kind, registration.value); err != nil {
			return nil, err
		}
	}
	lease, err := backupasset.NewLeaseService(dependencies.DB, dependencies.Now, leaseConfig)
	if err != nil {
		return nil, err
	}
	var processingManager *managedProcessingRuntime
	derivedSourceResolver := runtimeDerivedSourceAssetResolver(dependencies.DB)
	searchExcerpts := &runtimeSearchExcerptResolver{
		db:           dependencies.DB,
		resolveAsset: derivedSourceResolver,
		readArtifact: func(ctx context.Context, request content.DerivedArtifactRead, destination io.Writer) error {
			if processingManager == nil {
				return content.ErrDerivedRepresentationUnavailable
			}
			return processingManager.ReadDerivedArtifact(ctx, request, destination)
		},
		activePipeline: func(ctx context.Context, capability, outputProfile string) (string, error) {
			if processingManager == nil {
				return "", content.ErrDerivedRepresentationUnavailable
			}
			return processingManager.activePipelineFingerprint(ctx, capability, outputProfile)
		},
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
		DB: dependencies.DB, Scope: searchScope, Keys: keyring, Excerpts: searchExcerpts,
		Cursor: search.NewCursorCodec(keyring, dependencies.Now, runtimeProviderCursorTTL), Tags: overlayService, Now: dependencies.Now,
		Limits:         search.ServiceLimits{Query: searchQueryLimits, MaxCandidates: searchConfig.CandidateLimit, ExecutionTimeout: searchConfig.QueryTimeout},
		FeatureEnabled: foundation.FeatureEnabled,
		PipelineRevisions: func(ctx context.Context) (search.ContentPipelineRevisions, error) {
			revisions, revisionErr := dependencies.Settings.ProcessingPipelineRevisions(ctx)
			if revisionErr != nil {
				return search.ContentPipelineRevisions{}, revisionErr
			}
			return search.ContentPipelineRevisions{Content: revisions.Content, OCR: revisions.OCR}, nil
		},
		MalwareSafety: func(ctx context.Context, request search.MalwareSafetyRequest) (bool, error) {
			if processingManager == nil {
				return false, content.ErrDerivedRepresentationUnavailable
			}
			decision, safetyErr := processingManager.malwareSafetyForAsset(ctx, content.AuthorizedAsset{
				Ref: request.Ref, CatalogGenerationID: request.CatalogGenerationID,
				SourceFingerprint: request.SourceFingerprint, EntryFingerprint: request.EntryFingerprint,
				FingerprintStrength: request.FingerprintStrength, ProviderCapabilityRevision: request.ProviderCapabilityRevision,
				Size: request.Size, MediaType: request.MediaType,
			})
			return safetyErr == nil && decision.Safe, safetyErr
		},
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
		if dependencies.Metrics == nil {
			searchMetrics, err = search.NewPrometheusMetrics(prometheus.DefaultRegisterer)
			if err != nil {
				return nil, err
			}
		} else {
			searchMetrics = search.NoopMetrics{}
		}
	}
	gaMetrics := dependencies.GAMetrics
	if gaMetrics == nil {
		if dependencies.Metrics == nil {
			gaMetrics, err = ga.NewPrometheusMetrics(prometheus.DefaultRegisterer)
			if err != nil {
				return nil, err
			}
		} else {
			gaMetrics = ga.NoopMetrics{}
		}
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
	recoverySourceNamespace, err := recovery.NewRecoverySourceNamespaceAuthority(dependencies.DB, dependencies.Now)
	if err != nil {
		return nil, err
	}
	recoverySourceNamespaceAdapter := &managedRecoverySourceNamespaceAdapter{authority: recoverySourceNamespace}
	repositoryService, err := repository.NewService(repository.Dependencies{
		DB: dependencies.DB, Foundation: foundation, Registry: registry, Keyring: keyring, Now: dependencies.Now,
		Audit: auditSink, Admission: admission, History: history, Metrics: metricsSink, Publication: publicationService,
		CatalogOwnership: catalogOwnership, CatalogSummary: catalogService,
		RecoverySourceNamespace: recoverySourceNamespaceAdapter,
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
	processingMetrics := dependencies.ProcessingMetrics
	if processingMetrics == nil {
		if dependencies.Metrics != nil {
			processingMetrics = processing.NoopMetrics{}
		} else {
			processingMetrics, err = processing.NewPrometheusMetrics(prometheus.DefaultRegisterer)
			if err != nil {
				return nil, err
			}
		}
	}
	contentReady := &atomic.Bool{}
	derivedResolver, err := content.NewDerivedRepresentationResolver(
		dependencies.DB,
		func(ctx context.Context, request content.DerivedArtifactRead, destination io.Writer) error {
			if processingManager == nil {
				return content.ErrDerivedRepresentationUnavailable
			}
			return processingManager.ReadDerivedArtifact(ctx, request, destination)
		},
		func(ctx context.Context, capability, outputProfile string) (string, error) {
			if processingManager == nil {
				return "", content.ErrDerivedRepresentationUnavailable
			}
			return processingManager.activePipelineFingerprint(ctx, capability, outputProfile)
		},
		func(ctx context.Context, asset content.AuthorizedAsset) (bool, error) {
			if processingManager == nil {
				return false, content.ErrDerivedRepresentationUnavailable
			}
			decision, safetyErr := processingManager.malwareSafetyForAsset(ctx, asset)
			return safetyErr == nil && decision.Safe, safetyErr
		},
	)
	if err != nil {
		return nil, err
	}
	processingSource, err := content.NewDerivedAttemptSourceResolver(
		repositoryService,
		derivedResolver,
		processingSecurityPolicyRevision,
		derivedSourceResolver,
	)
	if err != nil {
		return nil, err
	}
	recoveryPublication := newManagedRecoveryPublication()
	recoveryAuthorization := &managedRecoveryAuthorizationFacade{publication: recoveryPublication}
	recoveryResults := &managedRecoveryResultFacade{publication: recoveryPublication}
	recoveryAPI, err := recovery.NewAPIService(recovery.APIServiceDependencies{
		DB: dependencies.DB, Now: dependencies.Now, Audit: auditWriter,
	})
	if err != nil {
		return nil, err
	}
	recoveryOperations := &managedRecoveryAPIFacade{publication: recoveryPublication, api: recoveryAPI}
	contentBroker, err := content.NewBroker(content.BrokerDependencies{
		DB: dependencies.DB, Now: dependencies.Now,
		FeatureEnabled: func(context.Context) (bool, error) {
			enabled, enabledErr := foundation.FeatureEnabled()
			return enabled && contentReady.Load(), enabledErr
		},
		Authorize: contentAuthorizer, RecoveryAuthorize: recoveryResults,
		Session: contentSession, Lease: lease, Source: repositoryService, RecoverySource: recoveryResults,
		Derived: derivedResolver,
		SecurityPolicyRevision: func(context.Context) (string, error) {
			return processingSecurityPolicyRevision, nil
		},
		Audit: contentAudit, Budget: contentBudget, Metrics: contentMetrics,
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
	contentDeliveryBranch, err := newContentBrokerDeliveryBranch(dependencies.DB, contentBroker)
	if err != nil {
		return nil, err
	}
	exportPublication := newManagedExportPublication()
	exportServiceFacade := &managedExportServiceFacade{publication: exportPublication}
	exportDeliveryFacade := &managedExportDeliveryFacade{publication: exportPublication}
	archiveMemberFacade := &managedArchiveMemberFacade{publication: exportPublication}
	contentService, err := newContentDeliveryMux(contentBroker, contentDeliveryBranch, exportDeliveryFacade)
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
	receiptReaper, err := recovery.NewAuthorizationReceiptReaper(dependencies.DB)
	if err != nil {
		return nil, err
	}
	receiptOwner, err := NewRecoveryAuthorizationReceiptOwner(RecoveryAuthorizationReceiptOwnerDependencies{
		Foundation: foundation,
		Reaper:     receiptReaper,
	})
	if err != nil {
		return nil, err
	}
	downgradeInspector, err := newManagedRecoveryDowngradeDBInspector(dependencies.DB)
	if err != nil {
		return nil, err
	}
	nodeWriteCoordinator, err := NewNodeWriteCoordinator(dependencies.DB)
	if err != nil {
		return nil, err
	}
	nodeRevisions := newManagedRecoveryNodeRevisionSource(dependencies.Now)
	targetRootProbe, err := recovery.NewRecoveryTargetRootRegistrationProbe(
		recovery.RecoveryTargetRootRegistrationProbeDependencies{
			DB: dependencies.DB, Revisions: nodeRevisions, Now: dependencies.Now,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build Recovery target-root registration probe: %w", err)
	}
	targetRootAuthority, err := recovery.NewTargetRootAuthorityService(
		recovery.TargetRootAuthorityServiceDependencies{
			DB: dependencies.DB, Registry: dependencies.Settings,
			Probe: targetRootProbe, Now: dependencies.Now,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build Recovery target-root authority: %w", err)
	}
	cleanupWorkerID, err := backupasset.NewOpaqueID()
	if err != nil {
		return nil, err
	}
	recoveryManager, err := newManagedRecoveryRuntime(managedRecoveryRuntimeDependencies{
		ReceiptOwner: receiptOwner, DowngradeInspector: downgradeInspector, Publication: recoveryPublication,
		Build: func(ctx context.Context, config backupasset.RecoveryConfig) (*managedRecoveryGraph, error) {
			if config.Enabled && !processingManager.recoverySecurityReady() {
				return nil, fmt.Errorf("%w: enabled Recovery requires ready Processing security evidence", backupasset.ErrInvalidState)
			}
			sourceEligibility, eligibilityErr := newManagedRecoveryEligibilitySourceAdapter(
				repositoryService, repositoryService,
			)
			if eligibilityErr != nil {
				return nil, fmt.Errorf("build Recovery source eligibility authority: %w", eligibilityErr)
			}
			securityEligibility, eligibilityErr := newManagedRecoveryEligibilitySecurityAdapter(processingManager)
			if eligibilityErr != nil {
				return nil, fmt.Errorf("build Recovery security eligibility authority: %w", eligibilityErr)
			}
			targetRootEligibility, eligibilityErr := recovery.NewRecoveryEligibilityTargetRootAuthority(
				recovery.RecoveryEligibilityTargetRootAuthorityDependencies{
					Registry: dependencies.Settings, Revisions: nodeRevisions,
				},
			)
			if eligibilityErr != nil {
				return nil, fmt.Errorf("build Recovery target-root eligibility authority: %w", eligibilityErr)
			}
			targetEligibility, eligibilityErr := recovery.NewRecoveryEligibilityTargetObservation(
				recovery.RecoveryEligibilityTargetObservationDependencies{
					DB: dependencies.DB, Revisions: nodeRevisions, Now: dependencies.Now,
				},
			)
			if eligibilityErr != nil {
				return nil, fmt.Errorf("build Recovery target eligibility authority: %w", eligibilityErr)
			}
			eligibility, eligibilityErr := newManagedRecoveryEligibilityAuthorities(
				recovery.RecoveryEligibilityAuthorityDependencies{
					DB: dependencies.DB, Source: sourceEligibility, Security: securityEligibility,
					TargetRoot: targetRootEligibility, TargetObservation: targetEligibility, Now: dependencies.Now,
				},
			)
			if eligibilityErr != nil {
				return nil, fmt.Errorf("build Recovery eligibility authority: %w", eligibilityErr)
			}
			return buildManagedRecoveryGraph(ctx, config, managedRecoveryGraphBuildDependencies{
				DB: dependencies.DB, Settings: dependencies.Settings, Now: dependencies.Now,
				Metrics:         recoveryMetrics,
				CleanupWorkerID: "recovery-cleanup-" + cleanupWorkerID,
				SourceLeases:    lease, NodeAdmission: nodeWriteCoordinator,
				NodeRevisions:        nodeRevisions,
				PreflightEvidence:    eligibility.preflight,
				AuthorityRevalidator: eligibility.live,
				PlanSecurity:         securityEligibility,
				WorkspaceKeys:        keyring, Audit: auditWriter, ContentLifecycle: contentBroker,
				SourceResolver:          repositoryService,
				Dialer:                  sshutil.NewNodeDialer(dependencies.DB),
				ReconciliationRevisions: eligibility.reconciliation,
				ReconciliationFindings:  newManagedRecoveryReconciliationFindingSink(dependencies.AlertDispatcher),
			})
		},
	})
	if err != nil {
		return nil, err
	}
	targetRootFacade := &managedRecoveryTargetRootFacade{service: targetRootAuthority, runtime: recoveryManager, audit: auditWriter}
	downgradeFacade, err := newManagedRecoveryDowngradeFacade(dependencies.DB, recoveryManager, dependencies.Now)
	if err != nil {
		return nil, err
	}
	downgradeFacade.audit = auditWriter
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
	processingManager, err = newProcessingRuntime(processingRuntimeDependencies{
		DB: dependencies.DB, Foundation: foundation, Settings: dependencies.Settings, Keyring: keyring, Lease: lease,
		Source: processingSource, Authorize: contentAuthorizer, ValidateRoot: repositoryService.ValidatePrivateRuntimeRoot,
		RevalidateSource: runtimeProcessingSourceRevalidator{source: processingSource},
		Projection: runtimeDerivedProjectionPort{
			db: dependencies.DB, ingest: searchIngest, classification: searchIngest,
			pipelineRevisions: func(ctx context.Context) (runtimeProjectionRevisions, error) {
				revisions, revisionErr := dependencies.Settings.ProcessingPipelineRevisions(ctx)
				if revisionErr != nil {
					return runtimeProjectionRevisions{}, revisionErr
				}
				return runtimeProjectionRevisions{Content: revisions.Content, OCR: revisions.OCR}, nil
			},
		},
		Metrics: processingMetrics,
		Now:     dependencies.Now,
	})
	if err != nil {
		return nil, err
	}
	exportManager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: dependencies.DB, Foundation: foundation, Keyring: keyring,
		ValidateRoot: repositoryService.ValidatePrivateRuntimeRoot,
		Publication:  exportPublication, Service: exportServiceFacade,
		Delivery: exportDeliveryFacade, Archive: archiveMemberFacade,
		Build: func(ctx context.Context, config backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			selection := &runtimeExportSelectionResolver{
				db: dependencies.DB, ownership: catalogOwnership, overlay: overlayService, search: searchService,
				queryLimits: searchQueryLimits,
			}
			serviceConfig := runtimeExportServiceConfig(config)
			service, buildErr := assetexport.NewService(assetexport.ServiceDependencies{
				DB: dependencies.DB, Now: dependencies.Now, Leases: lease, Keys: keyring, Resolver: selection,
				Config: serviceConfig,
			})
			if buildErr != nil {
				return nil, buildErr
			}
			audit, buildErr := assetexport.NewDeliveryAudit(auditSink)
			if buildErr != nil {
				return nil, buildErr
			}
			delivery, buildErr := assetexport.NewDeliveryGateway(assetexport.DeliveryGatewayDependencies{
				DB: dependencies.DB, Now: dependencies.Now, Session: contentSession, Store: store, Keys: keyring,
				ArchiveMembers: derivedResolver, ArchiveMemberAuthorize: contentAuthorizer, Audit: audit,
				Config: runtimeExportDeliveryConfig(config),
			})
			if buildErr != nil {
				return nil, buildErr
			}
			archiveMember, buildErr := newRuntimeArchiveMemberService(
				dependencies.DB, dependencies.Now, config, processingManager, contentAuthorizer, derivedResolver, delivery,
			)
			if buildErr != nil {
				return nil, buildErr
			}
			quota, buildErr := assetexport.NewQuotaService(dependencies.DB, dependencies.Now, serviceConfig.Quota)
			if buildErr != nil {
				return nil, buildErr
			}
			attemptBudget, buildErr := assetexport.NewAttemptBudgetService(dependencies.DB, dependencies.Now)
			if buildErr != nil {
				return nil, buildErr
			}
			attemptBroker, buildErr := content.NewAttemptBroker(repositoryService, attemptBudget, dependencies.Now)
			if buildErr != nil {
				return nil, buildErr
			}
			workerCapacity := assetexport.WorkerCapacityLimits{
				WorkerConcurrency: int64(config.WorkerConcurrency),
				UserActiveJobs:    int64(config.UserActiveJobs),
			}
			attemptWork := assetexport.NewAttemptWorkRegistry()
			attempts, buildErr := assetexport.NewAttemptCoordinatorWithWorkerCapacity(
				dependencies.DB, dependencies.Now, workerCapacity, lease,
			)
			if buildErr != nil {
				return nil, buildErr
			}
			workerOwnerID, buildErr := backupasset.NewOpaqueID()
			if buildErr != nil {
				return nil, buildErr
			}
			lifecyclePort, buildErr := assetexport.NewPersistentLifecyclePort(assetexport.PersistentLifecyclePortDependencies{
				DB: dependencies.DB, Delivery: delivery, Sources: lease, Quota: quota, Store: store, Now: dependencies.Now,
				WorkerCapacity: &workerCapacity, AttemptWork: attemptWork,
			})
			if buildErr != nil {
				return nil, buildErr
			}
			lifecycle, buildErr := assetexport.NewLifecycle(assetexport.LifecycleDependencies{
				DB: dependencies.DB, Port: lifecyclePort, Now: dependencies.Now,
			})
			if buildErr != nil {
				return nil, buildErr
			}
			exportWorker, buildErr := assetexport.NewPersistentWorker(assetexport.PersistentWorkerDependencies{
				DB: dependencies.DB, Keys: keyring, Broker: attemptBroker, Metadata: selection,
				Store: store, Lifecycle: lifecycle, SourceLeases: lease, WorkerCapacity: &workerCapacity, AttemptWork: attemptWork, Now: dependencies.Now,
			})
			if buildErr != nil {
				return nil, buildErr
			}
			runner, buildErr := newManagedExportWorker(managedExportWorkerDependencies{
				DB: dependencies.DB, Attempts: attempts, Worker: exportWorker, Lifecycle: lifecycle, Delivery: delivery,
				Archive: archiveMember, Budget: attemptBudget,
				Cadence: config.GCCadence, HeartbeatInterval: config.LeaseRenewMargin / 2,
				SourceLeaseInterval: leaseConfig.Heartbeat,
				BatchSize:           config.ReconcileBatchSize, WorkerConcurrency: config.WorkerConcurrency,
				WorkerOwner: "export-worker-" + workerOwnerID,
			})
			if buildErr != nil {
				return nil, buildErr
			}
			return &managedExportGraph{
				store: store, service: service, delivery: delivery, archiveMember: archiveMember, attempts: attempts,
				worker: exportWorker, lifecycle: lifecycle, runner: runner,
				stopAccepting: runner.StopAccepting, drain: runner.Drain,
				run: runner.Run, shutdown: runner.Shutdown,
				startup: runner.Startup,
				terminalize: func(ctx context.Context) error {
					return terminalizeExportRuntimeLifecycle(
						ctx, lifecycle, config.ReconcileBatchSize, exportWorker.ReconcileOrphans,
					)
				},
			}, nil
		},
	})
	if err != nil {
		return nil, err
	}
	if err := repositoryService.SetRebuildPorts(
		newCatalogRebuildAdapter(catalogIndexer),
		newDerivedBackfillAdapter(processingManager),
	); err != nil {
		return nil, err
	}
	retentionWorker, retentionPolicies, retentionHolds, retentionPurge, managedTaskRetention, err := composeRetentionRuntime(retentionRuntimeInput{
		DB: dependencies.DB, Foundation: foundation, Now: dependencies.Now, Lease: lease, Registry: registry,
		Repository: repositoryService, CatalogIndexer: catalogIndexer, SearchIndexer: searchIndexer,
		Overlay: overlayService, ContentBroker: contentBroker, ContentManager: contentManager,
		Processing: processingManager, Export: exportManager, Recovery: recoveryManager,
		AuditWriter: auditWriter, OverlayBatch: overlayConfig.BulkMaxItems, OverlayPasses: 32,
		OwnerBatch: retentionOwnerBatch(foundation), Metrics: retentionRuntimeMetrics(dependencies.Metrics),
	})
	if err != nil {
		return nil, err
	}
	inventory, err := composeGARuntime(gaRuntimeInput{DB: dependencies.DB, Now: dependencies.Now})
	if err != nil {
		return nil, err
	}
	return &Runtime{
		foundation: foundation, settings: dependencies.Settings,
		repository: repositoryService, publication: publicationService,
		resticStrategy: resticStrategy, rsyncStrategy: rsyncStrategy, rcloneStrategy: rcloneStrategy,
		admission: admission, worker: worker, healthWorker: healthWorker,
		catalogService: catalogService, catalogIndexer: catalogIndexer, catalogWorker: catalogWorker, catalogAudit: auditSink,
		keyring: keyring, searchService: searchService, searchIndexer: searchIndexer, searchIngest: searchIngest,
		searchWorker: searchWorker, overlayService: overlayService, searchReady: searchReady,
		contentBroker: contentBroker, contentService: contentService, exportDelivery: exportDeliveryFacade,
		contentBudget: contentBudget, contentAudit: contentAudit,
		contentReconciler: contentReconciler, contentReady: contentReady, contentManager: contentManager,
		exportManager: exportManager, recoveryManager: recoveryManager,
		recoverySourceNamespace: recoverySourceNamespace,
		recoveryTargetRoots:     targetRootFacade,
		recoveryDowngrade:       downgradeFacade,
		nodeWriteCoordinator:    nodeWriteCoordinator,
		recoveryAuthorization:   recoveryAuthorization, recoveryAPI: recoveryAPI,
		recoveryOperations: recoveryOperations, recoveryResults: recoveryResults,
		processingManager: processingManager, archiveMemberService: archiveMemberFacade,
		retentionWorker: retentionWorker, retentionPolicies: retentionPolicies,
		retentionHolds: retentionHolds, retentionPurge: retentionPurge,
		managedTaskRetention: managedTaskRetention,
		inventory:            inventory,
		enablement:           composeGAReadiness(dependencies.DB, dependencies.Settings, keyring),
		transitioner:         admission,
		metrics:              metricsSink,
		gaMetrics:            gaMetrics,
	}, nil
}

type runtimeSearchExcerptResolver struct {
	db             *gorm.DB
	resolveAsset   content.DerivedSourceAssetResolver
	readArtifact   func(context.Context, content.DerivedArtifactRead, io.Writer) error
	activePipeline func(context.Context, string, string) (string, error)
}

type runtimeSearchExcerptArtifact struct {
	CatalogGenerationID        string
	SourceFingerprint          string
	EntryFingerprint           string
	ProviderCapabilityRevision int64
	Capability                 string
	CapabilitySchema           string
	PipelineFingerprint        string
	OutputProfile              string
	ArtifactID                 string
	PlaintextSize              int64
}

func (resolver *runtimeSearchExcerptResolver) Verify(
	ctx context.Context,
	request search.ExcerptVerifyRequest,
) (search.VerifiedSnippet, bool, error) {
	if resolver == nil || resolver.db == nil || resolver.resolveAsset == nil || resolver.readArtifact == nil ||
		resolver.activePipeline == nil || backupasset.ValidateAssetRef(request.Ref) != nil ||
		backupasset.ValidateOpaqueID(request.ExcerptRef) != nil || !validRuntimeSearchExcerptField(request.Field) ||
		!validRuntimeSearchExcerptTerms(request.Field, request.Terms) {
		return search.VerifiedSnippet{}, false, nil
	}
	ctx = nonNilRuntimeContext(ctx)
	role := processing.ArtifactRoleContent
	if request.Field == search.SearchFieldOCR {
		role = processing.ArtifactRoleOCR
	}
	var rows []runtimeSearchExcerptArtifact
	query := resolver.db.WithContext(ctx).Table("backup_asset_derived_artifacts AS artifacts").
		Select(`sets.catalog_generation_id, sets.source_fingerprint,
			jobs.entry_fingerprint, jobs.provider_capability_revision,
			jobs.capability, jobs.capability_schema, jobs.pipeline_fingerprint, jobs.output_profile,
			artifacts.id AS artifact_id, artifacts.plaintext_size`).
		Joins(`JOIN backup_asset_derived_artifact_sets AS sets
			ON sets.id = artifacts.artifact_set_id`).
		Joins(`JOIN backup_asset_processing_jobs AS jobs
			ON jobs.id = sets.job_id AND jobs.current_artifact_set_id = sets.id`).
		Joins(`JOIN backup_asset_processing_attempts AS attempts
			ON attempts.id = sets.attempt_id AND attempts.job_id = jobs.id`).
		Where(`artifacts.id = ? AND artifacts.excerpt_ref = ? AND artifacts.role = ?
			AND artifacts.media_type = ? AND artifacts.completeness = ?
			AND artifacts.plaintext_size > 0 AND artifacts.plaintext_size <= ?`,
			request.ExcerptRef, request.ExcerptRef, role, "text/plain", processing.ArtifactComplete,
			runtimeSearchExcerptMaxBytes).
		Where(`sets.recovery_point_id = ? AND sets.entry_id = ?
			AND sets.security_policy_revision = ? AND sets.state = ? AND sets.completeness = ?
			AND sets.work_key = jobs.work_key AND sets.projection_required = ?
			AND sets.projection_published = ? AND sets.projection_revision > 0`,
			request.Ref.RecoveryPointID, request.Ref.EntryID, processingSecurityPolicyRevision,
			"active", processing.ArtifactComplete, true, true).
		Where(`jobs.recovery_point_id = sets.recovery_point_id
			AND jobs.catalog_generation_id = sets.catalog_generation_id
			AND jobs.entry_id = sets.entry_id AND jobs.source_fingerprint = sets.source_fingerprint
			AND jobs.security_policy_revision = sets.security_policy_revision
			AND jobs.state = ? AND jobs.is_current = ? AND jobs.finished_at IS NOT NULL
			AND jobs.current_attempt_id = sets.attempt_id
			AND attempts.state = ? AND attempts.is_current = ? AND attempts.finished_at IS NOT NULL`,
			processing.ProcessingSucceeded, false, "succeeded", false).
		Limit(2).Scan(&rows)
	if query.Error != nil {
		return search.VerifiedSnippet{}, false, fmt.Errorf("load current Search excerpt artifact: %w", query.Error)
	}
	if len(rows) != 1 {
		return search.VerifiedSnippet{}, false, nil
	}
	row := rows[0]
	profile, ok := capabilityspec.Lookup(row.Capability, row.OutputProfile, false)
	if !ok || profile.CapabilitySchema != row.CapabilitySchema ||
		backupasset.ValidateOpaqueID(row.ArtifactID) != nil || row.ArtifactID != request.ExcerptRef {
		return search.VerifiedSnippet{}, false, nil
	}
	if !runtimeSearchExcerptProfileAllows(profile, role, "text/plain") {
		return search.VerifiedSnippet{}, false, nil
	}
	activePipeline, err := resolver.activePipeline(ctx, row.Capability, row.OutputProfile)
	if err != nil {
		return search.VerifiedSnippet{}, false, err
	}
	if activePipeline == "" || activePipeline != row.PipelineFingerprint {
		return search.VerifiedSnippet{}, false, nil
	}
	asset, err := resolver.resolveAsset(ctx, request.Ref, row.CatalogGenerationID, row.SourceFingerprint)
	if err != nil {
		return search.VerifiedSnippet{}, false, nil
	}
	if asset.EntryFingerprint != row.EntryFingerprint ||
		asset.ProviderCapabilityRevision != row.ProviderCapabilityRevision {
		return search.VerifiedSnippet{}, false, nil
	}
	var plaintext bytes.Buffer
	plaintext.Grow(int(row.PlaintextSize))
	limited := &runtimeSearchExcerptWriter{destination: &plaintext, remaining: row.PlaintextSize}
	if err := resolver.readArtifact(ctx, content.DerivedArtifactRead{
		ArtifactID: row.ArtifactID, RecoveryPointID: request.Ref.RecoveryPointID,
		CatalogGenerationID: row.CatalogGenerationID, EntryID: request.Ref.EntryID,
		SourceFingerprint: row.SourceFingerprint,
	}, limited); err != nil {
		return search.VerifiedSnippet{}, false, err
	}
	if limited.remaining != 0 || int64(plaintext.Len()) != row.PlaintextSize ||
		!utf8.Valid(plaintext.Bytes()) || bytes.IndexByte(plaintext.Bytes(), 0) >= 0 {
		return search.VerifiedSnippet{}, false, nil
	}
	matchStart, matchEnd, matched := runtimeSearchExcerptTerms(plaintext.String(), request.Field, request.Terms)
	if !matched {
		return search.VerifiedSnippet{}, false, nil
	}
	snippet := runtimeBoundedSearchSnippet(plaintext.String(), matchStart, matchEnd)
	if snippet == "" {
		return search.VerifiedSnippet{}, false, nil
	}
	return search.VerifiedSnippet{Field: request.Field, Text: snippet}, true, nil
}

type runtimeSearchExcerptWriter struct {
	destination io.Writer
	remaining   int64
}

func (writer *runtimeSearchExcerptWriter) Write(value []byte) (int, error) {
	if writer == nil || writer.destination == nil || writer.remaining < 0 {
		return 0, content.ErrDerivedRepresentationUnavailable
	}
	if int64(len(value)) > writer.remaining {
		allowed := int(writer.remaining)
		if allowed > 0 {
			_, _ = writer.destination.Write(value[:allowed])
			writer.remaining = 0
		}
		return allowed, content.ErrDerivedRepresentationUnavailable
	}
	written, err := writer.destination.Write(value)
	writer.remaining -= int64(written)
	return written, err
}

func validRuntimeSearchExcerptField(field search.SearchField) bool {
	return field == search.SearchFieldContent || field == search.SearchFieldOCR
}

func runtimeSearchExcerptProfileAllows(profile capabilityspec.Profile, role processing.ArtifactRole, mediaType string) bool {
	for _, output := range profile.Outputs {
		if output.Role != string(role) || output.Maximum < 1 {
			continue
		}
		for _, allowedMediaType := range output.MediaTypes {
			if allowedMediaType == mediaType {
				return true
			}
		}
	}
	return false
}

func validRuntimeSearchExcerptTerms(field search.SearchField, terms []string) bool {
	if len(terms) == 0 || len(terms) > 512 {
		return false
	}
	for _, term := range terms {
		if term == "" || term != strings.TrimSpace(term) || len(term) > 4096 ||
			!utf8.ValidString(term) || strings.ContainsRune(term, 0) {
			return false
		}
		normalized, err := search.NormalizeFieldV1(field, term, search.DefaultNormalizerLimits())
		if err != nil {
			return false
		}
		canonical := false
		for _, token := range normalized.Tokens {
			if token.Value == term {
				canonical = true
				break
			}
		}
		if !canonical {
			return false
		}
	}
	return true
}

func runtimeSearchExcerptTerms(value string, field search.SearchField, terms []string) (int, int, bool) {
	remaining := make(map[string]bool, len(terms))
	for _, term := range terms {
		remaining[term] = true
	}
	firstStart, firstEnd := -1, -1
	for offset := 0; offset < len(value); {
		character, size := utf8.DecodeRuneInString(value[offset:])
		if unicode.IsSpace(character) {
			offset += size
			continue
		}
		start := offset
		for offset < len(value) {
			character, size = utf8.DecodeRuneInString(value[offset:])
			if unicode.IsSpace(character) {
				break
			}
			offset += size
		}
		end := offset
		normalized, err := search.NormalizeFieldV1(field, value[start:end], search.DefaultNormalizerLimits())
		if err != nil {
			return 0, 0, false
		}
		for _, token := range normalized.Tokens {
			if !remaining[token.Value] {
				continue
			}
			delete(remaining, token.Value)
			if firstStart < 0 {
				firstStart, firstEnd = start, end
			}
		}
		if len(remaining) == 0 {
			return firstStart, firstEnd, true
		}
	}
	return 0, 0, false
}

func runtimeBoundedSearchSnippet(value string, matchStart, matchEnd int) string {
	if matchStart < 0 || matchEnd <= matchStart || matchEnd > len(value) ||
		matchEnd-matchStart > runtimeSearchSnippetMaxBytes {
		return ""
	}
	contextBytes := runtimeSearchSnippetMaxBytes - (matchEnd - matchStart)
	start := max(0, matchStart-contextBytes/2)
	end := min(len(value), start+runtimeSearchSnippetMaxBytes)
	if end < matchEnd {
		end = matchEnd
		start = max(0, end-runtimeSearchSnippetMaxBytes)
	}
	if end == len(value) {
		start = max(0, end-runtimeSearchSnippetMaxBytes)
	}
	for start < matchStart && !utf8.RuneStart(value[start]) {
		start++
	}
	for end > matchEnd && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	if start > matchStart || end < matchEnd || start >= end || end-start > runtimeSearchSnippetMaxBytes {
		return ""
	}
	return value[start:end]
}

func runtimeDerivedSourceAssetResolver(db *gorm.DB) content.DerivedSourceAssetResolver {
	return func(
		ctx context.Context,
		ref backupasset.AssetRef,
		catalogGenerationID string,
		sourceFingerprint string,
	) (content.AuthorizedAsset, error) {
		if db == nil || backupasset.ValidateAssetRef(ref) != nil ||
			backupasset.ValidateOpaqueID(catalogGenerationID) != nil ||
			strings.TrimSpace(sourceFingerprint) == "" || len(sourceFingerprint) > 128 {
			return content.AuthorizedAsset{}, content.ErrDerivedRepresentationUnavailable
		}
		var rows []runtimeContentAssetRecord
		err := db.WithContext(nonNilRuntimeContext(ctx)).Table("catalog_entries AS entries").
			Select(`entries.generation_id AS catalog_generation_id,
				generations.source_fingerprint AS generation_source_fingerprint,
				points.repository_id AS repository_id,
				points.semantics AS point_semantics, points.state AS point_state,
				points.source_fingerprint AS point_source_fingerprint,
				points.capability_revision AS point_capability,
				points.physical_availability AS point_physical_availability,
				points.retired_at AS point_retired_at,
				repositories.provider_kind AS repository_provider,
				repositories.status AS repository_status,
				repositories.capability_revision AS repository_capability,
				entries.entry_type AS entry_type, entries.size AS entry_size,
				entries.mime_type AS entry_media_type, entries.fingerprint AS entry_fingerprint,
				entries.fingerprint_strength AS entry_fingerprint_strength,
				entries.security_state AS entry_security_state`).
			Joins(`JOIN catalog_generations AS generations
				ON generations.id = entries.generation_id AND generations.recovery_point_id = entries.recovery_point_id`).
			Joins("JOIN recovery_points AS points ON points.id = entries.recovery_point_id").
			Joins("JOIN backup_repositories AS repositories ON repositories.id = points.repository_id").
			Where(`entries.generation_id = ? AND entries.recovery_point_id = ? AND entries.entry_id = ?
				AND generations.id = ? AND generations.recovery_point_id = ?
				AND generations.state = ? AND generations.is_active = ?`,
				catalogGenerationID, ref.RecoveryPointID, ref.EntryID,
				catalogGenerationID, ref.RecoveryPointID, catalog.GenerationComplete, true).
			Limit(2).Scan(&rows).Error
		if err != nil {
			return content.AuthorizedAsset{}, fmt.Errorf("load Derived source binding: %w", err)
		}
		if len(rows) != 1 {
			return content.AuthorizedAsset{}, content.ErrDerivedRepresentationUnavailable
		}
		record := rows[0]
		provider := backupasset.ProviderKind(record.RepositoryProvider)
		strength, strengthErr := catalog.ParseFingerprintStrength(record.EntryFingerprintStrength)
		if record.GenerationSourceFingerprint != sourceFingerprint || record.PointSourceFingerprint != sourceFingerprint ||
			!runtimeContentPointVisible(record) || record.PointRetiredAt != nil ||
			record.RepositoryStatus != string(backupasset.RepositoryOnline) ||
			record.RepositoryCapability <= 0 || record.RepositoryCapability != record.PointCapability ||
			(provider != backupasset.ProviderRestic && provider != backupasset.ProviderRsync && provider != backupasset.ProviderRclone) ||
			strengthErr != nil || record.EntryType != string(backupasset.CatalogEntryFile) || record.EntrySize < 0 ||
			record.EntrySecurityState != "sealed" {
			return content.AuthorizedAsset{}, content.ErrDerivedRepresentationUnavailable
		}
		return content.AuthorizedAsset{
			Ref: ref, CatalogGenerationID: record.CatalogGenerationID, RepositoryID: record.RepositoryID,
			Provider: provider, ProviderCapabilityRevision: int64(record.PointCapability),
			SourceFingerprint: record.GenerationSourceFingerprint, EntryFingerprint: record.EntryFingerprint,
			FingerprintStrength: string(strength), Size: record.EntrySize, MediaType: record.EntryMediaType,
		}, nil
	}
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
func (runtime *Runtime) RetentionPolicies() *retention.PolicyService {
	if runtime == nil {
		return nil
	}
	return runtime.retentionPolicies
}

func (runtime *Runtime) RetentionHolds() *retention.HoldService {
	if runtime == nil {
		return nil
	}
	return runtime.retentionHolds
}

func (runtime *Runtime) RetentionPurge() *retention.PurgeService {
	if runtime == nil {
		return nil
	}
	return runtime.retentionPurge
}

func (runtime *Runtime) ContentBroker() *content.Broker {
	if runtime == nil {
		return nil
	}
	return runtime.contentBroker
}
func (runtime *Runtime) ContentService() *contentDeliveryMux {
	if runtime == nil {
		return nil
	}
	return runtime.contentService
}
func (runtime *Runtime) NodeWriteCoordinator() *NodeWriteCoordinator {
	if runtime == nil {
		return nil
	}
	return runtime.nodeWriteCoordinator
}
func (runtime *Runtime) RecoveryAuthorization() *managedRecoveryAuthorizationFacade {
	if runtime == nil {
		return nil
	}
	return runtime.recoveryAuthorization
}
func (runtime *Runtime) RecoveryAPI() *recovery.APIService {
	if runtime == nil {
		return nil
	}
	return runtime.recoveryAPI
}
func (runtime *Runtime) RecoveryOperations() *managedRecoveryAPIFacade {
	if runtime == nil {
		return nil
	}
	return runtime.recoveryOperations
}
func (runtime *Runtime) RecoveryTargetRoots() RecoveryTargetRootAuthority {
	if runtime == nil {
		return nil
	}
	return runtime.recoveryTargetRoots
}
func (runtime *Runtime) RecoveryResults() *managedRecoveryResultFacade {
	if runtime == nil {
		return nil
	}
	return runtime.recoveryResults
}
func (runtime *Runtime) RecoveryDowngradeReadiness(ctx context.Context) (RecoveryDowngradeReadiness, error) {
	if runtime == nil || runtime.recoveryManager == nil {
		return RecoveryDowngradeReadiness{}, fmt.Errorf("%w: Recovery downgrade readiness unavailable", backupasset.ErrInvalidState)
	}
	return runtime.recoveryManager.DowngradeReadiness(ctx)
}

func (runtime *Runtime) ReplayRecoveryDowngradeReadiness(
	ctx context.Context,
	request RecoveryDowngradeReadinessRequest,
) (RecoveryDowngradeReadiness, bool, error) {
	if runtime == nil || runtime.recoveryDowngrade == nil {
		return RecoveryDowngradeReadiness{}, false, backupasset.ErrInvalidState
	}
	return runtime.recoveryDowngrade.ReplayRecoveryDowngradeReadiness(ctx, request)
}

func (runtime *Runtime) RequestRecoveryDowngradeReadiness(
	ctx context.Context,
	request RecoveryDowngradeReadinessRequest,
) (RecoveryDowngradeReadiness, error) {
	if runtime == nil || runtime.recoveryDowngrade == nil {
		return RecoveryDowngradeReadiness{}, backupasset.ErrInvalidState
	}
	return runtime.recoveryDowngrade.RequestRecoveryDowngradeReadiness(ctx, request)
}
func (runtime *Runtime) ExportService() *managedExportServiceFacade {
	if runtime == nil || runtime.exportManager == nil {
		return nil
	}
	return runtime.exportManager.Service()
}
func (runtime *Runtime) ExportDeliveryGateway() *managedExportDeliveryFacade {
	if runtime == nil || runtime.exportManager == nil {
		return nil
	}
	return runtime.exportManager.Delivery()
}
func (runtime *Runtime) ArchiveMemberService() *managedArchiveMemberFacade {
	if runtime == nil {
		return nil
	}
	return runtime.archiveMemberService
}
func (runtime *Runtime) ContentConfig() (backupasset.ContentConfig, error) {
	if runtime == nil || runtime.foundation == nil {
		return backupasset.ContentConfig{}, fmt.Errorf("%w: Content config unavailable", backupasset.ErrInvalidState)
	}
	return runtime.foundation.ContentConfig()
}
func (runtime *Runtime) WorkerProtocol() *processing.WorkerProtocolService {
	if runtime == nil || runtime.processingManager == nil {
		return nil
	}
	return runtime.processingManager.WorkerProtocol()
}
func (runtime *Runtime) ProcessingConfig() (backupasset.ProcessingConfig, error) {
	if runtime == nil || runtime.processingManager == nil {
		return backupasset.ProcessingConfig{}, fmt.Errorf("%w: Processing config unavailable", backupasset.ErrInvalidState)
	}
	return runtime.processingManager.ProcessingConfig()
}
func (runtime *Runtime) ProcessingAdminSummary(ctx context.Context) (ProcessingAdminSummary, error) {
	if runtime == nil || runtime.processingManager == nil {
		return ProcessingAdminSummary{}, fmt.Errorf("%w: Processing summary unavailable", backupasset.ErrInvalidState)
	}
	return runtime.processingManager.AdminSummary(ctx)
}

func (runtime *Runtime) RequestProcessingPreview(
	ctx context.Context,
	request processing.PreviewJobRequest,
) (processing.PreviewJobResult, error) {
	if runtime == nil || runtime.processingManager == nil {
		return processing.PreviewJobResult{}, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.RequestPreview(ctx, request)
}

func (runtime *Runtime) PollProcessingPreview(
	ctx context.Context,
	lookup processing.PreviewJobLookup,
) (processing.PreviewJobResult, error) {
	if runtime == nil || runtime.processingManager == nil {
		return processing.PreviewJobResult{}, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.PollPreview(ctx, lookup)
}

func (runtime *Runtime) CancelProcessingPreview(ctx context.Context, lookup processing.PreviewJobLookup) error {
	if runtime == nil || runtime.processingManager == nil {
		return processing.ErrProcessingDisabled
	}
	return runtime.processingManager.CancelPreview(ctx, lookup)
}

func (runtime *Runtime) GetProcessingState(
	ctx context.Context,
	request processing.PreviewStateRequest,
) (processing.AssetProcessingState, error) {
	if runtime == nil || runtime.processingManager == nil {
		return processing.AssetProcessingState{}, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.ProcessingState(ctx, request)
}

func (runtime *Runtime) ProcessingCoverage(ctx context.Context) (processing.CoverageSummary, error) {
	if runtime == nil || runtime.processingManager == nil {
		return processing.CoverageSummary{}, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.ProcessingCoverage(ctx)
}

func (runtime *Runtime) ProcessingCapabilities(ctx context.Context) ([]processing.CapabilityInventoryItem, error) {
	if runtime == nil || runtime.processingManager == nil {
		return nil, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.ProcessingCapabilities(ctx)
}

func (runtime *Runtime) ProcessingUpdaterStatus(ctx context.Context) (ProcessingUpdaterStatus, error) {
	if runtime == nil || runtime.processingManager == nil {
		return ProcessingUpdaterStatus{}, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.ProcessingUpdaterStatus(ctx)
}

func (runtime *Runtime) ProcessingUpdaterCandidates(ctx context.Context) ([]ProcessingUpdaterCandidate, error) {
	if runtime == nil || runtime.processingManager == nil {
		return nil, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.ProcessingUpdaterCandidates(ctx)
}

func (runtime *Runtime) ProcessingBackfillPolicy() (ProcessingBackfillPolicy, error) {
	if runtime == nil || runtime.processingManager == nil {
		return ProcessingBackfillPolicy{}, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.ProcessingBackfillPolicy()
}

func (runtime *Runtime) UpdateProcessingBackfillPolicy(
	ctx context.Context,
	request ProcessingBackfillPolicyUpdate,
) (ProcessingBackfillPolicy, error) {
	if runtime == nil || runtime.processingManager == nil {
		return ProcessingBackfillPolicy{}, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.UpdateProcessingBackfillPolicy(ctx, request)
}

func (runtime *Runtime) RequestProcessingUpdaterScan(ctx context.Context) error {
	if runtime == nil || runtime.processingManager == nil {
		return processing.ErrProcessingDisabled
	}
	return runtime.processingManager.RequestProcessingUpdaterScan(ctx)
}

func (runtime *Runtime) ActivateProcessingUpdaterCandidate(ctx context.Context, request ProcessingUpdaterActivationRequest) error {
	if runtime == nil || runtime.processingManager == nil {
		return processing.ErrProcessingDisabled
	}
	return runtime.processingManager.ActivateProcessingUpdaterCandidate(ctx, request)
}

func (runtime *Runtime) RegisterUpdaterCandidate(
	ctx context.Context,
	identity processingupdater.UpdaterTransportIdentity,
	request processingupdater.RegisterCandidateRequest,
) (processingupdater.RegisterCandidateResult, error) {
	if runtime == nil || runtime.processingManager == nil {
		return processingupdater.RegisterCandidateResult{}, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.RegisterUpdaterCandidate(ctx, identity, request)
}

func (runtime *Runtime) PullUpdaterActivation(
	ctx context.Context,
	identity processingupdater.UpdaterTransportIdentity,
	request processingupdater.PullActivationRequest,
) (processingupdater.PullActivationResult, error) {
	if runtime == nil || runtime.processingManager == nil {
		return processingupdater.PullActivationResult{}, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.PullUpdaterActivation(ctx, identity, request)
}

func (runtime *Runtime) ReportUpdaterActivation(
	ctx context.Context,
	identity processingupdater.UpdaterTransportIdentity,
	request processingupdater.ActivationReportRequest,
) (processingupdater.ActivationReportResult, error) {
	if runtime == nil || runtime.processingManager == nil {
		return processingupdater.ActivationReportResult{}, processing.ErrProcessingDisabled
	}
	return runtime.processingManager.ReportUpdaterActivation(ctx, identity, request)
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

func (runtime *Runtime) Inventory() *ga.InventoryService {
	if runtime == nil {
		return nil
	}
	return runtime.inventory
}

func (runtime *Runtime) inventoryWorkerStarted() bool {
	return false
}

func (runtime *Runtime) TransitionFeature(ctx context.Context, enabled bool, persist func() error) error {
	if runtime == nil || runtime.transitioner == nil || runtime.contentManager == nil {
		return fmt.Errorf("%w: backup asset feature transition unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if enabled {
		if err := runtime.authorizeEnablement(ctx); err != nil {
			return err
		}
		if err := runtime.contentManager.PrepareEnable(ctx); err != nil {
			return err
		}
		persistEnablement := persist
		if persist != nil {
			persistEnablement = func() error {
				if err := runtime.recordEnablementSucceeded(ctx); err != nil {
					return err
				}
				return persist()
			}
		}
		err := runtime.transitioner.TransitionFeature(ctx, true, persistEnablement)
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

func (runtime *Runtime) authorizeEnablement(ctx context.Context) error {
	if runtime == nil || runtime.enablement == nil {
		return fmt.Errorf("%w: readiness unavailable", ga.ErrEnablementBlocked)
	}
	snapshot, err := runtime.enablement.CurrentReadiness(ctx)
	if err != nil {
		ga.ObserveReadiness(runtime.gaMetrics, snapshot)
		if runtime.gaMetrics != nil {
			runtime.gaMetrics.ObserveEnablementReject(ga.RejectReadinessUnavailable)
		}
		return fmt.Errorf("%w: %w", ga.ErrEnablementBlocked, err)
	}
	if err := ga.EvaluateEnablement(snapshot); err != nil {
		ga.ObserveReadiness(runtime.gaMetrics, snapshot)
		if runtime.gaMetrics != nil {
			runtime.gaMetrics.ObserveEnablementReject(ga.ClassifyEnablementReject(snapshot, err))
		}
		return err
	}
	if runtime.inventory != nil {
		if err := runtime.inventory.MaterializeReadiness(ctx, snapshot); err != nil {
			return err
		}
	}
	ga.ObserveReadiness(runtime.gaMetrics, snapshot)
	return nil
}

func (runtime *Runtime) recordEnablementSucceeded(ctx context.Context) error {
	if runtime == nil || runtime.inventory == nil {
		return nil
	}
	return runtime.inventory.RecordEnablementSucceeded(ctx)
}

func (runtime *Runtime) authorizeRequestedStartupEnablement(ctx context.Context) error {
	if runtime == nil || runtime.foundation == nil {
		return nil
	}
	enabled, err := runtime.foundation.FeatureEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	return runtime.authorizeEnablement(ctx)
}

func (runtime *Runtime) TransitionBackupAssetSettings(
	ctx context.Context,
	current map[string]string,
	overlay map[string]string,
	effective map[string]string,
	config backupasset.ExportConfig,
	persist func() error,
) error {
	if runtime == nil || runtime.exportManager == nil || runtime.transitioner == nil || persist == nil {
		return fmt.Errorf("%w: backup asset settings transition unavailable", backupasset.ErrInvalidState)
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(effective["backup_assets.enabled"]))
	if err != nil {
		return fmt.Errorf("%w: parse effective backup asset enabled setting: %v", backupasset.ErrInvalidState, err)
	}
	_, changesIdempotencyTTL := overlay["backup_assets.idempotency_ttl"]
	_, changesIdempotencyKeyMaxBytes := overlay["backup_assets.idempotency_key_max_bytes"]
	if (changesIdempotencyTTL || changesIdempotencyKeyMaxBytes) && runtime.overlayService == nil {
		return fmt.Errorf("%w: Overlay idempotency settings transition unavailable", backupasset.ErrInvalidState)
	}
	transitionExport := func() error {
		transitionPersist := persist
		if changesIdempotencyTTL || changesIdempotencyKeyMaxBytes {
			transitionPersist = func() error {
				return runtime.overlayService.TransitionIdempotencySettings(
					config.IdempotencyTTL,
					config.IdempotencyKeyMaxBytes,
					persist,
				)
			}
		}
		transitionRecovery := transitionPersist
		if runtime.recoveryManager != nil {
			recoveryConfig, recoveryErr := backupasset.RecoveryConfigFromValues(effective)
			if recoveryErr != nil {
				return recoveryErr
			}
			restoreValues := make(map[string]string)
			for key := range overlay {
				if !strings.HasPrefix(key, "backup_assets.recovery.") {
					continue
				}
				value, exists := current[key]
				if !exists {
					return fmt.Errorf("%w: prior Recovery setting unavailable", backupasset.ErrInvalidState)
				}
				restoreValues[key] = value
			}
			restoreRecovery := func() error {
				if len(restoreValues) == 0 {
					return nil
				}
				if runtime.settings == nil {
					return fmt.Errorf("%w: Recovery settings restoration unavailable", backupasset.ErrInvalidState)
				}
				return runtime.settings.UpdateMany(restoreValues)
			}
			transitionRecovery = func() error {
				return runtime.recoveryManager.TransitionSettingsWithRestore(
					ctx, recoveryConfig, transitionPersist, restoreRecovery,
				)
			}
		}
		return runtime.exportManager.TransitionSettings(ctx, enabled, config, transitionRecovery)
	}
	if _, changesGlobalEnabled := overlay["backup_assets.enabled"]; changesGlobalEnabled {
		return runtime.TransitionFeature(ctx, enabled, transitionExport)
	}
	return runtime.transitioner.TransitionFeature(ctx, enabled, transitionExport)
}

func (runtime *Runtime) PrepareApplicationDowngrade(ctx context.Context, callback func() error) error {
	if runtime == nil || runtime.transitioner == nil || runtime.contentManager == nil {
		return fmt.Errorf("%w: backup asset application downgrade unavailable", backupasset.ErrInvalidState)
	}
	runtime.contentManager.SetReady(false)
	if runtime.exportManager != nil {
		return runtime.exportManager.PrepareSchemaDown(ctx, func() error {
			return runtime.prepareRecoverySchemaDown(ctx, func() error {
				return runtime.contentManager.PrepareSchemaDown(ctx, func() error {
					return runtime.transitioner.PrepareApplicationDowngrade(ctx, callback)
				})
			})
		})
	}
	return runtime.prepareRecoverySchemaDown(ctx, func() error {
		return runtime.contentManager.PrepareSchemaDown(ctx, func() error {
			return runtime.transitioner.PrepareApplicationDowngrade(ctx, callback)
		})
	})
}

func (runtime *Runtime) PrepareSchemaDown(ctx context.Context, callback func() error) error {
	if runtime == nil || runtime.transitioner == nil || runtime.contentManager == nil {
		return fmt.Errorf("%w: backup asset schema down unavailable", backupasset.ErrInvalidState)
	}
	runtime.contentManager.SetReady(false)
	if runtime.exportManager != nil {
		return runtime.exportManager.PrepareSchemaDown(ctx, func() error {
			return runtime.prepareRecoverySchemaDown(ctx, func() error {
				return runtime.contentManager.PrepareSchemaDown(ctx, func() error {
					return runtime.transitioner.PrepareSchemaDown(ctx, callback)
				})
			})
		})
	}
	return runtime.prepareRecoverySchemaDown(ctx, func() error {
		return runtime.contentManager.PrepareSchemaDown(ctx, func() error {
			return runtime.transitioner.PrepareSchemaDown(ctx, callback)
		})
	})
}

func (runtime *Runtime) prepareRecoverySchemaDown(ctx context.Context, callback func() error) error {
	if runtime.recoveryManager != nil {
		return runtime.recoveryManager.PrepareSchemaDown(ctx, callback)
	}
	return callback()
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
	if installer, ok := readiness.(interface {
		SetManagedRecoveryPointRetention(task.ManagedRecoveryPointRetention)
	}); ok && runtime.managedTaskRetention != nil {
		installer.SetManagedRecoveryPointRetention(runtime.managedTaskRetention)
	}
	return nil
}

// StartupPass initializes admission before any command may be admitted, runs a
// bounded catalog/health pass, then uses unfiltered publication/TaskRun
// readiness before retention or later workers may mutate lifecycle state.
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
	if err := runtime.authorizeRequestedStartupEnablement(ctx); err != nil {
		return err
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
	if err := runtime.startupSearch(ctx); err != nil {
		return err
	}
	if runtime.processingManager != nil {
		if err := runtime.processingManager.Startup(ctx); err != nil {
			logger.Module("backup_asset_processing").Warn().Str("stage", "startup").Msg("备份资产处理运行时不可用，核心服务继续启动")
		}
	}
	if runtime.exportManager != nil {
		if err := runtime.exportManager.Startup(ctx); err != nil {
			logger.Module("backup_asset_export").Warn().Str("stage", "startup").Msg("备份资产导出运行时不可用，核心服务继续启动")
		}
	}
	if runtime.recoveryManager != nil {
		config, err := runtime.foundation.RecoveryConfig()
		if err != nil {
			return err
		}
		if err := runtime.recoveryManager.StartupWithConfig(ctx, config); err != nil {
			return err
		}
	}
	if runtime.retentionWorker != nil {
		if err := runtime.retentionWorker.StartupPass(ctx); err != nil {
			return err
		}
	}
	return nil
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
	coreDomains := append([]backupasset.KeyDomain(nil), backupasset.RequiredKeyDomains...)
	coreDomains = append(coreDomains, backupasset.KeyDomainSearchToken)
	if _, err := runtime.keyring.RewrapDomains(ctx, coreDomains...); err != nil {
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
	if runtime.retentionWorker != nil {
		runtime.retentionWorker.StopAccepting()
	}
	if runtime.recoveryManager != nil {
		runtime.recoveryManager.StopAccepting()
	}
	if runtime.exportManager != nil {
		runtime.exportManager.StopAccepting()
	}
	if runtime.contentManager != nil {
		runtime.contentManager.StopAccepting()
	}
	if runtime.processingManager != nil {
		runtime.processingManager.StopAccepting()
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
	if runtime.recoveryManager != nil {
		health.Add(1)
		go func() {
			defer health.Done()
			runtime.recoveryManager.Run(runCtx)
		}()
	}
	if runtime.exportManager != nil {
		health.Add(1)
		go func() {
			defer health.Done()
			runtime.exportManager.Run(runCtx)
		}()
	}
	if runtime.processingManager != nil {
		health.Add(1)
		go func() {
			defer health.Done()
			runtime.processingManager.Run(runCtx)
		}()
	}
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
	if runtime.retentionWorker != nil {
		health.Add(1)
		go func() {
			defer health.Done()
			runtime.retentionWorker.Run(runCtx)
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
	if runtime.retentionWorker != nil {
		if err := runtime.retentionWorker.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if runtime.recoveryManager != nil {
		if err := runtime.recoveryManager.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if runtime.exportManager != nil {
		if err := runtime.exportManager.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if runtime.processingManager != nil {
		if err := runtime.processingManager.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
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
	stopped       atomic.Bool
	runMu         sync.Mutex
	runCancel     context.CancelFunc
	runDone       chan struct{}
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
	runtime.runMu.Lock()
	if runtime.stopped.Load() || runtime.runDone != nil {
		runtime.runMu.Unlock()
		return
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	done := make(chan struct{})
	runtime.runCancel = cancelRun
	runtime.runDone = done
	runtime.runMu.Unlock()
	defer func() {
		cancelRun()
		runtime.runMu.Lock()
		if runtime.runDone == done {
			runtime.runCancel = nil
			runtime.runDone = nil
			close(done)
		}
		runtime.runMu.Unlock()
	}()
	for {
		if runtime.stopped.Load() {
			return
		}
		config, err := runtime.foundation.ContentConfig()
		interval := time.Minute
		if err == nil && config.ReconcileInterval > 0 {
			interval = config.ReconcileInterval
		}
		timer := time.NewTimer(interval)
		select {
		case <-runCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		stateErr, cacheErr := runtime.reconcileCycle(runCtx)
		if stateErr != nil && !errors.Is(stateErr, context.Canceled) {
			logger.Module("backup_asset_content").Warn().Str("stage", "state_reconcile").Msg("备份内容状态对账失败")
		}
		if cacheErr != nil && !errors.Is(cacheErr, context.Canceled) {
			logger.Module("backup_asset_content").Warn().Str("stage", "cache_reconcile").Msg("备份内容缓存对账失败")
		}
	}
}

func (runtime *managedContentRuntime) reconcileCycle(ctx context.Context) (stateErr, cacheErr error) {
	if runtime == nil || runtime.stopped.Load() {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if runtime.reconciler != nil {
		stateErr = runtime.reconciler.Reconcile(ctx)
	}
	if runtime.ready == nil || !runtime.ready.Load() {
		return stateErr, nil
	}
	runtime.mu.Lock()
	cache := runtime.cache
	runtime.mu.Unlock()
	if cache != nil {
		cacheErr = cache.Reconcile(ctx)
	}
	return stateErr, cacheErr
}

func (runtime *managedContentRuntime) Shutdown(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.stopped.Store(true)
	runtime.ready.Store(false)
	var shutdownErrors []error
	runtime.runMu.Lock()
	cancelRun := runtime.runCancel
	done := runtime.runDone
	if cancelRun != nil {
		cancelRun()
	}
	runtime.runMu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			shutdownErrors = append(shutdownErrors, fmt.Errorf("join Content run loop: %w", ctx.Err()))
		}
	}
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
		current.ProviderCapabilityRevision != expected.ProviderCapabilityRevision ||
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
		Provider: providerKind, ProviderCapabilityRevision: int64(record.PointCapability), SourceFingerprint: record.GenerationSourceFingerprint,
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
