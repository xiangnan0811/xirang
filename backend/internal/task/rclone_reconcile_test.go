package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	gormrepo "xirang/backend/internal/repository/gorm"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func legacyRcloneReconcileActor() credentialaudit.Event {
	return credentialaudit.Event{UserID: 41, Username: "reconcile-admin", Role: "admin"}
}

func seedPausedUnknownRcloneRun(t *testing.T, db *gorm.DB) (model.Task, model.TaskRun) {
	t.Helper()
	taskEntity := seedLegacyRcloneTask(t, db)
	lastError := "RCLONE_UNKNOWN_REMOTE_DIAGNOSTIC_FOR_TEST_ONLY"
	run := model.TaskRun{
		TaskID:                  taskEntity.ID,
		NodeIDSnapshot:          taskEntity.NodeID,
		TriggerType:             "manual",
		Status:                  model.TaskRunStatusFailed,
		BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(taskEntity),
		BackupGenerationState:   model.TaskRunGenerationStateUnknown,
		LastError:               lastError,
		CreatedAt:               time.Now().UTC().Add(-time.Minute),
		UpdatedAt:               time.Now().UTC().Add(-time.Minute),
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create unresolved Rclone run: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]any{
		"enabled":     false,
		"status":      string(StatusFailed),
		"last_error":  "RCLONE_TASK_DIAGNOSTIC_FOR_TEST_ONLY",
		"next_run_at": nil,
	}).Error; err != nil {
		t.Fatalf("pause unresolved Rclone task: %v", err)
	}
	if err := db.First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload paused unresolved Rclone task: %v", err)
	}
	return taskEntity, run
}

func countLegacyRcloneReconcileAudits(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&model.CredentialAuditEvent{}).
		Where("action = ?", "task.legacy_rclone_reconcile").Count(&count).Error; err != nil {
		t.Fatalf("count reconciliation audits: %v", err)
	}
	return count
}

