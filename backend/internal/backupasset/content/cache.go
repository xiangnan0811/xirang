package content

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"xirang/backend/internal/backupasset"
)

var (
	ErrInvalidCacheBinding = errors.New("invalid content cache binding")
	ErrCacheIntegrity      = errors.New("content cache integrity failure")
	ErrCacheNonceReuse     = errors.New("content cache nonce reuse")
	ErrCacheClosed         = errors.New("content cache closed")
	ErrInvalidCacheConfig  = errors.New("invalid content cache config")
	ErrCacheMiss           = errors.New("content cache miss")
	ErrCacheQuota          = errors.New("content cache quota exhausted")
	ErrCacheBusy           = errors.New("content cache entry busy")
	ErrCacheUnsafeRoot     = errors.New("unsafe content cache root")
	ErrCacheSourceChanged  = errors.New("content cache source changed")
)

const (
	cacheChunkMagic            = "XRCACHE1"
	cacheChunkVersion          = byte(1)
	cacheChunkVersionOffset    = len(cacheChunkMagic)
	cacheChunkGenerationOffset = cacheChunkVersionOffset + 1
	cacheChunkGenerationSize   = 16
	cacheChunkNonceOffset      = cacheChunkGenerationOffset + cacheChunkGenerationSize
	cacheChunkNonceSize        = 12
	cacheChunkLengthOffset     = cacheChunkNonceOffset + cacheChunkNonceSize
	cacheChunkHeaderSize       = cacheChunkLengthOffset + 8
)

type CacheChunkBinding struct {
	OwnerUserID         uint
	Provider            backupasset.ProviderKind
	Ref                 backupasset.AssetRef
	CatalogGenerationID string
	SourceFingerprint   string
	ContentFingerprint  string
	Renderer            Renderer
	Profile             RendererProfile
	ChunkIndex          uint64
	PlaintextLength     int64
}

type CacheCipher struct {
	mu          sync.Mutex
	random      io.Reader
	aead        cipher.AEAD
	key         [32]byte
	filenameKey [32]byte
	generation  [cacheChunkGenerationSize]byte
	usedNonces  map[[cacheChunkNonceSize]byte]struct{}
	closed      bool
}

func NewCacheCipher(random io.Reader) (*CacheCipher, error) {
	if random == nil {
		return nil, ErrInvalidCacheBinding
	}
	result := &CacheCipher{random: random, usedNonces: make(map[[cacheChunkNonceSize]byte]struct{})}
	if _, err := io.ReadFull(random, result.key[:]); err != nil {
		return nil, ErrInvalidCacheBinding
	}
	if _, err := io.ReadFull(random, result.generation[:]); err != nil {
		zeroBytes(result.key[:])
		return nil, ErrInvalidCacheBinding
	}
	block, err := aes.NewCipher(result.key[:])
	if err != nil {
		zeroBytes(result.key[:])
		zeroBytes(result.generation[:])
		return nil, ErrInvalidCacheBinding
	}
	result.aead, err = cipher.NewGCM(block)
	if err != nil || result.aead.NonceSize() != cacheChunkNonceSize {
		zeroBytes(result.key[:])
		zeroBytes(result.generation[:])
		return nil, ErrInvalidCacheBinding
	}
	mac := hmac.New(sha256.New, result.key[:])
	_, _ = mac.Write([]byte("xirang/content-cache/filename/v1"))
	copy(result.filenameKey[:], mac.Sum(nil))
	return result, nil
}

func (cacheCipher *CacheCipher) Seal(binding CacheChunkBinding, plaintext []byte) ([]byte, error) {
	if cacheCipher == nil || !validCacheChunkBinding(binding) || int64(len(plaintext)) != binding.PlaintextLength {
		return nil, ErrInvalidCacheBinding
	}
	cacheCipher.mu.Lock()
	defer cacheCipher.mu.Unlock()
	if cacheCipher.closed {
		return nil, ErrCacheClosed
	}
	var nonce [cacheChunkNonceSize]byte
	if _, err := io.ReadFull(cacheCipher.random, nonce[:]); err != nil {
		return nil, ErrCacheIntegrity
	}
	if _, exists := cacheCipher.usedNonces[nonce]; exists {
		return nil, ErrCacheNonceReuse
	}
	cacheCipher.usedNonces[nonce] = struct{}{}
	aad := cacheCipher.associatedData(binding)
	ciphertext := cacheCipher.aead.Seal(nil, nonce[:], plaintext, aad)
	sealed := make([]byte, cacheChunkHeaderSize+len(ciphertext))
	copy(sealed, cacheChunkMagic)
	sealed[cacheChunkVersionOffset] = cacheChunkVersion
	copy(sealed[cacheChunkGenerationOffset:cacheChunkNonceOffset], cacheCipher.generation[:])
	copy(sealed[cacheChunkNonceOffset:cacheChunkLengthOffset], nonce[:])
	binary.BigEndian.PutUint64(sealed[cacheChunkLengthOffset:cacheChunkHeaderSize], uint64(binding.PlaintextLength))
	copy(sealed[cacheChunkHeaderSize:], ciphertext)
	return sealed, nil
}

func (cacheCipher *CacheCipher) Open(binding CacheChunkBinding, sealed []byte) ([]byte, error) {
	if cacheCipher == nil || !validCacheChunkBinding(binding) {
		return nil, ErrInvalidCacheBinding
	}
	cacheCipher.mu.Lock()
	defer cacheCipher.mu.Unlock()
	if cacheCipher.closed {
		return nil, ErrCacheClosed
	}
	if len(sealed) < cacheChunkHeaderSize+cacheCipher.aead.Overhead() ||
		string(sealed[:cacheChunkVersionOffset]) != cacheChunkMagic ||
		sealed[cacheChunkVersionOffset] != cacheChunkVersion ||
		!hmac.Equal(sealed[cacheChunkGenerationOffset:cacheChunkNonceOffset], cacheCipher.generation[:]) {
		return nil, ErrCacheIntegrity
	}
	encodedLength := binary.BigEndian.Uint64(sealed[cacheChunkLengthOffset:cacheChunkHeaderSize])
	if encodedLength > math.MaxInt64 || int64(encodedLength) != binding.PlaintextLength ||
		len(sealed) != cacheChunkHeaderSize+int(encodedLength)+cacheCipher.aead.Overhead() {
		return nil, ErrCacheIntegrity
	}
	nonce := sealed[cacheChunkNonceOffset:cacheChunkLengthOffset]
	plaintext, err := cacheCipher.aead.Open(nil, nonce, sealed[cacheChunkHeaderSize:], cacheCipher.associatedData(binding))
	if err != nil || int64(len(plaintext)) != binding.PlaintextLength {
		zeroBytes(plaintext)
		return nil, ErrCacheIntegrity
	}
	return plaintext, nil
}

