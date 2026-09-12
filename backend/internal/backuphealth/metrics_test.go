package backuphealth

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"xirang/backend/internal/model"
)

func TestBackupCompletionCollectorUsesVerifiedFactsAcrossRestart(t *testing.T) {
	db := openFactTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("migrate task tables: %v", err)
	}
	node := factTestNode(t, db, "metric-node")
	for _, task := range []model.Task{
		{ID: 1, Name: "shared-name", NodeID: node.ID, ExecutorType: "rsync"},
		{ID: 2, Name: "shared-name", NodeID: node.ID, ExecutorType: "restic"},
		{ID: 3, Name: "command-only", NodeID: node.ID, ExecutorType: "command"},
		{ID: 4, Name: "pending-only", NodeID: node.ID, ExecutorType: "rsync"},
	} {
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("create task %d: %v", task.ID, err)
		}
	}

	older := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	for _, input := range []LegacyTransferInput{
		{TaskID: 1, TaskRunID: 101, NodeID: node.ID, ExecutorType: "rsync", CompletedAt: older},
		{TaskID: 2, TaskRunID: 102, NodeID: node.ID, ExecutorType: "restic", CompletedAt: newer},
	} {
		if err := db.Transaction(func(tx *gorm.DB) error {
			return RecordLegacyTransferTx(context.Background(), tx, input)
		}); err != nil {
			t.Fatalf("record verified fact: %v", err)
		}
	}

	// Facts may outlive a deleted task row; the public metric must not invent a
	// task-ID label for such history.
	if err := db.Transaction(func(tx *gorm.DB) error {
		return RecordLegacyTransferTx(context.Background(), tx, LegacyTransferInput{
			TaskID: 99, TaskRunID: 199, NodeID: node.ID, ExecutorType: "restic", CompletedAt: newer.Add(time.Hour),
		})
	}); err != nil {
		t.Fatalf("record orphaned verified fact: %v", err)
	}
	// A successful ordinary command and a pending backup attempt are not facts;
	// neither may produce a metric series.
	finished := newer
	if err := db.Create(&model.TaskRun{ID: 301, TaskID: 3, NodeIDSnapshot: node.ID, ExecutorTypeSnapshot: "command", Status: "success", FinishedAt: &finished}).Error; err != nil {
		t.Fatalf("create command run: %v", err)
	}
	if err := db.Create(&model.TaskRun{ID: 401, TaskID: 4, NodeIDSnapshot: node.ID, ExecutorTypeSnapshot: "rsync", Status: "pending"}).Error; err != nil {
		t.Fatalf("create pending run: %v", err)
	}

	gather := func() *dto.MetricFamily {
		registry := prometheus.NewRegistry()
		registry.MustRegister(NewBackupCompletionCollector(db))
		families, err := registry.Gather()
		if err != nil {
			t.Fatalf("gather backup completion metric: %v", err)
		}
		for _, family := range families {
			if family.GetName() == backupLastSuccessTimestampMetricName {
				return family
			}
		}
		t.Fatalf("metric family %q missing", backupLastSuccessTimestampMetricName)
		return nil
	}

	assertFamily := func(family *dto.MetricFamily) {
		if len(family.Metric) != 1 {
			t.Fatalf("duplicate task names/ordinary runs must yield one metric, got %d", len(family.Metric))
		}
		metric := family.Metric[0]
		if len(metric.Label) != 1 || metric.Label[0].GetName() != "task_name" || metric.Label[0].GetValue() != "shared-name" {
			t.Fatalf("unexpected metric labels: %+v", metric.Label)
		}

		if got := metric.GetGauge().GetValue(); got != float64(newer.Unix()) {
			t.Fatalf("metric timestamp=%v, want %v", got, float64(newer.Unix()))
		}
	}

	assertFamily(gather())
	// A fresh registry/collector represents a process restart: the value comes
	// from durable facts rather than process-local gauge state.
	assertFamily(gather())
}
func TestBackupCompletionCollectorReducesLargeHistoryByTaskName(t *testing.T) {
	db := openFactTestDB(t)
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatalf("migrate task table: %v", err)
	}
	node := factTestNode(t, db, "metric-history-node")

	const (
		taskCount  = 24
		labelCount = 8
		history    = 2400
	)
	tasks := make([]model.Task, 0, taskCount)
	for index := 0; index < taskCount; index++ {
		tasks = append(tasks, model.Task{
			ID:           uint(index + 1),
			Name:         fmt.Sprintf("history-%d", index%labelCount),
			NodeID:       node.ID,
			ExecutorType: "rsync",
		})
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatalf("create history tasks: %v", err)
	}

	base := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	facts := make([]model.BackupCompletion, 0, history)
	want := make(map[string]time.Time, labelCount)
	for index := 0; index < history; index++ {
		taskID := uint(index%taskCount + 1)
		taskName := fmt.Sprintf("history-%d", index%labelCount)
		completedAt := base.Add(time.Duration(index) * time.Second)
		taskRunID := uint(10000 + index)
		facts = append(facts, model.BackupCompletion{
			TaskID: &taskID, TaskRunID: &taskRunID, NodeID: node.ID,
			ExecutorType: "rsync", FactKind: model.BackupCompletionKindLegacyTransferCompleted,
			EvidenceStatus: model.BackupCompletionEvidenceVerified, CompletedAt: completedAt,
			CreatedAt: completedAt, UpdatedAt: completedAt,
		})
		if previous, ok := want[taskName]; !ok || completedAt.After(previous) {
			want[taskName] = completedAt
		}
	}
	if err := db.CreateInBatches(&facts, 200).Error; err != nil {
		t.Fatalf("create history facts: %v", err)
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewBackupCompletionCollector(db))
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather history metric: %v", err)
	}
	var family *dto.MetricFamily
	for _, candidate := range families {
		if candidate.GetName() == backupLastSuccessTimestampMetricName {
			family = candidate
			break
		}
	}
	if family == nil {
		t.Fatalf("metric family %q missing", backupLastSuccessTimestampMetricName)
	}
	if len(family.Metric) != labelCount {
		t.Fatalf("history should reduce to one series per task name: got %d, want %d", len(family.Metric), labelCount)
	}
	for _, metric := range family.Metric {
		if len(metric.Label) != 1 {
			t.Fatalf("unexpected history metric labels: %+v", metric.Label)
		}
		name := metric.Label[0].GetValue()
		if got, ok := want[name]; !ok || metric.GetGauge().GetValue() != float64(got.Unix()) {
			t.Fatalf("history metric %q timestamp=%v, want %v", name, metric.GetGauge().GetValue(), want[name])
		}
	}
}

