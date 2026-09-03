package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const (
	maxContentEntryLocatorBytes = 1 << 20
	contentSourceCleanupTimeout = 5 * time.Second
)

// ValidateContentCacheRoot proves that a dedicated local cache root does not
// overlap any local Task or managed Repository source known to this process.
// It returns only a closed cache error and never exposes the private source.
func (service *Service) ValidateContentCacheRoot(ctx context.Context, candidate string) error {
	if err := service.ValidatePrivateRuntimeRoot(ctx, candidate); err != nil {
		return fmt.Errorf("%w: private runtime root proof failed", content.ErrCacheUnsafeRoot)
	}
	return nil
}

type contentEntryLocatorV1 struct {
	Version int    `json:"version"`
	Native  string `json:"native"`
}

type exactContentRecord struct {
	repository model.BackupRepository
	point      model.RecoveryPoint
	generation model.CatalogGeneration
	entry      model.CatalogEntry
	locator    provider.EntryLocator
}

type contentAdmissionToken struct {
	inner publication.AdmissionToken
	once  sync.Once
	err   error
}

func (token *contentAdmissionToken) Generation() uint64 {
	if token == nil || token.inner == nil {
		return 0
	}
	return token.inner.Generation()
}

func (token *contentAdmissionToken) Mode() publication.AdmissionMode {
	if token == nil || token.inner == nil {
		return ""
	}
	return token.inner.Mode()
}

func (token *contentAdmissionToken) Operation() publication.ResticOperation {
	if token == nil || token.inner == nil {
		return ""
	}
	return token.inner.Operation()
}

func (token *contentAdmissionToken) Close() error {
	if token == nil || token.inner == nil {
		return nil
	}
	token.once.Do(func() { token.err = token.inner.Close() })
	return token.err
}

