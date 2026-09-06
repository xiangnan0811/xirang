package retention

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

func TestRetentionWorkerStartupPassRunsBoundedReconciliation(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 1, eligiblePoints: 3,
	})
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatalf("StartupPass: %v", err)
	}
	claimed := fixture.countAttempts(t)
	if claimed != 1 {
		t.Fatalf("startup claimed=%d, want bounded batch 1", claimed)
	}
	if fixture.importRebuild.imports != 1 || fixture.importRebuild.rebuilds != 1 {
		t.Fatalf("startup import/rebuild calls=%d/%d, want one bounded pass each", fixture.importRebuild.imports, fixture.importRebuild.rebuilds)
	}
	if fixture.importRebuild.lastImportLimit != 1 || fixture.importRebuild.lastRebuildLimit != 1 {
		t.Fatalf("startup import/rebuild limits=%d/%d, want batch 1", fixture.importRebuild.lastImportLimit, fixture.importRebuild.lastRebuildLimit)
	}
	if fixture.audit.calls != 1 || fixture.audit.lastLimit != 1 {
		t.Fatalf("startup audit purge calls=%d limit=%d, want 1/1", fixture.audit.calls, fixture.audit.lastLimit)
	}
}

func TestRetentionWorkerDefersLateHoldSettledAuditRetryBeforeDue(t *testing.T) {
	ctx := context.Background()
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 1, eligiblePoints: 1,
	})
	fixture.deleter.err = nil
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("a", 64),
	}
	holdID := testOpaqueID(fixture.base + 4)
	fixture.deleter.afterEffect = func() {
		if err := fixture.db.Create(&model.RecoveryPointHold{
			ID: holdID, RecoveryPointID: fixture.pointIDs[0],
			HoldType: string(backupasset.RecoveryPointHoldLegal), State: string(backupasset.HoldActive),
			EncryptedReason: "late hold settled-audit retry", CreatedBy: 1,
			CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
		}).Error; err != nil {
			t.Errorf("create late hold: %v", err)
			return
		}
		var point model.RecoveryPoint
		if err := fixture.db.First(&point, "id = ?", fixture.pointIDs[0]).Error; err != nil {
			t.Errorf("load late-hold point: %v", err)
			return
		}
		if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
			Updates(map[string]any{
				"hold_state": backupasset.HoldActive, "point_revision": point.PointRevision + 1,
				"updated_at": fixture.clock,
			}).Error; err != nil {
			t.Errorf("project late hold: %v", err)
		}
	}
	auditErr := errors.New("late-hold settled audit failure")
	sink := &acceptanceAuditSink{failLeft: 1, failErr: auditErr}
	fixture.worker.coordinator.audit = sink
	if err := fixture.worker.StartupPass(ctx); err != nil {
		t.Fatalf("initial worker pass: %v", err)
	}
	var failedAttempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&failedAttempt).Error; err != nil {
		t.Fatalf("load late-hold failed attempt: %v", err)
	}
	if failedAttempt.Phase != string(backupasset.LifecyclePhaseBlocked) ||
		failedAttempt.BlockedReason != string(backupasset.LifecycleBlockedActiveHold) ||
		failedAttempt.RetryAt == nil || !failedAttempt.RetryAt.After(fixture.clock) {
		t.Fatalf("late-hold failed attempt=%+v, want blocked active_hold with future retry", failedAttempt)
	}
	if sink.Writes() != 0 || sink.calls != 0 || fixture.deleter.calls != 1 {
		t.Fatalf("late-hold proof sink_calls=%d sink_writes=%d provider_calls=%d, want 0/0/1",
			sink.calls, sink.Writes(), fixture.deleter.calls)
	}
	fixture.clock = failedAttempt.RetryAt.UTC()
	if err := fixture.worker.settleClaimed(ctx, 1); err == nil || !errors.Is(err, auditErr) {
		t.Fatalf("late-hold terminal audit failure error=%v, want sentinel", err)
	}
	if err := fixture.db.First(&failedAttempt, "id = ?", failedAttempt.ID).Error; err != nil {
		t.Fatalf("reload late-hold failed attempt after audit error: %v", err)
	}
	if failedAttempt.Phase != string(backupasset.LifecyclePhaseBlocked) ||
		failedAttempt.BlockedReason != string(backupasset.LifecycleBlockedActiveHold) ||
		failedAttempt.RetryAt == nil || !failedAttempt.RetryAt.After(fixture.clock) {
		t.Fatalf("late-hold retry attempt=%+v, want blocked active_hold with future retry", failedAttempt)
	}
	if sink.Writes() != 0 || sink.calls != 1 || fixture.deleter.calls != 1 {
		t.Fatalf("late-hold terminal audit failure sink_calls=%d sink_writes=%d provider_calls=%d, want 1/0/1",
			sink.calls, sink.Writes(), fixture.deleter.calls)
	}

	if _, err := fixture.holds.Release(ctx, ReleaseHoldRequest{
		Actor:           backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		RecoveryPointID: fixture.pointIDs[0], HoldID: holdID,
		Reason: "release late hold for settled-audit retry",
	}); err != nil {
		t.Fatalf("release late hold: %v", err)
	}
	loadDurable := func() (
		model.RecoveryPointLifecycleAttempt,
		model.RecoveryPoint,
		model.RecoveryPointLease,
		model.RecoveryPointLifecycleEffectClaim,
		model.RecoveryPointLifecycleTombstone,
		error,
	) {
		var attempt model.RecoveryPointLifecycleAttempt
		if err := fixture.db.First(&attempt, "id = ?", failedAttempt.ID).Error; err != nil {
			return attempt, model.RecoveryPoint{}, model.RecoveryPointLease{},
				model.RecoveryPointLifecycleEffectClaim{}, model.RecoveryPointLifecycleTombstone{}, err
		}
		var point model.RecoveryPoint
		if err := fixture.db.First(&point, "id = ?", fixture.pointIDs[0]).Error; err != nil {
			return attempt, point, model.RecoveryPointLease{},
				model.RecoveryPointLifecycleEffectClaim{}, model.RecoveryPointLifecycleTombstone{}, err
		}
		if attempt.LeaseID == nil {
			return attempt, point, model.RecoveryPointLease{},
				model.RecoveryPointLifecycleEffectClaim{}, model.RecoveryPointLifecycleTombstone{},
				errors.New("late-hold attempt has no lease")
		}
		var lease model.RecoveryPointLease
		if err := fixture.db.First(&lease, "id = ?", *attempt.LeaseID).Error; err != nil {
			return attempt, point, lease,
				model.RecoveryPointLifecycleEffectClaim{}, model.RecoveryPointLifecycleTombstone{}, err
		}
		var claim model.RecoveryPointLifecycleEffectClaim
		if err := fixture.db.Where("attempt_id = ?", attempt.ID).First(&claim).Error; err != nil {
			return attempt, point, lease, claim,
				model.RecoveryPointLifecycleTombstone{}, err
		}
		var tombstone model.RecoveryPointLifecycleTombstone
		if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?",
			point.ID, backupasset.LifecycleRetentionExpire).First(&tombstone).Error; err != nil {
			return attempt, point, lease, claim, tombstone, err
		}
		return attempt, point, lease, claim, tombstone, nil
	}
	beforeAttempt, beforePoint, beforeLease, beforeClaim, beforeTombstone, err := loadDurable()
	if err != nil {
		t.Fatalf("snapshot late-hold durable state: %v", err)
	}
	beforeCalls, beforeWrites, beforeProviderCalls := sink.calls, sink.Writes(), fixture.deleter.calls
	if err := fixture.worker.settleClaimed(ctx, 1); err != nil {
		t.Fatalf("worker pass before settled-audit retry due: %v", err)
	}
	afterAttempt, afterPoint, afterLease, afterClaim, afterTombstone, err := loadDurable()
	if err != nil {
		t.Fatalf("reload late-hold durable state before due: %v", err)
	}
	if !reflect.DeepEqual(beforeAttempt, afterAttempt) ||
		!reflect.DeepEqual(beforePoint, afterPoint) ||
		!reflect.DeepEqual(beforeLease, afterLease) ||
		!reflect.DeepEqual(beforeClaim, afterClaim) ||
		!reflect.DeepEqual(beforeTombstone, afterTombstone) {
		t.Fatal("worker pass before settled-audit retry due mutated durable lifecycle state")
	}
	if sink.calls != beforeCalls || sink.Writes() != beforeWrites || fixture.deleter.calls != beforeProviderCalls {
		t.Fatalf("worker pass before due sink_calls=%d/%d sink_writes=%d/%d provider_calls=%d/%d, want unchanged",
			sink.calls, beforeCalls, sink.Writes(), beforeWrites, fixture.deleter.calls, beforeProviderCalls)
	}

	fixture.clock = beforeAttempt.RetryAt.UTC()
	if err := fixture.worker.settleClaimed(ctx, 1); err != nil {
		t.Fatalf("worker pass at settled-audit retry due: %v", err)
	}
	var completed model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&completed, "id = ?", failedAttempt.ID).Error; err != nil {
		t.Fatalf("load completed late-hold attempt: %v", err)
	}
	var slots int64
	if err := fixture.db.Model(&model.RecoveryPointLifecycleAuditSlot{}).
		Where("attempt_id = ?", failedAttempt.ID).Count(&slots).Error; err != nil {
		t.Fatalf("count completed late-hold audit slots: %v", err)
	}
	if completed.Phase != string(backupasset.LifecyclePhaseComplete) ||
		sink.calls != beforeCalls+1 || sink.Writes() != beforeWrites+1 ||
		slots != 1 || fixture.deleter.calls != beforeProviderCalls {
		t.Fatalf("late-hold due completion phase=%q sink_calls=%d/%d sink_writes=%d/%d slots=%d provider_calls=%d/%d, want complete/one audit slot/no provider replay",
			completed.Phase, sink.calls, beforeCalls, sink.Writes(), beforeWrites, slots, fixture.deleter.calls, beforeProviderCalls)
	}
}

