package anomaly

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openEWMADB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.NodeMetricSample{}, &model.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// seedNode uses raw SQL to avoid GORM default:true overriding the archived field
// and to satisfy the UNIQUE(backup_dir) constraint in the nodes table.
func seedNode(t *testing.T, db *gorm.DB, id uint, name, backupDir string) {
	t.Helper()
	if err := db.Exec(
		"INSERT INTO nodes (id, name, host, username, backup_dir, archived) VALUES (?, ?, ?, ?, ?, 0)",
		id, name, "h-"+name, "u", backupDir,
	).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
}

func seedSample(t *testing.T, db *gorm.DB, nodeID uint, ts time.Time, cpu, mem, load float64, probeOK bool) {
	t.Helper()
	s := model.NodeMetricSample{
		NodeID: nodeID, CpuPct: cpu, MemPct: mem, DiskPct: 50, Load1m: load,
		ProbeOK: probeOK, SampledAt: ts,
	}
	if err := db.Create(&s).Error; err != nil {
		t.Fatalf("seed sample: %v", err)
	}
}

func newEWMADetector(t *testing.T, db *gorm.DB, now time.Time) *EWMADetector {
	t.Helper()
	s := settings.NewService(db)
	d := NewEWMADetector(db, s)
	d.SetNowFn(func() time.Time { return now })
	return d
}

func TestEWMA_InsufficientSamples_NoFindings(t *testing.T) {
	db := openEWMADB(t)
	seedNode(t, db, 1, "n1", "/b1")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// Only 3 samples — below default min=24.
	for i := 0; i < 3; i++ {
		seedSample(t, db, 1, now.Add(time.Duration(-i)*time.Minute), 20, 20, 0.5, true)
	}
	d := newEWMADetector(t, db, now)
	findings, err := d.Evaluate(context.Background())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings, got %d", len(findings))
	}
}

func TestEWMA_ConstantSeries_NoFindings(t *testing.T) {
	db := openEWMADB(t)
	seedNode(t, db, 1, "n1", "/b1")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		seedSample(t, db, 1, now.Add(time.Duration(-i)*time.Minute), 20, 20, 0.5, true)
	}
	d := newEWMADetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	if len(findings) != 0 {
		t.Fatalf("constant series should not fire; got %d", len(findings))
	}
}

func TestEWMA_CpuSpike_Warning(t *testing.T) {
	db := openEWMADB(t)
	seedNode(t, db, 1, "n1", "/b1")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// 24 baseline samples around 20 with small noise, last sample spikes.
	values := []float64{
		18, 22, 20, 21, 19, 20, 22, 21, 20, 19, 21, 20,
		18, 22, 20, 21, 19, 20, 22, 21, 20, 19, 21, 20,
		80,
	}
	for i, v := range values {
		ts := now.Add(-time.Duration(len(values)-1-i) * time.Minute)
		seedSample(t, db, 1, ts, v, 20, 0.5, true)
	}
	d := newEWMADetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	if len(findings) == 0 {
		t.Fatalf("expected at least one finding")
	}
	cpuFinding := filterFindings(findings, "cpu_pct")
	if cpuFinding == nil {
		t.Fatalf("no cpu_pct finding")
	}
	if cpuFinding.Severity != "warning" && cpuFinding.Severity != "critical" {
		t.Fatalf("severity=%s, expected warning or critical", cpuFinding.Severity)
	}
	if cpuFinding.Sigma == nil || *cpuFinding.Sigma <= 0 {
		t.Fatalf("sigma should be positive")
	}
	if cpuFinding.ErrorCode != "XR-ANOMALY-CPU-1" {
		t.Fatalf("error code=%s", cpuFinding.ErrorCode)
	}
}

func TestEWMA_ProbeFailedSamplesFiltered(t *testing.T) {
	db := openEWMADB(t)
	seedNode(t, db, 1, "n1", "/b1")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// 10 probe-failed samples with huge values — must be filtered out
	for i := 0; i < 10; i++ {
		seedSample(t, db, 1, now.Add(-time.Duration(10-i)*time.Minute), 99, 99, 10, false)
	}
	// Only 2 probe-ok samples — below min; returns 0
	for i := 0; i < 2; i++ {
		seedSample(t, db, 1, now.Add(-time.Duration(i)*time.Minute), 20, 20, 0.5, true)
	}
	d := newEWMADetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings (probe_ok samples insufficient), got %d", len(findings))
	}
}

func TestEWMA_MultipleNodesIndependent(t *testing.T) {
	db := openEWMADB(t)
	seedNode(t, db, 1, "n1", "/b1")
	seedNode(t, db, 2, "n2", "/b2")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// n1 has a noisy baseline then a spike.
	node1Values := []float64{
		18, 22, 20, 21, 19, 20, 22, 21, 19, 20, 18, 22,
		20, 21, 19, 20, 22, 21, 19, 20, 18, 22, 20, 21,
		90,
	}
	for i, v := range node1Values {
		seedSample(t, db, 1, now.Add(-time.Duration(len(node1Values)-1-i)*time.Minute), v, 20, 0.5, true)
	}
	// n2 is calm
	for i := 0; i < 25; i++ {
		seedSample(t, db, 2, now.Add(-time.Duration(i)*time.Minute), 20, 20, 0.5, true)
	}
	d := newEWMADetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	node1Findings := 0
	node2Findings := 0
	for _, f := range findings {
		if f.NodeID == 1 {
			node1Findings++
		}
		if f.NodeID == 2 {
			node2Findings++
		}
	}
	if node1Findings == 0 {
		t.Fatalf("n1 should have findings")
	}
	if node2Findings != 0 {
		t.Fatalf("n2 should not have findings, got %d", node2Findings)
	}
}

func TestEWMA_AnomalyDisabled_Short(t *testing.T) {
	db := openEWMADB(t)
	seedNode(t, db, 1, "n1", "/b1")
	// disable via system_settings row
	db.Create(&model.SystemSetting{Key: "anomaly.enabled", Value: "false"})
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	for i, v := range []float64{20, 20, 20, 20, 20, 20, 20, 20, 20, 90} {
		seedSample(t, db, 1, now.Add(-time.Duration(9-i)*time.Minute), v, 20, 0.5, true)
	}
	d := newEWMADetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	if len(findings) != 0 {
		t.Fatalf("anomaly disabled → expected 0, got %d", len(findings))
	}
}

// filterFindings picks the first finding matching metric, or nil.
func filterFindings(findings []Finding, metric string) *Finding {
	for i := range findings {
		if findings[i].Metric == metric {
			return &findings[i]
		}
	}
	return nil
}