// OpenContentSource resolves one exact active Catalog tuple. Provider locators,
// access bindings, admission tokens, and reader handles stay sealed inside the
// returned session.
func (service *Service) OpenContentSource(ctx context.Context, request content.SourceRequest) (content.SourceSession, error) {
	if err := service.ensureEnabled(""); err != nil {
		return nil, err
	}
	if err := service.requireRuntime(); err != nil {
		return nil, err
	}
	if service.admission == nil || content.ValidateSourceRequest(request) != nil {
		return nil, fmt.Errorf("%w: invalid content source request", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rawToken, err := service.admission.Acquire(ctx, publication.OperationContentRead)
	if err != nil {
		return nil, err
	}
	token := &contentAdmissionToken{inner: rawToken}
	keepToken := false
	defer func() {
		if !keepToken {
			_ = token.Close()
		}
	}()
	if token.Operation() != publication.OperationContentRead ||
		(token.Mode() != publication.AdmissionManaged && token.Mode() != publication.AdmissionRollbackSafe) {
		return nil, fmt.Errorf("%w: content source is not admitted", backupasset.ErrForbidden)
	}
	record, err := service.loadExactContentRecord(ctx, request)
	if err != nil {
		return nil, err
	}
	kind := backupasset.ProviderKind(record.repository.ProviderKind)
	if kind == backupasset.ProviderCommand {
		return nil, capabilityError(backupasset.CapabilityTaskArtifactContractMissing, "")
	}
	if record.point.Semantics == string(backupasset.PointMutableHead) {
		if record.point.State != string(backupasset.RecoveryPointObserved) {
			return nil, capabilityError(backupasset.CapabilityPointNotCommitted, "")
		}
		session, openErr := service.openMutableContentSource(ctx, request, record, kind, token)
		if openErr == nil && session != nil {
			keepToken = true
		}
		return session, openErr
	}
	if record.point.State != string(backupasset.RecoveryPointCommitted) && record.point.State != string(backupasset.RecoveryPointDegraded) {
		return nil, capabilityError(backupasset.CapabilityPointNotCommitted, "")
	}
	session, openErr := service.openImmutableContentSource(ctx, request, record, kind, token)
	if openErr == nil && session != nil {
		keepToken = true
	}
	return session, openErr
}

func (service *Service) loadExactContentRecord(ctx context.Context, request content.SourceRequest) (exactContentRecord, error) {
	var generation model.CatalogGeneration
	result := service.db.WithContext(ctx).
		Where("id = ? AND recovery_point_id = ? AND state = ? AND is_active = ?",
			request.CatalogGenerationID, request.Ref.RecoveryPointID, catalog.GenerationComplete, true).
		Limit(1).Find(&generation)
	if result.Error != nil {
		return exactContentRecord{}, fmt.Errorf("load exact content generation: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return exactContentRecord{}, fmt.Errorf("%w: active content generation", backupasset.ErrNotFound)
	}
	var entry model.CatalogEntry
	result = service.db.WithContext(ctx).
		Where("generation_id = ? AND entry_id = ? AND recovery_point_id = ?",
			generation.ID, request.Ref.EntryID, request.Ref.RecoveryPointID).
		Limit(1).Find(&entry)
	if result.Error != nil {
		return exactContentRecord{}, fmt.Errorf("load exact content entry: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return exactContentRecord{}, fmt.Errorf("%w: active content entry", backupasset.ErrNotFound)
	}
	var point model.RecoveryPoint
	if err := service.db.WithContext(ctx).First(&point, "id = ?", request.Ref.RecoveryPointID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return exactContentRecord{}, fmt.Errorf("%w: content recovery point", backupasset.ErrNotFound)
		}
		return exactContentRecord{}, fmt.Errorf("load content recovery point: %w", err)
	}
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).First(&repository, "id = ?", point.RepositoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return exactContentRecord{}, fmt.Errorf("%w: content repository", backupasset.ErrNotFound)
		}
		return exactContentRecord{}, fmt.Errorf("load content repository: %w", err)
	}
	if generation.SourceFingerprint != request.ExpectedSource || point.SourceFingerprint != request.ExpectedSource ||
		entry.Fingerprint != request.ExpectedEntry || entry.EntryType != string(backupasset.CatalogEntryFile) ||
		entry.SecurityState != "sealed" || entry.Size < 0 ||
		repository.Status != string(backupasset.RepositoryOnline) ||
		point.CapabilityRevision <= 0 || point.CapabilityRevision != repository.CapabilityRevision {
		return exactContentRecord{}, fmt.Errorf("%w: content source binding changed", backupasset.ErrConflict)
	}
	locator, err := decodeContentEntryLocator(entry.EncryptedProviderLocator)
	if err != nil {
		return exactContentRecord{}, err
	}
	return exactContentRecord{repository: repository, point: point, generation: generation, entry: entry, locator: locator}, nil
}

func decodeContentEntryLocator(payload string) (provider.EntryLocator, error) {
	if len(payload) == 0 || len(payload) > maxContentEntryLocatorBytes || rejectDuplicateOrNullJSONMembers(payload) != nil {
		return provider.EntryLocator{}, fmt.Errorf("%w: invalid content entry locator", backupasset.ErrConflict)
	}
	var document contentEntryLocatorV1
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.Version != 1 || document.Native == "" {
		return provider.EntryLocator{}, fmt.Errorf("%w: invalid content entry locator", backupasset.ErrConflict)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return provider.EntryLocator{}, fmt.Errorf("%w: trailing content entry locator", backupasset.ErrConflict)
	}
	return provider.EntryLocator{Native: document.Native}, nil
}

func (service *Service) openMutableContentSource(
	ctx context.Context,
	request content.SourceRequest,
	record exactContentRecord,
	kind backupasset.ProviderKind,
	token publication.AdmissionToken,
) (content.SourceSession, error) {
	if kind != backupasset.ProviderRsync && kind != backupasset.ProviderRclone {
		return nil, capabilityError(backupasset.CapabilityCatalogUnavailable, "")
	}
	if record.point.PhysicalAvailability != string(backupasset.PhysicalOnline) {
		return nil, capabilityError(backupasset.CapabilityRepositoryOffline, "")
	}
	runtime, err := service.loadRepositoryRuntime(ctx, record.repository.ID)
	if err != nil {
		return nil, err
	}
	if runtime.repository.ID != record.repository.ID || runtime.access.Provider != kind ||
		runtime.repository.CapabilityRevision != record.point.CapabilityRevision {
		return nil, fmt.Errorf("%w: mutable content runtime changed", backupasset.ErrConflict)
	}
	snapshot := provider.ReadSnapshot{
		RepositoryID: record.repository.ID, CapabilityRevision: record.point.CapabilityRevision,
		SourceRevision: request.ExpectedSource, Access: runtime.access,
	}
	pointLister, err := service.registry.PointLister(kind)
	if err != nil {
		return nil, err
	}
	capabilities, err := decodeRepositoryCapabilities(record.repository.CapabilitiesJSON)
	if err != nil {
		return nil, err
	}
	return service.openResolvedProviderContentSource(
		ctx, request, record, kind, snapshot, true, capabilities.OpenRange, token,
		func(resolveCtx context.Context) (provider.PointLocator, error) {
			return resolveMutableContentPoint(resolveCtx, pointLister, snapshot)
		},
		func(validateCtx context.Context) error {
			return service.revalidateMutableContentSource(validateCtx, request, record.repository.ID, kind)
		},
	)
}

func (service *Service) openImmutableContentSource(
	ctx context.Context,
	request content.SourceRequest,
	record exactContentRecord,
	kind backupasset.ProviderKind,
	token publication.AdmissionToken,
) (content.SourceSession, error) {
	switch kind {
	case backupasset.ProviderRestic:
		snapshot, point, err := service.resolveResticContentBinding(ctx, record)
		if err != nil {
			return nil, err
		}
		return service.openResolvedProviderContentSource(
			ctx, request, record, kind, snapshot, false, false, token,
			func(context.Context) (provider.PointLocator, error) { return point, nil },
			func(validateCtx context.Context) error {
				current, err := service.loadExactContentRecord(validateCtx, request)
				if err != nil || current.point.State != string(backupasset.RecoveryPointCommitted) && current.point.State != string(backupasset.RecoveryPointDegraded) {
					return fmt.Errorf("%w: immutable content source changed", backupasset.ErrConflict)
				}
				currentSnapshot, currentPoint, err := service.resolveResticContentBinding(validateCtx, current)
				if err != nil || currentPoint != point || currentSnapshot.RepositoryID != snapshot.RepositoryID ||
					currentSnapshot.CapabilityRevision != snapshot.CapabilityRevision || currentSnapshot.SourceRevision != snapshot.SourceRevision {
					return fmt.Errorf("%w: immutable content source changed", backupasset.ErrConflict)
				}
				return nil
			},
		)
	case backupasset.ProviderRsync:
		return service.openManagedRsyncContentSource(ctx, request, record, token)
	case backupasset.ProviderRclone:
		return service.openManagedRcloneContentSource(ctx, request, record, token)
	default:
		return nil, capabilityError(backupasset.CapabilityCatalogUnavailable, "")
	}
}

func (service *Service) openManagedRcloneContentSource(
	ctx context.Context,
	request content.SourceRequest,
	record exactContentRecord,
	token publication.AdmissionToken,
) (content.SourceSession, error) {
	pointLocator, err := decodeManagedRclonePointLocator(record.point.EncryptedProviderLocator)
	if err != nil {
		return nil, err
	}
	if pointLocator.PublicationMode == backupasset.PublicationNativeObjectVersions {
		return service.openManagedRcloneNativeContentSource(ctx, request, record, token)
	}
	snapshot, point, resolved, rangeProven, err := service.resolveManagedRclonePortableContentBinding(ctx, record)
	if err != nil {
		return nil, err
	}
	return service.openResolvedProviderContentSource(
		ctx, request, resolved, backupasset.ProviderRclone, snapshot, false, rangeProven, token,
		func(context.Context) (provider.PointLocator, error) { return point, nil },
		func(validateCtx context.Context) error {
			current, loadErr := service.loadExactContentRecord(validateCtx, request)
			if loadErr != nil || current.point.State != string(backupasset.RecoveryPointCommitted) && current.point.State != string(backupasset.RecoveryPointDegraded) {
				return fmt.Errorf("%w: immutable Rclone content source changed", backupasset.ErrConflict)
			}
			currentSnapshot, currentPoint, currentRecord, currentRange, resolveErr := service.resolveManagedRclonePortableContentBinding(validateCtx, current)
			if resolveErr != nil || currentPoint != point || currentRecord.locator != resolved.locator || currentRange != rangeProven ||
				currentSnapshot.RepositoryID != snapshot.RepositoryID || currentSnapshot.CapabilityRevision != snapshot.CapabilityRevision ||
				currentSnapshot.SourceRevision != snapshot.SourceRevision {
				return fmt.Errorf("%w: immutable Rclone content source changed", backupasset.ErrConflict)
			}
			return nil
		},
	)
}

func (service *Service) openManagedRcloneNativeContentSource(
	ctx context.Context,
	request content.SourceRequest,
	record exactContentRecord,
	token publication.AdmissionToken,
) (content.SourceSession, error) {
	s3, physicalKey, versionID, initialHead, err := service.resolveManagedRcloneNativeContentBinding(ctx, record)
	if err != nil {
		return nil, err
	}
	reader, err := openRcloneNativeContentReader(ctx, s3, request, physicalKey, versionID)
	if err != nil {
		return nil, err
	}
	modifiedAt := record.entry.ModifiedAt
	if modifiedAt != nil {
		utc := modifiedAt.UTC()
		modifiedAt = &utc
	}
	stat := content.SourceStat{
		Size: record.entry.Size, ModifiedAt: modifiedAt, MediaType: record.entry.MimeType,
		SourceFingerprint: request.ExpectedSource, EntryFingerprint: request.ExpectedEntry,
		FingerprintStrong: record.entry.FingerprintStrength == string(catalog.FingerprintStrong),
	}
	session := &sealedContentSourceSession{
		stat: stat,
		capabilities: content.SourceCapabilities{
			Provider: backupasset.ProviderRclone, Sequential: true, Range: true,
		},
		reader: newOnceReadCloser(reader, false), token: token,
		cleanupParent: ctx, cleanupTimeout: contentSourceCleanupTimeout,
		revalidate: func(validateCtx context.Context) error {
			current, loadErr := service.loadExactContentRecord(validateCtx, request)
			if loadErr != nil {
				return loadErr
			}
			if current.point.State != string(backupasset.RecoveryPointCommitted) && current.point.State != string(backupasset.RecoveryPointDegraded) ||
				current.repository.CapabilityRevision != record.repository.CapabilityRevision ||
				current.point.CapabilityRevision != record.point.CapabilityRevision ||
				current.locator.Native != record.locator.Native ||
				current.point.EncryptedProviderLocator != record.point.EncryptedProviderLocator {
				return fmt.Errorf("%w: immutable native Rclone content source changed", backupasset.ErrConflict)
			}
			_, currentPhysicalKey, currentVersionID, currentHead, resolveErr :=
				service.resolveManagedRcloneNativeContentBinding(validateCtx, current)
			if resolveErr != nil {
				return resolveErr
			}
			if currentPhysicalKey != physicalKey || currentVersionID != versionID ||
				!sameRcloneNativeContentHead(initialHead, currentHead) {
				return fmt.Errorf("%w: immutable native Rclone content source changed", backupasset.ErrConflict)
			}
			return nil
		},
	}
	return session, nil
}

type rcloneNativeContentReader interface {
	OpenVersion(context.Context, provider.RcloneNativeExactReadRequest) (io.ReadCloser, error)
	OpenVersionRange(context.Context, provider.RcloneNativeExactRangeRequest) (io.ReadCloser, error)
}

func openRcloneNativeContentReader(
	ctx context.Context,
	reader rcloneNativeContentReader,
	request content.SourceRequest,
	physicalKey string,
	versionID string,
) (io.ReadCloser, error) {
	if reader == nil || physicalKey == "" || versionID == "" {
		return nil, fmt.Errorf("%w: native Rclone content reader is unavailable", backupasset.ErrInvalidState)
	}
	switch request.Mode {
	case content.SourceModeSequential:
		return reader.OpenVersion(ctx, provider.RcloneNativeExactReadRequest{PhysicalKey: physicalKey, VersionID: versionID})
	case content.SourceModeRange:
		if request.Range == nil || request.Range.Offset < 0 || request.Range.Length <= 0 {
			return nil, fmt.Errorf("%w: invalid native Rclone content range", backupasset.ErrInvalidState)
		}
		return reader.OpenVersionRange(ctx, provider.RcloneNativeExactRangeRequest{
			PhysicalKey: physicalKey, VersionID: versionID,
			Offset: uint64(request.Range.Offset), Length: uint64(request.Range.Length),
		})
	case content.SourceModeStat:
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: unsupported native Rclone content source mode", backupasset.ErrInvalidState)
	}
}

func (service *Service) resolveManagedRcloneNativeContentBinding(
	ctx context.Context,
	record exactContentRecord,
) (provider.S3Native, string, string, provider.RcloneNativeExactObjectHead, error) {
	zero := provider.RcloneNativeExactObjectHead{}
	if service.publication == nil || service.publication.keyring == nil || record.point.ProducingTaskID == nil ||
		record.point.Semantics != string(backupasset.PointXirangManifest) {
		return nil, "", "", zero, fmt.Errorf("%w: native Rclone content dependencies unavailable", backupasset.ErrInvalidState)
	}
	manifest, _, err := service.activeCatalogManifest(ctx, record.point)
	if err != nil {
		return nil, "", "", zero, err
	}
	if record.generation.ManifestID == nil || *record.generation.ManifestID != manifest.ID {
		return nil, "", "", zero, fmt.Errorf("%w: native Rclone Catalog generation changed", backupasset.ErrConflict)
	}
	kind, lineage, consistency, err := publicationReconciliationFacts(record.point)
	if err != nil || kind != backupasset.ProviderRclone || lineage.TaskID != *record.point.ProducingTaskID ||
		consistency.Provider != backupasset.ProviderRclone {
		return nil, "", "", zero, fmt.Errorf("%w: native Rclone content lineage changed", backupasset.ErrConflict)
	}
	locator, err := decodeManagedRclonePointLocator(record.point.EncryptedProviderLocator)
	if err != nil {
		return nil, "", "", zero, err
	}
	attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	if err != nil {
		return nil, "", "", zero, err
	}
	commit, err := provider.DecodeRcloneCommitV1(locator.TaggedCommit)
	if err != nil || commit.Native == nil {
		return nil, "", "", zero, fmt.Errorf("%w: native Rclone content commit is invalid", backupasset.ErrConflict)
	}
	if err := validateRcloneDurableCapabilityEvidence(record.point, consistency, attempt, commit); err != nil {
		return nil, "", "", zero, err
	}
	commitDigest, err := canonicalRcloneProviderCommitDigest(commit)
	if err != nil || commitDigest != locator.ProviderCommitDigest || commitDigest != consistency.ProviderCommitDigest ||
		manifest.EncryptedCommitEvidence != locator.TaggedCommit || record.point.SourceFingerprint != locator.PhysicalIdentityDigest ||
		commit.ManifestIndexDigest != manifest.Digest || int64(commit.ManifestEntryCount) != manifest.EntryCount ||
		commit.RecoveryPointID != record.point.ID || commit.RepositoryID != record.repository.ID ||
		locator.PublicationMode != backupasset.PublicationNativeObjectVersions || attempt.Native == nil {
		return nil, "", "", zero, fmt.Errorf("%w: native Rclone content evidence changed", backupasset.ErrConflict)
	}
	runtime, err := service.publication.loadExactManagedRclonePublicationRuntime(ctx, lineage.TaskID)
	if err != nil {
		return nil, "", "", zero, err
	}
	if err := validateRcloneReconcileRuntime(runtime, record.point, attempt); err != nil {
		return nil, "", "", zero, err
	}
	markerKey, err := service.publication.rcloneMarkerKey(ctx, record.repository.ID)
	if err != nil {
		return nil, "", "", zero, err
	}
	exactCommitKey := managedRcloneNativeControlCommitKey(runtime.binding, attempt)
	owned, _, evidenceErr := loadManagedRcloneNativeVersionEvidenceTx(
		ctx, service.db, record.repository.ID, record.point.ID, markerKey, locator, exactCommitKey,
	)
	if evidenceErr != nil {
		return nil, "", "", zero, evidenceErr
	}
	exactCommitVersionID, evidenceErr := managedRcloneNativeCommitVersion(owned, exactCommitKey)
	if evidenceErr != nil {
		return nil, "", "", zero, evidenceErr
	}
	commit.Native.CommitKey = exactCommitKey
	commit.Native.CommitVersionID = exactCommitVersionID
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		return nil, "", "", zero, err
	}
	input, err := service.publication.rcloneReconcileInput(
		ctx, runtime, attempt, markerKey, leaseConfig, exactCommitKey, exactCommitVersionID,
	)
	if err != nil {
		return nil, "", "", zero, err
	}
	if input.NativeRequest == nil || input.NativeRequest.ClientFactory == nil {
		return nil, "", "", zero, fmt.Errorf("%w: native Rclone exact client unavailable", backupasset.ErrInvalidState)
	}
	physicalKey, versionID, err := decodeRcloneNativeContentEntryLocator(record.locator.Native, runtime.binding.Native.ManagedPrefix)
	if err != nil {
		return nil, "", "", zero, err
	}
	s3, err := input.NativeRequest.ClientFactory.S3(
		input.NativeRequest.Session, input.NativeRequest.Profile, input.NativeRequest.KMSKeyBindings,
	)
	if err != nil {
		return nil, "", "", zero, err
	}
	if s3 == nil {
		return nil, "", "", zero, capabilityError(backupasset.CapabilityProviderUnavailable, "")
	}
	head, err := s3.HeadVersion(ctx, provider.RcloneNativeExactReadRequest{PhysicalKey: physicalKey, VersionID: versionID})
	if err != nil {
		return nil, "", "", zero, err
	}
	if head.PhysicalKey != physicalKey || head.VersionID != versionID || head.Size != uint64(record.entry.Size) ||
		head.EncryptionProfile != input.NativeRequest.Encryption.Profile ||
		head.BucketKeyEnabled != input.NativeRequest.EncryptionEvidence.BucketKeyEnabled ||
		!validRcloneNativeContentKMSDigest(head, input.NativeRequest.Encryption.Profile, input.NativeRequest.KMSKeyBindings) {
		return nil, "", "", zero, fmt.Errorf("%w: native Rclone exact object changed", backupasset.ErrConflict)
	}
	return s3, physicalKey, versionID, head, nil
}

