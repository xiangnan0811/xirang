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
	if existing, found, err := store.findReusableBlob(ctx, declaration); err != nil {
		return DerivedBlobHandle{}, err
	} else if found {
		return blobHandle(existing, true), nil
	}
	estimate, err := derivedPhysicalSizeEstimate(declaration.PlaintextSize, store.config.ChunkSize)
	if err != nil {
		return DerivedBlobHandle{}, err
	}
	if err := store.admitQuota(ctx, estimate); err != nil {
		return DerivedBlobHandle{}, err
	}
	key, err := store.keyring.Active(ctx, backupasset.KeyDomainDerivedStore)
	if err != nil {
		return DerivedBlobHandle{}, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	defer zeroBytesLocal(key.Key)
	blobID, err := backupasset.NewOpaqueID()
	if err != nil {
		return DerivedBlobHandle{}, err
	}
	locator := blobID + ".xrd"
	stagingName := ".staging-" + blobID
	stagingPath := filepath.Join(store.config.Root, stagingName)
	finalPath := filepath.Join(store.config.Root, locator)
	file, err := os.OpenFile(stagingPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return DerivedBlobHandle{}, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	keepStaging := false
	defer func() {
		_ = file.Close()
		if !keepStaging {
			_ = os.Remove(stagingPath)
		}
	}()
	metadata, err := store.cipher.Encrypt(file, plaintext, DerivedCryptoSpec{
		BlobID: blobID, PlaintextDigest: declaration.PlaintextDigest, PlaintextSize: declaration.PlaintextSize,
		ChunkSize: store.config.ChunkSize, KEKVersion: key.Version, KEK: key.Key,
	})
	if err != nil {
		return DerivedBlobHandle{}, err
	}
	if metadata.PhysicalSize > store.config.GlobalMaxBytes || metadata.PhysicalSize > estimate {
		return DerivedBlobHandle{}, ErrDerivedQuotaExceeded
	}
	if err := file.Sync(); err != nil {
		return DerivedBlobHandle{}, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	if err := file.Close(); err != nil {
		return DerivedBlobHandle{}, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	now := store.utcNow()
	row := model.BackupAssetDerivedBlob{
		ID: blobID, PlaintextDigest: declaration.PlaintextDigest, PlaintextSize: declaration.PlaintextSize,
		PhysicalSize: metadata.PhysicalSize, CipherFormatVersion: metadata.CipherFormatVersion,
		ChunkSize: metadata.ChunkSize, ChunkCount: metadata.ChunkCount, NoncePrefix: metadata.NoncePrefix,
		OpaqueLocator: locator, WrappedDEK: metadata.WrappedDEK, EnvelopeNonce: metadata.EnvelopeNonce,
		DerivedKEKVersion: metadata.KEKVersion, State: "staged", RefCount: 0, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.db.WithContext(ctx).Create(&row).Error; err != nil {
		if existing, found, findErr := store.findReusableBlob(ctx, declaration); findErr == nil && found {
			return blobHandle(existing, true), nil
		}
		return DerivedBlobHandle{}, fmt.Errorf("persist staged Derived blob: %w", err)
	}
	keepStaging = true
	if _, err := os.Lstat(finalPath); !errors.Is(err, os.ErrNotExist) {
		return DerivedBlobHandle{}, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	if err := os.Rename(stagingPath, finalPath); err != nil {
		return DerivedBlobHandle{}, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	if err := syncDerivedDirectory(store.config.Root); err != nil {
		return DerivedBlobHandle{}, err
	}
	updated := store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
		Where("id = ? AND state = ?", row.ID, "staged").Updates(map[string]any{"state": "active", "updated_at": now})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return DerivedBlobHandle{}, errors.Join(ErrDerivedStoreUnavailable, updated.Error)
	}
	row.State = "active"
	return blobHandle(row, false), nil
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
			Where("id = ? AND derived_kek_version = ? AND state IN ?", row.ID, oldVersion, []string{"staged", "active"}).
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
	if store == nil || backupasset.ValidateOpaqueID(blobID) != nil {
		return ErrDerivedBlobUnavailable
	}
	var locator string
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var blob model.BackupAssetDerivedBlob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND state IN ?", blobID, []string{"staged", "active"}).Limit(1).Find(&blob)
		if result.Error != nil || result.RowsAffected != 1 {
			return result.Error
		}
		var references int64
		if err := tx.Model(&model.BackupAssetDerivedBlobReference{}).Where("blob_id = ? AND state = ?", blobID, "active").Count(&references).Error; err != nil {
			return err
		}
		var uploads int64
		if tx.Migrator().HasTable(&model.BackupAssetProcessingUpload{}) {
			if err := tx.Model(&model.BackupAssetProcessingUpload{}).Where("staging_id = ? AND state IN ?", blobID, []string{"staged", "committed"}).Count(&uploads).Error; err != nil {
				return err
			}
		}
		if references > 0 || uploads > 0 {
			return nil
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
		_ = store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", blobID).Update("state", "purge_failed").Error
		return errors.Join(ErrDerivedBlobUnavailable, err)
	}
	return nil
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
