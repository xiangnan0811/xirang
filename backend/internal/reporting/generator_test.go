package reporting

import (
	"fmt"
	"math"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// reportingTimeAnchor is the fixed reference "now" for every time-sensitive
// test in this package. Pinning to a literal date keeps the suite stable
// regardless of when CI fires (cf. metrics aggregator flake fix).
var reportingTimeAnchor = time.Date(2026, 4, 1, 12, 30, 0, 0, time.UTC)

func openReportingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Node{},
		&model.Task{},
		&model.TaskRun{},
		&model.NodeMetricSample{},
		&model.Alert{},
		&model.ReportConfig{},
		&model.Report{},
		&model.Integration{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// approxEqual compares floats with the project's standard 1e-6 epsilon.
func approxEqual(got, want float64) bool {
	return math.Abs(got-want) < 1e-6
}
