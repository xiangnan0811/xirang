package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"xirang/backend/internal/automation"
	"xirang/backend/internal/model"
	taskexec "xirang/backend/internal/task/executor"
)

type retryRejectionBoundaryFixture struct {
	manager       *Manager
	task          model.Task
	source        model.TaskRun
	retry         model.TaskRun
	regularCursor time.Time
	oldDeadline   time.Time
}

func seedRetryRejectionBoundary(
	t *testing.T,
	db *gorm.DB,
	mode model.TaskRunCronCursorMode,
	maxRetries, retryCount int,
) retryRejectionBoundaryFixture {
	t.Helper()
	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.AutoMigrate(&model.AutomationRule{}, &model.AutomationRuleLog{}, &model.AlertDelivery{}); err != nil {
		t.Fatalf("migrate retry rejection effect support tables: %v", err)
	}
	policy := model.Policy{
		Name:             fmt.Sprintf("retry-rejection-boundary-policy-%d", time.Now().UnixNano()),
		MaxRetries:       maxRetries,
		RetryBaseSeconds: 1,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create retry rejection policy: %v", err)
	}
	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).
		Updates(map[string]interface{}{"max_retries": maxRetries, "retry_base_seconds": 1}).Error; err != nil {
		t.Fatalf("persist retry rejection policy: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	regularCursor := now.Add(2 * time.Hour)
	oldDeadline := now.Add(-time.Minute)
	nextRunAt := oldDeadline
	cronSpec := ""
	if mode == model.TaskRunCronCursorModeRegularV1 {
		cronSpec = followupCronSpec
		nextRunAt = regularCursor
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      string(StatusRetrying),
		"policy_id":   policy.ID,
		"cron_spec":   cronSpec,
		"next_run_at": nextRunAt,
		"retry_count": retryCount,
		"enabled":     true,
	}).Error; err != nil {
		t.Fatalf("prepare retry rejection task: %v", err)
	}
	var upstream *uint
	source := model.TaskRun{
		TaskID:            taskEntity.ID,
		NodeIDSnapshot:    taskEntity.NodeID,
		TriggerType:       "manual",
		Status:            model.TaskRunStatusFailed,
		ChainRunID:        "retry-rejection-boundary-chain",
		UpstreamTaskRunID: upstream,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create retry rejection predecessor: %v", err)
	}
	payload := retryTaskRunEffect{
		TaskID: taskEntity.ID, ChainRunID: source.ChainRunID, PredecessorRunID: source.ID,
	}
	if mode == model.TaskRunCronCursorModeRegularV1 {
		payload.CronCursorMode = string(mode)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode retry rejection predecessor: %v", err)
	}
	if err := db.Create(&model.TaskRunEffect{
		TaskRunID: source.ID, EffectKey: "retry", EffectType: model.TaskRunEffectTypeRetry,
		Payload: string(encoded), Status: model.TaskRunEffectStatusSucceeded,
	}).Error; err != nil {
		t.Fatalf("create retry rejection predecessor effect: %v", err)
	}
	retry := model.TaskRun{
		TaskID:         taskEntity.ID,
		NodeIDSnapshot: model.TaskRunNodeIDLegacyUnknown,
		TriggerType:    "retry",
		Status:         model.TaskRunStatusPending,
		ChainRunID:     source.ChainRunID,
	}
	if err := db.Create(&retry).Error; err != nil {
		t.Fatalf("create retry rejection run: %v", err)
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", retry.ID).
		UpdateColumn("node_id_snapshot", model.TaskRunNodeIDLegacyUnknown).Error; err != nil {
		t.Fatalf("install non-authoritative retry snapshot: %v", err)
	}
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	manager.autoDispatcher = automation.NewDispatcher(db)
	shutdownManagerOnCleanup(t, manager)
	return retryRejectionBoundaryFixture{
		manager: manager, task: taskEntity, source: source, retry: retry,
		regularCursor: regularCursor, oldDeadline: oldDeadline,
	}
}

func loadRetryRejectionBoundaryState(t *testing.T, db *gorm.DB, fixture retryRejectionBoundaryFixture) (model.Task, model.TaskRun, []model.TaskRunEffect) {
	t.Helper()
	var taskEntity model.Task
	if err := db.First(&taskEntity, fixture.task.ID).Error; err != nil {
		t.Fatalf("reload retry rejection task: %v", err)
	}
	var run model.TaskRun
	if err := db.First(&run, fixture.retry.ID).Error; err != nil {
		t.Fatalf("reload rejected retry run: %v", err)
	}
	var effects []model.TaskRunEffect
	if err := db.Where("task_run_id = ?", fixture.retry.ID).Order("id ASC").Find(&effects).Error; err != nil {
		t.Fatalf("reload rejected retry effects: %v", err)
	}
	return taskEntity, run, effects
}

func assertRetryRejectionScheduled(t *testing.T, db *gorm.DB, fixture retryRejectionBoundaryFixture, marked bool) {
	t.Helper()
	taskEntity, run, effects := loadRetryRejectionBoundaryState(t, db, fixture)
	if run.Status != model.TaskRunStatusFailed {
		t.Fatalf("rejected retry status=%q, want failed", run.Status)
	}
	if len(effects) != 1 || effects[0].EffectType != model.TaskRunEffectTypeRetry {
		t.Fatalf("rejected retry effects=%+v, want one retry effect", effects)
	}
	if effects[0].NextAttemptAt == nil || !effects[0].NextAttemptAt.After(time.Now().UTC()) {
		t.Fatalf("retry deadline=%v, want future backoff", effects[0].NextAttemptAt)
	}
	if taskEntity.Status != string(StatusRetrying) || taskEntity.RetryCount != 1 {
		t.Fatalf("retry aggregate=%+v, want retrying/count=1", taskEntity)
	}
	if marked {
		if taskEntity.NextRunAt == nil || !taskEntity.NextRunAt.Equal(fixture.regularCursor) {
			t.Fatalf("marked retry changed regular cursor=%v, want %v", taskEntity.NextRunAt, fixture.regularCursor)
		}
		mode, err := model.ParseTaskRunEffectCronCursorMode(effects[0].Payload)
		if err != nil || mode != model.TaskRunCronCursorModeRegularV1 {
			t.Fatalf("marked retry effect mode=%q err=%v payload=%s", mode, err, effects[0].Payload)
		}
	} else if taskEntity.NextRunAt == nil || !taskEntity.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("legacy retry did not persist replacement deadline=%v", taskEntity.NextRunAt)
	}
}

