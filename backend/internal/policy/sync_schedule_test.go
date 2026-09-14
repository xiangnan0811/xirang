package policy

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestTaskCronScheduleBulkUpdatesPreservePausedRetryAndSameSpecCursors(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s/policy-schedule.db?_busy_timeout=5000", t.TempDir())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}, &model.TaskRunEffect{}); err != nil {
		t.Fatalf("migrate policy schedule tables: %v", err)
	}
	node := model.Node{Name: "policy-schedule-node", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	normalNext := now.Add(3 * time.Hour)
	pausedNext := now.Add(3 * time.Hour)
	retryNext := now.Add(3 * time.Hour)
	staleNoCronNext := now.Add(3 * time.Hour)
	normal := model.Task{
		Name: "normal", NodeID: node.ID, ExecutorType: "local", CronSpec: "@every 1h",
		Status: "success", Enabled: true, NextRunAt: &normalNext,
	}
	paused := model.Task{
		Name: "paused", NodeID: node.ID, ExecutorType: "local", CronSpec: "@every 1h",
		Status: "success", Enabled: true, NextRunAt: &pausedNext,
	}
	retrying := model.Task{
		Name: "retrying", NodeID: node.ID, ExecutorType: "local", CronSpec: "@every 1h",
		Status: "retrying", Enabled: true, NextRunAt: &retryNext,
	}
	staleNoCron := model.Task{
		Name: "stale-no-cron", NodeID: node.ID, ExecutorType: "local",
		Status: "success", Enabled: true, NextRunAt: &staleNoCronNext,
	}
	for _, task := range []*model.Task{&normal, &paused, &retrying, &staleNoCron} {
		if err := db.Create(task).Error; err != nil {
			t.Fatalf("create task %q: %v", task.Name, err)
		}
	}
	if err := db.Model(&model.Task{}).Where("id = ?", paused.ID).
		Updates(map[string]interface{}{"enabled": false}).Error; err != nil {
		t.Fatalf("pause task fixture: %v", err)
	}

	applySchedule := func(taskID uint, cronSpec string) error {
		return db.Transaction(func(tx *gorm.DB) error {
			var task model.Task
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, taskID).Error; err != nil {
				return err
			}
			updates, err := taskCronScheduleUpdatesTx(tx, task, cronSpec)
			if err != nil {
				return err
			}
			return tx.Model(&model.Task{}).Where("id = ?", taskID).Updates(updates).Error
		})
	}
	if err := applySchedule(normal.ID, "@every 1h"); err != nil {
		t.Fatalf("same-spec schedule update: %v", err)
	}
	if err := applySchedule(paused.ID, "@every 2h"); err != nil {
		t.Fatalf("paused schedule update: %v", err)
	}
	if err := applySchedule(retrying.ID, "@every 2h"); err != nil {
		t.Fatalf("retrying schedule update: %v", err)
	}
	if err := applySchedule(staleNoCron.ID, ""); err != nil {
		t.Fatalf("empty schedule cleanup: %v", err)
	}

	var gotNormal, gotPaused, gotRetrying, gotStaleNoCron model.Task
	for task, got := range map[uint]*model.Task{
		normal.ID:      &gotNormal,
		paused.ID:      &gotPaused,
		retrying.ID:    &gotRetrying,
		staleNoCron.ID: &gotStaleNoCron,
	} {
		if err := db.First(got, task).Error; err != nil {
			t.Fatalf("reload task %d: %v", task, err)
		}
	}
	if gotNormal.NextRunAt == nil || !gotNormal.NextRunAt.Equal(normalNext) {
		t.Fatalf("same-spec update reset normal cursor: got %v, want %v", gotNormal.NextRunAt, normalNext)
	}
	if gotPaused.Enabled || gotPaused.NextRunAt != nil || gotPaused.CronSpec != "@every 2h" {
		t.Fatalf("paused update state=%+v, want disabled with empty cursor", gotPaused)
	}
	if gotRetrying.NextRunAt == nil || !gotRetrying.NextRunAt.Equal(retryNext) || gotRetrying.CronSpec != "@every 2h" {
		t.Fatalf("retrying update reset retry cursor: got next=%v cron=%q, want %v/@every 2h",
			gotRetrying.NextRunAt, gotRetrying.CronSpec, retryNext)
	}
	if gotStaleNoCron.NextRunAt != nil || gotStaleNoCron.CronSpec != "" {
		t.Fatalf("empty schedule cleanup retained stale cursor: cron=%q next=%v",
			gotStaleNoCron.CronSpec, gotStaleNoCron.NextRunAt)
	}
}