func TestReconcileLegacyRcloneWriteRequiresNewGenerationBeforeRestore(t *testing.T) {
	run := func(t *testing.T, db *gorm.DB) {
		t.Helper()
		taskEntity := seedLegacyRcloneTask(t, db)
		verified := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess,
			model.TaskRunGenerationStateVerified, "", time.Now().UTC().Add(-3*time.Minute))
		unknown := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusFailed,
			model.TaskRunGenerationStateUnknown, "RCLONE_UNKNOWN_REMOTE_DIAGNOSTIC_FOR_TEST_ONLY", time.Now().UTC().Add(-2*time.Minute))
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]any{
			"enabled":     false,
			"status":      string(StatusFailed),
			"last_error":  "RCLONE_TASK_DIAGNOSTIC_FOR_TEST_ONLY",
			"next_run_at": nil,
		}).Error; err != nil {
			t.Fatalf("pause unresolved Rclone task: %v", err)
		}
		if _, err := (&Manager{db: db}).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); !errors.Is(err, ErrRestoreRequiresNewBackup) {
			t.Fatalf("restore before reconciliation error=%v, want ErrRestoreRequiresNewBackup", err)
		}

		// The persisted target may have been edited since the failed run. The
		// reconciliation decision is based on the selected run's evidence, not
		// on reclassifying the current mutable target.
		changedTarget := filepath.Join(t.TempDir(), "edited-target")
		editedNode := model.Node{Name: "reconcile-edited-node", Host: "edited.example.invalid", Port: 22, Username: "root", AuthType: "key", BackupDir: t.TempDir()}
		if err := db.Create(&editedNode).Error; err != nil {
			t.Fatalf("create edited node: %v", err)
		}
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]any{
			"rsync_target": changedTarget, "node_id": editedNode.ID,
		}).Error; err != nil {
			t.Fatalf("edit current Rclone target and node: %v", err)
		}
		reason := "operator confirmed the remote writer stopped after inspection"
		if err := ReconcileLegacyRcloneWrite(context.Background(), db, LegacyRcloneReconcileRequest{
			TaskID: taskEntity.ID, TaskRunID: unknown.ID, RemoteStopped: true,
			Reason: reason, Actor: legacyRcloneReconcileActor(),
		}); err != nil {
			t.Fatalf("reconcile unknown Rclone run: %v", err)
		}

		var reconciled model.TaskRun
		if err := db.First(&reconciled, unknown.ID).Error; err != nil {
			t.Fatalf("reload reconciled Rclone run: %v", err)
		}
		if reconciled.Status != model.TaskRunStatusFailed || reconciled.BackupGenerationState != model.TaskRunGenerationStateDirty {
			t.Fatalf("reconciled run status=%q state=%q, want failed/dirty", reconciled.Status, reconciled.BackupGenerationState)
		}
		if reconciled.LastError != "RCLONE_UNKNOWN_REMOTE_DIAGNOSTIC_FOR_TEST_ONLY" {
			t.Fatalf("reconciled run diagnostics=%q were overwritten", reconciled.LastError)
		}
		var paused model.Task
		if err := db.First(&paused, taskEntity.ID).Error; err != nil {
			t.Fatalf("reload paused task: %v", err)
		}
		if paused.Enabled || paused.LastError != "RCLONE_TASK_DIAGNOSTIC_FOR_TEST_ONLY" {
			t.Fatalf("reconciliation changed paused task enabled=%v last_error=%q", paused.Enabled, paused.LastError)
		}
		if _, err := (&Manager{db: db}).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); !errors.Is(err, ErrRestoreRequiresNewBackup) {
			t.Fatalf("restore after reconciliation error=%v, want ErrRestoreRequiresNewBackup", err)
		}

		var audit model.CredentialAuditEvent
		if err := db.Where("action = ?", "task.legacy_rclone_reconcile").Order("id DESC").First(&audit).Error; err != nil {
			t.Fatalf("load reconciliation audit: %v", err)
		}
		if audit.UserID != 41 || audit.Action != "task.legacy_rclone_reconcile" || audit.Purpose != "task_command" ||
			audit.NodeID == nil || *audit.NodeID != taskEntity.NodeID ||
			audit.TaskID == nil || *audit.TaskID != taskEntity.ID || audit.TaskRunID == nil || *audit.TaskRunID != unknown.ID ||
			audit.Outcome != credentialaudit.OutcomeSuccess {
			t.Fatalf("reconciliation audit identity/outcome=%+v", audit)
		}
		var metadata map[string]any
		if err := json.Unmarshal([]byte(audit.Metadata), &metadata); err != nil {
			t.Fatalf("decode reconciliation audit metadata: %v", err)
		}
		if metadata["old_state"] != model.TaskRunGenerationStateUnknown || metadata["new_state"] != model.TaskRunGenerationStateDirty ||
			metadata["remote_stopped"] != true || metadata["reason"] != reason {
			t.Fatalf("reconciliation audit metadata=%v", metadata)
		}

		manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		if err := manager.Resume(taskEntity.ID); err != nil {
			t.Fatalf("resume after reconciliation: %v", err)
		}
		cID, err := manager.TriggerManual(taskEntity.ID)
		if err != nil || cID == 0 {
			t.Fatalf("trigger new Rclone generation c id=%d err=%v", cID, err)
		}
		completed := waitTaskRunTerminal(t, db, cID)
		if completed.Status != model.TaskRunStatusSuccess || completed.BackupGenerationState != model.TaskRunGenerationStateVerified {
			t.Fatalf("new Rclone generation status=%q state=%q error=%q, want success/verified", completed.Status, completed.BackupGenerationState, completed.LastError)
		}
		if completed.ID == verified.ID || completed.ID == unknown.ID {
			t.Fatalf("new Rclone generation reused historical run id=%d", completed.ID)
		}
		if _, err := (&Manager{db: db}).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err != nil {
			t.Fatalf("restore after completed new generation: %v", err)
		}
	}
	t.Run("sqlite", func(t *testing.T) { run(t, openManagerTestDB(t)) })
}

func TestReconcileLegacyRcloneWriteRequiresNewGenerationBeforeRestorePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(&model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate PostgreSQL credential audit table: %v", err)
	}
	// Keep the PostgreSQL proof focused on the same externally visible domain
	// transition; the SQLite subtest above also exercises the manager's C path.
	taskEntity, run := seedPausedUnknownRcloneRun(t, db)
	if err := ReconcileLegacyRcloneWrite(context.Background(), db, LegacyRcloneReconcileRequest{
		TaskID: taskEntity.ID, TaskRunID: run.ID, RemoteStopped: true,
		Reason: "postgres operator confirmation", Actor: legacyRcloneReconcileActor(),
	}); err != nil {
		t.Fatalf("reconcile PostgreSQL unknown Rclone run: %v", err)
	}
	var reconciled model.TaskRun
	if err := db.First(&reconciled, run.ID).Error; err != nil {
		t.Fatalf("reload PostgreSQL reconciled run: %v", err)
	}
	if reconciled.BackupGenerationState != model.TaskRunGenerationStateDirty || reconciled.LastError != run.LastError {
		t.Fatalf("PostgreSQL reconciled run=%+v", reconciled)
	}
	if got := countLegacyRcloneReconcileAudits(t, db); got != 1 {
		t.Fatalf("PostgreSQL reconciliation audit count=%d, want 1", got)
	}
}

