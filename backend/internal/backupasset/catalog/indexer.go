package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const catalogBuildOwnerPrefix = "catalog:"

const maxCatalogBuildTeardown = 30 * time.Second

type PointReadRequest struct {
	RepositoryID    string
	RecoveryPointID string
}

type PointReadFactory interface {
	OpenCatalogRead(context.Context, PointReadRequest) (provider.CatalogReadSession, error)
}

type IdentityKeySource interface {
	Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error)
}

type CatalogLease interface {
	Acquire(context.Context, backupasset.AcquireLeaseRequest) (backupasset.Lease, error)
	Release(context.Context, backupasset.LeaseFence) error
	ValidateFenceTx(context.Context, *gorm.DB, backupasset.LeaseFence) error
}

type renewableCatalogLease interface {
	Renew(context.Context, backupasset.LeaseFence) (backupasset.Lease, error)
}

type IndexerConfig struct {
	BatchSize         int
	BuildTimeout      time.Duration
	MaxEntries        int64
	HeartbeatInterval time.Duration
}

type IndexerDependencies struct {
	DB           *gorm.DB
	Factory      PointReadFactory
	Lease        CatalogLease
	IdentityKeys IdentityKeySource
	Now          func() time.Time
	Config       IndexerConfig
}

type Indexer struct {
	db           *gorm.DB
	factory      PointReadFactory
	lease        CatalogLease
	identityKeys IdentityKeySource
	now          func() time.Time
	config       IndexerConfig

	attemptsMu sync.Mutex
	attempts   map[string]activeCatalogBuild
}

type activeCatalogBuild struct {
	fence  backupasset.LeaseFence
	cancel context.CancelFunc
	done   chan struct{}
}

type BuildRequest struct {
	RepositoryID    string
	RecoveryPointID string
	CorrelationID   string
}

type BuildCandidate struct {
	RepositoryID    string
	RecoveryPointID string
}

type frozenBuild struct {
	repository model.BackupRepository
	point      model.RecoveryPoint
	manifest   *model.RecoveryPointManifest
	mode       provider.CatalogProofMode
	proof      provider.CatalogManifestProof
}

