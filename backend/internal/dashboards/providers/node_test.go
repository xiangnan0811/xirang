package providers

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/dashboards"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openNodeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.NodeMetricSample{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedNode(db *gorm.DB, id uint, name string) {
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (?, ?, ?, ?, ?)",
		id, name, "h-"+name, "u", "/bak/"+name)
}

func seedSample(db *gorm.DB, nodeID uint, ts time.Time, cpu float64, latency int64) {
	s := model.NodeMetricSample{
		NodeID: nodeID, CpuPct: cpu, MemPct: 50, DiskPct: 30, Load1m: 1.0,
		LatencyMs: &latency, ProbeOK: true, SampledAt: ts,
	}
	db.Create(&s)
}

func TestNodeProvider_Supports(t *testing.T) {
	p := NewNodeProvider(nil)
	for _, m := range []string{"node.cpu", "node.memory", "node.disk_pct", "node.load", "node.latency_ms"} {
		if !p.Supports(m) {
			t.Fatalf("should support %s", m)
		}
	}
	if p.Supports("task.success_rate") {
		t.Fatal("should not support task metric")
	}
}

func TestNodeProvider_SupportedAggregations(t *testing.T) {
	p := NewNodeProvider(nil)
	lat := p.SupportedAggregations("node.latency_ms")
	if len(lat) != 6 {
		t.Fatalf("latency: expected 6 aggs, got %v", lat)
	}
	cpu := p.SupportedAggregations("node.cpu")
	if len(cpu) != 3 {
		t.Fatalf("cpu: expected 3 aggs, got %v", cpu)
	}
	if p.SupportedAggregations("unknown") != nil {
		t.Fatal("unknown should return nil")
	}
}

func TestNodeProvider_Query_BucketingAvg(t *testing.T) {
	db := openNodeTestDB(t)
	seedNode(db, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// 6 samples across 3 buckets of 60s each: bucket 0 = [10, 20], bucket 1 = [30, 40], bucket 2 = [50, 60]
	seedSample(db, 1, base.Add(10*time.Second), 10, 5)
	seedSample(db, 1, base.Add(50*time.Second), 20, 5)
	seedSample(db, 1, base.Add(70*time.Second), 30, 5)
	seedSample(db, 1, base.Add(110*time.Second), 40, 5)
	seedSample(db, 1, base.Add(130*time.Second), 50, 5)
	seedSample(db, 1, base.Add(170*time.Second), 60, 5)

	p := NewNodeProvider(db)
	resp, err := p.Query(context.Background(), dashboards.QueryRequest{
		Metric:      "node.cpu",
		Filters:     dashboards.Filters{NodeIDs: []uint{1}},
		Aggregation: "avg",
		Start:       base, End: base.Add(3 * time.Minute),
	}, 60)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(resp.Series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(resp.Series))
	}
	pts := resp.Series[0].Points
	if len(pts) != 3 {
		t.Fatalf("expected 3 buckets, got %d", len(pts))
	}
	want := []float64{15, 35, 55}
	for i, v := range want {
		if pts[i].Value != v {
			t.Fatalf("bucket %d: got %v, want %v", i, pts[i].Value, v)
		}
	}
}

func TestNodeProvider_Query_MaxAndMin(t *testing.T) {
	db := openNodeTestDB(t)
	seedNode(db, 1, "a")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	seedSample(db, 1, base.Add(10*time.Second), 10, 1)
	seedSample(db, 1, base.Add(30*time.Second), 50, 1)
	seedSample(db, 1, base.Add(50*time.Second), 20, 1)

	p := NewNodeProvider(db)
	for _, agg := range []struct {
		name string
		want float64
	}{{"max", 50}, {"min", 10}} {
		resp, err := p.Query(context.Background(), dashboards.QueryRequest{
			Metric: "node.cpu", Filters: dashboards.Filters{NodeIDs: []uint{1}},
			Aggregation: agg.name, Start: base, End: base.Add(time.Minute),
		}, 60)
		if err != nil {
			t.Fatalf("%s: %v", agg.name, err)
		}
		if resp.Series[0].Points[0].Value != agg.want {
			t.Fatalf("%s: got %v want %v", agg.name, resp.Series[0].Points[0].Value, agg.want)
		}
	}
}