func TestReconcileLegacyRcloneWriteRejectsInvalidStates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, db *gorm.DB, taskEntity model.Task, run model.TaskRun, request *LegacyRcloneReconcileRequest)
		want  error
	}{
		{
			name: "unconfirmed remote",
			setup: func(_ *testing.T, _ *gorm.DB, _ model.Task, _ model.TaskRun, request *LegacyRcloneReconcileRequest) {
				request.RemoteStopped = false
			},
			want: apperrErrValidationForTest(),
		},
		{
			name: "nonadmin actor",
			setup: func(_ *testing.T, _ *gorm.DB, _ model.Task, _ model.TaskRun, request *LegacyRcloneReconcileRequest) {
				request.Actor.Role = "operator"
			},
			want: apperrErrValidationForTest(),
		},
		{
			name: "blank reason",
			setup: func(_ *testing.T, _ *gorm.DB, _ model.Task, _ model.TaskRun, request *LegacyRcloneReconcileRequest) {
				request.Reason = " \t\n"
			},
			want: apperrErrValidationForTest(),
		},
		{
			name: "oversized reason",
			setup: func(_ *testing.T, _ *gorm.DB, _ model.Task, _ model.TaskRun, request *LegacyRcloneReconcileRequest) {
				request.Reason = strings.Repeat("x", legacyRcloneReconcileReasonMaxRunes+1)
			},
			want: apperrErrValidationForTest(),
		},
		{
			name: "enabled task",
			setup: func(t *testing.T, db *gorm.DB, taskEntity model.Task, _ model.TaskRun, _ *LegacyRcloneReconcileRequest) {
				if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("enabled", true).Error; err != nil {
					t.Fatalf("enable task: %v", err)
				}
			},
			want: apperrErrConflictForTest(),
		},
		{
			name: "live lease",
			setup: func(t *testing.T, db *gorm.DB, _ model.Task, run model.TaskRun, _ *LegacyRcloneReconcileRequest) {
				lease := time.Now().UTC().Add(time.Hour)
				if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
					"execution_owner_id": "live-owner", "execution_lease_until": &lease,
				}).Error; err != nil {
					t.Fatalf("set live lease: %v", err)
				}
			},
			want: apperrErrConflictForTest(),
		},
		{
			name: "unbounded owner",
			setup: func(t *testing.T, db *gorm.DB, _ model.Task, run model.TaskRun, _ *LegacyRcloneReconcileRequest) {
				if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Update("execution_owner_id", "unbounded-owner").Error; err != nil {
					t.Fatalf("set unbounded owner: %v", err)
				}
			},
			want: apperrErrConflictForTest(),
		},
		{
			name: "wrong run",
			setup: func(_ *testing.T, _ *gorm.DB, _ model.Task, run model.TaskRun, request *LegacyRcloneReconcileRequest) {
				request.TaskRunID = run.ID + 1000000
			},
			want: apperrErrNotFoundForTest(),
		},
		{
			name: "restore run",
			setup: func(t *testing.T, db *gorm.DB, _ model.Task, run model.TaskRun, _ *LegacyRcloneReconcileRequest) {
				if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Update("trigger_type", "restore").Error; err != nil {
					t.Fatalf("mark restore run: %v", err)
				}
			},
			want: apperrErrConflictForTest(),
		},
		{
			name: "active writing missing lease",
			setup: func(t *testing.T, db *gorm.DB, taskEntity model.Task, run model.TaskRun, _ *LegacyRcloneReconcileRequest) {
				started := time.Now().UTC().Add(-time.Minute)
				if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("status", string(StatusRunning)).Error; err != nil {
					t.Fatalf("mark task running: %v", err)
				}
				if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
					"status": model.TaskRunStatusRunning, "backup_generation_state": model.TaskRunGenerationStateWriting,
					"started_at": &started, "execution_owner_id": "", "execution_lease_until": nil,
				}).Error; err != nil {
					t.Fatalf("mark active writing without lease: %v", err)
				}
			},
			want: apperrErrConflictForTest(),
		},
		{
			name: "already dirty",
			setup: func(t *testing.T, db *gorm.DB, _ model.Task, run model.TaskRun, _ *LegacyRcloneReconcileRequest) {
				if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Update("backup_generation_state", model.TaskRunGenerationStateDirty).Error; err != nil {
					t.Fatalf("mark dirty run: %v", err)
				}
			},
			want: apperrErrConflictForTest(),
		},
		{
			name: "active sibling",
			setup: func(t *testing.T, db *gorm.DB, taskEntity model.Task, _ model.TaskRun, _ *LegacyRcloneReconcileRequest) {
				sibling := model.TaskRun{TaskID: taskEntity.ID, NodeIDSnapshot: taskEntity.NodeID, TriggerType: "manual", Status: model.TaskRunStatusPending}
				if err := db.Create(&sibling).Error; err != nil {
					t.Fatalf("create active sibling: %v", err)
				}
			},
			want: apperrErrConflictForTest(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openManagerTestDB(t)
			taskEntity, run := seedPausedUnknownRcloneRun(t, db)
			request := LegacyRcloneReconcileRequest{
				TaskID: taskEntity.ID, TaskRunID: run.ID, RemoteStopped: true,
				Reason: "valid operator reason", Actor: legacyRcloneReconcileActor(),
			}
			test.setup(t, db, taskEntity, run, &request)
			var before model.TaskRun
			if err := db.First(&before, run.ID).Error; err != nil {
				t.Fatalf("reload run before rejected reconciliation: %v", err)
			}
			if err := ReconcileLegacyRcloneWrite(context.Background(), db, request); !errors.Is(err, test.want) {
				t.Fatalf("reconciliation error=%v, want errors.Is(...,%v)", err, test.want)
			}
			var after model.TaskRun
			if err := db.First(&after, run.ID).Error; err != nil {
				t.Fatalf("reload rejected run: %v", err)
			}
			if after.Status != before.Status || after.BackupGenerationState != before.BackupGenerationState ||
				after.ExecutionOwnerID != before.ExecutionOwnerID ||
				(after.ExecutionLeaseUntil == nil) != (before.ExecutionLeaseUntil == nil) {
				t.Fatalf("rejected run changed unexpectedly before=%+v after=%+v", before, after)
			}
			if count := countLegacyRcloneReconcileAudits(t, db); count != 0 {
				t.Fatalf("rejected reconciliation wrote %d audit rows", count)
			}
		})
	}
}