func NewIndexer(dependencies IndexerDependencies) (*Indexer, error) {
	if dependencies.Config.HeartbeatInterval <= 0 && dependencies.Config.BuildTimeout > 0 {
		dependencies.Config.HeartbeatInterval = dependencies.Config.BuildTimeout / 4
		if dependencies.Config.HeartbeatInterval > 30*time.Second {
			dependencies.Config.HeartbeatInterval = 30 * time.Second
		}
	}
	if dependencies.DB == nil || dependencies.Factory == nil || dependencies.Lease == nil || dependencies.IdentityKeys == nil ||
		dependencies.Config.BatchSize <= 0 || dependencies.Config.BatchSize > 100000 ||
		dependencies.Config.BuildTimeout <= 0 || dependencies.Config.MaxEntries <= 0 || dependencies.Config.HeartbeatInterval <= 0 {
		return nil, fmt.Errorf("%w: invalid Catalog indexer dependencies", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Indexer{
		db: dependencies.DB, factory: dependencies.Factory, lease: dependencies.Lease,
		identityKeys: dependencies.IdentityKeys, now: dependencies.Now, config: dependencies.Config,
		attempts: make(map[string]activeCatalogBuild),
	}, nil
}

func (indexer *Indexer) Build(ctx context.Context, request BuildRequest) (result model.CatalogGeneration, buildErr error) {
	if indexer == nil || indexer.db == nil || backupasset.ValidateOpaqueID(request.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil || len(request.CorrelationID) > 64 ||
		strings.ContainsAny(request.CorrelationID, "\r\n\x00") {
		return model.CatalogGeneration{}, fmt.Errorf("%w: invalid Catalog build request", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	frozen, err := indexer.loadFrozenBuild(ctx, request)
	if err != nil {
		return model.CatalogGeneration{}, err
	}
	lease, err := indexer.lease.Acquire(ctx, backupasset.AcquireLeaseRequest{
		RecoveryPointID: request.RecoveryPointID, HolderType: backupasset.LeaseHolderCatalogBuild,
		OwnerID: catalogBuildOwnerPrefix + request.RecoveryPointID,
	})
	if err != nil {
		return model.CatalogGeneration{}, err
	}
	registered := false
	var (
		buildCancel       context.CancelFunc
		stopHeartbeat     func()
		renewalErrors     chan error
		generation        model.CatalogGeneration
		generationSettled bool
	)
	defer func() {
		if stopHeartbeat != nil {
			stopHeartbeat()
		}
		if buildCancel != nil {
			buildCancel()
		}
		if renewalErrors != nil {
			select {
			case renewalErr := <-renewalErrors:
				if renewalErr != nil && (buildErr == nil || errors.Is(buildErr, context.Canceled) || errors.Is(buildErr, context.DeadlineExceeded)) {
					buildErr = renewalErr
				}
			default:
			}
		}

		teardownCtx, cancelTeardown := indexer.newBuildTeardownContext()
		defer cancelTeardown()
		if registered && generation.ID != "" && !generationSettled {
			state, code := classifyCatalogBuildFailure(buildErr)
			if err := indexer.markGenerationFailure(teardownCtx, generation.ID, lease.Fence, state, code); err != nil {
				evidenceErr := fmt.Errorf("%w: Catalog failure evidence unavailable", backupasset.ErrInvalidState)
				buildErr = errors.Join(buildErr, evidenceErr)
			}
		}
		if err := indexer.lease.Release(teardownCtx, lease.Fence); err != nil {
			if buildErr == nil {
				buildErr = err
			} else {
				buildErr = errors.Join(buildErr, fmt.Errorf("%w: Catalog lease release failed", backupasset.ErrInvalidState))
			}
		}
		if registered {
			indexer.unregisterActiveBuild(request.RecoveryPointID, lease.Fence)
		}
	}()

	deadline := indexer.utcNow().Add(indexer.config.BuildTimeout)
	if lease.AbsoluteDeadline.Before(deadline) {
		deadline = lease.AbsoluteDeadline
	}
	remaining := deadline.Sub(indexer.utcNow())
	if remaining <= 0 {
		return model.CatalogGeneration{}, fmt.Errorf("%w: Catalog build deadline reached", backupasset.ErrLeaseDeadlineExceeded)
	}
	buildContext, cancel := context.WithTimeout(ctx, remaining)
	buildCancel = cancel
	if err := indexer.registerActiveBuild(request.RecoveryPointID, lease.Fence, cancel); err != nil {
		return model.CatalogGeneration{}, err
	}
	registered = true
	renewalErrors = make(chan error, 1)
	stopHeartbeat = indexer.startLeaseHeartbeat(buildContext, lease.Fence, cancel, renewalErrors)
	generation, err = indexer.beginGeneration(buildContext, request, frozen, lease.Fence)
	if err != nil {
		return model.CatalogGeneration{}, err
	}

	session, err := indexer.factory.OpenCatalogRead(buildContext, PointReadRequest{
		RepositoryID: request.RepositoryID, RecoveryPointID: request.RecoveryPointID,
	})
	if err != nil {
		return generation, err
	}
	if session == nil {
		return generation, fmt.Errorf("%w: nil Catalog read session", backupasset.ErrInvalidState)
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close()
		}
	}()
	if session.SourceRevision() != frozen.point.SourceFingerprint {
		return generation, fmt.Errorf("%w: Provider source revision changed", ErrCatalogSourceChanged)
	}
	key, err := indexer.identityKeys.Active(buildContext, backupasset.KeyDomainEntryIdentity)
	if err != nil || key.Domain != backupasset.KeyDomainEntryIdentity || key.State != backupasset.DomainKeyActive || len(key.Key) < 32 {
		return generation, fmt.Errorf("%w: entry identity key unavailable", ErrIdentityKeyUnavailable)
	}
	accumulator, err := provider.NewCatalogProjectionAccumulator(
		backupasset.ProviderKind(frozen.repository.ProviderKind), frozen.repository.ID, frozen.point.ID, frozen.point.SourceFingerprint,
	)
	if err != nil {
		return generation, err
	}
	identityRegistry := NewIdentityRegistry()
	paths := make(map[string]string)
	batch := make([]model.CatalogEntry, 0, indexer.config.BatchSize)
	cursor := ""
	var written int64
	for {
		page, pageErr := session.ListCanonical(buildContext, provider.PageRequest{Limit: indexer.config.BatchSize, Cursor: cursor})
		if pageErr != nil {
			return generation, pageErr
		}
		if len(page.Items) == 0 && page.NextCursor != "" {
			return generation, fmt.Errorf("%w: empty Catalog page has a continuation", ErrCatalogProofMismatch)
		}
		for _, record := range page.Items {
			written++
			if written > indexer.config.MaxEntries {
				return generation, fmt.Errorf("%w: maximum entries exceeded", ErrCatalogBuildLimit)
			}
			entry, entryErr := indexer.catalogEntryFromRecord(key.Key, generation, record, identityRegistry, paths)
			if entryErr != nil {
				return generation, entryErr
			}
			if entryErr = accumulator.Write(record); entryErr != nil {
				return generation, entryErr
			}
			paths[record.NormalizedPath] = entry.EntryID
			batch = append(batch, entry)
			if len(batch) == indexer.config.BatchSize {
				if entryErr = indexer.insertBatch(buildContext, batch); entryErr != nil {
					return generation, entryErr
				}
				batch = batch[:0]
			}
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			return generation, fmt.Errorf("%w: Catalog continuation did not advance", ErrCatalogProofMismatch)
		}
		cursor = page.NextCursor
	}
	if len(batch) > 0 {
		if err := indexer.insertBatch(buildContext, batch); err != nil {
			return generation, err
		}
	}
	digest, counted, err := accumulator.Finalize()
	if err != nil {
		return generation, err
	}
	proof, err := session.Finalize(buildContext)
	if err != nil {
		return generation, err
	}
	if err := validateBuildProof(frozen, proof, digest, counted, written); err != nil {
		return generation, err
	}
	if err := session.Close(); err != nil {
		return generation, err
	}
	closed = true
	activated, err := indexer.activate(buildContext, generation, frozen, lease.Fence, proof, written, digest)
	if err != nil {
		return generation, err
	}
	generationSettled = true
	return activated, nil
}

func (indexer *Indexer) loadFrozenBuild(ctx context.Context, request BuildRequest) (frozenBuild, error) {
	var frozen frozenBuild
	if err := indexer.db.WithContext(ctx).First(&frozen.repository, "id = ?", request.RepositoryID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return frozenBuild{}, fmt.Errorf("%w: Catalog repository", backupasset.ErrNotFound)
	} else if err != nil {
		return frozenBuild{}, fmt.Errorf("load Catalog repository: %w", err)
	}
	if err := indexer.db.WithContext(ctx).Where("id = ? AND repository_id = ?", request.RecoveryPointID, request.RepositoryID).
		First(&frozen.point).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return frozenBuild{}, fmt.Errorf("%w: Catalog point", backupasset.ErrNotFound)
	} else if err != nil {
		return frozenBuild{}, fmt.Errorf("load Catalog point: %w", err)
	}
	if frozen.repository.ProviderKind == string(backupasset.ProviderCommand) {
		return frozenBuild{}, fmt.Errorf("%w: Command has no artifact contract", backupasset.ErrCapabilityUnavailable)
	}
	if frozen.point.CapabilityRevision != frozen.repository.CapabilityRevision || strings.TrimSpace(frozen.point.SourceFingerprint) == "" {
		return frozenBuild{}, fmt.Errorf("%w: Catalog point facts changed", backupasset.ErrConflict)
	}
	switch backupasset.PointVersionSemantics(frozen.point.Semantics) {
	case backupasset.PointMutableHead:
		if frozen.point.State != string(backupasset.RecoveryPointObserved) {
			return frozenBuild{}, fmt.Errorf("%w: mutable Catalog point is not observed", backupasset.ErrConflict)
		}
		frozen.mode = provider.CatalogProofMutableObservation
	case backupasset.PointNativeSnapshot, backupasset.PointXirangManifest, backupasset.PointImportedBaseline:
		if frozen.point.State != string(backupasset.RecoveryPointCommitted) && frozen.point.State != string(backupasset.RecoveryPointDegraded) {
			return frozenBuild{}, fmt.Errorf("%w: immutable Catalog point is not committed", backupasset.ErrConflict)
		}
		var manifest model.RecoveryPointManifest
		if err := indexer.db.WithContext(ctx).Where("recovery_point_id = ? AND is_active = ?", frozen.point.ID, true).
			First(&manifest).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return frozenBuild{}, fmt.Errorf("%w: active Catalog manifest", backupasset.ErrConflict)
		} else if err != nil {
			return frozenBuild{}, fmt.Errorf("load active Catalog manifest: %w", err)
		}
		if manifest.Completeness != string(backupasset.ManifestComplete) || manifest.DigestAlgorithm != "sha256" ||
			manifest.Digest == "" || manifest.EntryCount < 0 || manifest.Digest != frozen.point.ManifestDigest ||
			manifest.EntryCount != frozen.point.EntryCount {
			return frozenBuild{}, fmt.Errorf("%w: active Catalog manifest facts are invalid", backupasset.ErrConflict)
		}
		frozen.manifest = &manifest
		frozen.mode = provider.CatalogProofPublicationManifest
		frozen.proof = provider.CatalogManifestProof{
			ManifestID: manifest.ID, Revision: manifest.Revision, DigestAlgorithm: manifest.DigestAlgorithm,
			Digest: manifest.Digest, EntryCount: manifest.EntryCount, Completeness: backupasset.ManifestComplete,
			SourceRevision: frozen.point.SourceFingerprint,
		}
	default:
		return frozenBuild{}, fmt.Errorf("%w: unsupported Catalog point semantics", backupasset.ErrInvalidState)
	}
	return frozen, nil
}

func (indexer *Indexer) beginGeneration(
	ctx context.Context,
	request BuildRequest,
	frozen frozenBuild,
	fence backupasset.LeaseFence,
) (model.CatalogGeneration, error) {
	var generation model.CatalogGeneration
	err := indexer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateRecoveryPointWriteAdmissionTx(ctx, tx, frozen.point.ID); err != nil {
			return err
		}
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND repository_id = ?", frozen.point.ID, frozen.repository.ID).
			First(&point).Error; err != nil {
			return fmt.Errorf("lock Catalog point: %w", err)
		}
		if !sameFrozenPoint(point, frozen.point) {
			return fmt.Errorf("%w: Catalog point changed before build", ErrCatalogSourceChanged)
		}
		if err := indexer.lease.ValidateFenceTx(ctx, tx, fence); err != nil {
			return err
		}
		var sequence int
		if err := tx.Model(&model.CatalogGeneration{}).Where("recovery_point_id = ?", frozen.point.ID).
			Select("COALESCE(MAX(generation), 0)").Scan(&sequence).Error; err != nil {
			return fmt.Errorf("load Catalog generation sequence: %w", err)
		}
		id, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		startedAt := indexer.utcNow()
		generation = model.CatalogGeneration{
			ID: id, RecoveryPointID: frozen.point.ID, Generation: sequence + 1, State: string(GenerationBuilding),
			SourceFingerprint: frozen.point.SourceFingerprint, CorrelationID: request.CorrelationID,
			StartedAt: startedAt, CreatedAt: startedAt, UpdatedAt: startedAt,
		}
		if frozen.manifest != nil {
			manifestID := frozen.manifest.ID
			generation.ManifestID = &manifestID
			generation.ExpectedEntryCount = frozen.manifest.EntryCount
			generation.ExpectedDigest = frozen.manifest.Digest
		}
		if err := tx.Create(&generation).Error; err != nil {
			return fmt.Errorf("create Catalog generation: %w", err)
		}
		return nil
	})
	return generation, err
}

