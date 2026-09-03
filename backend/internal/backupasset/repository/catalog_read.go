package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

type CatalogPointReadRequest = catalog.PointReadRequest

// OpenCatalogRead is the only Catalog entry point that resolves Provider
// access and native point locators. Its caller supplies opaque database IDs,
// never a Task, path, remote, prefix, snapshot ID, or credential.
func (service *Service) OpenCatalogRead(ctx context.Context, request CatalogPointReadRequest) (provider.CatalogReadSession, error) {
	if err := service.ensureEnabled(""); err != nil {
		return nil, err
	}
	if err := service.requireRuntime(); err != nil {
		return nil, err
	}
	if backupasset.ValidateOpaqueID(request.RepositoryID) != nil || backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil {
		return nil, fmt.Errorf("%w: Catalog point", backupasset.ErrNotFound)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var point model.RecoveryPoint
	if err := service.db.WithContext(ctx).
		Where("id = ? AND repository_id = ?", request.RecoveryPointID, request.RepositoryID).
		First(&point).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: Catalog point", backupasset.ErrNotFound)
	} else if err != nil {
		return nil, fmt.Errorf("load Catalog point: %w", err)
	}
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).First(&repository, "id = ?", request.RepositoryID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: Catalog repository", backupasset.ErrNotFound)
	} else if err != nil {
		return nil, fmt.Errorf("load Catalog repository: %w", err)
	}
	if point.CapabilityRevision <= 0 || repository.CapabilityRevision <= 0 ||
		point.CapabilityRevision != repository.CapabilityRevision {
		return nil, fmt.Errorf("%w: Catalog capability revision changed", backupasset.ErrConflict)
	}

	kind := backupasset.ProviderKind(repository.ProviderKind)
	if kind == backupasset.ProviderCommand {
		return nil, capabilityError(backupasset.CapabilityTaskArtifactContractMissing, "")
	}
	if point.Semantics == string(backupasset.PointMutableHead) {
		if point.State != string(backupasset.RecoveryPointObserved) {
			return nil, capabilityError(backupasset.CapabilityCatalogUnavailable, "")
		}
		return service.openMutableCatalogRead(ctx, repository, point, kind)
	}
	if point.State != string(backupasset.RecoveryPointCommitted) && point.State != string(backupasset.RecoveryPointDegraded) {
		return nil, capabilityError(backupasset.CapabilityPointNotCommitted, "")
	}
	manifest, proof, err := service.activeCatalogManifest(ctx, point)
	if err != nil {
		return nil, err
	}
	switch kind {
	case backupasset.ProviderRestic:
		return service.openResticCatalogRead(ctx, repository, point, manifest, proof)
	case backupasset.ProviderRsync:
		return service.openRsyncCatalogRead(ctx, repository, point, manifest, proof)
	case backupasset.ProviderRclone:
		return service.openRcloneCatalogRead(ctx, repository, point, manifest, proof)
	default:
		return nil, capabilityError(backupasset.CapabilityCatalogUnavailable, "")
	}
}

func (service *Service) acquireCatalogAdmission(
	ctx context.Context,
	operation publication.ResticOperation,
) (publication.AdmissionToken, error) {
	if service == nil || service.admission == nil {
		return nil, fmt.Errorf("%w: immutable Catalog admission unavailable", backupasset.ErrInvalidState)
	}
	token, err := service.admission.Acquire(ctx, operation)
	if err != nil {
		if token != nil {
			_ = token.Close()
		}
		return nil, err
	}
	if token == nil || token.Operation() != operation ||
		(token.Mode() != publication.AdmissionManaged && token.Mode() != publication.AdmissionRollbackSafe) {
		if token != nil {
			_ = token.Close()
		}
		return nil, fmt.Errorf("%w: immutable Catalog is not admitted", backupasset.ErrForbidden)
	}
	return token, nil
}