func TestRetentionWorkerPeriodicBatchesHonorDynamicConfig(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 1, eligiblePoints: 4,
	})
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.countAttempts(t) != 1 {
		t.Fatalf("first pass claimed=%d, want 1", fixture.countAttempts(t))
	}
	fixture.settings["backup_assets.retention_batch_size"] = "2"
	fixture.settings["backup_assets.retention_reconcile_interval"] = "45s"
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		fixture.worker.Run(runCtx)
	}()
	fixture.signalAfter(45 * time.Second)
	waitFor(t, func() bool {
		importLimit, rebuildLimit := fixture.importRebuild.limits()
		return fixture.countAttempts(t) == 3 && importLimit == 2 && rebuildLimit == 2
	})
	cancel()
	if err := fixture.worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	<-done
	if fixture.countAttempts(t) != 3 {
		t.Fatalf("after dynamic batch=2 claimed=%d, want 3", fixture.countAttempts(t))
	}
	importLimit, rebuildLimit := fixture.importRebuild.limits()
	if importLimit != 2 || rebuildLimit != 2 {
		t.Fatalf("dynamic import/rebuild limits=%d/%d, want 2", importLimit, rebuildLimit)
	}
}

func TestRetentionWorkerProviderClaimHeartbeatIsObserveOnly(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 10, eligiblePoints: 1,
	})
	fixture.deleter.err = nil
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("a", 64),
	}
	fixture.deleter.entered = make(chan struct{})
	fixture.deleter.release = make(chan struct{})
	passDone := make(chan error, 1)
	go func() {
		passDone <- fixture.worker.StartupPass(context.Background())
	}()
	select {
	case <-fixture.deleter.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter provider execution")
	}
	var attempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&attempt).Error; err != nil {
		t.Fatalf("load in-flight provider attempt: %v", err)
	}
	if attempt.Phase != string(backupasset.LifecyclePhaseProviderDelete) {
		t.Fatalf("in-flight provider attempt phase=%q, want provider_delete", attempt.Phase)
	}
	if attempt.LeaseID == nil {
		t.Fatal("in-flight provider attempt has no lifecycle lease")
	}
	var lease model.RecoveryPointLease
	if err := fixture.db.First(&lease, "id = ?", *attempt.LeaseID).Error; err != nil {
		t.Fatalf("load claimed lease: %v", err)
	}
	if lease.HolderType != string(backupasset.LeaseHolderRetentionWorker) || lease.OwnerID != "retention-worker" ||
		lease.Status != string(backupasset.LeaseActive) {
		t.Fatalf("claimed lease holder=%s owner=%s status=%s, want retention_worker/retention-worker/active",
			lease.HolderType, lease.OwnerID, lease.Status)
	}
	firstHeartbeat := lease.LastHeartbeatAt.UTC()
	if _, err := fixture.worker.coordinator.Heartbeat(context.Background(), attempt.ID); err != nil {
		t.Fatalf("provider claim heartbeat: %v", err)
	}
	if err := fixture.db.First(&lease, "id = ?", lease.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !lease.LastHeartbeatAt.UTC().Equal(firstHeartbeat) {
		t.Fatalf("provider claim heartbeat changed lease from %s to %s, want observe-only",
			firstHeartbeat, lease.LastHeartbeatAt.UTC())
	}
	close(fixture.deleter.release)
	if err := <-passDone; err != nil {
		t.Fatalf("worker startup pass after provider completion: %v", err)
	}
}

