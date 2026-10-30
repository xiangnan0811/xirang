package retention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// These scenarios enter through the coordinator and its registered deletion
// adapter. Only the provider and the access resolver stand in for external
// systems; attempts, effect claims, and deletion proof use the real database.
func TestRegistryEntrypointSQLite(t *testing.T) {
	runRegistryEntrypointScenarios(t, newLifecycleCoordinatorTestDB(t))
}

func TestRegistryEntrypointPostgres(t *testing.T) {
	runRegistryEntrypointScenarios(t, newLifecycleAcceptancePostgresDB(t))
}

func runRegistryEntrypointScenarios(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Run("hold_before_effect", func(t *testing.T) {
		fixture, deleter, _, _ := newAcceptanceRegistryFixture(t, db, 40000, registryEntrypointReceipt())
		acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
		acceptanceMakeHold(t, fixture, 40090)

		blocked, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
		if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked || blocked.BlockedReason != backupasset.LifecycleBlockedActiveHold {
			t.Fatalf("held attempt=%+v error=%v, want active-hold block", blocked, err)
		}
		if deleter.Calls() != 0 || acceptanceCountClaims(t, db, fixture.attempt.ID) != 0 || acceptanceCountTombstones(t, db, fixture.pointID) != 0 {
			t.Fatalf("held deletion: calls=%d claims=%d tombstones=%d, want zero", deleter.Calls(),
				acceptanceCountClaims(t, db, fixture.attempt.ID), acceptanceCountTombstones(t, db, fixture.pointID))
		}
		assertLifecyclePointState(t, db, fixture.pointID, backupasset.RecoveryPointPurgeBlocked)
	})
	t.Run("resolver_mutation", func(t *testing.T) {
		runRegistryEntrypointResolverMutation(t, db)
	})
	t.Run("post_effect_drift", func(t *testing.T) {
		runRegistryEntrypointDrift(t, db)
	})
	t.Run("invalid_receipts", func(t *testing.T) {
		runRegistryEntrypointInvalidReceipts(t, db)
	})
	t.Run("provider_errors", func(t *testing.T) {
		runRegistryEntrypointProviderErrors(t, db)
	})
	t.Run("independent_repository_write", func(t *testing.T) {
		runRegistryEntrypointRepositoryWrite(t, db)
	})
	t.Run("cancel_during_resolution", func(t *testing.T) {
		runRegistryEntrypointCancellation(t, db)
	})
	t.Run("cancel_before_registered_provider", func(t *testing.T) {
		fixture, deleter, _, adapter := newAcceptanceRegistryFixture(t, db, 42100, registryEntrypointReceipt())
		acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
		preparation, err := fixture.coordinator.prepareProviderDelete(context.Background(), fixture.attempt.ID)
		if err != nil || !preparation.acquired {
			t.Fatalf("prepare registered deletion acquired=%t error=%v", preparation.acquired, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		execution, err := adapter.Execute(ctx, preparation.prepared)
		if !errors.Is(err, context.Canceled) || execution.ProviderCalled || deleter.Calls() != 0 {
			t.Fatalf("canceled registered deletion called=%t calls=%d error=%v, want no provider effect",
				execution.ProviderCalled, deleter.Calls(), err)
		}
		claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
		if claim.State != "in_flight" || acceptanceCountTombstones(t, db, fixture.pointID) != 0 {
			t.Fatalf("canceled adapter settled proof: claim=%q tombstones=%d", claim.State,
				acceptanceCountTombstones(t, db, fixture.pointID))
		}
		assertLifecyclePointState(t, db, fixture.pointID, backupasset.RecoveryPointExpiring)
	})
}

func registryEntrypointReceipt() provider.DeletePointResult {
	return provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("a", 64)}
}

func runRegistryEntrypointResolverMutation(t *testing.T, db *gorm.DB) {
	t.Helper()
	for index, mutation := range []struct {
		name  string
		point bool
		repo  bool
	}{
		{name: "point", point: true},
		{name: "repository", repo: true},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			fixture, deleter, _, adapter := newAcceptanceRegistryFixture(t, db, 41900+uint64(index)*100, registryEntrypointReceipt())
			acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
			mutationObserved := false
			adapter.resolve = transactionalMutationDeleteResolver{
				mutatePoint: mutation.point, mutateRepository: mutation.repo, mutationObserved: &mutationObserved,
			}

			blocked, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			if !mutationObserved {
				t.Fatal("resolver mutation was not applied inside the transaction")
			}
			if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked || blocked.RetryAt == nil ||
				(blocked.BlockedReason != backupasset.LifecycleBlockedProviderIdentityConflict && blocked.BlockedReason != backupasset.LifecycleBlockedProviderDeleteUnproven) {
				t.Fatalf("resolver mutation attempt=%+v error=%v, want blocked with retry", blocked, err)
			}
			var stored model.RecoveryPointLifecycleAttempt
			if err := db.First(&stored, "id = ?", fixture.attempt.ID).Error; err != nil {
				t.Fatalf("load blocked resolver mutation attempt: %v", err)
			}
			if stored.Phase != string(backupasset.LifecyclePhaseBlocked) || stored.BlockedReason != string(blocked.BlockedReason) || stored.RetryAt == nil {
				t.Fatalf("persisted resolver mutation attempt=%+v, want blocked with retry", stored)
			}
			if deleter.Calls() != 0 || acceptanceCountClaims(t, db, fixture.attempt.ID) != 0 || acceptanceCountTombstones(t, db, fixture.pointID) != 0 {
				t.Fatalf("resolver mutation calls=%d claims=%d tombstones=%d, want zero", deleter.Calls(),
					acceptanceCountClaims(t, db, fixture.attempt.ID), acceptanceCountTombstones(t, db, fixture.pointID))
			}
			var point model.RecoveryPoint
			if err := db.First(&point, "id = ?", fixture.pointID).Error; err != nil {
				t.Fatalf("load resolver mutation point: %v", err)
			}
			var repository model.BackupRepository
			if err := db.First(&repository, "id = ?", point.RepositoryID).Error; err != nil {
				t.Fatalf("load resolver mutation repository: %v", err)
			}
			if point.State != string(backupasset.RecoveryPointPurgeBlocked) || point.EncryptedProviderLocator == "" {
				t.Fatalf("resolver mutation point state=%q locator_present=%t, want blocked with locator", point.State, point.EncryptedProviderLocator != "")
			}
			t.Logf("resolver %s persisted point source=%q repository capability=%d", mutation.name, point.SourceFingerprint, repository.CapabilityRevision)
		})
	}
}