// Keep the negative matrix coupled to the public sentinel values without
// introducing task-package aliases or new error contracts.
func apperrErrValidationForTest() error { return apperr.ErrValidation }
func apperrErrConflictForTest() error   { return apperr.ErrConflict }
func apperrErrNotFoundForTest() error   { return apperr.ErrNotFound }

func TestReconcileLegacyRcloneWriteAuditFailureRollsBack(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity, run := seedPausedUnknownRcloneRun(t, db)
	expired := time.Now().UTC().Add(-time.Minute)
	if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"execution_owner_id": "stale-owner", "execution_lease_until": &expired,
	}).Error; err != nil {
		t.Fatalf("set expired owner: %v", err)
	}
	injectedErr := errors.New("RCLONE_RECONCILE_AUDIT_FAILURE_FOR_TEST_ONLY")
	var injected atomic.Bool
	callbackName := "test:rclone-reconcile-audit-failure:" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil &&
			tx.Statement.Schema.Table == "credential_audit_events" && injected.CompareAndSwap(false, true) {
			_ = tx.AddError(injectedErr)
		}
	}); err != nil {
		t.Fatalf("register audit failure callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	err := ReconcileLegacyRcloneWrite(context.Background(), db, LegacyRcloneReconcileRequest{
		TaskID: taskEntity.ID, TaskRunID: run.ID, RemoteStopped: true,
		Reason: "valid operator reason", Actor: legacyRcloneReconcileActor(),
	})
	if !injected.Load() || !errors.Is(err, injectedErr) {
		t.Fatalf("audit failure error=%v injected=%v, want injected error", err, injected.Load())
	}
	var afterRun model.TaskRun
	if err := db.First(&afterRun, run.ID).Error; err != nil {
		t.Fatalf("reload rolled-back run: %v", err)
	}
	if afterRun.BackupGenerationState != model.TaskRunGenerationStateUnknown || afterRun.ExecutionOwnerID != "stale-owner" ||
		afterRun.ExecutionLeaseUntil == nil || afterRun.LastError != run.LastError {
		t.Fatalf("run changed despite audit rollback: %+v", afterRun)
	}
	var afterTask model.Task
	if err := db.First(&afterTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload rolled-back task: %v", err)
	}
	if afterTask.Status != taskEntity.Status || afterTask.Enabled != taskEntity.Enabled || afterTask.LastError != taskEntity.LastError {
		t.Fatalf("task changed despite audit rollback: %+v", afterTask)
	}
	if count := countLegacyRcloneReconcileAudits(t, db); count != 0 {
		t.Fatalf("audit rollback left %d rows", count)
	}
}

