package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupDispatcherTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Use a unique named in-memory DB per test with cache=shared so that
	// GORM's connection pool uses the same database for all connections.
	dsn := fmt.Sprintf("file:%s?mode=memory&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.AutomationRule{},
		&model.AutomationRuleLog{},
		&model.Policy{},
		&model.Task{},
		&model.TaskRun{},
		&model.TaskRunEffect{},
	); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	// Create test data that actions will reference (do not set explicit IDs).
	db.Create(&model.Policy{Name: "test-policy", SourcePath: "/src", TargetPath: "/dst", CronSpec: "0 0 * * *", Enabled: true})
	db.Create(&model.Task{Name: "test-task", NodeID: 1, ExecutorType: "local", Status: "idle", Enabled: true})
	return db
}

func seedRule(t *testing.T, db *gorm.DB, r model.AutomationRule) {
	t.Helper()
	if err := db.Create(&r).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}
}

// ---- matchFilter ----

func TestMatchFilter_Empty(t *testing.T) {
	if !matchFilter("{}", nil) {
		t.Error("empty filter should match everything")
	}
	if !matchFilter("", nil) {
		t.Error("blank filter should match everything")
	}
}

func TestMatchFilter_ExactMatch(t *testing.T) {
	if !matchFilter(`{"metric":"ransomware_pattern"}`, map[string]interface{}{"metric": "ransomware_pattern"}) {
		t.Error("exact string match should pass")
	}
	if matchFilter(`{"metric":"ransomware_pattern"}`, map[string]interface{}{"metric": "cpu_spike"}) {
		t.Error("mismatched string should fail")
	}
}

func TestMatchFilter_NumericMatch(t *testing.T) {
	// JSON number 1 unmarshals as float64
	if !matchFilter(`{"severity":1}`, map[string]interface{}{"severity": 1}) {
		t.Error("numeric match int should pass")
	}
	if !matchFilter(`{"severity":1}`, map[string]interface{}{"severity": uint(1)}) {
		t.Error("numeric match uint should pass")
	}
	if matchFilter(`{"severity":2}`, map[string]interface{}{"severity": 1}) {
		t.Error("mismatched number should fail")
	}
}

func TestMatchFilter_MissingKey(t *testing.T) {
	if matchFilter(`{"metric":"ransom"}`, map[string]interface{}{"other": "val"}) {
		t.Error("missing key should fail filter")
	}
}

func TestMatchFilter_MultipleKeys(t *testing.T) {
	filter := `{"metric":"ransom","severity":5}`
	ctx := map[string]interface{}{"metric": "ransom", "severity": 5, "node_id": uint(3)}
	if !matchFilter(filter, ctx) {
		t.Error("all AND keys match should pass")
	}
	ctx2 := map[string]interface{}{"metric": "ransom", "severity": 1}
	if matchFilter(filter, ctx2) {
		t.Error("any key mismatch should fail")
	}
}

func TestMatchFilter_InvalidJSON(t *testing.T) {
	if matchFilter(`not json`, map[string]interface{}{"k": "v"}) {
		t.Error("invalid JSON filter should not match")
	}
}

// ---- renderTemplate ----

func TestRenderTemplate_Plain(t *testing.T) {
	result := renderTemplate("hello world", map[string]interface{}{})
	if result != "hello world" {
		t.Errorf("plain text should be unchanged, got %q", result)
	}
}

func TestRenderTemplate_SingleVar(t *testing.T) {
	result := renderTemplate("Policy {{.PolicyID}} paused", map[string]interface{}{"PolicyID": uint(42)})
	if result != "Policy 42 paused" {
		t.Errorf("expected 'Policy 42 paused', got %q", result)
	}
}

func TestRenderTemplate_MissingVar(t *testing.T) {
	result := renderTemplate("{{.Missing}}", map[string]interface{}{})
	if result != "{{.Missing}}" {
		t.Errorf("missing var should be left unchanged, got %q", result)
	}
}

