package task

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const followupCronSpec = "@every 1h"

func migrateFollowupPostgresSupport(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(
		&model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{},
		&model.AlertDelivery{}, &model.Integration{}, &model.RestoreDrillEvidence{},
		&model.CredentialAuditEvent{},
	); err != nil {
		t.Fatalf("migrate lifecycle followup support tables: %v", err)
	}
}

func encodeFollowupRetryEffect(t *testing.T, taskID uint, chainRunID string, predecessorID uint, mode model.TaskRunCronCursorMode) string {
	t.Helper()
	payload := retryTaskRunEffect{
		TaskID:           taskID,
		ChainRunID:       chainRunID,
		PredecessorRunID: predecessorID,
	}
	if mode != model.TaskRunCronCursorModeLegacy {
		payload.CronCursorMode = string(mode)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode retry effect: %v", err)
	}
	return string(encoded)
}

func seedFollowupRetryTask(
	t *testing.T,
	db *gorm.DB,
	mode model.TaskRunCronCursorMode,
	effectStatus string,
) (model.Task, model.TaskRun, time.Time, time.Time) {
	t.Helper()
	taskEntity := seedTaskForManagerTest(t, db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	regularCursor := now.Add(2 * time.Hour)
	retryDeadline := now.Add(-time.Minute)
	nextRunAt := regularCursor
	if mode == model.TaskRunCronCursorModeLegacy {
		nextRunAt = retryDeadline
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      string(StatusRetrying),
		"enabled":     true,
		"cron_spec":   followupCronSpec,
		"next_run_at": nextRunAt,
		"retry_count": 0,
	}).Error; err != nil {
		t.Fatalf("prepare retrying cron task: %v", err)
	}
	source := model.TaskRun{
		TaskID:         taskEntity.ID,
		NodeIDSnapshot: taskEntity.NodeID,
		TriggerType:    "cron",
		Status:         model.TaskRunStatusFailed,
		ChainRunID:     "followup-retry-chain",
		LastError:      "FOLLOWUP_RETRY_SOURCE_FAILURE_FOR_TEST_ONLY",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create retry predecessor: %v", err)
	}
	modePayload := encodeFollowupRetryEffect(t, taskEntity.ID, source.ChainRunID, source.ID, mode)
	effect := model.TaskRunEffect{
		TaskRunID:  source.ID,
		EffectKey:  "retry",
		EffectType: model.TaskRunEffectTypeRetry,
		Payload:    modePayload,
		Status:     effectStatus,
		NextAttemptAt: func() *time.Time {
			if mode == model.TaskRunCronCursorModeLegacy {
				return nil
			}
			return &retryDeadline
		}(),
	}
	if err := db.Create(&effect).Error; err != nil {
		t.Fatalf("create retry predecessor effect: %v", err)
	}
	return taskEntity, source, regularCursor, retryDeadline
}

