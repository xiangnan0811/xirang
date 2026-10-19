package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"xirang/backend/internal/automation"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task"
)

func TestAutomationRuleLogsDispatcherSQLite(t *testing.T) {
	testAutomationHistoryDispatcher(t, setupAutomationRuleRBACFixture(t))
}

func TestAutomationRuleLogsDispatcherPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN required for PostgreSQL automation history acceptance")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("PostgreSQL URL required: %v", err)
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = baseSQL.Close() })
	schema := fmt.Sprintf("automation_history_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := base.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	testAutomationHistoryDispatcher(t, automationRuleRBACFixtureWithDB(t, db))
}

type automationHistoryResponse struct {
	ID              uint    `json:"id"`
	RuleID          uint    `json:"rule_id"`
	ActionType      string  `json:"action_type"`
	Result          string  `json:"result"`
	ErrorCode       *string `json:"error_code"`
	TaskID          *uint   `json:"target_task_id"`
	TaskRunID       *uint   `json:"target_task_run_id"`
	MetadataVersion *int    `json:"metadata_version"`
	ExecutionStage  string  `json:"execution_stage"`
	ReasonCode      *string `json:"reason_code"`
	SourceNodeID    *uint   `json:"source_node_id"`
	SourcePolicyID  *uint   `json:"source_policy_id"`
	SourceTaskID    *uint   `json:"source_task_id"`
	SourceTaskRunID *uint   `json:"source_task_run_id"`
	TargetPolicyID  *uint   `json:"target_policy_id"`
}

