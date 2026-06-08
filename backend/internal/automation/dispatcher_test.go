package automation

import (
	"context"
	"encoding/json"
	"fmt"
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
		EventType:    EventNodeOffline,
		EventFilter:  `{}`,
		ActionType:   ActionTriggerTask,
		ActionConfig: `{"task_id":"1"}`,
		Enabled:      true,
	})

	evt := Event{Type: EventNodeOffline, Context: map[string]interface{}{"node_id": uint(3)}}
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
	var details map[string]uint
	if err := json.Unmarshal([]byte(logs[0].Details), &details); err != nil {
		t.Fatalf("parse trigger details: %v", err)
	}
	if details["task_run_id"] != 42 || details["task_id"] != 1 {
		t.Fatalf("unexpected trigger details: %#v", details)
	}
}

func TestDispatch_TriggerTaskWithoutRuntimeTriggererLogsError(t *testing.T) {
	db := setupDispatcherTestDB(t)
	d := NewDispatcher(db)

	seedRule(t, db, model.AutomationRule{
		Name:         "FAKE_TEST_RULE_TRIGGER_TASK_WITHOUT_RUNTIME_FOR_TEST_ONLY",
		EventType:    EventNodeOffline,
		EventFilter:  `{}`,
		ActionType:   ActionTriggerTask,
		ActionConfig: `{"task_id":"1"}`,
		Enabled:      true,
	})

	evt := Event{Type: EventNodeOffline, Context: map[string]interface{}{"node_id": uint(3)}}
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 execution log, got %d", len(logs))
	}
	if logs[0].Result != ResultError || !strings.Contains(logs[0].Error, "任务执行器未初始化") {
		t.Fatalf("expected missing triggerer error log, got result=%s error=%s", logs[0].Result, logs[0].Error)
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
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Result != ResultError {
		t.Errorf("expected error for nonexistent policy, got %s", logs[0].Result)
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

	evt := Event{Type: EventNodeOffline, Context: map[string]interface{}{}}
	if err := d.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var logs []model.AutomationRuleLog
	db.Find(&logs)
	if len(logs) != 0 {
		t.Errorf("event type mismatch should produce 0 logs, got %d", len(logs))
	}
}
