package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type rcloneBindingSetupKind string

const (
	rcloneBindingSetupPortable rcloneBindingSetupKind = "portable"
	rcloneBindingSetupNative   rcloneBindingSetupKind = "native"
)

type rcloneVersioningSetupRecord struct {
	id                   string
	kind                 rcloneBindingSetupKind
	taskID               uint
	expectedTaskRevision uint64
	baseBindingRevision  uint64
	externalID           string
	expiresAt            time.Time
}

// rcloneVersioningSetupStore intentionally remains process-local. A restart
// invalidates every outstanding setup so provider identity and secret input
// must be submitted against a fresh Task revision.
type rcloneVersioningSetupStore struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	records map[string]rcloneVersioningSetupRecord
}

type rclonePortableBindingCandidate struct {
	targetRemote       string
	managedRootLocator string
	boundConfig        provider.RcloneBoundConfig
}

type rcloneNativeBindingCandidate struct {
	request    backupasset.RcloneNativeBindingRequest
	externalID string
}

type rcloneBindingCandidate struct {
	kind                 rcloneBindingSetupKind
	purpose              backupasset.RclonePreflightPurpose
	taskID               uint
	expectedTaskRevision uint64
	bindingRevision      uint64
	configRevision       uint64
	credentialRevision   uint64
	setupID              string
	expiresAt            time.Time
	latestPreflightID    string
	preflighting         bool
	nativeObserved       bool
	settling             *RcloneVersioningPreflightEvidence
	settlingConfig       *backupasset.PublicationConfig
	plan                 rcloneBindingWorkflowPlan
	portable             *rclonePortableBindingCandidate
	native               *rcloneNativeBindingCandidate
}

type rcloneBindingCandidateStore struct {
	mu      sync.Mutex
	now     func() time.Time
	records map[uint]rcloneBindingCandidate
}

type RclonePortablePreflightInput struct {
	PreflightID            string
	CandidateID            string
	CandidateExpiresAt     time.Time
	TaskRevision           uint64
	ConfigRevision         uint64
	CredentialRevision     uint64
	CapabilityRevision     uint64
	TaskID                 uint
	NodeID                 uint
	BindingRevision        uint64
	BoundConfig            provider.RcloneBoundConfig
	TargetRemote           string `json:"-"`
	ManagedRootLocator     string `json:"-"`
	LegacyLocator          string `json:"-"`
	Runtime                provider.RemoteCommandAccess
	AbsoluteDeadline       time.Time
	LowLevelRetries        int
	ControlPayloadMaxBytes int64
	FullVerifyMaxBytes     int64
	ManifestOptions        provider.RcloneManifestBuildOptions
}

type RcloneNativePreflightInput struct {
	PreflightID            string
	CandidateID            string
	CandidateExpiresAt     time.Time
	TaskRevision           uint64
	ConfigRevision         uint64
	CredentialRevision     uint64
	CapabilityRevision     uint64
	PublicationConfig      backupasset.PublicationConfig
	TaskID                 uint
	NodeID                 uint
	BindingRevision        uint64
	Request                backupasset.RcloneNativeBindingRequest `json:"-"`
	ExternalID             string                                 `json:"-"`
	LegacyLocator          string                                 `json:"-"`
	Runtime                provider.RemoteCommandAccess
	AbsoluteDeadline       time.Time
	AWSSDKMaxAttempts      int
	LowLevelRetries        int
	ControlPayloadMaxBytes int64
	FullVerifyMaxBytes     uint64
	KMSReadKeyMaxCount     int
	ObservationLimits      provider.RcloneNativeObservationLimits
	RetainedReadKeys       []managedRcloneKMSReadKeyV3
}

type RcloneVersioningPreflightEvidence struct {
	Settling                  bool
	SettlingObservedAt        time.Time
	Mode                      backupasset.TaskPublicationMode
	CapabilityRevision        uint64
	ConsistencyClass          backupasset.RcloneConsistencyClass
	HashFidelity              backupasset.RcloneHashFidelity
	EstimatedReadBytes        uint64
	APICostClass              backupasset.RcloneCostClass
	StorageCostClass          backupasset.RcloneCostClass
	EgressCostClass           backupasset.RcloneCostClass
	CredentialExpiresAt       *time.Time
	EncryptionProfile         backupasset.RcloneEncryptionProfile
	KMSKeyStatus              backupasset.RcloneKMSKeyStatus
	KMSReadKeyCount           uint32
	ManagedRootIdentityDigest string
	RepositoryMarkerDigest    string
	EvidenceDigest            string
	Native                    *rcloneNativePreflightEvidence
}

type rcloneNativePreflightEvidence struct {
	VersioningDigest               string
	LifecycleDigest                string
	CapabilityStableObservedAt     time.Time
	BucketEncryptionDigest         string
	BucketKeyEnabled               bool
	CanaryEncryptionEvidenceDigest string
	ActiveKMSKeyDigest             string
	KMSCapabilityRevision          uint64
	RetainedReadKeys               []managedRcloneKMSReadKeyV3
}

type RcloneVersioningPreflighter interface {
	PreflightPortable(context.Context, RclonePortablePreflightInput) (RcloneVersioningPreflightEvidence, error)
	PreflightNative(context.Context, RcloneNativePreflightInput) (RcloneVersioningPreflightEvidence, error)
}

type rcloneVersioningPreflightRecord struct {
	id                string
	candidate         rcloneBindingCandidate
	evidence          RcloneVersioningPreflightEvidence
	expiresAt         time.Time
	publicationConfig backupasset.PublicationConfig
}

type rcloneImportedBaselinePreparation struct {
	preparedAt        time.Time
	markerKey         []byte
	leaseConfig       backupasset.LeaseConfig
	publicationConfig backupasset.PublicationConfig
	nativeInput       *managedRcloneNativeProcessInput
}

type rcloneImportedBaselineActivation struct {
	taskID        uint
	taskRunID     uint
	startedAt     time.Time
	legacyLocator string
	preflightID   string
	attempt       provider.RcloneAttemptV1
	childLease    backupasset.Lease
	binding       managedRcloneBindingDocumentV3
	markerKey     []byte
	leaseConfig   backupasset.LeaseConfig
	nativeInput   *managedRcloneNativeProcessInput
}

type rcloneVersioningActivationState struct {
	importedBaseline *rcloneImportedBaselineActivation
}

type rcloneVersioningPreflightStore struct {
	mu      sync.Mutex
	now     func() time.Time
	records map[string]rcloneVersioningPreflightRecord
}

func newRcloneVersioningPreflightStore(now func() time.Time) (*rcloneVersioningPreflightStore, error) {
	if now == nil {
		return nil, fmt.Errorf("%w: invalid Rclone preflight store", backupasset.ErrInvalidState)
	}
	return &rcloneVersioningPreflightStore{now: now, records: make(map[string]rcloneVersioningPreflightRecord)}, nil
}

func (store *rcloneVersioningPreflightStore) put(record rcloneVersioningPreflightRecord) error {
	if store == nil || backupasset.ValidateOpaqueID(record.id) != nil || record.candidate.taskID == 0 ||
		!record.expiresAt.After(store.now().UTC()) || validateRcloneVersioningPreflightEvidence(record.evidence) != nil {
		return fmt.Errorf("%w: invalid Rclone preflight record", backupasset.ErrInvalidState)
	}
	store.mu.Lock()
	store.purgeExpiredLocked(store.now().UTC())
	store.records[record.id] = record
	store.mu.Unlock()
	return nil
}

func (store *rcloneVersioningPreflightStore) get(id string) (rcloneVersioningPreflightRecord, bool) {
	if store == nil || backupasset.ValidateOpaqueID(id) != nil {
		return rcloneVersioningPreflightRecord{}, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.purgeExpiredLocked(store.now().UTC())
	record, ok := store.records[id]
	return record, ok
}

func (store *rcloneVersioningPreflightStore) consume(id string, taskID uint, expectedTaskRevision uint64) (rcloneVersioningPreflightRecord, bool) {
	if store == nil || backupasset.ValidateOpaqueID(id) != nil || taskID == 0 || expectedTaskRevision == 0 {
		return rcloneVersioningPreflightRecord{}, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.purgeExpiredLocked(store.now().UTC())
	record, ok := store.records[id]
	if !ok || record.candidate.taskID != taskID || record.candidate.expectedTaskRevision != expectedTaskRevision {
		return rcloneVersioningPreflightRecord{}, false
	}
	delete(store.records, id)
	return record, true
}

func (store *rcloneVersioningPreflightStore) purgeExpiredLocked(now time.Time) {
	for id, record := range store.records {
		if !record.expiresAt.After(now) {
			delete(store.records, id)
		}
	}
}

func newRcloneBindingCandidateStore(now func() time.Time) (*rcloneBindingCandidateStore, error) {
	if now == nil {
		return nil, fmt.Errorf("%w: invalid Rclone binding candidate store", backupasset.ErrInvalidState)
	}
	return &rcloneBindingCandidateStore{now: now, records: make(map[uint]rcloneBindingCandidate)}, nil
}

func (store *rcloneBindingCandidateStore) put(candidate rcloneBindingCandidate) error {
	if store == nil || candidate.taskID == 0 || candidate.expectedTaskRevision == 0 || candidate.bindingRevision == 0 ||
		candidate.configRevision == 0 || candidate.credentialRevision == 0 || backupasset.ValidateOpaqueID(candidate.setupID) != nil ||
		!candidate.expiresAt.After(store.now().UTC()) || (candidate.portable == nil) == (candidate.native == nil) {
		return fmt.Errorf("%w: invalid Rclone binding candidate", backupasset.ErrInvalidState)
	}
	store.mu.Lock()
	store.records[candidate.taskID] = candidate
	store.mu.Unlock()
	return nil
}

func (store *rcloneBindingCandidateStore) get(taskID uint) (rcloneBindingCandidate, bool) {
	if store == nil || taskID == 0 {
		return rcloneBindingCandidate{}, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	candidate, ok := store.records[taskID]
	if !ok {
		return rcloneBindingCandidate{}, false
	}
	if !candidate.expiresAt.After(store.now().UTC()) {
		delete(store.records, taskID)
		return rcloneBindingCandidate{}, false
	}
	return candidate, true
}

func (store *rcloneBindingCandidateStore) delete(taskID uint) {
	if store == nil || taskID == 0 {
		return
	}
	store.mu.Lock()
	delete(store.records, taskID)
	store.mu.Unlock()
}

func newRcloneVersioningSetupStore(now func() time.Time, ttl time.Duration) (*rcloneVersioningSetupStore, error) {
	if now == nil || ttl <= 0 {
		return nil, fmt.Errorf("%w: invalid Rclone setup store configuration", backupasset.ErrInvalidState)
	}
	return &rcloneVersioningSetupStore{now: now, ttl: ttl, records: make(map[string]rcloneVersioningSetupRecord)}, nil
}

func (store *rcloneVersioningSetupStore) create(request backupasset.RcloneBindingSetupRequest, kind rcloneBindingSetupKind, baseBindingRevision uint64) (backupasset.RcloneBindingSetupResult, error) {
	if store == nil || request.Validate() != nil || (kind != rcloneBindingSetupPortable && kind != rcloneBindingSetupNative) {
		return backupasset.RcloneBindingSetupResult{}, fmt.Errorf("%w: invalid Rclone binding setup", backupasset.ErrInvalidState)
	}
	setupID, err := backupasset.NewOpaqueID()
	if err != nil {
		return backupasset.RcloneBindingSetupResult{}, err
	}
	externalID := ""
	if kind == rcloneBindingSetupNative {
		externalComponent, err := backupasset.NewOpaqueID()
		if err != nil {
			return backupasset.RcloneBindingSetupResult{}, err
		}
		externalID = "xirang-" + externalComponent
	}
	record := rcloneVersioningSetupRecord{
		id: setupID, kind: kind, taskID: request.TaskID, expectedTaskRevision: request.ExpectedTaskRevision,
		baseBindingRevision: baseBindingRevision, externalID: externalID, expiresAt: store.now().UTC().Add(store.ttl),
	}
	store.mu.Lock()
	store.purgeExpiredLocked(store.now().UTC())
	store.records[setupID] = record
	store.mu.Unlock()
	result := backupasset.RcloneBindingSetupResult{
		SetupID: setupID, ExpiresAt: record.expiresAt, BaseBindingRevision: strconv.FormatUint(record.baseBindingRevision, 10), ExternalID: externalID,
	}
	if err := result.Validate(kind == rcloneBindingSetupNative); err != nil {
		return backupasset.RcloneBindingSetupResult{}, err
	}
	return result, nil
}

func (store *rcloneVersioningSetupStore) consume(id string, taskID uint, expectedTaskRevision uint64, kind rcloneBindingSetupKind) (rcloneVersioningSetupRecord, bool) {
	if store == nil || backupasset.ValidateOpaqueID(id) != nil || taskID == 0 || expectedTaskRevision == 0 ||
		(kind != rcloneBindingSetupPortable && kind != rcloneBindingSetupNative) {
		return rcloneVersioningSetupRecord{}, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	store.purgeExpiredLocked(now)
	record, ok := store.records[id]
	if !ok || record.kind != kind || record.taskID != taskID || record.expectedTaskRevision != expectedTaskRevision || !record.expiresAt.After(now) {
		return rcloneVersioningSetupRecord{}, false
	}
	delete(store.records, id)
	return record, true
}

func (store *rcloneVersioningSetupStore) purgeExpiredLocked(now time.Time) {
	for id, record := range store.records {
		if !record.expiresAt.After(now) {
			delete(store.records, id)
		}
	}
}

func (service *Service) CreateRclonePortableBindingSetup(ctx context.Context, request backupasset.RcloneBindingSetupRequest) (backupasset.RcloneBindingSetupResult, error) {
	return service.createRcloneBindingSetup(ctx, request, rcloneBindingSetupPortable)
}

func (service *Service) CreateRclonePortableBindingSetupForRequest(ctx context.Context, request backupasset.RcloneBindingSetupRequest, requestContext RequestContext) (backupasset.RcloneBindingSetupResult, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningPortableSetup, request.TaskID, backupasset.PublicationVersionedPrefix, "", "", err)
		return backupasset.RcloneBindingSetupResult{}, err
	}
	result, err := service.CreateRclonePortableBindingSetup(ctx, request)
	service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningPortableSetup, request.TaskID, backupasset.PublicationVersionedPrefix, "", "", err)
	return result, err
}

func (service *Service) CreateRcloneNativeBindingSetup(ctx context.Context, request backupasset.RcloneBindingSetupRequest) (backupasset.RcloneBindingSetupResult, error) {
	return service.createRcloneBindingSetup(ctx, request, rcloneBindingSetupNative)
}

func (service *Service) CreateRcloneNativeBindingSetupForRequest(ctx context.Context, request backupasset.RcloneBindingSetupRequest, requestContext RequestContext) (backupasset.RcloneBindingSetupResult, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningNativeSetup, request.TaskID, backupasset.PublicationNativeObjectVersions, "", "", err)
		return backupasset.RcloneBindingSetupResult{}, err
	}
	result, err := service.CreateRcloneNativeBindingSetup(ctx, request)
	service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningNativeSetup, request.TaskID, backupasset.PublicationNativeObjectVersions, "", "", err)
	return result, err
}

