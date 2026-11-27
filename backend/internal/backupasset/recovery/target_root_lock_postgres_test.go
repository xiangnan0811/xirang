package recovery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The runtime implementation lives in a package that imports recovery. This
// adapter takes the same Node then SSHKey row locks and returns stable test
// revisions; registration and revocation use the real settings registry.
type targetRootPostgresLockingRevisions struct{ started chan<- int }

func (source targetRootPostgresLockingRevisions) ResolveRecoveryNodeRevisionsTx(
	ctx context.Context, tx *gorm.DB, nodeID uint, purpose TargetPurpose,
) (RecoveryNodeRevisionSnapshot, error) {
	if purpose != TargetPurposePreflight && purpose != TargetPurposeReconcile {
		return RecoveryNodeRevisionSnapshot{}, ErrRecoveryTargetUnavailable
	}
	if source.started != nil {
		var pid int
		if err := tx.WithContext(ctx).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
			return RecoveryNodeRevisionSnapshot{}, err
		}
		source.started <- pid
	}
	var node model.Node
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", nodeID).Limit(1).Find(&node)
	if loaded.Error != nil {
		return RecoveryNodeRevisionSnapshot{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || node.ID != nodeID || node.Archived || node.AuthType != "key" || node.SSHKeyID == nil {
		return RecoveryNodeRevisionSnapshot{}, ErrRecoveryTargetUnavailable
	}
	var key model.SSHKey
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", *node.SSHKeyID).Limit(1).Find(&key)
	if loaded.Error != nil {
		return RecoveryNodeRevisionSnapshot{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || key.ID != *node.SSHKeyID {
		return RecoveryNodeRevisionSnapshot{}, ErrRecoveryTargetUnavailable
	}
	return RecoveryNodeRevisionSnapshot{NodeRevision: "node-v1", CredentialRevision: "credential-v1"}, nil
}

type targetRootPostgresMutationContextKey struct{}

type targetRootPostgresReadResult struct {
	snapshot RecoveryEligibilityTargetRootSnapshot
	expected recoveryReconciliationExpectedSnapshot
	err      error
}

func TestRecoveryTargetRootReadersPostgresConcurrentRevocation(t *testing.T) {
	for _, name := range []string{"capture", "revalidate", "reconciliation", "plan_enumeration_capture", "old_order_control"} {
		t.Run(name, func(t *testing.T) {
			db := newAuthorizationReceiptPostgresScopedDB(t)
			fixture := newTargetRootAuthorityServiceFixtureOnDB(t, db)
			request := fixture.request("/srv/recovery-root", "recovery root")
			registered, err := fixture.service.Register(context.Background(), request)
			if err != nil {
				t.Fatalf("register target root: %v", err)
			}
			revisions := targetRootPostgresLockingRevisions{}
			port, err := NewRecoveryEligibilityTargetRootAuthority(RecoveryEligibilityTargetRootAuthorityDependencies{
				Registry: fixture.registry, Revisions: revisions,
			})
			if err != nil {
				t.Fatal(err)
			}
			binding := RecoveryAuthorityBinding{
				TargetNodeID: request.NodeID, TargetRootID: request.RootID,
				RootLocatorDigest: registered.LocatorDigest, PreflightNodeRevision: "node-v1", CredentialScopeRevision: "credential-v1",
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			var operations sync.WaitGroup
			defer func() {
				cancel()
				operations.Wait()
			}()
			var captured RecoveryEligibilityTargetRootSnapshot
			if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var captureErr error
				captured, captureErr = port.CaptureRecoveryEligibilityTargetRootTx(ctx, tx, binding)
				return captureErr
			}); err != nil || captured.AuthorityRevision != registered.AuthorityRevision {
				t.Fatalf("capture registered authority: %v", err)
			}
			if name == "reconciliation" {
				if err := db.AutoMigrate(&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryJob{},
					&model.BackupAssetRecoveryResultSet{}, &model.BackupAssetRecoveryResult{}); err != nil {
					t.Fatal(err)
				}
				service := RecoveryReconciliationService{db: db, roots: fixture.registry, revisions: port}
				current, err := service.buildRecoveryReconciliationExpectedSnapshot(ctx, ReconcileRecoveryRootRequest{NodeID: request.NodeID, RootID: request.RootID})
				if err != nil || current.root.AuthorityRevision != registered.AuthorityRevision ||
					current.session.nodeRevision != "node-v1" || current.session.credentialRevision != "credential-v1" ||
					current.session.rootRevision != registered.AuthorityRevision || len(current.candidates) != 0 {
					t.Fatalf("build current reconciliation snapshot on PostgreSQL: %v", err)
				}
			}

			// Pause the real Delete after its Node SELECT FOR UPDATE has returned,
			// before it can acquire the settings row. Only this mutation's context
			// carries the marker; reader queries never enter the barrier.
			mutationStarted := make(chan int, 1)
			releaseMutation := make(chan struct{})
			const callback = "test:target_root_revoke_node_barrier"
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table != "nodes" || tx.Statement.Context.Value(targetRootPostgresMutationContextKey{}) == nil || tx.Error != nil {
					return
				}
				var pid int
				if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
					_ = tx.AddError(err)
					return
				}
				mutationStarted <- pid
				select {
				case <-releaseMutation:
				case <-tx.Statement.Context.Done():
					_ = tx.AddError(tx.Statement.Context.Err())
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
			mutationCtx := context.WithValue(ctx, targetRootPostgresMutationContextKey{}, true)
			mutationResult := make(chan error, 1)
			operations.Go(func() {
				mutationResult <- fixture.service.Delete(mutationCtx, request.NodeID, request.RootID)
			})
			var mutationPID int
			select {
			case mutationPID = <-mutationStarted:
			case err := <-mutationResult:
				t.Fatalf("revocation ended before Node barrier: %v", err)
			case <-ctx.Done():
				t.Fatalf("waiting for revocation Node barrier: %v", ctx.Err())
			}
			if mutationPID <= 0 {
				t.Fatal("revocation has no backend PID")
			}

			readerCtx, cancelReader := context.WithCancel(ctx)
			defer cancelReader()
			readerStarted := make(chan int, 1)
			readerResult := make(chan targetRootPostgresReadResult, 1)
			operations.Go(func() {
				result := targetRootPostgresReadResult{}
				if name == "reconciliation" {
					readerPort, err := NewRecoveryEligibilityTargetRootAuthority(RecoveryEligibilityTargetRootAuthorityDependencies{
						Registry: fixture.registry, Revisions: targetRootPostgresLockingRevisions{started: readerStarted},
					})
					if err != nil {
						result.err = err
					} else {
						service := RecoveryReconciliationService{db: db, roots: fixture.registry, revisions: readerPort}
						result.expected, result.err = service.buildRecoveryReconciliationExpectedSnapshot(readerCtx,
							ReconcileRecoveryRootRequest{NodeID: request.NodeID, RootID: request.RootID})
					}
					readerResult <- result
					return
				}
				result.err = db.WithContext(readerCtx).Transaction(func(tx *gorm.DB) error {
					var pid int
					if err := tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
						return err
					}
					readerStarted <- pid
					switch name {
					case "capture":
						result.snapshot, result.err = port.CaptureRecoveryEligibilityTargetRootTx(readerCtx, tx, binding)
					case "revalidate":
						result.err = port.RevalidateRecoveryEligibilityTargetRootTx(readerCtx, tx, binding, captured)
					case "plan_enumeration_capture":
						authority := newRecoveryPlanTargetEnumerationAuthorityForTest(recoveryPlanTargetEnumerationDependencies{DB: db, Roots: fixture.registry, Revisions: revisions})
						result.snapshot, result.err = authority.captureTargetRootTx(readerCtx, tx, RecoveryPlanTargetEnumerationRequest{TargetNodeID: request.NodeID, TargetRootID: request.RootID})
					case "old_order_control":
						if result.err = lockRecoveryEligibilityTargetRootRowTx(readerCtx, tx, request.NodeID, request.RootID); result.err == nil {
							_, result.err = fixture.registry.ResolveRecoveryTargetRootTx(readerCtx, tx, request.NodeID, request.RootID)
						}
						if result.err == nil {
							_, result.err = revisions.ResolveRecoveryNodeRevisionsTx(readerCtx, tx, request.NodeID, TargetPurposePreflight)
						}
					}
					return result.err
				})
				readerResult <- result
			})
			var readerPID int
			select {
			case readerPID = <-readerStarted:
			case result := <-readerResult:
				t.Fatalf("reader ended before backend PID: %v", result.err)
			case <-ctx.Done():
				t.Fatalf("waiting for reader PID: %v", ctx.Err())
			}
			waitTargetRootPostgresBlocked(t, ctx, db, readerPID, mutationPID, readerResult)

			// NOWAIT distinguishes the old order deterministically: its blocked
			// reader already owns the settings row, creating the deadlock edge.
			key := targetRootAuthorityRecordKey(request.NodeID, request.RootID)
			probeErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var row model.SystemSetting
				loaded := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: "NOWAIT"}).Where("key = ?", key).Take(&row)
				return loaded.Error
			})
			if name == "old_order_control" {
				var postgresErr *pgconn.PgError
				if !errors.As(probeErr, &postgresErr) || postgresErr.Code != "55P03" {
					t.Fatalf("old root-first reader did not hold the root while waiting for Node: %v", probeErr)
				}
				cancelReader()
				select {
				case result := <-readerResult:
					if result.err == nil {
						t.Fatal("canceled old-order reader unexpectedly succeeded")
					}
				case <-ctx.Done():
					t.Fatalf("waiting for canceled old-order reader: %v", ctx.Err())
				}
			} else if probeErr != nil {
				t.Fatalf("blocked reader held the root before Node: %v", probeErr)
			}
			close(releaseMutation)
			select {
			case err := <-mutationResult:
				if err != nil {
					t.Fatalf("revoke target root: %v", err)
				}
			case <-ctx.Done():
				t.Fatalf("waiting for revocation: %v", ctx.Err())
			}
			if name != "old_order_control" {
				select {
				case result := <-readerResult:
					want := ErrRecoveryTargetUnavailable
					if name == "revalidate" {
						want = ErrRecoveryTargetChanged
					}
					if name == "reconciliation" {
						want = errRecoveryReconciliationIncomplete
					}
					if !errors.Is(result.err, want) || result.snapshot != (RecoveryEligibilityTargetRootSnapshot{}) ||
						result.expected.root.NodeID != 0 || result.expected.session.nodeID != 0 || len(result.expected.candidates) != 0 {
						t.Fatalf("reader after revocation: error=%v, want %v and no partial result", result.err, want)
					}
				case <-ctx.Done():
					t.Fatalf("waiting for reader after revocation: %v", ctx.Err())
				}
			}
			if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				_, err := fixture.registry.ResolveRecoveryTargetRootTx(ctx, tx, request.NodeID, request.RootID)
				if !errors.Is(err, settings.ErrRecoveryTargetRootNotFound) {
					return fmt.Errorf("revoked registry resolution: %v", err)
				}
				var node model.Node
				if err := tx.First(&node, request.NodeID).Error; err != nil {
					return err
				}
				if node.SSHKeyID == nil {
					return errors.New("revocation changed node credential")
				}
				var key model.SSHKey
				return tx.First(&key, *node.SSHKeyID).Error
			}); err != nil {
				t.Fatalf("verify committed revocation and intact Node/SSH key: %v", err)
			}
		})
	}
}

