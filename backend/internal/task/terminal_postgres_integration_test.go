package task

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const requirePostgresTaskTerminalTestEnv = "REQUIRE_POSTGRES_TASK_TERMINAL_TEST"

// TestTaskTerminalAtomicityPostgres keeps the cross-engine contract visible: a
// terminal transaction rolls back both rows on an injected TaskRun failure,
// and concurrent terminal writers leave one coherent pair without a stale
// execution lease. The isolated schema keeps this test safe for a shared CI
// PostgreSQL instance.
func TestTaskTerminalAtomicityPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv(requirePostgresTaskTerminalTestEnv)) == "1" {
			t.Fatalf("TEST_POSTGRES_DSN is required when %s=1", requirePostgresTaskTerminalTestEnv)
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)

	node := model.Node{Name: "task-terminal-pg-node", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	taskEntity := model.Task{
		Name: "task-terminal-pg", NodeID: node.ID, ExecutorType: "rsync", Status: string(StatusRunning),
		RsyncSource: "/tmp/src", RsyncTarget: "/tmp/dst",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	leaseUntil := time.Now().UTC().Add(time.Hour)
	run := model.TaskRun{
		TaskID: taskEntity.ID, NodeIDSnapshot: node.ID, TriggerType: "manual", Status: model.TaskRunStatusRunning,
		ExecutionOwnerID: "owner-a", ExecutionLeaseUntil: &leaseUntil,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create task run: %v", err)
	}

	manager := &Manager{db: db, stateMachine: NewStateMachine(), executionOwnerID: "owner-a", executionLeaseDuration: time.Minute}
	callbackName := fmt.Sprintf("test:postgres-terminal-rollback-%d", run.ID)
	var injected atomic.Bool
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "task_runs" && injected.CompareAndSwap(false, true) {
			_ = tx.AddError(errors.New("INTERNAL_POSTGRES_TASK_RUN_TERMINAL_INJECTION"))
		}
	}); err != nil {
		t.Fatalf("register rollback injection: %v", err)
	}
	rollbackErr := manager.terminalizeTaskRun(
		context.Background(), taskEntity.ID, run.ID, []string{model.TaskRunStatusRunning},
		taskTerminalStatusPtr(StatusSuccess), map[string]interface{}{"last_error": "should roll back"},
		StatusSuccess, map[string]interface{}{"last_error": "should roll back"},
	)
	if rollbackErr == nil || !injected.Load() {
		t.Fatalf("terminal transaction injection error=%v injected=%v", rollbackErr, injected.Load())
	}
	if err := db.Callback().Update().Remove(callbackName); err != nil {
		t.Fatalf("remove rollback injection: %v", err)
	}

	var rolledBackTask model.Task
	var rolledBackRun model.TaskRun
	if err := db.First(&rolledBackTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload rolled-back task: %v", err)
	}
	if err := db.First(&rolledBackRun, run.ID).Error; err != nil {
		t.Fatalf("reload rolled-back run: %v", err)
	}
	if rolledBackTask.Status != string(StatusRunning) || rolledBackTask.LastError != "" {
		t.Fatalf("Task changed despite TaskRun rollback: status=%q last_error=%q", rolledBackTask.Status, rolledBackTask.LastError)
	}
	if rolledBackRun.Status != model.TaskRunStatusRunning || rolledBackRun.LastError != "" || rolledBackRun.ExecutionOwnerID != "owner-a" || rolledBackRun.ExecutionLeaseUntil == nil {
		t.Fatalf("TaskRun changed despite rollback: %+v", rolledBackRun)
	}
	var effects int64
	if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id = ?", run.ID).Count(&effects).Error; err != nil {
		t.Fatalf("count effects after rollback: %v", err)
	}
	if effects != 0 {
		t.Fatalf("rollback left %d durable effects", effects)
	}

	managerB := &Manager{db: db, stateMachine: NewStateMachine(), executionOwnerID: "owner-a", executionLeaseDuration: time.Minute}
	start := make(chan struct{})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		errCh <- manager.terminalizeTaskRun(
			context.Background(), taskEntity.ID, run.ID, []string{model.TaskRunStatusRunning},
			taskTerminalStatusPtr(StatusSuccess), map[string]interface{}{"last_error": "success winner"},
			StatusSuccess, map[string]interface{}{"last_error": "success winner"},
		)
	}()
	go func() {
		defer wg.Done()
		<-start
		errCh <- managerB.terminalizeTaskRun(
			context.Background(), taskEntity.ID, run.ID, []string{model.TaskRunStatusRunning},
			taskTerminalStatusPtr(StatusFailed), map[string]interface{}{"last_error": "failed winner"},
			StatusFailed, map[string]interface{}{"last_error": "failed winner"},
		)
	}()
	close(start)
	wg.Wait()
	firstErr, secondErr := <-errCh, <-errCh
	if (firstErr == nil) == (secondErr == nil) {
		t.Fatalf("concurrent terminal writers yielded errors %v and %v, want exactly one winner", firstErr, secondErr)
	}

	var finalTask model.Task
	var finalRun model.TaskRun
	if err := db.First(&finalTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload final task: %v", err)
	}
	if err := db.First(&finalRun, run.ID).Error; err != nil {
		t.Fatalf("reload final run: %v", err)
	}
	if finalRun.Status != model.TaskRunStatusSuccess && finalRun.Status != model.TaskRunStatusFailed {
		t.Fatalf("final TaskRun status=%q, want success or failed", finalRun.Status)
	}
	if finalTask.Status != finalRun.Status {
		t.Fatalf("split terminal pair: Task=%q TaskRun=%q", finalTask.Status, finalRun.Status)
	}
	if finalRun.ExecutionOwnerID != "" || finalRun.ExecutionLeaseUntil != nil {
		t.Fatalf("terminal TaskRun retained stale owner/lease: owner=%q lease=%v", finalRun.ExecutionOwnerID, finalRun.ExecutionLeaseUntil)
	}
	if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id = ?", run.ID).Count(&effects).Error; err != nil {
		t.Fatalf("count final effects: %v", err)
	}
	if effects != 1 {
		t.Fatalf("final terminal pair has %d effects, want one idempotent alert effect", effects)
	}
	var effect model.TaskRunEffect
	if err := db.Where("task_run_id = ?", run.ID).First(&effect).Error; err != nil {
		t.Fatalf("load final effect: %v", err)
	}
	if effect.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("final effect status=%q, want succeeded", effect.Status)
	}
}

