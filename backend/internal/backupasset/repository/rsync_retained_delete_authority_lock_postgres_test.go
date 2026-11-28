package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/internal/authoritylock"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/retention"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type rsyncLifecycleAuthorityResolver struct{ service *Service }

func (resolver rsyncLifecycleAuthorityResolver) ResolveDeletePoint(ctx context.Context, tx *gorm.DB, request retention.LifecyclePointRequest, point model.RecoveryPoint, repository model.BackupRepository) (provider.DeletePointRequest, error) {
	return resolver.service.ResolveLifecycleDeletePointTx(ctx, tx, request.AttemptID, point, repository)
}

type rsyncLifecycleAuthorityOwners struct{}

func (rsyncLifecycleAuthorityOwners) RevokeRecoveryPoint(context.Context, retention.LifecyclePointRequest) error {
	return nil
}

func (rsyncLifecycleAuthorityOwners) CleanupRecoveryPoint(context.Context, retention.LifecyclePointRequest) error {
	return nil
}

// The barrier wraps the physical local provider; it pauses only after the real
// delete has returned, before its receipt is handed back to the coordinator.
type rsyncLifecycleAuthorityDeleter struct {
	delegate *provider.RsyncPointDeleter
	calls    atomic.Int32
	finished chan provider.DeletePointResult
	release  chan struct{}
}

func (*rsyncLifecycleAuthorityDeleter) ProviderKind() backupasset.ProviderKind {
	return backupasset.ProviderRsync
}

func (deleter *rsyncLifecycleAuthorityDeleter) DeletePoint(ctx context.Context, request provider.DeletePointRequest) (provider.DeletePointResult, error) {
	deleter.calls.Add(1)
	result, err := deleter.delegate.DeletePoint(ctx, request)
	if err != nil {
		return result, err
	}
	select {
	case deleter.finished <- result:
	case <-ctx.Done():
		return result, ctx.Err()
	}
	select {
	case <-deleter.release:
		return result, nil
	case <-ctx.Done():
		return result, ctx.Err()
	}
}