func (service *Service) openRcloneCatalogRead(
	ctx context.Context,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	manifest model.RecoveryPointManifest,
	proof provider.CatalogManifestProof,
) (provider.CatalogReadSession, error) {
	if service.publication == nil || service.admission == nil || service.keyring == nil || point.ProducingTaskID == nil {
		return nil, fmt.Errorf("%w: immutable Rclone Catalog dependencies unavailable", backupasset.ErrInvalidState)
	}
	if point.CapabilityRevision <= 0 || repository.CapabilityRevision <= 0 ||
		point.CapabilityRevision != repository.CapabilityRevision {
		return nil, fmt.Errorf("%w: immutable Rclone Catalog capability revision changed", backupasset.ErrConflict)
	}
	kind, lineage, consistency, err := publicationReconciliationFacts(point)
	if err != nil || kind != backupasset.ProviderRclone || lineage.TaskID != *point.ProducingTaskID ||
		consistency.Provider != backupasset.ProviderRclone {
		return nil, fmt.Errorf("%w: immutable Rclone Catalog lineage changed", backupasset.ErrConflict)
	}
	locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
	if err != nil {
		return nil, err
	}
	attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	if err != nil {
		return nil, err
	}
	commit, err := provider.DecodeRcloneCommitV1(locator.TaggedCommit)
	if err != nil {
		return nil, err
	}
	if err := validateRcloneDurableCapabilityEvidence(point, consistency, attempt, commit); err != nil {
		return nil, err
	}
	commitDigest, err := canonicalRcloneProviderCommitDigest(commit)
	if err != nil || commitDigest != locator.ProviderCommitDigest || commitDigest != consistency.ProviderCommitDigest ||
		manifest.EncryptedCommitEvidence != locator.TaggedCommit || point.SourceFingerprint != locator.PhysicalIdentityDigest ||
		commit.ManifestIndexDigest != manifest.Digest || int64(commit.ManifestEntryCount) != manifest.EntryCount ||
		commit.RecoveryPointID != point.ID || commit.RepositoryID != repository.ID {
		return nil, fmt.Errorf("%w: immutable Rclone Catalog evidence changed", backupasset.ErrConflict)
	}
	token, err := service.acquireCatalogAdmission(ctx, publication.OperationManifest)
	if err != nil {
		return nil, err
	}
	keepToken := false
	defer func() {
		if !keepToken && token != nil {
			_ = token.Close()
		}
	}()
	runtime, err := service.publication.loadExactManagedRclonePublicationRuntime(ctx, lineage.TaskID)
	if err != nil {
		return nil, err
	}
	if err := validateRcloneReconcileRuntime(runtime, point, attempt); err != nil {
		return nil, err
	}
	markerKey, err := service.publication.rcloneMarkerKey(ctx, repository.ID)
	if err != nil {
		return nil, err
	}
	exactCommitKey, exactCommitVersionID := "", ""
	if attempt.PublicationMode == backupasset.PublicationNativeObjectVersions {
		if commit.Native == nil {
			return nil, fmt.Errorf("%w: immutable Rclone Catalog native commit evidence missing", backupasset.ErrConflict)
		}
		exactCommitKey = managedRcloneNativeControlCommitKey(runtime.binding, attempt)
		owned, _, evidenceErr := loadManagedRcloneNativeVersionEvidenceTx(
			ctx, service.db, repository.ID, point.ID, markerKey, locator, exactCommitKey,
		)
		if evidenceErr != nil {
			return nil, evidenceErr
		}
		exactCommitVersionID, evidenceErr = managedRcloneNativeCommitVersion(owned, exactCommitKey)
		if evidenceErr != nil {
			return nil, evidenceErr
		}
		commit.Native.CommitKey = exactCommitKey
		commit.Native.CommitVersionID = exactCommitVersionID
	}
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		return nil, err
	}
	reconcile, err := service.publication.rcloneReconcileInput(
		ctx, runtime, attempt, markerKey, leaseConfig, exactCommitKey, exactCommitVersionID,
	)
	if err != nil {
		return nil, err
	}
	_, maxItems, err := service.catalogManifestLimits()
	if err != nil {
		return nil, err
	}
	reader, err := service.registry.CatalogReader(backupasset.ProviderRclone)
	if err != nil {
		return nil, err
	}
	access := provider.AccessBinding{Provider: backupasset.ProviderRclone, RepositoryID: repository.ID}
	inner, err := reader.OpenCatalogRead(ctx, provider.CatalogReadRequest{
		Provider: backupasset.ProviderRclone, RecoveryPointID: point.ID,
		Snapshot: provider.ReadSnapshot{RepositoryID: repository.ID, CapabilityRevision: point.CapabilityRevision, SourceRevision: point.SourceFingerprint, Access: access},
		Point:    provider.PointLocator{Native: "managed:" + locator.PhysicalIdentityDigest}, Mode: provider.CatalogProofPublicationManifest,
		Manifest: proof, RcloneProof: &provider.RcloneCatalogProofInput{Reconcile: reconcile, Commit: commit}, MaxItems: maxItems,
	})
	if err != nil {
		return nil, err
	}
	if inner == nil {
		return nil, fmt.Errorf("%w: immutable Rclone Catalog reader returned nil session", backupasset.ErrInvalidState)
	}
	session := &sealedCatalogReadSession{inner: inner, token: token}
	keepToken = true
	return session, nil
}