func (indexer *Indexer) catalogEntryFromRecord(
	key []byte,
	generation model.CatalogGeneration,
	record provider.CatalogRecord,
	registry *IdentityRegistry,
	paths map[string]string,
) (model.CatalogEntry, error) {
	if record.ProviderLocator.Native != "" || !secure.IsEncrypted(record.SealedProviderLocator) {
		return model.CatalogEntry{}, fmt.Errorf("%w: Provider locator crossed the Repository boundary", ErrInvalidCatalogContract)
	}
	identity, err := DeriveEntryIdentity(key, generation.RecoveryPointID, record.NormalizedPath)
	if err != nil {
		return model.CatalogEntry{}, err
	}
	if identity.Name != record.Name || strings.Join(pathComponents(record.NormalizedPath)[:len(pathComponents(record.NormalizedPath))-1], "/") != record.ParentNormalizedPath {
		return model.CatalogEntry{}, fmt.Errorf("%w: Catalog path graph mismatch", ErrInvalidCatalogContract)
	}
	if err := registry.Add(identity); err != nil {
		return model.CatalogEntry{}, err
	}
	if identity.ParentEntryID != nil {
		parentID, ok := paths[record.ParentNormalizedPath]
		if !ok || parentID != *identity.ParentEntryID {
			return model.CatalogEntry{}, fmt.Errorf("%w: Catalog parent is missing", ErrInvalidCatalogContract)
		}
	}
	strength, err := ParseFingerprintStrength(record.FingerprintStrength)
	if err != nil {
		return model.CatalogEntry{}, err
	}
	createdAt := indexer.utcNow()
	return model.CatalogEntry{
		GenerationID: generation.ID, EntryID: identity.EntryID, RecoveryPointID: generation.RecoveryPointID,
		ParentEntryID: identity.ParentEntryID, NormalizedPath: identity.NormalizedPath, Name: identity.Name,
		EntryType: string(record.Type), Size: record.Size, ModifiedAt: record.ModifiedAt, Mode: record.Mode, Owner: record.Owner,
		MimeType: record.MIMEType, Fingerprint: record.Fingerprint, FingerprintStrength: string(strength),
		EncryptedProviderLocator: record.SealedProviderLocator, SecurityState: "sealed", CreatedAt: createdAt,
	}, nil
}

