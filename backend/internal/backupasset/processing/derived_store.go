package processing

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrDerivedStoreUnavailable = errors.New("derived store unavailable")
	ErrDerivedQuotaExceeded    = errors.New("derived store quota exceeded")
	ErrDerivedBlobUnavailable  = errors.New("derived blob unavailable")
)

type DerivedStoreConfig struct {
	Root           string
	ChunkSize      int64
	BlobMaxBytes   int64
	GlobalMaxBytes int64
	Random         io.Reader
	ValidateRoot   func(context.Context, string) error
}

type DerivedBlobDeclaration struct {
	PlaintextSize   int64
	PlaintextDigest string
}

type DerivedBlobHandle struct {
	BlobID          string
	OpaqueLocator   string
	PlaintextSize   int64
	PlaintextDigest string
	PhysicalSize    int64
	Reused          bool
}

type DerivedStore struct {
	db         *gorm.DB
	keyring    *backupasset.Keyring
	cipher     *DerivedCipher
	now        func() time.Time
	config     DerivedStoreConfig
	removeFile func(string) error
}

func NewDerivedStore(ctx context.Context, db *gorm.DB, keyring *backupasset.Keyring, config DerivedStoreConfig, now func() time.Time) (*DerivedStore, error) {
	if db == nil || keyring == nil || config.ValidateRoot == nil || !validDerivedStoreConfig(config) {
		return nil, ErrDerivedStoreUnavailable
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	if err := config.ValidateRoot(ctx, config.Root); err != nil {
		return nil, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	if err := prepareDerivedRoot(config.Root); err != nil {
		return nil, err
	}
	cipher, err := NewDerivedCipher(config.Random)
	if err != nil {
		return nil, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	return &DerivedStore{db: db, keyring: keyring, cipher: cipher, now: now, config: config, removeFile: os.Remove}, nil
}

func (store *DerivedStore) PutBlob(ctx context.Context, declaration DerivedBlobDeclaration, plaintext io.Reader) (DerivedBlobHandle, error) {
	if store == nil || plaintext == nil || !validDerivedBlobDeclaration(declaration, store.config.BlobMaxBytes) {
		return DerivedBlobHandle{}, ErrDerivedInvalid
	}
	replay, err := store.newReplay(ctx, declaration, plaintext)
	if err != nil {
		return DerivedBlobHandle{}, err
	}
	defer replay.Close()
	return store.putReplay(ctx, declaration, replay)
}

func (store *DerivedStore) putReplay(ctx context.Context, declaration DerivedBlobDeclaration, replay *derivedReplay) (DerivedBlobHandle, error) {
	estimate, err := derivedPhysicalSizeEstimate(declaration.PlaintextSize, store.config.ChunkSize)
	if err != nil {
		return DerivedBlobHandle{}, err
	}
	for attempt := range 20 {
		if err := ctx.Err(); err != nil {
			return DerivedBlobHandle{}, err
		}
		var existing model.BackupAssetDerivedBlob
		result := store.db.WithContext(ctx).Where("plaintext_digest = ? AND plaintext_size = ? AND cipher_format_version = ? AND chunk_size = ?", declaration.PlaintextDigest, declaration.PlaintextSize, derivedCipherFormatVersion, store.config.ChunkSize).Limit(1).Find(&existing)
		if result.Error != nil {
			if !retryableCoordinatorConflict(result.Error) {
				return DerivedBlobHandle{}, result.Error
			}
		} else if result.RowsAffected == 1 && existing.State == "active" {
			row, found, err := store.findReusableBlob(ctx, declaration)
			if err != nil && !errors.Is(err, ErrDerivedBlobUnavailable) && !errors.Is(err, os.ErrNotExist) {
				return DerivedBlobHandle{}, err
			}
			if found {
				return blobHandle(row, true), nil
			}
		} else if result.RowsAffected == 0 || existing.State == "unavailable" || existing.State == "purging" || existing.State == "purge_failed" {
			if err := store.admitQuota(ctx, estimate); err != nil {
				var count int64
				check := store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).Where("plaintext_digest = ? AND plaintext_size = ? AND cipher_format_version = ? AND chunk_size = ? AND state IN ?", declaration.PlaintextDigest, declaration.PlaintextSize, derivedCipherFormatVersion, store.config.ChunkSize, []string{"active", "staged"}).Count(&count)
				if check.Error != nil {
					return DerivedBlobHandle{}, check.Error
				}
				if count == 0 {
					return DerivedBlobHandle{}, err
				}
			} else {
				row, conflict, err := store.writeDerivedBlob(ctx, declaration, replay, existing, result.RowsAffected == 1, estimate)
				if err != nil && !conflict {
					return DerivedBlobHandle{}, err
				}
				if err == nil && !conflict {
					return blobHandle(row, false), nil
				}
			}
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return DerivedBlobHandle{}, ctx.Err()
		case <-timer.C:
		}
	}
	return DerivedBlobHandle{}, ErrDerivedStoreUnavailable
}

func (store *DerivedStore) writeDerivedBlob(ctx context.Context, declaration DerivedBlobDeclaration, replay *derivedReplay, existing model.BackupAssetDerivedBlob, reactivating bool, estimate int64) (model.BackupAssetDerivedBlob, bool, error) {
	var row model.BackupAssetDerivedBlob
	blobID := existing.ID
	if !reactivating {
		blobID = replay.spec.BlobID
	}
	if err := replay.rebind(ctx, blobID); err != nil {
		return row, false, err
	}
	locator, stagingName := blobID+".xrd", ".staging-"+blobID
	if reactivating {
		incarnation, err := backupasset.NewOpaqueID()
		if err != nil {
			return row, false, err
		}
		locator, stagingName = blobID+"-"+incarnation+".xrd", ".staging-"+blobID+"-"+incarnation
	}
	stagingPath, finalPath := filepath.Join(store.config.Root, stagingName), filepath.Join(store.config.Root, locator)
	writingPath := filepath.Join(store.config.Root, ".writing-"+strings.TrimPrefix(stagingName, ".staging-"))
	if err := os.Link(replay.file.Name(), writingPath); err != nil {
		return row, false, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	replay.installed = true
	defer func() { _ = os.Remove(writingPath) }()
	metadata := replay.metadata
	if metadata.PhysicalSize > store.config.GlobalMaxBytes || metadata.PhysicalSize > estimate {
		return row, false, ErrDerivedQuotaExceeded
	}
	now := store.utcNow()
	row = model.BackupAssetDerivedBlob{ID: blobID, PlaintextDigest: declaration.PlaintextDigest, PlaintextSize: declaration.PlaintextSize, PhysicalSize: metadata.PhysicalSize, CipherFormatVersion: metadata.CipherFormatVersion, ChunkSize: metadata.ChunkSize, ChunkCount: metadata.ChunkCount, NoncePrefix: metadata.NoncePrefix, OpaqueLocator: locator, WrappedDEK: metadata.WrappedDEK, EnvelopeNonce: metadata.EnvelopeNonce, DerivedKEKVersion: metadata.KEKVersion, State: "staged", RefCount: 0, CreatedAt: now, UpdatedAt: now}
	if reactivating {
		updates := map[string]any{"physical_size": metadata.PhysicalSize, "cipher_format_version": metadata.CipherFormatVersion, "chunk_size": metadata.ChunkSize, "chunk_count": metadata.ChunkCount, "nonce_prefix": metadata.NoncePrefix, "opaque_locator": locator, "wrapped_dek": metadata.WrappedDEK, "envelope_nonce": metadata.EnvelopeNonce, "derived_kek_version": metadata.KEKVersion, "state": "staged", "ref_count": 0, "unavailable_at": nil, "updated_at": now}
		updated := store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).Where("id = ? AND state IN ?", blobID, []string{"unavailable", "purging", "purge_failed"}).Updates(updates)
		if updated.Error != nil || updated.RowsAffected != 1 {
			if updated.Error == nil || retryableCoordinatorConflict(updated.Error) {
				return row, true, updated.Error
			}
			return row, false, errors.Join(ErrDerivedStoreUnavailable, updated.Error)
		}
	} else if err := store.db.WithContext(ctx).Create(&row).Error; err != nil {
		return row, retryableCoordinatorConflict(err), err
	}
	if err := os.Rename(writingPath, stagingPath); err != nil {
		return row, false, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	if _, err := os.Lstat(finalPath); !errors.Is(err, os.ErrNotExist) {
		return row, false, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	if err := os.Rename(stagingPath, finalPath); err != nil {
		return row, false, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	if err := syncDerivedDirectory(store.config.Root); err != nil {
		return row, false, err
	}
	updated := store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).Where("id = ? AND state = ? AND opaque_locator = ?", blobID, "staged", locator).Updates(map[string]any{"state": "active", "updated_at": now})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return row, false, errors.Join(ErrDerivedStoreUnavailable, updated.Error)
	}
	row.State = "active"
	return row, false, nil
}

func (store *DerivedStore) readBlob(ctx context.Context, blobID string, destination io.Writer) error {
	if store == nil || backupasset.ValidateOpaqueID(blobID) != nil || destination == nil {
		return ErrDerivedBlobUnavailable
	}
	var row model.BackupAssetDerivedBlob
	result := store.db.WithContext(ctx).Where("id = ? AND state = ?", blobID, "active").Limit(1).Find(&row)
	if result.Error != nil {
		return fmt.Errorf("load Derived blob: %w", result.Error)
	}
	if result.RowsAffected != 1 || len(row.WrappedDEK) == 0 || !safeOpaqueLocator(row.OpaqueLocator) {
		return ErrDerivedBlobUnavailable
	}
	path := filepath.Join(store.config.Root, row.OpaqueLocator)
	file, err := openDerivedRegularFile(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	key, err := store.keyring.ByVersion(ctx, backupasset.KeyDomainDerivedStore, row.DerivedKEKVersion)
	if err != nil {
		return errors.Join(ErrDerivedBlobUnavailable, err)
	}
	defer zeroBytesLocal(key.Key)
	metadata := derivedMetadataFromRow(row)
	if err := store.cipher.Decrypt(destination, file, DerivedCryptoSpec{
		BlobID: row.ID, PlaintextDigest: row.PlaintextDigest, PlaintextSize: row.PlaintextSize,
		ChunkSize: row.ChunkSize, KEKVersion: row.DerivedKEKVersion, KEK: key.Key,
	}, metadata); err != nil {
		return err
	}
	return nil
}

func (store *DerivedStore) RewrapBatch(ctx context.Context, oldVersion, newVersion, limit int) (int, error) {
	if store == nil || oldVersion <= 0 || newVersion <= 0 || oldVersion == newVersion || limit <= 0 || limit > 10000 {
		return 0, ErrDerivedInvalid
	}
	oldKey, err := store.keyring.ByVersion(ctx, backupasset.KeyDomainDerivedStore, oldVersion)
	if err != nil {
		return 0, err
	}
	defer zeroBytesLocal(oldKey.Key)
	newKey, err := store.keyring.ByVersion(ctx, backupasset.KeyDomainDerivedStore, newVersion)
	if err != nil || newKey.State != backupasset.DomainKeyActive {
		return 0, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	defer zeroBytesLocal(newKey.Key)
	var rows []model.BackupAssetDerivedBlob
	if err := store.db.WithContext(ctx).Where("derived_kek_version = ? AND state IN ?", oldVersion, []string{"staged", "active"}).
		Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("load Derived blobs for rewrap: %w", err)
	}
	count := 0
	for _, row := range rows {
		oldSpec := DerivedCryptoSpec{
			BlobID: row.ID, PlaintextDigest: row.PlaintextDigest, PlaintextSize: row.PlaintextSize,
			ChunkSize: row.ChunkSize, KEKVersion: oldVersion, KEK: oldKey.Key,
		}
		newSpec := oldSpec
		newSpec.KEKVersion = newVersion
		newSpec.KEK = newKey.Key
		rewrapped, err := store.cipher.RewrapDEK(derivedMetadataFromRow(row), oldSpec, newSpec)
		if err != nil {
			return count, err
		}
		updated := store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
			Where("id = ? AND opaque_locator = ? AND wrapped_dek = ? AND envelope_nonce = ? AND derived_kek_version = ? AND state IN ?", row.ID, row.OpaqueLocator, row.WrappedDEK, row.EnvelopeNonce, oldVersion, []string{"staged", "active"}).
			Updates(map[string]any{
				"wrapped_dek": rewrapped.WrappedDEK, "envelope_nonce": rewrapped.EnvelopeNonce,
				"derived_kek_version": newVersion, "updated_at": store.utcNow(),
			})
		if updated.Error != nil {
			return count, fmt.Errorf("persist Derived DEK rewrap: %w", updated.Error)
		}
		if updated.RowsAffected == 1 {
			count++
		}
	}
	return count, nil
}

func (store *DerivedStore) discardBlobIfUnreferenced(ctx context.Context, blobID string) error {
	return store.discardBlobIfUnreferencedObserved(ctx, blobID, "")
}

func (store *DerivedStore) discardBlobIfUnreferencedObserved(ctx context.Context, blobID, expectedLocator string) error {
	if store == nil || backupasset.ValidateOpaqueID(blobID) != nil {
		return ErrDerivedBlobUnavailable
	}
	var locator string
	err := derivedCleanupTransaction(ctx, store.db, func(tx *gorm.DB) error {
		locator = ""
		blobIDs := []string{blobID}
		jobs, err := lockDerivedHolderJobsTx(tx, blobIDs)
		if err != nil {
			return err
		}
		if expectedLocator == "" {
			if err := orphanDerivedHoldersTx(tx, blobIDs, jobs, store.utcNow()); err != nil {
				return err
			}
		}
		var blob model.BackupAssetDerivedBlob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND state IN ?", blobID, []string{"staged", "active"}).Limit(1).Find(&blob)
		if result.Error != nil || result.RowsAffected != 1 {
			return result.Error
		}
		if expectedLocator != "" && blob.OpaqueLocator != expectedLocator {
			return nil
		}
		if err := checkDerivedHolderCensusTx(tx, blobIDs, jobs); err != nil {
			return err
		}
		var references int64
		if err := tx.Model(&model.BackupAssetDerivedBlobReference{}).Where("blob_id = ? AND state = ?", blobID, "active").Count(&references).Error; err != nil {
			return err
		}
		uploads, err := liveStagedUploadCountTx(tx, blobID, store.utcNow())
		if err != nil {
			return err
		}
		if references > 0 || uploads > 0 {
			return tx.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", blob.ID).Update("ref_count", references).Error
		}
		now := store.utcNow()
		updated := tx.Model(&model.BackupAssetDerivedBlob{}).Where("id = ? AND state IN ?", blob.ID, []string{"staged", "active"}).
			Updates(map[string]any{"state": "unavailable", "wrapped_dek": []byte{}, "ref_count": 0, "unavailable_at": now, "updated_at": now})
		if updated.Error != nil || updated.RowsAffected != 1 {
			return errors.Join(ErrDerivedBlobUnavailable, updated.Error)
		}
		locator = blob.OpaqueLocator
		return nil
	})
	if err != nil || locator == "" {
		return err
	}
	if !safeOpaqueLocator(locator) {
		return ErrDerivedBlobUnavailable
	}
	if err := store.removeFile(filepath.Join(store.config.Root, locator)); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).Where("id = ? AND state = ? AND opaque_locator = ?", blobID, "unavailable", locator).Update("state", "purge_failed").Error
		return errors.Join(ErrDerivedBlobUnavailable, err)
	}
	return nil
}

// The holder proof is read-only so callers can use it while holding blob locks.
func liveStagedUploadCountTx(tx *gorm.DB, blobID string, now time.Time) (int64, error) {
	var holders []struct {
		FenceToken string
		FenceHash  string
	}
	err := tx.Table("backup_asset_processing_uploads AS u").
		Select("l.fence_token, a.recovery_point_fence_hash AS fence_hash").
		Joins("JOIN backup_asset_processing_jobs AS j ON j.id = u.job_id").
		Joins("JOIN backup_asset_processing_attempts AS a ON a.id = u.attempt_id AND a.job_id = j.id").
		Joins("JOIN backup_asset_processing_grants AS g ON g.id = u.grant_id AND g.job_id = j.id AND g.attempt_id = a.id AND g.worker_id = a.worker_id").
		Joins("JOIN recovery_point_leases AS l ON l.id = a.recovery_point_lease_id AND l.recovery_point_id = j.recovery_point_id AND l.owner_id = j.id AND l.attempt_id = a.recovery_point_attempt_id").
		Where(`u.staging_id = ? AND u.state = ? AND j.is_current = ? AND j.current_attempt_id = a.id AND j.absolute_deadline > ? AND a.state = ? AND a.is_current = ? AND a.worker_lease_expires_at > ? AND a.absolute_deadline > ? AND g.kind = ? AND g.state = ? AND g.expires_at > ? AND g.fence_hash = a.recovery_point_fence_hash AND l.holder_type = ? AND l.status = ? AND l.lease_expires_at > ? AND l.absolute_deadline > ?`, blobID, "staged", true, now, "active", true, now, now, GrantSink, GrantActive, now, backupasset.LeaseHolderProcessingJob, backupasset.LeaseActive, now, now).
		Scan(&holders).Error
	if err != nil {
		return 0, err
	}
	var count int64
	for _, holder := range holders {
		if hashFence(holder.FenceToken) == holder.FenceHash {
			count++
		}
	}
	return count, nil
}

func (store *DerivedStore) findReusableBlob(ctx context.Context, declaration DerivedBlobDeclaration) (model.BackupAssetDerivedBlob, bool, error) {
	var row model.BackupAssetDerivedBlob
	result := store.db.WithContext(ctx).
		Where("plaintext_digest = ? AND plaintext_size = ? AND cipher_format_version = ? AND chunk_size = ? AND state = ?",
			declaration.PlaintextDigest, declaration.PlaintextSize, derivedCipherFormatVersion, store.config.ChunkSize, "active").
		Limit(1).Find(&row)
	if result.Error != nil {
		return row, false, fmt.Errorf("find reusable Derived blob: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return row, false, nil
	}
	if !safeOpaqueLocator(row.OpaqueLocator) {
		return row, false, ErrDerivedBlobUnavailable
	}
	file, err := openDerivedRegularFile(filepath.Join(store.config.Root, row.OpaqueLocator))
	if err != nil {
		return row, false, err
	}
	_ = file.Close()
	return row, true, nil
}

func (store *DerivedStore) admitQuota(ctx context.Context, estimate int64) error {
	var used int64
	if err := store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
		Where("state IN ?", []string{"staged", "active"}).Select("COALESCE(SUM(physical_size), 0)").Scan(&used).Error; err != nil {
		return fmt.Errorf("load Derived Store quota: %w", err)
	}
	if used < 0 || estimate < 0 || estimate > store.config.GlobalMaxBytes-used {
		return ErrDerivedQuotaExceeded
	}
	return nil
}

func validDerivedStoreConfig(config DerivedStoreConfig) bool {
	root := strings.TrimSpace(config.Root)
	return root != "" && filepath.IsAbs(root) && filepath.Clean(root) == root && root != string(filepath.Separator) &&
		config.ChunkSize >= derivedMinimumChunkSize && config.ChunkSize <= derivedMaximumChunkSize &&
		config.BlobMaxBytes >= config.ChunkSize && config.BlobMaxBytes <= derivedMaximumBlobSize &&
		config.GlobalMaxBytes >= config.BlobMaxBytes && config.GlobalMaxBytes <= 1<<40 && safePrivateRootPath(root)
}

func validDerivedBlobDeclaration(value DerivedBlobDeclaration, maximum int64) bool {
	return value.PlaintextSize >= 0 && value.PlaintextSize <= maximum && lowerHex(value.PlaintextDigest, 64)
}

func prepareDerivedRoot(root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return errors.Join(ErrDerivedStoreUnavailable, err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(resolved) != root {
		return errors.Join(ErrDerivedStoreUnavailable, err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.Join(ErrDerivedStoreUnavailable, err)
	}
	return nil
}

func safePrivateRootPath(path string) bool {
	for _, forbidden := range []string{"/data", "/backup", "/logs"} {
		if pathsRelated(path, forbidden) {
			return false
		}
	}
	return true
}

func pathsRelated(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	separator := string(filepath.Separator)
	return left == right || strings.HasPrefix(left, right+separator) || strings.HasPrefix(right, left+separator)
}

func safeOpaqueLocator(value string) bool {
	return strings.TrimSpace(value) == value && len(value) >= 1 && len(value) <= 128 && !strings.ContainsAny(value, `/\`) && value != "." && value != ".."
}

func openDerivedRegularFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.Join(ErrDerivedBlobUnavailable, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.Join(ErrDerivedBlobUnavailable, err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.Join(ErrDerivedBlobUnavailable, err)
	}
	return file, nil
}

func syncDerivedDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.Join(ErrDerivedStoreUnavailable, err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(ErrDerivedStoreUnavailable, syncErr, closeErr)
	}
	return nil
}

func derivedPhysicalSizeEstimate(plaintextSize, chunkSize int64) (int64, error) {
	count, err := expectedDerivedChunkCount(plaintextSize, chunkSize)
	if err != nil || count > (1<<63-1-4-plaintextSize)/20 {
		return 0, ErrDerivedInvalid
	}
	return 4 + plaintextSize + count*20, nil
}

func derivedMetadataFromRow(row model.BackupAssetDerivedBlob) DerivedCryptoMetadata {
	return DerivedCryptoMetadata{
		CipherFormatVersion: row.CipherFormatVersion, ChunkSize: row.ChunkSize, ChunkCount: row.ChunkCount,
		NoncePrefix: append([]byte(nil), row.NoncePrefix...), WrappedDEK: append([]byte(nil), row.WrappedDEK...),
		EnvelopeNonce: append([]byte(nil), row.EnvelopeNonce...), KEKVersion: row.DerivedKEKVersion,
		PhysicalSize: row.PhysicalSize,
	}
}

func blobHandle(row model.BackupAssetDerivedBlob, reused bool) DerivedBlobHandle {
	return DerivedBlobHandle{
		BlobID: row.ID, OpaqueLocator: row.OpaqueLocator, PlaintextSize: row.PlaintextSize,
		PlaintextDigest: row.PlaintextDigest, PhysicalSize: row.PhysicalSize, Reused: reused,
	}
}

func (store *DerivedStore) utcNow() time.Time { return store.now().UTC() }