func (service *Service) createRcloneBindingSetup(ctx context.Context, request backupasset.RcloneBindingSetupRequest, kind rcloneBindingSetupKind) (backupasset.RcloneBindingSetupResult, error) {
	if err := request.Validate(); err != nil {
		return backupasset.RcloneBindingSetupResult{}, err
	}
	if err := service.ensureEnabled(""); err != nil {
		return backupasset.RcloneBindingSetupResult{}, err
	}
	if service == nil || service.db == nil {
		return backupasset.RcloneBindingSetupResult{}, fmt.Errorf("%w: Rclone binding setup dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	plan, err := service.validateRcloneBindingSetupTask(ctx, request, kind)
	if err != nil {
		return backupasset.RcloneBindingSetupResult{}, err
	}
	store, err := service.rcloneSetupStore()
	if err != nil {
		return backupasset.RcloneBindingSetupResult{}, err
	}
	return store.create(request, kind, plan.bindingRevision)
}

func (service *Service) rcloneSetupStore() (*rcloneVersioningSetupStore, error) {
	if service == nil || service.foundation == nil {
		return nil, fmt.Errorf("%w: Rclone setup store dependencies are unavailable", backupasset.ErrInvalidState)
	}
	publicationConfig, err := service.foundation.PublicationConfig()
	if err != nil {
		return nil, err
	}
	service.rcloneWorkflowMu.Lock()
	defer service.rcloneWorkflowMu.Unlock()
	if service.rcloneSetups == nil {
		store, err := newRcloneVersioningSetupStore(service.now, publicationConfig.Rclone.PreflightTTL)
		if err != nil {
			return nil, err
		}
		service.rcloneSetups = store
	}
	if service.rcloneCandidates == nil {
		candidates, err := newRcloneBindingCandidateStore(service.now)
		if err != nil {
			return nil, err
		}
		service.rcloneCandidates = candidates
	}
	if service.rclonePreflights == nil {
		preflights, err := newRcloneVersioningPreflightStore(service.now)
		if err != nil {
			return nil, err
		}
		service.rclonePreflights = preflights
	}
	return service.rcloneSetups, nil
}

func (service *Service) validateRcloneBindingSetupTask(ctx context.Context, request backupasset.RcloneBindingSetupRequest, kind rcloneBindingSetupKind) (rcloneBindingWorkflowPlan, error) {
	plan, err := service.loadRcloneBindingWorkflowPlan(ctx, request.TaskID, request.ExpectedTaskRevision, kind)
	if err != nil {
		return rcloneBindingWorkflowPlan{}, err
	}
	if plan.managedBinding != nil {
		return rcloneBindingWorkflowPlan{}, fmt.Errorf("%w: managed Rclone binding cannot be replaced", backupasset.ErrConflict)
	}
	return plan, nil
}

type rcloneBindingWorkflowPlan struct {
	task              model.Task
	link              model.TaskRepositoryLink
	repository        model.BackupRepository
	legacyBinding     bindingDocument
	managedBinding    *managedRcloneBindingDocumentV3
	binding           model.RepositoryAccessBinding
	bindingCiphertext string
	bindingRevision   uint64
}

func (service *Service) loadRcloneBindingWorkflowPlan(ctx context.Context, taskID uint, expectedTaskRevision uint64, kind rcloneBindingSetupKind) (rcloneBindingWorkflowPlan, error) {
	var plan rcloneBindingWorkflowPlan
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		if err := tx.Preload("Node.SSHKey").Where("id = ? AND archived_at IS NULL", taskID).First(&taskEntity).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: Rclone versioning Task", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load Rclone versioning Task: %w", err)
		}
		if bindingProviderForTask(taskEntity) != backupasset.ProviderRclone {
			return fmt.Errorf("%w: Rclone binding setup requires an Rclone Task", backupasset.ErrCapabilityUnavailable)
		}
		revision, err := managedRsyncTaskRevision(taskEntity)
		if err != nil {
			return err
		}
		if revision != expectedTaskRevision {
			return fmt.Errorf("%w: Rclone versioning Task revision changed", backupasset.ErrConflict)
		}

		var link model.TaskRepositoryLink
		if err := tx.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: Rclone versioning link", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load Rclone versioning link: %w", err)
		}
		var repository model.BackupRepository
		if err := tx.First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
			return fmt.Errorf("load Rclone versioning repository: %w", err)
		}
		var binding model.RepositoryAccessBinding
		if err := tx.Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
			return fmt.Errorf("load Rclone versioning binding: %w", err)
		}
		var bindingCiphertext string
		if err := tx.Raw("SELECT encrypted_config FROM repository_access_bindings WHERE id = ?", binding.ID).Scan(&bindingCiphertext).Error; err != nil {
			return fmt.Errorf("load Rclone binding envelope: %w", err)
		}
		stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
		if err != nil {
			return err
		}

		switch backupasset.TaskPublicationMode(link.PublicationMode) {
		case backupasset.PublicationLegacyMutable:
			if repository.ProviderKind != string(backupasset.ProviderRclone) || repository.VersionMode != string(backupasset.VersionMutableHead) ||
				repository.ImmutabilityLevel != string(backupasset.ImmutabilityMutable) || link.EncryptedLegacyLocator == "" ||
				link.EncryptedLegacyLocator != taskEntity.RsyncTarget || stored.V1 == nil || stored.ManagedRsyncV2 != nil || stored.ManagedRcloneV3 != nil ||
				stored.V1.Provider != backupasset.ProviderRclone || stored.V1.TaskID != taskEntity.ID || stored.V1.NodeID != taskEntity.NodeID ||
				stored.V1.Locator != link.EncryptedLegacyLocator || stored.V1.ConfigSource != provider.RcloneConfigNodeDefault {
				return fmt.Errorf("%w: Rclone binding setup requires exact legacy state", backupasset.ErrConflict)
			}
			plan = rcloneBindingWorkflowPlan{task: taskEntity, link: link, repository: repository, binding: binding, legacyBinding: *stored.V1}
		case backupasset.PublicationVersionedPrefix, backupasset.PublicationNativeObjectVersions:
			if stored.ManagedRcloneV3 == nil || stored.V1 != nil || stored.ManagedRsyncV2 != nil ||
				validateManagedRcloneBindingAssociation(*stored.ManagedRcloneV3, managedRcloneBindingAssociation{
					Task: taskEntity, Link: link, Repository: repository,
				}) != nil || (kind == rcloneBindingSetupPortable) != (link.PublicationMode == string(backupasset.PublicationVersionedPrefix)) {
				return fmt.Errorf("%w: Rclone binding rotation state changed", backupasset.ErrConflict)
			}
			legacy, err := decodeBindingDocument(stored.ManagedRcloneV3.LegacyBindingV1)
			if err != nil {
				return err
			}
			document := *stored.ManagedRcloneV3
			plan = rcloneBindingWorkflowPlan{
				task: taskEntity, link: link, repository: repository, binding: binding, legacyBinding: legacy,
				managedBinding: &document, bindingRevision: document.BindingRevision,
			}
		default:
			return fmt.Errorf("%w: unsupported Rclone binding setup mode", backupasset.ErrCapabilityUnavailable)
		}
		plan.bindingCiphertext = bindingCiphertext
		return nil
	})
	if err != nil {
		return rcloneBindingWorkflowPlan{}, err
	}
	return plan, nil
}

func (service *Service) SetRclonePortableBinding(ctx context.Context, request backupasset.RclonePortableBindingRequest) (backupasset.RclonePublicationSummary, error) {
	if err := request.Validate(); err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	if err := service.ensureEnabled(""); err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	setups, err := service.rcloneSetupStore()
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	setup, ok := setups.consume(request.SetupID, request.TaskID, request.ExpectedTaskRevision, rcloneBindingSetupPortable)
	if !ok {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: Rclone portable binding setup expired or unavailable", backupasset.ErrConflict)
	}
	plan, err := service.loadRcloneBindingWorkflowPlan(ctx, request.TaskID, request.ExpectedTaskRevision, rcloneBindingSetupPortable)
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	if plan.managedBinding != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: managed Rclone binding cannot be replaced", backupasset.ErrConflict)
	}
	if request.ExpectedBindingRevision != setup.baseBindingRevision || setup.baseBindingRevision != plan.bindingRevision {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: Rclone binding revision changed", backupasset.ErrConflict)
	}
	salt, err := hexDecodeSalt(plan.legacyBinding.IdentitySalt)
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	publicationConfig, err := service.foundation.PublicationConfig()
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	bound, err := provider.ValidateRcloneBoundConfigV1744(
		[]byte(request.BoundConfig), request.TargetRemote, salt, publicationConfig.Rclone.BoundConfigMaxBytes,
	)
	if err != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: portable Rclone bound config rejected", backupasset.ErrCapabilityUnavailable)
	}
	if _, err := provider.NewRclonePrivateLocator(request.ManagedRootLocator); err != nil ||
		!strings.HasPrefix(request.ManagedRootLocator, request.TargetRemote+":") ||
		rclonePrivateLocatorsOverlap(plan.legacyBinding.Locator, request.ManagedRootLocator) {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: portable Rclone managed root overlaps legacy target", backupasset.ErrConflict)
	}
	candidate := rcloneBindingCandidate{
		kind: rcloneBindingSetupPortable, taskID: request.TaskID, expectedTaskRevision: request.ExpectedTaskRevision,
		bindingRevision: plan.bindingRevision + 1, configRevision: plan.bindingRevision + 1,
		credentialRevision: plan.bindingRevision + 1, setupID: setup.id, expiresAt: setup.expiresAt, plan: plan,
		portable: &rclonePortableBindingCandidate{
			targetRemote: request.TargetRemote, managedRootLocator: request.ManagedRootLocator, boundConfig: bound,
		},
	}
	if service.rcloneCandidates == nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: Rclone binding candidate store unavailable", backupasset.ErrInvalidState)
	}
	if err := service.replaceRcloneCandidate(candidate); err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	return rcloneBindingCandidateSummary(candidate), nil
}

func (service *Service) SetRclonePortableBindingForRequest(ctx context.Context, request backupasset.RclonePortableBindingRequest, requestContext RequestContext) (backupasset.RclonePublicationSummary, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningPortableBinding, request.TaskID, backupasset.PublicationVersionedPrefix, "", "", err)
		return backupasset.RclonePublicationSummary{}, err
	}
	result, err := service.SetRclonePortableBinding(ctx, request)
	service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningPortableBinding, request.TaskID, backupasset.PublicationVersionedPrefix, result.State, result.ReasonCode, err)
	return result, err
}

