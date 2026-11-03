package recovery

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

type cleanupPrecisionExpected struct {
	claim  time.Duration
	revoke time.Duration
	drain  time.Duration
}

type cleanupPrecisionCase struct {
	name     string
	now      time.Duration
	ttl      time.Duration
	sqlite   cleanupPrecisionExpected
	postgres cleanupPrecisionExpected
}

type cleanupPrecisionOps[C any] struct {
	claim     func(context.Context) (C, error)
	revoke    func(context.Context, C) (C, error)
	drain     func(context.Context, C) (C, error)
	validate  func(context.Context, C) (C, error)
	advance   func(context.Context, C) (RecoveryCleanupProgress, error)
	expiry    func(C) time.Time
	phase     func(C) CleanupPhase
	withState func(C, time.Time, CleanupPhase) C
	assert    func(*testing.T, C)
}

func TestRecoveryCleanupLeasePrecisionSQLite(t *testing.T) {
	testRecoveryCleanupLeasePrecision(t, newRecoveryResultLifecycleFixture, false)
}

func TestRecoveryCleanupLeasePrecisionPostgres(t *testing.T) {
	testRecoveryCleanupLeasePrecision(t, newRecoveryResultLifecyclePostgresFixture, true)
}

func testRecoveryCleanupLeasePrecision(
	t *testing.T,
	newFixture func(*testing.T) *recoveryResultLifecycleFixture,
	postgres bool,
) {
	t.Helper()
	cases := []cleanupPrecisionCase{
		{
			name: "clock_nanos", now: 123 * time.Nanosecond, ttl: 5 * time.Minute,
			sqlite:   cleanupPrecisionExpected{5*time.Minute + 123*time.Nanosecond, 5*time.Minute + 124*time.Nanosecond, 5*time.Minute + 4*time.Second + 123*time.Nanosecond},
			postgres: cleanupPrecisionExpected{5 * time.Minute, 5*time.Minute + time.Second, 5*time.Minute + 4*time.Second},
		},
		{
			name: "ttl_nanos", now: 0, ttl: 5*time.Minute + 321*time.Nanosecond,
			sqlite:   cleanupPrecisionExpected{5*time.Minute + 321*time.Nanosecond, 5*time.Minute + 322*time.Nanosecond, 5*time.Minute + 4*time.Second + 321*time.Nanosecond},
			postgres: cleanupPrecisionExpected{5 * time.Minute, 5*time.Minute + time.Second, 5*time.Minute + 4*time.Second},
		},
		{
			name: "both_micro_carry", now: 800 * time.Nanosecond, ttl: 5*time.Minute + 700*time.Nanosecond,
			sqlite:   cleanupPrecisionExpected{5*time.Minute + 1500*time.Nanosecond, 5*time.Minute + 1501*time.Nanosecond, 5*time.Minute + 4*time.Second + 1500*time.Nanosecond},
			postgres: cleanupPrecisionExpected{5*time.Minute + time.Microsecond, 5*time.Minute + time.Second + time.Microsecond, 5*time.Minute + 4*time.Second + time.Microsecond},
		},
		{
			name: "both_second_carry", now: 999999800 * time.Nanosecond, ttl: 5*time.Minute + 700*time.Nanosecond,
			sqlite:   cleanupPrecisionExpected{5*time.Minute + time.Second + 500*time.Nanosecond, 5*time.Minute + time.Second + 501*time.Nanosecond, 5*time.Minute + 5*time.Second + 500*time.Nanosecond},
			postgres: cleanupPrecisionExpected{5*time.Minute + time.Second, 5*time.Minute + 2*time.Second, 5*time.Minute + 5*time.Second},
		},
	}
	shortCases := []struct {
		name       string
		now        time.Duration
		ttl        time.Duration
		sqlite     time.Duration
		postgres   time.Duration
		pgConflict bool
	}{
		{"rounded_before_now", 900 * time.Nanosecond, 50 * time.Nanosecond, 950 * time.Nanosecond, 0, true},
		{"rounded_equal_now", 0, 500 * time.Nanosecond, 500 * time.Nanosecond, 0, true},
		{"rounded_after_now", 900 * time.Nanosecond, 200 * time.Nanosecond, 1100 * time.Nanosecond, time.Microsecond, false},
	}

	for _, resource := range []string{"workspace", "published"} {
		t.Run(resource, func(t *testing.T) {
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					fixture := newFixture(t)
					base := fixture.now.Truncate(time.Second)
					fixture.now = base.Add(test.now)
					configureCleanupPrecisionTTL(t, fixture, test.ttl)
					want := test.sqlite
					if postgres {
						want = test.postgres
					}
					if resource == "workspace" {
						fixture.prepareWorkspaceCleanupDue(t)
						exerciseCleanupPrecision(t, fixture, base, want, false, workspaceCleanupPrecisionOps(fixture))
					} else {
						published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
						if err != nil {
							t.Fatalf("publish cleanup fixture: %v", err)
						}
						exerciseCleanupPrecision(t, fixture, base, want, true, resultCleanupPrecisionOps(fixture, published.ResultSetID))
					}
				})
			}
			for _, test := range shortCases {
				t.Run(test.name, func(t *testing.T) {
					fixture := newFixture(t)
					base := fixture.now.Truncate(time.Second)
					fixture.now = base.Add(test.now)
					configureCleanupPrecisionTTL(t, fixture, test.ttl)
					want, conflict := test.sqlite, false
					if postgres {
						want, conflict = test.postgres, test.pgConflict
					}
					if resource == "workspace" {
						fixture.prepareWorkspaceCleanupDue(t)
						exerciseShortCleanupPrecision(t, fixture, base, want, conflict, workspaceCleanupPrecisionOps(fixture))
					} else {
						published, err := fixture.service.Publish(context.Background(), fixture.publishRequest())
						if err != nil {
							t.Fatalf("publish cleanup fixture: %v", err)
						}
						exerciseShortCleanupPrecision(t, fixture, base, want, conflict, resultCleanupPrecisionOps(fixture, published.ResultSetID))
					}
				})
			}
		})
	}
}