func pathComponents(value string) []string { return strings.Split(value, "/") }

func (indexer *Indexer) insertBatch(ctx context.Context, entries []model.CatalogEntry) error {
	if len(entries) == 0 || len(entries) > indexer.config.BatchSize {
		return fmt.Errorf("%w: invalid Catalog entry batch", backupasset.ErrInvalidState)
	}
	if err := indexer.db.WithContext(ctx).Create(&entries).Error; err != nil {
		return fmt.Errorf("write Catalog entry batch: %w", err)
	}
	return nil
}

func validateBuildProof(frozen frozenBuild, proof provider.CatalogReadProof, digest string, counted, written int64) error {
	if proof.Provider != backupasset.ProviderKind(frozen.repository.ProviderKind) || proof.Mode != frozen.mode ||
		proof.SourceRevision != frozen.point.SourceFingerprint || proof.Catalog.DigestAlgorithm != "sha256" ||
		proof.Catalog.Digest != digest || proof.Catalog.EntryCount != counted || counted != written || !proof.Catalog.Complete {
		return fmt.Errorf("%w: Catalog projection proof changed", ErrCatalogProofMismatch)
	}
	if frozen.mode == provider.CatalogProofPublicationManifest {
		if proof.Manifest != frozen.proof || proof.Manifest.EntryCount != written {
			return fmt.Errorf("%w: publication manifest proof changed", ErrCatalogProofMismatch)
		}
	} else if proof.Manifest != (provider.CatalogManifestProof{}) {
		return fmt.Errorf("%w: mutable Catalog returned a manifest proof", ErrCatalogProofMismatch)
	}
	return nil
}

func (indexer *Indexer) activate(
	ctx context.Context,
	generation model.CatalogGeneration,
	frozen frozenBuild,
	fence backupasset.LeaseFence,
	proof provider.CatalogReadProof,
	written int64,
	digest string,
) (model.CatalogGeneration, error) {
	var activated model.CatalogGeneration
	err := indexer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateRecoveryPointWriteAdmissionTx(ctx, tx, frozen.point.ID); err != nil {
			return err
		}
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", frozen.point.ID).Error; err != nil {
			return fmt.Errorf("lock Catalog activation point: %w", err)
		}
		if !sameFrozenPoint(point, frozen.point) {
			return fmt.Errorf("%w: Catalog source changed during build", ErrCatalogSourceChanged)
		}
		if frozen.manifest != nil {
			var manifest model.RecoveryPointManifest
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND recovery_point_id = ? AND is_active = ?", frozen.manifest.ID, point.ID, true).
				First(&manifest).Error; err != nil {
				return fmt.Errorf("lock active Catalog manifest: %w", err)
			}
			if !sameFrozenManifest(manifest, *frozen.manifest) {
				return fmt.Errorf("%w: active manifest changed during Catalog build", ErrCatalogSourceChanged)
			}
		}
		if err := indexer.lease.ValidateFenceTx(ctx, tx, fence); err != nil {
			return err
		}
		var current model.CatalogGeneration
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND recovery_point_id = ? AND state = ? AND is_active = ?", generation.ID, point.ID, GenerationBuilding, false).
			Limit(1).Find(&current)
		if result.Error != nil || result.RowsAffected != 1 {
			if result.Error != nil {
				return fmt.Errorf("lock building Catalog generation: %w", result.Error)
			}
			return fmt.Errorf("%w: building Catalog generation changed", backupasset.ErrConflict)
		}
		now := indexer.utcNow()
		if err := tx.Model(&model.CatalogGeneration{}).Where("recovery_point_id = ? AND is_active = ?", point.ID, true).
			Updates(map[string]any{"state": GenerationSuperseded, "is_active": false, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("supersede active Catalog generation: %w", err)
		}
		updates := map[string]any{
			"state": GenerationComplete, "is_active": true, "written_entry_count": written,
			"written_digest": digest, "error_code": "", "finished_at": now, "updated_at": now,
		}
		result = tx.Model(&model.CatalogGeneration{}).Where("id = ? AND state = ? AND is_active = ?", generation.ID, GenerationBuilding, false).Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("activate Catalog generation: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: Catalog activation lost compare-and-swap", backupasset.ErrConflict)
		}
		if err := tx.First(&activated, "id = ?", generation.ID).Error; err != nil {
			return fmt.Errorf("reload active Catalog generation: %w", err)
		}
		if activated.WrittenDigest != proof.Catalog.Digest || activated.WrittenEntryCount != proof.Catalog.EntryCount {
			return fmt.Errorf("%w: activated Catalog proof changed", ErrCatalogProofMismatch)
		}
		return nil
	})
	return activated, err
}