func TestReconcileLegacyRcloneWriteFencesExpiredOwner(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity, run := seedPausedUnknownRcloneRun(t, db)
	started := time.Now().UTC().Add(-3 * time.Minute)
	expired := time.Now().UTC().Add(-time.Minute)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("status", string(StatusRunning)).Error; err != nil {
		t.Fatalf("mark task running: %v", err)
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status": model.TaskRunStatusRunning, "backup_generation_state": model.TaskRunGenerationStateWriting,
		"started_at": &started, "execution_owner_id": "stale-owner", "execution_lease_until": &expired,
	}).Error; err != nil {
		t.Fatalf("mark expired writing run: %v", err)
	}
	if err := ReconcileLegacyRcloneWrite(context.Background(), db, LegacyRcloneReconcileRequest{
		TaskID: taskEntity.ID, TaskRunID: run.ID, RemoteStopped: true,
		Reason: "expired writer was independently confirmed stopped", Actor: legacyRcloneReconcileActor(),
	}); err != nil {
		t.Fatalf("reconcile expired writing run: %v", err)
	}
	var afterRun model.TaskRun
	if err := db.First(&afterRun, run.ID).Error; err != nil {
		t.Fatalf("reload fenced run: %v", err)
	}
	if afterRun.Status != model.TaskRunStatusFailed || afterRun.BackupGenerationState != model.TaskRunGenerationStateDirty ||
		afterRun.ExecutionOwnerID != "" || afterRun.ExecutionLeaseUntil != nil || afterRun.LastError != run.LastError {
		t.Fatalf("expired owner was not fenced coherently: %+v", afterRun)
	}
	var afterTask model.Task
	if err := db.First(&afterTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload settled task: %v", err)
	}
	if afterTask.Status != string(StatusFailed) || afterTask.Enabled || afterTask.LastError != taskEntity.LastError {
		t.Fatalf("aggregate settlement changed paused task incorrectly: %+v", afterTask)
	}

	staleManager := &Manager{db: db, stateMachine: NewStateMachine(), executionOwnerID: "stale-owner"}
	if err := staleManager.terminalizeTaskRun(context.Background(), taskEntity.ID, run.ID,
		[]string{model.TaskRunStatusRunning}, taskTerminalStatusPtr(StatusSuccess),
		map[string]any{"last_error": "stale owner must not publish"}, StatusSuccess,
		map[string]any{"last_error": "stale owner must not publish"}); err == nil {
		t.Fatal("stale owner terminalization unexpectedly succeeded after fencing")
	}
	if err := db.First(&afterRun, run.ID).Error; err != nil {
		t.Fatalf("reload run after stale owner attempt: %v", err)
	}
	if afterRun.Status != model.TaskRunStatusFailed || afterRun.BackupGenerationState != model.TaskRunGenerationStateDirty || afterRun.LastError != run.LastError {
		t.Fatalf("stale owner changed reconciled result: %+v", afterRun)
	}
}