func (cacheCipher *CacheCipher) OpaqueFilename(binding CacheChunkBinding) (string, error) {
	if cacheCipher == nil || !validCacheChunkBinding(binding) {
		return "", ErrInvalidCacheBinding
	}
	cacheCipher.mu.Lock()
	defer cacheCipher.mu.Unlock()
	if cacheCipher.closed {
		return "", ErrCacheClosed
	}
	mac := hmac.New(sha256.New, cacheCipher.filenameKey[:])
	_, _ = mac.Write(cacheCipher.associatedData(binding))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (cacheCipher *CacheCipher) Zero() {
	if cacheCipher == nil {
		return
	}
	cacheCipher.mu.Lock()
	defer cacheCipher.mu.Unlock()
	if cacheCipher.closed {
		return
	}
	cacheCipher.closed = true
	zeroBytes(cacheCipher.key[:])
	zeroBytes(cacheCipher.filenameKey[:])
	zeroBytes(cacheCipher.generation[:])
	clear(cacheCipher.usedNonces)
}

func (cacheCipher *CacheCipher) associatedData(binding CacheChunkBinding) []byte {
	var buffer bytes.Buffer
	writeLengthPrefixed(&buffer, []byte("xirang/content-cache/v1"))
	writeLengthPrefixed(&buffer, cacheCipher.generation[:])
	_ = binary.Write(&buffer, binary.BigEndian, uint64(binding.OwnerUserID))
	writeLengthPrefixed(&buffer, []byte(binding.Provider))
	writeLengthPrefixed(&buffer, []byte(binding.Ref.RecoveryPointID))
	writeLengthPrefixed(&buffer, []byte(binding.Ref.EntryID))
	writeLengthPrefixed(&buffer, []byte(binding.CatalogGenerationID))
	writeLengthPrefixed(&buffer, []byte(binding.SourceFingerprint))
	writeLengthPrefixed(&buffer, []byte(binding.ContentFingerprint))
	writeLengthPrefixed(&buffer, []byte(binding.Renderer))
	writeLengthPrefixed(&buffer, []byte(binding.Profile))
	_ = binary.Write(&buffer, binary.BigEndian, binding.ChunkIndex)
	_ = binary.Write(&buffer, binary.BigEndian, uint64(binding.PlaintextLength))
	return buffer.Bytes()
}

func writeLengthPrefixed(buffer *bytes.Buffer, value []byte) {
	_ = binary.Write(buffer, binary.BigEndian, uint32(len(value)))
	_, _ = buffer.Write(value)
}

func validCacheChunkBinding(binding CacheChunkBinding) bool {
	return binding.OwnerUserID > 0 && validCacheProvider(binding.Provider) &&
		backupasset.ValidateAssetRef(binding.Ref) == nil && backupasset.ValidateOpaqueID(binding.CatalogGenerationID) == nil &&
		len(binding.SourceFingerprint) > 0 && len(binding.SourceFingerprint) <= 128 &&
		len(binding.ContentFingerprint) > 0 && len(binding.ContentFingerprint) <= 128 &&
		validCacheRendererProfile(binding.Renderer, binding.Profile) && binding.PlaintextLength >= 0
}

func validCacheProvider(provider backupasset.ProviderKind) bool {
	return provider == backupasset.ProviderRestic || provider == backupasset.ProviderRsync || provider == backupasset.ProviderRclone
}

func validCacheRendererProfile(renderer Renderer, profile RendererProfile) bool {
	switch renderer {
	case RendererEscapedText:
		return profile == ProfileTextV1
	case RendererSafeRaster:
		return profile == ProfileRasterV1
	case RendererSameOriginPDF:
		return profile == ProfilePDFV1
	case RendererNativeAudio:
		return profile == ProfileAudioV1
	case RendererNativeVideo:
		return profile == ProfileVideoV1
	case RendererMetadataHex:
		return profile == ProfileHexV1
	case RendererAttachment:
		return profile == ProfileOriginalV1
	default:
		return false
	}
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

const cacheProcessLockName = ".xirang-asset-content.lock"

type CacheDisableReason string

const (
	CacheReasonNone           CacheDisableReason = ""
	CacheReasonConfigDisabled CacheDisableReason = "cache_disabled"
	CacheReasonUnsafeRoot     CacheDisableReason = "cache_root_unsafe"
	CacheReasonSourceOverlap  CacheDisableReason = "cache_source_overlap"
	CacheReasonRootUnverified CacheDisableReason = "cache_root_unverified"
	CacheReasonInUse          CacheDisableReason = "cache_root_in_use"
	CacheReasonCleanupFailed  CacheDisableReason = "cache_cleanup_failed"
	CacheReasonFull           CacheDisableReason = "cache_full"
	CacheReasonClosed         CacheDisableReason = "cache_closed"
)

type CacheTier string

const (
	CacheTierMemory CacheTier = "memory"
	CacheTierDisk   CacheTier = "disk"
)

type CacheStatus struct {
	DiskEnabled bool
	Reason      CacheDisableReason
}

type CacheConfig struct {
	DiskEnabled bool
	Root        string
	ChunkBytes  int64

	ObjectBytes   int64
	UserBytes     int64
	ProviderBytes int64
	GlobalBytes   int64

	ObjectFiles   int64
	UserFiles     int64
	ProviderFiles int64
	GlobalFiles   int64

	MemoryObjectBytes   int64
	MemoryUserBytes     int64
	MemoryProviderBytes int64
	MemoryGlobalBytes   int64

	IdleTTL            time.Duration
	AbsoluteTTL        time.Duration
	ReconcileBatchSize int
}

type CacheRootSourceValidator interface {
	ValidateContentCacheRoot(context.Context, string) error
}

type CacheDependencies struct {
	Config      CacheConfig
	Now         func() time.Time
	Random      io.Reader
	SourceRoots CacheRootSourceValidator
	VerifyMount func(string) error
	Metrics     Metrics
}

type CacheObject struct {
	OwnerUserID         uint
	Provider            backupasset.ProviderKind
	Ref                 backupasset.AssetRef
	CatalogGenerationID string
	SourceFingerprint   string
	ContentFingerprint  string
	Renderer            Renderer
	Profile             RendererProfile
	Size                int64
}

type CacheEntryInfo struct {
	Tier          CacheTier
	Size          int64
	RangeCapable  bool
	ProviderBytes int64
}

type cacheChunk struct {
	name            string
	index           uint64
	plaintextLength int64
}

type cacheEntry struct {
	key               string
	object            CacheObject
	tier              CacheTier
	memory            []byte
	chunks            []cacheChunk
	quotaBytes        int64
	quotaFiles        int64
	idleExpiresAt     time.Time
	absoluteExpiresAt time.Time
	leases            int64
	invalid           bool
}

type cacheQuotaUsage struct {
	bytes int64
	files int64
}

type cacheReservation struct {
	tier     CacheTier
	bytes    int64
	files    int64
	ownerID  uint
	provider backupasset.ProviderKind
}

type AuthenticatedCache struct {
	mu        sync.Mutex
	config    CacheConfig
	now       func() time.Time
	cipher    *CacheCipher
	metrics   Metrics
	status    CacheStatus
	accepting bool
	closed    bool
	activeOps int64
	opsDone   chan struct{}

	rootPath string
	root     *os.Root
	lockFile *os.File

	entries     map[string]*cacheEntry
	writing     map[string]cacheReservation
	activeFiles map[string]bool

	memoryGlobal    cacheQuotaUsage
	memoryUsers     map[uint]cacheQuotaUsage
	memoryProviders map[backupasset.ProviderKind]cacheQuotaUsage
	diskGlobal      cacheQuotaUsage
	diskUsers       map[uint]cacheQuotaUsage
	diskProviders   map[backupasset.ProviderKind]cacheQuotaUsage

	writeChunkFile  func(string, []byte) error
	renameChunkFile func(string, string) error
	removeChunkFile func(string) error
}

type CacheLease struct {
	mu           sync.Mutex
	cache        *AuthenticatedCache
	entry        *cacheEntry
	offset       int64
	remaining    int64
	currentChunk []byte
	currentIndex int64
	closed       bool
}

type CacheMaterialization struct {
	mu          sync.Mutex
	cache       *AuthenticatedCache
	key         string
	object      CacheObject
	reservation cacheReservation
	finished    bool
}

func NewAuthenticatedCache(ctx context.Context, dependencies CacheDependencies) (*AuthenticatedCache, error) {
	if dependencies.Now == nil || dependencies.Random == nil || !validCacheConfig(dependencies.Config) {
		return nil, ErrInvalidCacheConfig
	}
	cacheCipher, err := NewCacheCipher(dependencies.Random)
	if err != nil {
		return nil, err
	}
	if dependencies.Metrics == nil {
		dependencies.Metrics = NoopMetrics{}
	}
	done := make(chan struct{})
	close(done)
	cache := &AuthenticatedCache{
		config: dependencies.Config, now: dependencies.Now, cipher: cacheCipher, metrics: dependencies.Metrics,
		status: CacheStatus{Reason: CacheReasonConfigDisabled}, accepting: true, opsDone: done,
		entries: make(map[string]*cacheEntry), writing: make(map[string]cacheReservation), activeFiles: make(map[string]bool),
		memoryUsers: make(map[uint]cacheQuotaUsage), memoryProviders: make(map[backupasset.ProviderKind]cacheQuotaUsage),
		diskUsers: make(map[uint]cacheQuotaUsage), diskProviders: make(map[backupasset.ProviderKind]cacheQuotaUsage),
	}
	if !dependencies.Config.DiskEnabled {
		return cache, nil
	}
	reason := cache.initializeDisk(nonNilContext(ctx), dependencies)
	cache.mu.Lock()
	cache.status = CacheStatus{DiskEnabled: reason == CacheReasonNone, Reason: reason}
	cache.mu.Unlock()
	return cache, nil
}

func (cache *AuthenticatedCache) Status() CacheStatus {
	if cache == nil {
		return CacheStatus{Reason: CacheReasonClosed}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.status
}

func (cache *AuthenticatedCache) Materialize(ctx context.Context, object CacheObject, source SourceSession) (CacheEntryInfo, error) {
	if cache == nil || !validCacheObject(object) || source == nil {
		if source != nil {
			_ = source.Close()
		}
		return CacheEntryInfo{}, ErrInvalidCacheBinding
	}
	materialization, hit, err := cache.BeginMaterialization(object)
	if err != nil {
		closeErr := source.Close()
		if closeErr != nil {
			return CacheEntryInfo{}, ErrCacheSourceChanged
		}
		return CacheEntryInfo{}, err
	}
	if materialization == nil {
		if closeErr := source.Close(); closeErr != nil {
			return CacheEntryInfo{}, ErrCacheSourceChanged
		}
		return hit, nil
	}
	return materialization.Commit(ctx, source)
}

func (cache *AuthenticatedCache) BeginMaterialization(object CacheObject) (*CacheMaterialization, CacheEntryInfo, error) {
	if cache == nil || !validCacheObject(object) {
		return nil, CacheEntryInfo{}, ErrInvalidCacheBinding
	}
	if err := cache.beginOperation(); err != nil {
		return nil, CacheEntryInfo{}, err
	}
	key, err := cache.objectKey(object)
	if err != nil {
		cache.endOperation()
		return nil, CacheEntryInfo{}, err
	}
	reservation, hit, err := cache.reserveMaterialization(key, object)
	if hit != nil {
		cache.endOperation()
		return nil, cacheEntryInfo(hit), nil
	}
	if err != nil {
		cache.endOperation()
		return nil, CacheEntryInfo{}, err
	}
	return &CacheMaterialization{cache: cache, key: key, object: object, reservation: reservation}, CacheEntryInfo{}, nil
}

func (materialization *CacheMaterialization) Commit(ctx context.Context, source SourceSession) (CacheEntryInfo, error) {
	if materialization == nil || materialization.cache == nil || source == nil {
		if source != nil {
			_ = source.Close()
		}
		return CacheEntryInfo{}, ErrInvalidCacheBinding
	}
	materialization.mu.Lock()
	if materialization.finished {
		materialization.mu.Unlock()
		_ = source.Close()
		return CacheEntryInfo{}, ErrCacheClosed
	}
	materialization.finished = true
	materialization.mu.Unlock()
	cache := materialization.cache
	committed := false
	defer func() {
		if !committed {
			cache.releaseWriting(materialization.key, materialization.reservation)
		}
		cache.endOperation()
	}()
	ctx = nonNilContext(ctx)
	if err := validateCacheSource(materialization.object, source); err != nil {
		_ = source.Close()
		return CacheEntryInfo{}, err
	}
	if err := source.Revalidate(ctx); err != nil {
		_ = source.Close()
		return CacheEntryInfo{}, ErrCacheSourceChanged
	}
	reader := source.Reader()
	if reader == nil {
		_ = source.Close()
		return CacheEntryInfo{}, ErrCacheSourceChanged
	}

	var entry *cacheEntry
	var err error
	switch materialization.reservation.tier {
	case CacheTierMemory:
		entry, err = cache.materializeMemory(ctx, materialization.key, materialization.object, reader)
	case CacheTierDisk:
		entry, err = cache.materializeDisk(ctx, materialization.key, materialization.object, reader)
	default:
		err = ErrCacheQuota
	}
	closeErr := source.Close()
	if err != nil {
		if closeErr != nil {
			return CacheEntryInfo{}, ErrCacheSourceChanged
		}
		return CacheEntryInfo{}, err
	}
	providerBytes := reader.ProviderBytes()
	if closeErr != nil || providerBytes < 0 {
		_ = cache.deleteEntryStorage(entry)
		return CacheEntryInfo{}, ErrCacheSourceChanged
	}
	if entry.tier == CacheTierDisk {
		if err := cache.publishDiskEntry(entry); err != nil {
			_ = cache.deleteEntryStorage(entry)
			return CacheEntryInfo{}, err
		}
	}
	now := cache.now().UTC()
	entry.idleExpiresAt = now.Add(cache.config.IdleTTL)
	entry.absoluteExpiresAt = now.Add(cache.config.AbsoluteTTL)
	cache.mu.Lock()
	delete(cache.writing, materialization.key)
	cache.entries[materialization.key] = entry
	cache.mu.Unlock()
	committed = true
	info := cacheEntryInfo(entry)
	info.ProviderBytes = providerBytes
	return info, nil
}

func (materialization *CacheMaterialization) Abort() error {
	if materialization == nil || materialization.cache == nil {
		return ErrInvalidCacheBinding
	}
	materialization.mu.Lock()
	if materialization.finished {
		materialization.mu.Unlock()
		return nil
	}
	materialization.finished = true
	materialization.mu.Unlock()
	materialization.cache.releaseWriting(materialization.key, materialization.reservation)
	materialization.cache.endOperation()
	return nil
}

func (cache *AuthenticatedCache) OpenRange(object CacheObject, offset, length int64) (*CacheLease, error) {
	if cache == nil || !validCacheObject(object) || offset < 0 || length < 0 ||
		offset > object.Size || length > object.Size-offset || length == 0 && object.Size != 0 {
		return nil, ErrInvalidCacheBinding
	}
	if err := cache.beginOperation(); err != nil {
		return nil, err
	}
	key, err := cache.objectKey(object)
	if err != nil {
		cache.endOperation()
		return nil, err
	}
	now := cache.now().UTC()
	cache.mu.Lock()
	entry := cache.entries[key]
	if entry == nil || entry.invalid || !now.Before(entry.idleExpiresAt) || !now.Before(entry.absoluteExpiresAt) {
		cache.mu.Unlock()
		cache.endOperation()
		return nil, ErrCacheMiss
	}
	entry.leases++
	refreshed := now.Add(cache.config.IdleTTL)
	if refreshed.After(entry.absoluteExpiresAt) {
		refreshed = entry.absoluteExpiresAt
	}
	entry.idleExpiresAt = refreshed
	cache.mu.Unlock()
	return &CacheLease{cache: cache, entry: entry, offset: offset, remaining: length, currentIndex: -1}, nil
}

func (cache *AuthenticatedCache) Evict(object CacheObject) error {
	if cache == nil || !validCacheObject(object) {
		return ErrInvalidCacheBinding
	}
	key, err := cache.objectKey(object)
	if err != nil {
		return err
	}
	cache.mu.Lock()
	entry := cache.entries[key]
	if entry == nil {
		cache.mu.Unlock()
		return ErrCacheMiss
	}
	if entry.leases > 0 {
		cache.mu.Unlock()
		return ErrCacheBusy
	}
	cache.removeEntryLocked(entry)
	cache.mu.Unlock()
	return cache.deleteEntryStorage(entry)
}

func (cache *AuthenticatedCache) Reconcile(ctx context.Context) error {
	if cache == nil {
		return ErrCacheClosed
	}
	if err := cache.beginOperation(); err != nil {
		return err
	}
	defer cache.endOperation()
	ctx = nonNilContext(ctx)
	now := cache.now().UTC()
	var removals []*cacheEntry
	cache.mu.Lock()
	remaining := cache.config.ReconcileBatchSize
	for _, entry := range cache.entries {
		if remaining == 0 {
			break
		}
		if entry.leases == 0 && (entry.invalid || !now.Before(entry.idleExpiresAt) || !now.Before(entry.absoluteExpiresAt)) {
			cache.removeEntryLocked(entry)
			removals = append(removals, entry)
			remaining--
		}
	}
	cache.mu.Unlock()
	for _, entry := range removals {
		if err := cache.deleteEntryStorage(entry); err != nil {
			return err
		}
	}
	if remaining <= 0 || !cache.Status().DiskEnabled {
		return nil
	}
	entries, err := fs.ReadDir(cache.root.FS(), ".")
	if err != nil {
		return ErrCacheUnsafeRoot
	}
	for _, item := range entries {
		if remaining == 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := item.Name()
		if name == cacheProcessLockName || cache.fileTracked(name) {
			continue
		}
		info, statErr := cache.root.Lstat(name)
		if statErr != nil {
			return ErrCacheUnsafeRoot
		}
		if !info.Mode().IsRegular() {
			cache.disableDisk(CacheReasonUnsafeRoot)
			return ErrCacheUnsafeRoot
		}
		if err := cache.root.Remove(name); err != nil {
			return ErrCacheUnsafeRoot
		}
		cache.metrics.ObserveCache(MetricCacheOrphan)
		remaining--
	}
	return nil
}

func (cache *AuthenticatedCache) Shutdown(ctx context.Context) error {
	if cache == nil {
		return nil
	}
	ctx = nonNilContext(ctx)
	cache.mu.Lock()
	if cache.closed {
		cache.mu.Unlock()
		return nil
	}
	cache.accepting = false
	done := cache.opsDone
	cache.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	cache.mu.Lock()
	entries := make([]*cacheEntry, 0, len(cache.entries))
	for _, entry := range cache.entries {
		cache.removeEntryLocked(entry)
		entries = append(entries, entry)
	}
	cache.closed = true
	cache.status = CacheStatus{Reason: CacheReasonClosed}
	cache.mu.Unlock()
	var firstErr error
	for _, entry := range entries {
		if err := cache.deleteEntryStorage(entry); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	cache.releaseDiskRoot()
	cache.cipher.Zero()
	return firstErr
}

func (lease *CacheLease) Read(buffer []byte) (int, error) {
	if lease == nil {
		return 0, ErrCacheClosed
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return 0, ErrCacheClosed
	}
	if lease.remaining == 0 {
		return 0, io.EOF
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if lease.entry.tier == CacheTierMemory {
		count := int(min(int64(len(buffer)), lease.remaining))
		copy(buffer[:count], lease.entry.memory[lease.offset:lease.offset+int64(count)])
		lease.offset += int64(count)
		lease.remaining -= int64(count)
		return count, nil
	}
	return lease.readDisk(buffer)
}

func (lease *CacheLease) Close() error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		return nil
	}
	lease.closed = true
	zeroBytes(lease.currentChunk)
	lease.currentChunk = nil
	lease.mu.Unlock()
	cache := lease.cache
	cache.mu.Lock()
	lease.entry.leases--
	now := cache.now().UTC()
	remove := lease.entry.leases == 0 && (lease.entry.invalid || !now.Before(lease.entry.idleExpiresAt) || !now.Before(lease.entry.absoluteExpiresAt))
	if remove {
		cache.removeEntryLocked(lease.entry)
	}
	cache.mu.Unlock()
	if remove {
		_ = cache.deleteEntryStorage(lease.entry)
	}
	cache.endOperation()
	return nil
}

func (lease *CacheLease) readDisk(buffer []byte) (int, error) {
	chunkIndex := lease.offset / lease.cache.config.ChunkBytes
	if lease.currentIndex != chunkIndex {
		zeroBytes(lease.currentChunk)
		lease.currentChunk = nil
		if chunkIndex < 0 || chunkIndex >= int64(len(lease.entry.chunks)) {
			lease.invalidate()
			return 0, ErrCacheIntegrity
		}
		chunk := lease.entry.chunks[chunkIndex]
		sealed, err := lease.cache.root.ReadFile(chunk.name)
		if err != nil {
			lease.invalidate()
			return 0, ErrCacheIntegrity
		}
		binding := lease.cache.chunkBinding(lease.entry.object, chunk.index, chunk.plaintextLength)
		plaintext, err := lease.cache.cipher.Open(binding, sealed)
		if err != nil {
			lease.invalidate()
			return 0, ErrCacheIntegrity
		}
		lease.currentChunk = plaintext
		lease.currentIndex = chunkIndex
	}
	within := lease.offset - chunkIndex*lease.cache.config.ChunkBytes
	available := int64(len(lease.currentChunk)) - within
	if available <= 0 {
		lease.invalidate()
		return 0, ErrCacheIntegrity
	}
	count := int(min(int64(len(buffer)), min(available, lease.remaining)))
	copy(buffer[:count], lease.currentChunk[within:within+int64(count)])
	lease.offset += int64(count)
	lease.remaining -= int64(count)
	return count, nil
}

func (lease *CacheLease) invalidate() {
	lease.cache.mu.Lock()
	lease.entry.invalid = true
	lease.cache.mu.Unlock()
	lease.cache.metrics.ObserveCache(MetricCacheTamper)
}

func (cache *AuthenticatedCache) initializeDisk(ctx context.Context, dependencies CacheDependencies) CacheDisableReason {
	rootPath := dependencies.Config.Root
	if !safeCacheRootPath(rootPath) {
		return CacheReasonUnsafeRoot
	}
	if err := ensureCacheRootPath(rootPath); err != nil {
		return CacheReasonUnsafeRoot
	}
	resolved, err := filepath.EvalSymlinks(rootPath)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(rootPath) || !safeCacheRootPath(resolved) {
		return CacheReasonUnsafeRoot
	}
	validatedInfo, err := os.Lstat(resolved)
	if err != nil || validatedInfo.Mode()&os.ModeSymlink != 0 || !validatedInfo.IsDir() {
		return CacheReasonRootUnverified
	}
	verifyMount := dependencies.VerifyMount
	if verifyMount == nil {
		verifyMount = defaultCacheMountVerifier
	}
	if err := verifyMount(resolved); err != nil {
		return CacheReasonRootUnverified
	}
	if dependencies.SourceRoots == nil {
		return CacheReasonRootUnverified
	}
	if err := dependencies.SourceRoots.ValidateContentCacheRoot(ctx, resolved); err != nil {
		return CacheReasonSourceOverlap
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return CacheReasonRootUnverified
	}
	openedInfo, openedErr := root.Stat(".")
	currentInfo, currentErr := os.Lstat(resolved)
	if openedErr != nil || currentErr != nil || !openedInfo.IsDir() ||
		currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.IsDir() ||
		!os.SameFile(validatedInfo, openedInfo) || !os.SameFile(openedInfo, currentInfo) {
		_ = root.Close()
		return CacheReasonRootUnverified
	}
	lockFile, err := root.OpenFile(cacheProcessLockName, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		_ = root.Close()
		return CacheReasonUnsafeRoot
	}
	lockInfo, err := lockFile.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() || lockFile.Chmod(0o600) != nil {
		_ = lockFile.Close()
		_ = root.Close()
		return CacheReasonUnsafeRoot
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		_ = root.Close()
		return CacheReasonInUse
	}
	cache.rootPath, cache.root, cache.lockFile = resolved, root, lockFile
	if reason := cache.cleanStartupRoot(); reason != CacheReasonNone {
		cache.releaseDiskRoot()
		return reason
	}
	cache.writeChunkFile = cache.writeCiphertextFile
	cache.renameChunkFile = cache.root.Rename
	cache.removeChunkFile = cache.root.Remove
	return CacheReasonNone
}

func (cache *AuthenticatedCache) cleanStartupRoot() CacheDisableReason {
	entries, err := fs.ReadDir(cache.root.FS(), ".")
	if err != nil {
		return CacheReasonCleanupFailed
	}
	for _, entry := range entries {
		if entry.Name() == cacheProcessLockName {
			continue
		}
		info, err := cache.root.Lstat(entry.Name())
		if err != nil || !info.Mode().IsRegular() {
			return CacheReasonUnsafeRoot
		}
		if err := cache.root.Remove(entry.Name()); err != nil {
			return CacheReasonCleanupFailed
		}
		cache.metrics.ObserveCache(MetricCacheKeyLoss)
	}
	return CacheReasonNone
}

func (cache *AuthenticatedCache) writeCiphertextFile(name string, payload []byte) error {
	if !validCacheFileName(name) || cache.root == nil {
		return ErrCacheUnsafeRoot
	}
	file, err := cache.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(payload)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (cache *AuthenticatedCache) materializeMemory(ctx context.Context, key string, object CacheObject, reader SourceReader) (*cacheEntry, error) {
	if object.Size > int64(maxInt()) {
		return nil, ErrCacheQuota
	}
	payload := make([]byte, int(object.Size))
	if err := readCacheSourceExact(ctx, reader, payload); err != nil {
		zeroBytes(payload)
		return nil, err
	}
	return &cacheEntry{
		key: key, object: object, tier: CacheTierMemory, memory: payload,
		quotaBytes: object.Size,
	}, nil
}

func (cache *AuthenticatedCache) materializeDisk(ctx context.Context, key string, object CacheObject, reader SourceReader) (*cacheEntry, error) {
	entry := &cacheEntry{key: key, object: object, tier: CacheTierDisk, quotaBytes: object.Size}
	var written int64
	for index := uint64(0); written < object.Size; index++ {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, cache.deleteEntryStorage(entry))
		}
		length := min(cache.config.ChunkBytes, object.Size-written)
		plaintext := make([]byte, int(length))
		if err := readCacheSourceExact(ctx, reader, plaintext); err != nil {
			zeroBytes(plaintext)
			return nil, errors.Join(err, cache.deleteEntryStorage(entry))
		}
		binding := cache.chunkBinding(object, index, length)
		sealed, err := cache.cipher.Seal(binding, plaintext)
		zeroBytes(plaintext)
		if err != nil {
			return nil, errors.Join(err, cache.deleteEntryStorage(entry))
		}
		name, err := cache.cipher.OpaqueFilename(binding)
		if err != nil {
			return nil, errors.Join(err, cache.deleteEntryStorage(entry))
		}
		partial := name + ".partial"
		cache.trackFile(partial, true)
		if err := cache.writeChunkFile(partial, sealed); err != nil {
			if cache.removeChunkFile != nil {
				_ = cache.removeChunkFile(partial)
			}
			cache.trackFile(partial, false)
			cleanupErr := cache.deleteEntryStorage(entry)
			if errors.Is(err, syscall.ENOSPC) {
				cache.disableDisk(CacheReasonFull)
			}
			return nil, errors.Join(err, cleanupErr)
		}
		entry.chunks = append(entry.chunks, cacheChunk{name: partial, index: index, plaintextLength: length})
		written += length
	}
	entry.quotaFiles = int64(len(entry.chunks)) + 1
	return entry, nil
}

func (cache *AuthenticatedCache) publishDiskEntry(entry *cacheEntry) error {
	if cache == nil || entry == nil || entry.tier != CacheTierDisk || cache.renameChunkFile == nil {
		return ErrCacheIntegrity
	}
	for index := range entry.chunks {
		partial := entry.chunks[index].name
		final := strings.TrimSuffix(partial, ".partial")
		if partial == final || !validCacheFileName(partial) || !validCacheFileName(final) {
			return ErrCacheIntegrity
		}
		if err := cache.renameChunkFile(partial, final); err != nil {
			return err
		}
		cache.trackFile(partial, false)
		cache.trackFile(final, true)
		entry.chunks[index].name = final
	}
	return nil
}

func readCacheSourceExact(ctx context.Context, reader io.Reader, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := io.ReadFull(reader, payload)
	return err
}

func validateCacheSource(object CacheObject, source SourceSession) error {
	stat := source.Stat()
	capabilities := source.Capabilities()
	if stat.Size != object.Size || stat.SourceFingerprint != object.SourceFingerprint ||
		stat.EntryFingerprint != object.ContentFingerprint || capabilities.Provider != object.Provider || !capabilities.Sequential {
		return ErrCacheSourceChanged
	}
	return nil
}

func (cache *AuthenticatedCache) reserveMaterialization(key string, object CacheObject) (cacheReservation, *cacheEntry, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if entry := cache.entries[key]; entry != nil && !entry.invalid {
		return cacheReservation{}, entry, nil
	}
	if _, exists := cache.writing[key]; exists {
		return cacheReservation{}, nil, ErrCacheBusy
	}
	if object.Size <= cache.config.MemoryObjectBytes {
		reservation := cacheReservation{tier: CacheTierMemory, bytes: object.Size, ownerID: object.OwnerUserID, provider: object.Provider}
		if cache.quotaFits(reservation) {
			cache.applyQuota(reservation, 1)
			cache.writing[key] = reservation
			return reservation, nil, nil
		}
	}
	files := cacheObjectFiles(object.Size, cache.config.ChunkBytes)
	reservation := cacheReservation{
		tier: CacheTierDisk, bytes: object.Size, files: files,
		ownerID: object.OwnerUserID, provider: object.Provider,
	}
	if cache.status.DiskEnabled && object.Size <= cache.config.ObjectBytes && files <= cache.config.ObjectFiles && cache.quotaFits(reservation) {
		cache.applyQuota(reservation, 1)
		cache.writing[key] = reservation
		return reservation, nil, nil
	}
	return cacheReservation{}, nil, ErrCacheQuota
}

func (cache *AuthenticatedCache) releaseWriting(key string, reservation cacheReservation) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if current, exists := cache.writing[key]; exists && current == reservation {
		delete(cache.writing, key)
		cache.applyQuota(reservation, -1)
	}
}

func (cache *AuthenticatedCache) quotaFits(reservation cacheReservation) bool {
	if reservation.tier == CacheTierMemory {
		return quotaFits(cache.memoryGlobal, reservation.bytes, 0, cache.config.MemoryGlobalBytes, 0) &&
			quotaFits(cache.memoryUsers[reservation.ownerID], reservation.bytes, 0, cache.config.MemoryUserBytes, 0) &&
			quotaFits(cache.memoryProviders[reservation.provider], reservation.bytes, 0, cache.config.MemoryProviderBytes, 0)
	}
	return quotaFits(cache.diskGlobal, reservation.bytes, reservation.files, cache.config.GlobalBytes, cache.config.GlobalFiles) &&
		quotaFits(cache.diskUsers[reservation.ownerID], reservation.bytes, reservation.files, cache.config.UserBytes, cache.config.UserFiles) &&
		quotaFits(cache.diskProviders[reservation.provider], reservation.bytes, reservation.files, cache.config.ProviderBytes, cache.config.ProviderFiles)
}

func (cache *AuthenticatedCache) applyQuota(reservation cacheReservation, direction int64) {
	if reservation.tier == CacheTierMemory {
		cache.memoryGlobal = addCacheUsage(cache.memoryGlobal, reservation.bytes*direction, 0)
		cache.memoryUsers[reservation.ownerID] = addCacheUsage(cache.memoryUsers[reservation.ownerID], reservation.bytes*direction, 0)
		cache.memoryProviders[reservation.provider] = addCacheUsage(cache.memoryProviders[reservation.provider], reservation.bytes*direction, 0)
		return
	}
	cache.diskGlobal = addCacheUsage(cache.diskGlobal, reservation.bytes*direction, reservation.files*direction)
	cache.diskUsers[reservation.ownerID] = addCacheUsage(cache.diskUsers[reservation.ownerID], reservation.bytes*direction, reservation.files*direction)
	cache.diskProviders[reservation.provider] = addCacheUsage(cache.diskProviders[reservation.provider], reservation.bytes*direction, reservation.files*direction)
}

func (cache *AuthenticatedCache) removeEntryLocked(entry *cacheEntry) {
	if entry == nil || cache.entries[entry.key] != entry {
		return
	}
	delete(cache.entries, entry.key)
	cache.applyQuota(cacheReservation{
		tier: entry.tier, bytes: entry.quotaBytes, files: entry.quotaFiles,
		ownerID: entry.object.OwnerUserID, provider: entry.object.Provider,
	}, -1)
}

func (cache *AuthenticatedCache) deleteEntryStorage(entry *cacheEntry) error {
	if entry == nil {
		return nil
	}
	if entry.tier == CacheTierMemory {
		zeroBytes(entry.memory)
		entry.memory = nil
		return nil
	}
	var firstErr error
	for _, chunk := range entry.chunks {
		if cache.removeChunkFile != nil {
			if err := cache.removeChunkFile(chunk.name); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
		}
		cache.trackFile(chunk.name, false)
	}
	entry.chunks = nil
	return firstErr
}

func (cache *AuthenticatedCache) objectKey(object CacheObject) (string, error) {
	if !validCacheObject(object) {
		return "", ErrInvalidCacheBinding
	}
	binding := cache.chunkBinding(object, math.MaxUint64, object.Size)
	return cache.cipher.OpaqueFilename(binding)
}

func (cache *AuthenticatedCache) chunkBinding(object CacheObject, index uint64, length int64) CacheChunkBinding {
	return CacheChunkBinding{
		OwnerUserID: object.OwnerUserID, Provider: object.Provider, Ref: object.Ref,
		CatalogGenerationID: object.CatalogGenerationID, SourceFingerprint: object.SourceFingerprint,
		ContentFingerprint: object.ContentFingerprint, Renderer: object.Renderer, Profile: object.Profile,
		ChunkIndex: index, PlaintextLength: length,
	}
}

func (cache *AuthenticatedCache) beginOperation() error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if !cache.accepting || cache.closed {
		return ErrCacheClosed
	}
	if cache.activeOps == 0 {
		cache.opsDone = make(chan struct{})
	}
	cache.activeOps++
	return nil
}