func configureCleanupPrecisionTTL(t *testing.T, fixture *recoveryResultLifecycleFixture, ttl time.Duration) {
	t.Helper()
	previous := fixture.service
	service, err := NewResultLifecycleService(ResultLifecycleDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now }, Audit: previous.audit,
		WorkspaceKeys: previous.workspaceKeys, DefaultPlaintextTTL: previous.defaultPlaintextTTL,
		RetainHardCap: previous.retainHardCap, NodeAdmission: fixture.nodeAdmission,
		ContentLifecycle: fixture.contentLifecycle, Target: fixture.target,
		CleanupLeaseTTL: ttl, NewID: previous.newID,
	})
	if err != nil {
		t.Fatalf("configure cleanup precision TTL: %v", err)
	}
	fixture.service = service
}

func workspaceCleanupPrecisionOps(fixture *recoveryResultLifecycleFixture) cleanupPrecisionOps[RecoveryWorkspaceCleanupClaim] {
	return cleanupPrecisionOps[RecoveryWorkspaceCleanupClaim]{
		claim: func(ctx context.Context) (RecoveryWorkspaceCleanupClaim, error) {
			return fixture.service.ClaimWorkspaceCleanup(ctx, ClaimRecoveryWorkspaceCleanupRequest{JobID: fixture.job.ID, WorkerID: "cleanup-precision-worker"})
		},
		revoke:   fixture.service.RevokeRecoveryWorkspaceCleanup,
		drain:    fixture.service.DrainRecoveryWorkspaceCleanup,
		validate: fixture.service.ValidateRecoveryWorkspaceCleanup,
		advance:  fixture.service.AdvanceRecoveryWorkspaceCleanup,
		expiry:   func(claim RecoveryWorkspaceCleanupClaim) time.Time { return claim.LeaseExpiresAt },
		phase:    func(claim RecoveryWorkspaceCleanupClaim) CleanupPhase { return claim.Phase },
		withState: func(claim RecoveryWorkspaceCleanupClaim, expiry time.Time, phase CleanupPhase) RecoveryWorkspaceCleanupClaim {
			claim.LeaseExpiresAt, claim.Phase = expiry, phase
			return claim
		},
		assert: func(t *testing.T, claim RecoveryWorkspaceCleanupClaim) {
			t.Helper()
			fixture.assertWorkspaceCleanupClaim(t, claim)
			fixture.assertCleanupNodeLease(t, claim.NodeLeaseID, claim.JobID, claim.WorkerID, claim.NodeFence, claim.LeaseExpiresAt)
		},
	}
}