func classifyCatalogBuildFailure(err error) (GenerationState, string) {
	switch {
	case errors.Is(err, provider.ErrCatalogSessionIncomplete):
		return GenerationPartial, "catalog_build_incomplete"
	case errors.Is(err, ErrCatalogBuildLimit):
		return GenerationPartial, "catalog_build_limit"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, backupasset.ErrLeaseDeadlineExceeded):
		return GenerationPartial, "catalog_build_timeout"
	case errors.Is(err, ErrCatalogSourceChanged):
		return GenerationFailed, "catalog_source_changed"
	case errors.Is(err, ErrCatalogProofMismatch), errors.Is(err, provider.ErrCatalogProofMismatch):
		return GenerationFailed, "catalog_proof_mismatch"
	case errors.Is(err, ErrInvalidCatalogContract), errors.Is(err, provider.ErrCatalogProtocol):
		return GenerationFailed, "catalog_invalid_record"
	case errors.Is(err, ErrIdentityKeyUnavailable):
		return GenerationFailed, "catalog_identity_key_unavailable"
	}
	var capabilityError *provider.CapabilityError
	if errors.As(err, &capabilityError) {
		switch capabilityError.Reason.Code {
		case backupasset.CapabilityProviderOperationTimeout:
			return GenerationPartial, "catalog_provider_timeout"
		case backupasset.CapabilityProviderResourceLimit:
			return GenerationPartial, "catalog_provider_resource_limit"
		case backupasset.CapabilityProviderUnavailable:
			return GenerationFailed, "catalog_provider_unavailable"
		}
	}
	return GenerationFailed, "catalog_build_failed"
}

func (indexer *Indexer) markGenerationFailure(
	ctx context.Context,
	generationID string,
	fence backupasset.LeaseFence,
	state GenerationState,
	code string,
) error {
	if backupasset.ValidateOpaqueID(generationID) != nil || (state != GenerationPartial && state != GenerationFailed) ||
		strings.TrimSpace(code) == "" || len(code) > 64 {
		return fmt.Errorf("%w: invalid failed Catalog generation", backupasset.ErrInvalidState)
	}
	return indexer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := indexer.lease.ValidateFenceTx(ctx, tx, fence); err != nil {
			return err
		}
		now := indexer.utcNow()
		result := tx.Model(&model.CatalogGeneration{}).
			Where("id = ? AND recovery_point_id = ? AND state = ? AND is_active = ?", generationID, fence.RecoveryPointID, GenerationBuilding, false).
			Updates(map[string]any{"state": state, "error_code": code, "finished_at": now, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("mark Catalog generation failed: %w", result.Error)
		}
		return nil
	})
}

func (indexer *Indexer) RetireProjection(ctx context.Context, recoveryPointID string) error {
	if indexer == nil || indexer.db == nil || backupasset.ValidateOpaqueID(recoveryPointID) != nil {
		return fmt.Errorf("%w: invalid retired Catalog point", backupasset.ErrInvalidState)
	}
	return indexer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return indexer.DeactivatePointProjectionTx(ctx, tx, recoveryPointID)
	})
}