type registryEntrypointAccessResolver struct {
	delegate              *acceptanceRegistryResolver
	drift                 atomic.Bool
	nativeRepositoryDrift atomic.Bool
}

func (resolver *registryEntrypointAccessResolver) ResolveDeletePoint(
	ctx context.Context,
	tx *gorm.DB,
	request LifecyclePointRequest,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) (provider.DeletePointRequest, error) {
	resolved, err := resolver.delegate.ResolveDeletePoint(ctx, tx, request, point, repository)
	if err == nil && resolver.drift.Load() {
		resolved.Snapshot.Access.Secret = []byte("FAKE_POST_EFFECT_ACCESS_FOR_TEST_ONLY")
	}
	if err == nil && resolver.nativeRepositoryDrift.Load() {
		access := resolved.Snapshot.Access.AdapterData.(provider.ResticRuntimeAccess)
		access.NativeRepositoryID = strings.Repeat("1", 64)
		resolved.Snapshot.Access.AdapterData = access
	}
	return resolved, err
}

func runRegistryEntrypointDrift(t *testing.T, db *gorm.DB) {
	t.Helper()
	for index, test := range []struct {
		name string
	}{
		{name: "restic_native_repository"},
		{name: "resolved_access"},
		{name: "native_authority_digest"},
		{name: "point_source"},
		{name: "point_locator"},
		{name: "repository_capability"},
		{name: "attempt_authority"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := 40100 + uint64(index)*100
			var fixture *claimedExpiryFixture
			var deleter *acceptanceRegistryDeleter
			var resolver *acceptanceRegistryResolver
			var adapter *RegistryPointDeletion
			if test.name == "native_authority_digest" {
				fixture = newClaimedExpiryFixtureWithDBAndProvider(t, db, base, backupasset.ProviderRclone)
				deleter = &acceptanceRegistryDeleter{kind: backupasset.ProviderRclone, result: registryEntrypointReceipt()}
				resolver = &acceptanceRegistryResolver{
					kind:           backupasset.ProviderRclone,
					nativeVersions: []provider.RcloneNativeExactVersion{{PhysicalKey: "points/p", VersionID: "v1"}},
					authority:      strings.Repeat("d", 64),
				}
				adapter = acceptanceUseRegistry(t, fixture, backupasset.ProviderRclone, deleter, resolver)
			} else {
				fixture, deleter, resolver, adapter = newAcceptanceRegistryFixture(t, db, base, registryEntrypointReceipt())
			}
			access := &registryEntrypointAccessResolver{delegate: resolver}
			adapter.resolve = access
			before := acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
			var mutationErr error
			deleter.SetAfterEffect(func() {
				switch test.name {
				case "resolved_access":
					access.drift.Store(true)
				case "restic_native_repository":
					access.nativeRepositoryDrift.Store(true)
				case "native_authority_digest":
					resolver.mu.Lock()
					resolver.authority = strings.Repeat("e", 64)
					resolver.mu.Unlock()
				case "point_source":
					mutationErr = registryEntrypointUpdate(db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).
						Updates(map[string]any{"source_fingerprint": strings.Repeat("e", 64), "point_revision": gorm.Expr("point_revision + 1")}))
				case "point_locator":
					mutationErr = registryEntrypointUpdate(db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).
						Updates(map[string]any{"encrypted_provider_locator": `{"snapshot":"FAKE_DRIFTED_LOCATOR_FOR_TEST_ONLY"}`, "point_revision": gorm.Expr("point_revision + 1")}))
				case "repository_capability":
					mutationErr = registryEntrypointUpdate(db.Model(&model.BackupRepository{}).
						Where("id = ?", fixture.claim.PolicySelection.ScopeID).Update("capability_revision", 4))
				case "attempt_authority":
					mutationErr = registryEntrypointUpdate(db.Model(&model.RecoveryPointLifecycleAttempt{}).
						Where("id = ?", fixture.attempt.ID).Update("transition_revision", gorm.Expr("transition_revision + 1")))
				}
			})

			got, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			if mutationErr != nil {
				t.Fatalf("inject post-effect %s drift: %v", test.name, mutationErr)
			}
			if err == nil || got.Phase != backupasset.LifecyclePhaseProviderDelete {
				t.Fatalf("post-effect %s attempt=%+v error=%v, want unproven provider_delete", test.name, got, err)
			}
			wantClaimState := "uncertain"
			if test.name == "attempt_authority" {
				// A changed attempt fence prevents the original execution from
				// updating its claim; the durable in-flight fact must remain.
				wantClaimState = "in_flight"
				if got.TransitionRevision != before.TransitionRevision+1 {
					t.Fatalf("attempt authority revision=%d, want %d", got.TransitionRevision, before.TransitionRevision+1)
				}
			} else if got.RetryAt == nil {
				t.Fatalf("post-effect %s has no retry fact: %+v", test.name, got)
			}
			registryEntrypointAssertUnproven(t, fixture, deleter, wantClaimState)
		})
	}
}

