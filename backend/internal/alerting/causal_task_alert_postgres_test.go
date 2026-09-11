package alerting

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCausalTaskAlertsPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openCausalTaskAlertPostgresDB(t, dsn)

	node := model.Node{Name: "causal-alert-pg-node", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	task := model.Task{Name: "causal-alert-pg-task", NodeID: node.ID, Status: "success", Enabled: true}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	dispatcher := NewDispatcher(db, nil, nil)
	firstFailure := createCausalPostgresRun(t, db, task, "manual", model.TaskRunStatusFailed)
	if err := dispatcher.RaiseTaskFailureForRun(task, firstFailure.ID, "first failure"); err != nil {
		t.Fatalf("raise first failure: %v", err)
	}
	intermediateSuccess := createCausalPostgresRun(t, db, task, "manual", model.TaskRunStatusSuccess)
	latestFailure := createCausalPostgresRun(t, db, task, "manual", model.TaskRunStatusFailed)
	if err := dispatcher.RaiseTaskFailureForRun(task, latestFailure.ID, "latest failure"); err != nil {
		t.Fatalf("raise latest failure: %v", err)
	}
	if err := dispatcher.ResolveTaskAlertsForRun(task.ID, intermediateSuccess.ID, "intermediate recovery"); err != nil {
		t.Fatalf("resolve intermediate success: %v", err)
	}

	var alerts []model.Alert
	if err := db.Where("task_id = ?", task.ID).Order("id ASC").Find(&alerts).Error; err != nil {
		t.Fatalf("load causal alerts: %v", err)
	}
	if len(alerts) != 2 {
		t.Fatalf("alerts=%d, want one alert per failure run", len(alerts))
	}
	if alerts[0].Status != "resolved" || alerts[1].Status != "open" ||
		alerts[0].TaskRunID == nil || *alerts[0].TaskRunID != firstFailure.ID ||
		alerts[1].TaskRunID == nil || *alerts[1].TaskRunID != latestFailure.ID {
		t.Fatalf("causal alerts=%+v, want first resolved/latest open", alerts)
	}
}

func createCausalPostgresRun(t *testing.T, db *gorm.DB, task model.Task, trigger, status string) model.TaskRun {
	t.Helper()
	run := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: trigger,
		Status: status, FinishedAt: new(time.Now().UTC()),
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create %s run: %v", trigger, err)
	}
	return run
}

func openCausalTaskAlertPostgresDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	t.Setenv("DATA_ENCRYPTION_KEY", "dGVzdC1rZXktZGF0YS1lbmNyeXB0aW9uLWtleS0zMmIh")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
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
	schema := fmt.Sprintf("xirang_alert_causal_%d", time.Now().UTC().UnixNano())
	if _, err := baseSQL.Exec("CREATE SCHEMA " + schema); err != nil {
		_ = baseSQL.Close()
		t.Fatalf("create isolated PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := baseSQL.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Errorf("drop isolated PostgreSQL schema: %v", err)
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
		t.Fatalf("open isolated PostgreSQL connection: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get isolated PostgreSQL connection: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{},
		&model.Alert{}, &model.AlertDelivery{}, &model.Integration{},
		&model.AlertEscalationEvent{},
	); err != nil {
		t.Fatalf("migrate isolated PostgreSQL causal alert tables: %v", err)
	}
	return db
}

func TestCausalRestoreAlertsPostgres(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openCausalTaskAlertPostgresDB(t, dsn)

	node := model.Node{Name: "causal-restore-pg-node", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	task := model.Task{Name: "causal-restore-pg-task", NodeID: node.ID, Status: "success", Enabled: true}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	dispatcher := NewDispatcher(db, nil, nil)
	restoreFailure := createCausalPostgresRun(t, db, task, "restore", model.TaskRunStatusFailed)
	if err := dispatcher.RaiseTaskFailureForRestoreRun(task, restoreFailure.ID, "restore failure"); err != nil {
		t.Fatalf("raise restore failure: %v", err)
	}
	ordinaryFailure := createCausalPostgresRun(t, db, task, "manual", model.TaskRunStatusFailed)
	if err := dispatcher.RaiseTaskFailureForRun(task, ordinaryFailure.ID, "ordinary failure"); err != nil {
		t.Fatalf("raise ordinary failure: %v", err)
	}
	ordinarySuccess := createCausalPostgresRun(t, db, task, "manual", model.TaskRunStatusSuccess)
	if err := dispatcher.ResolveTaskAlertsForRun(task.ID, ordinarySuccess.ID, "ordinary recovery"); err != nil {
		t.Fatalf("resolve ordinary recovery: %v", err)
	}
	restoreWarning := createCausalPostgresRun(t, db, task, "restore", model.TaskRunStatusWarning)
	if err := dispatcher.RaiseVerificationFailureForRestoreRun(task, restoreWarning.ID, "restore verification warning"); err != nil {
		t.Fatalf("raise restore verification warning: %v", err)
	}
	if err := dispatcher.RaiseVerificationFailureForRestoreRun(task, restoreWarning.ID, "replayed restore verification warning"); err != nil {
		t.Fatalf("replay restore verification warning: %v", err)
	}
	restoreSuccess := createCausalPostgresRun(t, db, task, "restore", model.TaskRunStatusSuccess)
	if err := dispatcher.ResolveTaskAlertsForRestoreRun(task.ID, restoreSuccess.ID, "restore recovery"); err != nil {
		t.Fatalf("resolve restore recovery: %v", err)
	}

	var alerts []model.Alert
	if err := db.Where("task_id = ?", task.ID).Order("id ASC").Find(&alerts).Error; err != nil {
		t.Fatalf("load causal restore alerts: %v", err)
	}
	if len(alerts) != 3 {
		t.Fatalf("restore alerts=%d, want one per failure action/run", len(alerts))
	}
	for _, alert := range alerts {
		if alert.Status != "resolved" || alert.TaskRunID == nil {
			t.Fatalf("restore alert=%+v, want resolved with run identity", alert)
		}
	}
	if *alerts[0].TaskRunID != restoreFailure.ID ||
		*alerts[1].TaskRunID != ordinaryFailure.ID ||
		*alerts[2].TaskRunID != restoreWarning.ID {
		t.Fatalf("restore alert run identities=%+v", alerts)
	}
}