func TestRetentionWorkerCancellationAndShutdownJoin(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: time.Hour, batchSize: 1, eligiblePoints: 1,
	})
	started := make(chan struct{})
	fixture.cleanup.waitForCancellation = true
	fixture.cleanup.onStart = func() { close(started) }
	passDone := make(chan error, 1)
	go func() { passDone <- fixture.worker.StartupPass(context.Background()) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter in-flight advance")
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := fixture.worker.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown did not join: %v", err)
	}
	select {
	case err := <-passDone:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancelled StartupPass error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartupPass did not return after Shutdown")
	}
}

func TestRetentionWorkerDisabledStopsNewClaimsAndSettlesClaimedAttempts(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 10, eligiblePoints: 2,
	})
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.countAttempts(t) != 2 {
		t.Fatalf("enabled startup claimed=%d, want 2", fixture.countAttempts(t))
	}
	var first model.RecoveryPointLifecycleAttempt
	if err := fixture.db.Order("id ASC").First(&first).Error; err != nil {
		t.Fatal(err)
	}

	fixture.settings["backup_assets.enabled"] = "false"
	newPoint := newSelectionPoint(testOpaqueID(fixture.base+80), fixture.repositoryID, nil, fixture.clock.Add(-72*time.Hour), 9)
	newPoint.PointRevision = 1
	if err := fixture.db.Create(&newPoint).Error; err != nil {
		t.Fatalf("seed post-disable point: %v", err)
	}
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.countAttempts(t) != 2 {
		t.Fatalf("disabled pass created new claims: attempts=%d", fixture.countAttempts(t))
	}
	if err := fixture.db.First(&first, "id = ?", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if first.Phase == string(backupasset.LifecyclePhaseSelected) {
		t.Fatal("disabled maintenance left the already-claimed attempt unadvanced")
	}
}

func TestRetentionWorkerPolicySelectionDrivesCoordinator(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 10, eligiblePoints: 2, keepLatest: 1,
	})
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.countAttempts(t) != 1 {
		t.Fatalf("policy selection claimed=%d, want the one expired point", fixture.countAttempts(t))
	}
	var attempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.Operation != string(backupasset.LifecycleRetentionExpire) || attempt.PolicyID == nil ||
		*attempt.PolicyID != fixture.policyID {
		t.Fatalf("selection-driven claim=%+v, want retention_expire for policy %s", attempt, fixture.policyID)
	}
	if fixture.metrics.count(MetricSelected) < 1 {
		t.Fatalf("selected metric=%d, want at least 1", fixture.metrics.count(MetricSelected))
	}
}

func TestRetentionWorkerRetriesBlockedAttempts(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 10, eligiblePoints: 1,
		prepareErr: ErrPointDeletionWORM,
	})
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	var attempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.Phase != string(backupasset.LifecyclePhaseBlocked) ||
		attempt.BlockedReason != string(backupasset.LifecycleBlockedProviderWORM) {
		t.Fatalf("first settle attempt=%+v, want blocked/provider_worm", attempt)
	}
	fixture.deleter.prepareErr = nil
	fixture.deleter.err = nil
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionAlreadyAbsent, ReceiptDigest: strings.Repeat("5", 64),
	}
	fixture.clock = attempt.RetryAt.UTC().Add(time.Second)
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&attempt, "id = ?", attempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.Phase != string(backupasset.LifecyclePhaseComplete) {
		t.Fatalf("due blocked retry phase=%s, want complete", attempt.Phase)
	}
	if fixture.metrics.count(MetricRetried) < 1 {
		t.Fatal("blocked retry did not record a retried aggregate metric")
	}
}