func waitFollowupPendingOwner(t *testing.T, db *gorm.DB, manager *Manager, taskID uint) model.TaskRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var run model.TaskRun
		if err := db.Where("task_id = ? AND trigger_type = ?", taskID, "retry").Order("id DESC").First(&run).Error; err == nil {
			if run.Status == model.TaskRunStatusPending && run.ExecutionOwnerID == manager.executionOwnerID {
				if _, owned := manager.pendingRuns.Load(taskID); owned {
					return run
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("retry run did not reach the locally owned pending barrier")
	return model.TaskRun{}
}

func runRejectedRetryRunPreservesExactProvenance(t *testing.T, db *gorm.DB) {
	t.Helper()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	taskEntity := seedTaskForManagerTest(t, db)
	before := time.Now().UTC()
	cursor := before.Add(2 * time.Hour).Truncate(time.Microsecond)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      string(StatusRetrying),
		"enabled":     true,
		"cron_spec":   followupCronSpec,
		"next_run_at": cursor,
		"retry_count": 0,
	}).Error; err != nil {
		t.Fatalf("prepare rejected retry task: %v", err)
	}
	source := model.TaskRun{
		TaskID:         taskEntity.ID,
		NodeIDSnapshot: taskEntity.NodeID,
		TriggerType:    "cron",
		Status:         model.TaskRunStatusFailed,
		ChainRunID:     "exact-followup-chain",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create exact retry predecessor: %v", err)
	}
	markedPayload := encodeFollowupRetryEffect(t, taskEntity.ID, source.ChainRunID, source.ID, model.TaskRunCronCursorModeRegularV1)
	if err := db.Create(&model.TaskRunEffect{
		TaskRunID: source.ID, EffectKey: "retry", EffectType: model.TaskRunEffectTypeRetry,
		Payload: markedPayload, Status: model.TaskRunEffectStatusSucceeded,
	}).Error; err != nil {
		t.Fatalf("create marked predecessor effect: %v", err)
	}
	// A newer effect from another chain must not be selected as this retry's
	// provenance merely because it is the newest effect for the Task.
	distractor := model.TaskRun{
		TaskID:         taskEntity.ID,
		NodeIDSnapshot: taskEntity.NodeID,
		TriggerType:    "manual",
		Status:         model.TaskRunStatusFailed,
		ChainRunID:     "unrelated-followup-chain",
	}
	if err := db.Create(&distractor).Error; err != nil {
		t.Fatalf("create unrelated retry predecessor: %v", err)
	}
	legacyPayload := encodeFollowupRetryEffect(t, taskEntity.ID, distractor.ChainRunID, distractor.ID, model.TaskRunCronCursorModeLegacy)
	if err := db.Create(&model.TaskRunEffect{
		TaskRunID: distractor.ID, EffectKey: "retry", EffectType: model.TaskRunEffectTypeRetry,
		Payload: legacyPayload, Status: model.TaskRunEffectStatusSucceeded,
	}).Error; err != nil {
		t.Fatalf("create unrelated legacy effect: %v", err)
	}
	retryRun := model.TaskRun{
		TaskID:         taskEntity.ID,
		NodeIDSnapshot: model.TaskRunNodeIDLegacyUnknown,
		TriggerType:    "retry",
		Status:         model.TaskRunStatusPending,
		ChainRunID:     source.ChainRunID,
	}
	if err := db.Create(&retryRun).Error; err != nil {
		t.Fatalf("create malformed retry run: %v", err)
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", retryRun.ID).
		UpdateColumn("node_id_snapshot", model.TaskRunNodeIDLegacyUnknown).Error; err != nil {
		t.Fatalf("install malformed retry node snapshot: %v", err)
	}

	// Enter through the real runner boundary: the immutable node snapshot is
	// rejected before any executor call and the durable retry effect is rebuilt.
	manager.runTaskWithContext(taskEntity.ID, retryRun.ID, "retry", retryRun.ChainRunID, context.Background(), nil, func() {})

	var storedRun model.TaskRun
	if err := db.First(&storedRun, retryRun.ID).Error; err != nil {
		t.Fatalf("reload rejected retry run: %v", err)
	}
	if storedRun.Status != model.TaskRunStatusFailed {
		t.Fatalf("rejected retry run status=%q, want failed", storedRun.Status)
	}
	var rebuilt model.TaskRunEffect
	if err := db.Where("task_run_id = ? AND effect_type = ?", retryRun.ID, model.TaskRunEffectTypeRetry).First(&rebuilt).Error; err != nil {
		t.Fatalf("load rebuilt retry effect: %v", err)
	}
	mode, err := model.ParseTaskRunEffectCronCursorMode(rebuilt.Payload)
	if err != nil || mode != model.TaskRunCronCursorModeRegularV1 {
		t.Fatalf("rebuilt retry mode=%q err=%v payload=%s", mode, err, rebuilt.Payload)
	}
	if rebuilt.NextAttemptAt == nil || !rebuilt.NextAttemptAt.After(before.Add(20*time.Second)) {
		t.Fatalf("rebuilt retry deadline=%v, want real backoff after rejection", rebuilt.NextAttemptAt)
	}
	if rebuilt.NextAttemptAt.Equal(cursor) {
		t.Fatalf("rebuilt retry reused regular cron cursor as retry deadline: %v", rebuilt.NextAttemptAt)
	}
	var storedTask model.Task
	if err := db.First(&storedTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload rejected retry task: %v", err)
	}
	if storedTask.Status != string(StatusRetrying) || storedTask.NextRunAt == nil || !storedTask.NextRunAt.Equal(cursor) {
		t.Fatalf("rejected retry aggregate=%+v, want retrying with unchanged cursor", storedTask)
	}
}

func TestRejectedRetryRunPreservesExactProvenanceSQLite(t *testing.T) {
	runRejectedRetryRunPreservesExactProvenance(t, openManagerTestDB(t))
}

func TestRejectedRetryRunPreservesExactProvenancePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runRejectedRetryRunPreservesExactProvenance(t, db)
}

