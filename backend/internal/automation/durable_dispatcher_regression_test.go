package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type faultTransactionalPolicyController struct {
	fail           bool
	reconcileCalls int
}

func (c *faultTransactionalPolicyController) PausePolicyNext(context.Context, uint) error {
	return errors.New("non-transactional policy control is not used in durable dispatch")
}

func (c *faultTransactionalPolicyController) DisablePolicy(context.Context, uint) error {
	return errors.New("non-transactional policy control is not used in durable dispatch")
}

func (c *faultTransactionalPolicyController) PausePolicyNextTx(_ context.Context, tx *gorm.DB, policyID uint) error {
	if err := tx.Model(&model.Policy{}).Where("id = ?", policyID).Update("skip_next", true).Error; err != nil {
		return err
	}
	if c.fail {
		return errors.New("injected policy action failure")
	}
	return nil
}

func (c *faultTransactionalPolicyController) DisablePolicyTx(_ context.Context, tx *gorm.DB, policyID uint) ([]uint, error) {
	var taskIDs []uint
	if err := tx.Model(&model.Task{}).Where("policy_id = ? AND source = ?", policyID, "policy").Pluck("id", &taskIDs).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&model.Policy{}).Where("id = ?", policyID).Update("enabled", false).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&model.Task{}).Where("policy_id = ? AND source = ?", policyID, "policy").Update("cron_spec", "").Error; err != nil {
		return nil, err
	}
	if c.fail {
		return nil, errors.New("injected policy action failure")
	}
	return taskIDs, nil
}

func (c *faultTransactionalPolicyController) ReconcilePolicySchedules(uint, []uint) error {
	c.reconcileCalls++
	return errors.New("injected post-commit scheduler failure")
}