// DeactivatePointProjectionTx lets the point lifecycle coordinator remove a
// mutable point's public projection in the same transaction that retires it.
func (indexer *Indexer) DeactivatePointProjectionTx(ctx context.Context, tx *gorm.DB, recoveryPointID string) error {
	if indexer == nil || tx == nil || backupasset.ValidateOpaqueID(recoveryPointID) != nil {
		return fmt.Errorf("%w: invalid retired Catalog point transaction", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var point model.RecoveryPoint
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", recoveryPointID).Error; err != nil {
		return fmt.Errorf("lock retired Catalog point: %w", err)
	}
	if backupasset.PointVersionSemantics(point.Semantics) != backupasset.PointMutableHead {
		return fmt.Errorf("%w: immutable Catalog point cannot be retired through mutable projection", backupasset.ErrConflict)
	}
	switch backupasset.RecoveryPointState(point.State) {
	case backupasset.RecoveryPointObserved:
		// The caller must transition the point to retired before committing.
	case backupasset.RecoveryPointRetired:
		if point.RetiredAt == nil || point.RetirementReason == nil {
			return fmt.Errorf("%w: retired Catalog point is incomplete", backupasset.ErrConflict)
		}
	default:
		return fmt.Errorf("%w: Catalog point is not observed or retired", backupasset.ErrConflict)
	}
	now := indexer.utcNow()
	result := tx.WithContext(ctx).Model(&model.CatalogGeneration{}).
		Where("recovery_point_id = ? AND state = ? AND is_active = ?", point.ID, GenerationComplete, true).
		Updates(map[string]any{"state": GenerationSuperseded, "is_active": false, "updated_at": now})
	if result.Error != nil {
		return fmt.Errorf("retire Catalog projection: %w", result.Error)
	}
	if result.RowsAffected > 1 {
		return fmt.Errorf("%w: multiple active Catalog projections", backupasset.ErrConflict)
	}
	return nil
}

func (indexer *Indexer) ReconcileAbandoned(ctx context.Context, abandonedAfter time.Duration, limit int) (int, error) {
	if indexer == nil || indexer.db == nil || abandonedAfter <= 0 || limit <= 0 || limit > 10000 {
		return 0, fmt.Errorf("%w: invalid abandoned Catalog reconciliation", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := indexer.utcNow()
	if err := indexer.reconcileRetiredProjections(ctx, now, limit); err != nil {
		return 0, err
	}
	cutoff := now.Add(-abandonedAfter)
	var candidates []model.CatalogGeneration
	if err := indexer.db.WithContext(ctx).
		Select("id", "recovery_point_id").Where("state = ? AND is_active = ? AND updated_at <= ?", GenerationBuilding, false, cutoff).
		Order("updated_at ASC, id ASC").Limit(limit).Find(&candidates).Error; err != nil {
		return 0, fmt.Errorf("list abandoned Catalog generations: %w", err)
	}
	reconciled := 0
	for _, candidate := range candidates {
		changed := false
		err := indexer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var generation model.CatalogGeneration
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND recovery_point_id = ? AND state = ? AND is_active = ? AND updated_at <= ?",
					candidate.ID, candidate.RecoveryPointID, GenerationBuilding, false, cutoff).
				Limit(1).Find(&generation)
			if result.Error != nil {
				return fmt.Errorf("lock abandoned Catalog generation: %w", result.Error)
			}
			if result.RowsAffected == 0 {
				return nil
			}
			var activeLeases int64
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&model.RecoveryPointLease{}).
				Where("recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ? AND lease_expires_at > ? AND absolute_deadline > ?",
					generation.RecoveryPointID, backupasset.LeaseHolderCatalogBuild, catalogBuildOwnerPrefix+generation.RecoveryPointID,
					backupasset.LeaseActive, now, now).
				Count(&activeLeases).Error; err != nil {
				return fmt.Errorf("check abandoned Catalog lease: %w", err)
			}
			if activeLeases != 0 {
				return nil
			}
			update := tx.Model(&model.CatalogGeneration{}).
				Where("id = ? AND state = ? AND is_active = ?", generation.ID, GenerationBuilding, false).
				Updates(map[string]any{
					"state": GenerationFailed, "error_code": "catalog_build_abandoned",
					"finished_at": now, "updated_at": now,
				})
			if update.Error != nil {
				return fmt.Errorf("reconcile abandoned Catalog generation: %w", update.Error)
			}
			changed = update.RowsAffected == 1
			return nil
		})
		if err != nil {
			return reconciled, err
		}
		if changed {
			reconciled++
		}
	}
	return reconciled, nil
}

type retiredProjectionControl struct {
	GenerationID    string
	RecoveryPointID string
}