func TestRetentionWorkerHonorsUncertainProviderRetryAt(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 1, eligiblePoints: 1,
	})
	fixture.deleter.err = errors.New("provider execution uncertain")
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("d", 64),
	}
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatalf("initial worker pass: %v", err)
	}
	var attempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&attempt).Error; err != nil {
		t.Fatalf("load initial provider attempt: %v", err)
	}
	if attempt.Phase != string(backupasset.LifecyclePhaseProviderDelete) ||
		attempt.RetryAt == nil {
		t.Fatalf("initial worker pass attempt=%+v, want provider_delete with retry", attempt)
	}
	initialRevision := attempt.TransitionRevision
	initialRetryAt := attempt.RetryAt.UTC()
	initialPrepareCalls := fixture.deleter.prepareCalls
	initialProviderCalls := fixture.deleter.calls
	fixture.clock = initialRetryAt.Add(-time.Nanosecond)
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatalf("not-due worker pass: %v", err)
	}
	if err := fixture.db.First(&attempt, "id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load not-due provider attempt: %v", err)
	}
	if attempt.Phase != string(backupasset.LifecyclePhaseProviderDelete) ||
		attempt.TransitionRevision != initialRevision || attempt.RetryAt == nil ||
		!attempt.RetryAt.UTC().Equal(initialRetryAt) ||
		fixture.deleter.prepareCalls != initialPrepareCalls ||
		fixture.deleter.calls != initialProviderCalls {
		t.Fatalf("not-due worker attempt=%+v prepare/calls=%d/%d, want unchanged rev=%d retry=%s prepare/calls=%d/%d",
			attempt, fixture.deleter.prepareCalls, fixture.deleter.calls,
			initialRevision, initialRetryAt, initialPrepareCalls, initialProviderCalls)
	}
	fixture.clock = initialRetryAt
	fixture.deleter.err = nil
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatalf("due worker pass: %v", err)
	}
	if err := fixture.db.First(&attempt, "id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load due provider attempt: %v", err)
	}
	if attempt.Phase != string(backupasset.LifecyclePhaseComplete) ||
		fixture.deleter.calls != initialProviderCalls+1 ||
		fixture.deleter.prepareCalls != initialPrepareCalls+2 {
		t.Fatalf("due worker attempt=%+v prepare/calls=%d/%d, want complete and one retry from %d/%d",
			attempt, fixture.deleter.prepareCalls, fixture.deleter.calls,
			initialPrepareCalls, initialProviderCalls)
	}
}
func TestRetentionWorkerBlockedMetricIsEdgeTriggeredOnNotDueRevisit(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 10, eligiblePoints: 1,
		prepareErr: ErrPointDeletionWORM,
	})
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	var attempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.Phase != string(backupasset.LifecyclePhaseBlocked) {
		t.Fatalf("first settle phase=%s, want blocked", attempt.Phase)
	}
	if blocked := fixture.metrics.count(MetricBlocked); blocked != 1 {
		t.Fatalf("first settle blocked metric=%d, want 1", blocked)
	}
	retryAt := fixture.clock.Add(time.Hour)
	if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", attempt.ID).
		Update("retry_at", retryAt).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if blocked := fixture.metrics.count(MetricBlocked); blocked != 1 {
		t.Fatalf("second settle of the same not-due blocked attempt increment blocked=%d, want 1", blocked)
	}
}