func decodeRcloneNativeContentEntryLocator(value, managedPrefix string) (string, string, error) {
	if !strings.HasPrefix(value, "native:") {
		return "", "", fmt.Errorf("%w: native Rclone entry locator required", backupasset.ErrConflict)
	}
	parts := strings.Split(strings.TrimPrefix(value, "native:"), "\x00")
	dataPrefix := strings.TrimSuffix(managedPrefix, "/") + "/data/"
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || !strings.HasPrefix(parts[0], dataPrefix) ||
		strings.ContainsAny(parts[0], "\r\n") || strings.ContainsAny(parts[1], "\r\n") {
		return "", "", fmt.Errorf("%w: invalid native Rclone entry locator", backupasset.ErrConflict)
	}
	return parts[0], parts[1], nil
}

func validRcloneNativeContentKMSDigest(
	head provider.RcloneNativeExactObjectHead,
	profile provider.RcloneNativeEncryptionProfileCode,
	bindings []provider.RcloneNativeKMSKeyDigestBinding,
) bool {
	if profile == provider.RcloneNativeSSES3V1 {
		return head.KMSKeyDigest == ""
	}
	if profile != provider.RcloneNativeSSEKMSV1 || head.KMSKeyDigest == "" {
		return false
	}
	for _, binding := range bindings {
		if binding.Digest == head.KMSKeyDigest {
			return true
		}
	}
	return false
}