func runRetryEffectUnknownModeFailsClosed(t *testing.T, db *gorm.DB) {
	t.Helper()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity, source, cursor, _ := seedFollowupRetryTask(t, db, model.TaskRunCronCursorModeRegularV1, model.TaskRunEffectStatusPending)
	payload := `{"task_id":` + string(mustJSONNumber(t, taskEntity.ID)) + `,"chain_run_id":"` + source.ChainRunID + `","predecessor_run_id":` + string(mustJSONNumber(t, source.ID)) + `,"cron_cursor_mode":"unknown_cursor_v9"}`
	if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND effect_type = ?", source.ID, model.TaskRunEffectTypeRetry).
		Update("payload", payload).Error; err != nil {
		t.Fatalf("install unknown retry cursor mode: %v", err)
	}
	if err := manager.drainReadyTaskRunEffects(context.Background()); err == nil {
		t.Fatal("unknown retry cursor mode unexpectedly drained")
	}
	var effect model.TaskRunEffect
	if err := db.Where("task_run_id = ? AND effect_type = ?", source.ID, model.TaskRunEffectTypeRetry).First(&effect).Error; err != nil {
		t.Fatalf("reload unknown retry effect: %v", err)
	}
	if effect.Status != model.TaskRunEffectStatusFailed {
		t.Fatalf("unknown retry effect status=%q, want failed", effect.Status)
	}
	var storedTask model.Task
	if err := db.First(&storedTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload unknown-mode task: %v", err)
	}
	if storedTask.NextRunAt == nil || !storedTask.NextRunAt.Equal(cursor) || storedTask.Status != string(StatusRetrying) {
		t.Fatalf("unknown retry mode mutated task=%+v", storedTask)
	}
	var retryRuns int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "retry").Count(&retryRuns).Error; err != nil {
		t.Fatalf("count unknown-mode retry runs: %v", err)
	}
	if retryRuns != 0 {
		t.Fatalf("unknown retry mode created %d retry runs", retryRuns)
	}
}

func mustJSONNumber(t *testing.T, value uint) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode numeric retry payload field: %v", err)
	}
	return encoded
}

func TestRetryEffectUnknownModeFailsClosedSQLite(t *testing.T) {
	runRetryEffectUnknownModeFailsClosed(t, openManagerTestDB(t))
}

func TestRetryEffectUnknownModeFailsClosedPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runRetryEffectUnknownModeFailsClosed(t, db)
}

