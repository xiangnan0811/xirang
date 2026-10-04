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

func testAutomationHistoryDispatcher(t *testing.T, fx automationRuleRBACTestFixture) {
	t.Helper()
	db := fx.db
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{}, &model.TaskRunEffect{}); err != nil {
		t.Fatal(err)
	}
	legacy := model.AutomationRule{Name: "legacy-failure", EventType: automation.EventAnomalyDetected, EventFilter: "{}", ActionType: automation.ActionPausePolicy, ActionConfig: `{"policy_id":"FAKE_PASSWORD_FOR_TEST_ONLY","endpoint":"FAKE_ENDPOINT_FOR_TEST_ONLY"}`, Enabled: true}
	durable := model.AutomationRule{Name: "durable-record", EventType: automation.EventBackupSucceeded, EventFilter: "{}", ActionType: automation.ActionSendNotification, ActionConfig: `{"message":"FAKE_NOTIFICATION_FOR_TEST_ONLY"}`, Enabled: true}
	for _, value := range []any{&legacy, &durable} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	dispatcher := automation.NewDispatcher(db)
	if err := dispatcher.Dispatch(context.Background(), automation.Event{Type: automation.EventAnomalyDetected}); err == nil {
		t.Fatal("invalid legacy action should fail")
	}
	node := model.Node{Name: "history-node", Host: "127.0.0.1", Username: "test", BackupDir: "history-backup"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	task := model.Task{Name: "history-task", NodeID: node.ID, ExecutorType: "local", Status: "success"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	source := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: node.ID, TriggerType: "manual", Status: model.TaskRunStatusSuccess}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	parent := model.TaskRunEffect{TaskRunID: source.ID, EffectKey: "automation:history", EffectType: model.TaskRunEffectTypeAutomation, Payload: `{"event_type":"backup_succeeded","context":{}}`, Status: model.TaskRunEffectStatusRunning, ClaimedBy: "history-owner"}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), automation.Event{}, parent); err != nil {
		t.Fatal(err)
	}
	var child model.TaskRunEffect
	if err := db.Where("task_run_id = ? AND effect_type = ?", source.ID, model.TaskRunEffectTypeAutomationRule).First(&child).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&child).Updates(map[string]any{"status": model.TaskRunEffectStatusRunning, "claimed_by": "history-owner"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&child, child.ID).Error; err != nil {
		t.Fatal(err)
	}

	readHistory := func(result string) []model.AutomationRuleLog {
		t.Helper()
		resp := performAutomationRuleRBACRequest(t, fx.router, http.MethodGet, "/api/v1/automation-rule-logs"+result, fx.tokens["admin"], "")
		if resp.Code != 200 || strings.Contains(resp.Body.String(), "FAKE_") {
			t.Fatalf("unsafe history: HTTP=%d %s", resp.Code, resp.Body.String())
		}
		var envelope struct {
			Data  []model.AutomationRuleLog `json:"data"`
			Total int64                     `json:"total"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if int64(len(envelope.Data)) != envelope.Total {
			t.Fatalf("unexpected total: %s", resp.Body.String())
		}
		return envelope.Data
	}
	// Fail after the action is evaluated, at the real log write in its transaction.
	if err := db.Callback().Create().Before("gorm:create").Register("history_log_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "automation_rule_logs" {
			_ = tx.AddError(errors.New("FAKE_LOG_WRITE_FAILURE_FOR_TEST_ONLY"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), automation.Event{}, child); err == nil {
		t.Fatal("durable log write failure must roll back")
	}
	if err := db.Callback().Create().Remove("history_log_failure"); err != nil {
		t.Fatal(err)
	}
	if rows := readHistory("?result=success"); len(rows) != 0 {
		t.Fatalf("rolled-back action published history: %+v", rows)
	}
	rows := readHistory("?result=error")
	if len(rows) != 1 || rows[0].RuleID != legacy.ID || rows[0].Result != automation.ResultError {
		t.Fatalf("legacy failure missing: %+v", rows)
	}
	if err := db.First(&child, child.ID).Error; err != nil {
		t.Fatal(err)
	}
	if child.Status != model.TaskRunEffectStatusRunning {
		t.Fatalf("failed transaction acknowledged effect: %s", child.Status)
	}
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), automation.Event{}, child); err != nil {
		t.Fatal(err)
	}
	rows = readHistory("?result=success")
	if len(rows) != 1 || rows[0].RuleID != durable.ID || rows[0].ActionType != automation.ActionSendNotification {
		t.Fatalf("committed action missing: %+v", rows)
	}
	if err := db.Delete(&durable).Error; err != nil {
		t.Fatal(err)
	}
	rows = readHistory(fmt.Sprintf("?rule_id=%d", durable.ID))
	if len(rows) != 1 || rows[0].RuleID != durable.ID {
		t.Fatalf("deleted-rule history lost: %+v", rows)
	}
	if err := db.First(&child, child.ID).Error; err != nil {
		t.Fatal(err)
	}
	if child.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("committed log without effect acknowledgement: %s", child.Status)
	}
}