func sameRcloneNativeContentHead(left, right provider.RcloneNativeExactObjectHead) bool {
	return left.PhysicalKey == right.PhysicalKey && left.VersionID == right.VersionID && left.Size == right.Size &&
		left.EncryptionProfile == right.EncryptionProfile && left.KMSKeyDigest == right.KMSKeyDigest &&
		left.BucketKeyEnabled == right.BucketKeyEnabled
}

func (service *Service) resolveManagedRclonePortableContentBinding(
	ctx context.Context,
	record exactContentRecord,
) (provider.ReadSnapshot, provider.PointLocator, exactContentRecord, bool, error) {
	if service.publication == nil || service.publication.keyring == nil || record.point.ProducingTaskID == nil ||
		record.point.Semantics != string(backupasset.PointXirangManifest) {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false,
			fmt.Errorf("%w: immutable Rclone content dependencies unavailable", backupasset.ErrInvalidState)
	}
	manifest, _, err := service.activeCatalogManifest(ctx, record.point)
	if err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false, err
	}
	if record.generation.ManifestID == nil || *record.generation.ManifestID != manifest.ID {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false,
			fmt.Errorf("%w: immutable Rclone Catalog generation changed", backupasset.ErrConflict)
	}
	kind, lineage, consistency, err := publicationReconciliationFacts(record.point)
	if err != nil || kind != backupasset.ProviderRclone || lineage.TaskID != *record.point.ProducingTaskID ||
		consistency.Provider != backupasset.ProviderRclone {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false,
			fmt.Errorf("%w: immutable Rclone content lineage changed", backupasset.ErrConflict)
	}
	locator, err := decodeManagedRclonePointLocator(record.point.EncryptedProviderLocator)
	if err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false, err
	}
	attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	if err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false, err
	}
	commit, err := provider.DecodeRcloneCommitV1(locator.TaggedCommit)
	if err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false, err
	}
	if err := validateRcloneDurableCapabilityEvidence(record.point, consistency, attempt, commit); err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false, err
	}
	commitDigest, err := canonicalRcloneProviderCommitDigest(commit)
	if err != nil || commitDigest != locator.ProviderCommitDigest || commitDigest != consistency.ProviderCommitDigest ||
		manifest.EncryptedCommitEvidence != locator.TaggedCommit || record.point.SourceFingerprint != locator.PhysicalIdentityDigest ||
		commit.ManifestIndexDigest != manifest.Digest || int64(commit.ManifestEntryCount) != manifest.EntryCount ||
		commit.RecoveryPointID != record.point.ID || commit.RepositoryID != record.repository.ID {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false,
			fmt.Errorf("%w: immutable Rclone content evidence changed", backupasset.ErrConflict)
	}
	if locator.PublicationMode != backupasset.PublicationVersionedPrefix || attempt.Portable == nil || commit.Portable == nil ||
		!strings.HasPrefix(record.locator.Native, "portable:") {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false,
			capabilityError(backupasset.CapabilitySequentialReadUnavailable, "")
	}
	runtime, err := service.publication.loadExactManagedRclonePublicationRuntime(ctx, lineage.TaskID)
	if err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false, err
	}
	if err := validateRcloneReconcileRuntime(runtime, record.point, attempt); err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false, err
	}
	if runtime.repository.ID != record.repository.ID || runtime.binding.Portable == nil ||
		runtime.binding.Portable.ConfigDigest != attempt.ConfigDigest {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false,
			fmt.Errorf("%w: immutable Rclone portable binding changed", backupasset.ErrConflict)
	}
	salt, err := hexDecodeSalt(runtime.binding.IdentitySalt)
	if err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false, err
	}
	legacy, err := decodeBindingDocument(runtime.binding.LegacyBindingV1)
	if err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false, err
	}
	dataLocator := strings.TrimSuffix(locator.PortableAttemptRoot, "/") + "/data"
	if _, err := provider.NewRclonePrivateLocator(dataLocator); err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false, err
	}
	capabilities, err := decodeRepositoryCapabilities(record.repository.CapabilitiesJSON)
	if err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false, err
	}
	managed := &provider.RcloneManagedPointAccess{
		RecoveryPointID: record.point.ID, AttemptID: attempt.AttemptID, DataLocator: dataLocator,
		ManifestDigest: record.point.ManifestDigest, SourceRevision: commit.DestinationObservationDigest, Committed: true,
	}
	access := provider.AccessBinding{
		Provider: backupasset.ProviderRclone, RepositoryID: record.repository.ID, TaskID: runtime.task.ID, NodeID: runtime.task.NodeID,
		IdentitySalt: append([]byte(nil), salt...), EndpointFacts: append([]string(nil), legacy.EndpointFacts...),
		Locator: dataLocator, Secret: []byte(runtime.binding.Portable.BoundConfig),
		AdapterData: provider.RcloneRuntimeAccess{
			Backend: runtime.binding.Portable.Backend, RangeProven: capabilities.OpenRange,
			ConfigSource: provider.RcloneConfigBound, Command: &provider.RemoteCommandAccess{Node: runtime.task.Node}, ManagedPoint: managed,
		},
	}
	physicalPath := strings.TrimPrefix(record.locator.Native, "portable:")
	if physicalPath == "" || strings.HasPrefix(physicalPath, "/") || strings.ContainsRune(physicalPath, '\x00') {
		return provider.ReadSnapshot{}, provider.PointLocator{}, exactContentRecord{}, false,
			fmt.Errorf("%w: invalid immutable Rclone portable entry locator", backupasset.ErrConflict)
	}
	record.locator = provider.EntryLocator{Native: physicalPath}
	return provider.ReadSnapshot{
		RepositoryID: record.repository.ID, CapabilityRevision: record.point.CapabilityRevision,
		SourceRevision: commit.DestinationObservationDigest, Access: access,
	}, provider.PointLocator{
		Native: "managed:" + record.point.ID + ":" + attempt.AttemptID + ":" + record.point.ManifestDigest,
	}, record, capabilities.OpenRange, nil
}

