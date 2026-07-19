package backupasset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestEnsureRequiredDomainsCreatesIndependentRandomKeys(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	materials, err := ring.EnsureRequiredDomains(context.Background())
	if err != nil {
		t.Fatalf("EnsureRequiredDomains: %v", err)
	}
	if len(materials) != len(RequiredKeyDomains) {
		t.Fatalf("got %d domain keys, want %d", len(materials), len(RequiredKeyDomains))
	}
	seen := make(map[string]KeyDomain)
	for _, domain := range RequiredKeyDomains {
		material, ok := materials[domain]
		if !ok || len(material.Key) != secure.DomainKeySize || material.Version != 1 || material.State != DomainKeyActive {
			t.Fatalf("invalid material for %s: %+v", domain, material)
		}
		encoded := string(material.Key)
		if previous, duplicate := seen[encoded]; duplicate {
			t.Fatalf("domains %s and %s share key material", previous, domain)
		}
		seen[encoded] = domain
	}
}

func TestSearchTokenDomainIsIndependentAndRandom(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	required, err := ring.EnsureRequiredDomains(ctx)
	if err != nil {
		t.Fatalf("EnsureRequiredDomains: %v", err)
	}
	if _, bootRequired := required[KeyDomainSearchToken]; bootRequired {
		t.Fatal("search token key was created by unconditional required-domain startup")
	}
	searchKey, err := ring.Ensure(ctx, KeyDomainSearchToken)
	if err != nil {
		t.Fatalf("Ensure search token: %v", err)
	}
	if searchKey.Domain != KeyDomainSearchToken || searchKey.Version != 1 || len(searchKey.Key) != secure.DomainKeySize {
		t.Fatalf("invalid search token material: %+v", searchKey)
	}
	for domain, material := range required {
		if bytes.Equal(searchKey.Key, material.Key) {
			t.Fatalf("search token reuses %s key bytes", domain)
		}
	}
}

func TestDerivedStoreDomainIsOptionalIndependentAndRotatable(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	required, err := ring.EnsureRequiredDomains(ctx)
	if err != nil {
		t.Fatalf("EnsureRequiredDomains: %v", err)
	}
	if _, bootRequired := required[KeyDomainDerivedStore]; bootRequired {
		t.Fatal("derived store key was created by unconditional required-domain startup")
	}
	before, err := ring.Ensure(ctx, KeyDomainDerivedStore)
	if err != nil {
		t.Fatalf("Ensure derived store: %v", err)
	}
	if before.Domain != KeyDomainDerivedStore || before.Version != 1 || len(before.Key) != secure.DomainKeySize {
		t.Fatalf("invalid derived store material: %+v", before)
	}
	for domain, material := range required {
		if bytes.Equal(before.Key, material.Key) {
			t.Fatalf("derived store reuses %s key bytes", domain)
		}
	}
	after, err := ring.Rotate(ctx, KeyDomainDerivedStore, 0)
	if err != nil {
		t.Fatalf("Rotate derived store: %v", err)
	}
	if after.Version != before.Version+1 || bytes.Equal(after.Key, before.Key) {
		t.Fatalf("derived rotation did not create independent next key: before=%+v after=%+v", before, after)
	}
	old, err := ring.ByVersion(ctx, KeyDomainDerivedStore, before.Version)
	if err != nil || old.State != DomainKeyVerifyOnly || !bytes.Equal(old.Key, before.Key) {
		t.Fatalf("old derived key is not verify-only: old=%+v err=%v", old, err)
	}
}