func TestRenderTemplate_MultipleVars(t *testing.T) {
	result := renderTemplate("Task {{.TaskID}} on Node {{.NodeID}}", map[string]interface{}{
		"TaskID": uint(1),
		"NodeID": uint(5),
	})
	if result != "Task 1 on Node 5" {
		t.Errorf("got %q", result)
	}
}

// ---- renderConfig ----

func TestRenderConfig_WithPlaceholder(t *testing.T) {
	config := `{"policy_id":"{{.PolicyID}}"}`
	ctx := map[string]interface{}{"PolicyID": uint(10)}
	result := renderConfig(config, ctx)
	if result != `{"policy_id":"10"}` {
		t.Errorf("got %q", result)
	}
}

// ---- parseConfigUint ----

func TestParseConfigUint(t *testing.T) {
	if v := parseConfigUint(`{"policy_id":42}`, "policy_id"); v != 42 {
		t.Errorf("expected 42, got %d", v)
	}
	if v := parseConfigUint(`{"policy_id":"42"}`, "policy_id"); v != 42 {
		t.Errorf("string '42' should parse, got %d", v)
	}
	if v := parseConfigUint(`{}`, "policy_id"); v != 0 {
		t.Errorf("missing key should return 0, got %d", v)
	}
	if v := parseConfigUint("", "policy_id"); v != 0 {
		t.Errorf("empty string should return 0, got %d", v)
	}
}

// ---- valueMatches ----

func TestValueMatches_Nil(t *testing.T) {
	if !valueMatches(nil, nil) {
		t.Error("nil == nil")
	}
	if valueMatches(nil, "x") {
		t.Error("nil != 'x'")
	}
	if valueMatches("x", nil) {
		t.Error("'x' != nil")
	}
}

func TestValueMatches_FloatTypes(t *testing.T) {
	if !valueMatches(float64(1.0), uint(1)) {
		t.Error("float64(1) == uint(1)")
	}
	if !valueMatches(json.Number("42"), int64(42)) {
		t.Error("json.Number(42) == int64(42)")
	}
}

// ---- Dispatch (integration) ----

func TestDispatch_NoMatchingRules(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	evt := Event{Type: EventBackupFailed, Context: map[string]interface{}{"policy_id": uint(1)}}
	err := d.Dispatch(context.Background(), evt)
	if err != nil {
		t.Fatalf("dispatch should not error on no rules: %v", err)
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 0 {
		t.Errorf("expected 0 execution logs, got %d", len(logs))
	}
}

func TestDispatch_FilterMismatch(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	seedRule(t, db, model.AutomationRule{
		Name:        "FAKE_TEST_RULE_FAIL_FILTER_FOR_TEST_ONLY",
		EventType:   EventBackupFailed,
		EventFilter: `{"metric":"ransomware_pattern"}`,
		ActionType:  ActionSendNotification,
		Enabled:     true,
	})

	evt := Event{Type: EventBackupFailed, Context: map[string]interface{}{"policy_id": uint(1)}}
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 0 {
		t.Errorf("filter mismatch should produce 0 logs, got %d", len(logs))
	}
}

func TestDispatch_DisabledRule(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	seedRule(t, db, model.AutomationRule{
		Name:       "FAKE_TEST_DISABLED_RULE_FOR_TEST_ONLY",
		EventType:  EventBackupFailed,
		ActionType: ActionSendNotification,
		Enabled:    false,
	})

	evt := Event{Type: EventBackupFailed, Context: map[string]interface{}{}}
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 0 {
		t.Errorf("disabled rule should not fire, got %d logs", len(logs))
	}
}

func TestDispatch_PausePolicy(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	seedRule(t, db, model.AutomationRule{
		Name:         "FAKE_TEST_RULE_PAUSE_POLICY_FOR_TEST_ONLY",
		EventType:    EventBackupFailed,
		EventFilter:  `{}`,
		ActionType:   ActionPausePolicy,
		ActionConfig: `{"policy_id":"1"}`,
		Enabled:      true,
	})

	evt := Event{Type: EventBackupFailed, Context: map[string]interface{}{
		"policy_id": uint(1),
		"node_id":   uint(3),
	}}
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Verify policy was paused
	var p model.Policy
	db.First(&p, uint(1))
	if !p.SkipNext {
		t.Error("policy.SkipNext should be true after pause_policy action")
	}

	// Verify execution log
	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 execution log, got %d", len(logs))
	}
	if logs[0].Result != ResultSuccess {
		t.Errorf("expected success, got %s: %s", logs[0].Result, logs[0].Error)
	}
}