func (service *Service) openManagedRsyncContentSource(
	ctx context.Context,
	request content.SourceRequest,
	record exactContentRecord,
	token publication.AdmissionToken,
) (content.SourceSession, error) {
	semantics := backupasset.PointVersionSemantics(record.point.Semantics)
	if record.point.ProducingTaskID == nil ||
		(semantics != backupasset.PointXirangManifest && semantics != backupasset.PointImportedBaseline) {
		return nil, fmt.Errorf("%w: immutable Rsync content dependencies unavailable", backupasset.ErrInvalidState)
	}
	manifest, _, err := service.activeCatalogManifest(ctx, record.point)
	if err != nil {
		return nil, err
	}
	if record.generation.ManifestID == nil || *record.generation.ManifestID != manifest.ID {
		return nil, fmt.Errorf("%w: immutable Rsync Catalog generation changed", backupasset.ErrConflict)
	}
	session, err := service.beginManagedRsyncPointReadWithAdmission(ctx, *record.point.ProducingTaskID, record.point.ID, token)
	if err != nil {
		return nil, err
	}
	return service.openManagedRsyncContentSession(ctx, request, record, session)
}

func (service *Service) openManagedRsyncContentSession(
	ctx context.Context,
	request content.SourceRequest,
	record exactContentRecord,
	session *ManagedRsyncPointReadSession,
) (content.SourceSession, error) {
	if session == nil {
		return nil, fmt.Errorf("%w: immutable Rsync content session unavailable", backupasset.ErrInvalidState)
	}
	keepSession := false
	defer func() {
		if !keepSession {
			_ = session.Close()
		}
	}()
	entryStat, err := session.StatEntry(ctx, record.locator)
	if err != nil {
		return nil, err
	}
	if err := validateContentEntryStat(record, entryStat, false); err != nil {
		return nil, err
	}
	var reader provider.ReadHandle
	contentStat := provider.ContentStat{
		Size: entryStat.Size, ModTime: entryStat.ModTime, SourceRevision: entryStat.SourceRevision,
		MediaType: record.entry.MimeType,
	}
	switch request.Mode {
	case content.SourceModeSequential:
		reader, contentStat, err = session.OpenSequential(ctx, record.locator, provider.ReadRequest{MaxBytes: request.MaxBytes})
	case content.SourceModeRange:
		reader, contentStat, err = session.OpenRange(ctx, record.locator, provider.ByteRange{Offset: request.Range.Offset, Length: request.Range.Length})
	case content.SourceModeStat:
	default:
		err = fmt.Errorf("%w: unsupported content source mode", backupasset.ErrInvalidState)
	}
	if err != nil {
		return nil, err
	}
	if err := validateContentOpenStat(record, entryStat, contentStat, false); err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, err
	}
	modifiedAt := contentStat.ModTime.UTC()
	if contentStat.ModTime.IsZero() {
		modifiedAt = time.Time{}
	}
	stat := content.SourceStat{
		Size: record.entry.Size, MediaType: firstNonEmpty(contentStat.MediaType, record.entry.MimeType),
		SourceFingerprint: request.ExpectedSource, EntryFingerprint: request.ExpectedEntry,
		FingerprintStrong: record.entry.FingerprintStrength == string(catalog.FingerprintStrong),
	}
	if !modifiedAt.IsZero() {
		stat.ModifiedAt = &modifiedAt
	}
	result := &sealedContentSourceSession{
		stat: stat,
		capabilities: content.SourceCapabilities{
			Provider: backupasset.ProviderRsync, Sequential: true, Range: true,
		},
		reader:        newOnceReadCloser(reader, request.Mode == content.SourceModeSequential),
		cleanupParent: ctx, cleanupTimeout: contentSourceCleanupTimeout,
		revalidate: func(validateCtx context.Context) error {
			current, loadErr := service.loadExactContentRecord(validateCtx, request)
			if loadErr != nil || current.point.State != string(backupasset.RecoveryPointCommitted) && current.point.State != string(backupasset.RecoveryPointDegraded) {
				return fmt.Errorf("%w: immutable Rsync content source changed", backupasset.ErrConflict)
			}
			currentStat, statErr := session.StatEntry(validateCtx, current.locator)
			if statErr != nil {
				return fmt.Errorf("%w: immutable Rsync content source changed", backupasset.ErrConflict)
			}
			if err := validateContentEntryStat(current, currentStat, false); err != nil {
				return err
			}
			return validateProviderEntryStable(entryStat, currentStat, false)
		},
		release: session.Close,
	}
	keepSession = true
	return result, nil
}