func runRetryRejectionSchedulesNextAttempt(t *testing.T, db *gorm.DB, mode model.TaskRunCronCursorMode) {
	t.Helper()
	fixture := seedRetryRejectionBoundary(t, db, mode, 2, 0)
	if err := fixture.manager.failTaskRunBeforeExecutor(context.Background(), fixture.retry.ID, "RETRY_REJECTION_BOUNDARY_FAILURE"); err != nil {
		t.Fatalf("reject retry before executor: %v", err)
	}
	assertRetryRejectionScheduled(t, db, fixture, mode == model.TaskRunCronCursorModeRegularV1)
}

func TestRetryRejectionSchedulesNextAttemptMarkedSQLite(t *testing.T) {
	runRetryRejectionSchedulesNextAttempt(t, openManagerTestDB(t), model.TaskRunCronCursorModeRegularV1)
}

func TestRetryRejectionSchedulesNextAttemptLegacySQLite(t *testing.T) {
	runRetryRejectionSchedulesNextAttempt(t, openManagerTestDB(t), model.TaskRunCronCursorModeLegacy)
}

func TestRetryRejectionSchedulesNextAttemptMarkedPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runRetryRejectionSchedulesNextAttempt(t, db, model.TaskRunCronCursorModeRegularV1)
}

func TestRetryRejectionSchedulesNextAttemptLegacyPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runRetryRejectionSchedulesNextAttempt(t, db, model.TaskRunCronCursorModeLegacy)
}