func (service *Service) openRsyncCatalogRead(
	ctx context.Context,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	manifest model.RecoveryPointManifest,
	proof provider.CatalogManifestProof,
) (provider.CatalogReadSession, error) {
	if service.admission == nil || service.keyring == nil || point.ProducingTaskID == nil {
		return nil, fmt.Errorf("%w: immutable Rsync Catalog dependencies unavailable", backupasset.ErrInvalidState)
	}
	if point.CapabilityRevision <= 0 || repository.CapabilityRevision <= 0 ||
		point.CapabilityRevision != repository.CapabilityRevision {
		return nil, fmt.Errorf("%w: immutable Rsync Catalog capability revision changed", backupasset.ErrConflict)
	}
	token, err := service.acquireCatalogAdmission(ctx, publication.OperationManagedRsyncPointRead)
	if err != nil {
		return nil, err
	}
	keepToken := false
	defer func() {
		if !keepToken && token != nil {
			_ = token.Close()
		}
	}()
	runtime, err := loadExactManagedRsyncPublicationRuntime(ctx, service.db, *point.ProducingTaskID)
	if err != nil {
		return nil, err
	}
	if runtime.repository.ID != repository.ID {
		return nil, fmt.Errorf("%w: immutable Rsync Catalog binding changed", backupasset.ErrConflict)
	}
	readRequest, access, err := service.managedRsyncCommittedPointReadRequest(ctx, runtime, point)
	if err != nil {
		return nil, err
	}
	if readRequest.ManifestDigest != manifest.Digest || int64(readRequest.ManifestEntryCount) != manifest.EntryCount ||
		readRequest.SourceFingerprint != point.SourceFingerprint {
		return nil, fmt.Errorf("%w: immutable Rsync Catalog manifest changed", backupasset.ErrConflict)
	}
	adapter, err := service.newManagedRsyncCommittedPointAdapter()
	if err != nil {
		return nil, err
	}
	runtimeAccess, err := provider.NewRsyncCommittedPointRuntimeAccess(ctx, readRequest)
	if err != nil {
		return nil, mapManagedRsyncCommittedPointReadOpenError(ctx, err)
	}
	access.AdapterData = runtimeAccess
	snapshot := provider.ReadSnapshot{
		RepositoryID: repository.ID, CapabilityRevision: point.CapabilityRevision,
		SourceRevision: runtimeAccess.SourceRevision, Access: access,
	}
	points, err := adapter.ListPoints(ctx, snapshot, provider.PageRequest{Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(points.Items) != 1 || points.NextCursor != "" || points.Items[0].SourceRevision != point.SourceFingerprint {
		return nil, fmt.Errorf("%w: immutable Rsync Catalog point changed", backupasset.ErrConflict)
	}
	_, maxItems, err := service.catalogManifestLimits()
	if err != nil {
		return nil, err
	}
	inner, err := adapter.OpenCatalogRead(ctx, provider.CatalogReadRequest{
		Provider: backupasset.ProviderRsync, RecoveryPointID: point.ID, Snapshot: snapshot, Point: points.Items[0].Locator,
		Mode: provider.CatalogProofPublicationManifest, Manifest: proof, MaxItems: maxItems,
	})
	if err != nil {
		return nil, err
	}
	if inner == nil {
		return nil, fmt.Errorf("%w: immutable Rsync Catalog reader returned nil session", backupasset.ErrInvalidState)
	}
	session := &sealedCatalogReadSession{inner: inner, token: token}
	keepToken = true
	return session, nil
}

func (service *Service) activeCatalogManifest(
	ctx context.Context,
	point model.RecoveryPoint,
) (model.RecoveryPointManifest, provider.CatalogManifestProof, error) {
	var manifest model.RecoveryPointManifest
	if err := service.db.WithContext(ctx).
		Where("recovery_point_id = ? AND is_active = ?", point.ID, true).First(&manifest).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return manifest, provider.CatalogManifestProof{}, fmt.Errorf("%w: active Catalog manifest", backupasset.ErrConflict)
	} else if err != nil {
		return manifest, provider.CatalogManifestProof{}, fmt.Errorf("load active Catalog manifest: %w", err)
	}
	if manifest.Completeness != string(backupasset.ManifestComplete) || manifest.DigestAlgorithm != "sha256" ||
		!isLowerHex64(manifest.Digest) || manifest.EntryCount < 0 || manifest.Digest != point.ManifestDigest ||
		manifest.EntryCount != point.EntryCount || strings.TrimSpace(point.SourceFingerprint) == "" {
		return manifest, provider.CatalogManifestProof{}, fmt.Errorf("%w: active Catalog manifest facts changed", backupasset.ErrConflict)
	}
	return manifest, provider.CatalogManifestProof{
		ManifestID: manifest.ID, Revision: manifest.Revision, DigestAlgorithm: manifest.DigestAlgorithm,
		Digest: manifest.Digest, EntryCount: manifest.EntryCount, Completeness: backupasset.ManifestComplete,
		SourceRevision: point.SourceFingerprint,
	}, nil
}

func (service *Service) openResticCatalogRead(
	ctx context.Context,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	manifest model.RecoveryPointManifest,
	proof provider.CatalogManifestProof,
) (provider.CatalogReadSession, error) {
	if service.publication == nil || service.admission == nil || point.Semantics != string(backupasset.PointNativeSnapshot) ||
		point.ProducingTaskID == nil || point.ProducingTaskRunID == nil || repository.RepositoryIdentity == nil {
		return nil, fmt.Errorf("%w: immutable Restic Catalog dependencies unavailable", backupasset.ErrInvalidState)
	}
	if point.CapabilityRevision <= 0 || repository.CapabilityRevision <= 0 ||
		point.CapabilityRevision != repository.CapabilityRevision {
		return nil, fmt.Errorf("%w: immutable Restic Catalog capability revision changed", backupasset.ErrConflict)
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil || lineage.TaskID != *point.ProducingTaskID || lineage.TaskRunID != *point.ProducingTaskRunID ||
		lineage.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
		return nil, fmt.Errorf("%w: immutable Restic Catalog lineage changed", backupasset.ErrConflict)
	}

	token, err := service.acquireCatalogAdmission(ctx, publication.OperationManifest)
	if err != nil {
		return nil, err
	}
	keepToken := false
	defer func() {
		if !keepToken && token != nil {
			_ = token.Close()
		}
	}()

	runtime, link, err := service.publication.loadExactPublicationRuntime(ctx, lineage.TaskID, backupasset.PublicationAuditContext{})
	if err != nil {
		return nil, err
	}
	if runtime.repository.ID != repository.ID || link.ID != lineage.TaskRepositoryLinkID {
		return nil, fmt.Errorf("%w: immutable Restic Catalog binding changed", backupasset.ErrConflict)
	}
	locator, err := decodeResticPointLocator(point.EncryptedProviderLocator)
	if err != nil {
		return nil, err
	}
	var evidence manifestCommitEvidenceV1
	decoder := json.NewDecoder(bytes.NewBufferString(manifest.EncryptedCommitEvidence))
	decoder.DisallowUnknownFields()
	if rejectDuplicateOrNullJSONMembers(manifest.EncryptedCommitEvidence) != nil || decoder.Decode(&evidence) != nil ||
		!errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return nil, fmt.Errorf("%w: immutable Restic Catalog evidence is invalid", backupasset.ErrConflict)
	}
	tags, err := deriveResticPublicationTags(link.ID, point.ID)
	if err != nil {
		return nil, err
	}
	if evidence.Version != 1 || evidence.Provider != backupasset.ProviderRestic || evidence.RepositoryIdentity != *repository.RepositoryIdentity ||
		evidence.NativePointID != locator.FullSnapshotID || evidence.ObservedTags != tags || evidence.ObservedTagDigest != publicationTagDigest(tags) ||
		evidence.CaptureStartedAt.IsZero() || evidence.CaptureFinishedAt.IsZero() ||
		evidence.CaptureFinishedAt.Before(evidence.CaptureStartedAt) {
		return nil, fmt.Errorf("%w: immutable Restic Catalog evidence changed", backupasset.ErrConflict)
	}
	if err := service.proveResticReadIdentity(ctx, repository.ID, *repository.RepositoryIdentity, point.CapabilityRevision, runtime); err != nil {
		return nil, err
	}
	limits, maxItems, err := service.catalogManifestLimits()
	if err != nil {
		return nil, err
	}
	commit := provider.ResticCommitV1{
		Provider: backupasset.ProviderRestic, RepositoryIdentity: evidence.RepositoryIdentity, NativePointID: evidence.NativePointID,
		CaptureStartedAt: evidence.CaptureStartedAt.UTC(), CaptureFinishedAt: evidence.CaptureFinishedAt.UTC(),
		FilesProcessed: evidence.FilesProcessed, LogicalBytes: evidence.LogicalBytes,
	}
	attempt := provider.ResticAttemptV1{
		Provider: backupasset.ProviderRestic, RepositoryID: repository.ID, RepositoryIdentity: evidence.RepositoryIdentity,
		TaskRepositoryLinkID: link.ID, RecoveryPointID: point.ID, TaskID: lineage.TaskID, TaskRunID: lineage.TaskRunID,
		RequiredTags: tags, PointDeadlineAt: lineage.PointDeadlineAt.UTC(), CapabilityRevision: point.CapabilityRevision,
		AdapterRevision: provider.ResticCatalogAdapterRevision, Access: runtime.access,
	}
	reader, err := service.registry.CatalogReader(backupasset.ProviderRestic)
	if err != nil {
		return nil, err
	}
	inner, err := reader.OpenCatalogRead(ctx, provider.CatalogReadRequest{
		Provider: backupasset.ProviderRestic, RecoveryPointID: point.ID,
		Snapshot: provider.ReadSnapshot{RepositoryID: repository.ID, CapabilityRevision: point.CapabilityRevision, SourceRevision: point.SourceFingerprint, Access: runtime.access},
		Point:    provider.PointLocator{Native: locator.FullSnapshotID}, Mode: provider.CatalogProofPublicationManifest,
		Manifest: proof, ResticProof: &provider.ResticCatalogProofInput{Attempt: attempt, Commit: commit, Limits: limits}, MaxItems: maxItems,
	})
	if err != nil {
		return nil, err
	}
	if inner == nil {
		return nil, fmt.Errorf("%w: immutable Restic Catalog reader returned nil session", backupasset.ErrInvalidState)
	}
	session := &sealedCatalogReadSession{inner: inner, token: token}
	keepToken = true
	return session, nil
}