func waitTargetRootPostgresBlocked(t *testing.T, ctx context.Context, db *gorm.DB, readerPID, mutationPID int, result <-chan targetRootPostgresReadResult) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case completed := <-result:
			t.Fatalf("reader completed before blocking: %v", completed.err)
		default:
		}
		var state struct {
			WaitEventType     string
			BlockedByMutation bool
		}
		loaded := db.WithContext(ctx).Raw(`SELECT COALESCE(wait_event_type, '') AS wait_event_type,
			COALESCE(? = ANY(pg_blocking_pids(pid)), false) AS blocked_by_mutation
			FROM pg_stat_activity WHERE pid = ?`, mutationPID, readerPID).Scan(&state)
		if loaded.Error != nil {
			t.Fatalf("inspect reader lock wait: %v", loaded.Error)
		}
		if loaded.RowsAffected == 1 && state.WaitEventType == "Lock" && state.BlockedByMutation {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("reader pid %d did not block on revocation pid %d: %v", readerPID, mutationPID, ctx.Err())
		}
	}
}

func TestRecoveryTargetRootRestoreMutationPostgres(t *testing.T) {
	t.Run("waits_for_root_before_receipt", func(t *testing.T) {
		db := newAuthorizationReceiptPostgresScopedDB(t)
		fixture := newTargetRootAuthorityServiceFixtureOnDB(t, db)
		initial := fixture.request("/srv/recovery-before-rotation", "original root")
		if _, err := fixture.service.Register(context.Background(), initial); err != nil {
			t.Fatalf("register original root: %v", err)
		}
		rootKey := targetRootAuthorityRecordKey(initial.NodeID, initial.RootID)
		var original model.SystemSetting
		if err := db.Where("key = ?", rootKey).Take(&original).Error; err != nil {
			t.Fatalf("load original root row: %v", err)
		}
		rotation := fixture.authorizedRequest(TargetRootMutationRotate, "compensation-rotation-key",
			"/srv/recovery-after-rotation", "rotated root")
		_, rollback, err := fixture.service.RegisterMutation(context.Background(), rotation)
		if err != nil {
			t.Fatalf("rotate root with receipt: %v", err)
		}
		if rollback.receiptKey == "" {
			t.Fatal("authorized rotation did not create a receipt")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		holder := db.WithContext(ctx).Begin()
		if holder.Error != nil {
			t.Fatalf("begin root holder transaction: %v", holder.Error)
		}
		defer func() { _ = holder.Rollback().Error }()
		var holderPID int
		if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil || holderPID <= 0 {
			t.Fatalf("load root holder PID: %v", err)
		}
		var lockedRoot model.SystemSetting
		if err := holder.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("key = ?", rootKey).Take(&lockedRoot).Error; err != nil {
			t.Fatalf("lock rotated root: %v", err)
		}
		if lockedRoot == original {
			t.Fatal("rotation did not change the original root row")
		}

		// Record the actual RestoreMutation backend before its first settings
		// query; this barrier does not interfere with the row lock schedule.
		type restoreContextKey struct{}
		started := make(chan int, 1)
		const callback = "test:target_root_restore_pid"
		if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table != "system_settings" || tx.Statement.Context.Value(restoreContextKey{}) == nil {
				return
			}
			select {
			case <-tx.Statement.Context.Done():
				return
			default:
			}
			var pid int
			if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				select {
				case started <- 0:
				default:
				}
				return
			}
			select {
			case started <- pid:
			default:
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
		result := make(chan targetRootPostgresReadResult, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			err := fixture.service.RestoreMutation(context.WithValue(ctx, restoreContextKey{}, true), rollback)
			result <- targetRootPostgresReadResult{err: err}
		}()
		defer func() {
			cancel()
			_ = holder.Rollback().Error
			<-done
		}()
		var restorePID int
		select {
		case restorePID = <-started:
		case completed := <-result:
			t.Fatalf("restore ended before settings query: %v", completed.err)
		case <-ctx.Done():
			t.Fatalf("waiting for restore PID: %v", ctx.Err())
		}
		if restorePID <= 0 {
			t.Fatal("restore has no backend PID")
		}
		waitTargetRootPostgresBlocked(t, ctx, db, restorePID, holderPID, result)
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var receipt model.SystemSetting
			return tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: "NOWAIT"}).
				Where("key = ?", rollback.receiptKey).Take(&receipt).Error
		}); err != nil {
			t.Fatalf("blocked restore already holds its receipt: %v", err)
		}
		if err := holder.Commit().Error; err != nil {
			t.Fatalf("release root holder: %v", err)
		}
		select {
		case completed := <-result:
			if completed.err != nil {
				t.Fatalf("restore rotation: %v", completed.err)
			}
		case <-ctx.Done():
			t.Fatalf("waiting for restore: %v", ctx.Err())
		}
		var restored model.SystemSetting
		if err := db.WithContext(ctx).Where("key = ?", rootKey).Take(&restored).Error; err != nil || restored != original {
			t.Fatalf("restore did not preserve exact original root row: %v", err)
		}
		var receiptCount int64
		if err := db.WithContext(ctx).Model(&model.SystemSetting{}).Where("key = ?", rollback.receiptKey).
			Count(&receiptCount).Error; err != nil || receiptCount != 0 {
			t.Fatalf("restore receipt count=%d error=%v, want absence", receiptCount, err)
		}
	})

	t.Run("stale_receipt_rolls_back_root", func(t *testing.T) {
		db := newAuthorizationReceiptPostgresScopedDB(t)
		fixture := newTargetRootAuthorityServiceFixtureOnDB(t, db)
		initial := fixture.request("/srv/recovery-before-stale-receipt", "original root")
		if _, err := fixture.service.Register(context.Background(), initial); err != nil {
			t.Fatalf("register original root: %v", err)
		}
		rotation := fixture.authorizedRequest(TargetRootMutationRotate, "compensation-stale-receipt-key",
			"/srv/recovery-after-stale-receipt", "rotated root")
		_, rollback, err := fixture.service.RegisterMutation(context.Background(), rotation)
		if err != nil || rollback.receiptKey == "" {
			t.Fatalf("rotate root with receipt: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		update := db.WithContext(ctx).Model(&model.SystemSetting{}).Where("key = ?", rollback.receiptKey).
			UpdateColumn("value", "changed receipt")
		if update.Error != nil || update.RowsAffected != 1 {
			t.Fatalf("make receipt stale: rows=%d error=%v", update.RowsAffected, update.Error)
		}
		rootKey := targetRootAuthorityRecordKey(initial.NodeID, initial.RootID)
		var rootBefore, receiptBefore model.SystemSetting
		if err := db.WithContext(ctx).Where("key = ?", rootKey).Take(&rootBefore).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.WithContext(ctx).Where("key = ?", rollback.receiptKey).Take(&receiptBefore).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.service.RestoreMutation(ctx, rollback); !errors.Is(err, ErrRecoveryTargetUnavailable) {
			t.Fatalf("restore with stale receipt error=%v, want unavailable", err)
		}
		var rootAfter, receiptAfter model.SystemSetting
		if err := db.WithContext(ctx).Where("key = ?", rootKey).Take(&rootAfter).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.WithContext(ctx).Where("key = ?", rollback.receiptKey).Take(&receiptAfter).Error; err != nil {
			t.Fatal(err)
		}
		if rootAfter != rootBefore || receiptAfter != receiptBefore {
			t.Fatal("failed restore changed the root or stale receipt")
		}
	})

	t.Run("deleted_node_restores_created_root_and_receipt_absence", func(t *testing.T) {
		db := newAuthorizationReceiptPostgresScopedDB(t)
		fixture := newTargetRootAuthorityServiceFixtureOnDB(t, db)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		request := fixture.authorizedRequest(TargetRootMutationRegister, "deleted-node-compensation-key",
			"/srv/recovery-deleted-node", "created root")
		_, rollback, err := fixture.service.RegisterMutation(ctx, request)
		if err != nil || rollback.key == "" || rollback.receiptKey == "" {
			t.Fatalf("register root with receipt: %v", err)
		}
		for _, key := range []string{rollback.key, rollback.receiptKey} {
			var count int64
			if err := db.WithContext(ctx).Model(&model.SystemSetting{}).Where("key = ?", key).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("created authority row count=%d error=%v, want 1", count, err)
			}
		}
		deleted := db.WithContext(ctx).Unscoped().Delete(&model.Node{}, request.NodeID)
		if deleted.Error != nil || deleted.RowsAffected != 1 {
			t.Fatalf("hard-delete target node: rows=%d error=%v", deleted.RowsAffected, deleted.Error)
		}
		if err := fixture.service.RestoreMutation(ctx, rollback); err != nil {
			t.Fatalf("restore created root after node deletion: %v", err)
		}
		for _, key := range []string{rollback.key, rollback.receiptKey} {
			var count int64
			if err := db.WithContext(ctx).Model(&model.SystemSetting{}).Where("key = ?", key).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("restored authority row count=%d error=%v, want absence", count, err)
			}
		}
	})
}
