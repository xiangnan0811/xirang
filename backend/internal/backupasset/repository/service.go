package repository

import (
	"context"
	"fmt"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"

	"gorm.io/gorm"
)

type AssetAuditSink interface {
	Write(context.Context, backupasset.AuditEventInput) error
}

type CatalogSummaryProjector interface {
	RepositorySummary(context.Context, string, catalog.AuthorizationScope) (catalog.RepositorySummaryDTO, error)
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
	recoverySourceNamespace RecoverySourceNamespaceAuthority
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
		preflights:              newRsyncVersioningPreflightStore(dependencies.Now),
	}, nil
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

func (service *Service) utcNow() time.Time { return service.now().UTC() }