func TestRetentionWorkerExpiresOperationalHoldsNotLegalHolds(t *testing.T) {
	enableHoldEncryption(t)
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 10, eligiblePoints: 2, skipPolicy: true,
	})
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	expiredAt := fixture.clock.Add(-time.Minute)
	operational, err := fixture.holds.Create(context.Background(), CreateHoldRequest{
		Actor: admin, RecoveryPointID: fixture.pointIDs[0], HoldType: backupasset.RecoveryPointHoldOperational,
		Reason: "FAKE_EXPIRED_OPERATIONAL_HOLD_FOR_TEST_ONLY", ExpiresAt: ptrTime(fixture.clock.Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("create operational hold: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPointHold{}).Where("id = ?", operational.ID).
		Update("expires_at", expiredAt).Error; err != nil {
		t.Fatal(err)
	}
	legal, err := fixture.holds.Create(context.Background(), CreateHoldRequest{
		Actor: admin, RecoveryPointID: fixture.pointIDs[1], HoldType: backupasset.RecoveryPointHoldLegal,
		Reason: "FAKE_LEGAL_HOLD_FOR_TEST_ONLY",
	})
	if err != nil {
		t.Fatalf("create legal hold: %v", err)
	}
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	var operationalRow, legalRow model.RecoveryPointHold
	if err := fixture.db.First(&operationalRow, "id = ?", operational.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&legalRow, "id = ?", legal.ID).Error; err != nil {
		t.Fatal(err)
	}
	if operationalRow.State != string(backupasset.HoldReleased) {
		t.Fatalf("operational hold state=%s, want released", operationalRow.State)
	}
	if legalRow.State != string(backupasset.HoldActive) {
		t.Fatalf("legal hold state=%s, want still active", legalRow.State)
	}
}

func TestRetentionWorkerImportRebuildReconciliationIsBounded(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 3, eligiblePoints: 0, skipPolicy: true,
	})
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.importRebuild.imports != 1 || fixture.importRebuild.rebuilds != 1 {
		t.Fatalf("import/rebuild calls=%d/%d, want one bounded pass", fixture.importRebuild.imports, fixture.importRebuild.rebuilds)
	}
	if fixture.importRebuild.lastImportLimit != 3 || fixture.importRebuild.lastRebuildLimit != 3 {
		t.Fatalf("import/rebuild limits=%d/%d, want batch 3", fixture.importRebuild.lastImportLimit, fixture.importRebuild.lastRebuildLimit)
	}
}

type retentionWorkerFixtureOptions struct {
	enabled        bool
	interval       time.Duration
	batchSize      int
	eligiblePoints int
	keepLatest     int
	skipPolicy     bool
	db             *gorm.DB
	prepareErr     error
}

type retentionWorkerFixture struct {
	db            *gorm.DB
	clock         time.Time
	base          uint64
	repositoryID  string
	policyID      string
	pointIDs      []string
	settings      map[string]string
	worker        *Worker
	holds         *HoldService
	cleanup       *retentionWorkerCleanupFake
	deleter       *lifecycleDeletionFake
	importRebuild *retentionImportRebuildFake
	audit         *retentionAuditFake
	metrics       *retentionMetricsFake
	after         chan time.Time
}

func newRetentionWorkerFixture(t *testing.T, options retentionWorkerFixtureOptions) *retentionWorkerFixture {
	t.Helper()
	if options.interval < 30*time.Second {
		options.interval = 30 * time.Second
	}
	if options.batchSize < 1 {
		options.batchSize = 1
	}
	db := options.db
	if db == nil {
		db = newLifecycleCoordinatorTestDB(t)
	}
	fixture := &retentionWorkerFixture{
		db:            db,
		clock:         time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
		base:          9000,
		cleanup:       &retentionWorkerCleanupFake{},
		importRebuild: &retentionImportRebuildFake{},
		audit:         &retentionAuditFake{},
		metrics:       &retentionMetricsFake{},
		after:         make(chan time.Time, 4),
	}
	fixture.repositoryID = testOpaqueID(fixture.base)
	fixture.policyID = testOpaqueID(fixture.base + 1)
	seedRetentionUsersAndRepository(t, fixture.db, fixture.repositoryID)

	for index := 0; index < options.eligiblePoints; index++ {
		pointID := testOpaqueID(fixture.base + 10 + uint64(index))
		point := newSelectionPoint(pointID, fixture.repositoryID, nil, fixture.clock.Add(-time.Duration(48+index*24)*time.Hour), 3)
		point.PointRevision = 4
		if err := fixture.db.Create(&point).Error; err != nil {
			t.Fatalf("seed eligible point: %v", err)
		}
		fixture.pointIDs = append(fixture.pointIDs, pointID)
	}

	now := func() time.Time { return fixture.clock.UTC() }
	if !options.skipPolicy {
		policies, err := NewPolicyService(PolicyServiceDependencies{DB: fixture.db, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		rules := PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 1}}
		if options.keepLatest > 0 {
			rules = PolicyRules{Version: PolicyRulesVersion1, Count: &CountRule{KeepLatest: options.keepLatest}}
		}
		created, err := policies.Create(context.Background(), CreatePolicyRequest{
			Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
			ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: fixture.repositoryID,
			Rules: rules,
		})
		if err != nil {
			t.Fatalf("create worker policy: %v", err)
		}
		fixture.policyID = created.ID
	}

	leases, err := backupasset.NewLeaseService(fixture.db, now, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.holds = mustNewLifecycleHoldService(t, fixture.db, now)
	fixture.deleter = &lifecycleDeletionFake{prepareErr: options.prepareErr, err: ErrPointDeletionWORM}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: fixture.db, Leases: leases, Holds: fixture.holds, Now: now,
		LeaseOwnerID: "retention-worker", Admissions: &lifecycleAdmissionFake{},
		Cleanup: fixture.cleanup, Deleter: fixture.deleter,
	})
	if err != nil {
		t.Fatal(err)
	}
	policies, err := NewPolicyService(PolicyServiceDependencies{DB: fixture.db, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	enabled := "false"
	if options.enabled {
		enabled = "true"
	}
	if err := fixture.db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatalf("migrate worker settings: %v", err)
	}
	settingsService := settings.NewService(fixture.db)
	fixture.settings = map[string]string{
		"backup_assets.enabled":                      enabled,
		"backup_assets.retention_reconcile_interval": options.interval.String(),
		"backup_assets.retention_batch_size":         strconv.Itoa(options.batchSize),
		"backup_assets.retention_drain_timeout":      "30s",
	}
	worker, err := NewWorker(WorkerDependencies{
		Foundation: backupasset.NewFoundationService(retentionSettingsOverlay{
			service: settingsService, overlay: fixture.settings,
		}),
		Coordinator:   coordinator,
		Policies:      policies,
		Holds:         fixture.holds,
		Audit:         fixture.audit,
		ImportRebuild: fixture.importRebuild,
		Metrics:       fixture.metrics,
		Now:           now,
		After:         func(time.Duration) <-chan time.Time { return fixture.after },
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	fixture.worker = worker
	return fixture
}

func (fixture *retentionWorkerFixture) countAttempts(t *testing.T) int {
	t.Helper()
	var count int64
	if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).Count(&count).Error; err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	return int(count)
}

func (fixture *retentionWorkerFixture) signalAfter(d time.Duration) {
	select {
	case fixture.after <- fixture.clock.Add(d):
	default:
		fixture.after <- fixture.clock.Add(d)
	}
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for worker progress")
}

func enableHoldEncryption(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

func ptrTime(value time.Time) *time.Time { return &value }

type retentionSettingsOverlay struct {
	service *settings.Service
	overlay map[string]string
}

func (reader retentionSettingsOverlay) GetEffective(key string) string {
	if value, ok := reader.overlay[key]; ok {
		return value
	}
	if reader.service != nil {
		return reader.service.GetEffective(key)
	}
	return ""
}

type retentionImportRebuildFake struct {
	mu               sync.Mutex
	imports          int
	rebuilds         int
	lastImportLimit  int
	lastRebuildLimit int
}

func (fake *retentionImportRebuildFake) ReconcileImports(_ context.Context, limit int) (int, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.imports++
	fake.lastImportLimit = limit
	return 0, nil
}

func (fake *retentionImportRebuildFake) ReconcileRebuilds(_ context.Context, limit int) (int, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.rebuilds++
	fake.lastRebuildLimit = limit
	return 0, nil
}

func (fake *retentionImportRebuildFake) limits() (importLimit int, rebuildLimit int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.lastImportLimit, fake.lastRebuildLimit
}

type retentionAuditFake struct {
	calls     int
	lastLimit int
}

func (fake *retentionAuditFake) PurgeEligibleDetails(_ context.Context, limit int) (int, error) {
	fake.calls++
	fake.lastLimit = limit
	return 0, nil
}

type retentionMetricsFake struct {
	mu     sync.Mutex
	counts map[MetricOutcome]int
}

func (fake *retentionMetricsFake) Observe(outcome MetricOutcome) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.counts == nil {
		fake.counts = map[MetricOutcome]int{}
	}
	fake.counts[outcome]++
}

func (fake *retentionMetricsFake) count(outcome MetricOutcome) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.counts[outcome]
}

type retentionWorkerCleanupFake struct {
	waitForCancellation bool
	onStart             func()
}

func (fake *retentionWorkerCleanupFake) CleanupRecoveryPoint(ctx context.Context, request LifecyclePointRequest) error {
	if fake.onStart != nil {
		fake.onStart()
	}
	if fake.waitForCancellation {
		<-ctx.Done()
		return ctx.Err()
	}
	if backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil {
		return errors.New("invalid cleanup request")
	}
	return nil
}

var _ ImportRebuildReconciler = (*retentionImportRebuildFake)(nil)

func TestRetentionWorkerRejectsNilDependencies(t *testing.T) {
	if worker, err := NewWorker(WorkerDependencies{}); worker != nil || !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("NewWorker(%v, %v), want nil/ErrInvalidState", worker, err)
	}
}

func TestRetentionWorkerShutdownIdempotent(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: false, interval: 30 * time.Second, batchSize: 1, skipPolicy: true,
	})
	if err := fixture.worker.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

func TestRetentionWorkerDoesNotStartUnjoinedWorkAfterShutdown(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 1, eligiblePoints: 1,
	})
	if err := fixture.worker.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := atomic.Bool{}
	go func() {
		started.Store(true)
		fixture.worker.Run(context.Background())
	}()
	time.Sleep(50 * time.Millisecond)
	if fixture.countAttempts(t) != 0 {
		t.Fatal("shutdown worker started new claims")
	}
	_ = started.Load()
}