func (service *Service) SetRcloneNativeBinding(ctx context.Context, request backupasset.RcloneNativeBindingRequest) (backupasset.RclonePublicationSummary, error) {
	if err := request.Validate(); err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	if err := service.ensureEnabled(""); err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	setups, err := service.rcloneSetupStore()
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	setup, ok := setups.consume(request.SetupID, request.TaskID, request.ExpectedTaskRevision, rcloneBindingSetupNative)
	if !ok {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: Rclone native binding setup expired or unavailable", backupasset.ErrConflict)
	}
	plan, err := service.loadRcloneBindingWorkflowPlan(ctx, request.TaskID, request.ExpectedTaskRevision, rcloneBindingSetupNative)
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	if plan.managedBinding != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: managed Rclone binding cannot be replaced", backupasset.ErrConflict)
	}
	if request.ExpectedBindingRevision != setup.baseBindingRevision || setup.baseBindingRevision != plan.bindingRevision {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: Rclone binding revision changed", backupasset.ErrConflict)
	}
	profile := provider.RcloneNativeProfile{
		Code: provider.RcloneNativeAWSS3GeneralPurposeV1, Region: request.Region, Bucket: request.Bucket,
		ManagedPrefix: request.ManagedPrefix, EndpointMode: provider.RcloneNativeEndpointAWSRegional,
		AddressingMode: provider.RcloneNativeAddressingDNS, BucketKind: provider.RcloneNativeBucketGeneralPurpose,
	}
	if err := provider.ValidateRcloneNativeProfile(profile); err != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: native Rclone profile rejected", backupasset.ErrCapabilityUnavailable)
	}
	roleAccount, roleOK := managedRcloneAWSRoleAccount(request.RoleARN)
	if !roleOK || !validManagedRcloneExternalID(setup.externalID) {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: native Rclone identity rejected", backupasset.ErrCapabilityUnavailable)
	}
	if request.EncryptionProfile == backupasset.RcloneEncryptionSSEKMS &&
		!validManagedRcloneKMSKeyARN(request.KMSKeyARN, request.Region, roleAccount) {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: native Rclone KMS identity rejected", backupasset.ErrCapabilityUnavailable)
	}
	candidate := rcloneBindingCandidate{
		kind: rcloneBindingSetupNative, taskID: request.TaskID, expectedTaskRevision: request.ExpectedTaskRevision,
		bindingRevision: plan.bindingRevision + 1, configRevision: plan.bindingRevision + 1,
		credentialRevision: plan.bindingRevision + 1, setupID: setup.id, expiresAt: setup.expiresAt, plan: plan,
		native: &rcloneNativeBindingCandidate{request: request, externalID: setup.externalID},
	}
	if service.rcloneCandidates == nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: Rclone binding candidate store unavailable", backupasset.ErrInvalidState)
	}
	if err := service.replaceRcloneCandidate(candidate); err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	return rcloneBindingCandidateSummary(candidate), nil
}

func (service *Service) SetRcloneNativeBindingForRequest(ctx context.Context, request backupasset.RcloneNativeBindingRequest, requestContext RequestContext) (backupasset.RclonePublicationSummary, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningNativeBinding, request.TaskID, backupasset.PublicationNativeObjectVersions, "", "", err)
		return backupasset.RclonePublicationSummary{}, err
	}
	result, err := service.SetRcloneNativeBinding(ctx, request)
	service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningNativeBinding, request.TaskID, backupasset.PublicationNativeObjectVersions, result.State, result.ReasonCode, err)
	return result, err
}

func rclonePrivateLocatorsOverlap(left, right string) bool {
	leftRemote, leftPath, leftOK := strings.Cut(left, ":")
	rightRemote, rightPath, rightOK := strings.Cut(right, ":")
	if !leftOK || !rightOK || leftRemote != rightRemote {
		return false
	}
	normalize := func(value string) string { return strings.Trim(strings.TrimSpace(value), "/") }
	leftPath, rightPath = normalize(leftPath), normalize(rightPath)
	if leftPath == "" || rightPath == "" {
		return true
	}
	return leftPath == rightPath || strings.HasPrefix(leftPath, rightPath+"/") || strings.HasPrefix(rightPath, leftPath+"/")
}

func rcloneBindingCandidateSummary(candidate rcloneBindingCandidate) backupasset.RclonePublicationSummary {
	encryption := backupasset.RcloneEncryptionNone
	kmsStatus := backupasset.RcloneKMSNotApplicable
	mode := backupasset.PublicationVersionedPrefix
	if candidate.native != nil {
		mode = backupasset.PublicationNativeObjectVersions
		encryption = candidate.native.request.EncryptionProfile
		kmsStatus = backupasset.RcloneKMSReady
		if encryption == backupasset.RcloneEncryptionSSES3 {
			kmsStatus = backupasset.RcloneKMSNotApplicable
		}
	}
	return backupasset.RclonePublicationSummary{
		Mode: mode, State: backupasset.RcloneStatePreflightRequired, ReasonCode: backupasset.RcloneReasonPreflightRequired,
		TaskRevision: strconv.FormatUint(candidate.expectedTaskRevision, 10), BindingRevision: strconv.FormatUint(candidate.bindingRevision, 10),
		CapabilityRevision: "0", ConsistencyClass: backupasset.RcloneConsistencyNotEvaluated,
		HashFidelity: backupasset.RcloneHashNotEvaluated, EstimatedReadBytes: "0",
		APICostClass: backupasset.RcloneCostNotEvaluated, StorageCostClass: backupasset.RcloneCostNotEvaluated,
		EgressCostClass: backupasset.RcloneCostNotEvaluated, EncryptionProfile: encryption, KMSKeyStatus: kmsStatus,
		RollbackLocatorPresent: true, RollbackCapability: backupasset.RcloneRollbackCleanAvailable,
	}
}

func (service *Service) CreateRcloneVersioningPreflight(ctx context.Context, request backupasset.RcloneVersioningPreflightRequest) (backupasset.RcloneVersioningPreflightResult, error) {
	if err := request.Validate(); err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	if err := service.ensureEnabled(""); err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := service.rcloneSetupStore(); err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	if service.rcloneCandidates == nil || service.rclonePreflights == nil || service.db == nil {
		return backupasset.RcloneVersioningPreflightResult{}, fmt.Errorf("%w: Rclone preflight dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if service.rclonePreflighter == nil {
		return backupasset.RcloneVersioningPreflightResult{}, &provider.CapabilityError{Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable}}
	}
	preflightID, err := backupasset.NewOpaqueID()
	if err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	if err := service.prepareRcloneRenewalCandidate(ctx, request); err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	candidate, err := service.beginRclonePreflight(request.TaskID, request.ExpectedTaskRevision, request.RequestedMode, preflightID)
	if err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	defer service.endRclonePreflight(candidate)
	if err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockRcloneWorkflowPlanTx(ctx, tx, candidate.plan, candidate.purpose == backupasset.RclonePreflightPurposeRenewal)
		if err == nil && candidate.purpose == backupasset.RclonePreflightPurposeRenewal && locked.task.Enabled {
			return fmt.Errorf("%w: Rclone renewal requires a paused Task", backupasset.ErrConflict)
		}
		return err
	}); err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	plan := candidate.plan
	if plan.repository.CapabilityRevision <= 0 || (candidate.purpose != backupasset.RclonePreflightPurposeRenewal && plan.repository.CapabilityRevision >= math.MaxInt32) {
		return backupasset.RcloneVersioningPreflightResult{}, fmt.Errorf("%w: Rclone capability revision overflow", backupasset.ErrConflict)
	}
	capabilityRevision := uint64(plan.repository.CapabilityRevision)
	if candidate.purpose != backupasset.RclonePreflightPurposeRenewal {
		capabilityRevision++
	}
	publicationConfig, err := service.foundation.PublicationConfig()
	if err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	manifestOptions, err := rcloneManifestOptions(publicationConfig)
	if err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	observationLimits, err := managedRcloneNativeObservationLimits(publicationConfig.ManifestMaxEntries)
	if err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	now := service.utcNow()
	if candidate.kind == rcloneBindingSetupNative && !candidate.nativeObserved &&
		!candidate.expiresAt.After(now.Add(rcloneNativePreflightSettleWindow+sshutil.CommandExecutionJoinTimeout)) {
		return backupasset.RcloneVersioningPreflightResult{}, fmt.Errorf("%w: Rclone candidate has insufficient observation time", backupasset.ErrConflict)
	}
	deadline := now.Add(publicationConfig.Rclone.PortableDeadline)
	if candidate.kind == rcloneBindingSetupNative {
		deadline = now.Add(publicationConfig.Rclone.NativeDeadline)
	}
	if candidate.expiresAt.Before(deadline) {
		deadline = candidate.expiresAt
	}
	if !deadline.After(now.Add(sshutil.CommandExecutionJoinTimeout)) {
		return backupasset.RcloneVersioningPreflightResult{}, fmt.Errorf("%w: Rclone preflight deadline is too short", backupasset.ErrConflict)
	}
	operationCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	runtime := provider.RemoteCommandAccess{Node: plan.task.Node}
	var evidence RcloneVersioningPreflightEvidence
	switch candidate.kind {
	case rcloneBindingSetupPortable:
		evidence, err = service.rclonePreflighter.PreflightPortable(operationCtx, RclonePortablePreflightInput{
			PreflightID: preflightID, CandidateID: candidate.setupID, CandidateExpiresAt: candidate.expiresAt,
			TaskID: request.TaskID, NodeID: plan.task.NodeID, TaskRevision: request.ExpectedTaskRevision,
			BindingRevision: candidate.bindingRevision, ConfigRevision: candidate.configRevision,
			CredentialRevision: candidate.credentialRevision, CapabilityRevision: capabilityRevision,
			BoundConfig: candidate.portable.boundConfig, TargetRemote: candidate.portable.targetRemote,
			ManagedRootLocator: candidate.portable.managedRootLocator, LegacyLocator: plan.legacyBinding.Locator,
			Runtime: runtime, AbsoluteDeadline: deadline, LowLevelRetries: publicationConfig.Rclone.LowLevelRetries,
			ControlPayloadMaxBytes: publicationConfig.Rclone.ControlPayloadMaxBytes,
			FullVerifyMaxBytes:     publicationConfig.Rclone.FullVerifyMaxBytes, ManifestOptions: manifestOptions,
		})
	case rcloneBindingSetupNative:
		var retainedReadKeys []managedRcloneKMSReadKeyV3
		if plan.managedBinding != nil && plan.managedBinding.Native != nil {
			retainedReadKeys = append(retainedReadKeys, plan.managedBinding.Native.RetainedReadKeys...)
		}
		evidence, err = service.rclonePreflighter.PreflightNative(operationCtx, RcloneNativePreflightInput{
			PreflightID: preflightID, CandidateID: candidate.setupID, CandidateExpiresAt: candidate.expiresAt,
			TaskID: request.TaskID, NodeID: plan.task.NodeID, TaskRevision: request.ExpectedTaskRevision,
			BindingRevision: candidate.bindingRevision, ConfigRevision: candidate.configRevision,
			CredentialRevision: candidate.credentialRevision, CapabilityRevision: capabilityRevision,
			PublicationConfig: publicationConfig,
			Request:           candidate.native.request, ExternalID: candidate.native.externalID, LegacyLocator: plan.legacyBinding.Locator,
			Runtime: runtime, AbsoluteDeadline: deadline, AWSSDKMaxAttempts: publicationConfig.Rclone.AWSSDKMaxAttempts,
			LowLevelRetries: publicationConfig.Rclone.LowLevelRetries, ControlPayloadMaxBytes: publicationConfig.Rclone.ControlPayloadMaxBytes,
			FullVerifyMaxBytes: uint64(publicationConfig.Rclone.FullVerifyMaxBytes),
			KMSReadKeyMaxCount: publicationConfig.Rclone.KMSReadKeyMaxCount, ObservationLimits: observationLimits,
			RetainedReadKeys: retainedReadKeys,
		})
	default:
		return backupasset.RcloneVersioningPreflightResult{}, fmt.Errorf("%w: invalid Rclone binding candidate", backupasset.ErrInvalidState)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && !candidate.expiresAt.After(service.utcNow()) {
			return backupasset.RcloneVersioningPreflightResult{}, errors.Join(fmt.Errorf("%w: Rclone candidate expired", backupasset.ErrConflict), err)
		}
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	if err := operationCtx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) && !candidate.expiresAt.After(service.utcNow()) {
			return backupasset.RcloneVersioningPreflightResult{}, fmt.Errorf("%w: Rclone candidate expired", backupasset.ErrConflict)
		}
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	if !deadline.After(service.utcNow()) {
		if !candidate.expiresAt.After(service.utcNow()) {
			return backupasset.RcloneVersioningPreflightResult{}, fmt.Errorf("%w: Rclone candidate expired", backupasset.ErrConflict)
		}
		return backupasset.RcloneVersioningPreflightResult{}, context.DeadlineExceeded
	}
	if evidence.Mode != request.RequestedMode {
		return backupasset.RcloneVersioningPreflightResult{}, fmt.Errorf("%w: invalid Rclone preflight evidence", backupasset.ErrInvalidState)
	}
	if candidate.native != nil && (evidence.EncryptionProfile != candidate.native.request.EncryptionProfile ||
		uint64(evidence.KMSReadKeyCount) > uint64(publicationConfig.Rclone.KMSReadKeyMaxCount)) {
		return backupasset.RcloneVersioningPreflightResult{}, fmt.Errorf("%w: inconsistent native Rclone encryption evidence", backupasset.ErrInvalidState)
	}
	if evidence.Settling {
		err = validateRcloneSettlingEvidence(evidence, service.utcNow())
	} else {
		err = validateRcloneVersioningPreflightEvidence(evidence)
		if err == nil && evidence.CapabilityRevision != capabilityRevision {
			err = fmt.Errorf("%w: Rclone capability revision changed", backupasset.ErrInvalidState)
		}
	}
	if err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	if !evidence.Settling && candidate.purpose == backupasset.RclonePreflightPurposeRenewal {
		if err := validateRcloneRenewalEvidence(candidate.plan.managedBinding, evidence); err != nil {
			return backupasset.RcloneVersioningPreflightResult{}, err
		}
	}
	currentConfig, err := service.foundation.PublicationConfig()
	if err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	if !reflect.DeepEqual(publicationConfig, currentConfig) {
		return backupasset.RcloneVersioningPreflightResult{}, fmt.Errorf("%w: Rclone publication configuration changed", backupasset.ErrConflict)
	}
	expiresAt := candidate.expiresAt
	if !evidence.Settling {
		if ttlExpiry := service.utcNow().Add(publicationConfig.Rclone.PreflightTTL); ttlExpiry.Before(expiresAt) {
			expiresAt = ttlExpiry
		}
		if evidence.CredentialExpiresAt != nil {
			if credentialExpiry := evidence.CredentialExpiresAt.Add(-rcloneNativePreflightSessionMargin); credentialExpiry.Before(expiresAt) {
				expiresAt = credentialExpiry
			}
		}
	}
	record := rcloneVersioningPreflightRecord{id: preflightID, candidate: candidate, evidence: evidence, expiresAt: expiresAt, publicationConfig: publicationConfig}
	summary := rclonePreflightSummary(candidate, evidence)
	purpose := candidate.purpose
	if purpose == "" {
		purpose = backupasset.RclonePreflightPurposeActivation
	}
	result := backupasset.RcloneVersioningPreflightResult{Purpose: purpose, PreflightID: preflightID, ExpiresAt: expiresAt, Summary: summary}
	if err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockRcloneWorkflowPlanTx(ctx, tx, candidate.plan, candidate.purpose == backupasset.RclonePreflightPurposeRenewal)
		if err != nil {
			return err
		}
		if candidate.purpose == backupasset.RclonePreflightPurposeRenewal && locked.task.Enabled {
			return fmt.Errorf("%w: Rclone renewal requires a paused Task", backupasset.ErrConflict)
		}
		current, err := service.foundation.PublicationConfigTx(ctx, tx)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, publicationConfig) {
			return fmt.Errorf("%w: Rclone publication configuration changed", backupasset.ErrConflict)
		}
		return nil
	}); err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	if purpose == backupasset.RclonePreflightPurposeRenewal {
		result.Summary.RollbackCapability, err = service.rcloneManagedRollbackCapability(ctx, plan.repository.ID)
		if err != nil {
			return backupasset.RcloneVersioningPreflightResult{}, err
		}
	}
	if err := result.Validate(); err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	if err := service.finalizeRclonePreflight(candidate, record); err != nil {
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	return result, nil
}