func (service *Service) catalogManifestLimits() (provider.ManifestLimits, int, error) {
	config, err := service.foundation.PublicationConfig()
	if err != nil {
		return provider.ManifestLimits{}, 0, err
	}
	if config.ManifestMaxEntries <= 0 || config.ManifestMaxEntries > math.MaxInt {
		return provider.ManifestLimits{}, 0, fmt.Errorf("%w: Catalog item bound unavailable", backupasset.ErrInvalidState)
	}
	return provider.ManifestLimits{
		Timeout: config.ManifestTimeout, MaxBytes: config.ManifestMaxBytes, MaxEntries: config.ManifestMaxEntries,
		MaxRecordBytes: config.ManifestMaxRecordBytes, MaxDepth: config.ManifestMaxDepth,
	}, int(config.ManifestMaxEntries), nil
}

func (service *Service) openMutableCatalogRead(
	ctx context.Context,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	kind backupasset.ProviderKind,
) (provider.CatalogReadSession, error) {
	if kind != backupasset.ProviderRsync && kind != backupasset.ProviderRclone {
		return nil, capabilityError(backupasset.CapabilityCatalogUnavailable, "")
	}
	runtime, err := service.loadRepositoryRuntime(ctx, repository.ID)
	if err != nil {
		return nil, err
	}
	if runtime.repository.ID != repository.ID || runtime.access.Provider != kind || runtime.repository.CapabilityRevision != point.CapabilityRevision ||
		strings.TrimSpace(point.SourceFingerprint) == "" {
		return nil, fmt.Errorf("%w: mutable Catalog source changed", backupasset.ErrConflict)
	}
	snapshot := provider.ReadSnapshot{
		RepositoryID: repository.ID, CapabilityRevision: point.CapabilityRevision,
		SourceRevision: point.SourceFingerprint, Access: runtime.access,
	}
	lister, err := service.registry.PointLister(kind)
	if err != nil {
		return nil, err
	}
	points, err := lister.ListPoints(ctx, snapshot, provider.PageRequest{Limit: 2})
	if err != nil {
		return nil, err
	}
	if len(points.Items) != 1 || points.NextCursor != "" || points.Items[0].Semantics != backupasset.PointMutableHead ||
		points.Items[0].SourceRevision != snapshot.SourceRevision {
		return nil, fmt.Errorf("%w: mutable Catalog point set changed", backupasset.ErrConflict)
	}
	publicationConfig, err := service.foundation.PublicationConfig()
	if err != nil {
		return nil, err
	}
	maxItems := publicationConfig.ManifestMaxEntries
	if maxItems <= 0 {
		return nil, fmt.Errorf("%w: Catalog item bound unavailable", backupasset.ErrInvalidState)
	}
	if maxItems > math.MaxInt {
		maxItems = math.MaxInt
	}
	reader, err := service.registry.CatalogReader(kind)
	if err != nil {
		return nil, err
	}
	inner, err := reader.OpenCatalogRead(ctx, provider.CatalogReadRequest{
		Provider: kind, RecoveryPointID: point.ID, Snapshot: snapshot, Point: points.Items[0].Locator,
		Mode: provider.CatalogProofMutableObservation, MaxItems: int(maxItems),
	})
	if err != nil {
		return nil, err
	}
	return &sealedCatalogReadSession{inner: inner}, nil
}

