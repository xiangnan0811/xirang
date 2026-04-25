package probe

import (
	"fmt"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var probeTimeAnchor = time.Date(2026, 4, 1, 12, 30, 0, 0, time.UTC)

func openProbeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.NodeMetricSample{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestParseMetricsOutput_HappyPath(t *testing.T) {
	nm, err := parseMetricsOutput("12.5 47.0 80.1 0.9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nm.cpuPct != 12.5 || nm.memPct != 47.0 || nm.diskPct != 80.1 || nm.load1m != 0.9 {
		t.Fatalf("unexpected fields: %+v", nm)
	}
}

func TestParseMetricsOutput_TruncatedReturnsError(t *testing.T) {
	// Fewer than 4 fields → format error per the contract.
	for _, in := range []string{"", "10", "10 20", "10 20 30"} {
		if _, err := parseMetricsOutput(in); err == nil {
			t.Fatalf("input %q: expected error, got nil", in)
		}
	}
}

// parseFloat in production coerces invalid / negative values to 0 silently
// (no error). This test pins that contract — non-numeric does NOT raise.
func TestParseMetricsOutput_NonNumericCoercesToZero(t *testing.T) {
	nm, err := parseMetricsOutput("garbage 47 80 0.9")
	if err != nil {
		t.Fatalf("unexpected error for non-numeric cpu field: %v", err)
	}
	if nm.cpuPct != 0 {
		t.Fatalf("cpuPct should coerce to 0 on non-numeric, got %f", nm.cpuPct)
	}
	if nm.memPct != 47 {
		t.Fatalf("memPct unaffected, got %f", nm.memPct)
	}
}

func TestIsInMaintenanceWindow(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name string
		node model.Node
		want bool
	}{
		{
			name: "no_window",
			node: model.Node{},
			want: false,
		},
		{
			name: "inside_window",
			node: func() model.Node {
				start := now.Add(-time.Hour)
				end := now.Add(time.Hour)
				return model.Node{MaintenanceStart: &start, MaintenanceEnd: &end}
			}(),
			want: true,
		},
		{
			name: "before_window",
			node: func() model.Node {
				start := now.Add(time.Hour)
				end := now.Add(2 * time.Hour)
				return model.Node{MaintenanceStart: &start, MaintenanceEnd: &end}
			}(),
			want: false,
		},
		{
			name: "after_window",
			node: func() model.Node {
				start := now.Add(-2 * time.Hour)
				end := now.Add(-time.Hour)
				return model.Node{MaintenanceStart: &start, MaintenanceEnd: &end}
			}(),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInMaintenanceWindow(tt.node); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanupOldMetrics_DropsBeyondRetention(t *testing.T) {
	db := openProbeTestDB(t)
	now := time.Now().UTC()
	// Production keeps 7 days. Seed one fresh sample (today) and one ancient (8 days old).
	fresh := &model.NodeMetricSample{NodeID: 1, SampledAt: now.Add(-time.Hour), CpuPct: 10, ProbeOK: true}
	old := &model.NodeMetricSample{NodeID: 1, SampledAt: now.AddDate(0, 0, -8), CpuPct: 20, ProbeOK: true}
	for _, s := range []*model.NodeMetricSample{fresh, old} {
		if err := db.Create(s).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	p := &Prober{db: db, metricRetentionDays: 7}
	p.cleanupOldMetrics()

	var rows []model.NodeMetricSample
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row remaining, got %d", len(rows))
	}
	if rows[0].CpuPct != 10 {
		t.Fatalf("survivor should be the fresh sample, got CpuPct=%f", rows[0].CpuPct)
	}
}

func TestCleanupOldMetrics_EmptyTableNoOp(t *testing.T) {
	db := openProbeTestDB(t)
	p := &Prober{db: db, metricRetentionDays: 7}
	// Must not panic on empty table.
	p.cleanupOldMetrics()
}