func runGracefulShutdownPreservesPendingRetry(t *testing.T, db *gorm.DB, mode model.TaskRunCronCursorMode) {
	t.Helper()
	oldExecutor := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: oldExecutor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity, _, regularCursor, _ := seedFollowupRetryTask(t, db, mode, model.TaskRunEffectStatusPending)
	for i := 0; i < cap(manager.semaphore); i++ {
		manager.semaphore <- struct{}{}
	}
	if err := manager.drainReadyTaskRunEffects(context.Background()); err != nil {
		t.Fatalf("drain ready retry effect before shutdown: %v", err)
	}
	pending := waitFollowupPendingOwner(t, db, manager, taskEntity.ID)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("graceful shutdown: %v", err)
	}
	var released model.TaskRun
	if err := db.First(&released, pending.ID).Error; err != nil {
		t.Fatalf("reload shutdown-released retry run: %v", err)
	}
	if released.Status != model.TaskRunStatusPending || released.ExecutionOwnerID != "" || released.ExecutionLeaseUntil != nil {
		t.Fatalf("shutdown terminalized pending retry: %+v", released)
	}
	var waitingTask model.Task
	if err := db.First(&waitingTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload shutdown retry task: %v", err)
	}
	if waitingTask.Status != string(StatusRetrying) {
		t.Fatalf("shutdown changed retry aggregate status=%q", waitingTask.Status)
	}
	if mode == model.TaskRunCronCursorModeRegularV1 {
		if waitingTask.NextRunAt == nil || !waitingTask.NextRunAt.Equal(regularCursor) {
			t.Fatalf("shutdown changed marked regular cursor=%v, want %v", waitingTask.NextRunAt, regularCursor)
		}
	}
	if oldExecutor.Calls() != 0 {
		t.Fatalf("shutdown invoked old executor %d times", oldExecutor.Calls())
	}

	newExecutor := &successExecutor{}
	restarted := NewManager(db, stubExecutorFactory{executor: newExecutor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, restarted)
	if err := restarted.LoadSchedules(context.Background()); err != nil {
		t.Fatalf("restart pending retry reconciliation: %v", err)
	}
	recovered := waitTaskRunTerminal(t, db, pending.ID)
	restarted.taskWG.Wait()
	if recovered.Status != model.TaskRunStatusSuccess {
		t.Fatalf("recovered retry status=%q error=%q", recovered.Status, recovered.LastError)
	}
	if newExecutor.Calls() != 1 {
		t.Fatalf("recovered retry executor calls=%d, want exactly one", newExecutor.Calls())
	}
	var retryRuns int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "retry").Count(&retryRuns).Error; err != nil {
		t.Fatalf("count recovered retry runs: %v", err)
	}
	if retryRuns != 1 {
		t.Fatalf("recovery created %d retry runs, want one", retryRuns)
	}
	var finalTask model.Task
	if err := db.First(&finalTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload recovered task: %v", err)
	}
	if finalTask.Status != string(StatusSuccess) {
		t.Fatalf("recovered task status=%q, want success", finalTask.Status)
	}
	if mode == model.TaskRunCronCursorModeRegularV1 {
		if finalTask.NextRunAt == nil || !finalTask.NextRunAt.Equal(regularCursor) {
			t.Fatalf("marked retry success changed regular cursor=%v, want %v", finalTask.NextRunAt, regularCursor)
		}
	} else if finalTask.NextRunAt == nil || !finalTask.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("legacy retry success did not re-anchor regular cursor=%v", finalTask.NextRunAt)
	}
}

func TestGracefulShutdownPreservesMarkedPendingRetrySQLite(t *testing.T) {
	runGracefulShutdownPreservesPendingRetry(t, openManagerTestDB(t), model.TaskRunCronCursorModeRegularV1)
}

func TestGracefulShutdownPreservesMarkedPendingRetryPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runGracefulShutdownPreservesPendingRetry(t, db, model.TaskRunCronCursorModeRegularV1)
}

func TestGracefulShutdownPreservesLegacyPendingRetrySQLite(t *testing.T) {
	runGracefulShutdownPreservesPendingRetry(t, openManagerTestDB(t), model.TaskRunCronCursorModeLegacy)
}

func TestGracefulShutdownPreservesLegacyPendingRetryPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runGracefulShutdownPreservesPendingRetry(t, db, model.TaskRunCronCursorModeLegacy)
}