func (service *Service) resolveResticContentBinding(
	ctx context.Context,
	record exactContentRecord,
) (provider.ReadSnapshot, provider.PointLocator, error) {
	if service.publication == nil || record.point.Semantics != string(backupasset.PointNativeSnapshot) ||
		record.point.ProducingTaskID == nil || record.point.ProducingTaskRunID == nil || record.repository.RepositoryIdentity == nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, fmt.Errorf("%w: immutable Restic content dependencies unavailable", backupasset.ErrInvalidState)
	}
	manifest, _, err := service.activeCatalogManifest(ctx, record.point)
	if err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, err
	}
	if record.generation.ManifestID == nil || *record.generation.ManifestID != manifest.ID {
		return provider.ReadSnapshot{}, provider.PointLocator{}, fmt.Errorf("%w: immutable Restic Catalog generation changed", backupasset.ErrConflict)
	}
	lineage, err := backupasset.DecodePublicationLineage(record.point.LineageJSON)
	if err != nil || lineage.TaskID != *record.point.ProducingTaskID || lineage.TaskRunID != *record.point.ProducingTaskRunID ||
		lineage.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
		return provider.ReadSnapshot{}, provider.PointLocator{}, fmt.Errorf("%w: immutable Restic content lineage changed", backupasset.ErrConflict)
	}
	runtime, link, err := service.publication.loadExactPublicationRuntime(ctx, lineage.TaskID, backupasset.PublicationAuditContext{})
	if err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, err
	}
	if runtime.repository.ID != record.repository.ID || link.ID != lineage.TaskRepositoryLinkID {
		return provider.ReadSnapshot{}, provider.PointLocator{}, fmt.Errorf("%w: immutable Restic content binding changed", backupasset.ErrConflict)
	}
	locator, err := decodeResticPointLocator(record.point.EncryptedProviderLocator)
	if err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, err
	}
	if record.point.SourceFingerprint != resticSourceFingerprint(*record.repository.RepositoryIdentity, locator.FullSnapshotID) {
		return provider.ReadSnapshot{}, provider.PointLocator{}, fmt.Errorf("%w: immutable Restic source changed", backupasset.ErrConflict)
	}
	if err := service.proveResticReadIdentity(ctx, record.repository.ID, *record.repository.RepositoryIdentity, record.point.CapabilityRevision, runtime); err != nil {
		return provider.ReadSnapshot{}, provider.PointLocator{}, err
	}
	return provider.ReadSnapshot{
		RepositoryID: record.repository.ID, CapabilityRevision: record.point.CapabilityRevision,
		SourceRevision: record.point.SourceFingerprint, Access: runtime.access,
	}, provider.PointLocator{Native: locator.FullSnapshotID}, nil
}

type contentPointResolver func(context.Context) (provider.PointLocator, error)

