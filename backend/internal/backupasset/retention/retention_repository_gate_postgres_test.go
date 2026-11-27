package retention

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The preview locks points in request order, whereas policy selection visits
// newest points first. The pinned newer point makes that inversion observable.
func TestRetentionRepositoryGatePostgresSerializesPreviewAndRetention(t *testing.T) {
	for _, mode := range []string{"repository selection", "task-link selection", "managed handoff"} {
		t.Run(mode, func(t *testing.T) {
			db := newLifecycleCoordinatorPostgresTestDB(t)
			now := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
			repositoryID, otherRepositoryID := testOpaqueID(3100), testOpaqueID(3101)
			linkID, repositoryPolicyID, taskPolicyID, otherPolicyID := testOpaqueID(3102), testOpaqueID(3103), testOpaqueID(3104), testOpaqueID(3105)
			taskID, taskPolicyBinding := uint(77), uint(91)
			seedRetentionUsersAndRepository(t, db, repositoryID)
			seedRetentionUsersAndRepository(t, db, otherRepositoryID)
			if err := db.Exec(`CREATE TABLE tasks (id bigint PRIMARY KEY, policy_id bigint, archived_at timestamptz)`).Error; err != nil {
				t.Fatalf("create task binding table: %v", err)
			}
			if err := db.Exec(`INSERT INTO tasks (id, policy_id) VALUES (?, ?)`, taskID, taskPolicyBinding).Error; err != nil {
				t.Fatalf("seed task binding: %v", err)
			}
			if err := db.Create(&model.TaskRepositoryLink{
				ID: linkID, TaskID: &taskID, RepositoryID: repositoryID, TaskNameSnapshot: "managed task",
				PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: now,
			}).Error; err != nil {
				t.Fatalf("seed task link: %v", err)
			}
			policies := []model.BackupRetentionPolicy{
				{ID: repositoryPolicyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID},
				{ID: taskPolicyID, ScopeKind: string(backupasset.RetentionPolicyScopeTaskLink), ScopeID: linkID},
				{ID: otherPolicyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: otherRepositoryID},
			}
			for i := range policies {
				policies[i].Revision = 1
				policies[i].RulesJSON = `{"version":1,"count":{"keep_latest":1}}`
				policies[i].Status = string(backupasset.RetentionPolicyActive)
				policies[i].CreatedBy, policies[i].UpdatedBy = 1, 1
				policies[i].CreatedAt, policies[i].UpdatedAt = now, now
			}
			if err := db.Create(&policies).Error; err != nil {
				t.Fatalf("seed retention policies: %v", err)
			}
			older := newSelectionPoint(testOpaqueID(3106), repositoryID, &taskID, now.Add(-3*time.Hour), 2)
			middle := newSelectionPoint(testOpaqueID(3107), repositoryID, &taskID, now.Add(-2*time.Hour), 3)
			newer := newSelectionPoint(testOpaqueID(3108), repositoryID, &taskID, now.Add(-time.Hour), 4)
			if err := db.Create(&[]model.RecoveryPoint{older, middle, newer}).Error; err != nil {
				t.Fatalf("seed recovery points: %v", err)
			}
			purge := newPostgresPurgePreviewService(t, db, now)
			policyService, err := NewPolicyService(PolicyServiceDependencies{DB: db, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatalf("NewPolicyService: %v", err)
			}
			facade, err := NewManagedTaskRetentionFacade(ManagedTaskRetentionFacadeDependencies{
				DB: db, Policies: policyService, Coordinator: purge.coordinator, Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatalf("NewManagedTaskRetentionFacade: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			holder := db.WithContext(ctx).Begin()
			if holder.Error != nil {
				cancel()
				t.Fatalf("begin newer point holder: %v", holder.Error)
			}
			var workers sync.WaitGroup
			defer func() {
				cancel()
				_ = holder.Rollback().Error
				workers.Wait()
			}()
			var holderPID int
			if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
				t.Fatalf("read holder PID: %v", err)
			}
			var locked model.RecoveryPoint
			if err := holder.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", newer.ID).Error; err != nil {
				t.Fatalf("pin newer point: %v", err)
			}
			type previewResult struct {
				view PurgePlanView
				err  error
			}
			previewCh := make(chan previewResult, 1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				view, err := purge.Preview(ctx, PreviewPurgeRequest{
					Actor:        backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
					RepositoryID: repositoryID, RecoveryPointIDs: []string{older.ID, newer.ID},
				})
				previewCh <- previewResult{view: view, err: err}
			}()
			previewPID := waitForRetentionGateBlocker[previewResult, struct{}](t, ctx, db, holderPID, false, previewCh, nil)
			assertRetentionGateRowLock(t, ctx, db, "recovery_points", older.ID, true)

			type retentionResult struct {
				selection Selection
				err       error
			}
			retentionCh := make(chan retentionResult, 1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				if mode == "managed handoff" {
					err := facade.EnforceManagedRetention(ctx, task.ManagedRecoveryPointRetentionRequest{
						TaskID: taskID, PolicyID: taskPolicyBinding, RepositoryID: repositoryID,
						RecoveryPointIDs: []string{older.ID, newer.ID},
					})
					retentionCh <- retentionResult{err: err}
					return
				}
				policyID := repositoryPolicyID
				if mode == "task-link selection" {
					policyID = taskPolicyID
				}
				selection, err := policyService.Select(ctx, SelectionRequest{PolicyID: policyID, ExpectedRevision: 1, EvaluatedAt: now})
				retentionCh <- retentionResult{selection: selection, err: err}
			}()
			waitForRetentionGateBlocker(t, ctx, db, previewPID, true, previewCh, retentionCh)

			// None of these business rows may be locked by the waiting retention transaction.
			assertRetentionGateRowLock(t, ctx, db, "recovery_points", middle.ID, false)
			assertRetentionGateRowLock(t, ctx, db, "backup_retention_policies", repositoryPolicyID, false)
			assertRetentionGateRowLock(t, ctx, db, "backup_retention_policies", taskPolicyID, false)
			assertRetentionGateRowLock(t, ctx, db, "task_repository_links", linkID, false)
			assertRetentionGateRowLock(t, ctx, db, "tasks", taskID, false)
			other, err := policyService.Select(ctx, SelectionRequest{PolicyID: otherPolicyID, ExpectedRevision: 1, EvaluatedAt: now})
			if err != nil || len(other.Points) != 0 {
				t.Fatalf("independent repository selection = %+v, %v", other, err)
			}

			if err := holder.Commit().Error; err != nil {
				t.Fatalf("release newer point: %v", err)
			}
			var preview previewResult
			select {
			case preview = <-previewCh:
			case <-ctx.Done():
				t.Fatalf("preview did not finish: %v", ctx.Err())
			}
			wantPreview := []PurgePlanItemView{
				{RecoveryPointID: older.ID, PointRevision: older.PointRevision, CapabilityRevision: older.CapabilityRevision},
				{RecoveryPointID: newer.ID, PointRevision: newer.PointRevision, CapabilityRevision: newer.CapabilityRevision},
			}
			if preview.err != nil || preview.view.ItemCount != 2 || !reflect.DeepEqual(preview.view.Items, wantPreview) || preview.view.ID != "" {
				t.Fatalf("ordered preview = %+v, %v; want %+v", preview.view, preview.err, wantPreview)
			}
			var result retentionResult
			select {
			case result = <-retentionCh:
			case <-ctx.Done():
				t.Fatalf("retention did not finish: %v", ctx.Err())
			}
			if result.err != nil {
				t.Fatalf("retention operation: %v", result.err)
			}
			if mode == "managed handoff" {
				var attempts []model.RecoveryPointLifecycleAttempt
				if err := db.Find(&attempts).Error; err != nil {
					t.Fatalf("load lifecycle attempts: %v", err)
				}
				if len(attempts) != 1 || attempts[0].RecoveryPointID != older.ID || attempts[0].Operation != string(backupasset.LifecycleRetentionExpire) {
					t.Fatalf("handoff attempts = %+v, want only older handed-off point", attempts)
				}
			} else {
				want := []SelectedPoint{
					{RecoveryPointID: middle.ID, PointRevision: middle.PointRevision, CapabilityRevision: middle.CapabilityRevision},
					{RecoveryPointID: older.ID, PointRevision: older.PointRevision, CapabilityRevision: older.CapabilityRevision},
				}
				if !reflect.DeepEqual(result.selection.Points, want) {
					t.Fatalf("selection = %+v, want %+v", result.selection.Points, want)
				}
			}
		})
	}
}

// Find the backend that is blocked by blockerPID; PostgreSQL reports the actual
// wait-for edge, avoiding timing assumptions about when a goroutine starts.
func waitForRetentionGateBlocker[P any, R any](t *testing.T, ctx context.Context, db *gorm.DB, blockerPID int, advisory bool, preview <-chan P, retention <-chan R) int {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		if err := db.WithContext(ctx).Raw(`SELECT COALESCE((SELECT pid FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND ? = ANY(pg_blocking_pids(pid))
			  AND (? = false OR (wait_event = 'advisory' AND query ILIKE '%pg_advisory_xact_lock%')) LIMIT 1), 0)`, blockerPID, advisory).Scan(&pid).Error; err != nil {
			t.Fatalf("inspect PostgreSQL wait graph: %v", err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case result := <-preview:
			t.Fatalf("preview finished before expected lock wait: %+v", result)
		case result := <-retention:
			t.Fatalf("retention finished before expected lock wait: %+v", result)
		case <-ctx.Done():
			t.Fatalf("missing lock wait behind PID %d: %v", blockerPID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertRetentionGateRowLock(t *testing.T, ctx context.Context, db *gorm.DB, table string, id any, wantBlocked bool) {
	t.Helper()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin %s lock probe: %v", table, tx.Error)
	}
	var found string
	result := tx.Raw("SELECT id::text FROM "+table+" WHERE id = ? FOR UPDATE NOWAIT", id).Scan(&found)
	err := result.Error
	_ = tx.Rollback().Error
	if !wantBlocked {
		if err != nil || result.RowsAffected != 1 {
			t.Fatalf("%s row %v should remain lockable: rows=%d error=%v", table, id, result.RowsAffected, err)
		}
		return
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("%s row %v lock probe = %v, want PostgreSQL lock_not_available", table, id, err)
	}
}