func runExplicitCancelPendingRetry(t *testing.T, db *gorm.DB, mode model.TaskRunCronCursorMode) {
	t.Helper()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity, source, regularCursor, retryDeadline := seedFollowupRetryTask(t, db, mode, model.TaskRunEffectStatusSucceeded)
	pending := model.TaskRun{
		TaskID:         taskEntity.ID,
		NodeIDSnapshot: taskEntity.NodeID,
		TriggerType:    "retry",
		Status:         model.TaskRunStatusPending,
		ChainRunID:     source.ChainRunID,
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create explicit-cancel retry run: %v", err)
	}
	for i := 0; i < cap(manager.semaphore); i++ {
		manager.semaphore <- struct{}{}
	}
	if err := manager.reconcilePendingDurableRuns(context.Background()); err != nil {
		t.Fatalf("launch explicit-cancel retry run: %v", err)
	}
	waitFollowupPendingOwner(t, db, manager, taskEntity.ID)
	if err := manager.Cancel(taskEntity.ID); err != nil {
		t.Fatalf("explicit cancel pending retry: %v", err)
	}
	var canceledRun model.TaskRun
	if err := db.First(&canceledRun, pending.ID).Error; err != nil {
		t.Fatalf("reload explicitly canceled retry: %v", err)
	}
	if canceledRun.Status != model.TaskRunStatusCanceled || canceledRun.ExecutionOwnerID != "" || canceledRun.ExecutionLeaseUntil != nil {
		t.Fatalf("explicit cancel left retry run=%+v", canceledRun)
	}
	var canceledTask model.Task
	if err := db.First(&canceledTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload explicitly canceled task: %v", err)
	}
	if canceledTask.Status != string(StatusCanceled) {
		t.Fatalf("explicit cancel task status=%q", canceledTask.Status)
	}
	if mode == model.TaskRunCronCursorModeRegularV1 {
		if canceledTask.NextRunAt == nil || !canceledTask.NextRunAt.Equal(regularCursor) {
			t.Fatalf("explicit cancel changed marked cursor=%v, want %v", canceledTask.NextRunAt, regularCursor)
		}
	} else {
		if canceledTask.NextRunAt == nil || canceledTask.NextRunAt.Equal(retryDeadline) || !canceledTask.NextRunAt.After(time.Now().UTC()) {
			t.Fatalf("explicit cancel did not re-anchor legacy retry cursor=%v", canceledTask.NextRunAt)
		}
	}
}

func TestExplicitCancelMarkedPendingRetrySQLite(t *testing.T) {
	runExplicitCancelPendingRetry(t, openManagerTestDB(t), model.TaskRunCronCursorModeRegularV1)
}

func TestExplicitCancelMarkedPendingRetryPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runExplicitCancelPendingRetry(t, db, model.TaskRunCronCursorModeRegularV1)
}

func TestExplicitCancelLegacyPendingRetrySQLite(t *testing.T) {
	runExplicitCancelPendingRetry(t, openManagerTestDB(t), model.TaskRunCronCursorModeLegacy)
}

func TestExplicitCancelLegacyPendingRetryPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runExplicitCancelPendingRetry(t, db, model.TaskRunCronCursorModeLegacy)
}