func (cache *AuthenticatedCache) endOperation() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.activeOps--
	if cache.activeOps == 0 {
		close(cache.opsDone)
	}
}

func (cache *AuthenticatedCache) trackFile(name string, active bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if active {
		cache.activeFiles[name] = true
	} else {
		delete(cache.activeFiles, name)
	}
}

func (cache *AuthenticatedCache) fileTracked(name string) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.activeFiles[name] {
		return true
	}
	for _, entry := range cache.entries {
		for _, chunk := range entry.chunks {
			if chunk.name == name {
				return true
			}
		}
	}
	return false
}

func (cache *AuthenticatedCache) disableDisk(reason CacheDisableReason) {
	var removals []*cacheEntry
	cache.mu.Lock()
	alreadyDisabled := !cache.status.DiskEnabled && cache.status.Reason == reason
	cache.status = CacheStatus{Reason: reason}
	for _, entry := range cache.entries {
		if entry.tier != CacheTierDisk {
			continue
		}
		entry.invalid = true
		if entry.leases == 0 {
			cache.removeEntryLocked(entry)
			removals = append(removals, entry)
		}
	}
	cache.mu.Unlock()
	if !alreadyDisabled {
		if reason == CacheReasonFull {
			cache.metrics.ObserveCache(MetricCacheFull)
		} else {
			cache.metrics.ObserveCache(MetricCacheDisabled)
		}
	}
	for _, entry := range removals {
		_ = cache.deleteEntryStorage(entry)
	}
}