func TestNodeProvider_Query_MultiNodeSeparateSeries(t *testing.T) {
	db := openNodeTestDB(t)
	seedNode(db, 1, "alpha")
	seedNode(db, 2, "beta")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	seedSample(db, 1, base.Add(10*time.Second), 10, 1)
	seedSample(db, 2, base.Add(10*time.Second), 80, 1)

	p := NewNodeProvider(db)
	resp, err := p.Query(context.Background(), dashboards.QueryRequest{
		Metric: "node.cpu", Filters: dashboards.Filters{NodeIDs: []uint{1, 2}},
		Aggregation: "avg", Start: base, End: base.Add(time.Minute),
	}, 60)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(resp.Series) != 2 {
		t.Fatalf("expected 2 series, got %d", len(resp.Series))
	}
	// Sorted by node ID
	if resp.Series[0].Name != "alpha" || resp.Series[1].Name != "beta" {
		t.Fatalf("names: %v / %v", resp.Series[0].Name, resp.Series[1].Name)
	}
}

func TestNodeProvider_Query_EmptyFilterReturnsAllNodes(t *testing.T) {
	db := openNodeTestDB(t)
	seedNode(db, 1, "alpha")
	seedNode(db, 2, "beta")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	seedSample(db, 1, base.Add(10*time.Second), 10, 1)
	seedSample(db, 2, base.Add(10*time.Second), 80, 1)

	p := NewNodeProvider(db)
	resp, _ := p.Query(context.Background(), dashboards.QueryRequest{
		Metric: "node.cpu", Aggregation: "avg",
		Start: base, End: base.Add(time.Minute),
	}, 60)
	if len(resp.Series) != 2 {
		t.Fatalf("expected 2 series (all nodes), got %d", len(resp.Series))
	}
}

func TestNodeProvider_Query_DeletedNodeSilentlySkipped(t *testing.T) {
	db := openNodeTestDB(t)
	seedNode(db, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// Seed a sample with a node id that doesn't exist in nodes table
	db.Create(&model.NodeMetricSample{
		NodeID: 99, CpuPct: 42, MemPct: 50, DiskPct: 30, Load1m: 1, ProbeOK: true,
		SampledAt: base.Add(10 * time.Second),
	})
	seedSample(db, 1, base.Add(20*time.Second), 10, 1)

	p := NewNodeProvider(db)
	resp, _ := p.Query(context.Background(), dashboards.QueryRequest{
		Metric: "node.cpu", Aggregation: "avg",
		Start: base, End: base.Add(time.Minute),
	}, 60)
	// Node 99 appears with numeric fallback name since no nodes row exists
	if len(resp.Series) != 2 {
		t.Fatalf("expected 2 series, got %d", len(resp.Series))
	}
	foundFallback := false
	for _, s := range resp.Series {
		if s.Name == "node-99" {
			foundFallback = true
		}
	}
	if !foundFallback {
		t.Fatal("expected fallback name 'node-99' for orphan sample")
	}
}

func TestNodeProvider_Query_P95Latency(t *testing.T) {
	db := openNodeTestDB(t)
	seedNode(db, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// 10 latency samples: 10..100ms. p95 ≈ 100 (nearest-rank at rank 10).
	for i := 1; i <= 10; i++ {
		seedSample(db, 1, base.Add(time.Duration(i*5)*time.Second), 0, int64(i*10))
	}
	p := NewNodeProvider(db)
	resp, _ := p.Query(context.Background(), dashboards.QueryRequest{
		Metric: "node.latency_ms", Filters: dashboards.Filters{NodeIDs: []uint{1}},
		Aggregation: "p95", Start: base, End: base.Add(time.Minute),
	}, 60)
	if resp.Series[0].Points[0].Value != 100 {
		t.Fatalf("p95: got %v want 100", resp.Series[0].Points[0].Value)
	}
}

func TestNodeProvider_Query_UnknownMetric(t *testing.T) {
	p := NewNodeProvider(openNodeTestDB(t))
	_, err := p.Query(context.Background(), dashboards.QueryRequest{
		Metric: "node.bogus", Aggregation: "avg",
		Start: time.Now(), End: time.Now().Add(time.Hour),
	}, 60)
	if err != dashboards.ErrInvalidMetric {
		t.Fatalf("expected ErrInvalidMetric, got %v", err)
	}
}