func TestDerivedStoreLossRequiresProjectionSafeInvalidation(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	material, err := ring.Ensure(ctx, KeyDomainDerivedStore)
	if err != nil {
		t.Fatalf("Ensure derived store: %v", err)
	}
	if err := ring.MarkLost(ctx, KeyDomainDerivedStore, material.Version); !errors.Is(err, ErrKeyRotationProhibited) {
		t.Fatalf("MarkLost derived store without invalidation got %v, want ErrKeyRotationProhibited", err)
	}
	callbackObservedActive := false
	err = ring.MarkRebuildableLost(ctx, KeyDomainDerivedStore, material.Version, func(
		_ context.Context, tx *gorm.DB, transition RebuildableKeyTransition,
	) error {
		var active model.WrappedDomainKey
		if err := tx.Where("domain = ? AND state = ?", KeyDomainDerivedStore, DomainKeyActive).First(&active).Error; err != nil {
			return err
		}
		callbackObservedActive = active.Version == material.Version && transition.Domain == KeyDomainDerivedStore && transition.NextVersion == 0
		return nil
	})
	if err != nil {
		t.Fatalf("MarkRebuildableLost derived store: %v", err)
	}
	if !callbackObservedActive {
		t.Fatal("derived projections were not invalidated before the key became lost")
	}
	if _, err := ring.Ensure(ctx, KeyDomainDerivedStore); !errors.Is(err, ErrKeyLost) {
		t.Fatalf("Ensure silently regenerated lost derived store key: %v", err)
	}
}

func TestDomainScopedRewrapIsolatesUnreadableDerivedMaterial(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	search, err := ring.Ensure(ctx, KeyDomainSearchToken)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := ring.Ensure(ctx, KeyDomainDerivedStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := ring.db.Model(&model.WrappedDomainKey{}).
		Where("domain = ? AND version = ?", KeyDomainDerivedStore, derived.Version).
		Update("wrapped_key", []byte("corrupt-derived-envelope")).Error; err != nil {
		t.Fatal(err)
	}

	count, err := ring.RewrapDomains(ctx, KeyDomainSearchToken)
	if err != nil || count != 1 {
		t.Fatalf("RewrapDomains(search) count=%d err=%v", count, err)
	}
	after, err := ring.Active(ctx, KeyDomainSearchToken)
	if err != nil || after.ID != search.ID || after.Version != search.Version || !bytes.Equal(after.Key, search.Key) {
		t.Fatalf("scoped rewrap changed or lost Search key: before=%+v after=%+v err=%v", search, after, err)
	}
	if _, err := ring.Active(ctx, KeyDomainDerivedStore); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("corrupt Derived material unexpectedly became readable: %v", err)
	}
	if _, err := ring.RewrapAll(ctx); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("full rewrap did not report corrupt Derived material: %v", err)
	}
}

func TestSearchTokenOrdinaryRotationIsProhibited(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	material, err := ring.Ensure(ctx, KeyDomainSearchToken)
	if err != nil {
		t.Fatalf("Ensure search token: %v", err)
	}
	if _, err := ring.Rotate(ctx, KeyDomainSearchToken, time.Hour); !errors.Is(err, ErrKeyRotationProhibited) {
		t.Fatalf("Rotate search token got %v, want ErrKeyRotationProhibited", err)
	}
	if err := ring.MarkLost(ctx, KeyDomainSearchToken, material.Version); !errors.Is(err, ErrKeyRotationProhibited) {
		t.Fatalf("MarkLost search token without invalidation got %v, want ErrKeyRotationProhibited", err)
	}
}

func TestSearchTokenKEKRewrapPreservesKeyAndVersion(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	before, err := ring.Ensure(ctx, KeyDomainSearchToken)
	if err != nil {
		t.Fatalf("Ensure search token: %v", err)
	}
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_KEYRING_NEW_SEARCH_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "FAKE_KEYRING_OLD_KEK_FOR_TEST_ONLY")
	secure.ResetForTesting()
	if _, err := ring.RewrapAll(ctx); err != nil {
		t.Fatalf("RewrapAll: %v", err)
	}
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	after, err := ring.Active(ctx, KeyDomainSearchToken)
	if err != nil {
		t.Fatalf("Active rewrapped search token: %v", err)
	}
	if before.ID != after.ID || before.Version != after.Version || !bytes.Equal(before.Key, after.Key) {
		t.Fatalf("rewrap changed search token identity/version/material: before=%+v after=%+v", before, after)
	}
}