func (cache *AuthenticatedCache) releaseDiskRoot() {
	cache.mu.Lock()
	root, lockFile := cache.root, cache.lockFile
	cache.root, cache.lockFile = nil, nil
	cache.mu.Unlock()
	if lockFile != nil {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}
	if root != nil {
		_ = root.Close()
	}
}

func validCacheConfig(config CacheConfig) bool {
	if config.ChunkBytes <= 0 || config.ObjectBytes < config.ChunkBytes ||
		config.ObjectBytes > config.UserBytes || config.ObjectBytes > config.ProviderBytes ||
		config.UserBytes > config.GlobalBytes || config.ProviderBytes > config.GlobalBytes ||
		config.ObjectFiles <= 0 || config.ObjectFiles > config.UserFiles || config.ObjectFiles > config.ProviderFiles ||
		config.UserFiles > config.GlobalFiles || config.ProviderFiles > config.GlobalFiles ||
		config.MemoryObjectBytes < 0 || config.MemoryObjectBytes > config.MemoryUserBytes ||
		config.MemoryObjectBytes > config.MemoryProviderBytes || config.MemoryUserBytes > config.MemoryGlobalBytes ||
		config.MemoryProviderBytes > config.MemoryGlobalBytes || config.IdleTTL <= 0 ||
		config.AbsoluteTTL < config.IdleTTL || config.ReconcileBatchSize <= 0 {
		return false
	}
	return !config.DiskEnabled || strings.TrimSpace(config.Root) != ""
}