func testAutomationHistoryDispatcher(t *testing.T, fx automationRuleRBACTestFixture) {
	t.Helper()
	db := fx.db
	if err := db.AutoMigrate(
		&model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{}, &model.TaskRunEffect{},
		&model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{},
	); err != nil {
		t.Fatal(err)
	}
	manager := task.NewManager(db, nil, nil, nil, nil, nil, 8, 90)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	dispatcher := automation.NewDispatcher(db)
	dispatcher.SetTaskTriggerer(manager)

	legacy := model.AutomationRule{Name: "legacy-failure", EventType: automation.EventAnomalyDetected, EventFilter: "{}", ActionType: automation.ActionPausePolicy, ActionConfig: `{"policy_id":"FAKE_PASSWORD_FOR_TEST_ONLY","endpoint":"FAKE_ENDPOINT_FOR_TEST_ONLY"}`, Enabled: true}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(context.Background(), automation.Event{Type: automation.EventAnomalyDetected}); err == nil {
		t.Fatal("invalid legacy action should fail")
	}
	node := model.Node{Name: "history-node", Host: "127.0.0.1", Username: "test", BackupDir: "history-backup"}
	sourcePolicy := model.Policy{Name: "history-source-policy", Enabled: true}
	targetPolicy := model.Policy{Name: "history-target-policy", Enabled: true, CronSpec: "@every 1h"}
	for _, value := range []any{&node, &sourcePolicy, &targetPolicy} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	sourceTask := model.Task{
		Name: "history-source-task", NodeID: node.ID, PolicyID: &sourcePolicy.ID,
		ExecutorType: "local", Status: "success", Enabled: true,
		RsyncSource: t.TempDir() + "/", RsyncTarget: t.TempDir(),
	}
	targetTask := model.Task{
		Name: "history-target-task", NodeID: node.ID, ExecutorType: "local", Status: "pending", Enabled: true,
		RsyncSource: t.TempDir() + "/", RsyncTarget: t.TempDir(),
	}
	policyTask := model.Task{
		Name: "history-policy-task", NodeID: node.ID, PolicyID: &targetPolicy.ID, Source: "policy",
		ExecutorType: "local", Status: "pending", Enabled: true, CronSpec: "@every 1h",
		RsyncSource: t.TempDir() + "/", RsyncTarget: t.TempDir(),
	}
	for _, value := range []any{&sourceTask, &targetTask, &policyTask} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	sourceRun := model.TaskRun{TaskID: sourceTask.ID, NodeIDSnapshot: node.ID, TriggerType: "manual", Status: model.TaskRunStatusSuccess}
	if err := db.Create(&sourceRun).Error; err != nil {
		t.Fatal(err)
	}
	filter := fmt.Sprintf(`{"task_id":%d,"task_run_id":%d}`, sourceTask.ID, sourceRun.ID)
	rules := []model.AutomationRule{
		{Name: "durable-notification", ActionType: automation.ActionSendNotification, ActionConfig: `{"message":"FAKE_NOTIFICATION_FOR_TEST_ONLY"}`},
		{Name: "durable-trigger", ActionType: automation.ActionTriggerTask, ActionConfig: fmt.Sprintf(`{"task_id":"%d"}`, targetTask.ID)},
		{Name: "durable-pause", ActionType: automation.ActionPausePolicy, ActionConfig: fmt.Sprintf(`{"policy_id":%d}`, targetPolicy.ID)},
		{Name: "durable-disable", ActionType: automation.ActionDisablePolicy, ActionConfig: fmt.Sprintf(`{"policy_id":"%d"}`, targetPolicy.ID)},
	}
	for i := range rules {
		rules[i].EventType, rules[i].EventFilter, rules[i].Enabled = automation.EventBackupSucceeded, filter, true
		if err := db.Create(&rules[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	payload, err := json.Marshal(struct {
		EventType string          `json:"event_type"`
		Context   map[string]uint `json:"context"`
	}{
		EventType: automation.EventBackupSucceeded,
		Context: map[string]uint{
			"node_id": node.ID, "policy_id": sourcePolicy.ID, "task_id": sourceTask.ID, "task_run_id": sourceRun.ID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease := time.Now().UTC().Add(time.Hour)
	parent := model.TaskRunEffect{
		TaskRunID: sourceRun.ID, EffectKey: "automation:history", EffectType: model.TaskRunEffectTypeAutomation,
		Payload: string(payload), Status: model.TaskRunEffectStatusRunning, ClaimedBy: "history-owner", ClaimLeaseUntil: &lease,
	}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), automation.Event{}, parent); err != nil {
		t.Fatal(err)
	}
	var childCount int64
	if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND effect_type = ?", sourceRun.ID, model.TaskRunEffectTypeAutomationRule).Count(&childCount).Error; err != nil {
		t.Fatal(err)
	}
	if childCount != int64(len(rules)) {
		t.Fatalf("fanout children=%d, want %d", childCount, len(rules))
	}
	// Consume in fixture order so pause precedes disable independently of the
	// database's fanout query order.
	children := make([]model.TaskRunEffect, len(rules))
	for i, rule := range rules {
		if err := db.Where("task_run_id = ? AND effect_key = ?", sourceRun.ID, fmt.Sprintf("%s:rule:%d", parent.EffectKey, rule.ID)).First(&children[i]).Error; err != nil {
			t.Fatal(err)
		}
		claim := db.Model(&model.TaskRunEffect{}).Where("id = ? AND status = ?", children[i].ID, model.TaskRunEffectStatusPending).
			Updates(map[string]any{"status": model.TaskRunEffectStatusRunning, "claimed_by": "history-owner", "claim_lease_until": &lease})
		if claim.Error != nil || claim.RowsAffected != 1 {
			t.Fatalf("claim child: rows=%d err=%v", claim.RowsAffected, claim.Error)
		}
		if err := db.First(&children[i], children[i].ID).Error; err != nil {
			t.Fatal(err)
		}
	}

	readHistory := func(query string) []automationHistoryResponse {
		t.Helper()
		resp := performAutomationRuleRBACRequest(t, fx.router, http.MethodGet, "/api/v1/automation-rule-logs"+query, fx.tokens["admin"], "")
		if resp.Code != http.StatusOK || strings.Contains(resp.Body.String(), "FAKE_") {
			t.Fatalf("unsafe history: HTTP=%d %s", resp.Code, resp.Body.String())
		}
		var envelope struct {
			Data  []automationHistoryResponse `json:"data"`
			Total int64                       `json:"total"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		var rawEnvelope struct {
			Data []map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &rawEnvelope); err != nil {
			t.Fatal(err)
		}
		for _, row := range rawEnvelope.Data {
			for _, privateKey := range []string{"details", "error", "message", "safe_execution"} {
				if _, exposed := row[privateKey]; exposed {
					t.Fatalf("private field %q exposed in history row", privateKey)
				}
			}
		}
		if int64(len(envelope.Data)) != envelope.Total {
			t.Fatalf("unexpected total: %s", resp.Body.String())
		}
		return envelope.Data
	}
	legacyRows := readHistory("?result=error")
	if len(legacyRows) != 1 {
		t.Fatalf("legacy failure missing: %+v", legacyRows)
	}
	legacyRow := legacyRows[0]
	if legacyRow.RuleID != legacy.ID || legacyRow.MetadataVersion == nil || *legacyRow.MetadataVersion != automation.SafeExecutionMetadataVersion ||
		legacyRow.ExecutionStage != automation.ExecutionStageActionValidation || legacyRow.ReasonCode == nil || *legacyRow.ReasonCode != automation.ReasonInvalidTarget ||
		legacyRow.ErrorCode == nil || *legacyRow.ErrorCode != automation.ReasonActionFailed || legacyRow.TargetPolicyID != nil ||
		legacyRow.TaskID != nil || legacyRow.TaskRunID != nil {
		t.Fatalf("legacy invalid target HTTP contract: %+v", legacyRow)
	}

	// Inject at the real Create boundary after each action mutates its transaction.
	// In particular the trigger must have reserved a real Run before logging fails.
	mutatedBeforeLogFailure := make(map[string]bool)
	if err := db.Callback().Create().Before("gorm:create").Register("history_log_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "automation_rule_logs" {
			entry, ok := tx.Statement.Dest.(*model.AutomationRuleLog)
			if !ok {
				_ = tx.AddError(errors.New("unexpected automation log Create destination"))
				return
			}
			probe := tx.Session(&gorm.Session{NewDB: true})
			switch entry.ActionType {
			case automation.ActionTriggerTask:
				var runs []model.TaskRun
				if err := probe.Where("task_id = ?", targetTask.ID).Find(&runs).Error; err != nil {
					_ = tx.AddError(err)
				} else {
					mutatedBeforeLogFailure[entry.ActionType] = len(runs) == 1 && runs[0].Status == model.TaskRunStatusPending
				}
			case automation.ActionPausePolicy, automation.ActionDisablePolicy:
				var policy model.Policy
				var generated model.Task
				if err := probe.First(&policy, targetPolicy.ID).Error; err != nil {
					_ = tx.AddError(err)
				} else if err := probe.First(&generated, policyTask.ID).Error; err != nil {
					_ = tx.AddError(err)
				} else if entry.ActionType == automation.ActionPausePolicy {
					mutatedBeforeLogFailure[entry.ActionType] = policy.SkipNext && generated.SkipNext
				} else {
					mutatedBeforeLogFailure[entry.ActionType] = !policy.Enabled && generated.CronSpec == ""
				}
			case automation.ActionSendNotification:
				mutatedBeforeLogFailure[entry.ActionType] = strings.Contains(entry.Details, "FAKE_NOTIFICATION_FOR_TEST_ONLY")
			}
			_ = tx.AddError(errors.New("FAKE_LOG_WRITE_FAILURE_FOR_TEST_ONLY"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove("history_log_failure") })
	for _, child := range children {
		if err := dispatcher.DispatchTaskRunEffect(context.Background(), automation.Event{}, child); err == nil {
			t.Fatal("durable log write failure must roll back")
		}
		var current model.TaskRunEffect
		if err := db.First(&current, child.ID).Error; err != nil {
			t.Fatal(err)
		}
		if current.Status != model.TaskRunEffectStatusRunning || current.ClaimedBy != child.ClaimedBy {
			t.Fatalf("failed transaction acknowledged/lost effect: %+v", current)
		}
	}
	for _, rule := range rules {
		if !mutatedBeforeLogFailure[rule.ActionType] {
			t.Fatalf("%s did not reach the real log Create boundary after its action", rule.ActionType)
		}
	}
	var reservedCount, succeededChildren int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", targetTask.ID).Count(&reservedCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND effect_type = ? AND status = ?", sourceRun.ID, model.TaskRunEffectTypeAutomationRule, model.TaskRunEffectStatusSucceeded).Count(&succeededChildren).Error; err != nil {
		t.Fatal(err)
	}
	var rolledBackPolicy model.Policy
	var rolledBackTask model.Task
	if err := db.First(&rolledBackPolicy, targetPolicy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&rolledBackTask, policyTask.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reservedCount != 0 || succeededChildren != 0 || !rolledBackPolicy.Enabled || rolledBackPolicy.SkipNext ||
		rolledBackTask.SkipNext || rolledBackTask.CronSpec != policyTask.CronSpec || !rolledBackTask.Enabled {
		t.Fatalf("failed log leaked action state: runs=%d succeeded=%d policy=%+v task=%+v", reservedCount, succeededChildren, rolledBackPolicy, rolledBackTask)
	}
	if rows := readHistory("?result=success"); len(rows) != 0 {
		t.Fatalf("rolled-back action published history: %+v", rows)
	}
	if err := db.Callback().Create().Remove("history_log_failure"); err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if err := dispatcher.DispatchTaskRunEffect(context.Background(), automation.Event{}, child); err != nil {
			t.Fatal(err)
		}
		// Replay the original claimed snapshot: the persisted succeeded marker
		// must win without repeating a mutation, reservation, or log.
		if err := dispatcher.DispatchTaskRunEffect(context.Background(), automation.Event{}, child); err != nil {
			t.Fatal(err)
		}
		var current model.TaskRunEffect
		if err := db.First(&current, child.ID).Error; err != nil {
			t.Fatal(err)
		}
		if current.Status != model.TaskRunEffectStatusSucceeded {
			t.Fatalf("committed log without effect acknowledgement: %+v", current)
		}
	}
	var targetRun model.TaskRun
	if err := db.Where("task_id = ? AND trigger_type = ?", targetTask.ID, "auto").First(&targetRun).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", targetTask.ID).Count(&reservedCount).Error; err != nil {
		t.Fatal(err)
	}
	if reservedCount != 1 || targetRun.ID == sourceRun.ID || targetRun.TaskID == sourceTask.ID || targetRun.Status != model.TaskRunStatusPending {
		t.Fatalf("target reservation not distinct/exactly once: count=%d run=%+v", reservedCount, targetRun)
	}
	var committedPolicy model.Policy
	var committedTask model.Task
	if err := db.First(&committedPolicy, targetPolicy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&committedTask, policyTask.ID).Error; err != nil {
		t.Fatal(err)
	}
	if committedPolicy.Enabled || !committedPolicy.SkipNext || committedTask.CronSpec != "" || !committedTask.SkipNext || !committedTask.Enabled {
		t.Fatalf("retry did not commit policy controls: policy=%+v task=%+v", committedPolicy, committedTask)
	}
	assertID := func(label string, got *uint, want uint) {
		t.Helper()
		if got == nil || *got != want {
			t.Fatalf("%s=%v, want %d", label, got, want)
		}
	}
	rows := readHistory("?result=success")
	if len(rows) != len(rules) {
		t.Fatalf("retry/replay logs=%d, want %d", len(rows), len(rules))
	}
	seenRules := make(map[uint]bool)
	for _, row := range rows {
		if seenRules[row.RuleID] {
			t.Fatalf("replay duplicated rule log: %+v", row)
		}
		seenRules[row.RuleID] = true
		if row.MetadataVersion == nil || *row.MetadataVersion != automation.SafeExecutionMetadataVersion || row.ReasonCode != nil || row.Result != automation.ResultSuccess {
			t.Fatalf("unsafe success metadata: %+v", row)
		}
		assertID("source node", row.SourceNodeID, node.ID)
		assertID("source policy", row.SourcePolicyID, sourcePolicy.ID)
		assertID("source task", row.SourceTaskID, sourceTask.ID)
		assertID("source run", row.SourceTaskRunID, sourceRun.ID)
		switch row.ActionType {
		case automation.ActionTriggerTask:
			if row.ExecutionStage != automation.ExecutionStageTaskRunReserved || row.TargetPolicyID != nil {
				t.Fatalf("trigger boundary: %+v", row)
			}
			assertID("target task", row.TaskID, targetTask.ID)
			assertID("target run", row.TaskRunID, targetRun.ID)
		case automation.ActionPausePolicy, automation.ActionDisablePolicy:
			if row.ExecutionStage != automation.ExecutionStagePolicyUpdated || row.TaskID != nil || row.TaskRunID != nil {
				t.Fatalf("policy boundary: %+v", row)
			}
			assertID("target policy", row.TargetPolicyID, targetPolicy.ID)
		case automation.ActionSendNotification:
			if row.ExecutionStage != automation.ExecutionStageNotificationRecorded || row.TargetPolicyID != nil || row.TaskID != nil || row.TaskRunID != nil {
				t.Fatalf("notification must only be recorded: %+v", row)
			}
		default:
			t.Fatalf("unexpected action: %+v", row)
		}
	}
	for _, rule := range rules {
		if !seenRules[rule.ID] {
			t.Fatalf("missing committed rule %d", rule.ID)
		}
		if err := db.Delete(&rule).Error; err != nil {
			t.Fatal(err)
		}
		retained := readHistory(fmt.Sprintf("?rule_id=%d", rule.ID))
		if len(retained) != 1 || retained[0].RuleID != rule.ID || retained[0].SourceTaskRunID == nil || *retained[0].SourceTaskRunID != sourceRun.ID {
			t.Fatalf("deleted-rule history lost: %+v", retained)
		}
	}
}