func runRetryRejectionExhausted(t *testing.T, db *gorm.DB, maxRetries, retryCount int, mode model.TaskRunCronCursorMode) {
	t.Helper()
	fixture := seedRetryRejectionBoundary(t, db, mode, maxRetries, retryCount)
	if err := fixture.manager.failTaskRunBeforeExecutor(context.Background(), fixture.retry.ID, "RETRY_REJECTION_EXHAUSTED_FAILURE"); err != nil {
		t.Fatalf("reject exhausted retry before executor: %v", err)
	}
	taskEntity, run, effects := loadRetryRejectionBoundaryState(t, db, fixture)
	if run.Status != model.TaskRunStatusFailed || taskEntity.Status != string(StatusFailed) {
		t.Fatalf("exhausted retry states task=%q run=%q", taskEntity.Status, run.Status)
	}
	if taskEntity.RetryCount != retryCount {
		t.Fatalf("exhausted retry count=%d, want %d", taskEntity.RetryCount, retryCount)
	}
	for _, effect := range effects {
		if effect.EffectType == model.TaskRunEffectTypeRetry {
			t.Fatalf("exhausted retry published retry effect=%+v", effect)
		}
	}
}

func TestRetryRejectionMaxRetriesZeroDoesNotRetrySQLite(t *testing.T) {
	runRetryRejectionExhausted(t, openManagerTestDB(t), 0, 0, model.TaskRunCronCursorModeLegacy)
}

func TestRetryRejectionAtLimitDoesNotRetrySQLite(t *testing.T) {
	runRetryRejectionExhausted(t, openManagerTestDB(t), 2, 2, model.TaskRunCronCursorModeLegacy)
}

func TestRetryRejectionMaxRetriesZeroDoesNotRetryPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runRetryRejectionExhausted(t, db, 0, 0, model.TaskRunCronCursorModeLegacy)
}

func TestRetryRejectionAtLimitDoesNotRetryPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runRetryRejectionExhausted(t, db, 2, 2, model.TaskRunCronCursorModeLegacy)
}