func runExplicitCancelWinsShutdownRace(t *testing.T, db *gorm.DB, mode model.TaskRunCronCursorMode) {
	t.Helper()
	executor := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: executor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity, source, regularCursor, retryDeadline := seedFollowupRetryTask(t, db, mode, model.TaskRunEffectStatusSucceeded)
	pending := model.TaskRun{
		TaskID:         taskEntity.ID,
		NodeIDSnapshot: taskEntity.NodeID,
		TriggerType:    "retry",
		Status:         model.TaskRunStatusPending,
		ChainRunID:     source.ChainRunID,
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create shutdown-race retry run: %v", err)
	}
	for i := 0; i < cap(manager.semaphore); i++ {
		manager.semaphore <- struct{}{}
	}
	if err := manager.reconcilePendingDurableRuns(context.Background()); err != nil {
		t.Fatalf("launch shutdown-race retry run: %v", err)
	}
	waitFollowupPendingOwner(t, db, manager, taskEntity.ID)

	start := make(chan struct{})
	shutdownResult := make(chan error, 1)
	cancelResult := make(chan error, 1)
	go func() {
		<-start
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownResult <- manager.Shutdown(ctx)
	}()
	go func() {
		<-start
		cancelResult <- manager.Cancel(taskEntity.ID)
	}()
	close(start)

	select {
	case err := <-cancelResult:
		if err != nil {
			t.Fatalf("explicit Cancel won shutdown race with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("explicit Cancel did not finish during shutdown race")
	}
	select {
	case err := <-shutdownResult:
		if err != nil {
			t.Fatalf("graceful shutdown raced with explicit Cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("graceful shutdown did not finish after explicit Cancel")
	}

	var canceledRun model.TaskRun
	if err := db.First(&canceledRun, pending.ID).Error; err != nil {
		t.Fatalf("reload shutdown-race retry run: %v", err)
	}
	if canceledRun.Status != model.TaskRunStatusCanceled || canceledRun.ExecutionOwnerID != "" || canceledRun.ExecutionLeaseUntil != nil {
		t.Fatalf("shutdown-race retry run=%+v, want explicit cancellation", canceledRun)
	}
	var canceledTask model.Task
	if err := db.First(&canceledTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload shutdown-race task: %v", err)
	}
	if canceledTask.Status != string(StatusCanceled) {
		t.Fatalf("shutdown-race task status=%q, want canceled", canceledTask.Status)
	}
	if mode == model.TaskRunCronCursorModeRegularV1 {
		if canceledTask.NextRunAt == nil || !canceledTask.NextRunAt.Equal(regularCursor) {
			t.Fatalf("shutdown-race changed marked cursor=%v, want %v", canceledTask.NextRunAt, regularCursor)
		}
	} else if canceledTask.NextRunAt == nil || canceledTask.NextRunAt.Equal(retryDeadline) ||
		!canceledTask.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("shutdown-race did not re-anchor legacy retry cursor=%v", canceledTask.NextRunAt)
	}
	if executor.Calls() != 0 {
		t.Fatalf("shutdown-race entered executor %d time(s)", executor.Calls())
	}
}

func TestExplicitCancelWinsShutdownRaceMarkedSQLite(t *testing.T) {
	runExplicitCancelWinsShutdownRace(t, openManagerTestDB(t), model.TaskRunCronCursorModeRegularV1)
}

func TestExplicitCancelWinsShutdownRaceMarkedPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runExplicitCancelWinsShutdownRace(t, db, model.TaskRunCronCursorModeRegularV1)
}

func TestExplicitCancelWinsShutdownRaceLegacySQLite(t *testing.T) {
	runExplicitCancelWinsShutdownRace(t, openManagerTestDB(t), model.TaskRunCronCursorModeLegacy)
}

func TestExplicitCancelWinsShutdownRaceLegacyPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runExplicitCancelWinsShutdownRace(t, db, model.TaskRunCronCursorModeLegacy)
}

func runShutdownSealsPendingAdmission(t *testing.T, db *gorm.DB) {
	t.Helper()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	_, ownership, claimed := manager.claimPendingRunOwnership(1)
	if !claimed || ownership == nil {
		t.Fatal("pre-shutdown admission claim was not registered")
	}

	shutdownResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownResult <- manager.Shutdown(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		manager.admissionMu.Lock()
		sealed := manager.admissionClosed
		manager.admissionMu.Unlock()
		if sealed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Shutdown did not seal task admission")
		}
		time.Sleep(time.Millisecond)
	}

	if _, _, claimed := manager.claimPendingRunOwnership(2); claimed {
		t.Fatal("claim succeeded after Shutdown sealed task admission")
	}
	if manager.handoffPendingRunAdmission(ownership) {
		t.Fatal("pre-shutdown owner handed off after Shutdown sealed task admission")
	}
	manager.pendingRuns.CompareAndDelete(1, ownership)
	if manager.chainRunner != nil {
		manager.chainRunner.Delete(1)
	}

	select {
	case err := <-shutdownResult:
		if err != nil {
			t.Fatalf("shutdown after admission seal: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown remained blocked after the in-flight admission was released")
	}
}

func TestShutdownSealsPendingAdmissionSQLite(t *testing.T) {
	runShutdownSealsPendingAdmission(t, openManagerTestDB(t))
}

func TestShutdownSealsPendingAdmissionPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runShutdownSealsPendingAdmission(t, db)
}