func validCacheObject(object CacheObject) bool {
	return object.Size >= 0 && validCacheChunkBinding(CacheChunkBinding{
		OwnerUserID: object.OwnerUserID, Provider: object.Provider, Ref: object.Ref,
		CatalogGenerationID: object.CatalogGenerationID, SourceFingerprint: object.SourceFingerprint,
		ContentFingerprint: object.ContentFingerprint, Renderer: object.Renderer, Profile: object.Profile,
		PlaintextLength: object.Size,
	})
}

func safeCacheRootPath(path string) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return false
	}
	clean := filepath.Clean(path)
	for _, forbidden := range []string{"/data", "/backup", "/logs"} {
		if pathsRelated(clean, forbidden) {
			return false
		}
	}
	return true
}

func ensureCacheRootPath(path string) error {
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrCacheUnsafeRoot
		}
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	current = string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrCacheUnsafeRoot
		}
	}
	return nil
}

func pathsRelated(first, second string) bool {
	return pathWithin(first, second) || pathWithin(second, first)
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && relative != "." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) || err == nil && relative == "."
}

func defaultCacheMountVerifier(path string) error {
	payload, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil || len(bytes.TrimSpace(payload)) == 0 {
		return ErrCacheUnsafeRoot
	}
	return verifyCacheMountInfo(path, payload)
}