func TestPurgePreviewAndPlanDoNotRequireRepositoryPolicy(t *testing.T) {
	fixture := newExplicitPurgeFixture(t)
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	items := []PurgePlanItemInput{{
		RecoveryPointID: fixture.oldPoint.ID, PointRevision: 1, CapabilityRevision: 1,
	}}

	preview, err := fixture.purge.Preview(context.Background(), PreviewPurgeRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, Items: items,
	})
	if err != nil {
		t.Fatalf("Preview without repository policy: %v", err)
	}
	if preview.ImpactRevision < 1 || preview.ItemCount != 1 || preview.HoldCount != 0 {
		t.Fatalf("preview=%+v, want positive purge impact revision without a repository policy", preview)
	}
	if preview.ImpactRevision == 1 {
		t.Fatal("purge impact revision collapsed to first repository policy revision when no such policy is present")
	}

	stale, err := fixture.purge.CreatePlan(context.Background(), CreatePurgePlanRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, ExpectedImpactRevision: preview.ImpactRevision + 1,
		Items: items,
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale purge impact CreatePlan error=%v plan=%+v, want ErrConflict", err, stale)
	}

	plan, err := fixture.purge.CreatePlan(context.Background(), CreatePurgePlanRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, ExpectedImpactRevision: preview.ImpactRevision,
		Items: items,
	})
	if err != nil {
		t.Fatalf("CreatePlan without repository policy: %v", err)
	}
	if plan.ImpactRevision != preview.ImpactRevision {
		t.Fatalf("plan impact=%d, want preview %d", plan.ImpactRevision, preview.ImpactRevision)
	}

	result, err := fixture.purge.Execute(context.Background(), ExecutePurgeRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, PlanID: plan.ID,
		ExpectedRevision: plan.Revision, ExpectedImpactRevision: plan.ImpactRevision,
		Reason: "independent-purge-preview", ProofDigest: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatalf("Execute without repository policy: %v", err)
	}
	if result.Claimed != 1 {
		t.Fatalf("claimed=%d, want 1", result.Claimed)
	}
}

func TestComputePurgeImpactRevisionBindsExactPointIDs(t *testing.T) {
	counts := lifecycleImpactCounts{}
	first := []model.RecoveryPoint{
		{ID: strings.Repeat("a", 32), PointRevision: 2, CapabilityRevision: 3},
		{ID: strings.Repeat("b", 32), PointRevision: 2, CapabilityRevision: 3},
	}
	second := []model.RecoveryPoint{
		{ID: strings.Repeat("c", 32), PointRevision: 2, CapabilityRevision: 3},
		{ID: strings.Repeat("d", 32), PointRevision: 2, CapabilityRevision: 3},
	}
	if computePurgeImpactRevision(0, first, counts) == computePurgeImpactRevision(0, second, counts) {
		t.Fatal("different recovery point ID sets produced the same purge impact revision")
	}
}

func TestPurgeCreatePlanRejectsStaleImpactRevisionAndAdmitsEligiblePoints(t *testing.T) {
	fixture := newExplicitPurgeFixture(t)
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	if _, err := fixture.policies.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: fixture.repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 1}},
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	items := []PurgePlanItemInput{{
		RecoveryPointID: fixture.oldPoint.ID, PointRevision: 1, CapabilityRevision: 1,
	}}
	preview, err := fixture.purge.Preview(context.Background(), PreviewPurgeRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, Items: items,
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	stale, err := fixture.purge.CreatePlan(context.Background(), CreatePurgePlanRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, ExpectedImpactRevision: preview.ImpactRevision + 1,
		Items: items,
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale impact CreatePlan error=%v plan=%+v, want ErrConflict", err, stale)
	}
	retained, err := fixture.purge.CreatePlan(context.Background(), CreatePurgePlanRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, ExpectedImpactRevision: preview.ImpactRevision,
		Items: items,
	})
	if err != nil {
		t.Fatalf("policy-retained CreatePlan error=%v, want success at current purge impact revision", err)
	}
	if retained.ImpactRevision != preview.ImpactRevision || retained.HoldCount != 0 {
		t.Fatalf("policy-retained plan=%+v preview=%+v", retained, preview)
	}

	retired := newSelectionPoint(testOpaqueID(904), fixture.repositoryID, nil, fixture.clock.Add(-72*time.Hour), 1)
	retired.Semantics = string(backupasset.PointMutableHead)
	retired.State = string(backupasset.RecoveryPointRetired)
	retired.ImmutabilityLevel = string(backupasset.ImmutabilityMutable)
	if err := fixture.db.Create(&retired).Error; err != nil {
		t.Fatalf("seed retired mutable: %v", err)
	}
	retiredItems := []PurgePlanItemInput{{
		RecoveryPointID: retired.ID, PointRevision: 1, CapabilityRevision: 1,
	}}
	retiredPreview, err := fixture.purge.Preview(context.Background(), PreviewPurgeRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, Items: retiredItems,
	})
	if err != nil {
		t.Fatalf("retired Preview: %v", err)
	}
	retiredPlan, err := fixture.purge.CreatePlan(context.Background(), CreatePurgePlanRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, ExpectedImpactRevision: retiredPreview.ImpactRevision,
		Items: retiredItems,
	})
	if err != nil {
		t.Fatalf("retired mutable CreatePlan error=%v, want success", err)
	}
	if retiredPlan.ItemCount != 1 {
		t.Fatalf("retired mutable plan item_count=%d", retiredPlan.ItemCount)
	}

	holds := mustNewLifecycleHoldService(t, fixture.db, func() time.Time { return fixture.clock })
	if _, err := holds.Create(context.Background(), CreateHoldRequest{
		Actor: admin, RecoveryPointID: fixture.oldPoint.ID, HoldType: backupasset.RecoveryPointHoldLegal,
		Reason: "FAKE_EXPLICIT_PURGE_HOLD_REASON_FOR_TEST_ONLY",
	}); err != nil {
		t.Fatalf("create hold: %v", err)
	}
	var heldPoint model.RecoveryPoint
	if err := fixture.db.First(&heldPoint, "id = ?", fixture.oldPoint.ID).Error; err != nil {
		t.Fatalf("reload held point: %v", err)
	}
	heldItems := []PurgePlanItemInput{{
		RecoveryPointID: heldPoint.ID, PointRevision: heldPoint.PointRevision, CapabilityRevision: heldPoint.CapabilityRevision,
	}}
	heldPreview, err := fixture.purge.Preview(context.Background(), PreviewPurgeRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, Items: heldItems,
	})
	if err != nil {
		t.Fatalf("held Preview: %v", err)
	}
	heldPlan, err := fixture.purge.CreatePlan(context.Background(), CreatePurgePlanRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, ExpectedImpactRevision: heldPreview.ImpactRevision,
		Items: heldItems,
	})
	if err != nil {
		t.Fatalf("held CreatePlan error=%v, want success", err)
	}
	if heldPlan.HoldCount != 1 {
		t.Fatalf("held plan hold_count=%d, want 1", heldPlan.HoldCount)
	}
}

