package repository

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// waitRsyncRepositoryAuthorityGate identifies both the holder and the exact
// repository advisory key, so a different blocked query cannot satisfy the test.
func waitRsyncRepositoryAuthorityGate(t *testing.T, ctx context.Context, db *gorm.DB, holderPID int, repositoryID string, done <-chan struct{}) {
	t.Helper()
	key := "xirang:recovery-authority-repository:v1:" + repositoryID
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiterPID int
		err := db.WithContext(ctx).Raw(`SELECT COALESCE((
			SELECT waiting.pid FROM pg_locks waiting
			JOIN pg_locks held ON held.locktype = waiting.locktype AND held.database = waiting.database
				AND held.classid = waiting.classid AND held.objid = waiting.objid AND held.objsubid = waiting.objsubid
			WHERE waiting.locktype = 'advisory' AND NOT waiting.granted AND waiting.mode = 'ExclusiveLock'
				AND held.granted AND held.mode = 'ExclusiveLock' AND held.pid = ?
				AND held.pid = ANY(pg_blocking_pids(waiting.pid)) AND waiting.objsubid = 1
				AND waiting.classid::bigint = ((hashtextextended(?, 0) >> 32) & 4294967295)
				AND waiting.objid::bigint = (hashtextextended(?, 0) & 4294967295)
			LIMIT 1), 0)`, holderPID, key, key).Scan(&waiterPID).Error
		if err != nil {
			t.Fatalf("inspect Rsync repository advisory wait: %v", err)
		}
		if waiterPID != 0 {
			return
		}
		select {
		case <-done:
			t.Fatal("Rsync operation finished before waiting on the repository gate")
		case <-ctx.Done():
			t.Fatalf("waiting for Rsync repository gate: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestPrepareRsyncVersioningRollbackAuthorityLockPostgres(t *testing.T) {
	for _, scenario := range []string{"success", "active_link_replacement", "revision_drift", "cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newPostgresRsyncRestoreResolverFixture(t)
			fixture.service.admission = &rsyncVersioningTransitioner{}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var point model.RecoveryPoint
			if err := fixture.db.WithContext(ctx).First(&point, "id = ?", fixture.ref.RecoveryPointID).Error; err != nil {
				t.Fatal(err)
			}
			if point.ProducingTaskID == nil || point.State != string(backupasset.RecoveryPointCommitted) {
				t.Fatalf("fixture requires a committed point with a producing Task: %+v", point)
			}
			taskID := *point.ProducingTaskID
			nextRun := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
			if err := fixture.db.WithContext(ctx).Model(&model.Task{}).Where("id = ?", taskID).
				Updates(map[string]any{"enabled": true, "skip_next": true, "next_run_at": nextRun}).Error; err != nil {
				t.Fatal(err)
			}
			var beforeTask model.Task
			var beforeLink model.TaskRepositoryLink
			var beforeBinding model.RepositoryAccessBinding
			if err := fixture.db.WithContext(ctx).First(&beforeTask, taskID).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskID).First(&beforeLink).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.WithContext(ctx).Where("repository_id = ? AND status = ?", fixture.ref.RepositoryID, bindingStatusActive).First(&beforeBinding).Error; err != nil {
				t.Fatal(err)
			}
			revision, err := managedRsyncTaskRevision(beforeTask)
			if err != nil {
				t.Fatal(err)
			}
			baselineAuthority, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(ctx, fixture.ref)
			if err != nil {
				t.Fatalf("capture real Recovery authority before rollback: %v", err)
			}

			holder := fixture.db.WithContext(ctx).Begin()
			if holder.Error != nil {
				t.Fatal(holder.Error)
			}
			defer holder.Rollback()
			var holderPID int
			if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
				t.Fatal(err)
			}
			key := "xirang:recovery-authority-repository:v1:" + fixture.ref.RepositoryID
			if err := holder.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
				t.Fatal(err)
			}

			type rollbackResult struct {
				result backupasset.RsyncVersioningRollbackPreparationResult
				err    error
			}
			operationCtx, cancelOperation := context.WithCancel(ctx)
			results := make(chan rollbackResult, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				result, err := fixture.service.PrepareRsyncVersioningRollback(operationCtx, backupasset.RsyncVersioningRollbackPreparationRequest{
					TaskID: taskID, ExpectedTaskRevision: revision,
				})
				results <- rollbackResult{result: result, err: err}
			}()
			defer func() {
				cancelOperation()
				_ = holder.Rollback().Error
				select {
				case <-done:
				case <-ctx.Done():
				}
			}()
			waitRsyncRepositoryAuthorityGate(t, ctx, fixture.db, holderPID, fixture.ref.RepositoryID, done)

			// The waiter must not own any of the rows Recovery can need while it
			// holds this repository gate. NOWAIT makes the lock order observable.
			if err := fixture.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var foundTask uint
				if result := tx.Raw("SELECT id FROM tasks WHERE id = ? FOR UPDATE NOWAIT", taskID).Scan(&foundTask); result.Error != nil || result.RowsAffected != 1 {
					if result.Error != nil {
						return result.Error
					}
					return gorm.ErrRecordNotFound
				}
				var foundLink, foundRepository string
				if result := tx.Raw("SELECT id FROM task_repository_links WHERE id = ? FOR UPDATE NOWAIT", beforeLink.ID).Scan(&foundLink); result.Error != nil || result.RowsAffected != 1 {
					if result.Error != nil {
						return result.Error
					}
					return gorm.ErrRecordNotFound
				}
				if result := tx.Raw("SELECT id FROM backup_repositories WHERE id = ? FOR UPDATE NOWAIT", fixture.ref.RepositoryID).Scan(&foundRepository); result.Error != nil || result.RowsAffected != 1 {
					if result.Error != nil {
						return result.Error
					}
					return gorm.ErrRecordNotFound
				}
				return nil
			}); err != nil {
				t.Fatalf("rollback locked a shared row before its repository gate: %v", err)
			}

			expectedTask := beforeTask
			expectedBinding := beforeBinding
			switch scenario {
			case "revision_drift":
				driftTime := beforeTask.UpdatedAt.Add(time.Minute)
				if err := fixture.db.WithContext(ctx).Model(&model.Task{}).Where("id = ?", taskID).UpdateColumn("updated_at", driftTime).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.WithContext(ctx).First(&expectedTask, taskID).Error; err != nil {
					t.Fatal(err)
				}
			case "active_link_replacement":
				if err := fixture.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					unlinkedAt := beforeLink.LinkedAt.Add(time.Minute)
					if err := tx.Model(&model.TaskRepositoryLink{}).Where("id = ?", beforeLink.ID).UpdateColumn("unlinked_at", unlinkedAt).Error; err != nil {
						return err
					}
					replacement := beforeLink
					replacement.ID = strings.Repeat("a", 32)
					if err := tx.Create(&replacement).Error; err != nil {
						return err
					}
					// Keep the replacement internally valid so rejection proves the
					// pre-gate link identity was revalidated, not just binding drift.
					stored, err := decodeStoredBindingDocument(beforeBinding.EncryptedConfig)
					if err != nil {
						return err
					}
					if stored.ManagedRsyncV2 == nil {
						return backupasset.ErrInvalidState
					}
					stored.ManagedRsyncV2.TaskRepositoryLinkID = replacement.ID
					encoded, err := encodeManagedRsyncBindingDocumentV2(*stored.ManagedRsyncV2)
					if err != nil {
						return err
					}
					replacementBinding := beforeBinding
					replacementBinding.EncryptedConfig = encoded
					return tx.Save(&replacementBinding).Error
				}); err != nil {
					t.Fatalf("replace active link while rollback waits: %v", err)
				}
				if err := fixture.db.WithContext(ctx).First(&expectedBinding, "id = ?", beforeBinding.ID).Error; err != nil {
					t.Fatal(err)
				}
				var unchangedTask model.Task
				if err := fixture.db.WithContext(ctx).First(&unchangedTask, taskID).Error; err != nil || !reflect.DeepEqual(unchangedTask, beforeTask) {
					t.Fatalf("link replacement changed Task: equal=%t err=%v", reflect.DeepEqual(unchangedTask, beforeTask), err)
				}
			}

			if scenario == "cancellation" {
				cancelOperation()
			} else if err := holder.Commit().Error; err != nil {
				t.Fatalf("release repository gate: %v", err)
			}
			var outcome rollbackResult
			select {
			case outcome = <-results:
			case <-ctx.Done():
				t.Fatalf("rollback did not finish: %v", ctx.Err())
			}
			if scenario == "success" {
				if outcome.err != nil || outcome.result.Summary.Mode != backupasset.PublicationVersionedFullCopy ||
					outcome.result.Summary.State != backupasset.RsyncVersioningRollbackPrepared ||
					outcome.result.Summary.ReasonCode != backupasset.RsyncVersioningReasonRollbackPrepared {
					t.Fatalf("rollback preparation: result=%+v err=%v", outcome.result, outcome.err)
				}
			} else if scenario == "cancellation" {
				if !errors.Is(outcome.err, context.Canceled) {
					t.Fatalf("canceled rollback error = %v", outcome.err)
				}
			} else if !errors.Is(outcome.err, backupasset.ErrConflict) {
				t.Fatalf("rollback accepted drift or returned wrong error: %v", outcome.err)
			}

			var afterTask model.Task
			var afterBinding model.RepositoryAccessBinding
			var afterPoint model.RecoveryPoint
			if err := fixture.db.WithContext(ctx).First(&afterTask, taskID).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.WithContext(ctx).First(&afterBinding, "id = ?", beforeBinding.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.WithContext(ctx).First(&afterPoint, "id = ?", point.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(afterPoint, point) {
				t.Fatal("rollback changed the committed recovery point")
			}
			if scenario == "success" {
				if afterTask.Enabled || afterTask.SkipNext || afterTask.NextRunAt != nil || afterTask.RsyncTarget != beforeTask.RsyncTarget {
					t.Fatalf("rollback did not pause the Task while retaining the legacy target: %+v", afterTask)
				}
				stored, err := decodeStoredBindingDocument(afterBinding.EncryptedConfig)
				if err != nil || stored.ManagedRsyncV2 == nil || !stored.ManagedRsyncV2.RollbackPrepared {
					t.Fatalf("rollback did not persist its binding: err=%v", err)
				}
				if err := fixture.service.revalidateRecoveryRsyncAuthoritySnapshot(ctx, fixture.ref, baselineAuthority); !errors.Is(err, provider.ErrInvalidRestoreRequest) {
					t.Fatalf("real Recovery authority accepted the pre-rollback snapshot: %v", err)
				}
			} else {
				if !reflect.DeepEqual(afterTask, expectedTask) || !reflect.DeepEqual(afterBinding, expectedBinding) {
					t.Fatalf("failed rollback changed durable Task or binding: task equal=%t binding equal=%t", reflect.DeepEqual(afterTask, expectedTask), reflect.DeepEqual(afterBinding, expectedBinding))
				}
			}
		})
	}
}