func TestDispatch_DisablePolicy(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	seedRule(t, db, model.AutomationRule{
		Name:         "FAKE_TEST_RULE_DISABLE_POLICY_FOR_TEST_ONLY",
		EventType:    EventBackupFailed,
		EventFilter:  `{}`,
		ActionType:   ActionDisablePolicy,
		ActionConfig: `{"policy_id":"1"}`,
		Enabled:      true,
	})

	evt := Event{Type: EventBackupFailed, Context: map[string]interface{}{"policy_id": uint(1)}}
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var p model.Policy
	db.First(&p, uint(1))
	if p.Enabled {
		t.Error("policy.Enabled should be false after disable_policy action")
	}
}

type fakeTaskTriggerer struct {
	runID   uint
	taskIDs []uint
	err     error
}

func (f *fakeTaskTriggerer) TriggerAutomation(taskID uint) (uint, error) {
	f.taskIDs = append(f.taskIDs, taskID)
	return f.runID, f.err
}

func TestDispatch_TriggerTask(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)
	triggerer := &fakeTaskTriggerer{runID: 42}
	d.SetTaskTriggerer(triggerer)

	seedRule(t, db, model.AutomationRule{
		Name:         "FAKE_TEST_RULE_TRIGGER_TASK_FOR_TEST_ONLY",
		EventType:    EventBackupFailed,
		EventFilter:  `{}`,
		ActionType:   ActionTriggerTask,
		ActionConfig: `{"task_id":"1"}`,
		Enabled:      true,
	})

	evt := Event{Type: EventBackupFailed, Context: map[string]interface{}{"node_id": uint(3)}}
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(triggerer.taskIDs) != 1 || triggerer.taskIDs[0] != 1 {
		t.Fatalf("expected runtime trigger for task 1, got %#v", triggerer.taskIDs)
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 execution log, got %d", len(logs))
	}
	if logs[0].Result != ResultSuccess {
		t.Fatalf("expected success log, got %s: %s", logs[0].Result, logs[0].Error)
	}
	var details struct {
		TaskID    uint `json:"task_id"`
		TaskRunID uint `json:"task_run_id"`
	}
	if err := json.Unmarshal([]byte(logs[0].Details), &details); err != nil {
		t.Fatalf("parse trigger details: %v", err)
	}
	if details.TaskRunID != 42 || details.TaskID != 1 {
		t.Fatalf("unexpected trigger details: %#v", details)
	}
}

func TestDispatch_TriggerTaskWithoutRuntimeTriggererLogsError(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	seedRule(t, db, model.AutomationRule{
		Name:         "FAKE_TEST_RULE_TRIGGER_TASK_WITHOUT_RUNTIME_FOR_TEST_ONLY",
		EventType:    EventBackupFailed,
		EventFilter:  `{}`,
		ActionType:   ActionTriggerTask,
		ActionConfig: `{"task_id":"1"}`,
		Enabled:      true,
	})

	evt := Event{Type: EventBackupFailed, Context: map[string]interface{}{"node_id": uint(3)}}
	if err := d.Dispatch(context.Background(), evt); err == nil {
		t.Fatal("dispatch should report the missing runtime triggerer")
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 execution log, got %d", len(logs))
	}
	if logs[0].Result != ResultError {
		t.Fatalf("expected missing triggerer error log, got result=%s", logs[0].Result)
	}
}