func TestPurgeExecuteRejectsStalePurgeImpactRevision(t *testing.T) {
	fixture := newExplicitPurgeFixture(t)
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	items := []PurgePlanItemInput{{
		RecoveryPointID: fixture.oldPoint.ID, PointRevision: 1, CapabilityRevision: 1,
	}}
	preview, err := fixture.purge.Preview(context.Background(), PreviewPurgeRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, Items: items,
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	plan, err := fixture.purge.CreatePlan(context.Background(), CreatePurgePlanRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, ExpectedImpactRevision: preview.ImpactRevision,
		Items: items,
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.oldPoint.ID).
		Update("immutability_level", backupasset.ImmutabilityStorageWORM).Error; err != nil {
		t.Fatalf("mark WORM without changing point revision: %v", err)
	}

	result, err := fixture.purge.Execute(context.Background(), ExecutePurgeRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, PlanID: plan.ID,
		ExpectedRevision: plan.Revision, ExpectedImpactRevision: plan.ImpactRevision,
		Reason: "stale-purge-impact-execute", ProofDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("Execute after WORM count change error=%v result=%+v, want ErrConflict", err, result)
	}
	assertNoClaimedAttempts(t, fixture.db)
	assertPlanStatus(t, fixture.db, plan.ID, backupasset.PurgePlanReady)
}

func TestRetentionWorkerSelectAndClaimInspectedBudgetContinuesPastEmptyPrefix(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 1, eligiblePoints: 0, skipPolicy: true,
	})
	workRepositoryID := testOpaqueID(fixture.base + 2)
	if err := fixture.db.Create(&model.BackupRepository{
		ID: workRepositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "later-expire",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed later repository: %v", err)
	}
	workPoint := newSelectionPoint(testOpaqueID(fixture.base+20), workRepositoryID, nil, fixture.clock.Add(-72*time.Hour), 3)
	workPoint.PointRevision = 4
	if err := fixture.db.Create(&workPoint).Error; err != nil {
		t.Fatalf("seed later expire point: %v", err)
	}
	ids := []string{testOpaqueID(1), testOpaqueID(2)}
	next := 0
	policies, err := NewPolicyService(PolicyServiceDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.clock },
		NewID: func() (string, error) {
			id := ids[next]
			next++
			return id, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	if _, err := policies.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: fixture.repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 365}},
	}); err != nil {
		t.Fatalf("create empty prefix policy: %v", err)
	}
	if _, err := policies.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: workRepositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 1}},
	}); err != nil {
		t.Fatalf("create later expire policy: %v", err)
	}
	if err := fixture.worker.selectAndClaim(context.Background(), 1); err != nil {
		t.Fatalf("first selectAndClaim: %v", err)
	}
	if fixture.countAttempts(t) != 0 {
		t.Fatalf("first tick claimed=%d, want 0 after inspecting empty prefix", fixture.countAttempts(t))
	}
	if err := fixture.worker.selectAndClaim(context.Background(), 1); err != nil {
		t.Fatalf("second selectAndClaim: %v", err)
	}
	if fixture.countAttempts(t) != 1 {
		t.Fatalf("second tick claimed=%d, want 1 from later policy", fixture.countAttempts(t))
	}
	var attempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&attempt).Error; err != nil {
		t.Fatalf("load claimed attempt: %v", err)
	}
	if attempt.RecoveryPointID != workPoint.ID {
		t.Fatalf("claimed point=%s, want later point %s", attempt.RecoveryPointID, workPoint.ID)
	}
}

func TestRetentionWorkerSelectAndClaimWalksPastEmptyPrefixPolicy(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 1, eligiblePoints: 0, skipPolicy: true,
	})
	workRepositoryID := testOpaqueID(fixture.base + 2)
	if err := fixture.db.Create(&model.BackupRepository{
		ID: workRepositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "later-expire",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed later repository: %v", err)
	}
	workPoint := newSelectionPoint(testOpaqueID(fixture.base+20), workRepositoryID, nil, fixture.clock.Add(-72*time.Hour), 3)
	workPoint.PointRevision = 4
	if err := fixture.db.Create(&workPoint).Error; err != nil {
		t.Fatalf("seed later expire point: %v", err)
	}
	ids := []string{testOpaqueID(1), testOpaqueID(2)}
	next := 0
	policies, err := NewPolicyService(PolicyServiceDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.clock },
		NewID: func() (string, error) {
			id := ids[next]
			next++
			return id, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	if _, err := policies.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: fixture.repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 365}},
	}); err != nil {
		t.Fatalf("create empty prefix policy: %v", err)
	}
	if _, err := policies.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: workRepositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 1}},
	}); err != nil {
		t.Fatalf("create later expire policy: %v", err)
	}
	if err := fixture.worker.selectAndClaim(context.Background(), 1); err != nil {
		t.Fatalf("first selectAndClaim: %v", err)
	}
	if fixture.countAttempts(t) != 0 {
		t.Fatalf("first tick claimed=%d, want 0 after inspecting empty prefix", fixture.countAttempts(t))
	}
	if err := fixture.worker.selectAndClaim(context.Background(), 1); err != nil {
		t.Fatalf("second selectAndClaim: %v", err)
	}
	if fixture.countAttempts(t) != 1 {
		t.Fatalf("claimed=%d, want 1 from later policy", fixture.countAttempts(t))
	}
	var attempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&attempt).Error; err != nil {
		t.Fatalf("load claimed attempt: %v", err)
	}
	if attempt.RecoveryPointID != workPoint.ID {
		t.Fatalf("claimed point=%s, want later point %s", attempt.RecoveryPointID, workPoint.ID)
	}
}