func (service *Service) openResolvedProviderContentSource(
	ctx context.Context,
	request content.SourceRequest,
	record exactContentRecord,
	kind backupasset.ProviderKind,
	snapshot provider.ReadSnapshot,
	mutable bool,
	rangeAllowed bool,
	token publication.AdmissionToken,
	resolvePoint contentPointResolver,
	revalidate func(context.Context) error,
) (content.SourceSession, error) {
	statter, err := service.registry.EntryStatter(kind)
	if err != nil {
		return nil, err
	}
	sequential, sequentialErr := service.registry.SequentialReader(kind)
	rangeReader, rangeErr := service.registry.RangeReader(kind)
	if !rangeAllowed {
		rangeReader = nil
		rangeErr = capabilityError(backupasset.CapabilityRangeUnavailable, "")
	}
	if request.Mode == content.SourceModeSequential && sequentialErr != nil {
		return nil, sequentialErr
	}
	if request.Mode == content.SourceModeRange && rangeErr != nil {
		return nil, rangeErr
	}
	point, err := resolvePoint(ctx)
	if err != nil {
		return nil, err
	}
	entryStat, err := statter.StatEntry(ctx, snapshot, point, record.locator)
	if err != nil {
		return nil, err
	}
	if err := validateContentEntryStat(record, entryStat, mutable); err != nil {
		return nil, err
	}

	var reader provider.ReadHandle
	contentStat := provider.ContentStat{
		Size: entryStat.Size, ModTime: entryStat.ModTime, SourceRevision: entryStat.SourceRevision,
		MediaType: record.entry.MimeType,
	}
	switch request.Mode {
	case content.SourceModeSequential:
		reader, contentStat, err = sequential.OpenSequential(ctx, snapshot, point, record.locator, provider.ReadRequest{MaxBytes: request.MaxBytes})
	case content.SourceModeRange:
		reader, contentStat, err = rangeReader.OpenRange(ctx, snapshot, point, record.locator, provider.ByteRange{Offset: request.Range.Offset, Length: request.Range.Length})
	case content.SourceModeStat:
	default:
		err = fmt.Errorf("%w: unsupported content source mode", backupasset.ErrInvalidState)
	}
	if err != nil {
		return nil, err
	}
	if err := validateContentOpenStat(record, entryStat, contentStat, mutable); err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, err
	}
	modifiedAt := contentStat.ModTime.UTC()
	if contentStat.ModTime.IsZero() {
		modifiedAt = time.Time{}
	}
	stat := content.SourceStat{
		Size: record.entry.Size, MediaType: firstNonEmpty(contentStat.MediaType, record.entry.MimeType),
		SourceFingerprint: request.ExpectedSource, EntryFingerprint: request.ExpectedEntry,
		FingerprintStrong: record.entry.FingerprintStrength == string(catalog.FingerprintStrong),
	}
	if !modifiedAt.IsZero() {
		stat.ModifiedAt = &modifiedAt
	}
	capabilities := content.SourceCapabilities{Provider: kind, Sequential: sequentialErr == nil, Range: rangeErr == nil}
	if sequentialErr != nil {
		reason := backupasset.CapabilityReason{Code: backupasset.CapabilitySequentialReadUnavailable}
		capabilities.Reason = &reason
	} else if rangeErr != nil {
		reason := backupasset.CapabilityReason{Code: backupasset.CapabilityRangeUnavailable}
		capabilities.Reason = &reason
	}
	providerRevalidate := func(validateCtx context.Context) error {
		if revalidate != nil {
			if err := revalidate(validateCtx); err != nil {
				return err
			}
		}
		currentPoint, err := resolvePoint(validateCtx)
		if err != nil {
			return err
		}
		current, err := statter.StatEntry(validateCtx, snapshot, currentPoint, record.locator)
		if err != nil {
			return contentSourceChangedError(mutable)
		}
		return validateProviderEntryStable(entryStat, current, mutable)
	}
	session := &sealedContentSourceSession{
		stat: stat, capabilities: capabilities,
		reader: newOnceReadCloser(reader, requiresProviderByteReporter(kind, request.Mode)), token: token,
		cleanupParent: ctx, cleanupTimeout: contentSourceCleanupTimeout,
		revalidate: providerRevalidate,
	}
	return session, nil
}

func resolveMutableContentPoint(ctx context.Context, lister provider.PointLister, snapshot provider.ReadSnapshot) (provider.PointLocator, error) {
	points, err := lister.ListPoints(ctx, snapshot, provider.PageRequest{Limit: 2})
	if err != nil {
		return provider.PointLocator{}, err
	}
	if len(points.Items) != 1 || points.NextCursor != "" || points.Items[0].Semantics != backupasset.PointMutableHead ||
		points.Items[0].SourceRevision != snapshot.SourceRevision || points.Items[0].Locator.Native == "" {
		return provider.PointLocator{}, capabilityError(backupasset.CapabilityMutableSourceChanged, "")
	}
	return points.Items[0].Locator, nil
}

func validateContentEntryStat(record exactContentRecord, stat provider.Entry, mutable bool) error {
	if stat.Type != backupasset.CatalogEntryFile || stat.Size != record.entry.Size || strings.TrimSpace(stat.SourceRevision) == "" {
		return contentSourceChangedError(mutable)
	}
	if record.entry.ModifiedAt != nil && !record.entry.ModifiedAt.IsZero() && !stat.ModTime.UTC().Equal(record.entry.ModifiedAt.UTC()) {
		return contentSourceChangedError(mutable)
	}
	return nil
}

func validateContentOpenStat(record exactContentRecord, expected provider.Entry, stat provider.ContentStat, mutable bool) error {
	if stat.Size != record.entry.Size || stat.SourceRevision != expected.SourceRevision {
		return contentSourceChangedError(mutable)
	}
	if record.entry.ModifiedAt != nil && !record.entry.ModifiedAt.IsZero() && !stat.ModTime.UTC().Equal(record.entry.ModifiedAt.UTC()) {
		return contentSourceChangedError(mutable)
	}
	return nil
}

func validateProviderEntryStable(expected, current provider.Entry, mutable bool) error {
	if current.Type != expected.Type || current.Size != expected.Size || current.SourceRevision != expected.SourceRevision ||
		!current.ModTime.UTC().Equal(expected.ModTime.UTC()) {
		return contentSourceChangedError(mutable)
	}
	return nil
}

func contentSourceChangedError(mutable bool) error {
	if mutable {
		return capabilityError(backupasset.CapabilityMutableSourceChanged, "")
	}
	return fmt.Errorf("%w: immutable content source changed", backupasset.ErrConflict)
}

