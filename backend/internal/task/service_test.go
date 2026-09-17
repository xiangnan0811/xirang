package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	gormrepo "xirang/backend/internal/repository/gorm"
	taskscheduler "xirang/backend/internal/task/scheduler"

	"gorm.io/gorm"
)

func taskUpdateInputForTest(current model.Task, desired CreateTaskInput) UpdateTaskInput {
	name := desired.Name
	nodeID := desired.NodeID
	command := desired.Command
	source := desired.RsyncSource
	target := desired.RsyncTarget
	executorType := desired.ExecutorType
	cronSpec := desired.CronSpec
	return UpdateTaskInput{
		ExpectedRevision:   TaskRevision(current),
		Name:               &name,
		NodeID:             &nodeID,
		PolicyID:           desired.PolicyID,
		PolicyIDSet:        true,
		DependsOnTaskID:    desired.DependsOnTaskID,
		DependsOnTaskIDSet: true,
		Command:            &command,
		RsyncSource:        &source,
		RsyncTarget:        &target,
		ExecutorType:       &executorType,
		CronSpec:           &cronSpec,
	}
}

func TestTaskUpdatePresenceRevisionAndCronClear(t *testing.T) {
	db := openManagerTestDB(t)
	t.Setenv("RSYNC_ALLOWED_SOURCE_PREFIXES", "/data")
	t.Setenv("RSYNC_ALLOWED_TARGET_PREFIXES", "/backup")

	node := model.Node{
		Name: "task-presence-node", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthType: "key", BackupDir: "task-presence-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	taskEntity := model.Task{
		Name: "task-presence-old", NodeID: node.ID, ExecutorType: "restic",
		RsyncSource: "/data/src", RsyncTarget: "/backup/repo",
		ExecutorConfig: `{"repository_password":"FAKE_TASK_PRESENCE_PASSWORD_FOR_TEST_ONLY","exclude_patterns":["cache"],"repository_version":2}`,
		CronSpec:       "@every 1h", Status: string(StatusPending), Enabled: true,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	repo := gormrepo.NewTaskRepository(db)
	api := NewTaskApiService(repo, gormrepo.NewNodeRepository(db), gormrepo.NewPolicyRepository(db), nil)

	current, err := repo.FindByID(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	renamed := "task-presence-new"
	updated, err := api.UpdateTask(context.Background(), taskEntity.ID, UpdateTaskInput{
		ExpectedRevision: TaskRevision(*current),
		Name:             &renamed,
	})
	if err != nil {
		t.Fatalf("presence-only update: %v", err)
	}
	settings, err := ProjectExecutorSettings(updated.ExecutorType, updated.ExecutorConfig)
	if err != nil {
		t.Fatalf("project settings after presence-only update: %v", err)
	}
	resticSettings, ok := settings.(ResticExecutorSettingsResponse)
	if !ok || len(resticSettings.ExcludePatterns) != 1 ||
		resticSettings.ExcludePatterns[0] != "cache" || resticSettings.RepositoryVersion == nil ||
		*resticSettings.RepositoryVersion != 2 {
		t.Fatalf("presence-only update changed safe settings: %#v", settings)
	}

	current, err = repo.FindByID(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatalf("reload task after rename: %v", err)
	}
	emptyPatterns := ExecutorSettingsPatch{ExcludePatternsSet: true, ExcludePatterns: []string{}}
	updated, err = api.UpdateTask(context.Background(), taskEntity.ID, UpdateTaskInput{
		ExpectedRevision: TaskRevision(*current),
		ExecutorSettings: &emptyPatterns,
	})
	if err != nil {
		t.Fatalf("explicit empty exclude update: %v", err)
	}
	settings, err = ProjectExecutorSettings(updated.ExecutorType, updated.ExecutorConfig)
	if err != nil {
		t.Fatalf("project settings after empty exclude update: %v", err)
	}
	resticSettings, ok = settings.(ResticExecutorSettingsResponse)
	if !ok || len(resticSettings.ExcludePatterns) != 0 {
		t.Fatalf("explicit empty exclude did not clear settings: %#v", settings)
	}

	current, err = repo.FindByID(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatalf("reload task before cron clear: %v", err)
	}
	emptyCron := ""
	updated, err = api.UpdateTask(context.Background(), taskEntity.ID, UpdateTaskInput{
		ExpectedRevision: TaskRevision(*current),
		CronSpec:         &emptyCron,
	})
	if err != nil {
		t.Fatalf("explicit empty cron update: %v", err)
	}
	if updated.CronSpec != "" || updated.NextRunAt != nil || !updated.CronOverride {
		t.Fatalf("explicit empty cron did not clear cursor or mark override: cron=%q next=%v override=%v",
			updated.CronSpec, updated.NextRunAt, updated.CronOverride)
	}

	staleName := "stale-write"
	_, err = api.UpdateTask(context.Background(), taskEntity.ID, UpdateTaskInput{
		ExpectedRevision: TaskRevision(*current),
		Name:             &staleName,
	})
	if !errors.Is(err, ErrTaskRevisionConflict) {
		t.Fatalf("stale update error=%v, want ErrTaskRevisionConflict", err)
	}
}

type taskUpdateScheduleFailureRunner struct {
	entered  chan<- struct{}
	release  <-chan struct{}
	syncErr  error
	removed  []uint
	syncCall []model.Task
}

func (r *taskUpdateScheduleFailureRunner) TriggerManual(uint) (uint, error) {
	return 0, nil
}

func (r *taskUpdateScheduleFailureRunner) SyncSchedule(task model.Task) error {
	r.syncCall = append(r.syncCall, task)
	close(r.entered)
	<-r.release
	return r.syncErr
}

func (r *taskUpdateScheduleFailureRunner) RemoveSchedule(taskID uint) {
	r.removed = append(r.removed, taskID)
}

type taskUpdateScheduleCursorRunner struct {
	db *gorm.DB
}

func (r *taskUpdateScheduleCursorRunner) TriggerManual(uint) (uint, error) {
	return 0, nil
}

func (r *taskUpdateScheduleCursorRunner) SyncSchedule(task model.Task) error {
	next := time.Now().UTC().Add(time.Hour)
	return r.db.Model(&model.Task{}).Where("id = ?", task.ID).Update("next_run_at", next).Error
}

func (r *taskUpdateScheduleCursorRunner) RemoveSchedule(uint) {}

func TestTaskUpdateReturnsPostScheduleRevision(t *testing.T) {
	db := openManagerTestDB(t)
	node := model.Node{
		Name: "task-update-revision-node", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthType: "key", BackupDir: "task-update-revision-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	taskEntity := model.Task{
		Name: "task-update-revision-old", NodeID: node.ID, ExecutorType: "command",
		Command: "true", CronSpec: "@every 1h", Status: string(StatusPending), Enabled: true,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	repo := gormrepo.NewTaskRepository(db)
	api := NewTaskApiService(
		repo,
		gormrepo.NewNodeRepository(db),
		gormrepo.NewPolicyRepository(db),
		&taskUpdateScheduleCursorRunner{db: db},
	)
	current, err := repo.FindByID(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	renamed := "task-update-revision-new"
	updated, err := api.UpdateTask(context.Background(), taskEntity.ID, UpdateTaskInput{
		ExpectedRevision: TaskRevision(*current),
		Name:             &renamed,
	})
	if err != nil {
		t.Fatalf("update task: %v", err)
	}
	var persisted model.Task
	if err := db.First(&persisted, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload persisted task: %v", err)
	}
	if updated.UpdatedAt.UnixNano() != persisted.UpdatedAt.UnixNano() ||
		updated.NextRunAt == nil || persisted.NextRunAt == nil ||
		!updated.NextRunAt.Equal(*persisted.NextRunAt) {
		t.Fatalf("update response is not post-sync state: response=%+v persisted=%+v", updated, persisted)
	}

	nextName := "task-update-revision-final"
	if _, err := api.UpdateTask(context.Background(), taskEntity.ID, UpdateTaskInput{
		ExpectedRevision: TaskRevision(updated),
		Name:             &nextName,
	}); err != nil {
		t.Fatalf("post-sync revision rejected next update: %v", err)
	}
}

type taskUpdateConcurrentMutation struct {
	name   string
	mutate func(*gorm.DB, uint) error
	check  func(model.Task) error
}

func TestTaskUpdateScheduleFailurePreservesConcurrentStateSQLite(t *testing.T) {
	exerciseTaskUpdateScheduleFailurePreservesConcurrentState(t, openManagerTestDB(t))
}

func TestTaskUpdateScheduleFailurePreservesConcurrentStatePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	exerciseTaskUpdateScheduleFailurePreservesConcurrentState(t, openTaskTerminalPostgresDB(t, dsn))
}

func exerciseTaskUpdateScheduleFailurePreservesConcurrentState(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Setenv("RSYNC_ALLOWED_SOURCE_PREFIXES", "/data")
	t.Setenv("RSYNC_ALLOWED_TARGET_PREFIXES", "/backup")
	if err := db.AutoMigrate(&model.TaskRepositoryLink{}); err != nil {
		t.Fatalf("migrate task repository links: %v", err)
	}

	node := model.Node{
		Name:      "task-update-node",
		Host:      "127.0.0.1",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BackupDir: "task-update-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create task update node: %v", err)
	}

	mutations := []taskUpdateConcurrentMutation{
		{
			name: "pause",
			mutate: func(db *gorm.DB, taskID uint) error {
				return db.Model(&model.Task{}).Where("id = ?", taskID).Updates(map[string]any{
					"enabled":     false,
					"skip_next":   false,
					"next_run_at": nil,
				}).Error
			},
			check: func(task model.Task) error {
				if task.Name != "task-edit-a" || task.RsyncSource != "/data/a" || task.RsyncTarget != "/backup/a" ||
					task.CronSpec != "*/10 * * * *" || task.Enabled {
					return fmt.Errorf("pause state was overwritten: %+v", task)
				}
				return nil
			},
		},
		{
			name: "new edit",
			mutate: func(db *gorm.DB, taskID uint) error {
				peer := NewTaskApiService(
					gormrepo.NewTaskRepository(db),
					gormrepo.NewNodeRepository(db),
					gormrepo.NewPolicyRepository(db),
					nil,
				)
				current, err := gormrepo.NewTaskRepository(db).FindByID(context.Background(), taskID)
				if err != nil {
					return err
				}
				_, err = peer.UpdateTask(context.Background(), taskID, taskUpdateInputForTest(*current, CreateTaskInput{
					Name: "task-edit-b", NodeID: node.ID, ExecutorType: "rsync",
					RsyncSource: "/data/b", RsyncTarget: "/backup/b", CronSpec: "*/15 * * * *",
				}))
				return err
			},
			check: func(task model.Task) error {
				if task.Name != "task-edit-b" || task.RsyncSource != "/data/b" || task.RsyncTarget != "/backup/b" ||
					task.CronSpec != "*/15 * * * *" || !task.Enabled {
					return fmt.Errorf("new edit state was overwritten: %+v", task)
				}
				return nil
			},
		},
		{
			name: "terminal result",
			mutate: func(db *gorm.DB, taskID uint) error {
				return db.Model(&model.Task{}).Where("id = ?", taskID).Updates(map[string]any{
					"status":      string(StatusFailed),
					"last_error":  "new terminal diagnostic",
					"retry_count": 7,
					"next_run_at": nil,
					"skip_next":   false,
				}).Error
			},
			check: func(task model.Task) error {
				if task.Name != "task-edit-a" || task.RsyncSource != "/data/a" || task.RsyncTarget != "/backup/a" ||
					task.CronSpec != "*/10 * * * *" || task.Status != string(StatusFailed) ||
					task.LastError != "new terminal diagnostic" || task.RetryCount != 7 {
					return fmt.Errorf("terminal result was overwritten: %+v", task)
				}
				return nil
			},
		},
		{
			name: "archive",
			mutate: func(db *gorm.DB, taskID uint) error {
				archiver := NewArchiveService(ArchiveDependencies{
					DB:  db,
					Now: func() time.Time { return time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC) },
				})
				_, err := archiver.Archive(context.Background(), taskID)
				return err
			},
			check: func(task model.Task) error {
				if task.Name != "task-edit-a" || task.RsyncSource != "/data/a" || task.RsyncTarget != "/backup/a" ||
					task.CronSpec != "*/10 * * * *" || task.Enabled || task.ArchivedAt == nil {
					return fmt.Errorf("archive state was overwritten: %+v", task)
				}
				return nil
			},
		},
	}

	for _, mutation := range mutations {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			taskEntity := model.Task{
				Name: "task-old", NodeID: node.ID, RsyncSource: "/data/old", RsyncTarget: "/backup/old",
				ExecutorType: "rsync", CronSpec: "*/5 * * * *", Status: string(StatusPending), Enabled: true,
			}
			if err := db.Create(&taskEntity).Error; err != nil {
				t.Fatalf("create task: %v", err)
			}
			if err := db.First(&taskEntity, taskEntity.ID).Error; err != nil {
				t.Fatalf("reload task revision: %v", err)
			}
			t.Cleanup(func() {
				_ = db.Unscoped().Delete(&model.Task{}, taskEntity.ID).Error
			})

			entered := make(chan struct{})
			release := make(chan struct{})
			runner := &taskUpdateScheduleFailureRunner{
				entered: entered, release: release,
				syncErr: errors.New("FAKE_SCHEDULE_SYNC_FAILURE_FOR_TEST_ONLY"),
			}
			api := NewTaskApiService(
				gormrepo.NewTaskRepository(db),
				gormrepo.NewNodeRepository(db),
				gormrepo.NewPolicyRepository(db),
				runner,
			)

			errCh := make(chan error, 1)
			go func() {
				_, err := api.UpdateTask(context.Background(), taskEntity.ID, taskUpdateInputForTest(taskEntity, CreateTaskInput{
					Name: "task-edit-a", NodeID: node.ID, ExecutorType: "rsync",
					RsyncSource: "/data/a", RsyncTarget: "/backup/a", CronSpec: "*/10 * * * *",
				}))
				errCh <- err
			}()

			select {
			case <-entered:
			case err := <-errCh:
				t.Fatalf("UpdateTask returned before schedule sync: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("UpdateTask did not reach schedule sync")
			}
			if err := mutation.mutate(db, taskEntity.ID); err != nil {
				close(release)
				t.Fatalf("concurrent %s mutation: %v", mutation.name, err)
			}
			close(release)
			if err := <-errCh; !errors.Is(err, ErrTaskScheduleSyncUnavailable) {
				t.Fatalf("UpdateTask error=%v, want ErrTaskScheduleSyncUnavailable", err)
			}
			if len(runner.syncCall) != 1 {
				t.Fatalf("SyncSchedule calls=%d, want 1", len(runner.syncCall))
			}
			if len(runner.removed) != 0 {
				t.Fatalf("failed schedule sync removed another request's schedule: %+v", runner.removed)
			}

			var persisted model.Task
			if err := db.First(&persisted, taskEntity.ID).Error; err != nil {
				t.Fatalf("load task after failed schedule sync: %v", err)
			}
			if err := mutation.check(persisted); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTaskUpdateCronChangeResetsStaleNextRunAt(t *testing.T) {
	db := openManagerTestDB(t)
	t.Setenv("RSYNC_ALLOWED_SOURCE_PREFIXES", "/data")
	t.Setenv("RSYNC_ALLOWED_TARGET_PREFIXES", "/backup")

	node := model.Node{Name: "task-cron-edit-node", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key", BackupDir: "task-cron-edit-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	stale := time.Now().UTC().Add(-time.Hour)
	taskEntity := model.Task{
		Name: "task-cron-old", NodeID: node.ID, ExecutorType: "rsync",
		RsyncSource: "/data/old", RsyncTarget: "/backup/old",
		CronSpec: "*/5 * * * *", NextRunAt: &stale, Status: string(StatusPending), Enabled: true,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	api := NewTaskApiService(
		gormrepo.NewTaskRepository(db),
		gormrepo.NewNodeRepository(db),
		gormrepo.NewPolicyRepository(db),
		nil,
	)
	before := time.Now().UTC()
	if _, err := api.UpdateTask(context.Background(), taskEntity.ID, taskUpdateInputForTest(taskEntity, CreateTaskInput{
		Name: "task-cron-new", NodeID: node.ID, ExecutorType: "rsync",
		RsyncSource: "/data/new", RsyncTarget: "/backup/new", CronSpec: "0 0 1 1 *",
	})); err != nil {
		t.Fatalf("update task cron: %v", err)
	}

	var persisted model.Task
	if err := db.First(&persisted, taskEntity.ID).Error; err != nil {
		t.Fatalf("load updated task: %v", err)
	}
	if persisted.CronSpec != "0 0 1 1 *" || persisted.NextRunAt == nil || !persisted.NextRunAt.After(before) {
		t.Fatalf("cron edit must replace stale due deadline, got cron=%q next_run_at=%v", persisted.CronSpec, persisted.NextRunAt)
	}
}

func TestTaskUpdateCronChangeWhilePausedKeepsCursorEmptySQLite(t *testing.T) {
	runTaskUpdateCronChangeWhilePausedKeepsCursorEmpty(t, openManagerTestDB(t))
}

func TestTaskUpdateCronChangeWhilePausedKeepsCursorEmptyPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskUpdateCronChangeWhilePausedKeepsCursorEmpty(t, openTaskTerminalPostgresDB(t, dsn))
}

func runTaskUpdateCronChangeWhilePausedKeepsCursorEmpty(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Setenv("RSYNC_ALLOWED_SOURCE_PREFIXES", "/data")
	t.Setenv("RSYNC_ALLOWED_TARGET_PREFIXES", "/backup")
	node := model.Node{
		Name: "task-paused-cron-edit-node", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthType: "key", BackupDir: "task-paused-cron-edit-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	taskEntity := model.Task{
		Name: "task-paused-cron-old", NodeID: node.ID, ExecutorType: "rsync",
		RsyncSource: "/data/old", RsyncTarget: "/backup/old",
		CronSpec: "*/5 * * * *", Status: string(StatusSuccess), Enabled: false,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create paused task: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
		Updates(map[string]interface{}{"enabled": false, "next_run_at": nil}).Error; err != nil {
		t.Fatalf("pause task fixture: %v", err)
	}
	if err := db.First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload paused task revision: %v", err)
	}
	api := NewTaskApiService(
		gormrepo.NewTaskRepository(db),
		gormrepo.NewNodeRepository(db),
		gormrepo.NewPolicyRepository(db),
		nil,
	)
	if _, err := api.UpdateTask(context.Background(), taskEntity.ID, taskUpdateInputForTest(taskEntity, CreateTaskInput{
		Name: "task-paused-cron-new", NodeID: node.ID, ExecutorType: "rsync",
		RsyncSource: "/data/new", RsyncTarget: "/backup/new", CronSpec: "@every 1h",
	})); err != nil {
		t.Fatalf("update paused task cron: %v", err)
	}
	var paused model.Task
	if err := db.First(&paused, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload paused task: %v", err)
	}
	if paused.CronSpec != "@every 1h" || paused.Enabled || paused.NextRunAt != nil {
		t.Fatalf("paused cron edit cron=%q enabled=%v next=%v, want disabled with empty cursor",
			paused.CronSpec, paused.Enabled, paused.NextRunAt)
	}

	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil,
		taskscheduler.NewCronScheduler(), nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	if err := manager.Resume(taskEntity.ID); err != nil {
		t.Fatalf("resume edited cron task: %v", err)
	}
	var resumed model.Task
	if err := db.First(&resumed, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload resumed task: %v", err)
	}
	if !resumed.Enabled || resumed.NextRunAt == nil || !resumed.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("resumed cron task did not initialize a future cursor: %+v", resumed)
	}
}
func TestTaskUpdateMarkedRetryCronChangeReanchorsRegularCursorSQLite(t *testing.T) {
	runTaskUpdateRetryCronCursorMode(t, openManagerTestDB(t), model.TaskRunCronCursorModeRegularV1)
}

func TestTaskUpdateMarkedRetryCronChangeReanchorsRegularCursorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskUpdateRetryCronCursorMode(t, openTaskTerminalPostgresDB(t, dsn), model.TaskRunCronCursorModeRegularV1)
}

func TestTaskUpdateLegacyRetryCronChangePreservesRetryDeadlineSQLite(t *testing.T) {
	runTaskUpdateRetryCronCursorMode(t, openManagerTestDB(t), model.TaskRunCronCursorModeLegacy)
}

func TestTaskUpdateLegacyRetryCronChangePreservesRetryDeadlinePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskUpdateRetryCronCursorMode(t, openTaskTerminalPostgresDB(t, dsn), model.TaskRunCronCursorModeLegacy)
}

func runTaskUpdateRetryCronCursorMode(t *testing.T, db *gorm.DB, mode model.TaskRunCronCursorMode) {
	t.Helper()
	t.Setenv("RSYNC_ALLOWED_SOURCE_PREFIXES", "/data")
	t.Setenv("RSYNC_ALLOWED_TARGET_PREFIXES", "/backup")
	node := model.Node{
		Name: "task-retry-cursor-node", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthType: "key", BackupDir: "task-retry-cursor-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	previousNext := now.Add(3 * time.Hour)
	retryDeadline := now.Add(30 * time.Minute)
	taskEntity := model.Task{
		Name: "task-retry-cursor", NodeID: node.ID, ExecutorType: "rsync",
		RsyncSource: "/data/source", RsyncTarget: "/backup/target",
		CronSpec: "*/5 * * * *", Status: model.TaskRunStatusRetrying,
		Enabled: true, NextRunAt: &previousNext,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create retrying task: %v", err)
	}
	if err := db.First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload retrying task revision: %v", err)
	}
	predecessor := model.TaskRun{
		TaskID: taskEntity.ID, NodeIDSnapshot: node.ID, TriggerType: "cron",
		Status: model.TaskRunStatusFailed,
	}
	if err := db.Create(&predecessor).Error; err != nil {
		t.Fatalf("create retry predecessor: %v", err)
	}
	payload := fmt.Sprintf(`{"task_id":%d,"predecessor_run_id":%d`, taskEntity.ID, predecessor.ID)
	if mode == model.TaskRunCronCursorModeRegularV1 {
		payload += `,"cron_cursor_mode":"regular_cursor_v1"`
	}
	payload += "}"
	effect := model.TaskRunEffect{
		TaskRunID: predecessor.ID, EffectKey: "retry", EffectType: model.TaskRunEffectTypeRetry,
		Payload: payload, Status: model.TaskRunEffectStatusPending, NextAttemptAt: &retryDeadline,
	}
	if err := db.Create(&effect).Error; err != nil {
		t.Fatalf("create retry effect: %v", err)
	}
	api := NewTaskApiService(
		gormrepo.NewTaskRepository(db),
		gormrepo.NewNodeRepository(db),
		gormrepo.NewPolicyRepository(db),
		nil,
	)
	if _, err := api.UpdateTask(context.Background(), taskEntity.ID, taskUpdateInputForTest(taskEntity, CreateTaskInput{
		Name: "task-retry-cursor-edited", NodeID: node.ID, ExecutorType: "rsync",
		RsyncSource: "/data/source", RsyncTarget: "/backup/target", CronSpec: "0 0 1 1 *",
	})); err != nil {
		t.Fatalf("update retrying task cron: %v", err)
	}
	var persisted model.Task
	if err := db.First(&persisted, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload retrying task: %v", err)
	}
	if persisted.CronSpec != "0 0 1 1 *" {
		t.Fatalf("cron spec=%q, want edited spec", persisted.CronSpec)
	}
	if mode == model.TaskRunCronCursorModeRegularV1 {
		if persisted.NextRunAt == nil || !persisted.NextRunAt.After(now) || persisted.NextRunAt.Equal(previousNext) {
			t.Fatalf("marked retry cursor=%v, want a re-anchored future cursor distinct from %v", persisted.NextRunAt, previousNext)
		}
	} else if persisted.NextRunAt == nil || !persisted.NextRunAt.Equal(previousNext) {
		t.Fatalf("legacy retry deadline=%v, want preserved %v", persisted.NextRunAt, previousNext)
	}
	var persistedEffect model.TaskRunEffect
	if err := db.First(&persistedEffect, effect.ID).Error; err != nil {
		t.Fatalf("reload retry effect: %v", err)
	}
	if persistedEffect.NextAttemptAt == nil || !persistedEffect.NextAttemptAt.Equal(retryDeadline) {
		t.Fatalf("retry effect deadline=%v, want unchanged %v", persistedEffect.NextAttemptAt, retryDeadline)
	}
}

func TestTaskUpdateRejectsCorruptRetryCronProvenanceSQLite(t *testing.T) {
	runTaskUpdateCorruptRetryCronProvenance(t, openManagerTestDB(t))
}

func TestTaskUpdateRejectsCorruptRetryCronProvenancePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskUpdateCorruptRetryCronProvenance(t, openTaskTerminalPostgresDB(t, dsn))
}

func runTaskUpdateCorruptRetryCronProvenance(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Setenv("RSYNC_ALLOWED_SOURCE_PREFIXES", "/data")
	t.Setenv("RSYNC_ALLOWED_TARGET_PREFIXES", "/backup")
	node := model.Node{
		Name: "task-corrupt-cursor-node", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthType: "key", BackupDir: "task-corrupt-cursor-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	taskEntity := model.Task{
		Name: "task-corrupt-cursor", NodeID: node.ID, ExecutorType: "rsync",
		RsyncSource: "/data/source", RsyncTarget: "/backup/target",
		CronSpec: "*/5 * * * *", Status: model.TaskRunStatusRetrying,
		Enabled: true, NextRunAt: &now,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create retrying task: %v", err)
	}
	if err := db.First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload corrupt retry task revision: %v", err)
	}
	predecessor := model.TaskRun{TaskID: taskEntity.ID, NodeIDSnapshot: node.ID, TriggerType: "cron", Status: model.TaskRunStatusFailed}
	if err := db.Create(&predecessor).Error; err != nil {
		t.Fatalf("create retry predecessor: %v", err)
	}
	effect := model.TaskRunEffect{
		TaskRunID: predecessor.ID, EffectKey: "retry", EffectType: model.TaskRunEffectTypeRetry,
		Payload: `{"cron_cursor_mode":"future_v2"}`, Status: model.TaskRunEffectStatusPending,
	}
	if err := db.Create(&effect).Error; err != nil {
		t.Fatalf("create corrupt retry effect: %v", err)
	}
	api := NewTaskApiService(
		gormrepo.NewTaskRepository(db),
		gormrepo.NewNodeRepository(db),
		gormrepo.NewPolicyRepository(db),
		nil,
	)
	if _, err := api.UpdateTask(context.Background(), taskEntity.ID, taskUpdateInputForTest(taskEntity, CreateTaskInput{
		Name: "task-corrupt-cursor-edited", NodeID: node.ID, ExecutorType: "rsync",
		RsyncSource: "/data/source", RsyncTarget: "/backup/target", CronSpec: "0 0 1 1 *",
	})); err == nil {
		t.Fatal("corrupt retry provenance update unexpectedly succeeded")
	}
	var persisted model.Task
	if err := db.First(&persisted, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload corrupt retry task: %v", err)
	}
	if persisted.CronSpec != taskEntity.CronSpec || persisted.NextRunAt == nil || !persisted.NextRunAt.Equal(now) {
		t.Fatalf("corrupt provenance changed task state: %+v", persisted)
	}
}