func (service *Service) CreateRcloneVersioningPreflightForRequest(ctx context.Context, request backupasset.RcloneVersioningPreflightRequest, requestContext RequestContext) (backupasset.RcloneVersioningPreflightResult, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningPreflight, request.TaskID, request.RequestedMode, "", "", err)
		return backupasset.RcloneVersioningPreflightResult{}, err
	}
	result, err := service.CreateRcloneVersioningPreflight(ctx, request)
	service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningPreflight, request.TaskID, request.RequestedMode, result.Summary.State, result.Summary.ReasonCode, err)
	return result, err
}

func validateRcloneSettlingEvidence(evidence RcloneVersioningPreflightEvidence, now time.Time) error {
	if !evidence.Settling || evidence.Mode != backupasset.PublicationNativeObjectVersions ||
		!isUTCTime(evidence.SettlingObservedAt) || evidence.SettlingObservedAt.After(now) ||
		evidence.CredentialExpiresAt == nil || !isUTCTime(*evidence.CredentialExpiresAt) || !evidence.CredentialExpiresAt.After(now) ||
		evidence.CapabilityRevision != 0 || evidence.Native != nil || evidence.ManagedRootIdentityDigest != "" ||
		evidence.RepositoryMarkerDigest != "" || evidence.EvidenceDigest != "" || evidence.EstimatedReadBytes != 0 ||
		evidence.ConsistencyClass != "" || evidence.HashFidelity != "" || evidence.APICostClass != "" ||
		evidence.StorageCostClass != "" || evidence.EgressCostClass != "" {
		return fmt.Errorf("%w: invalid settling Rclone evidence", backupasset.ErrInvalidState)
	}
	switch evidence.EncryptionProfile {
	case backupasset.RcloneEncryptionSSES3:
		if evidence.KMSKeyStatus != backupasset.RcloneKMSNotApplicable || evidence.KMSReadKeyCount != 0 {
			return fmt.Errorf("%w: invalid settling Rclone encryption", backupasset.ErrInvalidState)
		}
	case backupasset.RcloneEncryptionSSEKMS:
		if evidence.KMSKeyStatus != backupasset.RcloneKMSReady {
			return fmt.Errorf("%w: invalid settling Rclone KMS", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: invalid settling Rclone encryption", backupasset.ErrInvalidState)
	}
	return nil
}

func validateRcloneVersioningPreflightEvidence(evidence RcloneVersioningPreflightEvidence) error {
	if evidence.Settling || (evidence.Mode != backupasset.PublicationVersionedPrefix && evidence.Mode != backupasset.PublicationNativeObjectVersions) ||
		evidence.CapabilityRevision == 0 || !isLowerHex64(evidence.ManagedRootIdentityDigest) ||
		!isLowerHex64(evidence.RepositoryMarkerDigest) || !isLowerHex64(evidence.EvidenceDigest) {
		return fmt.Errorf("%w: invalid Rclone preflight evidence", backupasset.ErrInvalidState)
	}
	summary := backupasset.RclonePublicationSummary{
		Mode: evidence.Mode, State: backupasset.RcloneStateReady, ReasonCode: backupasset.RcloneReasonReady,
		TaskRevision: "1", BindingRevision: "1", CapabilityRevision: strconv.FormatUint(evidence.CapabilityRevision, 10),
		ConsistencyClass: evidence.ConsistencyClass, HashFidelity: evidence.HashFidelity,
		EstimatedReadBytes: strconv.FormatUint(evidence.EstimatedReadBytes, 10), APICostClass: evidence.APICostClass,
		StorageCostClass: evidence.StorageCostClass, EgressCostClass: evidence.EgressCostClass,
		CredentialExpiresAt: evidence.CredentialExpiresAt, EncryptionProfile: evidence.EncryptionProfile,
		KMSKeyStatus: evidence.KMSKeyStatus, KMSReadKeyCount: evidence.KMSReadKeyCount,
		RollbackLocatorPresent: true, RollbackCapability: backupasset.RcloneRollbackCleanAvailable,
	}
	if evidence.Mode == backupasset.PublicationVersionedPrefix {
		if evidence.Native != nil || evidence.CredentialExpiresAt != nil {
			return fmt.Errorf("%w: portable Rclone preflight contains native evidence", backupasset.ErrInvalidState)
		}
		summary.EncryptionProfile = backupasset.RcloneEncryptionNone
		summary.KMSKeyStatus = backupasset.RcloneKMSNotApplicable
	} else {
		if evidence.Native == nil || evidence.CredentialExpiresAt == nil || !isUTCTime(*evidence.CredentialExpiresAt) ||
			!isLowerHex64(evidence.Native.VersioningDigest) || !isLowerHex64(evidence.Native.LifecycleDigest) ||
			!validManagedRcloneUTCTime(evidence.Native.CapabilityStableObservedAt) ||
			!isLowerHex64(evidence.Native.BucketEncryptionDigest) ||
			!isLowerHex64(evidence.Native.CanaryEncryptionEvidenceDigest) {
			return fmt.Errorf("%w: incomplete native Rclone preflight evidence", backupasset.ErrInvalidState)
		}
		switch evidence.EncryptionProfile {
		case backupasset.RcloneEncryptionSSES3:
			if evidence.Native.ActiveKMSKeyDigest != "" || evidence.Native.KMSCapabilityRevision != 0 || len(evidence.Native.RetainedReadKeys) != 0 {
				return fmt.Errorf("%w: SSE-S3 Rclone preflight contains KMS evidence", backupasset.ErrInvalidState)
			}
		case backupasset.RcloneEncryptionSSEKMS:
			if !isLowerHex64(evidence.Native.ActiveKMSKeyDigest) || evidence.Native.KMSCapabilityRevision == 0 {
				return fmt.Errorf("%w: SSE-KMS Rclone preflight lacks key evidence", backupasset.ErrInvalidState)
			}
		default:
			return fmt.Errorf("%w: invalid native Rclone encryption evidence", backupasset.ErrInvalidState)
		}
	}
	return summary.Validate()
}

func isUTCTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Equal(value.UTC())
}

func (service *Service) ActivateRcloneVersioning(ctx context.Context, request backupasset.RcloneVersioningActivationRequest) (backupasset.RcloneVersioningActivationResult, error) {
	if err := request.Validate(); err != nil {
		return backupasset.RcloneVersioningActivationResult{}, err
	}
	if err := service.ensureEnabled(""); err != nil {
		return backupasset.RcloneVersioningActivationResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := service.rcloneSetupStore(); err != nil {
		return backupasset.RcloneVersioningActivationResult{}, err
	}
	if service.rclonePreflights == nil || service.db == nil || service.history == nil {
		return backupasset.RcloneVersioningActivationResult{}, fmt.Errorf("%w: Rclone activation dependencies are unavailable", backupasset.ErrInvalidState)
	}
	transitioner, ok := service.admission.(publication.FeatureTransitioner)
	if !ok || transitioner == nil {
		return backupasset.RcloneVersioningActivationResult{}, fmt.Errorf("%w: Rclone activation admission transition is unavailable", backupasset.ErrInvalidState)
	}
	record, err := service.reserveRcloneActivation(request)
	if err != nil {
		return backupasset.RcloneVersioningActivationResult{}, err
	}
	committed := false
	defer func() { service.releaseRcloneActivation(request.TaskID, record.candidate.setupID, committed) }()
	if request.MigrationChoice != backupasset.RcloneFirstNewPoint && request.MigrationChoice != backupasset.RcloneImportedBaseline {
		return backupasset.RcloneVersioningActivationResult{}, fmt.Errorf("%w: unsupported Rclone migration choice", backupasset.ErrCapabilityUnavailable)
	}
	if record.candidate.purpose == backupasset.RclonePreflightPurposeRenewal {
		if request.MigrationChoice != backupasset.RcloneFirstNewPoint {
			return backupasset.RcloneVersioningActivationResult{}, fmt.Errorf("%w: Rclone renewal does not import a baseline", backupasset.ErrConflict)
		}
		if err := transitioner.TransitionFeature(ctx, true, func() error {
			current, err := service.foundation.PublicationConfig()
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current, record.publicationConfig) {
				return fmt.Errorf("%w: Rclone publication configuration changed", backupasset.ErrConflict)
			}
			return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				return service.renewRcloneBindingTx(ctx, tx, record)
			})
		}); err != nil {
			return backupasset.RcloneVersioningActivationResult{}, err
		}
		committed = true
		summary, err := service.RcloneVersioningSummary(ctx, request.TaskID)
		if err != nil {
			return backupasset.RcloneVersioningActivationResult{}, err
		}
		result := backupasset.RcloneVersioningActivationResult{Summary: summary, MigrationChoice: request.MigrationChoice}
		return result, result.Validate()
	}
	if request.MigrationChoice == backupasset.RcloneImportedBaseline && service.publication == nil {
		return backupasset.RcloneVersioningActivationResult{}, fmt.Errorf("%w: imported Rclone baseline publication is unavailable", backupasset.ErrInvalidState)
	}
	document, encodedBinding, managedIdentity, taskConfig, err := buildManagedRcloneActivation(record)
	if err != nil {
		return backupasset.RcloneVersioningActivationResult{}, err
	}
	var baselinePreparation *rcloneImportedBaselinePreparation
	if request.MigrationChoice == backupasset.RcloneImportedBaseline {
		prepared, err := service.prepareImportedRcloneBaseline(ctx, record, document)
		if err != nil {
			return backupasset.RcloneVersioningActivationResult{}, err
		}
		baselinePreparation = &prepared
	}
	var activation rcloneVersioningActivationState
	if err := transitioner.TransitionFeature(ctx, true, func() error {
		currentConfig, configErr := service.foundation.PublicationConfig()
		if configErr != nil {
			return configErr
		}
		if !reflect.DeepEqual(record.publicationConfig, currentConfig) {
			return fmt.Errorf("%w: Rclone publication configuration changed", backupasset.ErrConflict)
		}
		state, err := service.activateRcloneVersioning(
			ctx, request, record, document, encodedBinding, managedIdentity, taskConfig, baselinePreparation,
		)
		if err != nil {
			return err
		}
		activation = state
		return nil
	}); err != nil {
		return backupasset.RcloneVersioningActivationResult{}, err
	}
	committed = true
	if activation.importedBaseline != nil {
		if _, err := service.publication.PublishImportedRcloneBaseline(ctx, *activation.importedBaseline); err != nil {
			return backupasset.RcloneVersioningActivationResult{}, fmt.Errorf("publish imported Rclone baseline: %w", err)
		}
	}
	summary, err := service.rcloneActivatedSummary(ctx, request.TaskID, record)
	if err != nil {
		return backupasset.RcloneVersioningActivationResult{}, err
	}
	result := backupasset.RcloneVersioningActivationResult{Summary: summary, MigrationChoice: request.MigrationChoice}
	if err := result.Validate(); err != nil {
		return backupasset.RcloneVersioningActivationResult{}, err
	}
	return result, nil
}

func (service *Service) ActivateRcloneVersioningForRequest(ctx context.Context, request backupasset.RcloneVersioningActivationRequest, requestContext RequestContext) (backupasset.RcloneVersioningActivationResult, error) {
	mode := backupasset.TaskPublicationMode("")
	if service != nil && service.rclonePreflights != nil {
		if record, ok := service.rclonePreflights.get(request.PreflightID); ok {
			mode = record.evidence.Mode
		}
	}
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningActivate, request.TaskID, mode, "", "", err)
		return backupasset.RcloneVersioningActivationResult{}, err
	}
	result, err := service.ActivateRcloneVersioning(ctx, request)
	if err == nil {
		mode = result.Summary.Mode
	}
	service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningActivate, request.TaskID, mode, result.Summary.State, result.Summary.ReasonCode, err)
	return result, err
}

