package repository

import (
	"context"
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

func (service *Service) ResolveLifecycleDeletePoint(
	ctx context.Context,
	operationID string,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) (provider.DeletePointRequest, error) {
	if service == nil || service.db == nil || backupasset.ValidateOpaqueID(operationID) != nil ||
		backupasset.ValidateOpaqueID(point.ID) != nil || backupasset.ValidateOpaqueID(repository.ID) != nil ||
		point.RepositoryID != repository.ID {
		return provider.DeletePointRequest{}, fmt.Errorf("%w: invalid lifecycle delete reconstruction", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	native, err := reconstructDeletePointNative(repository.ProviderKind, lifecycleDeleteLocator(point))
	if err != nil {
		return provider.DeletePointRequest{}, err
	}
	access, err := service.lifecycleDeleteAccess(ctx, repository, point, native)
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
			CapabilityRevision: repository.CapabilityRevision,
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
			return "", &provider.CapabilityError{
				Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityDeletionUnavailable},
			}
		}
		native := decoded.NativeCommitKey
		if native == "" {
			native = decoded.PortableAttemptRoot
		}
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
