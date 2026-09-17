package backuphealth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"

	"xirang/backend/internal/model"
)

const backupLastSuccessTimestampMetricName = "xirang_backup_last_success_timestamp"

var backupLastSuccessTimestampDesc = prometheus.NewDesc(
	backupLastSuccessTimestampMetricName,
	"Unix timestamp of last successful backup per task",
	[]string{"task_name"},
	nil,
)

// backupCompletionMetricQuery starts from retained task rows and probes the
// partial verified-fact index once per task. The literal predicate is
// intentional: both SQLite and PostgreSQL can prove that the existing partial
// index applies before executing the correlated lookup.
const backupCompletionMetricQuery = `
	SELECT retained_tasks.name AS task_name, winner.completed_at AS completed_at
	FROM tasks AS retained_tasks
	JOIN backup_completions AS winner
		ON winner.id = (
			SELECT newest.id
			FROM backup_completions AS newest
			WHERE newest.task_id = retained_tasks.id
			  AND newest.evidence_status = '` + model.BackupCompletionEvidenceVerified + `'
			ORDER BY newest.completed_at DESC, newest.id DESC
			LIMIT 1
		)
	WHERE TRIM(retained_tasks.name) <> ''
`

// BackupCompletionCollector exposes only authoritative verified completion facts.
// It reads from the database during each scrape, so restart and historical
// backfill do not depend on process-local gauge state. Task names are labels
// only; executor/fact classification is performed by the evidence query.
type BackupCompletionCollector struct {
	db *gorm.DB
}

// NewBackupCompletionCollector builds a collector backed by db.
func NewBackupCompletionCollector(db *gorm.DB) *BackupCompletionCollector {
	return &BackupCompletionCollector{db: db}
}

// RegisterBackupCompletionCollector registers the authoritative backup metric
// on Prometheus' default registry used by the /metrics endpoint.
func RegisterBackupCompletionCollector(db *gorm.DB) error {
	if db == nil {
		return errors.New("backup completion metric database is unavailable")
	}
	return prometheus.Register(NewBackupCompletionCollector(db))
}

func (c *BackupCompletionCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- backupLastSuccessTimestampDesc
}

func (c *BackupCompletionCollector) Collect(ch chan<- prometheus.Metric) {
	if c == nil || c.db == nil {
		ch <- prometheus.NewInvalidMetric(backupLastSuccessTimestampDesc, errors.New("backup completion metric database is unavailable"))
		return
	}

	type row struct {
		TaskName    string    `gorm:"column:task_name"`
		CompletedAt time.Time `gorm:"column:completed_at"`
	}
	var rows []row
	err := c.db.WithContext(context.Background()).Raw(backupCompletionMetricQuery).Scan(&rows).Error
	if err != nil {
		ch <- prometheus.NewInvalidMetric(backupLastSuccessTimestampDesc, fmt.Errorf("query verified backup completion metrics: %w", err))
		return
	}

	// One row is returned per retained task. A task name can be shared by
	// several tasks, so retain the newest timestamp for the public label.
	latestByName := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		completedAt := row.CompletedAt.UTC()
		if previous, ok := latestByName[row.TaskName]; !ok || completedAt.After(previous) {
			latestByName[row.TaskName] = completedAt
		}
	}
	labels := make([]string, 0, len(latestByName))
	for name := range latestByName {
		labels = append(labels, name)
	}
	sort.Strings(labels)
	for _, name := range labels {
		ch <- prometheus.MustNewConstMetric(
			backupLastSuccessTimestampDesc,
			prometheus.GaugeValue,
			float64(latestByName[name].Unix()),
			name,
		)
	}
}