func TestRsyncLifecycleDeletionAuthorityLockPostgres(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	db := openProducerRetentionIntegrationDB(t, "postgres")
	publication := newRsyncPublicationFixtureWithDB(t, db)
	source := rsyncRestoreResolverFixtureFromPublication(t, publication, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Publish a second physical full copy through the same Task and Repository.
	run := model.TaskRun{TaskID: publication.task.ID, TriggerType: "manual", Status: "running", StartedAt: timePointer(publication.now.Add(-time.Minute)), CreatedAt: publication.now, UpdatedAt: publication.now}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	publication.taskRun = run
	execution, err := publication.service.Prepare(ctx, publication.run())
	if err != nil {
		t.Fatalf("prepare second publication: %v", err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state, ok := execution.(*rsyncPublicationExecution)
	if !ok {
		t.Fatalf("second publication execution type %T", execution)
	}
	input, err := state.RsyncTreePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	input.Source = provider.RsyncTreeCommandSource{LocalPath: source.taskSource}
	strategy, err := provider.NewLocalRsyncTreePublicationStrategy(func() time.Time { return publication.now })
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := strategy.Prepare(ctx, provider.PublicationPrepareRequest{Attempt: provider.NewRsyncTreePublicationAttempt(state.attempt), RsyncTreeInput: &input})
	if err != nil {
		t.Fatal(err)
	}
	copyResult, err := strategy.Execute(ctx, prepared, provider.PublicationProgress{})
	if err != nil {
		t.Fatal(err)
	}
	commitRecord, err := strategy.RecordCommit(ctx, prepared, copyResult)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := commitRecord.RsyncTreeCommit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(ctx, provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	pointID := state.attempt.RecoveryPointID
	if pointID == source.ref.RecoveryPointID {
		t.Fatal("publication points must be distinct")
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).Updates(map[string]any{
		"state": string(backupasset.RecoveryPointCommitted), "committed_at": publication.now,
		"physical_availability": string(backupasset.PhysicalOnline),
	}).Error; err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.RepositoryID != source.ref.RepositoryID || point.ProducingTaskID == nil || *point.ProducingTaskID != publication.task.ID {
		t.Fatal("second point does not share source Task and Repository")
	}

	pinned, err := source.service.ResolveRsyncRestoreSource(ctx, source.ref)
	if err != nil {
		t.Fatalf("resolve committed Recovery source: %v", err)
	}
	defer func() {
		if err := pinned.Close(); err != nil {
			t.Errorf("close pinned Recovery source: %v", err)
		}
	}()
	if err := pinned.Revalidate(ctx); err != nil {
		t.Fatalf("revalidate committed Recovery source: %v", err)
	}
	snapshot, err := source.service.captureRecoveryRsyncAuthoritySnapshot(ctx, source.ref)
	if err != nil {
		t.Fatal(err)
	}
	request := provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: source.ref}
	observation := recoveryRsyncAuthorityObservation(source.ref, snapshot)

	realDeleter, err := provider.NewRsyncPointDeleter(func() time.Time { return publication.now })
	if err != nil {
		t.Fatal(err)
	}
	deleter := &rsyncLifecycleAuthorityDeleter{delegate: realDeleter, finished: make(chan provider.DeletePointResult, 1), release: make(chan struct{})}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{Prober: &scriptedProber{}, PointDeleter: deleter}); err != nil {
		t.Fatal(err)
	}
	adapter, err := retention.NewRegistryPointDeletion(db, registry, rsyncLifecycleAuthorityResolver{service: newLifecycleDeleteService(t, publication.service, publication.now)})
	if err != nil {
		t.Fatal(err)
	}
	holds, err := retention.NewHoldService(retention.HoldServiceDependencies{DB: db, Now: func() time.Time { return publication.now }})
	if err != nil {
		t.Fatal(err)
	}
	owners := rsyncLifecycleAuthorityOwners{}
	coordinator, err := retention.NewCoordinator(retention.CoordinatorDependencies{
		DB: db, Leases: publication.service.lease, Holds: holds, Now: func() time.Time { return publication.now },
		LeaseOwnerID: "rsync-authority-test", Admissions: owners, Cleanup: owners, Deleter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	admin := model.User{Username: "rsync-retention-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create retention admin: %v", err)
	}
	policies, err := retention.NewPolicyService(retention.PolicyServiceDependencies{DB: db, Now: func() time.Time { return publication.now }})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policies.Create(ctx, retention.CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: admin.ID, Username: admin.Username, Role: admin.Role},
		ScopeKind: backupasset.RetentionPolicyScopeRepository,
		ScopeID:   point.RepositoryID,
		Rules:     retention.PolicyRules{Version: retention.PolicyRulesVersion1, Count: &retention.CountRule{KeepLatest: 1}},
	})
	if err != nil {
		t.Fatalf("create retention policy: %v", err)
	}
	selection := retention.Selection{
		PolicyID: policy.ID, PolicyRevision: policy.Revision,
		ScopeKind: policy.ScopeKind, ScopeID: policy.ScopeID,
		RulesJSON: policy.RulesJSON, RuleDigest: policy.RuleDigest, EvaluatedAt: publication.now,
		Points: []retention.SelectedPoint{{RecoveryPointID: point.ID, PointRevision: point.PointRevision, CapabilityRevision: point.CapabilityRevision}},
	}
	attempt, err := coordinator.Claim(ctx, retention.ClaimRequest{RecoveryPointID: point.ID, Operation: backupasset.LifecycleRetentionExpire, PolicySelection: &selection})
	if err != nil {
		t.Fatalf("claim retention: %v", err)
	}
	for _, phase := range []backupasset.LifecyclePhase{backupasset.LifecyclePhaseRevoking, backupasset.LifecyclePhaseDraining, backupasset.LifecyclePhaseCleaning, backupasset.LifecyclePhaseProviderDelete} {
		attempt, err = coordinator.Advance(ctx, attempt.ID)
		if err != nil || attempt.Phase != phase {
			t.Fatalf("advance to %s: phase=%s err=%v", phase, attempt.Phase, err)
		}
	}

	// The holder is a real Recovery transaction: it takes the same repository
	// gate as the caller and revalidates A before B's deletion can take rows.
	newHolder := func() (*gorm.DB, int) {
		t.Helper()
		tx := db.WithContext(ctx).Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		if err := authoritylock.LockRepository(ctx, tx, point.RepositoryID); err != nil {
			_ = tx.Rollback().Error
			t.Fatal(err)
		}
		if err := source.service.RevalidateRecoverySourceAuthorityTx(ctx, tx, request, observation); err != nil {
			_ = tx.Rollback().Error
			t.Fatalf("revalidate Recovery authority in holder: %v", err)
		}
		var pid int
		if err := tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
			_ = tx.Rollback().Error
			t.Fatal(err)
		}
		return tx, pid
	}
	assertRowsAvailable := func(tx *gorm.DB, receipt bool) {
		t.Helper()
		probes := []struct {
			query string
			id    any
		}{
			{"SELECT id FROM backup_repositories WHERE id = ? FOR UPDATE NOWAIT", point.RepositoryID},
			{"SELECT id FROM recovery_points WHERE id = ? FOR UPDATE NOWAIT", source.ref.RecoveryPointID},
			{"SELECT id FROM recovery_points WHERE id = ? FOR UPDATE NOWAIT", point.ID},
			{"SELECT id FROM tasks WHERE id = ? FOR UPDATE NOWAIT", publication.task.ID},
			{"SELECT id FROM nodes WHERE id = ? FOR UPDATE NOWAIT", publication.task.NodeID},
			{"SELECT id FROM recovery_point_lifecycle_attempts WHERE id = ? FOR UPDATE NOWAIT", attempt.ID},
			{"SELECT id FROM recovery_point_leases WHERE id = ? FOR UPDATE NOWAIT", attempt.LeaseID},
		}
		if receipt {
			probes = append(probes, struct {
				query string
				id    any
			}{"SELECT id FROM recovery_point_lifecycle_effect_claims WHERE attempt_id = ? FOR UPDATE NOWAIT", attempt.ID})
		}
		for _, probe := range probes {
			rows, err := tx.Raw(probe.query, probe.id).Rows()
			if err != nil {
				t.Fatalf("shared row was locked before repository gate: %v", err)
			}
			found := rows.Next()
			rowErr := rows.Err()
			_ = rows.Close()
			if !found || rowErr != nil {
				t.Fatalf("shared row probe found=%v err=%v", found, rowErr)
			}
		}
	}
	holder, holderPID := newHolder()
	defer func() { _ = holder.Rollback().Error }()
	type advanceResult struct {
		attempt retention.LifecycleAttempt
		err     error
	}
	results := make(chan advanceResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		advanced, advanceErr := coordinator.Advance(ctx, attempt.ID)
		results <- advanceResult{advanced, advanceErr}
	}()
	defer func() {
		cancel()
		_ = holder.Rollback().Error
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()
	waitRsyncRepositoryAuthorityGate(t, ctx, db, holderPID, point.RepositoryID, done)
	assertRowsAvailable(holder, false)
	if deleter.calls.Load() != 0 {
		t.Fatal("provider called while preparation waits for Recovery authority")
	}
	if err := holder.Commit().Error; err != nil {
		t.Fatal(err)
	}

	var receipt provider.DeletePointResult
	select {
	case receipt = <-deleter.finished:
	case <-done:
		result := <-results
		t.Fatalf("delete returned before provider barrier: phase=%s err=%v", result.attempt.Phase, result.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if receipt.Outcome != provider.DeletePointDeleted || deleter.calls.Load() != 1 {
		t.Fatalf("physical delete outcome=%s calls=%d", receipt.Outcome, deleter.calls.Load())
	}
	holder, holderPID = newHolder()
	close(deleter.release)
	waitRsyncRepositoryAuthorityGate(t, ctx, db, holderPID, point.RepositoryID, done)
	assertRowsAvailable(holder, true)
	if deleter.calls.Load() != 1 {
		t.Fatal("extra provider call while receipt waits for Recovery authority")
	}
	if err := holder.Commit().Error; err != nil {
		t.Fatal(err)
	}
	var advanced advanceResult
	select {
	case advanced = <-results:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if advanced.err != nil || advanced.attempt.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("persist deletion receipt: phase=%s err=%v", advanced.attempt.Phase, advanced.err)
	}
	var tombstone model.RecoveryPointLifecycleTombstone
	if err := db.Where("recovery_point_id = ?", point.ID).First(&tombstone).Error; err != nil {
		t.Fatal(err)
	}
	if tombstone.RepositoryID != point.RepositoryID || tombstone.DeletionReceiptDigest == nil || *tombstone.DeletionReceiptDigest != receipt.ReceiptDigest || tombstone.ResultCode != string(retention.PointDeletionDeleted) {
		t.Fatalf("unexpected deletion tombstone: %+v", tombstone)
	}
	var claim model.RecoveryPointLifecycleEffectClaim
	if err := db.Where("attempt_id = ?", attempt.ID).First(&claim).Error; err != nil || claim.State != "proven" {
		t.Fatalf("effect proof state=%s err=%v", claim.State, err)
	}
	completed, err := coordinator.Advance(ctx, attempt.ID)
	if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete || deleter.calls.Load() != 1 {
		t.Fatalf("complete retention: phase=%s calls=%d err=%v", completed.Phase, deleter.calls.Load(), err)
	}
	if err := db.First(&point, "id = ?", point.ID).Error; err != nil || point.State != string(backupasset.RecoveryPointExpired) || point.EncryptedProviderLocator != "" {
		t.Fatalf("deleted point state=%s err=%v", point.State, err)
	}
	if _, err := os.Stat(filepath.Join(source.root, "points", point.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("physical deleted point remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source.root, "points", source.ref.RecoveryPointID)); err != nil {
		t.Fatalf("Recovery point A changed: %v", err)
	}
	if err := pinned.Revalidate(ctx); err != nil {
		t.Fatalf("Recovery source A after deletion: %v", err)
	}
	if err := source.service.revalidateRecoveryRsyncAuthoritySnapshot(ctx, source.ref, snapshot); err != nil {
		t.Fatalf("Recovery authority A after deletion: %v", err)
	}
}