func runRetryRejectionRepeatedPreEntry(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture := seedRetryRejectionBoundary(t, db, model.TaskRunCronCursorModeLegacy, 2, 0)
	for wantCount := 1; wantCount <= 2; wantCount++ {
		if err := fixture.manager.failTaskRunBeforeExecutor(context.Background(), fixture.retry.ID, "RETRY_REJECTION_REPEATED_FAILURE"); err != nil {
			t.Fatalf("reject retry attempt %d before executor: %v", wantCount, err)
		}
		var taskEntity model.Task
		if err := db.First(&taskEntity, fixture.task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if taskEntity.Status != string(StatusRetrying) || taskEntity.RetryCount != wantCount {
			t.Fatalf("after rejection %d task=%+v", wantCount, taskEntity)
		}
		next := model.TaskRun{
			TaskID: fixture.task.ID, NodeIDSnapshot: model.TaskRunNodeIDLegacyUnknown,
			TriggerType: "retry", Status: model.TaskRunStatusPending,
			ChainRunID: fixture.source.ChainRunID,
		}
		if err := db.Create(&next).Error; err != nil {
			t.Fatal(err)
		}
		fixture.retry = next
	}
	if err := fixture.manager.failTaskRunBeforeExecutor(context.Background(), fixture.retry.ID, "RETRY_REJECTION_REPEATED_FAILURE"); err != nil {
		t.Fatalf("reject exhausted repeated retry: %v", err)
	}
	var taskEntity model.Task
	if err := db.First(&taskEntity, fixture.task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if taskEntity.Status != string(StatusFailed) || taskEntity.RetryCount != 2 {
		t.Fatalf("repeated rejection did not settle task=%+v", taskEntity)
	}
	var retryEffects int64
	if err := db.Model(&model.TaskRunEffect{}).
		Where("task_run_id = ? AND effect_type = ?", fixture.retry.ID, model.TaskRunEffectTypeRetry).
		Count(&retryEffects).Error; err != nil {
		t.Fatal(err)
	}
	if retryEffects != 0 {
		t.Fatalf("exhausted repeated rejection published %d retry effects", retryEffects)
	}
}

func TestRetryRejectionRepeatedPreEntryIncrementsSQLite(t *testing.T) {
	runRetryRejectionRepeatedPreEntry(t, openManagerTestDB(t))
}

func TestRetryRejectionRepeatedPreEntryIncrementsPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runRetryRejectionRepeatedPreEntry(t, db)
}

func TestRetryRejectionRealRunnerBoundarySQLite(t *testing.T) {
	db := openManagerTestDB(t)
	executor := &countingRetryRejectionExecutor{}
	fixture := seedRetryRejectionBoundary(t, db, model.TaskRunCronCursorModeRegularV1, 2, 0)
	fixture.manager.executorFactory = stubExecutorFactory{executor: executor}
	var initialRun model.TaskRun
	if err := db.First(&initialRun, fixture.retry.ID).Error; err != nil {
		t.Fatal(err)
	}
	if initialRun.NodeIDSnapshot != model.TaskRunNodeIDLegacyUnknown {
		t.Fatalf("fixture retry snapshot=%d, want non-authoritative zero", initialRun.NodeIDSnapshot)
	}
	fixture.manager.runTaskWithContext(fixture.task.ID, fixture.retry.ID, "retry", fixture.retry.ChainRunID, context.Background(), nil, func() {})
	if got := atomic.LoadInt32(&executor.calls); got != 0 {
		t.Fatalf("rejected retry reached executor %d time(s)", got)
	}
	var run model.TaskRun
	if err := db.First(&run, fixture.retry.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.TaskRunStatusFailed {
		t.Fatalf("real runner rejection status=%q, want failed", run.Status)
	}
	var taskEntity model.Task
	if err := db.First(&taskEntity, fixture.task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if taskEntity.Status != string(StatusRetrying) || taskEntity.RetryCount != 1 {
		t.Fatalf("real runner rejection task=%+v", taskEntity)
	}
}

type countingRetryRejectionExecutor struct {
	calls int32
}

func (e *countingRetryRejectionExecutor) Run(
	context.Context,
	model.Task,
	taskexec.LogFunc,
	taskexec.ProgressFunc,
) (int, error) {
	atomic.AddInt32(&e.calls, 1)
	return 0, nil
}

func runRetryEffectUnknownCursorExhaustion(t *testing.T, db *gorm.DB) {
	t.Helper()
	manager := bareEffectManager(db, "retry-unknown-cursor-boundary-owner")
	taskEntity, source, regularCursor, _ := seedFollowupRetryTask(t, db, model.TaskRunCronCursorModeRegularV1, model.TaskRunEffectStatusPending)
	unknownPayload := fmt.Sprintf(`{"task_id":%d,"chain_run_id":%q,"predecessor_run_id":%d,"cron_cursor_mode":"unknown_cursor_v9"}`,
		taskEntity.ID, source.ChainRunID, source.ID)
	var effect model.TaskRunEffect
	if err := db.Where("task_run_id = ? AND effect_type = ?", source.ID, model.TaskRunEffectTypeRetry).First(&effect).Error; err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if err := db.Model(&effect).Updates(map[string]interface{}{
		"payload": unknownPayload, "attempts": taskRunEffectMaxAttempts - 1,
		"next_attempt_at": past, "status": model.TaskRunEffectStatusPending,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.drainReadyTaskRunEffects(context.Background()); err == nil {
		t.Fatal("unknown cursor exhaustion should report provenance failure")
	}
	var settled model.Task
	if err := db.First(&settled, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if settled.Status != string(StatusFailed) || settled.NextRunAt != nil {
		t.Fatalf("unknown cursor exhaustion task=%+v, want failed with explicit rebuild boundary", settled)
	}
	if settled.NextRunAt != nil && settled.NextRunAt.Equal(regularCursor) {
		t.Fatalf("unknown cursor fabricated regular cursor=%v", settled.NextRunAt)
	}
	var current model.TaskRunEffect
	if err := db.First(&current, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != model.TaskRunEffectStatusFailed || current.Attempts != taskRunEffectMaxAttempts || current.NextAttemptAt != nil {
		t.Fatalf("unknown cursor exhausted effect=%+v", current)
	}
	if err := manager.drainReadyTaskRunEffects(context.Background()); err != nil {
		t.Fatalf("second unknown cursor drain duplicated failure: %v", err)
	}
	var retryRuns int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "retry").Count(&retryRuns).Error; err != nil {
		t.Fatal(err)
	}
	if retryRuns != 0 {
		t.Fatalf("unknown cursor exhaustion created %d retry runs", retryRuns)
	}
}

func TestRetryEffectUnknownCursorExhaustionSQLite(t *testing.T) {
	runRetryEffectUnknownCursorExhaustion(t, openManagerTestDB(t))
}

func TestRetryEffectUnknownCursorExhaustionPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	migrateFollowupPostgresSupport(t, db)
	runRetryEffectUnknownCursorExhaustion(t, db)
}
