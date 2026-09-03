package repository

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type AssetAuditSink interface {
	Write(context.Context, backupasset.AuditEventInput) error
}

type CatalogSummaryProjector interface {
	RepositorySummary(context.Context, string, catalog.AuthorizationScope) (catalog.RepositorySummaryDTO, error)
}

type CatalogWakeRequester interface {
	TryWake() bool
}

type Dependencies struct {
	DB                      *gorm.DB
	Foundation              *backupasset.FoundationService
	Registry                *provider.Registry
	Keyring                 *backupasset.Keyring
	Now                     func() time.Time
	Audit                   AssetAuditSink
	Admission               publication.Admission
	History                 *ManagedHistoryResolver
	Metrics                 publication.Metrics
	Publication             *PublicationService
	RclonePreflight         RcloneVersioningPreflighter
	CatalogOwnership        *catalog.Ownership
	CatalogSummary          CatalogSummaryProjector
	RecoverySourceNamespace RecoverySourceNamespaceAuthority
	CatalogRebuild          CatalogRebuildStarter
	DerivedBackfill         DerivedBackfillQueuer
	DerivedExpectations     DerivedExpectationSource
	ManifestProof           ManagedManifestProofVerifier
}

type Service struct {
	db          *gorm.DB
	foundation  *backupasset.FoundationService
	registry    *provider.Registry
	keyring     *backupasset.Keyring
	now         func() time.Time
	audit       AssetAuditSink
	admission   publication.Admission
	history     *ManagedHistoryResolver
	metrics     publication.Metrics
	publication *PublicationService
	preflights  *rsyncVersioningPreflightStore

	rcloneWorkflowMu        sync.Mutex
	rcloneSetups            *rcloneVersioningSetupStore
	rcloneCandidates        *rcloneBindingCandidateStore
	rclonePreflights        *rcloneVersioningPreflightStore
	rclonePreflighter       RcloneVersioningPreflighter
	catalogOwnership        *catalog.Ownership
	catalogSummary          CatalogSummaryProjector
	catalogWakeMu           sync.RWMutex
	catalogWake             CatalogWakeRequester
	recoverySourceNamespace RecoverySourceNamespaceAuthority
	catalogRebuild          CatalogRebuildStarter
	derivedBackfill         DerivedBackfillQueuer
	derivedExpectations     DerivedExpectationSource
	manifestProof           ManagedManifestProofVerifier

	importListingMu        sync.Mutex
	importListingCursors   map[string]string
	importListingSeen      map[string]map[string]normalizedImportCandidate
	importListingFromEmpty map[string]bool
	importCycleStartedAt   map[string]time.Time
	importListingComplete  map[string]bool
	importStaleAfterID     map[string]string
	importAfterID          string
	rebuildAfterID         string
	pendingDerivedMu       sync.Mutex
	pendingDerived         map[string]DerivedBackfillRequest
}