func resultCleanupPrecisionOps(fixture *recoveryResultLifecycleFixture, resultSetID string) cleanupPrecisionOps[RecoveryResultCleanupClaim] {
	return cleanupPrecisionOps[RecoveryResultCleanupClaim]{
		claim: func(ctx context.Context) (RecoveryResultCleanupClaim, error) {
			return fixture.service.ClaimCleanup(ctx, ClaimRecoveryResultCleanupRequest{ResultSetID: resultSetID, WorkerID: "cleanup-precision-worker"})
		},
		revoke:   fixture.service.RevokeRecoveryResultCleanup,
		drain:    fixture.service.DrainRecoveryResultCleanup,
		validate: fixture.service.ValidateRecoveryResultCleanup,
		advance:  fixture.service.AdvanceRecoveryResultCleanup,
		expiry:   func(claim RecoveryResultCleanupClaim) time.Time { return claim.LeaseExpiresAt },
		phase:    func(claim RecoveryResultCleanupClaim) CleanupPhase { return claim.Phase },
		withState: func(claim RecoveryResultCleanupClaim, expiry time.Time, phase CleanupPhase) RecoveryResultCleanupClaim {
			claim.LeaseExpiresAt, claim.Phase = expiry, phase
			return claim
		},
		assert: func(t *testing.T, claim RecoveryResultCleanupClaim) {
			t.Helper()
			fixture.assertCleanupClaimRow(t, claim)
			fixture.assertCleanupNodeLease(t, claim.NodeLeaseID, claim.JobID, claim.WorkerID, claim.NodeFence, claim.LeaseExpiresAt)
		},
	}
}

func exerciseCleanupPrecision[C any](
	t *testing.T,
	fixture *recoveryResultLifecycleFixture,
	base time.Time,
	want cleanupPrecisionExpected,
	published bool,
	ops cleanupPrecisionOps[C],
) {
	t.Helper()
	ctx := context.Background()
	claim, err := ops.claim(ctx)
	if err != nil {
		t.Fatalf("claim cleanup: %v", err)
	}
	assertCleanupPrecisionClaim(t, ops, claim, CleanupPhaseClaimed, base.Add(want.claim))

	before := snapshotCleanupPrecision(t, fixture)
	tampered := ops.withState(claim, ops.expiry(claim).Add(time.Nanosecond), CleanupPhaseClaimed)
	if _, err := ops.revoke(ctx, tampered); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
		t.Fatalf("same-microsecond tampered claim error=%v, want cleanup conflict", err)
	}
	assertCleanupPrecisionUnchanged(t, fixture, before)

	originalNow := fixture.now
	fixture.now = ops.expiry(claim)
	if _, err := ops.revoke(ctx, claim); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
		t.Fatalf("claim at exact expiry error=%v, want cleanup conflict", err)
	}
	assertCleanupPrecisionUnchanged(t, fixture, before)
	fixture.now = originalNow.Add(time.Nanosecond)

	revoked, err := ops.revoke(ctx, claim)
	if err != nil {
		t.Fatalf("revoke cleanup: %v", err)
	}
	assertCleanupPrecisionClaim(t, ops, revoked, CleanupPhaseRevoked, base.Add(want.revoke))

	// Keep the durable phase current so the old expiry is the only stale field.
	before = snapshotCleanupPrecision(t, fixture)
	stale := ops.withState(claim, ops.expiry(claim), CleanupPhaseRevoked)
	if _, err := ops.drain(ctx, stale); !errors.Is(err, ErrRecoveryResultCleanupConflict) {
		t.Fatalf("pre-renewal claim error=%v, want cleanup conflict", err)
	}
	assertCleanupPrecisionUnchanged(t, fixture, before)

	fixture.now = originalNow.Add(4 * time.Second)
	drained, err := ops.drain(ctx, revoked)
	if err != nil {
		t.Fatalf("drain cleanup: %v", err)
	}
	drainExpiry := base.Add(want.drain)
	if published {
		// Published cleanup renews once before and once after draining readers.
		if !fixture.contentLifecycle.drainObservedLease.Equal(drainExpiry) {
			t.Fatalf("reader drain observed expiry=%s, want=%s", fixture.contentLifecycle.drainObservedLease, drainExpiry)
		}
		drainExpiry = drainExpiry.Add(time.Second)
	}
	assertCleanupPrecisionClaim(t, ops, drained, CleanupPhaseDrained, drainExpiry)

	validated, err := ops.validate(ctx, drained)
	if err != nil {
		t.Fatalf("validate cleanup: %v", err)
	}
	assertCleanupPrecisionClaim(t, ops, validated, CleanupPhaseValidated, drainExpiry.Add(2*time.Second))
	if fixture.target.calls != 1 || !fixture.target.observedLeaseExpiry.Equal(drainExpiry.Add(time.Second)) {
		t.Fatalf("target observed calls=%d expiry=%s, want one call with expiry=%s",
			fixture.target.calls, fixture.target.observedLeaseExpiry, drainExpiry.Add(time.Second))
	}

	fixture.target.plannedMutations = 1
	fixture.target.removal = OwnedJobDirRemoval{
		Complete: false, RemovedEntries: 1,
		ProgressDigest: framedDigest("xirang/recovery/cleanup-precision/v1", fixture.job.ID),
	}
	progress, err := ops.advance(ctx, validated)
	if err != nil {
		t.Fatalf("advance cleanup: %v", err)
	}
	if progress.Phase != CleanupPhaseDeleteStarted || progress.Complete || progress.RemovedEntries != 1 ||
		progress.ProgressDigest != fixture.target.removal.ProgressDigest || fixture.target.removeMutations != 1 {
		t.Fatalf("unexpected cleanup progress=%+v mutations=%d", progress, fixture.target.removeMutations)
	}
	if len(fixture.target.removePermits) != 1 || !fixture.target.removePermits[0].ExpiresAt.Equal(drainExpiry.Add(3*time.Second)) {
		t.Fatalf("unexpected cleanup removal permits=%+v", fixture.target.removePermits)
	}
	advanced := ops.withState(validated, drainExpiry.Add(4*time.Second), CleanupPhaseDeleteStarted)
	assertCleanupPrecisionClaim(t, ops, advanced, CleanupPhaseDeleteStarted, drainExpiry.Add(4*time.Second))
}