func registryEntrypointUpdate(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("updated %d rows, want one", result.RowsAffected)
	}
	return nil
}

func registryEntrypointAssertUnproven(t *testing.T, fixture *claimedExpiryFixture, deleter *acceptanceRegistryDeleter, wantClaimState string) {
	t.Helper()
	claim := acceptanceLoadClaim(t, fixture.db, fixture.attempt.ID)
	claims := acceptanceCountClaims(t, fixture.db, fixture.attempt.ID)
	tombstones := acceptanceCountTombstones(t, fixture.db, fixture.pointID)
	if deleter.Calls() != 1 || claims != 1 || claim.State != wantClaimState || tombstones != 0 {
		t.Fatalf("unproven deletion: calls=%d claims=%d claim_state=%q tombstones=%d, want one/one/%s/zero",
			deleter.Calls(), claims, claim.State, tombstones, wantClaimState)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", fixture.pointID).Error; err != nil {
		t.Fatalf("load unproven point: %v", err)
	}
	if point.State != string(backupasset.RecoveryPointExpiring) || point.EncryptedProviderLocator == "" {
		t.Fatalf("unproven point state=%q locator_present=%t, want expiring with locator", point.State, point.EncryptedProviderLocator != "")
	}
}

func runRegistryEntrypointInvalidReceipts(t *testing.T, db *gorm.DB) {
	t.Helper()
	for index, test := range []struct {
		name   string
		result provider.DeletePointResult
	}{
		{name: "deleted_missing", result: provider.DeletePointResult{Outcome: provider.DeletePointDeleted}},
		{name: "already_absent_malformed", result: provider.DeletePointResult{Outcome: provider.DeletePointAlreadyAbsent, ReceiptDigest: strings.Repeat("A", 64)}},
		{name: "unknown_outcome", result: provider.DeletePointResult{Outcome: provider.DeletePointOutcome("unknown"), ReceiptDigest: strings.Repeat("a", 64)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, deleter, _, _ := newAcceptanceRegistryFixture(t, db, 41000+uint64(index)*100, test.result)
			acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
			got, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			if !errors.Is(err, backupasset.ErrInvalidState) || got.Phase != backupasset.LifecyclePhaseProviderDelete || got.RetryAt == nil {
				t.Fatalf("invalid receipt attempt=%+v error=%v, want retryable unproven deletion", got, err)
			}
			registryEntrypointAssertUnproven(t, fixture, deleter, "uncertain")
		})
	}
}