func TestDispatch_SendNotification(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	seedRule(t, db, model.AutomationRule{
		Name:         "FAKE_TEST_RULE_NOTIFY_FOR_TEST_ONLY",
		EventType:    EventBackupFailed,
		EventFilter:  `{}`,
		ActionType:   ActionSendNotification,
		ActionConfig: `{"message":"Backup failed for policy {{.PolicyID}} on node {{.NodeID}}"}`,
		Enabled:      true,
	})

	evt := Event{Type: EventBackupFailed, Context: map[string]interface{}{
		"PolicyID": uint(7),
		"NodeID":   uint(3),
	}}
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Result != ResultSuccess {
		t.Errorf("expected success, got %s", logs[0].Result)
	}

	var details map[string]interface{}
	_ = json.Unmarshal([]byte(logs[0].Details), &details)
	msg, _ := details["message"].(string)
	if msg != "Backup failed for policy 7 on node 3" {
		t.Errorf("unexpected message: %s", msg)
	}
}

func TestDispatch_PausePolicyTemplateRender(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	// Use template variable in action_config
	seedRule(t, db, model.AutomationRule{
		Name:         "FAKE_TEST_RULE_TEMPLATE_FOR_TEST_ONLY",
		EventType:    EventAnomalyDetected,
		EventFilter:  `{"metric":"ransomware_pattern"}`,
		ActionType:   ActionPausePolicy,
		ActionConfig: `{"policy_id":"{{.PolicyID}}"}`,
		Enabled:      true,
	})

	evt := Event{Type: EventAnomalyDetected, Context: map[string]interface{}{
		"metric":   "ransomware_pattern",
		"PolicyID": uint(1),
	}}
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var p model.Policy
	db.First(&p, uint(1))
	if !p.SkipNext {
		t.Error("template-rendered policy_id should result in SkipNext=true")
	}
}

func TestDispatch_PausePolicyNonexistentPolicy(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	seedRule(t, db, model.AutomationRule{
		Name:         "FAKE_TEST_RULE_BAD_POLICY_FOR_TEST_ONLY",
		EventType:    EventBackupFailed,
		EventFilter:  `{}`,
		ActionType:   ActionPausePolicy,
		ActionConfig: `{"policy_id":"9999"}`,
		Enabled:      true,
	})

	evt := Event{Type: EventBackupFailed, Context: map[string]interface{}{}}
	if err := d.Dispatch(context.Background(), evt); err == nil {
		t.Fatal("dispatch should report the nonexistent policy")
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 execution log, got %d", len(logs))
	}
	if logs[0].Result != ResultError {
		t.Fatalf("expected error for nonexistent policy, got %s", logs[0].Result)
	}
}

func TestDispatch_MultipleRulesSameEvent(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	seedRule(t, db, model.AutomationRule{
		Name:        "FAKE_TEST_RULE_A_1_FOR_TEST_ONLY",
		EventType:   EventBackupFailed,
		EventFilter: `{}`,
		ActionType:  ActionSendNotification,
		Enabled:     true,
	})
	seedRule(t, db, model.AutomationRule{
		Name:         "FAKE_TEST_RULE_A_2_FOR_TEST_ONLY",
		EventType:    EventBackupFailed,
		EventFilter:  `{}`,
		ActionType:   ActionPausePolicy,
		ActionConfig: `{"policy_id":"1"}`,
		Enabled:      true,
	})

	evt := Event{Type: EventBackupFailed, Context: map[string]interface{}{"policy_id": uint(1)}}
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(logs))
	}
}

func TestDispatch_EventTypeMismatch(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	seedRule(t, db, model.AutomationRule{
		Name:       "FAKE_TEST_RULE_MISMATCH_FOR_TEST_ONLY",
		EventType:  EventBackupFailed,
		ActionType: ActionSendNotification,
		Enabled:    true,
	})

	evt := Event{Type: EventBackupSucceeded, Context: map[string]interface{}{}}
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 0 {
		t.Errorf("event type mismatch should produce 0 logs, got %d", len(logs))
	}
}