func (indexer *Indexer) reconcileRetiredProjections(ctx context.Context, now time.Time, limit int) error {
	var candidates []retiredProjectionControl
	if err := indexer.db.WithContext(ctx).Table("catalog_generations AS generations").
		Select("generations.id AS generation_id, generations.recovery_point_id").
		Joins("JOIN recovery_points AS points ON points.id = generations.recovery_point_id").
		Where("generations.state = ? AND generations.is_active = ?", GenerationComplete, true).
		Where("points.semantics = ? AND points.state = ?", backupasset.PointMutableHead, backupasset.RecoveryPointRetired).
		Order("generations.recovery_point_id ASC, generations.id ASC").Limit(limit).Scan(&candidates).Error; err != nil {
		return fmt.Errorf("list retired Catalog projections: %w", err)
	}
	for _, candidate := range candidates {
		if err := indexer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var point model.RecoveryPoint
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "semantics", "state").
				Where("id = ? AND semantics = ? AND state = ?", candidate.RecoveryPointID, backupasset.PointMutableHead, backupasset.RecoveryPointRetired).
				Limit(1).Find(&point)
			if result.Error != nil {
				return fmt.Errorf("lock retired Catalog point: %w", result.Error)
			}
			if result.RowsAffected == 0 {
				return nil
			}
			update := tx.Model(&model.CatalogGeneration{}).
				Where("id = ? AND recovery_point_id = ? AND state = ? AND is_active = ?", candidate.GenerationID, point.ID, GenerationComplete, true).
				Updates(map[string]any{"state": GenerationSuperseded, "is_active": false, "updated_at": now})
			if update.Error != nil {
				return fmt.Errorf("deactivate retired Catalog projection: %w", update.Error)
			}
			if update.RowsAffected > 1 {
				return fmt.Errorf("%w: multiple retired Catalog projections", backupasset.ErrConflict)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (indexer *Indexer) registerActiveBuild(pointID string, fence backupasset.LeaseFence, cancel context.CancelFunc) error {
	indexer.attemptsMu.Lock()
	defer indexer.attemptsMu.Unlock()
	if _, exists := indexer.attempts[pointID]; exists {
		return fmt.Errorf("%w: Catalog point build already active", backupasset.ErrLeaseHeld)
	}
	indexer.attempts[pointID] = activeCatalogBuild{fence: fence, cancel: cancel, done: make(chan struct{})}
	return nil
}

func (indexer *Indexer) unregisterActiveBuild(pointID string, fence backupasset.LeaseFence) {
	indexer.attemptsMu.Lock()
	if attempt, exists := indexer.attempts[pointID]; exists && attempt.fence.FenceToken == fence.FenceToken {
		delete(indexer.attempts, pointID)
		close(attempt.done)
	}
	indexer.attemptsMu.Unlock()
}

func (indexer *Indexer) cancelAndJoinActiveBuild(ctx context.Context, pointID string) error {
	if indexer == nil {
		return fmt.Errorf("%w: Catalog Indexer is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	indexer.attemptsMu.Lock()
	attempt, exists := indexer.attempts[pointID]
	indexer.attemptsMu.Unlock()
	if !exists {
		return nil
	}
	attempt.cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-attempt.done:
		return nil
	}
}

func (indexer *Indexer) startLeaseHeartbeat(
	ctx context.Context,
	fence backupasset.LeaseFence,
	cancel context.CancelFunc,
	renewalErrors chan<- error,
) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	stopHeartbeat := func() {
		once.Do(func() { close(stop) })
		<-done
	}
	renewer, renewable := indexer.lease.(renewableCatalogLease)
	go func() {
		defer close(done)
		timer := time.NewTimer(indexer.config.HeartbeatInterval)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-timer.C:
				if !renewable {
					timer.Reset(indexer.config.HeartbeatInterval)
					continue
				}
				if _, err := renewer.Renew(ctx, fence); err != nil {
					select {
					case renewalErrors <- fmt.Errorf("%w: Catalog lease heartbeat failed", backupasset.ErrLeaseFenceLost):
					default:
					}
					cancel()
					return
				}
				timer.Reset(indexer.config.HeartbeatInterval)
			}
		}
	}()
	return stopHeartbeat
}

func (indexer *Indexer) newBuildTeardownContext() (context.Context, context.CancelFunc) {
	timeout := indexer.config.BuildTimeout
	if timeout > maxCatalogBuildTeardown {
		timeout = maxCatalogBuildTeardown
	}
	return context.WithTimeout(context.Background(), timeout)
}

// RevokeActiveBuilds cancels every in-process attempt and joins its unified
// teardown. Build remains the sole owner of durable failure evidence and exact
// lease release, so shutdown cannot release a fence ahead of Provider join.
func (indexer *Indexer) RevokeActiveBuilds(ctx context.Context) error {
	if indexer == nil || indexer.lease == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	indexer.attemptsMu.Lock()
	attempts := make([]activeCatalogBuild, 0, len(indexer.attempts))
	for _, attempt := range indexer.attempts {
		attempts = append(attempts, attempt)
	}
	indexer.attemptsMu.Unlock()
	for _, attempt := range attempts {
		attempt.cancel()
	}
	for _, attempt := range attempts {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-attempt.done:
		}
	}
	return nil
}