func runRegistryEntrypointProviderErrors(t *testing.T, db *gorm.DB) {
	t.Helper()
	unavailable := errors.New("provider temporarily unavailable")
	for index, test := range []struct {
		name string
		err  error
	}{
		{name: "ordinary", err: unavailable},
		{name: "native_version_referenced", err: provider.ErrDeletePointNativeVersionReferenced},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, deleter, _, _ := newAcceptanceRegistryFixture(t, db, 41400+uint64(index)*100, registryEntrypointReceipt())
			deleter.SetError(test.err)
			acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
			got, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			if !errors.Is(err, test.err) || got.Phase != backupasset.LifecyclePhaseProviderDelete || got.RetryAt == nil {
				t.Fatalf("provider failure attempt=%+v error=%v, want matching error and uncertain retry", got, err)
			}
			registryEntrypointAssertUnproven(t, fixture, deleter, "uncertain")
		})
	}
}

func runRegistryEntrypointRepositoryWrite(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture, deleter, _, _ := newAcceptanceRegistryFixture(t, db, 41700, registryEntrypointReceipt())
	acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	deleter.SetBarrier(entered, release)
	released, finished := false, false
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type advanceResult struct {
		attempt LifecycleAttempt
		err     error
	}
	done := make(chan advanceResult, 1)
	defer func() {
		cancel()
		if !released {
			close(release)
		}
		if !finished {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("deletion worker did not stop")
			}
		}
	}()
	go func() {
		attempt, err := fixture.coordinator.Advance(ctx, fixture.attempt.ID)
		done <- advanceResult{attempt: attempt, err: err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("registered provider did not start")
	}

	writerCtx, writerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer writerCancel()
	writerDone := make(chan error, 1)
	writerFinished := false
	defer func() {
		writerCancel()
		if !writerFinished {
			select {
			case <-writerDone:
			case <-time.After(5 * time.Second):
				t.Error("repository writer did not stop")
			}
		}
	}()
	go func() {
		writerDone <- registryEntrypointUpdate(db.WithContext(writerCtx).Model(&model.BackupRepository{}).
			Where("id = ?", fixture.claim.PolicySelection.ScopeID).Update("description", "independent writer completed"))
	}()
	select {
	case err := <-writerDone:
		writerFinished = true
		if err != nil {
			t.Fatalf("independent repository write while provider paused: %v", err)
		}
	case <-writerCtx.Done():
		t.Fatal("independent repository write did not finish while provider was paused")
	}
	close(release)
	released = true
	var result advanceResult
	select {
	case result = <-done:
		finished = true
	case <-ctx.Done():
		t.Fatal("deletion did not finish after provider release")
	}
	if result.err != nil || result.attempt.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("deletion after repository write attempt=%+v error=%v, want tombstoning", result.attempt, result.err)
	}
	var repository model.BackupRepository
	if err := db.First(&repository, "id = ?", fixture.claim.PolicySelection.ScopeID).Error; err != nil {
		t.Fatalf("load independently updated repository: %v", err)
	}
	if repository.Description != "independent writer completed" {
		t.Fatalf("repository description=%q, want independent writer update", repository.Description)
	}
	claim := acceptanceLoadClaim(t, db, fixture.attempt.ID)
	var tombstone model.RecoveryPointLifecycleTombstone
	if err := db.First(&tombstone, "recovery_point_id = ?", fixture.pointID).Error; err != nil {
		t.Fatalf("load deletion tombstone: %v", err)
	}
	if deleter.Calls() != 1 || acceptanceCountClaims(t, db, fixture.attempt.ID) != 1 || claim.State != "proven" ||
		acceptanceCountTombstones(t, db, fixture.pointID) != 1 || tombstone.ResultCode != string(PointDeletionDeleted) ||
		tombstone.DeletionReceiptDigest == nil || *tombstone.DeletionReceiptDigest != strings.Repeat("a", 64) {
		t.Fatalf("deletion after repository write: calls=%d claim_state=%q tombstone=%+v", deleter.Calls(), claim.State, tombstone)
	}
	completed, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete || deleter.Calls() != 1 {
		t.Fatalf("complete deletion after repository write: attempt=%+v calls=%d error=%v", completed, deleter.Calls(), err)
	}
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", fixture.pointID).Error; err != nil {
		t.Fatalf("load completed point: %v", err)
	}
	if point.State != string(backupasset.RecoveryPointExpired) || point.EncryptedProviderLocator != "" {
		t.Fatalf("completed point state=%q locator_present=%t", point.State, point.EncryptedProviderLocator != "")
	}
}