func NewService(dependencies Dependencies) (*Service, error) {
	if dependencies.Foundation == nil {
		return nil, fmt.Errorf("%w: backup asset foundation unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.CatalogOwnership == nil && dependencies.DB != nil {
		ownership, err := catalog.NewOwnership(dependencies.DB)
		if err != nil {
			return nil, err
		}
		dependencies.CatalogOwnership = ownership
	}
	return &Service{
		db: dependencies.DB, foundation: dependencies.Foundation, registry: dependencies.Registry, keyring: dependencies.Keyring,
		now: dependencies.Now, audit: dependencies.Audit, admission: dependencies.Admission, history: dependencies.History, metrics: dependencies.Metrics,
		publication:             dependencies.Publication,
		rclonePreflighter:       dependencies.RclonePreflight,
		catalogOwnership:        dependencies.CatalogOwnership,
		catalogSummary:          dependencies.CatalogSummary,
		recoverySourceNamespace: dependencies.RecoverySourceNamespace,
		catalogRebuild:          dependencies.CatalogRebuild,
		derivedBackfill:         dependencies.DerivedBackfill,
		derivedExpectations:     derivedExpectationSource(dependencies.DerivedExpectations, dependencies.DerivedBackfill),
		manifestProof:           dependencies.ManifestProof,
		preflights:              newRsyncVersioningPreflightStore(dependencies.Now),
		importListingCursors:    map[string]string{},
		importListingSeen:       map[string]map[string]normalizedImportCandidate{},
		importListingFromEmpty:  map[string]bool{},
		importCycleStartedAt:    map[string]time.Time{},
		importListingComplete:   map[string]bool{},
		importStaleAfterID:      map[string]string{},
		pendingDerived:          map[string]DerivedBackfillRequest{},
	}, nil
}

func (service *Service) SetRebuildPorts(catalogRebuild CatalogRebuildStarter, derivedBackfill DerivedBackfillQueuer) error {
	if service == nil {
		return fmt.Errorf("%w: repository service unavailable", backupasset.ErrInvalidState)
	}
	if catalogRebuild == nil || derivedBackfill == nil {
		return fmt.Errorf("%w: rebuild dependencies unavailable", backupasset.ErrInvalidState)
	}
	service.catalogRebuild = catalogRebuild
	service.derivedBackfill = derivedBackfill
	service.derivedExpectations = derivedExpectationSource(nil, derivedBackfill)
	return nil
}

func (service *Service) SetCatalogWake(requester CatalogWakeRequester) error {
	if service == nil {
		return fmt.Errorf("%w: repository service unavailable", backupasset.ErrInvalidState)
	}
	if requester == nil || isNilCatalogWakeRequester(requester) {
		return fmt.Errorf("%w: Catalog wake requester unavailable", backupasset.ErrInvalidState)
	}
	service.catalogWakeMu.Lock()
	defer service.catalogWakeMu.Unlock()
	if service.catalogWake != nil {
		return fmt.Errorf("%w: Catalog wake requester already configured", backupasset.ErrInvalidState)
	}
	service.catalogWake = requester
	return nil
}

func isNilCatalogWakeRequester(requester CatalogWakeRequester) bool {
	value := reflect.ValueOf(requester)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (service *Service) requestCatalogWake() {
	if service == nil {
		return
	}
	service.catalogWakeMu.RLock()
	requester := service.catalogWake
	service.catalogWakeMu.RUnlock()
	if requester != nil {
		_ = requester.TryWake()
	}
}

func derivedExpectationSource(explicit DerivedExpectationSource, queuer DerivedBackfillQueuer) DerivedExpectationSource {
	if explicit != nil {
		return explicit
	}
	if source, ok := queuer.(DerivedExpectationSource); ok {
		return source
	}
	return nil
}

func (service *Service) ensureEnabled(correlationID string) error {
	if service == nil || service.foundation == nil || !service.foundation.Enabled() {
		return capabilityError(backupasset.CapabilityFeatureDisabled, correlationID)
	}
	return nil
}

func (service *Service) requireRuntime() error {
	if service.db == nil || service.registry == nil {
		return fmt.Errorf("%w: repository service dependencies unavailable", backupasset.ErrInvalidState)
	}
	return nil
}

func (service *Service) PointDeleter(kind backupasset.ProviderKind) (provider.PointDeleter, error) {
	if err := service.requireRuntime(); err != nil {
		return nil, err
	}
	return service.registry.PointDeleter(kind)
}

func (service *Service) utcNow() time.Time { return service.now().UTC() }

// proveResticReadIdentity is the repository-owned fail-closed boundary for
// immutable Restic reads. The publication runtime carries access reconstructed
// from the point's producing Task; the live Probe therefore runs through this
// Service's registry rather than trusting the retained binding alone.
func (service *Service) proveResticReadIdentity(
	ctx context.Context,
	expectedRepositoryID string,
	expectedRepositoryIdentity string,
	expectedCapabilityRevision int,
	runtime publicationRepositoryRuntime,
) error {
	if service == nil || service.registry == nil || service.foundation == nil {
		return fmt.Errorf("%w: immutable Restic read proof unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if backupasset.ValidateOpaqueID(expectedRepositoryID) != nil {
		return fmt.Errorf("%w: immutable Restic repository ID is invalid", backupasset.ErrInvalidState)
	}
	nativeID := strings.TrimPrefix(expectedRepositoryIdentity, provider.NativeResticIdentityPrefix)
	normalizedIdentity, err := provider.NativeRepositoryIdentity(backupasset.ProviderRestic, nativeID)
	if err != nil || normalizedIdentity != expectedRepositoryIdentity {
		return fmt.Errorf("%w: immutable Restic repository identity is invalid", backupasset.ErrConflict)
	}
	if runtime.repository.ID != expectedRepositoryID || runtime.repository.ProviderKind != string(backupasset.ProviderRestic) ||
		runtime.repository.RepositoryIdentity == nil || *runtime.repository.RepositoryIdentity != expectedRepositoryIdentity {
		return fmt.Errorf("%w: immutable Restic read repository identity changed", backupasset.ErrConflict)
	}
	if runtime.document.Provider != backupasset.ProviderRestic || runtime.document.IdentityClass != provider.IdentityNativeRepository ||
		runtime.document.NativeRepositoryID != nativeID || strings.TrimSpace(runtime.document.AdapterRevision) == "" {
		return fmt.Errorf("%w: immutable Restic read binding identity changed", backupasset.ErrConflict)
	}
	if runtime.task.ID == 0 || runtime.task.NodeID == 0 ||
		runtime.access.Provider != backupasset.ProviderRestic ||
		runtime.access.RepositoryID != expectedRepositoryID ||
		runtime.access.TaskID != runtime.task.ID ||
		runtime.access.NodeID != runtime.task.NodeID ||
		strings.TrimSpace(runtime.access.Locator) == "" || len(runtime.access.Secret) == 0 {
		return fmt.Errorf("%w: immutable Restic read Task access changed", backupasset.ErrConflict)
	}
	runtimeAccess, ok := runtime.access.AdapterData.(provider.ResticRuntimeAccess)
	if !ok || runtimeAccess.Command == nil || runtimeAccess.Command.Node.ID != runtime.task.NodeID ||
		runtimeAccess.Command.Node.ID != runtime.access.NodeID || runtimeAccess.NativeRepositoryID != nativeID {
		return fmt.Errorf("%w: immutable Restic read runtime identity changed", backupasset.ErrConflict)
	}
	if expectedCapabilityRevision <= 0 {
		return fmt.Errorf("%w: immutable Restic capability revision is invalid", backupasset.ErrInvalidState)
	}
	if runtime.repository.CapabilityRevision != expectedCapabilityRevision {
		return fmt.Errorf("%w: immutable Restic read capability revision changed", backupasset.ErrConflict)
	}
	prober, err := service.registry.Prober(backupasset.ProviderRestic)
	if err != nil {
		return err
	}
	limits, err := service.providerOperationLimits()
	if err != nil {
		return err
	}
	observation, err := prober.Probe(ctx, runtime.access, limits)
	if err != nil {
		return err
	}
	if err := validateObservation(runtime.access, observation); err != nil {
		return err
	}
	if observation.RepositoryIdentity != expectedRepositoryIdentity || observation.AdapterRevision != runtime.document.AdapterRevision {
		return fmt.Errorf("%w: immutable Restic read observation changed", backupasset.ErrConflict)
	}
	return nil
}

func (service *Service) ResolveLifecycleDeletePoint(
	ctx context.Context,
	operationID string,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) (provider.DeletePointRequest, error) {
	if service == nil {
		return provider.DeletePointRequest{}, fmt.Errorf("%w: invalid lifecycle delete reconstruction", backupasset.ErrInvalidState)
	}
	return service.ResolveLifecycleDeletePointTx(ctx, service.db, operationID, point, repository)
}

// ResolveLifecycleDeletePointTx reconstructs the exact provider deletion
// request using only the caller-owned transaction. Every runtime, binding,
// and lineage lookup made by lifecycle deletion must use this entry point.
func (service *Service) ResolveLifecycleDeletePointTx(
	ctx context.Context,
	tx *gorm.DB,
	operationID string,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) (provider.DeletePointRequest, error) {
	if service == nil || tx == nil || backupasset.ValidateOpaqueID(operationID) != nil ||
		backupasset.ValidateOpaqueID(point.ID) != nil || backupasset.ValidateOpaqueID(repository.ID) != nil ||
		point.RepositoryID != repository.ID {
		return provider.DeletePointRequest{}, fmt.Errorf("%w: invalid lifecycle delete reconstruction", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if point.CapabilityRevision <= 0 || repository.CapabilityRevision <= 0 ||
		point.CapabilityRevision != repository.CapabilityRevision {
		return provider.DeletePointRequest{}, lifecycleDeleteIdentityConflict("lifecycle capability revision changed")
	}
	native, err := reconstructDeletePointNative(repository.ProviderKind, lifecycleDeleteLocator(point))
	if err != nil {
		return provider.DeletePointRequest{}, err
	}
	access, err := service.lifecycleDeleteAccessTx(ctx, tx, repository, point, native)
	if err != nil {
		return provider.DeletePointRequest{}, err
	}
	access.RepositoryID = repository.ID
	repositoryIdentity := ""
	if repository.RepositoryIdentity != nil {
		repositoryIdentity = strings.TrimSpace(*repository.RepositoryIdentity)
	}
	return provider.DeletePointRequest{
		Snapshot: provider.ReadSnapshot{
			RepositoryID:       repository.ID,
			CapabilityRevision: point.CapabilityRevision,
			SourceRevision:     point.SourceFingerprint,
			RepositoryIdentity: repositoryIdentity,
			Access:             access,
		},
		Point:                  provider.PointLocator{Native: native},
		ExpectedSourceRevision: point.SourceFingerprint,
		OperationID:            operationID,
	}, nil
}

func reconstructDeletePointNative(providerKind string, locator string) (string, error) {
	switch backupasset.ProviderKind(providerKind) {
	case backupasset.ProviderRestic:
		decoded, err := decodeResticPointLocator(locator)
		if err != nil || decoded.FullSnapshotID == "" {
			return "", &provider.CapabilityError{
				Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityDeletionUnavailable},
			}
		}
		return decoded.FullSnapshotID, nil
	case backupasset.ProviderRsync:
		decoded, err := decodeManagedRsyncPointLocator(locator)
		if err != nil || decoded.FinalComponent == "" {
			return "", &provider.CapabilityError{
				Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityDeletionUnavailable},
			}
		}
		return decoded.FinalComponent, nil
	case backupasset.ProviderRclone:
		decoded, err := decodeManagedRclonePointLocator(locator)
		if err != nil {
			if errors.Is(err, provider.ErrDeletePointIdentityConflict) {
				return "", err
			}
			return "", &provider.CapabilityError{
				Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityDeletionUnavailable},
			}
		}
		if decoded.PublicationMode == backupasset.PublicationNativeObjectVersions {
			attempt, attemptErr := provider.DecodeRcloneAttemptV1(decoded.TaggedAttempt)
			if attemptErr != nil || attempt.Native == nil {
				return "", &provider.CapabilityError{
					Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityDeletionUnavailable},
				}
			}
			// The managed prefix and exact commit version are recovered from
			// the locked runtime/evidence rows; the locator carries neither.
			return "native-commit", nil
		}
		native := decoded.PortableAttemptRoot
		if native == "" {
			return "", &provider.CapabilityError{
				Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityDeletionUnavailable},
			}
		}
		return native, nil
	default:
		return "", &provider.CapabilityError{
			Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityDeletionUnavailable},
		}
	}
}