// ListCandidates derives retry eligibility from durable point/generation facts.
// It returns only opaque Repository/RecoveryPoint IDs; the worker applies the
// process-local per-repository lane.
func (indexer *Indexer) ListCandidates(
	ctx context.Context,
	limit int,
	now time.Time,
	config backupasset.CatalogConfig,
) ([]BuildCandidate, error) {
	if indexer == nil || indexer.db == nil || limit <= 0 || limit > 10000 || now.IsZero() ||
		config.ReconcileInterval <= 0 || config.BuildTimeout <= 0 {
		return nil, fmt.Errorf("%w: invalid Catalog candidate request", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	type pointControl struct {
		ID                string
		RepositoryID      string
		Semantics         string
		State             string
		SourceFingerprint string
		ManifestDigest    string
		EntryCount        int64
		ObservedAt        *time.Time
		ProviderKind      string
	}
	scanLimit := limit * 20
	if scanLimit < 200 {
		scanLimit = 200
	}
	if scanLimit > 20000 {
		scanLimit = 20000
	}
	var points []pointControl
	if err := indexer.db.WithContext(ctx).Table("recovery_points AS points").
		Select(`points.id, points.repository_id, points.semantics, points.state, points.source_fingerprint,
			points.manifest_digest, points.entry_count, points.observed_at, repositories.provider_kind`).
		Joins("JOIN backup_repositories AS repositories ON repositories.id = points.repository_id").
		Where("repositories.provider_kind <> ?", backupasset.ProviderCommand).
		Where(`(points.semantics = ? AND points.state = ?) OR
			(points.semantics IN ? AND points.state IN ?)`,
			backupasset.PointMutableHead, backupasset.RecoveryPointObserved,
			[]string{string(backupasset.PointNativeSnapshot), string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline)},
			[]string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)}).
		Order("points.created_at ASC, points.id ASC").Limit(scanLimit).Scan(&points).Error; err != nil {
		return nil, fmt.Errorf("list Catalog candidate points: %w", err)
	}
	result := make([]BuildCandidate, 0, min(limit, len(points)))
	for _, point := range points {
		eligible, err := indexer.catalogPointEligibleAt(ctx, point.ID, point.Semantics, point.SourceFingerprint, point.ManifestDigest,
			point.EntryCount, point.ObservedAt, now.UTC(), config)
		if err != nil {
			return nil, err
		}
		if eligible {
			result = append(result, BuildCandidate{RepositoryID: point.RepositoryID, RecoveryPointID: point.ID})
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (indexer *Indexer) catalogPointEligibleAt(
	ctx context.Context,
	pointID, semantics, sourceFingerprint, manifestDigest string,
	entryCount int64,
	observedAt *time.Time,
	now time.Time,
	config backupasset.CatalogConfig,
) (bool, error) {
	var generations []model.CatalogGeneration
	if err := indexer.db.WithContext(ctx).Where("recovery_point_id = ?", pointID).
		Order("generation DESC").Limit(12).Find(&generations).Error; err != nil {
		return false, fmt.Errorf("load Catalog candidate history: %w", err)
	}
	var active *model.CatalogGeneration
	for index := range generations {
		if generations[index].IsActive {
			active = &generations[index]
			break
		}
	}
	if active != nil && active.State == string(GenerationComplete) && active.SourceFingerprint == sourceFingerprint {
		if backupasset.PointVersionSemantics(semantics) != backupasset.PointMutableHead {
			return active.ExpectedDigest != manifestDigest || active.ExpectedEntryCount != entryCount, nil
		}
		if observedAt != nil && now.Before(observedAt.UTC().Add(2*config.ReconcileInterval)) {
			return false, nil
		}
		return true, nil
	}
	if len(generations) == 0 {
		return true, nil
	}
	latest := generations[0]
	if latest.State == string(GenerationBuilding) {
		return false, nil
	}
	if latest.SourceFingerprint != sourceFingerprint {
		return true, nil
	}
	if latest.State != string(GenerationPartial) && latest.State != string(GenerationFailed) {
		return true, nil
	}
	if nonRetryableCatalogFailure(latest.ErrorCode) || latest.FinishedAt == nil {
		return false, nil
	}
	failureCount := 0
	for _, generation := range generations {
		if generation.SourceFingerprint != sourceFingerprint ||
			(generation.State != string(GenerationPartial) && generation.State != string(GenerationFailed)) {
			break
		}
		failureCount++
	}
	nextAt := latest.FinishedAt.UTC().Add(RetryDelay(config, failureCount, pointID, latest.ID))
	return !now.Before(nextAt), nil
}

func nonRetryableCatalogFailure(code string) bool {
	switch code {
	case "catalog_proof_mismatch", "catalog_projection_mismatch", "catalog_invalid_record", "catalog_identity_key_unavailable", "catalog_source_changed":
		return true
	default:
		return false
	}
}

func RetryDelay(config backupasset.CatalogConfig, failureCount int, pointID, latestGenerationID string) time.Duration {
	if failureCount <= 0 || config.ReconcileInterval <= 0 || config.BuildTimeout <= 0 {
		return 0
	}
	base := config.ReconcileInterval
	if base > 5*time.Minute {
		base = 5 * time.Minute
	}
	capDuration := config.BuildTimeout
	if capDuration > time.Hour {
		capDuration = time.Hour
	}
	if capDuration < base {
		capDuration = base
	}
	ordinal := failureCount - 1
	if ordinal > 10 {
		ordinal = 10
	}
	raw := base
	for index := 0; index < ordinal && raw < capDuration; index++ {
		if raw > capDuration/2 {
			raw = capDuration
		} else {
			raw *= 2
		}
	}
	if raw > capDuration {
		raw = capDuration
	}
	digest := sha256.Sum256([]byte(pointID + latestGenerationID + strconv.Itoa(ordinal)))
	ratio := float64(binary.BigEndian.Uint16(digest[:2])) / 65535
	return time.Duration(float64(raw) * (0.8 + 0.4*ratio))
}

func sameFrozenPoint(current, frozen model.RecoveryPoint) bool {
	return current.ID == frozen.ID && current.RepositoryID == frozen.RepositoryID && current.Semantics == frozen.Semantics &&
		current.State == frozen.State && current.SourceFingerprint == frozen.SourceFingerprint &&
		current.CapabilityRevision == frozen.CapabilityRevision && equalOptionalTime(current.ObservedAt, frozen.ObservedAt)
}

func sameFrozenManifest(current, frozen model.RecoveryPointManifest) bool {
	return current.ID == frozen.ID && current.RecoveryPointID == frozen.RecoveryPointID && current.Revision == frozen.Revision &&
		current.DigestAlgorithm == frozen.DigestAlgorithm && current.Digest == frozen.Digest && current.Completeness == frozen.Completeness &&
		current.EntryCount == frozen.EntryCount && current.IsActive == frozen.IsActive
}

func equalOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.UTC().Equal(right.UTC())
}

func (indexer *Indexer) utcNow() time.Time { return indexer.now().UTC() }