func buildManagedRcloneActivation(record rcloneVersioningPreflightRecord) (managedRcloneBindingDocumentV3, string, string, string, error) {
	candidate := record.candidate
	if !isUTCTime(record.expiresAt) || validateRcloneVersioningPreflightEvidence(record.evidence) != nil {
		return managedRcloneBindingDocumentV3{}, "", "", "", fmt.Errorf("%w: invalid Rclone activation evidence", backupasset.ErrInvalidState)
	}
	legacyBinding, err := encodeBindingDocument(candidate.plan.legacyBinding)
	if err != nil {
		return managedRcloneBindingDocumentV3{}, "", "", "", err
	}
	salt, err := hexDecodeSalt(candidate.plan.legacyBinding.IdentitySalt)
	if err != nil {
		return managedRcloneBindingDocumentV3{}, "", "", "", err
	}
	legacyPolicy := candidate.plan.task.ExecutorConfig
	if strings.TrimSpace(legacyPolicy) == "" {
		legacyPolicy = `{}`
	}
	document := managedRcloneBindingDocumentV3{
		Version: managedRcloneBindingDocumentVersion, Provider: backupasset.ProviderRclone,
		IdentityClass: provider.IdentityXirangManagedRepository, TaskID: candidate.plan.task.ID, NodeID: candidate.plan.task.NodeID,
		RepositoryID: candidate.plan.repository.ID, TaskRepositoryLinkID: candidate.plan.link.ID,
		LayoutRevision: managedRcloneLayoutRevisionV1, MinimumRuntimeRevision: managedRcloneMinimumRuntimeRevisionV1,
		PublicationMode: record.evidence.Mode, BindingRevision: candidate.bindingRevision,
		ConfigRevision: candidate.configRevision, CapabilityRevision: record.evidence.CapabilityRevision,
		CredentialRevision: candidate.credentialRevision, PreflightID: record.id, PreflightRevision: 1,
		PreflightDigest: record.evidence.EvidenceDigest, PreflightExpiresAt: record.expiresAt,
		ManagedRootIdentityDigest: record.evidence.ManagedRootIdentityDigest,
		RepositoryMarkerDigest:    record.evidence.RepositoryMarkerDigest,
		LegacyLocatorDigest:       managedRcloneBindingDigest(salt, "legacy-locator", candidate.plan.legacyBinding.Locator),
		LegacyBindingV1:           legacyBinding, LegacyBindingDigest: managedRcloneBindingDigest(salt, "legacy-binding", legacyBinding),
		LegacyTaskPolicy: legacyPolicy, LegacyTaskPolicyDigest: managedRcloneBindingDigest(salt, "legacy-task-policy", legacyPolicy),
		IdentitySalt: candidate.plan.legacyBinding.IdentitySalt,
	}
	switch candidate.kind {
	case rcloneBindingSetupPortable:
		document.Portable = &managedRclonePortableBindingV3{
			ManagedRootLocator: candidate.portable.managedRootLocator, TargetRemote: candidate.portable.targetRemote,
			BoundConfig: string(candidate.portable.boundConfig.ExactBytes()), ConfigDigest: candidate.portable.boundConfig.KeyedDigest(),
			Backend: candidate.portable.boundConfig.Backend(), DependencyRemotes: candidate.portable.boundConfig.DependencyRemotes(),
			ClassificationRevision: candidate.portable.boundConfig.ClassificationRevision(),
		}
	case rcloneBindingSetupNative:
		request := candidate.native.request
		bootstrap := &managedRcloneNativeBootstrapV3{}
		switch request.Bootstrap.Mode {
		case backupasset.RcloneBootstrapWorkloadChain:
			bootstrap.Mode = managedRcloneBootstrapWorkloadChain
			bootstrap.Workload = &managedRcloneWorkloadBootstrapV3{}
		case backupasset.RcloneBootstrapStaticSTS:
			bootstrap.Mode = managedRcloneBootstrapStaticSTS
			bootstrap.Static = &managedRcloneStaticSTSBootstrapV3{AccessKeyID: request.Bootstrap.AccessKeyID, SecretAccessKey: request.Bootstrap.SecretAccessKey}
		default:
			return managedRcloneBindingDocumentV3{}, "", "", "", fmt.Errorf("%w: invalid Rclone bootstrap", backupasset.ErrInvalidState)
		}
		native := record.evidence.Native
		document.Native = &managedRcloneNativeBindingV3{
			ProfileCode: provider.RcloneNativeAWSS3GeneralPurposeV1, Region: request.Region, Bucket: request.Bucket,
			ManagedPrefix: request.ManagedPrefix, RegionIdentityDigest: managedRcloneBindingDigest(salt, "region", request.Region),
			BucketIdentityDigest:        managedRcloneBindingDigest(salt, "bucket", request.Bucket),
			ManagedPrefixIdentityDigest: managedRcloneBindingDigest(salt, "managed-prefix", request.ManagedPrefix),
			RoleARN:                     request.RoleARN, ExternalID: candidate.native.externalID, Bootstrap: bootstrap,
			VersioningDigest: native.VersioningDigest, LifecycleDigest: native.LifecycleDigest,
			CapabilityStableObservedAt: native.CapabilityStableObservedAt,
			BucketEncryptionDigest:     native.BucketEncryptionDigest, BucketKeyEnabled: native.BucketKeyEnabled,
			CanaryEncryptionEvidenceDigest: native.CanaryEncryptionEvidenceDigest,
			ActiveKMSKeyDigest:             native.ActiveKMSKeyDigest, KMSCapabilityRevision: native.KMSCapabilityRevision,
			RetainedReadKeys: append([]managedRcloneKMSReadKeyV3(nil), native.RetainedReadKeys...),
		}
		switch request.EncryptionProfile {
		case backupasset.RcloneEncryptionSSES3:
			document.Native.EncryptionProfile = provider.RcloneNativeSSES3V1
		case backupasset.RcloneEncryptionSSEKMS:
			document.Native.EncryptionProfile = provider.RcloneNativeSSEKMSV1
			document.Native.ActiveKMSKeyARN = request.KMSKeyARN
		default:
			return managedRcloneBindingDocumentV3{}, "", "", "", fmt.Errorf("%w: invalid Rclone encryption profile", backupasset.ErrInvalidState)
		}
	default:
		return managedRcloneBindingDocumentV3{}, "", "", "", fmt.Errorf("%w: invalid Rclone binding candidate", backupasset.ErrInvalidState)
	}
	encodedBinding, err := encodeManagedRcloneBindingDocumentV3(document)
	if err != nil {
		return managedRcloneBindingDocumentV3{}, "", "", "", err
	}
	managedIdentity, err := managedRcloneRepositoryIdentity(document)
	if err != nil {
		return managedRcloneBindingDocumentV3{}, "", "", "", err
	}
	taskConfig, err := encodeManagedRcloneTaskConfig(legacyPolicy, record.evidence.Mode)
	if err != nil {
		return managedRcloneBindingDocumentV3{}, "", "", "", err
	}
	return document, encodedBinding, managedIdentity, taskConfig, nil
}

func encodeManagedRcloneTaskConfig(legacyPolicy string, mode backupasset.TaskPublicationMode) (string, error) {
	type taskConfigWire struct {
		Version         *int                             `json:"version"`
		PublicationMode *backupasset.TaskPublicationMode `json:"publication_mode"`
		BandwidthLimit  string                           `json:"bandwidth_limit,omitempty"`
		Transfers       int                              `json:"transfers,omitempty"`
	}
	if strings.TrimSpace(legacyPolicy) == "" {
		legacyPolicy = `{}`
	}
	if err := rejectDuplicateOrNullJSONMembers(legacyPolicy); err != nil {
		return "", fmt.Errorf("%w: invalid legacy Rclone Task policy", backupasset.ErrInvalidState)
	}
	decoder := json.NewDecoder(strings.NewReader(legacyPolicy))
	decoder.DisallowUnknownFields()
	var wire taskConfigWire
	if err := decoder.Decode(&wire); err != nil {
		return "", fmt.Errorf("%w: invalid legacy Rclone Task policy", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%w: trailing legacy Rclone Task policy", backupasset.ErrInvalidState)
	}
	if wire.Version != nil && *wire.Version != backupasset.RcloneTaskConfigSchemaVersion {
		return "", fmt.Errorf("%w: unsupported legacy Rclone Task policy", backupasset.ErrInvalidState)
	}
	if wire.PublicationMode != nil && *wire.PublicationMode != backupasset.PublicationLegacyMutable {
		return "", fmt.Errorf("%w: legacy Rclone Task is already managed", backupasset.ErrConflict)
	}
	if len(wire.BandwidthLimit) > 256 || wire.Transfers < 0 || wire.Transfers > 256 {
		return "", fmt.Errorf("%w: invalid legacy Rclone Task limits", backupasset.ErrInvalidState)
	}
	if mode != backupasset.PublicationVersionedPrefix && mode != backupasset.PublicationNativeObjectVersions {
		return "", fmt.Errorf("%w: unsupported managed Rclone Task mode", backupasset.ErrInvalidState)
	}
	result := struct {
		Version         int                             `json:"version"`
		PublicationMode backupasset.TaskPublicationMode `json:"publication_mode"`
		BandwidthLimit  string                          `json:"bandwidth_limit,omitempty"`
		Transfers       int                             `json:"transfers,omitempty"`
	}{Version: backupasset.RcloneTaskConfigSchemaVersion, PublicationMode: mode, BandwidthLimit: wire.BandwidthLimit, Transfers: wire.Transfers}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("encode managed Rclone Task config: %w", err)
	}
	return string(encoded), nil
}

func (service *Service) prepareImportedRcloneBaseline(
	ctx context.Context,
	record rcloneVersioningPreflightRecord,
	document managedRcloneBindingDocumentV3,
) (rcloneImportedBaselinePreparation, error) {
	if service == nil || service.publication == nil || service.publication.foundation == nil || service.publication.lease == nil ||
		validateRcloneVersioningPreflightEvidence(record.evidence) != nil || !record.expiresAt.After(service.utcNow()) {
		return rcloneImportedBaselinePreparation{}, fmt.Errorf("%w: imported Rclone baseline dependencies are unavailable", backupasset.ErrInvalidState)
	}
	leaseConfig, err := service.publication.foundation.LeaseConfig()
	if err != nil {
		return rcloneImportedBaselinePreparation{}, err
	}
	publicationConfig, err := service.publication.foundation.PublicationConfig()
	if err != nil {
		return rcloneImportedBaselinePreparation{}, err
	}
	markerKey, err := service.publication.rcloneMarkerKey(ctx, document.RepositoryID)
	if err != nil {
		return rcloneImportedBaselinePreparation{}, err
	}
	preparedAt := service.utcNow()
	preparation := rcloneImportedBaselinePreparation{
		preparedAt: preparedAt, markerKey: append([]byte(nil), markerKey...),
		leaseConfig: leaseConfig, publicationConfig: publicationConfig,
	}
	if document.PublicationMode == backupasset.PublicationNativeObjectVersions {
		profile, err := managedRcloneNativeProfile(document)
		if err != nil {
			return rcloneImportedBaselinePreparation{}, err
		}
		source, err := provider.ResolveRcloneNativeBaselineSource(record.candidate.plan.legacyBinding.Locator, profile)
		if err != nil {
			return rcloneImportedBaselinePreparation{}, err
		}
		if publicationConfig.Rclone.FullVerifyMaxBytes <= 0 {
			return rcloneImportedBaselinePreparation{}, fmt.Errorf("%w: native Rclone baseline read budget is invalid", backupasset.ErrInvalidState)
		}
		deadline := managedRclonePointDeadline(preparedAt, document, leaseConfig, publicationConfig)
		nativeInput, err := service.publication.prepareRcloneNativeProcessInput(
			ctx, document, markerKey, leaseConfig, publicationConfig, preparedAt, deadline, true,
			&managedRcloneNativeBaselineRequest{source: source, maxReadBytes: uint64(publicationConfig.Rclone.FullVerifyMaxBytes)},
		)
		if err != nil {
			return rcloneImportedBaselinePreparation{}, err
		}
		preparation.nativeInput = nativeInput
	}
	return preparation, nil
}