// These controllers exercise the actual dispatcher call boundaries without
// interpreting private error text as a stable reason.
type metadataPolicyController struct {
	db      *gorm.DB
	err     error
	targets []uint
}

func (c *metadataPolicyController) PausePolicyNext(_ context.Context, policyID uint) error {
	c.targets = append(c.targets, policyID)
	if c.db != nil {
		if err := c.db.Model(&model.Policy{}).Where("id = ?", policyID).Update("skip_next", true).Error; err != nil {
			return err
		}
	}
	return c.err
}

func (c *metadataPolicyController) DisablePolicy(_ context.Context, policyID uint) error {
	c.targets = append(c.targets, policyID)
	if c.db != nil {
		if err := c.db.Model(&model.Policy{}).Where("id = ?", policyID).Update("enabled", false).Error; err != nil {
			return err
		}
	}
	return c.err
}

func requireExecutionMetadata(t *testing.T, entry model.AutomationRuleLog, stage, reason string) *SafeExecutionMetadata {
	t.Helper()
	meta := ReadSafeExecutionMetadata(entry.Details, entry.EventType, entry.ActionType, entry.Result)
	if meta == nil || meta.Version != 1 || meta.Stage != stage || meta.ReasonCode != reason {
		t.Fatalf("metadata=%+v, want %s/%s (result=%s)", meta, stage, reason, entry.Result)
	}
	return meta
}