func TestSearchTokenLossDoesNotRegenerate(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	material, err := ring.Ensure(ctx, KeyDomainSearchToken)
	if err != nil {
		t.Fatalf("Ensure search token: %v", err)
	}
	invalidated := false
	err = ring.MarkRebuildableLost(ctx, KeyDomainSearchToken, material.Version, func(
		_ context.Context, _ *gorm.DB, transition RebuildableKeyTransition,
	) error {
		invalidated = true
		if transition.Domain != KeyDomainSearchToken || transition.PreviousVersion != material.Version || transition.NextVersion != 0 {
			t.Fatalf("unexpected loss transition: %+v", transition)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("MarkRebuildableLost: %v", err)
	}
	if !invalidated {
		t.Fatal("search projections were not invalidated before loss")
	}
	if _, err := ring.Ensure(ctx, KeyDomainSearchToken); !errors.Is(err, ErrKeyLost) {
		t.Fatalf("Ensure silently regenerated lost search token: %v", err)
	}
	var count int64
	if err := ring.db.Model(&model.WrappedDomainKey{}).Where("domain = ?", KeyDomainSearchToken).Count(&count).Error; err != nil {
		t.Fatalf("count search token rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("lost search token was replaced; row count=%d", count)
	}
}

func TestSearchTokenLossRejectsRetiredVersionWithoutInvalidation(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	before, err := ring.Ensure(ctx, KeyDomainSearchToken)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ring.ReplaceRebuildable(ctx, KeyDomainSearchToken, func(context.Context, *gorm.DB, RebuildableKeyTransition) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	invalidated := false
	err = ring.MarkRebuildableLost(ctx, KeyDomainSearchToken, before.Version, func(context.Context, *gorm.DB, RebuildableKeyTransition) error {
		invalidated = true
		return nil
	})
	if !errors.Is(err, ErrKeyUnavailable) || invalidated {
		t.Fatalf("retired Search Token loss err=%v invalidated=%t", err, invalidated)
	}
	active, err := ring.Active(ctx, KeyDomainSearchToken)
	if err != nil || active.Version != after.Version {
		t.Fatalf("retired loss changed active Search Token: active=%+v err=%v", active, err)
	}
}

func TestSearchTokenReplacementInvalidatesBeforeActivation(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	before, err := ring.Ensure(ctx, KeyDomainSearchToken)
	if err != nil {
		t.Fatalf("Ensure search token: %v", err)
	}
	callbackObservedOldActive := false
	after, err := ring.ReplaceRebuildable(ctx, KeyDomainSearchToken, func(
		_ context.Context, tx *gorm.DB, transition RebuildableKeyTransition,
	) error {
		var active model.WrappedDomainKey
		if err := tx.Where("domain = ? AND state = ?", KeyDomainSearchToken, DomainKeyActive).First(&active).Error; err != nil {
			return err
		}
		callbackObservedOldActive = active.Version == before.Version && transition.NextVersion == before.Version+1
		return nil
	})
	if err != nil {
		t.Fatalf("ReplaceRebuildable: %v", err)
	}
	if !callbackObservedOldActive {
		t.Fatal("replacement activated new key before the invalidation callback")
	}
	if after.Version != before.Version+1 || bytes.Equal(after.Key, before.Key) {
		t.Fatalf("replacement did not create independent next material: before=%+v after=%+v", before, after)
	}

	callbackErr := errors.New("invalidation failed")
	if _, err := ring.ReplaceRebuildable(ctx, KeyDomainSearchToken, func(context.Context, *gorm.DB, RebuildableKeyTransition) error {
		return callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("failed invalidation got %v, want callback error", err)
	}
	stillActive, err := ring.Active(ctx, KeyDomainSearchToken)
	if err != nil || stillActive.Version != after.Version || !bytes.Equal(stillActive.Key, after.Key) {
		t.Fatalf("failed invalidation changed active key: active=%+v err=%v", stillActive, err)
	}
}

func TestSearchTokenReplacementRekeysTagsBeforeTagAvailability(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	before, err := ring.Ensure(ctx, KeyDomainSearchToken)
	if err != nil {
		t.Fatalf("Ensure search token: %v", err)
	}
	type tagGate struct {
		ID         int `gorm:"primaryKey"`
		KeyVersion int
		Available  bool
	}
	if err := ring.db.AutoMigrate(&tagGate{}); err != nil {
		t.Fatalf("migrate tag gate: %v", err)
	}
	if err := ring.db.Create(&tagGate{ID: 1, KeyVersion: before.Version, Available: true}).Error; err != nil {
		t.Fatalf("seed tag gate: %v", err)
	}
	after, err := ring.ReplaceRebuildable(ctx, KeyDomainSearchToken, func(
		_ context.Context, tx *gorm.DB, transition RebuildableKeyTransition,
	) error {
		return tx.Model(&tagGate{}).Where("id = ? AND key_version = ?", 1, transition.PreviousVersion).
			Updates(map[string]any{"available": false, "key_version": transition.NextVersion}).Error
	})
	if err != nil {
		t.Fatalf("ReplaceRebuildable: %v", err)
	}
	var gate tagGate
	if err := ring.db.First(&gate, 1).Error; err != nil {
		t.Fatalf("load tag gate: %v", err)
	}
	if gate.Available || gate.KeyVersion != after.Version {
		t.Fatalf("tag lookup became available before rekey reconciliation: %+v", gate)
	}
}

func TestStableDomainsAllowRewrapButRejectRotate(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	for _, domain := range []KeyDomain{KeyDomainEntryIdentity, KeyDomainRecoveryCleanupOwnership} {
		before, err := ring.Ensure(ctx, domain)
		if err != nil {
			t.Fatalf("Ensure(%s): %v", domain, err)
		}
		if _, err := ring.Rotate(ctx, domain, time.Hour); !errors.Is(err, ErrKeyRotationProhibited) {
			t.Fatalf("Rotate(%s) got %v, want ErrKeyRotationProhibited", domain, err)
		}
		if _, err := ring.RewrapAll(ctx); err != nil {
			t.Fatalf("RewrapAll(%s): %v", domain, err)
		}
		after, err := ring.Active(ctx, domain)
		if err != nil {
			t.Fatalf("Active(%s): %v", domain, err)
		}
		if before.ID != after.ID || before.Version != after.Version || !bytes.Equal(before.Key, after.Key) {
			t.Fatalf("rewrap changed stable key identity/material: before=%+v after=%+v", before, after)
		}
	}
}

func TestCursorRotationKeepsBoundedVerifyOnlyOverlap(t *testing.T) {
	ring, clock := newKeyringTestHarness(t)
	ctx := context.Background()
	first, err := ring.Ensure(ctx, KeyDomainCursorSigning)
	if err != nil {
		t.Fatalf("Ensure cursor key: %v", err)
	}
	second, err := ring.Rotate(ctx, KeyDomainCursorSigning, 15*time.Minute)
	if err != nil {
		t.Fatalf("Rotate cursor key: %v", err)
	}
	if second.Version != first.Version+1 || bytes.Equal(first.Key, second.Key) {
		t.Fatalf("cursor rotation did not create a new version/material: first=%+v second=%+v", first, second)
	}
	old, err := ring.ByVersion(ctx, KeyDomainCursorSigning, first.Version)
	if err != nil || old.State != DomainKeyVerifyOnly || old.VerifyUntil == nil {
		t.Fatalf("old cursor key lacks verify-only overlap: %+v err=%v", old, err)
	}
	clock.Advance(16 * time.Minute)
	if _, err := ring.ByVersion(ctx, KeyDomainCursorSigning, first.Version); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("expired verify-only cursor key got %v, want ErrKeyUnavailable", err)
	}
}

func TestAuditFingerprintVersionIsExplicit(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	first, err := ring.Ensure(ctx, KeyDomainAuditFingerprint)
	if err != nil {
		t.Fatalf("Ensure audit key: %v", err)
	}
	second, err := ring.Rotate(ctx, KeyDomainAuditFingerprint, 0)
	if err != nil {
		t.Fatalf("Rotate audit key: %v", err)
	}
	if first.Version == second.Version {
		t.Fatal("audit fingerprint rotation reused key version")
	}
	old, err := ring.ByVersion(ctx, KeyDomainAuditFingerprint, first.Version)
	if err != nil {
		t.Fatalf("load old audit fingerprint version: %v", err)
	}
	if old.State != DomainKeyVerifyOnly || old.VerifyUntil != nil {
		t.Fatalf("audit fingerprint history must remain explicitly versioned without cursor TTL: %+v", old)
	}
}

func TestRewrapPreservesDomainVersionAndPlaintext(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	before, err := ring.EnsureRequiredDomains(ctx)
	if err != nil {
		t.Fatalf("EnsureRequiredDomains: %v", err)
	}
	var oldRow model.WrappedDomainKey
	if err := ring.db.Where("domain = ? AND state = ?", KeyDomainEntryIdentity, DomainKeyActive).First(&oldRow).Error; err != nil {
		t.Fatalf("load old wrapped row: %v", err)
	}

	oldKEK := "FAKE_KEYRING_OLD_KEK_FOR_TEST_ONLY"
	// The harness initially used oldKEK; rotate the wrapping KEK while retaining it as legacy.
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_KEYRING_NEW_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", oldKEK)
	secure.ResetForTesting()
	count, err := ring.RewrapAll(ctx)
	if err != nil {
		t.Fatalf("RewrapAll: %v", err)
	}
	if count != int64(len(RequiredKeyDomains)) {
		t.Fatalf("rewrapped %d rows, want %d", count, len(RequiredKeyDomains))
	}

	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	after, err := ring.EnsureRequiredDomains(ctx)
	if err != nil {
		t.Fatalf("read rewrapped domains without legacy KEK: %v", err)
	}
	for domain, prior := range before {
		current := after[domain]
		if prior.ID != current.ID || prior.Version != current.Version || !bytes.Equal(prior.Key, current.Key) {
			t.Fatalf("rewrap changed %s identity/version/plaintext", domain)
		}
	}
	var newRow model.WrappedDomainKey
	if err := ring.db.Where("id = ?", oldRow.ID).First(&newRow).Error; err != nil {
		t.Fatalf("load rewrapped row: %v", err)
	}
	if oldRow.WrappingKeyFingerprint == newRow.WrappingKeyFingerprint || oldRow.WrappedKey == newRow.WrappedKey {
		t.Fatal("rewrap did not replace envelope metadata")
	}
}

func TestRewrapIncludesExpiredVerifyOnlyKeys(t *testing.T) {
	ring, clock := newKeyringTestHarness(t)
	ctx := context.Background()
	oldMaterial, err := ring.Ensure(ctx, KeyDomainCursorSigning)
	if err != nil {
		t.Fatalf("Ensure cursor key: %v", err)
	}
	if _, err := ring.Rotate(ctx, KeyDomainCursorSigning, time.Minute); err != nil {
		t.Fatalf("Rotate cursor key: %v", err)
	}
	clock.Advance(2 * time.Minute)
	if _, err := ring.ByVersion(ctx, KeyDomainCursorSigning, oldMaterial.Version); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("expired verify-only key got %v, want ErrKeyUnavailable", err)
	}

	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_KEYRING_NEW_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "FAKE_KEYRING_OLD_KEK_FOR_TEST_ONLY")
	secure.ResetForTesting()
	count, err := ring.RewrapAll(ctx)
	if err != nil {
		t.Fatalf("RewrapAll expired verify-only key: %v", err)
	}
	if count != 2 {
		t.Fatalf("rewrapped %d cursor rows, want active + expired verify-only", count)
	}

	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	var oldRow model.WrappedDomainKey
	if err := ring.db.Where("domain = ? AND version = ?", KeyDomainCursorSigning, oldMaterial.Version).First(&oldRow).Error; err != nil {
		t.Fatalf("load rewrapped expired cursor row: %v", err)
	}
	plaintext, err := secure.UnwrapDomainKey(oldRow.Domain, oldRow.Version, secure.WrappedDomainKey{
		Envelope:       oldRow.WrappedKey,
		Algorithm:      oldRow.WrapAlgorithm,
		KEKFingerprint: oldRow.WrappingKeyFingerprint,
	})
	if err != nil {
		t.Fatalf("unwrap expired cursor row with only new KEK: %v", err)
	}
	if !bytes.Equal(plaintext, oldMaterial.Key) {
		t.Fatal("rewrap changed expired verify-only plaintext")
	}
}

func TestMissingOrLostKeyFailsClosedWithoutRegeneration(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	if _, err := ring.Active(ctx, KeyDomainEntryIdentity); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("missing active key got %v, want ErrKeyUnavailable", err)
	}
	created, err := ring.Ensure(ctx, KeyDomainEntryIdentity)
	if err != nil {
		t.Fatalf("Ensure entry identity: %v", err)
	}
	if err := ring.MarkLost(ctx, KeyDomainEntryIdentity, created.Version); err != nil {
		t.Fatalf("MarkLost: %v", err)
	}
	if _, err := ring.Active(ctx, KeyDomainEntryIdentity); !errors.Is(err, ErrKeyLost) {
		t.Fatalf("lost active key got %v, want ErrKeyLost", err)
	}
	if _, err := ring.Ensure(ctx, KeyDomainEntryIdentity); !errors.Is(err, ErrKeyLost) {
		t.Fatalf("Ensure silently regenerated a lost stable key: %v", err)
	}
	var count int64
	if err := ring.db.Model(&model.WrappedDomainKey{}).Where("domain = ?", KeyDomainEntryIdentity).Count(&count).Error; err != nil {
		t.Fatalf("count entry identity rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("lost key was silently replaced; row count=%d", count)
	}
}

func TestConcurrentEnsureCreatesOneActiveKeyPerDomain(t *testing.T) {
	ring, _ := newKeyringTestHarness(t)
	ctx := context.Background()
	const goroutines = 12
	results := make(chan DomainKeyMaterial, goroutines)
	errorsCh := make(chan error, goroutines)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			material, err := ring.Ensure(ctx, KeyDomainCursorSigning)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- material
		}()
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent Ensure: %v", err)
		}
	}
	var first *DomainKeyMaterial
	for result := range results {
		result := result
		if first == nil {
			first = &result
			continue
		}
		if result.ID != first.ID || result.Version != first.Version || !bytes.Equal(result.Key, first.Key) {
			t.Fatalf("concurrent Ensure returned different keys: first=%+v result=%+v", *first, result)
		}
	}
	var activeCount int64
	if err := ring.db.Model(&model.WrappedDomainKey{}).
		Where("domain = ? AND state = ?", KeyDomainCursorSigning, DomainKeyActive).
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count active cursor keys: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active cursor key count=%d, want 1", activeCount)
	}
}

type keyringTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *keyringTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *keyringTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func newKeyringTestHarness(t *testing.T) (*Keyring, *keyringTestClock) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_KEYRING_OLD_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{NowFunc: func() time.Time { return time.Now().UTC() }})
	if err != nil {
		t.Fatalf("open keyring database: %v", err)
	}
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatalf("migrate wrapped domain keys: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_wrapped_domain_keys_domain_version ON wrapped_domain_keys(domain, version)`).Error; err != nil {
		t.Fatalf("create domain/version key index: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_wrapped_domain_keys_active ON wrapped_domain_keys(domain) WHERE state = 'active'`).Error; err != nil {
		t.Fatalf("create active key index: %v", err)
	}
	clock := &keyringTestClock{now: time.Date(2026, 7, 13, 4, 5, 6, 0, time.UTC)}
	return NewKeyring(db, clock.Now), clock
}