func (service *Service) activateRcloneVersioning(
	ctx context.Context,
	request backupasset.RcloneVersioningActivationRequest,
	record rcloneVersioningPreflightRecord,
	document managedRcloneBindingDocumentV3,
	encodedBinding, managedIdentity, taskConfig string,
	baseline *rcloneImportedBaselinePreparation,
) (rcloneVersioningActivationState, error) {
	var activation rcloneVersioningActivationState
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockRcloneWorkflowPlanTx(ctx, tx, record.candidate.plan, true)
		if err != nil {
			return err
		}
		if !record.expiresAt.After(service.utcNow()) || !record.candidate.expiresAt.After(service.utcNow()) {
			return fmt.Errorf("%w: Rclone preflight expired during activation", backupasset.ErrConflict)
		}
		taskEntity, link, repository, binding := locked.task, locked.link, locked.repository, locked.binding
		if link.PublicationMode != string(backupasset.PublicationLegacyMutable) {
			return fmt.Errorf("%w: Rclone legacy link changed", backupasset.ErrConflict)
		}
		legacyLocator := link.EncryptedLegacyLocator
		var mutablePoints []model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("repository_id = ? AND semantics = ?", repository.ID, backupasset.PointMutableHead).Find(&mutablePoints).Error; err != nil {
			return fmt.Errorf("load Rclone mutable-head observations: %w", err)
		}
		for index := range mutablePoints {
			if mutablePoints[index].State != string(backupasset.RecoveryPointObserved) {
				return fmt.Errorf("%w: Rclone mutable-head point cannot be retired", backupasset.ErrConflict)
			}
			if err := tx.Delete(&mutablePoints[index]).Error; err != nil {
				return fmt.Errorf("remove synthetic Rclone mutable-head observation: %w", err)
			}
		}
		versionMode, _, _, err := backupasset.MapPublicationMode(backupasset.ProviderRclone, record.evidence.Mode)
		if err != nil {
			return err
		}
		immutability := backupasset.ImmutabilityXirangManaged
		if record.evidence.Mode == backupasset.PublicationNativeObjectVersions {
			immutability = backupasset.ImmutabilityBackendVersioned
		}
		now := service.utcNow()
		repository.VersionMode = string(versionMode)
		repository.ImmutabilityLevel = string(immutability)
		repository.RepositoryIdentity = &managedIdentity
		repository.CapabilityRevision = int(record.evidence.CapabilityRevision)
		repository.UpdatedAt = now
		if err := tx.Save(&repository).Error; err != nil {
			return fmt.Errorf("activate Rclone repository: %w", err)
		}
		link.PublicationMode = string(record.evidence.Mode)
		link.UpdatedAt = now
		if err := tx.Save(&link).Error; err != nil {
			return fmt.Errorf("activate Rclone link: %w", err)
		}
		salt, err := hexDecodeSalt(document.IdentitySalt)
		if err != nil {
			return err
		}
		binding.BindingKind = "managed_rclone_v3"
		binding.EncryptedConfig = encodedBinding
		binding.ConfigFingerprint = managedRcloneBindingDigest(salt, "active-binding", encodedBinding)
		binding.UpdatedAt = now
		if err := tx.Save(&binding).Error; err != nil {
			return fmt.Errorf("activate Rclone binding: %w", err)
		}
		taskEntity.ExecutorConfig = taskConfig
		taskEntity.RsyncTarget = ""
		taskEntity.Enabled = false
		taskEntity.SkipNext = false
		taskEntity.NextRunAt = nil
		if err := tx.Save(&taskEntity).Error; err != nil {
			return fmt.Errorf("pause activated Rclone Task: %w", err)
		}
		if baseline != nil {
			if request.MigrationChoice != backupasset.RcloneImportedBaseline || service.publication == nil {
				return fmt.Errorf("%w: imported Rclone baseline activation is invalid", backupasset.ErrInvalidState)
			}
			startedAt := baseline.preparedAt.UTC()
			migrationRun := model.TaskRun{
				TaskID: taskEntity.ID, TriggerType: "migration", Status: "running", StartedAt: &startedAt,
				CreatedAt: startedAt, UpdatedAt: startedAt,
			}
			if err := tx.Create(&migrationRun).Error; err != nil {
				return fmt.Errorf("create imported Rclone baseline TaskRun: %w", err)
			}
			run := publication.Run{
				Task: model.Task{ID: taskEntity.ID}, TaskRunID: migrationRun.ID, Trigger: "migration",
				StartedAt: startedAt, ImportedBaseline: true,
			}
			runtime := managedRclonePublicationRuntime{
				repository: repository, task: taskEntity, link: link, binding: document,
			}
			attempt, childLease, err := service.publication.prepareRclonePointTx(
				ctx, tx, run, runtime, baseline.markerKey, baseline.leaseConfig,
				baseline.publicationConfig, baseline.preparedAt, baseline.nativeInput,
			)
			if err != nil {
				return fmt.Errorf("reserve imported Rclone baseline: %w", err)
			}
			activation.importedBaseline = &rcloneImportedBaselineActivation{
				taskID: taskEntity.ID, taskRunID: migrationRun.ID, startedAt: startedAt,
				legacyLocator: legacyLocator, preflightID: record.id,
				attempt: attempt, childLease: childLease, binding: document,
				markerKey: append([]byte(nil), baseline.markerKey...), leaseConfig: baseline.leaseConfig,
				nativeInput: baseline.nativeInput,
			}
		}
		currentConfig, configErr := service.foundation.PublicationConfigTx(ctx, tx)
		if configErr != nil {
			return configErr
		}
		if !reflect.DeepEqual(currentConfig, record.publicationConfig) {
			return fmt.Errorf("%w: Rclone publication configuration changed", backupasset.ErrConflict)
		}
		if !record.expiresAt.After(service.utcNow()) || !record.candidate.expiresAt.After(service.utcNow()) {
			return fmt.Errorf("%w: Rclone preflight expired during activation", backupasset.ErrConflict)
		}
		return nil
	})
	if err != nil {
		return rcloneVersioningActivationState{}, err
	}
	return activation, nil
}

func (service *Service) rcloneActivatedSummary(ctx context.Context, taskID uint, record rcloneVersioningPreflightRecord) (backupasset.RclonePublicationSummary, error) {
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).First(&taskEntity, taskID).Error; err != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("load activated Rclone Task: %w", err)
	}
	revision, err := managedRsyncTaskRevision(taskEntity)
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	summary := rclonePreflightSummary(record.candidate, record.evidence)
	summary.TaskRevision = strconv.FormatUint(revision, 10)
	summary.RollbackCapability, err = service.rcloneManagedRollbackCapability(ctx, record.candidate.plan.repository.ID)
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	var point model.RecoveryPoint
	result := service.db.WithContext(ctx).
		Where("repository_id = ? AND producing_task_id = ? AND semantics IN ?", record.candidate.plan.repository.ID, taskID, []string{
			string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline),
		}).
		Order("created_at DESC").Limit(1).Find(&point)
	if result.Error != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("load activated Rclone point state: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		summary.RollbackCapability = backupasset.RcloneRollbackPreparationOnly
		switch backupasset.RecoveryPointState(point.State) {
		case backupasset.RecoveryPointPreparing:
			summary.State = backupasset.RcloneStatePreparing
		case backupasset.RecoveryPointVerifying:
			summary.State = backupasset.RcloneStateVerifying
		case backupasset.RecoveryPointCommitted:
			summary.State = backupasset.RcloneStateCommitted
		case backupasset.RecoveryPointFailed:
			summary.State = backupasset.RcloneStateFailed
			summary.ReasonCode = backupasset.RcloneReasonOutcomeUnknown
		default:
			summary.State = backupasset.RcloneStateBlocked
			summary.ReasonCode = backupasset.RcloneReasonUnsupportedProfile
		}
	}
	return summary, summary.Validate()
}

type rcloneCleanRollbackPlan struct {
	task       model.Task
	link       model.TaskRepositoryLink
	repository model.BackupRepository
	binding    model.RepositoryAccessBinding
	document   managedRcloneBindingDocumentV3
	legacy     bindingDocument
}

func (service *Service) CleanRollbackRcloneVersioning(ctx context.Context, request backupasset.RcloneVersioningCleanRollbackRequest) (backupasset.RcloneVersioningRollbackResult, error) {
	if err := request.Validate(); err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	if err := service.ensureEnabled(""); err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	if service == nil || service.db == nil || service.history == nil {
		return backupasset.RcloneVersioningRollbackResult{}, fmt.Errorf("%w: Rclone clean rollback dependencies are unavailable", backupasset.ErrInvalidState)
	}
	transitioner, ok := service.admission.(publication.FeatureTransitioner)
	if !ok || transitioner == nil {
		return backupasset.RcloneVersioningRollbackResult{}, fmt.Errorf("%w: Rclone clean rollback admission transition is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	plan, err := service.loadRcloneCleanRollbackPlan(ctx, request)
	if err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	available, err := service.history.rcloneCleanRollbackAvailable(ctx, plan.repository.ID)
	if err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	if !available {
		return backupasset.RcloneVersioningRollbackResult{}, fmt.Errorf("%w: Rclone clean rollback window is closed", backupasset.ErrConflict)
	}
	if err := transitioner.TransitionFeature(ctx, true, func() error {
		return service.restoreRcloneLegacyState(ctx, request, plan)
	}); err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	service.rcloneWorkflowMu.Lock()
	if service.rcloneCandidates != nil && service.rcloneActivations[request.TaskID] == "" {
		if candidate, ok := service.rcloneCandidates.get(request.TaskID); ok && candidate.expectedTaskRevision == request.ExpectedTaskRevision {
			service.rcloneCandidates.delete(request.TaskID)
			if service.rclonePreflights != nil {
				service.rclonePreflights.removeForTask(request.TaskID)
			}
		}
	}
	service.rcloneWorkflowMu.Unlock()
	summary, err := service.rcloneLegacySummary(ctx, request.TaskID)
	if err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	result := backupasset.RcloneVersioningRollbackResult{Summary: summary}
	return result, result.Validate()
}

func (service *Service) CleanRollbackRcloneVersioningForRequest(ctx context.Context, request backupasset.RcloneVersioningCleanRollbackRequest, requestContext RequestContext) (backupasset.RcloneVersioningRollbackResult, error) {
	mode := service.rcloneSummaryMode(ctx, request.TaskID)
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningCleanRollback, request.TaskID, mode, "", "", err)
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	result, err := service.CleanRollbackRcloneVersioning(ctx, request)
	if err == nil {
		mode = result.Summary.Mode
	}
	service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningCleanRollback, request.TaskID, mode, result.Summary.State, result.Summary.ReasonCode, err)
	return result, err
}

func (service *Service) loadRcloneCleanRollbackPlan(ctx context.Context, request backupasset.RcloneVersioningCleanRollbackRequest) (rcloneCleanRollbackPlan, error) {
	var plan rcloneCleanRollbackPlan
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Preload("Node").Where("id = ? AND archived_at IS NULL", request.TaskID).First(&plan.task).Error; err != nil {
			return fmt.Errorf("load Rclone clean rollback Task: %w", err)
		}
		revision, err := managedRsyncTaskRevision(plan.task)
		if err != nil {
			return err
		}
		if revision != request.ExpectedTaskRevision || bindingProviderForTask(plan.task) != backupasset.ProviderRclone || plan.task.Enabled {
			return fmt.Errorf("%w: Rclone clean rollback Task changed or is not paused", backupasset.ErrConflict)
		}
		if err := tx.Where("task_id = ? AND unlinked_at IS NULL", plan.task.ID).First(&plan.link).Error; err != nil {
			return fmt.Errorf("load Rclone clean rollback link: %w", err)
		}
		mode := backupasset.TaskPublicationMode(plan.link.PublicationMode)
		if mode != backupasset.PublicationVersionedPrefix && mode != backupasset.PublicationNativeObjectVersions {
			return fmt.Errorf("%w: Rclone clean rollback requires a managed link", backupasset.ErrConflict)
		}
		if err := tx.First(&plan.repository, "id = ?", plan.link.RepositoryID).Error; err != nil {
			return fmt.Errorf("load Rclone clean rollback repository: %w", err)
		}
		if err := tx.Where("repository_id = ? AND status = ?", plan.repository.ID, bindingStatusActive).First(&plan.binding).Error; err != nil {
			return fmt.Errorf("load Rclone clean rollback binding: %w", err)
		}
		stored, err := decodeStoredBindingDocument(plan.binding.EncryptedConfig)
		if err != nil || stored.ManagedRcloneV3 == nil || stored.V1 != nil || stored.ManagedRsyncV2 != nil {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: Rclone clean rollback binding is unavailable", backupasset.ErrConflict)
		}
		plan.document = *stored.ManagedRcloneV3
		if plan.document.BindingRevision != request.ExpectedBindingRevision ||
			validateManagedRcloneBindingAssociation(plan.document, managedRcloneBindingAssociation{Task: plan.task, Link: plan.link, Repository: plan.repository}) != nil {
			return fmt.Errorf("%w: Rclone clean rollback binding changed", backupasset.ErrConflict)
		}
		legacy, err := decodeBindingDocument(plan.document.LegacyBindingV1)
		if err != nil {
			return err
		}
		plan.legacy = legacy
		return nil
	})
	if err != nil {
		return rcloneCleanRollbackPlan{}, err
	}
	return plan, nil
}