func TestDispatchSafeExecutionActions(t *testing.T) {
	for _, action := range []string{ActionPausePolicy, ActionDisablePolicy, ActionTriggerTask, ActionSendNotification} {
		t.Run(action, func(t *testing.T) {
			db := setupDispatcherTestDB(t)
			d := NewDispatcher(db)
			controller := &metadataPolicyController{}
			d.SetPolicyController(controller)
			triggerer := &fakeTaskTriggerer{runID: 202}
			d.SetTaskTriggerer(triggerer)
			config := `{"policy_id":"{{.target}}"}`
			stage := ExecutionStagePolicyUpdated
			switch action {
			case ActionTriggerTask:
				config, stage = `{"task_id":"{{.target}}"}`, ExecutionStageTaskTriggerAccepted
			case ActionSendNotification:
				config, stage = `{"message":"private {{.secret}}"}`, ExecutionStageNotificationRecorded
			}
			seedRule(t, db, model.AutomationRule{
				Name: "metadata-action", EventType: EventBackupFailed, EventFilter: "{}",
				ActionType: action, ActionConfig: config, Enabled: true,
			})
			event := Event{Type: EventBackupFailed, Context: map[string]interface{}{
				"node_id": uint(9), "policy_id": uint(77), "task_id": uint(101), "task_run_id": uint(102),
				"target": uint(2), "secret": "FAKE_PRIVATE_NOTIFICATION_SENTINEL",
			}}
			if err := d.Dispatch(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			var entries []model.AutomationRuleLog
			if err := db.Find(&entries).Error; err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Result != ResultSuccess {
				t.Fatalf("execution entries=%+v", entries)
			}
			meta := requireExecutionMetadata(t, entries[0], stage, "")
			if meta.SourcePolicyID != 77 || meta.SourceNodeID != 9 || meta.SourceTaskID != 101 || meta.SourceTaskRunID != 102 {
				t.Fatalf("lost or confused source associations: %+v", meta)
			}
			var details actionDetails
			if err := json.Unmarshal([]byte(entries[0].Details), &details); err != nil {
				t.Fatal(err)
			}
			switch action {
			case ActionPausePolicy, ActionDisablePolicy:
				if meta.TargetPolicyID != 2 || len(controller.targets) != 1 || controller.targets[0] != 2 {
					t.Fatalf("policy target confused with source: meta=%+v calls=%v", meta, controller.targets)
				}
			case ActionTriggerTask:
				if details.TaskID != 2 || details.TaskRunID != 202 || meta.TargetPolicyID != 0 {
					t.Fatalf("trigger target confused with source: %+v meta=%+v", details, meta)
				}
			case ActionSendNotification:
				if details.Message == nil || !strings.Contains(*details.Message, "FAKE_PRIVATE_NOTIFICATION_SENTINEL") || meta.TargetPolicyID != 0 {
					t.Fatalf("notification must record private message, not a target: %+v", details)
				}
			}
		})
	}
}

func TestDispatchSafeExecutionFailures(t *testing.T) {
	for _, action := range []string{ActionPausePolicy, ActionDisablePolicy, ActionTriggerTask} {
		for _, tc := range []struct {
			name   string
			err    error
			reason string
		}{
			{"typed_cancel", context.Canceled, ReasonContextCanceled},
			{"wrapped_cancel", fmt.Errorf("private diagnostic: %w", context.Canceled), ReasonContextCanceled},
			{"wrapped_deadline", fmt.Errorf("private diagnostic: %w", context.DeadlineExceeded), ReasonDeadlineExceeded},
			{"typed_missing", gorm.ErrRecordNotFound, ReasonTargetNotFound},
			{"wrapped_missing", fmt.Errorf("private diagnostic: %w", gorm.ErrRecordNotFound), ReasonTargetNotFound},
			{"generic", errors.New("FAKE_PRIVATE_DIAGNOSTIC_TOKEN"), ReasonActionFailed},
			{"error_text_is_not_type", errors.New("context canceled: record not found"), ReasonActionFailed},
			{"cancel_precedes_deadline", errors.Join(context.DeadlineExceeded, context.Canceled, gorm.ErrRecordNotFound), ReasonContextCanceled},
			{"deadline_precedes_missing", errors.Join(gorm.ErrRecordNotFound, context.DeadlineExceeded), ReasonDeadlineExceeded},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				db := setupDispatcherTestDB(t)
				d := NewDispatcher(db)
				d.SetPolicyController(&metadataPolicyController{err: tc.err})
				d.SetTaskTriggerer(&fakeTaskTriggerer{err: tc.err})
				config := `{"policy_id":1}`
				if action == ActionTriggerTask {
					config = `{"task_id":1}`
				}
				seedRule(t, db, model.AutomationRule{Name: "failure", EventType: EventBackupFailed, EventFilter: "{}", ActionType: action, ActionConfig: config, Enabled: true})
				if err := d.Dispatch(context.Background(), Event{Type: EventBackupFailed}); err == nil {
					t.Fatal("action failure was acknowledged")
				}
				var entry model.AutomationRuleLog
				if err := db.First(&entry).Error; err != nil {
					t.Fatal(err)
				}
				meta := requireExecutionMetadata(t, entry, ExecutionStageActionExecution, tc.reason)
				if meta.TargetPolicyID != 0 || entry.Error == "" {
					t.Fatalf("failure invented successful target or lost private error: %+v", entry)
				}
			})
		}
	}
}

func TestDispatchSafeExecutionValidationAndCancellation(t *testing.T) {
	d := NewDispatcher(nil)
	for _, action := range []string{ActionPausePolicy, ActionDisablePolicy, ActionTriggerTask} {
		t.Run(action, func(t *testing.T) {
			entry := d.executeAction(context.Background(), model.AutomationRule{ActionType: action, ActionConfig: "{}"}, Event{Type: EventBackupFailed})
			requireExecutionMetadata(t, entry, ExecutionStageActionValidation, ReasonInvalidTarget)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			controller := &metadataPolicyController{}
			triggerer := &fakeTaskTriggerer{}
			d.SetPolicyController(controller)
			d.SetTaskTriggerer(triggerer)
			config := `{"policy_id":1,"task_id":1}`
			entry = d.executeAction(ctx, model.AutomationRule{ActionType: action, ActionConfig: config}, Event{Type: EventBackupFailed})
			requireExecutionMetadata(t, entry, ExecutionStageActionExecution, ReasonContextCanceled)
			if len(controller.targets) != 0 || len(triggerer.taskIDs) != 0 {
				t.Fatal("canceled action called executor")
			}
		})
	}
	d.SetTaskTriggerer(nil)
	entry := d.executeAction(context.Background(), model.AutomationRule{ActionType: ActionTriggerTask, ActionConfig: `{"task_id":1}`}, Event{Type: EventBackupFailed})
	requireExecutionMetadata(t, entry, ExecutionStageActionValidation, ReasonExecutorUnavailable)
	entry = d.executeAction(context.Background(), model.AutomationRule{ActionType: "future_action"}, Event{Type: EventBackupFailed})
	requireExecutionMetadata(t, entry, ExecutionStageActionValidation, ReasonUnsupportedAction)
}