func TestBackupCompletionCollectorReportsDatabaseErrors(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(NewBackupCompletionCollector(nil))
	if _, err := registry.Gather(); err == nil {
		t.Fatal("nil database should report an invalid metric instead of fabricating zero")
	}
}

func openBackupMetricPostgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN required")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL metric base: %v", err)
	}
	schema := fmt.Sprintf("xirang_backuphealth_metric_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create PostgreSQL metric schema: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		if sqlDB, dbErr := base.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		t.Fatalf("open PostgreSQL metric schema: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		if sqlDB, dbErr := base.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.BackupCompletion{}); err != nil {
		t.Fatalf("migrate PostgreSQL metric schema: %v", err)
	}
	return db
}

func TestBackupCompletionCollectorPostgresScrape(t *testing.T) {
	db := openBackupMetricPostgresTestDB(t)
	node := factTestNode(t, db, "metric-postgres-node")
	tasks := []model.Task{
		{ID: 1, Name: "pg-shared", NodeID: node.ID, ExecutorType: "rsync"},
		{ID: 2, Name: "pg-shared", NodeID: node.ID, ExecutorType: "restic"},
		{ID: 3, Name: "pg-other", NodeID: node.ID, ExecutorType: "rclone"},
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatalf("create PostgreSQL metric tasks: %v", err)
	}
	base := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	inputs := []LegacyTransferInput{
		{TaskID: 1, TaskRunID: 5001, NodeID: node.ID, ExecutorType: "rsync", CompletedAt: base},
		{TaskID: 2, TaskRunID: 5002, NodeID: node.ID, ExecutorType: "restic", CompletedAt: base.Add(17 * time.Minute)},
		{TaskID: 3, TaskRunID: 5003, NodeID: node.ID, ExecutorType: "rclone", CompletedAt: base.Add(3 * time.Minute)},
	}
	for _, input := range inputs {
		if err := db.Transaction(func(tx *gorm.DB) error {
			return RecordLegacyTransferTx(context.Background(), tx, input)
		}); err != nil {
			t.Fatalf("record PostgreSQL metric fact: %v", err)
		}
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewBackupCompletionCollector(db))
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather PostgreSQL metric: %v", err)
	}
	var family *dto.MetricFamily
	for _, candidate := range families {
		if candidate.GetName() == backupLastSuccessTimestampMetricName {
			family = candidate
			break
		}
	}
	if family == nil {
		t.Fatalf("metric family %q missing", backupLastSuccessTimestampMetricName)
	}
	if len(family.Metric) != 2 {
		t.Fatalf("PostgreSQL duplicate names should reduce to two series, got %d", len(family.Metric))
	}
	want := map[string]float64{
		"pg-shared": float64(base.Add(17 * time.Minute).Unix()),
		"pg-other":  float64(base.Add(3 * time.Minute).Unix()),
	}
	for _, metric := range family.Metric {
		if len(metric.Label) != 1 || metric.Label[0].GetName() != "task_name" {
			t.Fatalf("unexpected PostgreSQL metric labels: %+v", metric.Label)
		}
		name := metric.Label[0].GetValue()
		if got, ok := want[name]; !ok || metric.GetGauge().GetValue() != got {
			t.Fatalf("PostgreSQL metric %q timestamp=%v, want %v", name, metric.GetGauge().GetValue(), want[name])
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("PostgreSQL metric omitted labels: %+v", want)
	}
}
