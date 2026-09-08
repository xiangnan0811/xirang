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
	fail bool
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
	source := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: "manual", Status: model.TaskRunStatusSuccess}
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
		Rule: rule, Event: Event{Type: EventBackupFailed, Context: map[string]interface{}{}},
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
	if err := db.First(&currentEffect, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentEffect.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("replayed effect status=%q, want succeeded", currentEffect.Status)
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