func TestDispatchSafeExecutionStrictTargets(t *testing.T) {
	for _, action := range []string{ActionPausePolicy, ActionDisablePolicy, ActionTriggerTask} {
		for _, tc := range []struct {
			name  string
			value string
			known bool
		}{
			{"integer", "2", true}, {"decimal_string", `"002"`, true},
			{"fraction_executed", "2.75", false}, {"integral_float_executed", "2.0", false},
			{"exponent_executed", "2e0", false},
			{"unsafe_integer_executed", "9007199254740992", false},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				d := NewDispatcher(nil)
				controller := &metadataPolicyController{}
				triggerer := &fakeTaskTriggerer{runID: 202}
				d.SetPolicyController(controller)
				d.SetTaskTriggerer(triggerer)
				key, stage := "policy_id", ExecutionStagePolicyUpdated
				if action == ActionTriggerTask {
					key, stage = "task_id", ExecutionStageTaskTriggerAccepted
				}
				config := fmt.Sprintf(`{"%s":%s}`, key, tc.value)
				entry := d.executeAction(context.Background(), model.AutomationRule{ActionType: action, ActionConfig: config}, Event{Type: EventBackupFailed})
				meta := requireExecutionMetadata(t, entry, stage, "")
				var details actionDetails
				if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
					t.Fatal(err)
				}
				actual := parseConfigUint(config, key)
				if action == ActionTriggerTask {
					if len(triggerer.taskIDs) != 1 || triggerer.taskIDs[0] != actual {
						t.Fatalf("changed legacy admission: %v want %d", triggerer.taskIDs, actual)
					}
					if (details.TaskID != 0 && details.TaskRunID != 0) != tc.known || (!tc.known && (details.TaskID != 0 || details.TaskRunID != 0)) {
						t.Fatalf("invented or incomplete trigger target: %+v", details)
					}
				} else {
					if len(controller.targets) != 1 || controller.targets[0] != actual || (meta.TargetPolicyID != 0) != tc.known {
						t.Fatalf("changed admission or invented target: calls=%v meta=%+v", controller.targets, meta)
					}
				}
			})
		}
	}
	for _, runID := range []uint{0, uint(9007199254740992)} {
		d := NewDispatcher(nil)
		d.SetTaskTriggerer(&fakeTaskTriggerer{runID: runID})
		entry := d.executeAction(context.Background(), model.AutomationRule{ActionType: ActionTriggerTask, ActionConfig: `{"task_id":2}`}, Event{})
		requireExecutionMetadata(t, entry, ExecutionStageTaskTriggerAccepted, "")
		var details actionDetails
		if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
			t.Fatal(err)
		}
		if details.TaskID != 0 || details.TaskRunID != 0 {
			t.Fatalf("unsafe run left a partial target: %+v", details)
		}
	}
}

