package retention

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func newPostgresPurgePreviewService(t *testing.T, db *gorm.DB, now time.Time) *PurgeService {
	t.Helper()
	clock := func() time.Time { return now }
	leases, err := backupasset.NewLeaseService(db, clock, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leases, Holds: mustNewLifecycleHoldService(t, db, clock), Now: clock,
		LeaseOwnerID: "purge-preview-postgres", Admissions: &lifecycleAdmissionFake{}, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	service, err := NewPurgeService(coordinator)
	if err != nil {
		t.Fatalf("NewPurgeService: %v", err)
	}
	return service
}

func TestPurgePreviewIDsPostgresLocksRepositoryBeforePoints(t *testing.T) {
	db := newLifecycleCoordinatorPostgresTestDB(t)
	now := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
	repositoryID := testOpaqueID(2100)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	first := newSelectionPoint(testOpaqueID(2101), repositoryID, nil, now.Add(-time.Hour), 2)
	second := newSelectionPoint(testOpaqueID(2102), repositoryID, nil, now.Add(-2*time.Hour), 3)
	if err := db.Create(&[]model.RecoveryPoint{first, second}).Error; err != nil {
		t.Fatalf("seed recovery points: %v", err)
	}
	service := newPostgresPurgePreviewService(t, db, now)
	actor := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	holder := db.WithContext(ctx).Begin()
	if holder.Error != nil {
		cancel()
		t.Fatalf("begin repository holder: %v", holder.Error)
	}
	var holderPID int
	if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
		_ = holder.Rollback().Error
		cancel()
		t.Fatalf("read holder PID: %v", err)
	}
	var repository model.BackupRepository
	if err := holder.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", repositoryID).Error; err != nil {
		_ = holder.Rollback().Error
		cancel()
		t.Fatalf("lock repository: %v", err)
	}

	type previewResult struct {
		view PurgePlanView
		err  error
	}
	resultCh := make(chan previewResult, 1)
	go func() {
		view, err := service.Preview(ctx, PreviewPurgeRequest{
			Actor: actor, RepositoryID: repositoryID, RecoveryPointIDs: []string{second.ID, first.ID},
		})
		resultCh <- previewResult{view: view, err: err}
	}()
	released, joined := false, false
	defer func() {
		cancel()
		if !released {
			_ = holder.Rollback().Error
		}
		if !joined {
			<-resultCh
		}
	}()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := db.WithContext(ctx).Raw(`SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND ? = ANY(pg_blocking_pids(pid))
			  AND query ILIKE '%backup_repositories%' AND query ILIKE '%for update%'
		)`, holderPID).Scan(&blocked).Error; err != nil {
			t.Fatalf("inspect preview repository wait: %v", err)
		}
		if blocked {
			break
		}
		select {
		case result := <-resultCh:
			joined = true
			t.Fatalf("preview finished before repository lock was released: %+v, %v", result.view, result.err)
		case <-ctx.Done():
			t.Fatalf("preview did not wait on repository holder: %v", ctx.Err())
		case <-ticker.C:
		}
	}

	probe := db.WithContext(ctx).Begin()
	if probe.Error != nil {
		t.Fatalf("begin point lock probe: %v", probe.Error)
	}
	var point model.RecoveryPoint
	probeErr := probe.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&point, "id = ?", second.ID).Error
	_ = probe.Rollback().Error
	if probeErr != nil {
		t.Fatalf("requested point must remain lockable while preview waits for repository: %v", probeErr)
	}
	if err := holder.Model(&model.RecoveryPoint{}).Where("id = ?", second.ID).Updates(map[string]any{
		"point_revision": 7, "capability_revision": 5,
	}).Error; err != nil {
		t.Fatalf("update point under repository lock: %v", err)
	}
	if err := holder.Commit().Error; err != nil {
		t.Fatalf("commit repository holder: %v", err)
	}
	released = true
	var result previewResult
	select {
	case result = <-resultCh:
		joined = true
	case <-ctx.Done():
		t.Fatalf("preview did not finish after holder committed: %v", ctx.Err())
	}
	if result.err != nil {
		t.Fatalf("Preview: %v", result.err)
	}
	want := []PurgePlanItemView{
		{RecoveryPointID: second.ID, PointRevision: 7, CapabilityRevision: 5},
		{RecoveryPointID: first.ID, PointRevision: first.PointRevision, CapabilityRevision: first.CapabilityRevision},
	}
	if result.view.ItemCount != len(want) || !reflect.DeepEqual(result.view.Items, want) || result.view.ID != "" || result.view.Revision != 0 || result.view.ImpactRevision < 1 {
		t.Fatalf("preview=%+v, want ordered current items %+v and no persisted plan", result.view, want)
	}
	for _, table := range []any{&model.BackupAssetPurgePlan{}, &model.BackupAssetPurgePlanItem{}, &model.RecoveryPointLifecycleEffectClaim{}} {
		var count int64
		if err := db.Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("preview persisted %T rows: count=%d error=%v", table, count, err)
		}
	}
	assertNoClaimedAttempts(t, db)
}