func (service *Service) revalidateMutableContentSource(
	ctx context.Context,
	request content.SourceRequest,
	repositoryID string,
	kind backupasset.ProviderKind,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	record, err := service.loadExactContentRecord(ctx, request)
	if err != nil || record.repository.ID != repositoryID || record.point.Semantics != string(backupasset.PointMutableHead) ||
		record.point.State != string(backupasset.RecoveryPointObserved) {
		return capabilityError(backupasset.CapabilityMutableSourceChanged, "")
	}
	runtime, err := service.loadRepositoryRuntime(ctx, repositoryID)
	if err != nil || runtime.access.Provider != kind || runtime.repository.CapabilityRevision != record.point.CapabilityRevision {
		return capabilityError(backupasset.CapabilityMutableSourceChanged, "")
	}
	lister, err := service.registry.PointLister(kind)
	if err != nil {
		return err
	}
	_, err = resolveMutableContentPoint(ctx, lister, provider.ReadSnapshot{
		RepositoryID: repositoryID, CapabilityRevision: record.point.CapabilityRevision,
		SourceRevision: request.ExpectedSource, Access: runtime.access,
	})
	return err
}

type onceReadCloser struct {
	inner             io.ReadCloser
	once              sync.Once
	err               error
	mu                sync.Mutex
	visibleBytes      int64
	requireReporter   bool
	accountingInvalid bool
	providerObserved  bool
	lastProviderBytes int64
}

func newOnceReadCloser(inner io.ReadCloser, requireReporter bool) *onceReadCloser {
	if inner == nil {
		return nil
	}
	return &onceReadCloser{inner: inner, requireReporter: requireReporter}
}

func (reader *onceReadCloser) Read(buffer []byte) (int, error) {
	if reader == nil || reader.inner == nil {
		return 0, io.EOF
	}
	count, err := reader.inner.Read(buffer)
	reader.mu.Lock()
	if count < 0 || int64(count) > math.MaxInt64-reader.visibleBytes {
		reader.accountingInvalid = true
		if err == nil {
			err = fmt.Errorf("%w: source byte counter overflow", backupasset.ErrInvalidState)
		}
	} else {
		reader.visibleBytes += int64(count)
	}
	reader.mu.Unlock()
	return count, err
}

func (reader *onceReadCloser) Close() error {
	return reader.finish(false)
}

func (reader *onceReadCloser) ClosePrefix() error {
	return reader.finish(true)
}

func (reader *onceReadCloser) finish(prefix bool) error {
	if reader == nil {
		return nil
	}
	reader.once.Do(func() {
		if prefixCloser, ok := reader.inner.(interface{ ClosePrefix() error }); ok && prefix {
			reader.err = prefixCloser.ClosePrefix()
			return
		}
		reader.err = reader.inner.Close()
	})
	return reader.err
}

func (reader *onceReadCloser) ProviderBytes() int64 {
	if reader == nil || reader.inner == nil {
		return -1
	}
	reporter, ok := reader.inner.(provider.ProviderByteReporter)
	if !ok {
		reader.mu.Lock()
		defer reader.mu.Unlock()
		if reader.accountingInvalid || reader.requireReporter {
			return -1
		}
		return reader.visibleBytes
	}
	reported := reporter.ProviderBytes()
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.accountingInvalid || reported < 0 || reported < reader.visibleBytes ||
		(reader.providerObserved && reported < reader.lastProviderBytes) {
		reader.accountingInvalid = true
		return -1
	}
	reader.providerObserved = true
	reader.lastProviderBytes = reported
	return reported
}

func requiresProviderByteReporter(kind backupasset.ProviderKind, mode content.SourceMode) bool {
	return mode == content.SourceModeSequential || kind == backupasset.ProviderRclone && mode == content.SourceModeRange
}

type sealedContentSourceSession struct {
	stat           content.SourceStat
	capabilities   content.SourceCapabilities
	reader         *onceReadCloser
	token          publication.AdmissionToken
	revalidate     func(context.Context) error
	release        func() error
	cleanupParent  context.Context
	cleanupTimeout time.Duration
	closeOnce      sync.Once
	closeErr       error
}

func (session *sealedContentSourceSession) Stat() content.SourceStat {
	if session == nil {
		return content.SourceStat{}
	}
	return session.stat
}

func (session *sealedContentSourceSession) Capabilities() content.SourceCapabilities {
	if session == nil {
		return content.SourceCapabilities{}
	}
	return session.capabilities
}

func (session *sealedContentSourceSession) Reader() content.SourceReader {
	if session == nil {
		return nil
	}
	return session.reader
}

func (session *sealedContentSourceSession) Revalidate(ctx context.Context) error {
	if session == nil || session.revalidate == nil {
		return fmt.Errorf("%w: content source session unavailable", backupasset.ErrInvalidState)
	}
	return session.revalidate(ctx)
}

func (session *sealedContentSourceSession) Close() error {
	return session.finish(false)
}

func (session *sealedContentSourceSession) ClosePrefix() error {
	return session.finish(true)
}

func (session *sealedContentSourceSession) finish(prefix bool) error {
	if session == nil {
		return nil
	}
	session.closeOnce.Do(func() {
		var readerErr error
		if session.reader != nil {
			if prefix {
				readerErr = session.reader.ClosePrefix()
			} else {
				readerErr = session.reader.Close()
			}
		}
		var revalidateErr error
		if session.revalidate != nil {
			cleanupCtx, cleanupCancel := session.cleanupContext()
			revalidateErr = session.revalidate(cleanupCtx)
			cleanupCancel()
		}
		var releaseErr error
		if session.release != nil {
			releaseErr = session.release()
		}
		var tokenErr error
		if session.token != nil {
			tokenErr = session.token.Close()
		}
		// Revalidation/source drift is authoritative even though the reader must
		// be closed first to join Provider invariants and transports.
		session.closeErr = errors.Join(revalidateErr, readerErr, releaseErr, tokenErr)
	})
	return session.closeErr
}

func (session *sealedContentSourceSession) cleanupContext() (context.Context, context.CancelFunc) {
	parent := session.cleanupParent
	if parent == nil {
		parent = context.Background()
	}
	timeout := session.cleanupTimeout
	if timeout <= 0 || timeout > contentSourceCleanupTimeout {
		timeout = contentSourceCleanupTimeout
	}
	deadline := time.Now().Add(timeout)
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	return context.WithDeadline(context.WithoutCancel(parent), deadline)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