func (service *Service) restoreRcloneLegacyState(ctx context.Context, request backupasset.RcloneVersioningCleanRollbackRequest, plan rcloneCleanRollbackPlan) error {
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND archived_at IS NULL", request.TaskID).First(&taskEntity).Error; err != nil {
			return fmt.Errorf("lock Rclone clean rollback Task: %w", err)
		}
		revision, err := managedRsyncTaskRevision(taskEntity)
		if err != nil {
			return err
		}
		if revision != request.ExpectedTaskRevision || taskEntity.Enabled || taskEntity.RsyncTarget != "" {
			return fmt.Errorf("%w: Rclone clean rollback Task changed", backupasset.ErrConflict)
		}
		activeRuns, err := countAuthoritativeActiveTaskRuns(tx, taskEntity)
		if err != nil {
			return fmt.Errorf("count active Rclone rollback TaskRuns: %w", err)
		}
		if activeRuns != 0 {
			return fmt.Errorf("%w: Rclone clean rollback has active work", backupasset.ErrConflict)
		}
		var link model.TaskRepositoryLink
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND task_id = ? AND unlinked_at IS NULL", plan.link.ID, taskEntity.ID).First(&link).Error; err != nil {
			return fmt.Errorf("lock Rclone clean rollback link: %w", err)
		}
		var repository model.BackupRepository
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", plan.repository.ID).Error; err != nil {
			return fmt.Errorf("lock Rclone clean rollback repository: %w", err)
		}
		var binding model.RepositoryAccessBinding
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
			return fmt.Errorf("lock Rclone clean rollback binding: %w", err)
		}
		stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
		if err != nil || stored.ManagedRcloneV3 == nil || stored.ManagedRcloneV3.BindingRevision != request.ExpectedBindingRevision ||
			link.PublicationMode != string(plan.document.PublicationMode) {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: Rclone clean rollback state changed", backupasset.ErrConflict)
		}
		var blockers int64
		if err := tx.Model(&model.RecoveryPoint{}).Where("repository_id = ? AND semantics IN ?", repository.ID, managedHistoryPointSemantics()).Count(&blockers).Error; err != nil {
			return fmt.Errorf("count Rclone clean rollback points: %w", err)
		}
		if blockers != 0 {
			return fmt.Errorf("%w: Rclone clean rollback has a managed reservation", backupasset.ErrConflict)
		}
		if err := tx.Model(&model.BackupAssetManagedHistoryLatch{}).Where("scope = ? AND repository_id = ?", managedHistoryLatchScopeRepository, repository.ID).Count(&blockers).Error; err != nil {
			return fmt.Errorf("count Rclone clean rollback latches: %w", err)
		}
		if blockers != 0 {
			return fmt.Errorf("%w: Rclone clean rollback has managed history", backupasset.ErrConflict)
		}
		if err := tx.Model(&model.RecoveryPointLease{}).
			Joins("JOIN recovery_points ON recovery_points.id = recovery_point_leases.recovery_point_id").
			Where("recovery_points.repository_id = ? AND recovery_point_leases.holder_type IN ? AND recovery_point_leases.status = ?", repository.ID, managedHistoryLeaseHolderTypes(), backupasset.LeaseActive).
			Count(&blockers).Error; err != nil {
			return fmt.Errorf("count Rclone clean rollback leases: %w", err)
		}
		if blockers != 0 {
			return fmt.Errorf("%w: Rclone clean rollback has active leases", backupasset.ErrConflict)
		}
		salt, err := hexDecodeSalt(plan.legacy.IdentitySalt)
		if err != nil {
			return err
		}
		legacyIdentity, err := provider.DeriveScopedIdentity(salt, provider.ScopedIdentityDocument{
			Provider: backupasset.ProviderRclone, TaskID: plan.legacy.TaskID, NodeID: plan.legacy.NodeID,
			EndpointFacts: append([]string(nil), plan.legacy.EndpointFacts...),
		})
		if err != nil {
			return err
		}
		now := service.utcNow()
		repository.RepositoryIdentity = &legacyIdentity
		repository.VersionMode = string(backupasset.VersionMutableHead)
		repository.ImmutabilityLevel = string(backupasset.ImmutabilityMutable)
		repository.CapabilityRevision++
		repository.UpdatedAt = now
		if err := tx.Save(&repository).Error; err != nil {
			return fmt.Errorf("restore legacy Rclone repository: %w", err)
		}
		link.PublicationMode = string(backupasset.PublicationLegacyMutable)
		link.UpdatedAt = now
		if err := tx.Save(&link).Error; err != nil {
			return fmt.Errorf("restore legacy Rclone link: %w", err)
		}
		binding.BindingKind = "task_derived_v1"
		binding.EncryptedConfig = plan.document.LegacyBindingV1
		binding.ConfigFingerprint, err = provider.DeriveConfigFingerprint(salt, []byte(plan.document.LegacyBindingV1))
		if err != nil {
			return err
		}
		binding.UpdatedAt = now
		if err := tx.Save(&binding).Error; err != nil {
			return fmt.Errorf("restore legacy Rclone binding: %w", err)
		}
		taskEntity.ExecutorConfig = plan.document.LegacyTaskPolicy
		taskEntity.RsyncTarget = plan.legacy.Locator
		taskEntity.Enabled = false
		taskEntity.SkipNext = false
		taskEntity.NextRunAt = nil
		if err := tx.Save(&taskEntity).Error; err != nil {
			return fmt.Errorf("restore paused legacy Rclone Task: %w", err)
		}
		return nil
	})
}

func (service *Service) rcloneLegacySummary(ctx context.Context, taskID uint) (backupasset.RclonePublicationSummary, error) {
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).First(&taskEntity, taskID).Error; err != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("load legacy Rclone Task summary: %w", err)
	}
	revision, err := managedRsyncTaskRevision(taskEntity)
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	summary := backupasset.RclonePublicationSummary{
		Mode: backupasset.PublicationLegacyMutable, State: backupasset.RcloneStateLegacy, ReasonCode: backupasset.RcloneReasonLegacy,
		TaskRevision: strconv.FormatUint(revision, 10), BindingRevision: "0", CapabilityRevision: "0",
		ConsistencyClass: backupasset.RcloneConsistencyNotEvaluated, HashFidelity: backupasset.RcloneHashNotEvaluated,
		EstimatedReadBytes: "0", APICostClass: backupasset.RcloneCostNotEvaluated,
		StorageCostClass: backupasset.RcloneCostNotEvaluated, EgressCostClass: backupasset.RcloneCostNotEvaluated,
		EncryptionProfile: backupasset.RcloneEncryptionNone, KMSKeyStatus: backupasset.RcloneKMSNotApplicable,
		RollbackLocatorPresent: false, RollbackCapability: backupasset.RcloneRollbackPreparationOnly,
	}
	return summary, summary.Validate()
}

func (service *Service) PrepareRcloneVersioningRollback(ctx context.Context, request backupasset.RcloneVersioningRollbackPreparationRequest) (backupasset.RcloneVersioningRollbackResult, error) {
	if err := request.Validate(); err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	if err := service.ensureEnabled(""); err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	if service == nil || service.db == nil {
		return backupasset.RcloneVersioningRollbackResult{}, fmt.Errorf("%w: Rclone rollback preparation dependencies are unavailable", backupasset.ErrInvalidState)
	}
	transitioner, ok := service.admission.(publication.FeatureTransitioner)
	if !ok || transitioner == nil {
		return backupasset.RcloneVersioningRollbackResult{}, fmt.Errorf("%w: Rclone rollback preparation admission transition is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cleanRequest := backupasset.RcloneVersioningCleanRollbackRequest(request)
	plan, err := service.loadRcloneCleanRollbackPlan(ctx, cleanRequest)
	if err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	if err := service.reconcileRcloneRollbackPoints(ctx, plan.repository.ID); err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	if err := transitioner.TransitionFeature(ctx, true, func() error {
		return service.prepareRcloneRollbackAfterDrain(ctx, request, plan)
	}); err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	summary, err := service.rclonePreparedRollbackSummary(ctx, request.TaskID)
	if err != nil {
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	result := backupasset.RcloneVersioningRollbackResult{Summary: summary}
	return result, result.Validate()
}

func (service *Service) PrepareRcloneVersioningRollbackForRequest(ctx context.Context, request backupasset.RcloneVersioningRollbackPreparationRequest, requestContext RequestContext) (backupasset.RcloneVersioningRollbackResult, error) {
	mode := service.rcloneSummaryMode(ctx, request.TaskID)
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningRollbackPreparation, request.TaskID, mode, "", "", err)
		return backupasset.RcloneVersioningRollbackResult{}, err
	}
	result, err := service.PrepareRcloneVersioningRollback(ctx, request)
	if err == nil {
		mode = result.Summary.Mode
	}
	service.writeRcloneVersioningAudit(ctx, requestContext, backupasset.AuditActionRcloneVersioningRollbackPreparation, request.TaskID, mode, result.Summary.State, result.Summary.ReasonCode, err)
	return result, err
}

func (service *Service) reconcileRcloneRollbackPoints(ctx context.Context, repositoryID string) error {
	var points []model.RecoveryPoint
	if err := service.db.WithContext(ctx).
		Where("repository_id = ? AND semantics IN ? AND state IN ?", repositoryID, managedHistoryPointSemantics(), []string{
			string(backupasset.RecoveryPointPreparing), string(backupasset.RecoveryPointVerifying),
		}).Find(&points).Error; err != nil {
		return fmt.Errorf("load unresolved Rclone rollback points: %w", err)
	}
	if len(points) == 0 {
		return nil
	}
	if service.publication == nil {
		return fmt.Errorf("%w: unresolved Rclone rollback points require reconciliation", backupasset.ErrConflict)
	}
	for _, point := range points {
		if _, err := service.publication.ProcessPoint(ctx, point.ID); err != nil {
			return fmt.Errorf("%w: Rclone rollback reconciliation did not converge", backupasset.ErrConflict)
		}
	}
	var remaining int64
	if err := service.db.WithContext(ctx).Model(&model.RecoveryPoint{}).
		Where("repository_id = ? AND semantics IN ? AND state IN ?", repositoryID, managedHistoryPointSemantics(), []string{
			string(backupasset.RecoveryPointPreparing), string(backupasset.RecoveryPointVerifying),
		}).Count(&remaining).Error; err != nil {
		return fmt.Errorf("recheck unresolved Rclone rollback points: %w", err)
	}
	if remaining != 0 {
		return fmt.Errorf("%w: unresolved Rclone rollback evidence remains", backupasset.ErrConflict)
	}
	return nil
}

func (service *Service) prepareRcloneRollbackAfterDrain(ctx context.Context, request backupasset.RcloneVersioningRollbackPreparationRequest, plan rcloneCleanRollbackPlan) error {
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND archived_at IS NULL", request.TaskID).First(&taskEntity).Error; err != nil {
			return fmt.Errorf("lock Rclone rollback preparation Task: %w", err)
		}
		revision, err := managedRsyncTaskRevision(taskEntity)
		if err != nil {
			return err
		}
		if revision != request.ExpectedTaskRevision || taskEntity.Enabled || taskEntity.RsyncTarget != "" {
			return fmt.Errorf("%w: Rclone rollback preparation Task changed", backupasset.ErrConflict)
		}
		activeRuns, err := countAuthoritativeActiveTaskRuns(tx, taskEntity)
		if err != nil {
			return fmt.Errorf("count active Rclone rollback preparation runs: %w", err)
		}
		if activeRuns != 0 {
			return fmt.Errorf("%w: Rclone rollback preparation has active work", backupasset.ErrConflict)
		}
		var link model.TaskRepositoryLink
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND task_id = ? AND unlinked_at IS NULL", plan.link.ID, taskEntity.ID).First(&link).Error; err != nil {
			return fmt.Errorf("lock Rclone rollback preparation link: %w", err)
		}
		var repository model.BackupRepository
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", plan.repository.ID).Error; err != nil {
			return fmt.Errorf("lock Rclone rollback preparation repository: %w", err)
		}
		var binding model.RepositoryAccessBinding
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
			return fmt.Errorf("lock Rclone rollback preparation binding: %w", err)
		}
		stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
		if err != nil || stored.ManagedRcloneV3 == nil || stored.ManagedRcloneV3.BindingRevision != request.ExpectedBindingRevision ||
			stored.ManagedRcloneV3.RollbackPrepared || link.PublicationMode != string(plan.document.PublicationMode) {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: Rclone rollback preparation state changed", backupasset.ErrConflict)
		}
		document := *stored.ManagedRcloneV3
		document.RollbackPrepared = true
		encoded, err := encodeManagedRcloneBindingDocumentV3(document)
		if err != nil {
			return err
		}
		salt, err := hexDecodeSalt(document.IdentitySalt)
		if err != nil {
			return err
		}
		now := service.utcNow()
		binding.EncryptedConfig = encoded
		binding.ConfigFingerprint = managedRcloneBindingDigest(salt, "active-binding", encoded)
		binding.UpdatedAt = now
		if err := tx.Save(&binding).Error; err != nil {
			return fmt.Errorf("prepare Rclone rollback binding: %w", err)
		}
		taskEntity.RsyncTarget = plan.legacy.Locator
		taskEntity.Enabled = false
		taskEntity.SkipNext = false
		taskEntity.NextRunAt = nil
		if err := tx.Save(&taskEntity).Error; err != nil {
			return fmt.Errorf("prepare paused Rclone rollback Task: %w", err)
		}
		return nil
	})
}

func (service *Service) rclonePreparedRollbackSummary(ctx context.Context, taskID uint) (backupasset.RclonePublicationSummary, error) {
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).First(&taskEntity, taskID).Error; err != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("load rollback-prepared Rclone Task: %w", err)
	}
	revision, err := managedRsyncTaskRevision(taskEntity)
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	var link model.TaskRepositoryLink
	if err := service.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskID).First(&link).Error; err != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("load rollback-prepared Rclone link: %w", err)
	}
	var binding model.RepositoryAccessBinding
	if err := service.db.WithContext(ctx).Where("repository_id = ? AND status = ?", link.RepositoryID, bindingStatusActive).First(&binding).Error; err != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("load rollback-prepared Rclone binding: %w", err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil || stored.ManagedRcloneV3 == nil || !stored.ManagedRcloneV3.RollbackPrepared {
		if err != nil {
			return backupasset.RclonePublicationSummary{}, err
		}
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: Rclone rollback preparation is unavailable", backupasset.ErrConflict)
	}
	document := stored.ManagedRcloneV3
	encryption := backupasset.RcloneEncryptionNone
	kmsStatus := backupasset.RcloneKMSNotApplicable
	readKeys := uint32(0)
	if document.Native != nil {
		if document.Native.EncryptionProfile == provider.RcloneNativeSSES3V1 {
			encryption = backupasset.RcloneEncryptionSSES3
		} else {
			encryption = backupasset.RcloneEncryptionSSEKMS
			kmsStatus = backupasset.RcloneKMSReady
			readKeys = uint32(len(document.Native.RetainedReadKeys))
		}
	}
	summary := backupasset.RclonePublicationSummary{
		Mode: document.PublicationMode, State: backupasset.RcloneStateRollbackPrepared, ReasonCode: backupasset.RcloneReasonRollbackPrepared,
		TaskRevision: strconv.FormatUint(revision, 10), BindingRevision: strconv.FormatUint(document.BindingRevision, 10),
		CapabilityRevision: strconv.FormatUint(document.CapabilityRevision, 10),
		ConsistencyClass:   backupasset.RcloneConsistencyNotEvaluated, HashFidelity: backupasset.RcloneHashNotEvaluated,
		EstimatedReadBytes: "0", APICostClass: backupasset.RcloneCostNotEvaluated,
		StorageCostClass: backupasset.RcloneCostNotEvaluated, EgressCostClass: backupasset.RcloneCostNotEvaluated,
		EncryptionProfile: encryption, KMSKeyStatus: kmsStatus, KMSReadKeyCount: readKeys,
		RollbackLocatorPresent: true, RollbackCapability: backupasset.RcloneRollbackPrepared,
	}
	return summary, summary.Validate()
}