func TestRetentionWorkerSettleClaimedWalksPastPrefixAttempt(t *testing.T) {
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 2, eligiblePoints: 2,
	})
	if err := fixture.worker.selectAndClaim(context.Background(), 2); err != nil {
		t.Fatalf("selectAndClaim: %v", err)
	}
	var attempts []model.RecoveryPointLifecycleAttempt
	if err := fixture.db.Order("id ASC").Find(&attempts).Error; err != nil || len(attempts) != 2 {
		t.Fatalf("seeded attempts=%d err=%v, want 2", len(attempts), err)
	}
	fixture.worker.attemptAfterID = attempts[0].ID
	if err := fixture.worker.settleClaimed(context.Background(), 1); err != nil {
		t.Fatalf("settleClaimed: %v", err)
	}
	var reloaded []model.RecoveryPointLifecycleAttempt
	if err := fixture.db.Order("id ASC").Find(&reloaded).Error; err != nil {
		t.Fatalf("reload attempts: %v", err)
	}
	if reloaded[0].Phase != attempts[0].Phase {
		t.Fatalf("prefix attempt was re-settled after cursor advanced past it: before=%s after=%s", attempts[0].Phase, reloaded[0].Phase)
	}
	if reloaded[1].Phase == attempts[1].Phase {
		t.Fatalf("later attempt was not settled: before=%+v after=%+v", attempts[1], reloaded[1])
	}
}

func TestPurgeExecuteIsAtomicAcrossClaimFailures(t *testing.T) {
	fixture := newExplicitPurgeFixture(t)
	second := newSelectionPoint(testOpaqueID(903), fixture.repositoryID, nil, fixture.clock.Add(-72*time.Hour), 1)
	if err := fixture.db.Create(&second).Error; err != nil {
		t.Fatalf("seed second purge point: %v", err)
	}
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	items := []PurgePlanItemInput{
		{RecoveryPointID: fixture.oldPoint.ID, PointRevision: 1, CapabilityRevision: 1},
		{RecoveryPointID: second.ID, PointRevision: 1, CapabilityRevision: 1},
	}
	preview, err := fixture.purge.Preview(context.Background(), PreviewPurgeRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, Items: items,
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	plan, err := fixture.purge.CreatePlan(context.Background(), CreatePurgePlanRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, ExpectedImpactRevision: preview.ImpactRevision,
		Items: items,
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", second.ID).Update("point_revision", 2).Error; err != nil {
		t.Fatalf("bump second point revision: %v", err)
	}

	result, err := fixture.purge.Execute(context.Background(), ExecutePurgeRequest{
		Actor: admin, RepositoryID: fixture.repositoryID, PlanID: plan.ID,
		ExpectedRevision: plan.Revision, ExpectedImpactRevision: plan.ImpactRevision,
		Reason: "atomic-claim-execute", ProofDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	if err == nil {
		t.Fatalf("Execute with mid-list claim failure succeeded: %+v", result)
	}
	assertNoClaimedAttempts(t, fixture.db)
	var persisted model.BackupAssetPurgePlan
	if err := fixture.db.First(&persisted, "id = ?", plan.ID).Error; err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	if persisted.Status == string(backupasset.PurgePlanExecuting) {
		t.Fatalf("failed execute left plan executing")
	}
	if persisted.Status != string(backupasset.PurgePlanReady) && persisted.Status != string(backupasset.PurgePlanInvalidated) {
		t.Fatalf("failed execute plan status=%q, want ready or invalidated", persisted.Status)
	}
}

type explicitPurgeFixture struct {
	db           *gorm.DB
	clock        time.Time
	repositoryID string
	oldPoint     model.RecoveryPoint
	policies     *PolicyService
	purge        *PurgeService
}

func newExplicitPurgeFixture(t *testing.T) explicitPurgeFixture {
	t.Helper()
	db := newLifecycleCoordinatorTestDB(t)
	clock := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
	repositoryID := testOpaqueID(900)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	oldPoint := newSelectionPoint(testOpaqueID(901), repositoryID, nil, clock.Add(-48*time.Hour), 1)
	recent := newSelectionPoint(testOpaqueID(902), repositoryID, nil, clock.Add(-time.Hour), 1)
	if err := db.Create(&[]model.RecoveryPoint{oldPoint, recent}).Error; err != nil {
		t.Fatalf("seed purge points: %v", err)
	}
	now := func() time.Time { return clock }
	policies, err := NewPolicyService(PolicyServiceDependencies{DB: db, Now: now})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	leases, err := backupasset.NewLeaseService(db, now, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	holds := mustNewLifecycleHoldService(t, db, now)
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leases, Holds: holds, Now: now, LeaseOwnerID: "retention-worker",
		Admissions: &lifecycleAdmissionFake{}, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	purge, err := NewPurgeService(coordinator)
	if err != nil {
		t.Fatalf("NewPurgeService: %v", err)
	}
	return explicitPurgeFixture{
		db: db, clock: clock, repositoryID: repositoryID, oldPoint: oldPoint, policies: policies, purge: purge,
	}
}

func assertNoClaimedAttempts(t *testing.T, db *gorm.DB) {
	t.Helper()
	var attempts int64
	if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).Count(&attempts).Error; err != nil {
		t.Fatalf("count lifecycle attempts: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("claimed attempts=%d, want 0", attempts)
	}
}

func assertPlanStatus(t *testing.T, db *gorm.DB, planID string, want backupasset.PurgePlanStatus) {
	t.Helper()
	var plan model.BackupAssetPurgePlan
	if err := db.First(&plan, "id = ?", planID).Error; err != nil {
		t.Fatalf("load plan: %v", err)
	}
	if plan.Status != string(want) {
		t.Fatalf("plan status=%q, want %q", plan.Status, want)
	}
}
