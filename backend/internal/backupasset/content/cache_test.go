package content

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestCacheAEADUsesRandomNonceCanonicalAADAndOpaqueFilename(t *testing.T) {
	cipher, err := NewCacheCipher(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	binding := testCacheChunkBinding()
	plaintext := []byte("FAKE_CACHE_PLAINTEXT_FOR_TEST_ONLY")
	first, err := cipher.Seal(binding, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cipher.Seal(binding, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two chunks reused one nonce/ciphertext")
	}
	if bytes.Contains(first, plaintext) || bytes.Contains(second, plaintext) {
		t.Fatal("plaintext appears in authenticated chunk")
	}
	for _, sealed := range [][]byte{first, second} {
		opened, openErr := cipher.Open(binding, sealed)
		if openErr != nil || !bytes.Equal(opened, plaintext) {
			t.Fatalf("open authenticated chunk=%q err=%v", opened, openErr)
		}
	}
	filename, err := cipher.OpaqueFilename(binding)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(filename) {
		t.Fatalf("non-opaque filename=%q", filename)
	}
	for _, secretPart := range []string{
		binding.Ref.RecoveryPointID, binding.Ref.EntryID, binding.CatalogGenerationID,
		binding.SourceFingerprint, binding.ContentFingerprint,
	} {
		if strings.Contains(filename, secretPart) {
			t.Fatalf("filename contains private identity %q", secretPart)
		}
	}
}

func TestCacheAEADFailsClosedForTamperWrongBindingGenerationChunkAndKey(t *testing.T) {
	cipher, err := NewCacheCipher(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	binding := testCacheChunkBinding()
	plaintext := []byte("FAKE_CACHE_INTEGRITY_PAYLOAD_FOR_TEST_ONLY")
	binding.PlaintextLength = int64(len(plaintext))
	sealed, err := cipher.Seal(binding, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x80
	truncated := append([]byte(nil), sealed[:len(sealed)-1]...)
	generationTampered := append([]byte(nil), sealed...)
	generationTampered[cacheChunkGenerationOffset] ^= 0x01

	wrongOwner := binding
	wrongOwner.OwnerUserID++
	wrongPoint := binding
	wrongPoint.Ref.RecoveryPointID = strings.Repeat("f", 32)
	wrongEntry := binding
	wrongEntry.Ref.EntryID = strings.Repeat("e", 64)
	wrongCatalog := binding
	wrongCatalog.CatalogGenerationID = strings.Repeat("d", 32)
	wrongSource := binding
	wrongSource.SourceFingerprint = "source-v2"
	wrongContent := binding
	wrongContent.ContentFingerprint = "content-v2"
	wrongRenderer := binding
	wrongRenderer.Renderer, wrongRenderer.Profile = RendererMetadataHex, ProfileHexV1
	wrongChunk := binding
	wrongChunk.ChunkIndex++
	wrongLength := binding
	wrongLength.PlaintextLength++
	ambiguousBoundary := binding
	ambiguousBoundary.SourceFingerprint = "s"
	ambiguousBoundary.ContentFingerprint = "ource-v1content-v1"

	tests := []struct {
		name    string
		cipher  *CacheCipher
		binding CacheChunkBinding
		sealed  []byte
	}{
		{name: "tamper", cipher: cipher, binding: binding, sealed: tampered},
		{name: "truncation", cipher: cipher, binding: binding, sealed: truncated},
		{name: "generation", cipher: cipher, binding: binding, sealed: generationTampered},
		{name: "owner", cipher: cipher, binding: wrongOwner, sealed: sealed},
		{name: "point", cipher: cipher, binding: wrongPoint, sealed: sealed},
		{name: "entry", cipher: cipher, binding: wrongEntry, sealed: sealed},
		{name: "catalog", cipher: cipher, binding: wrongCatalog, sealed: sealed},
		{name: "source", cipher: cipher, binding: wrongSource, sealed: sealed},
		{name: "content", cipher: cipher, binding: wrongContent, sealed: sealed},
		{name: "renderer", cipher: cipher, binding: wrongRenderer, sealed: sealed},
		{name: "chunk", cipher: cipher, binding: wrongChunk, sealed: sealed},
		{name: "length", cipher: cipher, binding: wrongLength, sealed: sealed},
		{name: "canonical boundary", cipher: cipher, binding: ambiguousBoundary, sealed: sealed},
	}
	other, err := NewCacheCipher(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tests = append(tests, struct {
		name    string
		cipher  *CacheCipher
		binding CacheChunkBinding
		sealed  []byte
	}{name: "wrong key", cipher: other, binding: binding, sealed: sealed})

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if _, openErr := testCase.cipher.Open(testCase.binding, testCase.sealed); !errors.Is(openErr, ErrCacheIntegrity) {
				t.Fatalf("open error=%v", openErr)
			}
		})
	}
}

func TestCacheAEADRejectsSwappedChunksAndDuplicateNonce(t *testing.T) {
	cipher, err := NewCacheCipher(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	firstBinding := testCacheChunkBinding()
	firstBinding.PlaintextLength = int64(len("first"))
	secondBinding := firstBinding
	secondBinding.ChunkIndex = 1
	secondBinding.PlaintextLength = int64(len("second"))
	first, err := cipher.Seal(firstBinding, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := cipher.Seal(secondBinding, []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipher.Open(firstBinding, second); !errors.Is(err, ErrCacheIntegrity) {
		t.Fatalf("swapped second->first error=%v", err)
	}
	if _, err := cipher.Open(secondBinding, first); !errors.Is(err, ErrCacheIntegrity) {
		t.Fatalf("swapped first->second error=%v", err)
	}

	// 32-byte process key + 16-byte generation + two identical 12-byte nonces.
	deterministic := bytes.NewReader(make([]byte, 32+16+12+12))
	reuseCipher, err := NewCacheCipher(deterministic)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reuseCipher.Seal(firstBinding, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := reuseCipher.Seal(secondBinding, []byte("second")); !errors.Is(err, ErrCacheNonceReuse) {
		t.Fatalf("duplicate nonce error=%v", err)
	}
}

func TestCacheAEADRejectsMalformedBindingsAndZeroesKeys(t *testing.T) {
	cipher, err := NewCacheCipher(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	valid := testCacheChunkBinding()
	invalid := []CacheChunkBinding{
		{},
		func() CacheChunkBinding { value := valid; value.OwnerUserID = 0; return value }(),
		func() CacheChunkBinding { value := valid; value.Provider = backupasset.ProviderCommand; return value }(),
		func() CacheChunkBinding { value := valid; value.PlaintextLength = -1; return value }(),
		func() CacheChunkBinding { value := valid; value.Renderer = Renderer("future"); return value }(),
	}
	for index, binding := range invalid {
		if _, err := cipher.Seal(binding, nil); !errors.Is(err, ErrInvalidCacheBinding) {
			t.Fatalf("invalid binding %d error=%v", index, err)
		}
	}
	cipher.Zero()
	if _, err := cipher.Seal(valid, bytes.Repeat([]byte{'x'}, int(valid.PlaintextLength))); !errors.Is(err, ErrCacheClosed) {
		t.Fatalf("seal after zero error=%v", err)
	}
	if !allZero(cipher.key[:]) || !allZero(cipher.filenameKey[:]) {
		t.Fatal("cache key material not zeroed")
	}
}

func testCacheChunkBinding() CacheChunkBinding {
	return CacheChunkBinding{
		OwnerUserID: 42, Provider: backupasset.ProviderRsync,
		Ref:                 backupasset.AssetRef{RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64)},
		CatalogGenerationID: strings.Repeat("c", 32), SourceFingerprint: "source-v1",
		ContentFingerprint: "content-v1", Renderer: RendererSafeRaster, Profile: ProfileRasterV1,
		ChunkIndex: 0, PlaintextLength: int64(len("FAKE_CACHE_PLAINTEXT_FOR_TEST_ONLY")),
	}
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

func TestCacheRootFailsClosedForForbiddenSymlinkSourceOverlapAndUnverifiedMount(t *testing.T) {
	tests := []struct {
		name      string
		root      func(*testing.T) string
		validator error
		mount     error
		want      CacheDisableReason
	}{
		{name: "forbidden data child", root: func(*testing.T) string { return "/data/asset-content" }, want: CacheReasonUnsafeRoot},
		{name: "forbidden parent", root: func(*testing.T) string { return "/" }, want: CacheReasonUnsafeRoot},
		{name: "symlink component", root: func(t *testing.T) string {
			target := t.TempDir()
			parent := t.TempDir()
			link := filepath.Join(parent, "linked")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(link, "cache")
		}, want: CacheReasonUnsafeRoot},
		{name: "source overlap", root: func(t *testing.T) string { return filepath.Join(t.TempDir(), "cache") }, validator: errors.New("source overlap"), want: CacheReasonSourceOverlap},
		{name: "mount unverified", root: func(t *testing.T) string { return filepath.Join(t.TempDir(), "cache") }, mount: errors.New("mount unavailable"), want: CacheReasonRootUnverified},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			config := testCacheConfig(testCase.root(t))
			cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
				Config: config, Now: time.Now, Random: rand.Reader,
				SourceRoots: &cacheRootValidatorFake{err: testCase.validator},
				VerifyMount: func(string) error { return testCase.mount },
			})
			if err != nil {
				t.Fatal(err)
			}
			status := cache.Status()
			if status.DiskEnabled || status.Reason != testCase.want {
				t.Fatalf("cache status=%+v", status)
			}
			if err := cache.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCacheRootRejectsReplacementBetweenValidationAndOpen(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "cache")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	originalRoot := filepath.Join(parent, "cache-original")
	externalRoot := filepath.Join(parent, "external")
	if err := os.Mkdir(externalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(externalRoot, "do-not-delete")
	if err := os.WriteFile(sentinel, []byte("external-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	validator := &cacheRootValidatorFake{validate: func(_ string) error {
		if err := os.Rename(rootPath, originalRoot); err != nil {
			return err
		}
		return os.Symlink(externalRoot, rootPath)
	}}
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: testCacheConfig(rootPath), Now: time.Now, Random: rand.Reader,
		SourceRoots: validator, VerifyMount: func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cache.Shutdown(context.Background()) }()

	if status := cache.Status(); status.DiskEnabled || status.Reason != CacheReasonRootUnverified {
		t.Fatalf("replaced cache root status=%+v", status)
	}
	payload, err := os.ReadFile(sentinel)
	if err != nil || string(payload) != "external-data" {
		t.Fatalf("cache initialization followed replaced root: payload=%q err=%v", payload, err)
	}
}

func TestCacheMountInfoRejectsBindSubtreeAndAmbiguousEvidence(t *testing.T) {
	rootMount := "36 25 0:32 / / rw,relatime - overlay overlay rw\n"
	tests := []struct {
		name      string
		candidate string
		mountInfo string
		valid     bool
	}{
		{name: "root filesystem", candidate: "/var/cache/xirang/asset-content", mountInfo: rootMount, valid: true},
		{name: "root filesystem subvolume", candidate: "/var/cache/xirang/asset-content",
			mountInfo: "36 25 0:32 /@ / rw,relatime - btrfs /dev/sda rw\n", valid: true},
		{name: "dedicated filesystem root", candidate: "/var/cache/xirang/asset-content", mountInfo: rootMount +
			"40 36 0:45 / /var/cache/xirang rw,nosuid - tmpfs tmpfs rw\n", valid: true},
		{name: "escaped dedicated filesystem root", candidate: "/var/cache/xirang content/asset-content", mountInfo: rootMount +
			"40 36 0:45 / /var/cache/xirang\\040content rw,nosuid - tmpfs tmpfs rw\n", valid: true},
		{name: "bind source subtree", candidate: "/var/cache/xirang/asset-content", mountInfo: rootMount +
			"40 36 0:32 /backup/source /var/cache/xirang rw,relatime - ext4 /dev/sda rw\n"},
		{name: "filesystem root bind alias", candidate: "/var/cache/xirang/asset-content", mountInfo: rootMount +
			"40 36 0:45 / /backup/source rw,relatime - ext4 /dev/sdb rw\n" +
			"41 36 0:45 / /var/cache/xirang rw,relatime - ext4 /dev/sdb rw\n"},
		{name: "ambiguous duplicate mountpoint", candidate: "/var/cache/xirang/asset-content", mountInfo: rootMount +
			"40 36 0:45 / /var/cache/xirang rw - tmpfs tmpfs rw\n" +
			"41 36 0:46 / /var/cache/xirang rw - tmpfs tmpfs rw\n"},
		{name: "missing covering mount", candidate: "/var/cache/xirang/asset-content", mountInfo: "40 36 0:45 / /srv rw - tmpfs tmpfs rw\n"},
		{name: "malformed", candidate: "/var/cache/xirang/asset-content", mountInfo: "not mountinfo\n"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := verifyCacheMountInfo(testCase.candidate, []byte(testCase.mountInfo))
			if testCase.valid && err != nil {
				t.Fatalf("safe mount evidence rejected: %v", err)
			}
			if !testCase.valid && !errors.Is(err, ErrCacheUnsafeRoot) {
				t.Fatalf("unsafe mount evidence error=%v", err)
			}
		})
	}
}

func TestDefaultCacheMountVerifierAcceptsDedicatedTempFilesystem(t *testing.T) {
	root := filepath.Join(t.TempDir(), "asset-content")
	if err := defaultCacheMountVerifier(root); err != nil {
		t.Fatalf("default mount verifier rejected %q: %v", root, err)
	}
}

func TestCacheRootStartupDeletesRegularOrphansAndRejectsSpecialEntriesWithoutFollowing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, strings.Repeat("a", 64))
	if err := os.WriteFile(orphan, []byte("old ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := newDiskCacheForTest(t, testCacheConfig(root))
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup orphan remains: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("cache root mode=%v err=%v", info.Mode(), err)
	}
	if err := cache.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	root = filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "hostile")); err != nil {
		t.Fatal(err)
	}
	config := testCacheConfig(root)
	cache, err = NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: config, Now: time.Now, Random: rand.Reader, SourceRoots: &cacheRootValidatorFake{},
		VerifyMount: func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if status := cache.Status(); status.DiskEnabled || status.Reason != CacheReasonUnsafeRoot {
		t.Fatalf("special-entry status=%+v", status)
	}
	payload, err := os.ReadFile(target)
	if err != nil || string(payload) != "do-not-touch" {
		t.Fatalf("symlink target changed: %q err=%v", payload, err)
	}
	if err := cache.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	root = filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lockTarget := filepath.Join(t.TempDir(), "lock-target")
	if err := os.WriteFile(lockTarget, []byte("do-not-open-through-symlink"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(lockTarget, filepath.Join(root, cacheProcessLockName)); err != nil {
		t.Fatal(err)
	}
	cache, err = NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: testCacheConfig(root), Now: time.Now, Random: rand.Reader, SourceRoots: &cacheRootValidatorFake{},
		VerifyMount: func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if status := cache.Status(); status.DiskEnabled {
		t.Fatalf("symlink lock file enabled disk cache: %+v", status)
	}
	if payload, readErr := os.ReadFile(lockTarget); readErr != nil || string(payload) != "do-not-open-through-symlink" {
		t.Fatalf("lock symlink target changed: %q err=%v", payload, readErr)
	}
	if err := cache.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCacheMaterializesBoundedMemoryAndPartitionsIdentityByOwner(t *testing.T) {
	config := testCacheConfig("")
	config.DiskEnabled = false
	clock := &budgetTestClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: config, Now: clock.Now, Random: rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	object := testCacheObject(int64(len("0123456789abcdef")))
	source := newCacheSourceFake([]byte("0123456789abcdef"), object)
	info, err := cache.Materialize(context.Background(), object, source)
	if err != nil || info.Tier != CacheTierMemory || !info.RangeCapable || info.ProviderBytes != object.Size ||
		source.readerCalls != 1 || source.closeCalls != 1 {
		t.Fatalf("memory materialization=%+v source=%+v err=%v", info, source, err)
	}
	lease, err := cache.OpenRange(object, 5, 7)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(lease)
	if err != nil || string(payload) != "56789ab" {
		t.Fatalf("memory range=%q err=%v", payload, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}

	hitSource := newCacheSourceFake([]byte("must-not-read"), object)
	hit, err := cache.Materialize(context.Background(), object, hitSource)
	if err != nil {
		t.Fatal(err)
	}
	if hit.ProviderBytes != 0 || hitSource.readerCalls != 0 || hitSource.closeCalls != 1 {
		t.Fatalf("cache hit=%+v touched source reader: %+v", hit, hitSource)
	}
	otherOwner := object
	otherOwner.OwnerUserID++
	if _, err := cache.OpenRange(otherOwner, 0, 1); !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("cross-owner cache read error=%v", err)
	}
}

func TestCacheShutdownDoesNotFollowReplacedRootPath(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "cache")
	cache := newDiskCacheForTest(t, testCacheConfig(root))

	movedRoot := filepath.Join(parent, "cache-moved")
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(parent, "external")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(external, cacheProcessLockName)
	if err := os.WriteFile(sentinel, []byte("do-not-delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, root); err != nil {
		t.Fatal(err)
	}

	if err := cache.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(sentinel)
	if err != nil || string(payload) != "do-not-delete" {
		t.Fatalf("shutdown followed replaced root path: payload=%q err=%v", payload, err)
	}
}

func TestCacheStartupCleanupEnumeratesStableRootAfterPathReplacement(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "cache")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	orphanName := "orphan-cache-chunk"
	if err := os.WriteFile(filepath.Join(rootPath, orphanName), []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	movedRoot := filepath.Join(parent, "cache-moved")
	if err := os.Rename(rootPath, movedRoot); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(parent, "replacement")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, rootPath); err != nil {
		t.Fatal(err)
	}

	cache := &AuthenticatedCache{rootPath: rootPath, root: root, metrics: NoopMetrics{}}
	if reason := cache.cleanStartupRoot(); reason != CacheReasonNone {
		t.Fatalf("stable-root cleanup reason=%s", reason)
	}
	if _, err := os.Stat(filepath.Join(movedRoot, orphanName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan remained in stable root: %v", err)
	}
}

func TestCacheReconcileEnumeratesStableRootAfterPathReplacement(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "cache")
	cache := newDiskCacheForTest(t, testCacheConfig(rootPath))
	orphanName := "periodic-orphan-cache-chunk"
	if err := cache.root.WriteFile(orphanName, []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	movedRoot := filepath.Join(parent, "cache-moved")
	if err := os.Rename(rootPath, movedRoot); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(parent, "replacement")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, rootPath); err != nil {
		t.Fatal(err)
	}

	if err := cache.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(movedRoot, orphanName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("periodic orphan remained in stable root: %v", err)
	}
	if err := cache.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCacheDiskChunksAreAuthenticatedOpaqueAndRangeReadableOnlyAfterCommit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	config := testCacheConfig(root)
	config.MemoryObjectBytes = 4
	config.ChunkBytes = 8
	cache := newDiskCacheForTest(t, config)
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	plaintext := []byte("0123456789abcdefghijkl")
	object := testCacheObject(int64(len(plaintext)))
	source := newCacheSourceFake(plaintext, object)
	info, err := cache.Materialize(context.Background(), object, source)
	if err != nil || info.Tier != CacheTierDisk || !info.RangeCapable {
		t.Fatalf("disk materialization=%+v err=%v", info, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	chunkCount := 0
	for _, entry := range entries {
		if entry.Name() == cacheProcessLockName {
			continue
		}
		chunkCount++
		if strings.Contains(entry.Name(), ".partial") || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(entry.Name()) {
			t.Fatalf("unsafe cache filename=%q", entry.Name())
		}
		sealed, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		if readErr != nil || bytes.Contains(sealed, plaintext) || bytes.Contains(sealed, []byte("01234567")) {
			t.Fatalf("plaintext leaked in %q err=%v", entry.Name(), readErr)
		}
	}
	if chunkCount != 3 {
		t.Fatalf("chunk count=%d want=3", chunkCount)
	}
	lease, err := cache.OpenRange(object, 6, 13)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(lease)
	if closeErr := lease.Close(); err == nil {
		err = closeErr
	}
	if err != nil || string(payload) != "6789abcdefghi" {
		t.Fatalf("disk range=%q err=%v", payload, err)
	}

	entry := cacheEntryForTest(t, cache, object)
	chunkPath := filepath.Join(root, entry.chunks[0].name)
	sealed, err := os.ReadFile(chunkPath)
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 0x40
	if err := os.WriteFile(chunkPath, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	lease, err = cache.OpenRange(object, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(lease)
	_ = lease.Close()
	if !errors.Is(err, ErrCacheIntegrity) {
		t.Fatalf("tampered disk chunk error=%v", err)
	}
}

func TestCacheReservesQuotaBeforeSourceAndActiveLeaseBlocksEviction(t *testing.T) {
	config := testCacheConfig("")
	config.DiskEnabled = false
	config.MemoryObjectBytes = 8
	config.MemoryUserBytes = 10
	config.MemoryProviderBytes = 32
	config.MemoryGlobalBytes = 32
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{Config: config, Now: time.Now, Random: rand.Reader})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	first := testCacheObject(6)
	if _, err := cache.Materialize(context.Background(), first, newCacheSourceFake([]byte("first!"), first)); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ContentFingerprint = "content-v2"
	blockedSource := newCacheSourceFake([]byte("second"), second)
	if _, err := cache.Materialize(context.Background(), second, blockedSource); !errors.Is(err, ErrCacheQuota) {
		t.Fatalf("user quota error=%v", err)
	}
	if blockedSource.readerCalls != 0 || blockedSource.closeCalls != 1 {
		t.Fatalf("quota rejection touched source bytes: %+v", blockedSource)
	}
	lease, err := cache.OpenRange(first, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Evict(first); !errors.Is(err, ErrCacheBusy) {
		t.Fatalf("leased eviction error=%v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cache.Evict(first); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Materialize(context.Background(), second, blockedSource); err != nil {
		t.Fatalf("quota was not released after eviction: %v", err)
	}
}

func TestCacheBeginMaterializationReservesBeforeSourceAndAbortReleasesQuota(t *testing.T) {
	config := testCacheConfig("")
	config.DiskEnabled = false
	config.MemoryObjectBytes = 8
	config.MemoryUserBytes = 8
	config.MemoryProviderBytes = 16
	config.MemoryGlobalBytes = 16
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: config, Now: time.Now, Random: rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })

	first := testCacheObject(8)
	reservation, hit, err := cache.BeginMaterialization(first)
	if err != nil || reservation == nil || hit.RangeCapable {
		t.Fatalf("first reservation=%v hit=%+v err=%v", reservation, hit, err)
	}
	second := first
	second.ContentFingerprint = "content-v2"
	if pending, _, reserveErr := cache.BeginMaterialization(second); pending != nil || !errors.Is(reserveErr, ErrCacheQuota) {
		t.Fatalf("second reservation=%v err=%v", pending, reserveErr)
	}
	if err := reservation.Abort(); err != nil {
		t.Fatal(err)
	}
	reservation, _, err = cache.BeginMaterialization(second)
	if err != nil || reservation == nil {
		t.Fatalf("reservation after abort=%v err=%v", reservation, err)
	}
	if err := reservation.Abort(); err != nil {
		t.Fatal(err)
	}
}

func TestCachePartialCancelSourceDriftAndENOSPCNeverPublishOrFallBackPlaintext(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*AuthenticatedCache, *cacheSourceFake)
		context    func() context.Context
		wantReason CacheDisableReason
	}{
		{name: "short source", configure: func(_ *AuthenticatedCache, source *cacheSourceFake) { source.payload = source.payload[:3] }},
		{name: "source drift", configure: func(_ *AuthenticatedCache, source *cacheSourceFake) { source.closeErr = errors.New("source changed") }},
		{name: "canceled", context: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }},
		{name: "enospc", configure: func(cache *AuthenticatedCache, _ *cacheSourceFake) {
			cache.writeChunkFile = func(string, []byte) error { return syscall.ENOSPC }
		}, wantReason: CacheReasonFull},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "cache")
			config := testCacheConfig(root)
			config.MemoryObjectBytes = 1
			cache := newDiskCacheForTest(t, config)
			object := testCacheObject(8)
			source := newCacheSourceFake([]byte("12345678"), object)
			if testCase.configure != nil {
				testCase.configure(cache, source)
			}
			ctx := context.Background()
			if testCase.context != nil {
				ctx = testCase.context()
			}
			if _, err := cache.Materialize(ctx, object, source); err == nil {
				t.Fatal("failed materialization unexpectedly succeeded")
			}
			if _, err := cache.OpenRange(object, 0, 1); !errors.Is(err, ErrCacheMiss) {
				t.Fatalf("partial object became readable: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != cacheProcessLockName {
					t.Fatalf("partial ciphertext remains: %q", entry.Name())
				}
			}
			if testCase.wantReason != "" && cache.Status().Reason != testCase.wantReason {
				t.Fatalf("cache status=%+v", cache.Status())
			}
			if err := cache.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCacheENOSPCAfterPartialCiphertextWriteRemovesTheIncompleteFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	config := testCacheConfig(root)
	config.MemoryObjectBytes = 1
	cache := newDiskCacheForTest(t, config)
	object := testCacheObject(8)
	source := newCacheSourceFake([]byte("12345678"), object)
	cache.writeChunkFile = func(name string, payload []byte) error {
		if err := os.WriteFile(filepath.Join(root, name), payload[:len(payload)/2], 0o600); err != nil {
			return err
		}
		return syscall.ENOSPC
	}
	if _, err := cache.Materialize(context.Background(), object, source); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("materialize error=%v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != cacheProcessLockName {
			t.Fatalf("partial ciphertext remains after ENOSPC: %q", entry.Name())
		}
	}
	if err := cache.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCacheDiskPublishWaitsForSourceCloseRevalidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	config := testCacheConfig(root)
	config.MemoryObjectBytes = 1
	cache := newDiskCacheForTest(t, config)
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	object := testCacheObject(8)
	source := newCacheSourceFake([]byte("12345678"), object)
	source.closeErr = errors.New("FAKE_SOURCE_DRIFT_FOR_TEST_ONLY")
	renameCalls := 0
	cache.renameChunkFile = func(oldName, newName string) error {
		renameCalls++
		return cache.root.Rename(oldName, newName)
	}
	if _, err := cache.Materialize(context.Background(), object, source); !errors.Is(err, ErrCacheSourceChanged) {
		t.Fatalf("materialize error=%v", err)
	}
	if renameCalls != 0 {
		t.Fatalf("final cache rename ran %d time(s) before source close validation", renameCalls)
	}
}

func TestCacheDiskDisableRejectsPreviouslyCommittedEntries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	config := testCacheConfig(root)
	config.MemoryObjectBytes = 1
	cache := newDiskCacheForTest(t, config)
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	object := testCacheObject(8)
	if _, err := cache.Materialize(context.Background(), object, newCacheSourceFake([]byte("12345678"), object)); err != nil {
		t.Fatal(err)
	}
	cache.disableDisk(CacheReasonUnsafeRoot)
	lease, err := cache.OpenRange(object, 0, 1)
	if lease != nil {
		_ = lease.Close()
	}
	if !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("disabled disk cache reopened committed entry: %v", err)
	}
}

func TestCacheMetricsObserveStartupKeyLossPeriodicOrphanAndDisable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, strings.Repeat("a", 64)), []byte("old generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	metrics := newBrokerMetricsFake()
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: testCacheConfig(root), Now: time.Now, Random: rand.Reader,
		SourceRoots: &cacheRootValidatorFake{}, VerifyMount: func(string) error { return nil }, Metrics: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	if metrics.cacheCount(MetricCacheKeyLoss) != 1 {
		t.Fatalf("startup key-loss metrics=%+v", metrics.snapshot())
	}
	if err := os.WriteFile(filepath.Join(root, strings.Repeat("b", 64)), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cache.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	cache.disableDisk(CacheReasonUnsafeRoot)
	if metrics.cacheCount(MetricCacheOrphan) != 1 || metrics.cacheCount(MetricCacheDisabled) != 1 {
		t.Fatalf("cache lifecycle metrics=%+v", metrics.snapshot())
	}
}

func TestCacheTTLOrphanReconciliationAndShutdownWaitForLeases(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	config := testCacheConfig(root)
	config.MemoryObjectBytes = 1
	config.IdleTTL = time.Minute
	config.AbsoluteTTL = 2 * time.Minute
	clock := &budgetTestClock{now: time.Date(2026, 7, 18, 13, 0, 0, 0, time.UTC)}
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: config, Now: clock.Now, Random: rand.Reader, SourceRoots: &cacheRootValidatorFake{},
		VerifyMount: func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	object := testCacheObject(8)
	if _, err := cache.Materialize(context.Background(), object, newCacheSourceFake([]byte("12345678"), object)); err != nil {
		t.Fatal(err)
	}
	lease, err := cache.OpenRange(object, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(3 * time.Minute)
	if err := cache.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.OpenRange(object, 0, 1); !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("expired object admitted new lease: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, cacheEntryForTest(t, cache, object).chunks[0].name)); err != nil {
		t.Fatalf("active leased entry was deleted: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cache.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cacheHasEntryForTest(cache, object) {
		t.Fatal("expired unleased entry remains")
	}
	orphan := strings.Repeat("f", 64)
	if err := os.WriteFile(filepath.Join(root, orphan), []byte("orphan ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cache.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, orphan)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan remains: %v", err)
	}

	object.ContentFingerprint = "content-v2"
	if _, err := cache.Materialize(context.Background(), object, newCacheSourceFake([]byte("abcdefgh"), object)); err != nil {
		t.Fatal(err)
	}
	lease, err = cache.OpenRange(object, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- cache.Shutdown(context.Background()) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned with active lease: %v", err)
	default:
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if !allZero(cache.cipher.key[:]) {
		t.Fatal("shutdown did not zero process key")
	}
}

type cacheRootValidatorFake struct {
	err      error
	validate func(string) error
}

func (fake *cacheRootValidatorFake) ValidateContentCacheRoot(_ context.Context, root string) error {
	if fake.validate != nil {
		return fake.validate(root)
	}
	return fake.err
}

type cacheSourceReaderFake struct {
	*bytes.Reader
	providerBytes int64
}

func (reader *cacheSourceReaderFake) Close() error { return nil }

func (reader *cacheSourceReaderFake) ProviderBytes() int64 { return reader.providerBytes }

type cacheSourceFake struct {
	mu          sync.Mutex
	payload     []byte
	object      CacheObject
	readerCalls int
	closeCalls  int
	closeErr    error
}

func newCacheSourceFake(payload []byte, object CacheObject) *cacheSourceFake {
	return &cacheSourceFake{payload: append([]byte(nil), payload...), object: object}
}

func (source *cacheSourceFake) Stat() SourceStat {
	return SourceStat{
		Size: int64(len(source.payload)), SourceFingerprint: source.object.SourceFingerprint,
		EntryFingerprint: source.object.ContentFingerprint,
	}
}

func (source *cacheSourceFake) Capabilities() SourceCapabilities {
	return SourceCapabilities{Provider: source.object.Provider, Sequential: true}
}

func (source *cacheSourceFake) Reader() SourceReader {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.readerCalls++
	return &cacheSourceReaderFake{Reader: bytes.NewReader(source.payload), providerBytes: int64(len(source.payload))}
}

func (*cacheSourceFake) Revalidate(context.Context) error { return nil }

func (source *cacheSourceFake) Close() error {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.closeCalls++
	return source.closeErr
}

func testCacheObject(size int64) CacheObject {
	return CacheObject{
		OwnerUserID: 42, Provider: backupasset.ProviderRsync,
		Ref:                 backupasset.AssetRef{RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64)},
		CatalogGenerationID: strings.Repeat("c", 32), SourceFingerprint: "source-v1",
		ContentFingerprint: "content-v1", Renderer: RendererSafeRaster, Profile: ProfileRasterV1, Size: size,
	}
}

func testCacheConfig(root string) CacheConfig {
	return CacheConfig{
		DiskEnabled: true, Root: root, ChunkBytes: 8,
		ObjectBytes: 1 << 20, UserBytes: 1 << 20, ProviderBytes: 1 << 20, GlobalBytes: 1 << 20,
		ObjectFiles: 1_000, UserFiles: 1_000, ProviderFiles: 1_000, GlobalFiles: 1_000,
		MemoryObjectBytes: 64, MemoryUserBytes: 1 << 20, MemoryProviderBytes: 1 << 20, MemoryGlobalBytes: 1 << 20,
		IdleTTL: time.Minute, AbsoluteTTL: time.Hour, ReconcileBatchSize: 100,
	}
}

func newDiskCacheForTest(t *testing.T, config CacheConfig) *AuthenticatedCache {
	t.Helper()
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: config, Now: time.Now, Random: rand.Reader, SourceRoots: &cacheRootValidatorFake{},
		VerifyMount: func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cache.Status().DiskEnabled {
		t.Fatalf("disk cache disabled: %+v", cache.Status())
	}
	return cache
}

func cacheEntryForTest(t *testing.T, cache *AuthenticatedCache, object CacheObject) *cacheEntry {
	t.Helper()
	key, err := cache.objectKey(object)
	if err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry := cache.entries[key]
	if entry == nil {
		t.Fatal("cache entry is missing")
	}
	return entry
}

func cacheHasEntryForTest(cache *AuthenticatedCache, object CacheObject) bool {
	key, err := cache.objectKey(object)
	if err != nil {
		return false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.entries[key] != nil
}
