package processing

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

// derivedReplay keeps one authenticated ciphertext candidate until the caller has
// finished staging. The file contains neither plaintext nor key material.
type derivedReplay struct {
	store     *DerivedStore
	file      *os.File
	spec      DerivedCryptoSpec
	metadata  DerivedCryptoMetadata
	installed bool
}

type derivedContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r derivedContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (store *DerivedStore) newReplay(ctx context.Context, declaration DerivedBlobDeclaration, plaintext io.Reader) (*derivedReplay, error) {
	var existing struct{ ID string }
	if err := store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).Select("id").Where("plaintext_digest = ? AND plaintext_size = ? AND cipher_format_version = ? AND chunk_size = ?", declaration.PlaintextDigest, declaration.PlaintextSize, derivedCipherFormatVersion, store.config.ChunkSize).Limit(1).Scan(&existing).Error; err != nil {
		return nil, err
	}
	id := existing.ID
	var err error
	if id == "" {
		id, err = backupasset.NewOpaqueID()
		if err != nil {
			return nil, err
		}
	}
	key, err := store.keyring.Active(ctx, backupasset.KeyDomainDerivedStore)
	if err != nil {
		return nil, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	file, err := os.CreateTemp(store.config.Root, ".replay-")
	if err != nil {
		zeroBytesLocal(key.Key)
		return nil, errors.Join(ErrDerivedStoreUnavailable, err)
	}
	r := &derivedReplay{store: store, file: file, spec: DerivedCryptoSpec{BlobID: id, PlaintextDigest: declaration.PlaintextDigest, PlaintextSize: declaration.PlaintextSize, ChunkSize: store.config.ChunkSize, KEKVersion: key.Version, KEK: key.Key}}
	r.metadata, err = store.cipher.Encrypt(file, derivedContextReader{ctx, plaintext}, r.spec)
	if err == nil {
		err = file.Sync()
	}
	if err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func (r *derivedReplay) Close() {
	zeroBytesLocal(r.spec.KEK)
	_ = r.file.Close()
	_ = os.Remove(r.file.Name())
}

// rebind makes a fresh authenticated candidate whenever the prior inode may
// already have been installed, even when the logical blob ID is unchanged.
func (r *derivedReplay) rebind(ctx context.Context, id string) error {
	if r.spec.BlobID == id && !r.installed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if validateDerivedCryptoSpec(r.spec) != nil || validateDerivedCryptoMetadata(r.spec, r.metadata) != nil {
		return ErrDerivedTamper
	}
	oldEnvelope, err := newDerivedAEAD(r.spec.KEK)
	if err != nil {
		return ErrDerivedTamper
	}
	oldKey, err := oldEnvelope.Open(nil, r.metadata.EnvelopeNonce, r.metadata.WrappedDEK, derivedEnvelopeAAD(r.spec))
	if err != nil || len(oldKey) != derivedDEKSize {
		zeroBytesLocal(oldKey)
		return ErrDerivedTamper
	}
	defer zeroBytesLocal(oldKey)
	oldAEAD, err := newDerivedAEAD(oldKey)
	if err != nil {
		return ErrDerivedTamper
	}
	newKey := make([]byte, derivedDEKSize)
	defer zeroBytesLocal(newKey)
	prefix := make([]byte, derivedNoncePrefixSize)
	envelope := make([]byte, derivedEnvelopeNonceSize)
	if err := r.store.cipher.readRandom(newKey, prefix, envelope); err != nil {
		return err
	}
	newAEAD, err := newDerivedAEAD(newKey)
	if err != nil {
		return err
	}
	newSpec := r.spec
	newSpec.BlobID = id
	if validateDerivedCryptoSpec(newSpec) != nil {
		return ErrDerivedInvalid
	}
	file, err := os.CreateTemp(r.store.config.Root, ".replay-")
	if err != nil {
		return errors.Join(ErrDerivedStoreUnavailable, err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	var magic [4]byte
	if _, err := r.file.ReadAt(magic[:], 0); err != nil || magic != derivedCipherMagic {
		return ErrDerivedTamper
	}
	if err := writeDerivedBytes(file, magic[:]); err != nil {
		return err
	}
	hasher := sha256.New()
	offset := int64(len(magic))
	remaining := r.spec.PlaintextSize
	sealedBuffer := make([]byte, int(r.spec.ChunkSize)+oldAEAD.Overhead())
	plaintextBuffer := make([]byte, 0, int(r.spec.ChunkSize))
	for index := int64(0); index < r.metadata.ChunkCount; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := r.spec.ChunkSize
		if remaining < size {
			size = remaining
		}
		var length [4]byte
		if _, err := r.file.ReadAt(length[:], offset); err != nil || int64(binary.BigEndian.Uint32(length[:])) != size {
			return ErrDerivedTamper
		}
		sealed := sealedBuffer[:int(size)+oldAEAD.Overhead()]
		if _, err := r.file.ReadAt(sealed, offset+4); err != nil {
			return ErrDerivedTamper
		}
		nonce, err := derivedChunkNonce(r.metadata.NoncePrefix, index)
		if err != nil {
			return ErrDerivedTamper
		}
		plain, err := oldAEAD.Open(plaintextBuffer[:0], nonce, sealed, derivedChunkAAD(r.spec, index, size))
		if err != nil || int64(len(plain)) != size {
			zeroBytesLocal(plain)
			return ErrDerivedTamper
		}
		_, _ = hasher.Write(plain)
		newNonce, err := derivedChunkNonce(prefix, index)
		if err != nil {
			zeroBytesLocal(plain)
			return err
		}
		sealed = newAEAD.Seal(sealed[:0], newNonce, plain, derivedChunkAAD(newSpec, index, size))
		zeroBytesLocal(plain)
		if err := writeDerivedBytes(file, length[:]); err != nil {
			return err
		}
		if err := writeDerivedBytes(file, sealed); err != nil {
			return err
		}
		offset += 4 + int64(len(sealed))
		remaining -= size
	}
	var extra [1]byte
	count, readErr := r.file.ReadAt(extra[:], offset)
	if count != 0 || readErr != io.EOF || offset != r.metadata.PhysicalSize || remaining != 0 || !constantDigestEqual(hasher.Sum(nil), r.spec.PlaintextDigest) {
		return ErrDerivedTamper
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != r.metadata.PhysicalSize {
		return ErrDerivedTamper
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata := r.metadata
	metadata.NoncePrefix = prefix
	metadata.EnvelopeNonce = envelope
	metadata.WrappedDEK = oldEnvelope.Seal(nil, envelope, newKey, derivedEnvelopeAAD(newSpec))
	oldFile := r.file
	r.file, r.spec, r.metadata, r.installed = file, newSpec, metadata, false
	keep = true
	_ = oldFile.Close()
	_ = os.Remove(oldFile.Name())
	return nil
}