func TestDispatchSafeExecutionSourceWhitelist(t *testing.T) {
	valid := []interface{}{uint(7), uint64(7), int(7), int64(7), float64(7), json.Number("7"), uint64(9007199254740991)}
	invalid := []interface{}{nil, "7", int(-1), int64(-1), 0, float64(1.5), math.NaN(), math.Inf(1), json.Number("1e0"), json.Number("1.0"), json.Number("01"), uint64(9007199254740992), float64(9007199254740992), uint32(7)}
	d := NewDispatcher(nil)
	rule := model.AutomationRule{ActionType: ActionSendNotification, ActionConfig: `{"message":"private"}`}
	for index, value := range append(valid, invalid...) {
		t.Run(fmt.Sprintf("%T_%v", value, value), func(t *testing.T) {
			evt := Event{Type: EventBackupSucceeded, Context: map[string]interface{}{"node_id": value, "policy_id": value, "task_id": value, "task_run_id": value}}
			entry := d.executeAction(context.Background(), rule, evt)
			var details actionDetails
			if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
				t.Fatal(err)
			}
			meta := details.SafeExecution
			var want uint
			if index < len(valid) {
				want = 7
				if index == len(valid)-1 {
					want = 9007199254740991
				}
			}
			if meta.SourceNodeID != want || meta.SourcePolicyID != want || meta.SourceTaskID != want || meta.SourceTaskRunID != want {
				t.Fatalf("source projection=%+v want %d", meta, want)
			}
		})
	}
	for _, eventType := range []string{EventBackupSucceeded, EventBackupFailed, EventAnomalyDetected, EventDrillFailed, "future_event"} {
		t.Run(eventType, func(t *testing.T) {
			evt := Event{Type: eventType, Context: map[string]interface{}{"node_id": uint(9), "policy_id": uint(77), "task_id": uint(101), "task_run_id": uint(102)}}
			entry := d.executeAction(context.Background(), rule, evt)
			var raw actionDetails
			if err := json.Unmarshal([]byte(entry.Details), &raw); err != nil {
				t.Fatal(err)
			}
			meta := raw.SafeExecution
			backup := eventType == EventBackupSucceeded || eventType == EventBackupFailed
			if (meta.SourceNodeID != 0) != (backup || eventType == EventAnomalyDetected) || (meta.SourcePolicyID != 0) != (backup || eventType == EventDrillFailed) || (meta.SourceTaskID != 0) != backup || (meta.SourceTaskRunID != 0) != backup {
				t.Fatalf("event whitelist leaked associations: %+v", meta)
			}
			evt.Context["task_run_id"] = "102"
			entry = d.executeAction(context.Background(), rule, evt)
			meta = requireExecutionMetadata(t, entry, ExecutionStageNotificationRecorded, "")
			if meta.SourceTaskID != 0 || meta.SourceTaskRunID != 0 {
				t.Fatalf("partial source pair: %+v", meta)
			}
		})
	}
}

func TestDispatchSafeExecutionLegacyPartialPolicyFailure(t *testing.T) {
	for _, action := range []string{ActionPausePolicy, ActionDisablePolicy} {
		t.Run(action, func(t *testing.T) {
			db := setupDispatcherTestDB(t)
			d := NewDispatcher(db)
			d.SetPolicyController(&metadataPolicyController{db: db, err: errors.New("failure after durable policy change")})
			seedRule(t, db, model.AutomationRule{Name: "partial", EventType: EventBackupFailed, EventFilter: "{}", ActionType: action, ActionConfig: `{"policy_id":1}`, Enabled: true})
			if err := d.Dispatch(context.Background(), Event{Type: EventBackupFailed}); err == nil {
				t.Fatal("partial-effect error was suppressed")
			}
			var policy model.Policy
			if err := db.First(&policy, uint(1)).Error; err != nil {
				t.Fatal(err)
			}
			if (action == ActionPausePolicy && !policy.SkipNext) || (action == ActionDisablePolicy && policy.Enabled) {
				t.Fatal("fixture did not apply policy before error")
			}
			var entry model.AutomationRuleLog
			if err := db.First(&entry).Error; err != nil {
				t.Fatal(err)
			}
			meta := requireExecutionMetadata(t, entry, ExecutionStageActionExecution, ReasonActionFailed)
			if meta.TargetPolicyID != 0 {
				t.Fatal("failed call published a successful target")
			}
		})
	}
}