type sealedCatalogReadSession struct {
	inner     provider.CatalogReadSession
	token     publication.AdmissionToken
	closeOnce sync.Once
	closeErr  error
}

func (session *sealedCatalogReadSession) SourceRevision() string {
	if session == nil || session.inner == nil {
		return ""
	}
	return session.inner.SourceRevision()
}

func (session *sealedCatalogReadSession) ListCanonical(ctx context.Context, request provider.PageRequest) (provider.CatalogRecordPage, error) {
	if session == nil || session.inner == nil {
		return provider.CatalogRecordPage{}, fmt.Errorf("%w: Catalog read session unavailable", backupasset.ErrInvalidState)
	}
	page, err := session.inner.ListCanonical(ctx, request)
	if err != nil {
		return provider.CatalogRecordPage{}, err
	}
	for index := range page.Items {
		record := &page.Items[index]
		if record.ProviderLocator.Native == "" || record.SealedProviderLocator != "" {
			return provider.CatalogRecordPage{}, fmt.Errorf("%w: invalid Provider locator projection", backupasset.ErrInvalidState)
		}
		payload, err := json.Marshal(struct {
			Version int    `json:"version"`
			Native  string `json:"native"`
		}{Version: 1, Native: record.ProviderLocator.Native})
		if err != nil {
			return provider.CatalogRecordPage{}, fmt.Errorf("seal Provider locator: %w", err)
		}
		sealed, err := secure.EncryptIfNeeded(string(payload))
		if err != nil {
			return provider.CatalogRecordPage{}, fmt.Errorf("seal Provider locator: %w", err)
		}
		record.ProviderLocator = provider.EntryLocator{}
		record.SealedProviderLocator = sealed
	}
	return page, nil
}

func (session *sealedCatalogReadSession) Finalize(ctx context.Context) (provider.CatalogReadProof, error) {
	if session == nil || session.inner == nil {
		return provider.CatalogReadProof{}, fmt.Errorf("%w: Catalog read session unavailable", backupasset.ErrInvalidState)
	}
	return session.inner.Finalize(ctx)
}

func (session *sealedCatalogReadSession) Close() error {
	if session == nil {
		return nil
	}
	session.closeOnce.Do(func() {
		if session.inner != nil {
			session.closeErr = session.inner.Close()
		}
		if session.token != nil {
			session.closeErr = errors.Join(session.closeErr, session.token.Close())
		}
	})
	return session.closeErr
}