func exerciseShortCleanupPrecision[C any](
	t *testing.T,
	fixture *recoveryResultLifecycleFixture,
	base time.Time,
	want time.Duration,
	conflict bool,
	ops cleanupPrecisionOps[C],
) {
	t.Helper()
	ctx := context.Background()
	before := snapshotCleanupPrecision(t, fixture)
	claim, err := ops.claim(ctx)
	if conflict {
		if !errors.Is(err, ErrRecoveryResultCleanupConflict) {
			t.Fatalf("non-future canonical expiry error=%v, want cleanup conflict", err)
		}
		assertCleanupPrecisionUnchanged(t, fixture, before)
		return
	}
	if err != nil {
		t.Fatalf("claim future submicrosecond lease: %v", err)
	}
	assertCleanupPrecisionClaim(t, ops, claim, CleanupPhaseClaimed, base.Add(want))
	revoked, err := ops.revoke(ctx, claim)
	if err != nil {
		t.Fatalf("revoke future submicrosecond lease: %v", err)
	}
	assertCleanupPrecisionClaim(t, ops, revoked, CleanupPhaseRevoked, base.Add(want+time.Second))
}

func assertCleanupPrecisionClaim[C any](t *testing.T, ops cleanupPrecisionOps[C], claim C, phase CleanupPhase, expiry time.Time) {
	t.Helper()
	if ops.phase(claim) != phase || !ops.expiry(claim).Equal(expiry) {
		t.Fatalf("cleanup claim phase=%s expiry=%s, want phase=%s expiry=%s", ops.phase(claim), ops.expiry(claim), phase, expiry)
	}
	ops.assert(t, claim)
}

type cleanupPrecisionSnapshot struct {
	job        model.BackupAssetRecoveryJob
	resultSets []model.BackupAssetRecoveryResultSet
	results    []model.BackupAssetRecoveryResult
	leases     []model.BackupAssetRecoveryNodeLease
}

func snapshotCleanupPrecision(t *testing.T, fixture *recoveryResultLifecycleFixture) cleanupPrecisionSnapshot {
	t.Helper()
	var snapshot cleanupPrecisionSnapshot
	if err := fixture.db.Where("id = ?", fixture.job.ID).Take(&snapshot.job).Error; err != nil {
		t.Fatalf("read cleanup job: %v", err)
	}
	if err := fixture.db.Where("job_id = ?", fixture.job.ID).Order("id").Find(&snapshot.resultSets).Error; err != nil {
		t.Fatalf("read cleanup result sets: %v", err)
	}
	if err := fixture.db.Where("job_id = ?", fixture.job.ID).Order("id").Find(&snapshot.results).Error; err != nil {
		t.Fatalf("read cleanup results: %v", err)
	}
	if err := fixture.db.Where("job_id = ?", fixture.job.ID).Order("id").Find(&snapshot.leases).Error; err != nil {
		t.Fatalf("read cleanup node leases: %v", err)
	}
	return snapshot
}

func assertCleanupPrecisionUnchanged(t *testing.T, fixture *recoveryResultLifecycleFixture, before cleanupPrecisionSnapshot) {
	t.Helper()
	if after := snapshotCleanupPrecision(t, fixture); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected cleanup operation changed durable job, result, or node lease state")
	}
}