func rclonePreflightSummary(candidate rcloneBindingCandidate, evidence RcloneVersioningPreflightEvidence) backupasset.RclonePublicationSummary {
	summary := rcloneBindingCandidateSummary(candidate)
	if evidence.Settling {
		summary.State = backupasset.RcloneStateCapabilitySettling
		summary.ReasonCode = backupasset.RcloneReasonCapabilitySettling
		summary.CredentialExpiresAt = evidence.CredentialExpiresAt
		summary.KMSKeyStatus = evidence.KMSKeyStatus
		summary.KMSReadKeyCount = evidence.KMSReadKeyCount
		return summary
	}
	summary.State = backupasset.RcloneStateReady
	summary.ReasonCode = backupasset.RcloneReasonReady
	summary.CapabilityRevision = strconv.FormatUint(evidence.CapabilityRevision, 10)
	summary.ConsistencyClass = evidence.ConsistencyClass
	summary.HashFidelity = evidence.HashFidelity
	summary.EstimatedReadBytes = strconv.FormatUint(evidence.EstimatedReadBytes, 10)
	summary.APICostClass = evidence.APICostClass
	summary.StorageCostClass = evidence.StorageCostClass
	summary.EgressCostClass = evidence.EgressCostClass
	summary.CredentialExpiresAt = evidence.CredentialExpiresAt
	summary.KMSReadKeyCount = evidence.KMSReadKeyCount
	if candidate.native != nil {
		summary.EncryptionProfile = candidate.native.request.EncryptionProfile
		summary.KMSKeyStatus = evidence.KMSKeyStatus
	}
	return summary
}

// Managed rollback availability is a durable repository fact, independent of
// whether a binding is expired or a renewal preflight is in progress.
func (service *Service) rcloneManagedRollbackCapability(ctx context.Context, repositoryID string) (backupasset.RcloneRollbackCapability, error) {
	available, err := service.history.rcloneCleanRollbackAvailable(ctx, repositoryID)
	if err != nil {
		return "", err
	}
	if available {
		return backupasset.RcloneRollbackCleanAvailable, nil
	}
	return backupasset.RcloneRollbackPreparationOnly, nil
}

// RcloneVersioningSummary projects only closed, task-facing facts. Any stored
// binding or mode drift becomes a deterministic blocked result.
func (service *Service) RcloneVersioningSummary(ctx context.Context, taskID uint) (backupasset.RclonePublicationSummary, error) {
	if service == nil || service.db == nil || taskID == 0 {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: Rclone versioning summary dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).Where("id = ? AND archived_at IS NULL", taskID).First(&taskEntity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: Rclone versioning Task", backupasset.ErrNotFound)
		}
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("load Rclone versioning summary Task: %w", err)
	}
	if bindingProviderForTask(taskEntity) != backupasset.ProviderRclone {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("%w: Rclone versioning summary requires an Rclone Task", backupasset.ErrCapabilityUnavailable)
	}
	taskRevision, err := managedRsyncTaskRevision(taskEntity)
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}

	var link model.TaskRepositoryLink
	result := service.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskID).Limit(1).Find(&link)
	if result.Error != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("load Rclone versioning summary link: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return service.rcloneLegacySummary(ctx, taskID)
	}
	if backupasset.TaskPublicationMode(link.PublicationMode) == backupasset.PublicationLegacyMutable {
		if _, err := service.loadRcloneBindingWorkflowPlan(ctx, taskID, taskRevision, rcloneBindingSetupPortable); err != nil {
			if errors.Is(err, backupasset.ErrConflict) || errors.Is(err, backupasset.ErrNotFound) {
				return blockedRcloneVersioningSummary(taskRevision), nil
			}
			return backupasset.RclonePublicationSummary{}, err
		}
		summary, ok, err := service.rcloneCandidateProgress(ctx, taskID, taskRevision)
		if err != nil {
			return backupasset.RclonePublicationSummary{}, err
		}
		if ok {
			return summary, nil
		}
		return service.rcloneLegacySummary(ctx, taskID)
	}
	mode := backupasset.TaskPublicationMode(link.PublicationMode)
	if mode != backupasset.PublicationVersionedPrefix && mode != backupasset.PublicationNativeObjectVersions {
		return blockedRcloneVersioningSummary(taskRevision), nil
	}
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("load Rclone versioning summary repository: %w", err)
	}
	var access model.RepositoryAccessBinding
	if err := service.db.WithContext(ctx).Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&access).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return blockedRcloneVersioningSummary(taskRevision), nil
		}
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("load Rclone versioning summary binding: %w", err)
	}
	stored, err := decodeStoredBindingDocument(access.EncryptedConfig)
	if err != nil || stored.ManagedRcloneV3 == nil || stored.V1 != nil || stored.ManagedRsyncV2 != nil {
		return blockedRcloneVersioningSummary(taskRevision), nil
	}
	document := *stored.ManagedRcloneV3
	if document.PublicationMode != mode || validateManagedRcloneBindingAssociation(document, managedRcloneBindingAssociation{
		Task: taskEntity, Link: link, Repository: repository,
	}) != nil || !validRcloneRenewalAssociation(rcloneBindingWorkflowPlan{repository: repository, binding: access, managedBinding: &document}) {
		return blockedRcloneVersioningSummary(taskRevision), nil
	}
	summary := persistedRcloneVersioningSummary(taskRevision, document)
	if document.RollbackPrepared {
		summary.State = backupasset.RcloneStateRollbackPrepared
		summary.ReasonCode = backupasset.RcloneReasonRollbackPrepared
		summary.RollbackCapability = backupasset.RcloneRollbackPrepared
		return backupasset.SafeRclonePublicationSummary(summary), nil
	}
	candidateSummary, candidateOK, err := service.rcloneCandidateProgress(ctx, taskID, taskRevision)
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	rollbackCapability, err := service.rcloneManagedRollbackCapability(ctx, repository.ID)
	if err != nil {
		return backupasset.RclonePublicationSummary{}, err
	}
	if summary.RollbackCapability == backupasset.RcloneRollbackCleanAvailable {
		summary.RollbackCapability = rollbackCapability
	}
	if candidateOK {
		if candidateSummary.RollbackCapability == backupasset.RcloneRollbackCleanAvailable {
			candidateSummary.RollbackCapability = summary.RollbackCapability
		}
		return candidateSummary, nil
	}
	if !document.PreflightExpiresAt.After(service.utcNow()) {
		summary.State = backupasset.RcloneStatePreflightRequired
		summary.ReasonCode = backupasset.RcloneReasonPreflightExpired
		return backupasset.SafeRclonePublicationSummary(summary), nil
	}
	var point model.RecoveryPoint
	pointResult := service.db.WithContext(ctx).
		Where("repository_id = ? AND producing_task_id = ? AND semantics IN ?", repository.ID, taskID, []string{
			string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline),
		}).Order("created_at DESC, id ASC").Limit(1).Find(&point)
	if pointResult.Error != nil {
		return backupasset.RclonePublicationSummary{}, fmt.Errorf("load Rclone versioning summary point: %w", pointResult.Error)
	}
	if pointResult.RowsAffected == 1 {
		summary.RollbackCapability = backupasset.RcloneRollbackPreparationOnly
		switch backupasset.RecoveryPointState(point.State) {
		case backupasset.RecoveryPointPreparing:
			summary.State = backupasset.RcloneStatePreparing
		case backupasset.RecoveryPointVerifying:
			summary.State = backupasset.RcloneStateVerifying
		case backupasset.RecoveryPointCommitted:
			summary.State = backupasset.RcloneStateCommitted
		case backupasset.RecoveryPointDegraded:
			summary.State = backupasset.RcloneStateDegraded
			summary.ReasonCode = backupasset.RcloneReasonProviderUnavailable
		case backupasset.RecoveryPointFailed:
			summary.State = backupasset.RcloneStateFailed
			summary.ReasonCode = backupasset.RcloneReasonOutcomeUnknown
		default:
			return blockedRcloneVersioningSummary(taskRevision), nil
		}
	}
	return backupasset.SafeRclonePublicationSummary(summary), nil
}

func persistedRcloneVersioningSummary(taskRevision uint64, document managedRcloneBindingDocumentV3) backupasset.RclonePublicationSummary {
	encryption := backupasset.RcloneEncryptionNone
	kmsStatus := backupasset.RcloneKMSNotApplicable
	readKeys := uint32(0)
	if document.Native != nil {
		switch document.Native.EncryptionProfile {
		case provider.RcloneNativeSSES3V1:
			encryption = backupasset.RcloneEncryptionSSES3
		case provider.RcloneNativeSSEKMSV1:
			encryption = backupasset.RcloneEncryptionSSEKMS
			kmsStatus = backupasset.RcloneKMSReady
			readKeys = uint32(len(document.Native.RetainedReadKeys))
		default:
			return blockedRcloneVersioningSummary(taskRevision)
		}
	}
	return backupasset.RclonePublicationSummary{
		Mode: document.PublicationMode, State: backupasset.RcloneStateReady, ReasonCode: backupasset.RcloneReasonReady,
		TaskRevision: strconv.FormatUint(taskRevision, 10), BindingRevision: strconv.FormatUint(document.BindingRevision, 10),
		CapabilityRevision: strconv.FormatUint(document.CapabilityRevision, 10),
		ConsistencyClass:   backupasset.RcloneConsistencyNotEvaluated, HashFidelity: backupasset.RcloneHashNotEvaluated,
		EstimatedReadBytes: "0", APICostClass: backupasset.RcloneCostNotEvaluated,
		StorageCostClass: backupasset.RcloneCostNotEvaluated, EgressCostClass: backupasset.RcloneCostNotEvaluated,
		EncryptionProfile: encryption, KMSKeyStatus: kmsStatus, KMSReadKeyCount: readKeys,
		RollbackLocatorPresent: true, RollbackCapability: backupasset.RcloneRollbackCleanAvailable,
	}
}

func blockedRcloneVersioningSummary(taskRevision uint64) backupasset.RclonePublicationSummary {
	return backupasset.SafeRclonePublicationSummary(backupasset.RclonePublicationSummary{
		TaskRevision: strconv.FormatUint(taskRevision, 10),
	})
}

func (service *Service) rcloneSummaryMode(ctx context.Context, taskID uint) backupasset.TaskPublicationMode {
	if service == nil {
		return ""
	}
	summary, err := service.RcloneVersioningSummary(ctx, taskID)
	if err != nil {
		return ""
	}
	return summary.Mode
}

func (service *Service) writeRcloneVersioningAudit(
	ctx context.Context,
	requestContext RequestContext,
	action backupasset.AuditAction,
	taskID uint,
	mode backupasset.TaskPublicationMode,
	state backupasset.RcloneVersioningState,
	reason backupasset.RcloneVersioningReasonCode,
	operationErr error,
) {
	if service == nil || service.audit == nil || taskID == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	outcome := backupasset.AuditOutcomeSuccess
	if operationErr != nil {
		if errors.Is(operationErr, backupasset.ErrForbidden) || errors.Is(operationErr, backupasset.ErrCapabilityUnavailable) {
			outcome = backupasset.AuditOutcomeBlocked
		} else {
			outcome = backupasset.AuditOutcomeFailure
		}
	}
	fields := map[backupasset.AuditField]any{
		backupasset.AuditFieldStage:         "rclone_versioning",
		backupasset.AuditFieldCorrelationID: requestContext.CorrelationID,
	}
	if mode == backupasset.PublicationLegacyMutable || mode == backupasset.PublicationVersionedPrefix || mode == backupasset.PublicationNativeObjectVersions {
		fields[backupasset.AuditFieldMode] = string(mode)
	}
	if state != "" {
		fields[backupasset.AuditFieldStatus] = string(state)
	}
	if reason != "" {
		fields[backupasset.AuditFieldReasonCode] = string(reason)
	}
	input := backupasset.AuditEventInput{
		Actor: requestContext.Actor, Action: action, Outcome: outcome, TaskID: &taskID, Fields: fields,
	}
	if operationErr != nil {
		input.FailureCode = "operation_failed"
	}
	if err := service.audit.Write(ctx, input); err != nil {
		logger.Module("backup_repository").Warn().Str("action", string(action)).Uint("task_id", taskID).Msg("Rclone 版本化资产审计写入失败")
	}
}