// TestTaskTerminalRejectCancelLockOrderPostgres forces cancellation to hold
// the Task row while rejection reaches its first row lock. The rejection
// transaction must request Task first; the old TaskRun -> Task order forms a
// PostgreSQL deadlock with cancellation's Task -> TaskRun order.
func TestTaskTerminalRejectCancelLockOrderPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)

	node := model.Node{Name: "task-terminal-lock-order-node", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create lock-order node: %v", err)
	}
	taskEntity := model.Task{
		Name: "task-terminal-lock-order", NodeID: node.ID, ExecutorType: "rsync", Status: string(StatusRunning),
		RsyncSource: "/tmp/src", RsyncTarget: "/tmp/dst",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create lock-order task: %v", err)
	}
	leaseUntil := time.Now().UTC().Add(time.Hour)
	run := model.TaskRun{
		TaskID: taskEntity.ID, NodeIDSnapshot: node.ID, TriggerType: "manual", Status: model.TaskRunStatusRunning,
		ExecutionOwnerID: "lock-order-owner", ExecutionLeaseUntil: &leaseUntil,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create lock-order task run: %v", err)
	}

	rejectionManager := &Manager{
		db: db, stateMachine: NewStateMachine(), executionOwnerID: "lock-order-owner",
		executionLeaseDuration: time.Minute,
	}
	cancellationManager := &Manager{
		db: db, stateMachine: NewStateMachine(), executionOwnerID: "lock-order-owner",
		executionLeaseDuration: time.Minute,
	}
	ownership := &pendingRunOwnership{}
	cancellationManager.pendingRuns.Store(taskEntity.ID, ownership)
	t.Cleanup(func() { cancellationManager.pendingRuns.CompareAndDelete(taskEntity.ID, ownership) })

	isLockingQuery := func(tx *gorm.DB, table string) bool {
		if tx == nil || tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != table {
			return false
		}
		locking, ok := tx.Statement.Clauses["FOR"]
		if !ok {
			return false
		}
		switch locking.Expression.(type) {
		case clause.Locking, *clause.Locking:
			return true
		default:
			return false
		}
	}

	cancellationTaskLocked := make(chan struct{})
	releaseCancellationTask := make(chan struct{})
	var cancellationTaskOnce sync.Once
	var releaseCancellationOnce sync.Once
	releaseCancellation := func() {
		releaseCancellationOnce.Do(func() { close(releaseCancellationTask) })
	}
	t.Cleanup(releaseCancellation)

	rejectionStarted := atomic.Bool{}
	rejectionLockAttempted := make(chan string, 1)
	rejectionRunLocked := make(chan struct{})
	var rejectionLockOnce sync.Once
	var rejectionRunOnce sync.Once

	afterCallbackName := fmt.Sprintf("test:task-terminal-lock-order-after-%d", run.ID)
	if err := db.Callback().Query().After("gorm:query").Register(afterCallbackName, func(tx *gorm.DB) {
		if isLockingQuery(tx, "tasks") {
			cancellationTaskOnce.Do(func() {
				close(cancellationTaskLocked)
				<-releaseCancellationTask
			})
		}
		if rejectionStarted.Load() && isLockingQuery(tx, "task_runs") {
			rejectionRunOnce.Do(func() { close(rejectionRunLocked) })
		}
	}); err != nil {
		t.Fatalf("register lock-order query barrier: %v", err)
	}
	t.Cleanup(func() {
		releaseCancellation()
		_ = db.Callback().Query().Remove(afterCallbackName)
	})

	beforeCallbackName := fmt.Sprintf("test:task-terminal-lock-order-before-%d", run.ID)
	if err := db.Callback().Query().Before("gorm:query").Register(beforeCallbackName, func(tx *gorm.DB) {
		if !rejectionStarted.Load() || (!isLockingQuery(tx, "tasks") && !isLockingQuery(tx, "task_runs")) {
			return
		}
		rejectionLockOnce.Do(func() {
			// Bound the intentionally inverted pre-fix interleaving so the
			// test reports the lock-order failure without waiting for the
			// server's default deadlock detector interval.
			_ = tx.Exec("SET LOCAL lock_timeout = '1s'").Error
			rejectionLockAttempted <- tx.Statement.Schema.Table
		})
	}); err != nil {
		t.Fatalf("register lock-order query observer: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(beforeCallbackName) })

	type cancellationResult struct {
		canceled bool
		err      error
	}
	cancellationDone := make(chan cancellationResult, 1)
	go func() {
		canceled, err := cancellationManager.cancelOwnedRunningTask(taskEntity.ID, "任务已取消")
		cancellationDone <- cancellationResult{canceled: canceled, err: err}
	}()
	select {
	case <-cancellationTaskLocked:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not reach Task lock barrier")
	}

	rejectionStarted.Store(true)
	rejectionDone := make(chan error, 1)
	go func() {
		rejectionDone <- rejectionManager.failTaskRunBeforeExecutor(context.Background(), run.ID, "入口拒绝")
	}()

	var firstLock string
	select {
	case firstLock = <-rejectionLockAttempted:
	case <-time.After(3 * time.Second):
		t.Fatal("rejection did not reach its first row-lock barrier")
	}
	if firstLock == "task_runs" {
		select {
		case <-rejectionRunLocked:
		case <-time.After(3 * time.Second):
			t.Fatal("rejection did not acquire its TaskRun lock barrier")
		}
	}
	releaseCancellation()

	var canceled cancellationResult
	select {
	case canceled = <-cancellationDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation remained blocked after releasing Task barrier")
	}
	var rejectionErr error
	select {
	case rejectionErr = <-rejectionDone:
	case <-time.After(5 * time.Second):
		t.Fatal("rejection remained blocked after releasing Task barrier")
	}
	if canceled.err != nil || !canceled.canceled {
		t.Fatalf("cancellation result=%+v, want successful cancellation", canceled)
	}
	if rejectionErr != nil {
		t.Fatalf("rejection after cancellation: %v", rejectionErr)
	}

	var finalTask model.Task
	var finalRun model.TaskRun
	if err := db.First(&finalTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload lock-order task: %v", err)
	}
	if err := db.First(&finalRun, run.ID).Error; err != nil {
		t.Fatalf("reload lock-order run: %v", err)
	}
	if finalTask.Status != string(StatusCanceled) || finalRun.Status != model.TaskRunStatusCanceled {
		t.Fatalf("cancel/rejection split terminal pair: Task=%q TaskRun=%q", finalTask.Status, finalRun.Status)
	}
	if finalRun.ExecutionOwnerID != "" || finalRun.ExecutionLeaseUntil != nil {
		t.Fatalf("canceled TaskRun retained owner/lease: owner=%q lease=%v", finalRun.ExecutionOwnerID, finalRun.ExecutionLeaseUntil)
	}
}

func openTaskTerminalPostgresDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL base connection: %v", err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL base connection: %v", err)
	}
	schema := fmt.Sprintf("xirang_task_terminal_%d", time.Now().UTC().UnixNano())
	if _, err := baseSQL.Exec("CREATE SCHEMA " + schema); err != nil {
		_ = baseSQL.Close()
		t.Fatalf("create isolated PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := baseSQL.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Errorf("drop isolated PostgreSQL schema: %v", err)
		}
		_ = baseSQL.Close()
	})
	scoped := *parsed
	query := scoped.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	scoped.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(scoped.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open isolated PostgreSQL connection: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get isolated PostgreSQL connection: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{},
		&model.TaskCronOccurrence{}, &model.TaskRunEffect{}, &model.BackupCompletion{},
	); err != nil {
		t.Fatalf("migrate isolated PostgreSQL task tables: %v", err)
	}
	return db
}

func taskTerminalStatusPtr(status TaskStatus) *TaskStatus {
	return &status
}