func TestReconcileLegacyRcloneWriteSerializesReservationAndCancellation(t *testing.T) {
	t.Run("reservation sibling", func(t *testing.T) {
		db := openConcurrentManagerTestDB(t)
		taskEntity, run := seedPausedUnknownRcloneRun(t, db)
		ready := make(chan struct{})
		release := make(chan struct{})
		holderErr := make(chan error, 1)
		go func() {
			holderErr <- db.Transaction(func(tx *gorm.DB) error {
				if err := gormrepo.LockTaskIDsForUpdate(tx, []uint{taskEntity.ID}); err != nil {
					return err
				}
				sibling := model.TaskRun{TaskID: taskEntity.ID, NodeIDSnapshot: taskEntity.NodeID, TriggerType: "manual", Status: model.TaskRunStatusPending}
				if err := tx.Create(&sibling).Error; err != nil {
					return err
				}
				close(ready)
				<-release
				return nil
			})
		}()
		<-ready
		reconcileErr := make(chan error, 1)
		go func() {
			reconcileErr <- ReconcileLegacyRcloneWrite(context.Background(), db, LegacyRcloneReconcileRequest{
				TaskID: taskEntity.ID, TaskRunID: run.ID, RemoteStopped: true,
				Reason: "reservation race", Actor: legacyRcloneReconcileActor(),
			})
		}()
		close(release)
		if err := <-holderErr; err != nil {
			t.Fatalf("reservation holder transaction: %v", err)
		}
		if err := <-reconcileErr; !errors.Is(err, apperr.ErrConflict) {
			t.Fatalf("reconciliation raced reservation error=%v, want conflict", err)
		}
		var after model.TaskRun
		if err := db.First(&after, run.ID).Error; err != nil {
			t.Fatalf("reload run after reservation race: %v", err)
		}
		if after.BackupGenerationState != model.TaskRunGenerationStateUnknown {
			t.Fatalf("reservation race changed unresolved state=%q", after.BackupGenerationState)
		}
	})

	t.Run("cancellation winner", func(t *testing.T) {
		db := openConcurrentManagerTestDB(t)
		taskEntity, run := seedPausedUnknownRcloneRun(t, db)
		started := time.Now().UTC().Add(-time.Minute)
		expired := time.Now().UTC().Add(-time.Second)
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("status", string(StatusRunning)).Error; err != nil {
			t.Fatalf("mark cancellation-race task running: %v", err)
		}
		if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
			"status": model.TaskRunStatusRunning, "backup_generation_state": model.TaskRunGenerationStateWriting,
			"started_at": &started, "execution_owner_id": "old-owner", "execution_lease_until": &expired,
		}).Error; err != nil {
			t.Fatalf("mark cancellation-race run: %v", err)
		}
		ready := make(chan struct{})
		release := make(chan struct{})
		holderErr := make(chan error, 1)
		go func() {
			holderErr <- db.Transaction(func(tx *gorm.DB) error {
				if err := gormrepo.LockTaskIDsForUpdate(tx, []uint{taskEntity.ID}); err != nil {
					return err
				}
				var locked model.TaskRun
				if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
					Where("id = ? AND task_id = ?", run.ID, taskEntity.ID).First(&locked).Error; err != nil {
					return err
				}
				if err := tx.Model(&model.TaskRun{}).Where("id = ? AND status = ?", run.ID, model.TaskRunStatusRunning).
					Updates(map[string]any{"status": model.TaskRunStatusCanceled, "execution_owner_id": "", "execution_lease_until": nil}).Error; err != nil {
					return err
				}
				close(ready)
				<-release
				return nil
			})
		}()
		<-ready
		reconcileErr := make(chan error, 1)
		go func() {
			reconcileErr <- ReconcileLegacyRcloneWrite(context.Background(), db, LegacyRcloneReconcileRequest{
				TaskID: taskEntity.ID, TaskRunID: run.ID, RemoteStopped: true,
				Reason: "cancellation race", Actor: legacyRcloneReconcileActor(),
			})
		}()
		close(release)
		if err := <-holderErr; err != nil {
			t.Fatalf("cancellation holder transaction: %v", err)
		}
		if err := <-reconcileErr; err != nil {
			t.Fatalf("reconciliation after cancellation winner: %v", err)
		}
		var after model.TaskRun
		if err := db.First(&after, run.ID).Error; err != nil {
			t.Fatalf("reload run after cancellation race: %v", err)
		}
		if after.Status != model.TaskRunStatusCanceled || after.BackupGenerationState != model.TaskRunGenerationStateDirty ||
			after.ExecutionOwnerID != "" || after.ExecutionLeaseUntil != nil {
			t.Fatalf("cancellation race final run=%+v", after)
		}
	})
}