func verifyCacheMountInfo(candidate string, payload []byte) error {
	type mountIdentity struct {
		device string
		root   string
	}
	candidate = filepath.Clean(candidate)
	if !filepath.IsAbs(candidate) || len(bytes.TrimSpace(payload)) == 0 {
		return ErrCacheUnsafeRoot
	}
	mostSpecificLength := -1
	mostSpecificCount := 0
	mostSpecificRoot := ""
	mostSpecificPoint := ""
	mostSpecificIdentity := mountIdentity{}
	mountPoints := make(map[mountIdentity]map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 6 || len(fields) < separator+4 {
			continue
		}
		mountPoint, pointErr := decodeMountInfoPath(fields[4])
		if pointErr != nil || !filepath.IsAbs(mountPoint) || filepath.Clean(mountPoint) != mountPoint {
			continue
		}
		mountRoot, rootErr := decodeMountInfoPath(fields[3])
		if rootErr != nil || !filepath.IsAbs(mountRoot) || filepath.Clean(mountRoot) != mountRoot {
			if pathWithin(candidate, mountPoint) {
				return ErrCacheUnsafeRoot
			}
			continue
		}
		identity := mountIdentity{device: fields[2], root: mountRoot}
		if mountPoints[identity] == nil {
			mountPoints[identity] = make(map[string]struct{})
		}
		mountPoints[identity][mountPoint] = struct{}{}
		if !pathWithin(candidate, mountPoint) {
			continue
		}
		specificity := len(mountPoint)
		switch {
		case specificity > mostSpecificLength:
			mostSpecificLength = specificity
			mostSpecificCount = 1
			mostSpecificRoot = mountRoot
			mostSpecificPoint = mountPoint
			mostSpecificIdentity = identity
		case specificity == mostSpecificLength:
			mostSpecificCount++
		}
	}
	if scanner.Err() != nil || mostSpecificLength < 0 || mostSpecificCount != 1 ||
		mostSpecificPoint != "/" && mostSpecificRoot != "/" || len(mountPoints[mostSpecificIdentity]) != 1 {
		return ErrCacheUnsafeRoot
	}
	return nil
}