func TestPurgePreviewIDsPostgresDoesNotLockForeignPoint(t *testing.T) {
	db := newLifecycleCoordinatorPostgresTestDB(t)
	now := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
	repositoryID, foreignRepositoryID := testOpaqueID(2110), testOpaqueID(2111)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	seedRetentionUsersAndRepository(t, db, foreignRepositoryID)
	point := newSelectionPoint(testOpaqueID(2112), foreignRepositoryID, nil, now.Add(-time.Hour), 1)
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed foreign point: %v", err)
	}
	service := newPostgresPurgePreviewService(t, db, now)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	holder := db.WithContext(ctx).Begin()
	if holder.Error != nil {
		t.Fatalf("begin foreign point holder: %v", holder.Error)
	}
	defer holder.Rollback()
	var locked model.RecoveryPoint
	if err := holder.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", point.ID).Error; err != nil {
		t.Fatalf("lock foreign point: %v", err)
	}
	_, err := service.Preview(ctx, PreviewPurgeRequest{
		Actor:        backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		RepositoryID: repositoryID, RecoveryPointIDs: []string{point.ID},
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("Preview with locked foreign point: %v, want ErrConflict without waiting", err)
	}
	assertNoClaimedAttempts(t, db)
}

func TestPurgeFrozenRevisionsPostgresRejectStalePoint(t *testing.T) {
	db := newLifecycleCoordinatorPostgresTestDB(t)
	now := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
	repositoryID := testOpaqueID(2120)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(testOpaqueID(2121), repositoryID, nil, now.Add(-time.Hour), 3)
	point.PointRevision = 2
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed point: %v", err)
	}
	service := newPostgresPurgePreviewService(t, db, now)
	actor := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	current := PurgePlanItemInput{RecoveryPointID: point.ID, PointRevision: 2, CapabilityRevision: 3}
	preview, err := service.Preview(context.Background(), PreviewPurgeRequest{
		Actor: actor, RepositoryID: repositoryID, Items: []PurgePlanItemInput{current},
	})
	if err != nil {
		t.Fatalf("Preview current revisions: %v", err)
	}
	for _, stale := range []PurgePlanItemInput{
		{RecoveryPointID: point.ID, PointRevision: 1, CapabilityRevision: 3},
		{RecoveryPointID: point.ID, PointRevision: 2, CapabilityRevision: 2},
	} {
		_, err := service.Preview(context.Background(), PreviewPurgeRequest{
			Actor: actor, RepositoryID: repositoryID, Items: []PurgePlanItemInput{stale},
		})
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("Preview stale revisions %+v: %v, want ErrConflict", stale, err)
		}
		_, err = service.CreatePlan(context.Background(), CreatePurgePlanRequest{
			Actor: actor, RepositoryID: repositoryID, ExpectedImpactRevision: preview.ImpactRevision, Items: []PurgePlanItemInput{stale},
		})
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("CreatePlan stale revisions %+v: %v, want ErrConflict", stale, err)
		}
	}
	plan, err := service.CreatePlan(context.Background(), CreatePurgePlanRequest{
		Actor: actor, RepositoryID: repositoryID, ExpectedImpactRevision: preview.ImpactRevision, Items: []PurgePlanItemInput{current},
	})
	if err != nil {
		t.Fatalf("CreatePlan current revisions: %v", err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("point_revision", 3).Error; err != nil {
		t.Fatalf("advance point revision: %v", err)
	}
	_, err = service.Execute(context.Background(), ExecutePurgeRequest{
		Actor: actor, RepositoryID: repositoryID, PlanID: plan.ID, ExpectedRevision: plan.Revision,
		ExpectedImpactRevision: plan.ImpactRevision, Reason: "stale-point-test", ProofDigest: strings.Repeat("c", 64),
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("Execute stale point revision: %v, want ErrConflict", err)
	}
	assertNoClaimedAttempts(t, db)
}