func runAtomicDisableReplay(t *testing.T, db *gorm.DB) {
	t.Helper()
	node := model.Node{
		Name: fmt.Sprintf("atomic-node-%d", time.Now().UnixNano()),
		Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	policy := model.Policy{
		Name:       fmt.Sprintf("atomic-policy-%d", time.Now().UnixNano()),
		SourcePath: "/source", TargetPath: "/target", CronSpec: "@every 1h", Enabled: true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	task := model.Task{
		Name: fmt.Sprintf("atomic-task-%d", time.Now().UnixNano()), NodeID: node.ID,
		PolicyID: &policy.ID, Source: "policy", ExecutorType: "local", CronSpec: "@every 1h",
		Status: "success", Enabled: true,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	sourcePolicy := model.Policy{
		Name:       fmt.Sprintf("source-policy-%d", time.Now().UnixNano()),
		SourcePath: "/source", TargetPath: "/target", CronSpec: "@every 1h", Enabled: true,
	}
	if err := db.Create(&sourcePolicy).Error; err != nil {
		t.Fatal(err)
	}
	sourceTask := model.Task{
		Name: fmt.Sprintf("source-task-%d", time.Now().UnixNano()), NodeID: node.ID,
		PolicyID: &sourcePolicy.ID, ExecutorType: "local", Status: "success", Enabled: true,
	}
	if err := db.Create(&sourceTask).Error; err != nil {
		t.Fatal(err)
	}
	source := model.TaskRun{TaskID: sourceTask.ID, NodeIDSnapshot: node.ID, TriggerType: "manual", Status: model.TaskRunStatusSuccess}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	rule := model.AutomationRule{
		Name: fmt.Sprintf("atomic-rule-%d", time.Now().UnixNano()), EventType: EventBackupFailed,
		EventFilter: "{}", ActionType: ActionDisablePolicy, ActionConfig: fmt.Sprintf(`{"policy_id":"%d"}`, policy.ID), Enabled: true,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(taskRunAutomationEffect{
		Rule: rule, Event: Event{Type: EventBackupFailed, Context: map[string]interface{}{
			"node_id": node.ID, "policy_id": sourcePolicy.ID, "task_id": sourceTask.ID, "task_run_id": source.ID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	effect := model.TaskRunEffect{
		TaskRunID: source.ID, EffectKey: "automation-rule:atomic", EffectType: model.TaskRunEffectTypeAutomationRule,
		Payload: string(payload), Status: model.TaskRunEffectStatusRunning, ClaimedBy: "automation-owner",
		ClaimLeaseUntil: new(time.Time),
	}
	if err := db.Create(&effect).Error; err != nil {
		t.Fatal(err)
	}
	controller := &faultTransactionalPolicyController{fail: true}
	dispatcher := NewDispatcher(db)
	dispatcher.SetPolicyController(controller)
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), Event{}, effect); err == nil {
		t.Fatal("faulted policy action should be returned")
	}
	var currentPolicy model.Policy
	if err := db.First(&currentPolicy, policy.ID).Error; err != nil {
		t.Fatal(err)
	}
	var currentTask model.Task
	if err := db.First(&currentTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !currentPolicy.Enabled || currentTask.CronSpec != "@every 1h" {
		t.Fatalf("rolled-back policy action changed durable state: policy=%+v task=%+v", currentPolicy, currentTask)
	}
	var logs []model.AutomationRuleLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Fatalf("rolled-back action left %d execution logs", len(logs))
	}
	var currentEffect model.TaskRunEffect
	if err := db.First(&currentEffect, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentEffect.Status != model.TaskRunEffectStatusRunning {
		t.Fatalf("rolled-back effect status=%q, want running for replay", currentEffect.Status)
	}

	controller.fail = false
	const callbackName = "r16:reject_execution_log"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "automation_rule_logs" {
			_ = tx.AddError(errors.New("injected execution log create failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), Event{}, currentEffect); err == nil {
		t.Fatal("log failure must roll back the successful policy call")
	}
	if err := db.Callback().Create().Remove(callbackName); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&currentPolicy, policy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&currentTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&currentEffect, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.AutomationRuleLog{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if !currentPolicy.Enabled || currentTask.CronSpec != "@every 1h" || currentEffect.Status != model.TaskRunEffectStatusRunning || count != 0 || controller.reconcileCalls != 0 {
		t.Fatalf("log failure leaked commit/ack/history/reconciliation: policy=%+v task=%+v effect=%+v logs=%d reconciles=%d", currentPolicy, currentTask, currentEffect, count, controller.reconcileCalls)
	}
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), Event{}, currentEffect); err != nil {
		t.Fatalf("replay durable policy action: %v", err)
	}
	if err := db.First(&currentPolicy, policy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&currentTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentPolicy.Enabled || currentTask.CronSpec != "" {
		t.Fatalf("committed policy action state policy=%+v task=%+v", currentPolicy, currentTask)
	}
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Result != ResultSuccess {
		t.Fatalf("replay logs=%+v, want one success", logs)
	}
	meta := requireExecutionMetadata(t, logs[0], ExecutionStagePolicyUpdated, "")
	if meta.SourcePolicyID != sourcePolicy.ID || meta.TargetPolicyID != policy.ID || meta.SourceTaskID != sourceTask.ID || meta.SourceTaskRunID != source.ID || meta.SourceNodeID != node.ID {
		t.Fatalf("source/target metadata confused after commit: %+v", meta)
	}
	if controller.reconcileCalls != 1 {
		t.Fatalf("post-commit reconciliation calls=%d want 1", controller.reconcileCalls)
	}
	if err := db.First(&currentEffect, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentEffect.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("replayed effect status=%q, want succeeded", currentEffect.Status)
	}
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), Event{}, currentEffect); err != nil {
		t.Fatalf("acknowledged replay: %v", err)
	}
	if err := db.Model(&model.AutomationRuleLog{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || controller.reconcileCalls != 1 {
		t.Fatalf("replay duplicated committed history or reconciliation: logs=%d reconciles=%d", count, controller.reconcileCalls)
	}
}

func runAutomationParentFanout(t *testing.T, db *gorm.DB) {
	t.Helper()
	node := model.Node{
		Name: fmt.Sprintf("fanout-node-%d", time.Now().UnixNano()),
		Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	task := model.Task{
		Name: fmt.Sprintf("fanout-task-%d", time.Now().UnixNano()), NodeID: node.ID,
		ExecutorType: "local", Status: "success", Enabled: true,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	run := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: node.ID, TriggerType: "manual", Status: model.TaskRunStatusSuccess}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	matching := model.AutomationRule{
		Name: fmt.Sprintf("fanout-matching-%d", time.Now().UnixNano()), EventType: EventBackupFailed,
		EventFilter: `{"severity":"high"}`, ActionType: ActionSendNotification,
		ActionConfig: `{"message":"backup failed: {{.severity}}"}`, Enabled: true,
	}
	disabled := model.AutomationRule{
		Name: fmt.Sprintf("fanout-disabled-%d", time.Now().UnixNano()), EventType: EventBackupFailed,
		EventFilter: "{}", ActionType: ActionSendNotification, ActionConfig: `{"message":"disabled"}`, Enabled: false,
	}
	mismatch := model.AutomationRule{
		Name: fmt.Sprintf("fanout-mismatch-%d", time.Now().UnixNano()), EventType: EventBackupFailed,
		EventFilter: `{"severity":"low"}`, ActionType: ActionSendNotification,
		ActionConfig: `{"message":"mismatch"}`, Enabled: true,
	}
	for _, rule := range []*model.AutomationRule{&matching, &disabled, &mismatch} {
		if err := db.Create(rule).Error; err != nil {
			t.Fatal(err)
		}
	}
	parentPayload, err := json.Marshal(automationTaskRunEventPayload{
		EventType: EventBackupFailed, Context: map[string]interface{}{"severity": "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent := model.TaskRunEffect{
		TaskRunID: run.ID, EffectKey: "automation:fanout", EffectType: model.TaskRunEffectTypeAutomation,
		Payload: string(parentPayload), Status: model.TaskRunEffectStatusRunning, ClaimedBy: "fanout-owner",
	}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(db)
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), Event{}, parent); err != nil {
		t.Fatalf("fan out automation parent effect: %v", err)
	}
	var currentParent model.TaskRunEffect
	if err := db.First(&currentParent, parent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentParent.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("parent effect status=%q, want succeeded", currentParent.Status)
	}
	var children []model.TaskRunEffect
	if err := db.Where("task_run_id = ? AND effect_type = ?", run.ID, model.TaskRunEffectTypeAutomationRule).Find(&children).Error; err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("child automation effects=%d, want one matching enabled rule", len(children))
	}
	var childPayload taskRunAutomationEffect
	if err := json.Unmarshal([]byte(children[0].Payload), &childPayload); err != nil {
		t.Fatal(err)
	}
	if childPayload.Rule.ID != matching.ID {
		t.Fatalf("child rule=%d, want matching rule %d", childPayload.Rule.ID, matching.ID)
	}
	if err := db.Model(&model.TaskRunEffect{}).Where("id = ?", children[0].ID).Updates(map[string]interface{}{
		"status": model.TaskRunEffectStatusRunning, "claimed_by": "fanout-owner",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&children[0], children[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), Event{}, children[0]); err != nil {
		t.Fatalf("execute fanout child effect: %v", err)
	}
	var child model.TaskRunEffect
	if err := db.First(&child, children[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if child.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("child effect status=%q, want succeeded", child.Status)
	}
	var logs []model.AutomationRuleLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].RuleID != matching.ID || logs[0].Result != ResultSuccess {
		t.Fatalf("fanout action logs=%+v, want one successful matching-rule log", logs)
	}
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), Event{}, currentParent); err != nil {
		t.Fatalf("replay acknowledged parent effect: %v", err)
	}
	var childCount int64
	if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND effect_type = ?", run.ID, model.TaskRunEffectTypeAutomationRule).Count(&childCount).Error; err != nil {
		t.Fatal(err)
	}
	if childCount != 1 {
		t.Fatalf("replayed parent created %d duplicate child effects", childCount)
	}
}

func TestDurableAutomationParentFanoutAndReplaySQLite(t *testing.T) {
	runAutomationParentFanout(t, setupDispatcherTestDB(t))
}

func TestDurableAutomationParentFanoutAndReplayPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runAutomationParentFanout(t, openAutomationPostgresDB(t, dsn))
}

func TestDurableAutomationActionLogAckAtomicAndReplaySQLite(t *testing.T) {
	runAtomicDisableReplay(t, setupDispatcherTestDB(t))
}

func TestDurableAutomationActionLogAckAtomicAndReplayPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runAtomicDisableReplay(t, openAutomationPostgresDB(t, dsn))
}

func openAutomationPostgresDB(t *testing.T, dsn string) *gorm.DB {
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
	schema := fmt.Sprintf("xirang_automation_%d", time.Now().UTC().UnixNano())
	if _, err := baseSQL.Exec("CREATE SCHEMA " + schema); err != nil {
		_ = baseSQL.Close()
		t.Fatalf("create isolated PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = baseSQL.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
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
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{}, &model.TaskRunEffect{}, &model.AutomationRule{}, &model.AutomationRuleLog{}); err != nil {
		t.Fatalf("migrate isolated PostgreSQL automation tables: %v", err)
	}
	return db
}

type metadataTransactionalPolicyController struct {
	metadataPolicyController
}

func (c *metadataTransactionalPolicyController) PausePolicyNextTx(ctx context.Context, _ *gorm.DB, policyID uint) error {
	return c.PausePolicyNext(ctx, policyID)
}

func (c *metadataTransactionalPolicyController) DisablePolicyTx(ctx context.Context, _ *gorm.DB, policyID uint) ([]uint, error) {
	return nil, c.DisablePolicy(ctx, policyID)
}

type metadataTransactionalTriggerer struct {
	fakeTaskTriggerer
}

func (f *metadataTransactionalTriggerer) ReserveAutomationRunTx(_ context.Context, _ *gorm.DB, taskID uint) (uint, error) {
	f.taskIDs = append(f.taskIDs, taskID)
	return f.runID, f.err
}

func TestDurableSafeExecutionActions(t *testing.T) {
	for _, actionType := range []string{ActionPausePolicy, ActionDisablePolicy, ActionTriggerTask, ActionSendNotification} {
		for _, fractional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fractional_%t", actionType, fractional), func(t *testing.T) {
				d := NewDispatcher(nil)
				controller := &metadataTransactionalPolicyController{}
				triggerer := &metadataTransactionalTriggerer{fakeTaskTriggerer: fakeTaskTriggerer{runID: 202}}
				d.SetPolicyController(controller)
				d.SetTaskTriggerer(triggerer)
				value := "2"
				if fractional {
					value = "2.75"
				}
				config := fmt.Sprintf(`{"policy_id":%s,"task_id":%s,"message":"private"}`, value, value)
				action, err := d.executeActionTx(context.Background(), nil, model.AutomationRule{ActionType: actionType, ActionConfig: config}, Event{
					Type:    EventBackupSucceeded,
					Context: map[string]interface{}{"node_id": float64(9), "policy_id": float64(77), "task_id": float64(101), "task_run_id": float64(102)},
				})
				if err != nil {
					t.Fatal(err)
				}
				stage := ExecutionStagePolicyUpdated
				switch actionType {
				case ActionTriggerTask:
					stage = ExecutionStageTaskRunReserved
				case ActionSendNotification:
					stage = ExecutionStageNotificationRecorded
				}
				meta := requireExecutionMetadata(t, action.log, stage, "")
				if meta.SourceNodeID != 9 || meta.SourcePolicyID != 77 || meta.SourceTaskID != 101 || meta.SourceTaskRunID != 102 {
					t.Fatalf("JSON round-trip lost exact source: %+v", meta)
				}
				var details actionDetails
				if err := json.Unmarshal([]byte(action.log.Details), &details); err != nil {
					t.Fatal(err)
				}
				switch actionType {
				case ActionPausePolicy, ActionDisablePolicy:
					if len(controller.targets) != 1 || controller.targets[0] != 2 || (meta.TargetPolicyID != 0) == fractional {
						t.Fatalf("strict display check changed execution or invented target: %+v calls=%v", meta, controller.targets)
					}
				case ActionTriggerTask:
					if len(triggerer.taskIDs) != 1 || triggerer.taskIDs[0] != 2 {
						t.Fatalf("changed admission: %v", triggerer.taskIDs)
					}
					if (!fractional && (details.TaskID != 2 || details.TaskRunID != 202)) || (fractional && (details.TaskID != 0 || details.TaskRunID != 0)) {
						t.Fatalf("target pair confused with source or fractional config: %+v", details)
					}
				case ActionSendNotification:
					if details.Message == nil || *details.Message != "private" || details.TaskID != 0 || meta.TargetPolicyID != 0 {
						t.Fatalf("notification did more than record: %+v", details)
					}
				}
			})
		}
	}
}

func TestDurableSafeExecutionValidationAndErrors(t *testing.T) {
	for _, actionType := range []string{ActionPausePolicy, ActionDisablePolicy, ActionTriggerTask} {
		for _, tc := range []struct {
			name   string
			err    error
			reason string
		}{
			{"cancel", context.Canceled, ReasonContextCanceled},
			{"wrapped_cancel", fmt.Errorf("private: %w", context.Canceled), ReasonContextCanceled},
			{"deadline", fmt.Errorf("private: %w", context.DeadlineExceeded), ReasonDeadlineExceeded},
			{"missing", gorm.ErrRecordNotFound, ReasonTargetNotFound},
			{"wrapped_missing", fmt.Errorf("private: %w", gorm.ErrRecordNotFound), ReasonTargetNotFound},
			{"generic", errors.New("FAKE_PRIVATE_ERROR"), ReasonActionFailed},
		} {
			t.Run(actionType+"/"+tc.name, func(t *testing.T) {
				d := NewDispatcher(nil)
				d.SetPolicyController(&metadataTransactionalPolicyController{metadataPolicyController: metadataPolicyController{err: tc.err}})
				d.SetTaskTriggerer(&metadataTransactionalTriggerer{fakeTaskTriggerer: fakeTaskTriggerer{err: tc.err}})
				action, err := d.executeActionTx(context.Background(), nil, model.AutomationRule{ActionType: actionType, ActionConfig: `{"policy_id":2,"task_id":2}`}, Event{Type: EventBackupFailed})
				if !errors.Is(err, tc.err) {
					t.Fatalf("lost action error chain: %v", err)
				}
				requireExecutionMetadata(t, action.log, ExecutionStageActionExecution, tc.reason)
			})
		}
		t.Run(actionType+"/invalid", func(t *testing.T) {
			action, err := NewDispatcher(nil).executeActionTx(context.Background(), nil, model.AutomationRule{ActionType: actionType, ActionConfig: "{}"}, Event{})
			if err == nil {
				t.Fatal("invalid target accepted")
			}
			requireExecutionMetadata(t, action.log, ExecutionStageActionValidation, ReasonInvalidTarget)
		})
		t.Run(actionType+"/canceled_before_call", func(t *testing.T) {
			d := NewDispatcher(nil)
			controller := &metadataTransactionalPolicyController{}
			triggerer := &metadataTransactionalTriggerer{}
			d.SetPolicyController(controller)
			d.SetTaskTriggerer(triggerer)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			action, err := d.executeActionTx(ctx, nil, model.AutomationRule{ActionType: actionType, ActionConfig: `{"policy_id":2,"task_id":2}`}, Event{})
			if !errors.Is(err, context.Canceled) || len(controller.targets) != 0 || len(triggerer.taskIDs) != 0 {
				t.Fatalf("canceled action called executor or lost error: %v", err)
			}
			requireExecutionMetadata(t, action.log, ExecutionStageActionExecution, ReasonContextCanceled)
		})
	}
	for _, tc := range []struct {
		name   string
		action string
		reason string
	}{
		{"pause_controller", ActionPausePolicy, ReasonPolicyControllerUnavailable},
		{"disable_controller", ActionDisablePolicy, ReasonPolicyControllerUnavailable},
		{"trigger_capability", ActionTriggerTask, ReasonTransactionalExecutorUnavailable},
		{"trigger_missing", ActionTriggerTask, ReasonExecutorUnavailable},
		{"unsupported", "future_action", ReasonUnsupportedAction},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(nil)
			d.SetPolicyController(&metadataPolicyController{})
			if tc.name != "trigger_missing" {
				d.SetTaskTriggerer(&fakeTaskTriggerer{})
			}
			action, err := d.executeActionTx(context.Background(), nil, model.AutomationRule{ActionType: tc.action, ActionConfig: `{"policy_id":2,"task_id":2}`}, Event{})
			if err == nil {
				t.Fatal("missing capability accepted")
			}
			requireExecutionMetadata(t, action.log, ExecutionStageActionValidation, tc.reason)
		})
	}
}