func decodeMountInfoPath(value string) (string, error) {
	var result strings.Builder
	result.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' {
			result.WriteByte(value[index])
			continue
		}
		if index+3 >= len(value) {
			return "", ErrCacheUnsafeRoot
		}
		var decoded byte
		switch value[index+1 : index+4] {
		case "040":
			decoded = ' '
		case "011":
			decoded = '\t'
		case "012":
			decoded = '\n'
		case "134":
			decoded = '\\'
		default:
			return "", ErrCacheUnsafeRoot
		}
		result.WriteByte(decoded)
		index += 3
	}
	return result.String(), nil
}

func validCacheFileName(name string) bool {
	base := strings.TrimSuffix(name, ".partial")
	if len(base) != 64 || strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	_, err := hex.DecodeString(base)
	return err == nil
}

func cacheObjectFiles(size, chunkBytes int64) int64 {
	if size == 0 {
		return 1
	}
	return (size+chunkBytes-1)/chunkBytes + 1
}

func quotaFits(current cacheQuotaUsage, bytes, files, maxBytes, maxFiles int64) bool {
	return bytes >= 0 && files >= 0 && current.bytes >= 0 && current.files >= 0 &&
		bytes <= maxBytes-current.bytes && (maxFiles == 0 && files == 0 || maxFiles > 0 && files <= maxFiles-current.files)
}

func addCacheUsage(current cacheQuotaUsage, bytes, files int64) cacheQuotaUsage {
	current.bytes += bytes
	current.files += files
	return current
}

func cacheEntryInfo(entry *cacheEntry) CacheEntryInfo {
	return CacheEntryInfo{Tier: entry.tier, Size: entry.object.Size, RangeCapable: true}
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