func runRetryEntryCancelBoundary(t *testing.T, db *gorm.DB, mode model.TaskRunCronCursorMode) {
	t.Helper()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity, source, regularCursor, _ := seedFollowupRetryTask(t, db, mode, model.TaskRunEffectStatusSucceeded)

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("open retry-entry SQL database: %v", err)
	}
	commitEntered := make(chan struct{})
	commitRelease := make(chan struct{})
	commitPool := &taskEntryCommitBarrierPool{
		DB: sqlDB, committed: commitEntered, release: commitRelease,
	}
	db.ConnPool = commitPool
	db.Statement.ConnPool = commitPool
	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	manager.SetNodeWriteAdmission(&nodeWriteAdmissionFake{
		startEntered: startEntered,
		startRelease: startRelease,
	})
	t.Cleanup(func() { closeOnce(commitRelease) })

	runID, err := manager.triggerCore(taskEntity.ID, "retry", source.ChainRunID, nil, nil)
	if err != nil {
		t.Fatalf("trigger retry entry: %v", err)
	}
	select {
	case <-startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("retry runner did not reach entry admission")
	}
	commitPool.armed.Store(true)
	close(startRelease)
	select {
	case <-commitEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("retry entry did not commit")
	}

	cancelResult := make(chan error, 1)
	go func() { cancelResult <- manager.Cancel(taskEntity.ID) }()
	select {
	case err := <-cancelResult:
		if err != nil {
			t.Fatalf("cancel retry entry: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel retry entry did not finish")
	}
	close(commitRelease)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown retry-entry manager: %v", err)
	}

	var canceledRun model.TaskRun
	if err := db.First(&canceledRun, runID).Error; err != nil {
		t.Fatalf("reload canceled retry entry: %v", err)
	}
	if canceledRun.Status != model.TaskRunStatusCanceled ||
		canceledRun.ExecutionOwnerID != "" || canceledRun.ExecutionLeaseUntil != nil ||
		canceledRun.StartedAt != nil {
		t.Fatalf("retry entry run=%+v, want canceled pre-provider run", canceledRun)
	}
	var canceledTask model.Task
	if err := db.First(&canceledTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload canceled retry task: %v", err)
	}
	if canceledTask.Status == string(StatusRetrying) {
		t.Fatalf("retry entry cancellation stranded aggregate in retrying: %+v", canceledTask)
	}
	if mode == model.TaskRunCronCursorModeRegularV1 &&
		(canceledTask.NextRunAt == nil || !canceledTask.NextRunAt.Equal(regularCursor)) {
		t.Fatalf("marked retry entry changed regular cursor=%v, want %v", canceledTask.NextRunAt, regularCursor)
	}
	var failedEffects int64
	if err := db.Model(&model.TaskRunEffect{}).
		Where("task_run_id = ? AND status = ?", runID, model.TaskRunEffectStatusFailed).
		Count(&failedEffects).Error; err != nil {
		t.Fatalf("count retry-entry failure effects: %v", err)
	}
	if failedEffects != 0 {
		t.Fatalf("retry entry cancellation created %d failure effects", failedEffects)
	}

	restarted := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, restarted)
	if err := restarted.reconcilePendingDurableRuns(context.Background()); err != nil {
		t.Fatalf("reconcile after retry-entry cancellation: %v", err)
	}
	var pendingRetries int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND trigger_type = ? AND status = ?", taskEntity.ID, "retry", model.TaskRunStatusPending).
		Count(&pendingRetries).Error; err != nil {
		t.Fatalf("count stranded retry entries: %v", err)
	}
	if pendingRetries != 0 {
		t.Fatalf("restart left %d pending retry entries", pendingRetries)
	}
}

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func TestRetryEntryCancelBoundaryMarkedSQLite(t *testing.T) {
	runRetryEntryCancelBoundary(t, openConcurrentManagerTestDB(t), model.TaskRunCronCursorModeRegularV1)
}

func TestRetryEntryCancelBoundaryLegacySQLite(t *testing.T) {
	runRetryEntryCancelBoundary(t, openConcurrentManagerTestDB(t), model.TaskRunCronCursorModeLegacy)
}

func TestRetryEntryCancelBoundaryMarkedPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runRetryEntryCancelBoundary(t, db, model.TaskRunCronCursorModeRegularV1)
}

func TestRetryEntryCancelBoundaryLegacyPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runRetryEntryCancelBoundary(t, db, model.TaskRunCronCursorModeLegacy)
}