func runRegistryEntrypointCancellation(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture, deleter, resolver, _ := newAcceptanceRegistryFixture(t, db, 41800, registryEntrypointReceipt())
	before := acceptanceAdvanceToProviderDelete(t, fixture.coordinator, fixture.attempt.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	resolver.SetBarrier(&acceptanceResolverBarrier{target: 1, entered: entered, release: release})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := false
	defer func() {
		cancel()
		close(release)
		if !finished {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("canceled preparation worker did not stop")
			}
		}
	}()
	go func() {
		_, err := fixture.coordinator.Advance(ctx, fixture.attempt.ID)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("registered access resolver was not reached")
	}
	cancel()
	select {
	case err := <-done:
		finished = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled preparation error=%v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled deletion did not finish")
	}
	persisted, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
	if err != nil {
		t.Fatalf("reload canceled attempt: %v", err)
	}
	if persisted.Phase != backupasset.LifecyclePhaseProviderDelete || persisted.TransitionRevision != before.TransitionRevision ||
		deleter.Calls() != 0 || acceptanceCountClaims(t, db, fixture.attempt.ID) != 0 || acceptanceCountTombstones(t, db, fixture.pointID) != 0 {
		t.Fatalf("canceled attempt=%+v calls=%d claims=%d tombstones=%d, want unchanged and no effect",
			persisted, deleter.Calls(), acceptanceCountClaims(t, db, fixture.attempt.ID), acceptanceCountTombstones(t, db, fixture.pointID))
	}
	assertLifecyclePointState(t, db, fixture.pointID, backupasset.RecoveryPointExpiring)
}