func TestPolicyScheduleModesSQLite(t *testing.T) {
	runPolicyScheduleModes(t, openPolicyScheduleSQLiteDB(t))
}

func TestPolicyScheduleModesPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runPolicyScheduleModes(t, openPolicySchedulePostgresDB(t, dsn))
}

func runPolicyScheduleModes(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(
		&model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{},
		&model.TaskRun{}, &model.TaskRunEffect{},
	); err != nil {
		t.Fatalf("migrate policy schedule mode tables: %v", err)
	}
	node := model.Node{
		Name: "policy-mode-node", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthType: "key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create policy mode node: %v", err)
	}
	policy := model.Policy{
		Name: "policy-mode", SourcePath: "/data/source", TargetPath: "/backup/target",
		CronSpec: "@every 1h", Enabled: true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create policy mode policy: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: node.ID}).Error; err != nil {
		t.Fatalf("create policy-node association: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	markedCursor := now.Add(4 * time.Hour)
	legacyDeadline := now.Add(30 * time.Minute)
	normalCursor := now.Add(4 * time.Hour)
	policyID := policy.ID
	tasks := []model.Task{
		{
			Name: "policy-marked", NodeID: node.ID, PolicyID: &policyID, Source: "policy",
			ExecutorType: "rsync", CronSpec: "@every 1h", Status: model.TaskRunStatusRetrying,
			Enabled: true, NextRunAt: &markedCursor,
		},
		{
			Name: "policy-legacy", NodeID: node.ID, PolicyID: &policyID, Source: "policy",
			ExecutorType: "rsync", CronSpec: "@every 1h", Status: model.TaskRunStatusRetrying,
			Enabled: true, NextRunAt: &legacyDeadline,
		},
		{
			Name: "policy-normal", NodeID: node.ID, PolicyID: &policyID, Source: "policy",
			ExecutorType: "rsync", CronSpec: "@every 1h", Status: model.TaskRunStatusSuccess,
			Enabled: true, NextRunAt: &normalCursor,
		},
	}
	for i := range tasks {
		if err := db.Create(&tasks[i]).Error; err != nil {
			t.Fatalf("create policy task %q: %v", tasks[i].Name, err)
		}
	}
	retryDeadline := now.Add(90 * time.Minute)
	for i, mode := range []string{"regular_cursor_v1", ""} {
		run := model.TaskRun{
			TaskID: tasks[i].ID, NodeIDSnapshot: node.ID, TriggerType: "cron",
			Status: model.TaskRunStatusFailed,
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("create retry predecessor %d: %v", i, err)
		}
		payload := fmt.Sprintf(`{"task_id":%d,"predecessor_run_id":%d`, tasks[i].ID, run.ID)
		if mode != "" {
			payload += fmt.Sprintf(`,"cron_cursor_mode":"%s"`, mode)
		}
		payload += "}"
		effect := model.TaskRunEffect{
			TaskRunID: run.ID, EffectKey: "retry", EffectType: model.TaskRunEffectTypeRetry,
			Payload: payload, Status: model.TaskRunEffectStatusPending, NextAttemptAt: &retryDeadline,
		}
		if err := db.Create(&effect).Error; err != nil {
			t.Fatalf("create retry effect %d: %v", i, err)
		}
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return PauseTasksForPolicy(tx, nil, policy.ID)
	}); err != nil {
		t.Fatalf("pause policy tasks: %v", err)
	}
	var marked, legacy, normal model.Task
	if err := db.First(&marked, tasks[0].ID).Error; err != nil {
		t.Fatalf("reload marked paused task: %v", err)
	}
	if err := db.First(&legacy, tasks[1].ID).Error; err != nil {
		t.Fatalf("reload legacy paused task: %v", err)
	}
	if err := db.First(&normal, tasks[2].ID).Error; err != nil {
		t.Fatalf("reload normal paused task: %v", err)
	}
	if marked.CronSpec != "" || marked.NextRunAt != nil {
		t.Fatalf("marked pause state=%+v, want empty cron/cursor", marked)
	}
	if legacy.CronSpec != "" || legacy.NextRunAt == nil || !legacy.NextRunAt.Equal(legacyDeadline) {
		t.Fatalf("legacy pause state=%+v, want preserved deadline %v", legacy, legacyDeadline)
	}
	if normal.CronSpec != "" || normal.NextRunAt != nil {
		t.Fatalf("normal pause state=%+v, want empty cron/cursor", normal)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return ResumeTasksForPolicy(tx, nil, policy.ID, "@every 2h")
	}); err != nil {
		t.Fatalf("resume policy tasks: %v", err)
	}
	var resumedMarked, resumedLegacy, resumedNormal model.Task
	if err := db.First(&resumedMarked, tasks[0].ID).Error; err != nil {
		t.Fatalf("reload marked resumed task: %v", err)
	}
	if err := db.First(&resumedLegacy, tasks[1].ID).Error; err != nil {
		t.Fatalf("reload legacy resumed task: %v", err)
	}
	if err := db.First(&resumedNormal, tasks[2].ID).Error; err != nil {
		t.Fatalf("reload normal resumed task: %v", err)
	}
	if resumedMarked.CronSpec != "@every 2h" || resumedMarked.NextRunAt == nil ||
		resumedMarked.NextRunAt.Equal(markedCursor) {
		t.Fatalf("marked resume state=%+v, want new regular cursor", resumedMarked)
	}
	if resumedLegacy.CronSpec != "@every 2h" || resumedLegacy.NextRunAt == nil ||
		!resumedLegacy.NextRunAt.Equal(legacyDeadline) {
		t.Fatalf("legacy resume state=%+v, want preserved retry deadline %v", resumedLegacy, legacyDeadline)
	}
	if resumedNormal.CronSpec != "@every 2h" || resumedNormal.NextRunAt == nil ||
		resumedNormal.NextRunAt.Equal(normalCursor) {
		t.Fatalf("normal resume state=%+v, want new regular cursor", resumedNormal)
	}
	markedAfterResume := *resumedMarked.NextRunAt
	normalAfterResume := *resumedNormal.NextRunAt
	if err := db.Transaction(func(tx *gorm.DB) error {
		return ResumeTasksForPolicy(tx, nil, policy.ID, "@every 2h")
	}); err != nil {
		t.Fatalf("same-spec policy resume: %v", err)
	}
	if err := db.First(&resumedMarked, tasks[0].ID).Error; err != nil {
		t.Fatalf("reload marked same-spec task: %v", err)
	}
	if err := db.First(&resumedNormal, tasks[2].ID).Error; err != nil {
		t.Fatalf("reload normal same-spec task: %v", err)
	}
	if resumedMarked.NextRunAt == nil || !resumedMarked.NextRunAt.Equal(markedAfterResume) ||
		resumedNormal.NextRunAt == nil || !resumedNormal.NextRunAt.Equal(normalAfterResume) {
		t.Fatalf("same-spec policy resume reset cursors: marked=%v normal=%v", resumedMarked.NextRunAt, resumedNormal.NextRunAt)
	}

	control := NewControlService(db, nil)
	if err := control.Disable(context.Background(), policy.ID); err != nil {
		t.Fatalf("disable policy: %v", err)
	}
	var disabledMarked, disabledLegacy, disabledNormal model.Task
	for i, task := range []*model.Task{&disabledMarked, &disabledLegacy, &disabledNormal} {
		if err := db.First(task, tasks[i].ID).Error; err != nil {
			t.Fatalf("reload disabled task %d: %v", i, err)
		}
	}
	if disabledMarked.CronSpec != "" || disabledMarked.NextRunAt != nil {
		t.Fatalf("marked disable state=%+v, want empty cron/cursor", disabledMarked)
	}
	if disabledLegacy.CronSpec != "" || disabledLegacy.NextRunAt == nil ||
		!disabledLegacy.NextRunAt.Equal(legacyDeadline) {
		t.Fatalf("legacy disable state=%+v, want preserved retry deadline %v", disabledLegacy, legacyDeadline)
	}
	if disabledNormal.CronSpec != "" || disabledNormal.NextRunAt != nil {
		t.Fatalf("normal disable state=%+v, want empty cron/cursor", disabledNormal)
	}
	var disabledPolicy model.Policy
	if err := db.First(&disabledPolicy, policy.ID).Error; err != nil {
		t.Fatalf("reload disabled policy: %v", err)
	}
	if disabledPolicy.Enabled {
		t.Fatalf("policy remained enabled after disable: %+v", disabledPolicy)
	}

	var effects []model.TaskRunEffect
	if err := db.Order("id").Find(&effects).Error; err != nil {
		t.Fatalf("reload retry effects: %v", err)
	}
	if len(effects) != 2 || effects[0].NextAttemptAt == nil || effects[1].NextAttemptAt == nil ||
		!effects[0].NextAttemptAt.Equal(retryDeadline) || !effects[1].NextAttemptAt.Equal(retryDeadline) {
		t.Fatalf("policy schedule changed retry effect deadlines: %+v", effects)
	}
}
func TestPolicyTaskCronOverrideLifecycleSQLite(t *testing.T) {
	runPolicyTaskCronOverrideLifecycle(t, openPolicyScheduleSQLiteDB(t))
}

func TestPolicyTaskCronOverrideLifecyclePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runPolicyTaskCronOverrideLifecycle(t, openPolicySchedulePostgresDB(t, dsn))
}

func runPolicyTaskCronOverrideLifecycle(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Setenv("RSYNC_ALLOWED_SOURCE_PREFIXES", "/data")
	t.Setenv("RSYNC_ALLOWED_TARGET_PREFIXES", "/backup")
	if err := db.AutoMigrate(
		&model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{},
		&model.TaskRun{}, &model.TaskRunEffect{},
	); err != nil {
		t.Fatalf("migrate policy cron override tables: %v", err)
	}
	nodes := []model.Node{
		{Name: "policy-override-manual-node", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key", BackupDir: "policy-override-manual-node"},
		{Name: "policy-override-custom-node", Host: "127.0.0.1", Port: 23, Username: "root", AuthType: "key", BackupDir: "policy-override-custom-node"},
		{Name: "policy-override-inherited-node", Host: "127.0.0.1", Port: 24, Username: "root", AuthType: "key", BackupDir: "policy-override-inherited-node"},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatalf("create policy cron override node %d: %v", i, err)
		}
	}
	policy := model.Policy{
		Name: "policy-cron-override", SourcePath: "/data/source",
		TargetPath: "/backup/policy-cron-override", CronSpec: "@every 1h", Enabled: true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create policy cron override policy: %v", err)
	}
	for i := range nodes {
		if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: nodes[i].ID}).Error; err != nil {
			t.Fatalf("create policy cron override association %d: %v", i, err)
		}
	}
	policyID := policy.ID
	tasks := []model.Task{
		{
			Name: "manual-override", NodeID: nodes[0].ID, PolicyID: &policyID,
			RsyncSource: "/data/source", RsyncTarget: PolicyNodeTargetPath(policy.TargetPath, policy.ID, nodes[0].ID),
			ExecutorType: "rsync", CronSpec: "", CronOverride: true,
			Status: model.TaskRunStatusPending, Enabled: true, Source: "policy",
		},
		{
			Name: "custom-override", NodeID: nodes[1].ID, PolicyID: &policyID,
			RsyncSource: "/data/source", RsyncTarget: PolicyNodeTargetPath(policy.TargetPath, policy.ID, nodes[1].ID),
			ExecutorType: "rsync", CronSpec: "@every 30m", CronOverride: true,
			Status: model.TaskRunStatusPending, Enabled: true, Source: "policy",
		},
	}
	for i := range tasks {
		if err := db.Create(&tasks[i]).Error; err != nil {
			t.Fatalf("create policy cron override task %d: %v", i, err)
		}
	}

	nodeIDs := []uint{nodes[0].ID, nodes[1].ID, nodes[2].ID}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return SyncPolicyTasks(tx, nil, policy, nodeIDs)
	}); err != nil {
		t.Fatalf("initial policy task sync: %v", err)
	}
	var inherited model.Task
	if err := db.Where("node_id = ? AND policy_id = ?", nodes[2].ID, policy.ID).First(&inherited).Error; err != nil {
		t.Fatalf("load generated inherited task: %v", err)
	}
	if inherited.CronOverride || inherited.CronSpec != "@every 1h" || inherited.NextRunAt == nil {
		t.Fatalf("generated inherited task state=%+v", inherited)
	}

	policy.CronSpec = "@every 2h"
	policy.Name = "policy-cron-override-renamed"
	if err := db.Transaction(func(tx *gorm.DB) error {
		return SyncPolicyTasks(tx, nil, policy, nodeIDs)
	}); err != nil {
		t.Fatalf("metadata and cron policy sync: %v", err)
	}
	var manual, custom model.Task
	if err := db.First(&manual, tasks[0].ID).Error; err != nil {
		t.Fatalf("reload manual override after sync: %v", err)
	}
	if err := db.First(&custom, tasks[1].ID).Error; err != nil {
		t.Fatalf("reload custom override after sync: %v", err)
	}
	if manual.CronSpec != "" || !manual.CronOverride || manual.NextRunAt != nil ||
		custom.CronSpec != "@every 30m" || !custom.CronOverride {
		t.Fatalf("policy edit changed task-owned schedules: manual=%+v custom=%+v", manual, custom)
	}
	if err := db.First(&inherited, inherited.ID).Error; err != nil {
		t.Fatalf("reload inherited task after policy cron edit: %v", err)
	}
	if inherited.CronOverride || inherited.CronSpec != "@every 2h" || inherited.NextRunAt == nil {
		t.Fatalf("policy edit failed to update inherited task: %+v", inherited)
	}
	inheritedID := inherited.ID

	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable policy row: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return PauseTasksForPolicy(tx, nil, policy.ID)
	}); err != nil {
		t.Fatalf("pause policy task schedules: %v", err)
	}
	for i, task := range []*model.Task{&manual, &custom, &inherited} {
		*task = model.Task{}
		if err := db.First(task, []uint{tasks[0].ID, tasks[1].ID, inheritedID}[i]).Error; err != nil {
			t.Fatalf("reload paused task %d: %v", i, err)
		}
	}
	if manual.CronSpec != "" || manual.NextRunAt != nil || !manual.CronOverride ||
		custom.CronSpec != "@every 30m" || custom.NextRunAt != nil || !custom.CronOverride ||
		inherited.CronSpec != "" || inherited.NextRunAt != nil || inherited.CronOverride {
		t.Fatalf("policy pause state lost provenance/cursors: manual=%+v custom=%+v inherited=%+v",
			manual, custom, inherited)
	}

	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).Update("enabled", true).Error; err != nil {
		t.Fatalf("enable policy row: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return ResumeTasksForPolicy(tx, nil, policy.ID, "@every 3h")
	}); err != nil {
		t.Fatalf("resume policy task schedules: %v", err)
	}
	for i, task := range []*model.Task{&manual, &custom, &inherited} {
		*task = model.Task{}
		if err := db.First(task, []uint{tasks[0].ID, tasks[1].ID, inheritedID}[i]).Error; err != nil {
			t.Fatalf("reload resumed task %d: %v", i, err)
		}
	}
	if manual.CronSpec != "" || manual.NextRunAt != nil || !manual.CronOverride ||
		custom.CronSpec != "@every 30m" || custom.NextRunAt == nil || !custom.CronOverride ||
		inherited.CronSpec != "@every 3h" || inherited.NextRunAt == nil || inherited.CronOverride {
		t.Fatalf("policy resume state lost provenance/cursors: manual=%+v custom=%+v inherited=%+v",
			manual, custom, inherited)
	}
}

func openPolicyScheduleSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s/policy-mode.db?_busy_timeout=5000", t.TempDir())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open policy schedule SQLite database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get policy schedule SQLite connection: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func openPolicySchedulePostgresDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open policy schedule PostgreSQL base: %v", err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatalf("get policy schedule PostgreSQL base connection: %v", err)
	}
	schema := fmt.Sprintf("xirang_policy_schedule_%d", time.Now().UTC().UnixNano())
	if _, err := baseSQL.Exec("CREATE SCHEMA " + schema); err != nil {
		_ = baseSQL.Close()
		t.Fatalf("create policy schedule PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := baseSQL.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Errorf("drop policy schedule PostgreSQL schema: %v", err)
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
		t.Fatalf("open policy schedule PostgreSQL scoped connection: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get policy schedule PostgreSQL scoped connection: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}
