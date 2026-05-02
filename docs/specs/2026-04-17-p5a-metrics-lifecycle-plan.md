# P5a: Metrics Lifecycle & Node Detail Page — Implementation Plan

> **For agentic workers:** Use the current repo-approved task execution workflow to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add 3-tier metric storage (raw 7d / hourly 90d / daily 2y), a pluggable metric sink with optional Prometheus remote_write, and the missing `/nodes/:id` detail page.

**Architecture:** Extend `node_metric_samples` with previously-discarded fields, add two aggregate tables and an `AggregationWorker` goroutine that rolls up hourly and daily buckets (backfilling synchronously at boot). Decouple the prober from DB writes via a `Sink` interface and `FanSink` so a Prometheus remote_write sink can be opted in via `system_settings`. Build a new tabbed React page under `/nodes/:id`.

**Tech Stack:** Go 1.24 + Gin + GORM (sqlite & postgres), Prometheus client_golang, React 18 + TypeScript + Vite + Tailwind + Sage tokens + Recharts + TanStack Query.

**Spec reference:** [2026-04-17-p5a-metrics-lifecycle-design.md](./2026-04-17-p5a-metrics-lifecycle-design.md)

## Spec-vs-reality deviations

Discovered during planning; reflected in this plan:

- The current `NodeMetricSample` struct lacks `latency_ms`, `disk_gb_used`, `disk_gb_total`, `probe_ok` columns (prober collects latency but discards it). Task 1 adds these columns via migration `000033` before the aggregate migrations. Aggregate migrations become `000034` (hourly) and `000035` (daily).
- `Prober.cleanupOldMetrics` is confirmed wired correctly (the TTL precondition in the spec can be treated as satisfied).
- Frontend router lives at `web/src/router.tsx` (not under `web/src/app/`).

---

## File Structure

**New backend files:**
- `backend/internal/database/migrations/{sqlite,postgres}/000033_node_metric_samples_extend.{up,down}.sql`
- `backend/internal/database/migrations/{sqlite,postgres}/000034_node_metric_samples_hourly.{up,down}.sql`
- `backend/internal/database/migrations/{sqlite,postgres}/000035_node_metric_samples_daily.{up,down}.sql`
- `backend/internal/metrics/fields.go` — field-name constants
- `backend/internal/metrics/sample.go` — `Sample` struct
- `backend/internal/metrics/sink.go` — `Sink` interface + `FanSink`
- `backend/internal/metrics/db_sink.go` — `DBSink`
- `backend/internal/metrics/db_sink_test.go`
- `backend/internal/metrics/remote_write_sink.go` — optional `RemoteWriteSink`
- `backend/internal/metrics/remote_write_sink_test.go`
- `backend/internal/metrics/aggregator.go` — `AggregationWorker`
- `backend/internal/metrics/aggregator_test.go`
- `backend/internal/metrics/forecast.go` — disk linear regression
- `backend/internal/metrics/forecast_test.go`
- `backend/internal/metrics/granularity.go` — `SelectGranularity`
- `backend/internal/metrics/granularity_test.go`
- `backend/internal/metrics/obs.go` — Prometheus registrations (rollup lag, dropped)
- `backend/internal/api/handlers/node_metrics_handler.go`
- `backend/internal/api/handlers/node_metrics_handler_test.go`
- `backend/internal/api/handlers/admin_metrics_handler.go`

**Modified backend files:**
- `backend/internal/model/models.go` — extend `NodeMetricSample`, add `NodeMetricSampleHourly`, `NodeMetricSampleDaily`
- `backend/internal/probe/prober.go` — write through `metrics.Sink` instead of `db.Create`; populate new fields
- `backend/internal/sshutil/probe.go` (read-only confirm existing `ProbeResult` has latency)
- `backend/cmd/server/main.go` — wire sink fan + aggregator
- `backend/internal/api/router.go` — register new routes
- `backend/internal/settings/service.go` (or equivalent) — new `metrics.remote_write.*` keys

**New frontend files:**
- `web/src/pages/nodes-detail-page.tsx`
- `web/src/features/nodes-detail/use-node-status.ts`
- `web/src/features/nodes-detail/use-node-metrics.ts`
- `web/src/features/nodes-detail/use-disk-forecast.ts`
- `web/src/features/nodes-detail/overview-tab.tsx`
- `web/src/features/nodes-detail/metrics-tab.tsx`
- `web/src/features/nodes-detail/tasks-tab.tsx`
- `web/src/features/nodes-detail/alerts-tab.tsx`
- `web/src/features/nodes-detail/profile-tab.tsx`
- `web/src/features/nodes-detail/stat-card.tsx`
- `web/src/features/nodes-detail/trend-chart.tsx`
- `web/src/features/nodes-detail/disk-forecast-card.tsx`
- Corresponding `*.test.tsx` files for each of the above.

**Modified frontend files:**
- `web/src/router.tsx` — register `/nodes/:id`
- `web/src/pages/overview-page.tsx` (and/or matrix component) — node dots → `/nodes/:id`
- `web/src/pages/nodes-page.tsx` (and/or list row) — row links to `/nodes/:id`
- `web/src/pages/alerts-page.tsx` (and/or row component) — "查看节点" action

---

## Task 1: Extend raw `node_metric_samples` schema

**Files:**
- Create: `backend/internal/database/migrations/sqlite/000033_node_metric_samples_extend.up.sql`
- Create: `backend/internal/database/migrations/sqlite/000033_node_metric_samples_extend.down.sql`
- Create: `backend/internal/database/migrations/postgres/000033_node_metric_samples_extend.up.sql`
- Create: `backend/internal/database/migrations/postgres/000033_node_metric_samples_extend.down.sql`
- Modify: `backend/internal/model/models.go:316-325`

- [ ] **Step 1: Write SQLite up migration**

Create `backend/internal/database/migrations/sqlite/000033_node_metric_samples_extend.up.sql`:
```sql
ALTER TABLE node_metric_samples ADD COLUMN latency_ms INTEGER;
ALTER TABLE node_metric_samples ADD COLUMN disk_gb_used REAL;
ALTER TABLE node_metric_samples ADD COLUMN disk_gb_total REAL;
ALTER TABLE node_metric_samples ADD COLUMN probe_ok INTEGER NOT NULL DEFAULT 1;
```

- [ ] **Step 2: Write SQLite down migration**

Create `backend/internal/database/migrations/sqlite/000033_node_metric_samples_extend.down.sql`:
```sql
-- SQLite does not support DROP COLUMN before v3.35; use table rebuild.
CREATE TABLE node_metric_samples_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    cpu_pct REAL NOT NULL DEFAULT 0,
    mem_pct REAL NOT NULL DEFAULT 0,
    disk_pct REAL NOT NULL DEFAULT 0,
    load_1m REAL NOT NULL DEFAULT 0,
    sampled_at DATETIME NOT NULL,
    created_at DATETIME
);
INSERT INTO node_metric_samples_new
    SELECT id, node_id, cpu_pct, mem_pct, disk_pct, load_1m, sampled_at, created_at
    FROM node_metric_samples;
DROP TABLE node_metric_samples;
ALTER TABLE node_metric_samples_new RENAME TO node_metric_samples;
CREATE INDEX idx_node_metric_node_sampled ON node_metric_samples(node_id, sampled_at);
CREATE INDEX idx_node_metric_sampled_at ON node_metric_samples(sampled_at);
```

- [ ] **Step 3: Write Postgres up/down**

`000033_node_metric_samples_extend.up.sql`:
```sql
ALTER TABLE node_metric_samples ADD COLUMN latency_ms INTEGER;
ALTER TABLE node_metric_samples ADD COLUMN disk_gb_used DOUBLE PRECISION;
ALTER TABLE node_metric_samples ADD COLUMN disk_gb_total DOUBLE PRECISION;
ALTER TABLE node_metric_samples ADD COLUMN probe_ok BOOLEAN NOT NULL DEFAULT TRUE;
```

`000033_node_metric_samples_extend.down.sql`:
```sql
ALTER TABLE node_metric_samples DROP COLUMN probe_ok;
ALTER TABLE node_metric_samples DROP COLUMN disk_gb_total;
ALTER TABLE node_metric_samples DROP COLUMN disk_gb_used;
ALTER TABLE node_metric_samples DROP COLUMN latency_ms;
```

- [ ] **Step 4: Update `NodeMetricSample` struct**

In `backend/internal/model/models.go`, replace lines 315–325 with:
```go
// NodeMetricSample 节点资源采样记录
type NodeMetricSample struct {
    ID          uint      `gorm:"primaryKey" json:"id"`
    NodeID      uint      `gorm:"not null;index:idx_node_metric_node_sampled,priority:1" json:"node_id"`
    CpuPct      float64   `gorm:"not null;default:0" json:"cpu_pct"`
    MemPct      float64   `gorm:"not null;default:0" json:"mem_pct"`
    DiskPct     float64   `gorm:"not null;default:0" json:"disk_pct"`
    Load1m      float64   `gorm:"column:load_1m;not null;default:0" json:"load_1m"`
    LatencyMs   *int      `json:"latency_ms,omitempty"`
    DiskGBUsed  *float64  `json:"disk_gb_used,omitempty"`
    DiskGBTotal *float64  `json:"disk_gb_total,omitempty"`
    ProbeOK     bool      `gorm:"not null;default:true" json:"probe_ok"`
    SampledAt   time.Time `gorm:"not null;index:idx_node_metric_node_sampled,priority:2;index:idx_node_metric_sampled_at" json:"sampled_at"`
    CreatedAt   time.Time `json:"created_at"`
}
```

Nullable columns use pointer types so historical rows (pre-migration) stay NULL instead of zero-valued.

- [ ] **Step 5: Run migrations + build**

```
cd backend && go build ./...
```
Expected: no output (success). Start the server locally once to trigger migrations, then revert with:
```
make db-migrate-down N=1 && make db-migrate-up
```
(or whatever migration invocation the project uses; if there is none, the server run should be sufficient).

- [ ] **Step 6: Commit**

```bash
git add backend/internal/database/migrations backend/internal/model/models.go
git commit -m "feat(metrics): extend node_metric_samples with latency/disk_gb/probe_ok"
```

---

## Task 2: Populate new raw columns from prober

**Files:**
- Modify: `backend/internal/probe/prober.go:200-220`

- [ ] **Step 1: Locate the sample-building block**

In `prober.go` around line 206, the current block is:
```go
sample := model.NodeMetricSample{
    NodeID:    node.ID,
    CpuPct:    metrics.cpuPct,
    MemPct:    metrics.memPct,
    DiskPct:   metrics.diskPct,
    Load1m:    metrics.load1m,
    SampledAt: time.Now().UTC(),
}
```

- [ ] **Step 2: Plumb `ProbeResult.Latency` and disk GB to the sample**

Read `sshutil.ProbeNode` signature to confirm which of `disk_gb_used` / `disk_gb_total` it returns. Example (adapt names to actual struct):
```go
probe, probeErr := sshutil.ProbeNode(node, p.db)
// ...
lat := probe.Latency
sample := model.NodeMetricSample{
    NodeID:      node.ID,
    CpuPct:      metrics.cpuPct,
    MemPct:      metrics.memPct,
    DiskPct:     metrics.diskPct,
    Load1m:      metrics.load1m,
    LatencyMs:   &lat,
    DiskGBUsed:  metrics.diskGBUsed,  // assume existing *float64 in metrics
    DiskGBTotal: metrics.diskGBTotal,
    ProbeOK:     probeErr == nil,
    SampledAt:   time.Now().UTC(),
}
```

If `metrics.diskGBUsed/Total` don't exist in the intermediate `metrics` struct, extend it: read `Prober.collectMetrics` and confirm; extend the internal struct to carry these values through from the SSH `df` output parse.

- [ ] **Step 3: Verify build + existing tests still pass**

```
cd backend && go test ./internal/probe/... -count=1
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/probe/prober.go
git commit -m "feat(metrics): capture latency/disk_gb/probe_ok in raw samples"
```

---

## Task 3: Hourly aggregate table (migration 000034 + model)

**Files:**
- Create: `backend/internal/database/migrations/{sqlite,postgres}/000034_node_metric_samples_hourly.{up,down}.sql`
- Modify: `backend/internal/model/models.go` (append)

- [ ] **Step 1: Write SQLite up migration**

```sql
CREATE TABLE node_metric_samples_hourly (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    bucket_start DATETIME NOT NULL,
    cpu_pct_avg REAL,
    cpu_pct_max REAL,
    mem_pct_avg REAL,
    mem_pct_max REAL,
    disk_pct_avg REAL,
    disk_pct_max REAL,
    load1_avg REAL,
    load1_max REAL,
    latency_ms_avg REAL,
    latency_ms_max REAL,
    disk_gb_used_avg REAL,
    disk_gb_total REAL,
    probe_ok INTEGER NOT NULL DEFAULT 0,
    probe_fail INTEGER NOT NULL DEFAULT 0,
    sample_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    UNIQUE (node_id, bucket_start)
);
CREATE INDEX idx_nmsh_node_bucket ON node_metric_samples_hourly(node_id, bucket_start);
CREATE INDEX idx_nmsh_bucket ON node_metric_samples_hourly(bucket_start);
```

Down: `DROP TABLE node_metric_samples_hourly;`

- [ ] **Step 2: Write Postgres up migration**

```sql
CREATE TABLE node_metric_samples_hourly (
    id BIGSERIAL PRIMARY KEY,
    node_id BIGINT NOT NULL,
    bucket_start TIMESTAMPTZ NOT NULL,
    cpu_pct_avg DOUBLE PRECISION,
    cpu_pct_max DOUBLE PRECISION,
    mem_pct_avg DOUBLE PRECISION,
    mem_pct_max DOUBLE PRECISION,
    disk_pct_avg DOUBLE PRECISION,
    disk_pct_max DOUBLE PRECISION,
    load1_avg DOUBLE PRECISION,
    load1_max DOUBLE PRECISION,
    latency_ms_avg DOUBLE PRECISION,
    latency_ms_max DOUBLE PRECISION,
    disk_gb_used_avg DOUBLE PRECISION,
    disk_gb_total DOUBLE PRECISION,
    probe_ok BIGINT NOT NULL DEFAULT 0,
    probe_fail BIGINT NOT NULL DEFAULT 0,
    sample_count BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (node_id, bucket_start)
);
CREATE INDEX idx_nmsh_node_bucket ON node_metric_samples_hourly(node_id, bucket_start);
CREATE INDEX idx_nmsh_bucket ON node_metric_samples_hourly(bucket_start);
```

Down: `DROP TABLE node_metric_samples_hourly;`

- [ ] **Step 3: Append struct to `models.go`**

```go
// NodeMetricSampleHourly 节点资源采样 1h 聚合桶
type NodeMetricSampleHourly struct {
    ID             uint      `gorm:"primaryKey" json:"id"`
    NodeID         uint      `gorm:"not null;uniqueIndex:idx_nmsh_node_bucket,priority:1" json:"node_id"`
    BucketStart    time.Time `gorm:"not null;uniqueIndex:idx_nmsh_node_bucket,priority:2;index:idx_nmsh_bucket" json:"bucket_start"`
    CpuPctAvg      *float64  `json:"cpu_pct_avg,omitempty"`
    CpuPctMax      *float64  `json:"cpu_pct_max,omitempty"`
    MemPctAvg      *float64  `json:"mem_pct_avg,omitempty"`
    MemPctMax      *float64  `json:"mem_pct_max,omitempty"`
    DiskPctAvg     *float64  `json:"disk_pct_avg,omitempty"`
    DiskPctMax     *float64  `json:"disk_pct_max,omitempty"`
    Load1Avg       *float64  `json:"load1_avg,omitempty"`
    Load1Max       *float64  `json:"load1_max,omitempty"`
    LatencyMsAvg   *float64  `json:"latency_ms_avg,omitempty"`
    LatencyMsMax   *float64  `json:"latency_ms_max,omitempty"`
    DiskGBUsedAvg  *float64  `json:"disk_gb_used_avg,omitempty"`
    DiskGBTotal    *float64  `json:"disk_gb_total,omitempty"`
    ProbeOK        int64     `gorm:"not null;default:0" json:"probe_ok"`
    ProbeFail      int64     `gorm:"not null;default:0" json:"probe_fail"`
    SampleCount    int64     `gorm:"not null;default:0" json:"sample_count"`
    CreatedAt      time.Time `json:"created_at"`
}
```

- [ ] **Step 4: Verify build**

```
cd backend && go build ./...
```
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/database/migrations backend/internal/model/models.go
git commit -m "feat(metrics): add node_metric_samples_hourly aggregate table"
```

---

## Task 4: Daily aggregate table (migration 000035 + model)

**Files:**
- Create: `backend/internal/database/migrations/{sqlite,postgres}/000035_node_metric_samples_daily.{up,down}.sql`
- Modify: `backend/internal/model/models.go` (append)

- [ ] **Step 1: Copy hourly migrations, replacing table/index names**

SQLite up (replace `hourly` with `daily`, index name `nmsd`):
```sql
CREATE TABLE node_metric_samples_daily (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id INTEGER NOT NULL,
    bucket_start DATETIME NOT NULL,
    cpu_pct_avg REAL, cpu_pct_max REAL,
    mem_pct_avg REAL, mem_pct_max REAL,
    disk_pct_avg REAL, disk_pct_max REAL,
    load1_avg REAL, load1_max REAL,
    latency_ms_avg REAL, latency_ms_max REAL,
    disk_gb_used_avg REAL, disk_gb_total REAL,
    probe_ok INTEGER NOT NULL DEFAULT 0,
    probe_fail INTEGER NOT NULL DEFAULT 0,
    sample_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    UNIQUE (node_id, bucket_start)
);
CREATE INDEX idx_nmsd_node_bucket ON node_metric_samples_daily(node_id, bucket_start);
CREATE INDEX idx_nmsd_bucket ON node_metric_samples_daily(bucket_start);
```

Postgres up: same shape with `BIGSERIAL` / `TIMESTAMPTZ` / `DOUBLE PRECISION` / `BIGINT`.

Down (both): `DROP TABLE node_metric_samples_daily;`

- [ ] **Step 2: Append struct (copy hourly, rename type)**

```go
// NodeMetricSampleDaily 节点资源采样 1d 聚合桶
type NodeMetricSampleDaily struct {
    // identical shape to NodeMetricSampleHourly; copy fields verbatim.
}
```

Set GORM `TableName()` methods if necessary (follow existing convention — most models rely on default pluralization; override only if naming clashes).

- [ ] **Step 3: Verify build + commit**

```
cd backend && go build ./...
git add backend/internal/database/migrations backend/internal/model/models.go
git commit -m "feat(metrics): add node_metric_samples_daily aggregate table"
```

---

## Task 5: Metrics package scaffolding — `Sample`, `Sink`, `FanSink`, field constants

**Files:**
- Create: `backend/internal/metrics/fields.go`
- Create: `backend/internal/metrics/sample.go`
- Create: `backend/internal/metrics/sink.go`
- Create: `backend/internal/metrics/sink_test.go`

- [ ] **Step 1: Write field constants**

`fields.go`:
```go
package metrics

type Field string

const (
    FieldCPUPct       Field = "cpu_pct"
    FieldMemPct       Field = "mem_pct"
    FieldDiskPct      Field = "disk_pct"
    FieldLoad1        Field = "load1"
    FieldLatencyMs    Field = "latency_ms"
    FieldDiskGBUsed   Field = "disk_gb_used"
    FieldProbeOKRatio Field = "probe_ok_ratio"
)

// AllFields is the default set returned by /nodes/:id/metrics when `fields`
// query parameter is omitted.
var AllFields = []Field{
    FieldCPUPct, FieldMemPct, FieldDiskPct, FieldLoad1,
    FieldLatencyMs, FieldDiskGBUsed, FieldProbeOKRatio,
}
```

- [ ] **Step 2: Write `Sample` struct**

`sample.go`:
```go
package metrics

import "time"

type Sample struct {
    NodeID      uint
    NodeName    string
    SampledAt   time.Time
    CPUPct      *float64
    MemPct      *float64
    DiskPct     *float64
    Load1       *float64
    LatencyMs   *float64
    DiskGBUsed  *float64
    DiskGBTotal *float64
    ProbeOK     bool
}
```

- [ ] **Step 3: Write `Sink` interface and `FanSink`**

`sink.go`:
```go
package metrics

import (
    "context"
    "xirang/backend/internal/logger"
)

type Sink interface {
    Name() string
    Write(ctx context.Context, s Sample) error
}

type FanSink struct {
    sinks []Sink
}

func NewFanSink(sinks ...Sink) *FanSink { return &FanSink{sinks: sinks} }

// Write dispatches to each sink. Failures in one sink are logged but do not
// prevent delivery to others.
func (f *FanSink) Write(ctx context.Context, s Sample) {
    for _, sink := range f.sinks {
        if err := sink.Write(ctx, s); err != nil {
            logger.Module("metrics").Warn().
                Str("sink", sink.Name()).
                Uint("node_id", s.NodeID).
                Err(err).Msg("metric sink write failed")
        }
    }
}
```

- [ ] **Step 4: Write fan-sink test**

`sink_test.go`:
```go
package metrics

import (
    "context"
    "errors"
    "testing"
    "time"
)

type recorderSink struct {
    name   string
    fail   bool
    called int
}

func (r *recorderSink) Name() string { return r.name }
func (r *recorderSink) Write(_ context.Context, _ Sample) error {
    r.called++
    if r.fail {
        return errors.New("boom")
    }
    return nil
}

func TestFanSink_DispatchesToAll(t *testing.T) {
    a := &recorderSink{name: "a"}
    b := &recorderSink{name: "b"}
    fan := NewFanSink(a, b)
    fan.Write(context.Background(), Sample{NodeID: 1, SampledAt: time.Now()})
    if a.called != 1 || b.called != 1 {
        t.Fatalf("expected both sinks called once, got a=%d b=%d", a.called, b.called)
    }
}

func TestFanSink_OneFailsDoesNotBlockOthers(t *testing.T) {
    a := &recorderSink{name: "a", fail: true}
    b := &recorderSink{name: "b"}
    fan := NewFanSink(a, b)
    fan.Write(context.Background(), Sample{NodeID: 1, SampledAt: time.Now()})
    if b.called != 1 {
        t.Fatalf("expected b to be called despite a failing, got %d", b.called)
    }
}
```

- [ ] **Step 5: Run tests**

```
cd backend && go test ./internal/metrics/... -count=1 -run FanSink
```
Expected: PASS for both tests.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/metrics/
git commit -m "feat(metrics): scaffold sink abstraction and field constants"
```

---

## Task 6: `DBSink` implementation

**Files:**
- Create: `backend/internal/metrics/db_sink.go`
- Create: `backend/internal/metrics/db_sink_test.go`

- [ ] **Step 1: Write the failing test**

`db_sink_test.go`:
```go
package metrics

import (
    "context"
    "testing"
    "time"

    "xirang/backend/internal/model"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
    t.Helper()
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    if err != nil {
        t.Fatalf("open sqlite: %v", err)
    }
    if err := db.AutoMigrate(&model.NodeMetricSample{}); err != nil {
        t.Fatalf("migrate: %v", err)
    }
    return db
}

func TestDBSink_WriteInsertsRow(t *testing.T) {
    db := newTestDB(t)
    sink := NewDBSink(db)
    cpu, mem := 12.5, 45.0
    if err := sink.Write(context.Background(), Sample{
        NodeID: 7, SampledAt: time.Now().UTC(),
        CPUPct: &cpu, MemPct: &mem, ProbeOK: true,
    }); err != nil {
        t.Fatalf("write: %v", err)
    }
    var got model.NodeMetricSample
    if err := db.First(&got, "node_id = ?", 7).Error; err != nil {
        t.Fatalf("read back: %v", err)
    }
    if got.CpuPct != 12.5 || !got.ProbeOK {
        t.Fatalf("unexpected row: %+v", got)
    }
}
```

- [ ] **Step 2: Run test to verify it fails (compile error expected)**

```
cd backend && go test ./internal/metrics/... -count=1 -run DBSink
```
Expected: FAIL — `NewDBSink` undefined.

- [ ] **Step 3: Implement `DBSink`**

`db_sink.go`:
```go
package metrics

import (
    "context"

    "xirang/backend/internal/model"

    "gorm.io/gorm"
)

type DBSink struct{ db *gorm.DB }

func NewDBSink(db *gorm.DB) *DBSink { return &DBSink{db: db} }

func (s *DBSink) Name() string { return "db" }

func (s *DBSink) Write(ctx context.Context, sample Sample) error {
    var latency *int
    if sample.LatencyMs != nil {
        v := int(*sample.LatencyMs)
        latency = &v
    }
    row := model.NodeMetricSample{
        NodeID:      sample.NodeID,
        SampledAt:   sample.SampledAt,
        LatencyMs:   latency,
        DiskGBUsed:  sample.DiskGBUsed,
        DiskGBTotal: sample.DiskGBTotal,
        ProbeOK:     sample.ProbeOK,
    }
    if sample.CPUPct != nil {
        row.CpuPct = *sample.CPUPct
    }
    if sample.MemPct != nil {
        row.MemPct = *sample.MemPct
    }
    if sample.DiskPct != nil {
        row.DiskPct = *sample.DiskPct
    }
    if sample.Load1 != nil {
        row.Load1m = *sample.Load1
    }
    return s.db.WithContext(ctx).Create(&row).Error
}
```

- [ ] **Step 4: Run test to verify PASS**

```
cd backend && go test ./internal/metrics/... -count=1 -run DBSink
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/metrics/db_sink.go backend/internal/metrics/db_sink_test.go
git commit -m "feat(metrics): add DBSink implementation"
```

---

## Task 7: Wire `FanSink` into `Prober`

**Files:**
- Modify: `backend/internal/probe/prober.go` (constructor + sample-write site)

- [ ] **Step 1: Extend `Prober` to carry a fan sink**

In `prober.go`, change the struct and constructor:
```go
type Prober struct {
    db                  *gorm.DB
    interval            time.Duration
    failThreshold       int
    concurrency         int
    metricRetentionDays int
    sink                *metrics.FanSink   // NEW
    cancel              context.CancelFunc
    done                chan struct{}
}

func NewProber(db *gorm.DB, interval time.Duration, failThreshold, concurrency int, sink *metrics.FanSink) *Prober {
    return &Prober{
        db:                  db,
        interval:            interval,
        failThreshold:       failThreshold,
        concurrency:         concurrency,
        metricRetentionDays: 7,
        sink:                sink,
        done:                make(chan struct{}),
    }
}
```

Import `xirang/backend/internal/metrics`.

- [ ] **Step 2: Replace the `db.Create(&sample)` block**

Around line 206, after the current `sample := model.NodeMetricSample{...}` creation, replace the `db.Create` path with:
```go
s := metrics.Sample{
    NodeID:      node.ID,
    NodeName:    node.Name,
    SampledAt:   sample.SampledAt,
    CPUPct:      ptrFloat(sample.CpuPct),
    MemPct:      ptrFloat(sample.MemPct),
    DiskPct:     ptrFloat(sample.DiskPct),
    Load1:       ptrFloat(sample.Load1m),
    DiskGBUsed:  sample.DiskGBUsed,
    DiskGBTotal: sample.DiskGBTotal,
    ProbeOK:     sample.ProbeOK,
}
if sample.LatencyMs != nil {
    lat := float64(*sample.LatencyMs)
    s.LatencyMs = &lat
}
p.sink.Write(ctx, s)
```

Add a helper at the bottom of `prober.go`:
```go
func ptrFloat(v float64) *float64 { return &v }
```

- [ ] **Step 3: Update every `NewProber(...)` caller**

Search:
```
cd backend && grep -rn "NewProber(" --include="*.go"
```
Update each call site (likely only `cmd/server/main.go` and one test) to pass a `*metrics.FanSink`. In `main.go`, create the sink shortly before constructing the prober — for now just `metrics.NewFanSink(metrics.NewDBSink(db))`.

- [ ] **Step 4: Run existing prober tests**

```
cd backend && go test ./internal/probe/... ./cmd/... -count=1
```
Expected: PASS. If a test constructs `Prober` directly, pass `metrics.NewFanSink(metrics.NewDBSink(db))` there too.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/probe/prober.go backend/cmd/server/main.go
git commit -m "refactor(metrics): route prober writes through FanSink"
```

---

## Task 8: Hourly rollup SQL (pure function, TDD)

**Files:**
- Create: `backend/internal/metrics/aggregator.go` (first part — just the rollup function)
- Create: `backend/internal/metrics/aggregator_test.go`

- [ ] **Step 1: Write the failing test**

`aggregator_test.go`:
```go
package metrics

import (
    "context"
    "testing"
    "time"

    "xirang/backend/internal/model"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"
)

func newAggTestDB(t *testing.T) *gorm.DB {
    t.Helper()
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    if err != nil {
        t.Fatalf("open: %v", err)
    }
    if err := db.AutoMigrate(&model.NodeMetricSample{}, &model.NodeMetricSampleHourly{}, &model.NodeMetricSampleDaily{}); err != nil {
        t.Fatalf("migrate: %v", err)
    }
    return db
}

func TestRollupHourly_FillsBucket(t *testing.T) {
    db := newAggTestDB(t)
    base := time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC)
    // Seed 3 raw samples in the 10:00 bucket.
    for i := 0; i < 3; i++ {
        cpu := 10.0 + float64(i)*10 // 10, 20, 30
        lat := 100 + i*10
        db.Create(&model.NodeMetricSample{
            NodeID: 1, CpuPct: cpu, MemPct: 50, DiskPct: 40, Load1m: 0.5,
            LatencyMs: &lat, ProbeOK: true,
            SampledAt: base.Add(time.Duration(i) * 10 * time.Minute),
        })
    }
    agg := &Aggregator{db: db, dialect: "sqlite"}

    n, err := agg.rollupHourly(context.Background(), base, base.Add(time.Hour))
    if err != nil {
        t.Fatalf("rollup: %v", err)
    }
    if n != 1 {
        t.Fatalf("expected 1 bucket written, got %d", n)
    }
    var got model.NodeMetricSampleHourly
    if err := db.First(&got, "node_id = ?", 1).Error; err != nil {
        t.Fatalf("read back: %v", err)
    }
    if got.CpuPctAvg == nil || *got.CpuPctAvg != 20 {
        t.Fatalf("expected cpu_pct_avg=20, got %v", got.CpuPctAvg)
    }
    if got.CpuPctMax == nil || *got.CpuPctMax != 30 {
        t.Fatalf("expected cpu_pct_max=30, got %v", got.CpuPctMax)
    }
    if got.ProbeOK != 3 || got.ProbeFail != 0 || got.SampleCount != 3 {
        t.Fatalf("bad counts: ok=%d fail=%d total=%d", got.ProbeOK, got.ProbeFail, got.SampleCount)
    }
}

func TestRollupHourly_Idempotent(t *testing.T) {
    db := newAggTestDB(t)
    base := time.Date(2026, 4, 17, 11, 0, 0, 0, time.UTC)
    db.Create(&model.NodeMetricSample{
        NodeID: 1, CpuPct: 50, ProbeOK: true, SampledAt: base.Add(15 * time.Minute),
    })
    agg := &Aggregator{db: db, dialect: "sqlite"}
    if _, err := agg.rollupHourly(context.Background(), base, base.Add(time.Hour)); err != nil {
        t.Fatalf("first rollup: %v", err)
    }
    // Add another raw sample, rerun — ON CONFLICT DO UPDATE should refresh aggregates.
    db.Create(&model.NodeMetricSample{
        NodeID: 1, CpuPct: 150, ProbeOK: true, SampledAt: base.Add(30 * time.Minute),
    })
    if _, err := agg.rollupHourly(context.Background(), base, base.Add(time.Hour)); err != nil {
        t.Fatalf("second rollup: %v", err)
    }
    var got model.NodeMetricSampleHourly
    db.First(&got, "node_id = ?", 1)
    if got.CpuPctMax == nil || *got.CpuPctMax != 150 {
        t.Fatalf("expected max to update to 150, got %v", got.CpuPctMax)
    }
}
```

- [ ] **Step 2: Run test — expect compile failure**

```
cd backend && go test ./internal/metrics/... -count=1 -run Rollup
```
Expected: `Aggregator undefined`.

- [ ] **Step 3: Implement `Aggregator` and `rollupHourly`**

`aggregator.go`:
```go
package metrics

import (
    "context"
    "fmt"
    "sync"
    "time"

    "gorm.io/gorm"
)

type Aggregator struct {
    db      *gorm.DB
    dialect string // "sqlite" or "postgres"
    mu      sync.Mutex
    cancel  context.CancelFunc
    done    chan struct{}
}

func NewAggregator(db *gorm.DB, dialect string) *Aggregator {
    return &Aggregator{db: db, dialect: dialect, done: make(chan struct{})}
}

// bucketExpr returns the dialect-specific SQL to truncate sampled_at to the given unit.
func (a *Aggregator) bucketExpr(unit string) string {
    if a.dialect == "postgres" {
        return fmt.Sprintf("date_trunc('%s', sampled_at)", unit)
    }
    // SQLite
    switch unit {
    case "hour":
        return "datetime(strftime('%Y-%m-%d %H:00:00', sampled_at))"
    case "day":
        return "datetime(strftime('%Y-%m-%d 00:00:00', sampled_at))"
    }
    return "sampled_at"
}

// rollupHourly aggregates raw samples with sampled_at in [from, to) into hourly buckets.
// Returns number of buckets written.
func (a *Aggregator) rollupHourly(ctx context.Context, from, to time.Time) (int, error) {
    query := fmt.Sprintf(`
        INSERT INTO node_metric_samples_hourly (
            node_id, bucket_start,
            cpu_pct_avg, cpu_pct_max,
            mem_pct_avg, mem_pct_max,
            disk_pct_avg, disk_pct_max,
            load1_avg, load1_max,
            latency_ms_avg, latency_ms_max,
            disk_gb_used_avg, disk_gb_total,
            probe_ok, probe_fail, sample_count, created_at
        )
        SELECT
            node_id,
            %[1]s AS bucket_start,
            AVG(cpu_pct), MAX(cpu_pct),
            AVG(mem_pct), MAX(mem_pct),
            AVG(disk_pct), MAX(disk_pct),
            AVG(load_1m),  MAX(load_1m),
            AVG(latency_ms), MAX(latency_ms),
            AVG(disk_gb_used),
            MAX(disk_gb_total),
            SUM(CASE WHEN probe_ok THEN 1 ELSE 0 END),
            SUM(CASE WHEN probe_ok THEN 0 ELSE 1 END),
            COUNT(*),
            CURRENT_TIMESTAMP
        FROM node_metric_samples
        WHERE sampled_at >= ? AND sampled_at < ?
        GROUP BY node_id, %[1]s
        ON CONFLICT (node_id, bucket_start) DO UPDATE SET
            cpu_pct_avg = excluded.cpu_pct_avg,
            cpu_pct_max = excluded.cpu_pct_max,
            mem_pct_avg = excluded.mem_pct_avg,
            mem_pct_max = excluded.mem_pct_max,
            disk_pct_avg = excluded.disk_pct_avg,
            disk_pct_max = excluded.disk_pct_max,
            load1_avg = excluded.load1_avg,
            load1_max = excluded.load1_max,
            latency_ms_avg = excluded.latency_ms_avg,
            latency_ms_max = excluded.latency_ms_max,
            disk_gb_used_avg = excluded.disk_gb_used_avg,
            disk_gb_total = excluded.disk_gb_total,
            probe_ok = excluded.probe_ok,
            probe_fail = excluded.probe_fail,
            sample_count = excluded.sample_count
    `, a.bucketExpr("hour"))

    result := a.db.WithContext(ctx).Exec(query, from, to)
    if result.Error != nil {
        return 0, result.Error
    }
    return int(result.RowsAffected), nil
}
```

Note: `MAX(disk_gb_total)` approximates the spec's "terminal value" — acceptable since `disk_gb_total` rarely changes within a window; if it does (disk resize), the larger value wins.

- [ ] **Step 4: Run tests — expect PASS**

```
cd backend && go test ./internal/metrics/... -count=1 -run Rollup
```
Expected: PASS for both.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/metrics/aggregator.go backend/internal/metrics/aggregator_test.go
git commit -m "feat(metrics): hourly rollup aggregator with idempotent upsert"
```

---

## Task 9: Daily rollup SQL

**Files:**
- Modify: `backend/internal/metrics/aggregator.go`
- Modify: `backend/internal/metrics/aggregator_test.go`

- [ ] **Step 1: Write the failing test**

Append to `aggregator_test.go`:
```go
func TestRollupDaily_FromHourly(t *testing.T) {
    db := newAggTestDB(t)
    day := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
    // Seed 24 hourly buckets in the day.
    for h := 0; h < 24; h++ {
        cpuAvg := float64(h)
        cpuMax := float64(h) + 5
        db.Create(&model.NodeMetricSampleHourly{
            NodeID: 1,
            BucketStart: day.Add(time.Duration(h) * time.Hour),
            CpuPctAvg: &cpuAvg, CpuPctMax: &cpuMax,
            ProbeOK: 10, ProbeFail: 0, SampleCount: 10,
        })
    }
    agg := &Aggregator{db: db, dialect: "sqlite"}

    n, err := agg.rollupDaily(context.Background(), day, day.Add(24*time.Hour))
    if err != nil {
        t.Fatalf("rollup: %v", err)
    }
    if n != 1 {
        t.Fatalf("expected 1 day bucket, got %d", n)
    }
    var got model.NodeMetricSampleDaily
    db.First(&got, "node_id = ?", 1)
    if got.CpuPctAvg == nil || *got.CpuPctAvg != 11.5 { // avg of 0..23
        t.Fatalf("expected cpu_pct_avg=11.5, got %v", got.CpuPctAvg)
    }
    if got.CpuPctMax == nil || *got.CpuPctMax != 28 { // max of 0..23 + 5
        t.Fatalf("expected cpu_pct_max=28, got %v", got.CpuPctMax)
    }
}
```

- [ ] **Step 2: Implement `rollupDaily`**

Append to `aggregator.go`:
```go
func (a *Aggregator) rollupDaily(ctx context.Context, from, to time.Time) (int, error) {
    query := fmt.Sprintf(`
        INSERT INTO node_metric_samples_daily (
            node_id, bucket_start,
            cpu_pct_avg, cpu_pct_max,
            mem_pct_avg, mem_pct_max,
            disk_pct_avg, disk_pct_max,
            load1_avg, load1_max,
            latency_ms_avg, latency_ms_max,
            disk_gb_used_avg, disk_gb_total,
            probe_ok, probe_fail, sample_count, created_at
        )
        SELECT
            node_id,
            %[1]s AS bucket_start,
            AVG(cpu_pct_avg), MAX(cpu_pct_max),
            AVG(mem_pct_avg), MAX(mem_pct_max),
            AVG(disk_pct_avg), MAX(disk_pct_max),
            AVG(load1_avg),    MAX(load1_max),
            AVG(latency_ms_avg), MAX(latency_ms_max),
            AVG(disk_gb_used_avg),
            MAX(disk_gb_total),
            SUM(probe_ok), SUM(probe_fail), SUM(sample_count),
            CURRENT_TIMESTAMP
        FROM node_metric_samples_hourly
        WHERE bucket_start >= ? AND bucket_start < ?
        GROUP BY node_id, %[1]s
        ON CONFLICT (node_id, bucket_start) DO UPDATE SET
            cpu_pct_avg = excluded.cpu_pct_avg,
            cpu_pct_max = excluded.cpu_pct_max,
            mem_pct_avg = excluded.mem_pct_avg,
            mem_pct_max = excluded.mem_pct_max,
            disk_pct_avg = excluded.disk_pct_avg,
            disk_pct_max = excluded.disk_pct_max,
            load1_avg = excluded.load1_avg,
            load1_max = excluded.load1_max,
            latency_ms_avg = excluded.latency_ms_avg,
            latency_ms_max = excluded.latency_ms_max,
            disk_gb_used_avg = excluded.disk_gb_used_avg,
            disk_gb_total = excluded.disk_gb_total,
            probe_ok = excluded.probe_ok,
            probe_fail = excluded.probe_fail,
            sample_count = excluded.sample_count
    `, a.bucketExpr("day"))
    result := a.db.WithContext(ctx).Exec(query, from, to)
    if result.Error != nil {
        return 0, result.Error
    }
    return int(result.RowsAffected), nil
}
```

Note: the daily bucket expression operates on `bucket_start` from hourly, not `sampled_at`. Adjust `bucketExpr` to accept a column name:

Refactor `bucketExpr`:
```go
func (a *Aggregator) bucketExpr(unit, col string) string {
    if a.dialect == "postgres" {
        return fmt.Sprintf("date_trunc('%s', %s)", unit, col)
    }
    switch unit {
    case "hour":
        return fmt.Sprintf("datetime(strftime('%%Y-%%m-%%d %%H:00:00', %s))", col)
    case "day":
        return fmt.Sprintf("datetime(strftime('%%Y-%%m-%%d 00:00:00', %s))", col)
    }
    return col
}
```

Update `rollupHourly` call site to `a.bucketExpr("hour", "sampled_at")` and `rollupDaily` to `a.bucketExpr("day", "bucket_start")`.

- [ ] **Step 3: Run tests**

```
cd backend && go test ./internal/metrics/... -count=1 -run Rollup
```
Expected: PASS for all three rollup tests.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/metrics/
git commit -m "feat(metrics): daily rollup from hourly aggregates"
```

---

## Task 10: Aggregator worker — backfill + scheduling + self-metrics

**Files:**
- Modify: `backend/internal/metrics/aggregator.go` (add worker loop)
- Create: `backend/internal/metrics/obs.go` (Prometheus gauges)
- Modify: `backend/internal/metrics/aggregator_test.go` (backfill test)

- [ ] **Step 1: Write backfill test**

Append:
```go
func TestAggregator_BackfillsHourlyFromRaw(t *testing.T) {
    db := newAggTestDB(t)
    now := time.Now().UTC().Truncate(time.Hour)
    for h := -3; h <= -1; h++ {
        db.Create(&model.NodeMetricSample{
            NodeID: 1, CpuPct: float64(h) + 50, ProbeOK: true,
            SampledAt: now.Add(time.Duration(h) * time.Hour).Add(15 * time.Minute),
        })
    }
    agg := NewAggregator(db, "sqlite")
    if err := agg.backfill(context.Background()); err != nil {
        t.Fatalf("backfill: %v", err)
    }
    var count int64
    db.Model(&model.NodeMetricSampleHourly{}).Count(&count)
    if count != 3 {
        t.Fatalf("expected 3 hourly buckets after backfill, got %d", count)
    }
}
```

- [ ] **Step 2: Implement `backfill`, `runHourlyTick`, `runDailyTick`, `Start`, `Stop`**

Append to `aggregator.go`:
```go
// backfill runs hourly then daily rollups synchronously from the oldest missing bucket
// up to (now - 5min) / (now - 1d) respectively. Safe to call on a cold boot.
func (a *Aggregator) backfill(ctx context.Context) error {
    if err := a.catchUpHourly(ctx); err != nil {
        return fmt.Errorf("hourly backfill: %w", err)
    }
    return a.catchUpDaily(ctx)
}

func (a *Aggregator) catchUpHourly(ctx context.Context) error {
    a.mu.Lock()
    defer a.mu.Unlock()

    var lastBucket time.Time
    a.db.WithContext(ctx).
        Raw("SELECT COALESCE(MAX(bucket_start), ?) FROM node_metric_samples_hourly", time.Time{}).
        Scan(&lastBucket)

    var oldestSample time.Time
    a.db.WithContext(ctx).
        Raw("SELECT COALESCE(MIN(sampled_at), ?) FROM node_metric_samples", time.Time{}).
        Scan(&oldestSample)

    var from time.Time
    if lastBucket.IsZero() {
        if oldestSample.IsZero() {
            return nil
        }
        from = oldestSample.Truncate(time.Hour)
    } else {
        from = lastBucket.Add(time.Hour)
    }
    end := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Hour)
    for from.Before(end) {
        to := from.Add(time.Hour)
        if _, err := a.rollupHourly(ctx, from, to); err != nil {
            return err
        }
        from = to
    }
    rollupLagSeconds.WithLabelValues("hourly").Set(time.Since(end).Seconds())
    return nil
}

func (a *Aggregator) catchUpDaily(ctx context.Context) error {
    a.mu.Lock()
    defer a.mu.Unlock()

    var lastBucket time.Time
    a.db.WithContext(ctx).
        Raw("SELECT COALESCE(MAX(bucket_start), ?) FROM node_metric_samples_daily", time.Time{}).
        Scan(&lastBucket)

    var oldestHourly time.Time
    a.db.WithContext(ctx).
        Raw("SELECT COALESCE(MIN(bucket_start), ?) FROM node_metric_samples_hourly", time.Time{}).
        Scan(&oldestHourly)

    var from time.Time
    if lastBucket.IsZero() {
        if oldestHourly.IsZero() {
            return nil
        }
        from = oldestHourly.Truncate(24 * time.Hour)
    } else {
        from = lastBucket.Add(24 * time.Hour)
    }
    end := time.Now().UTC().Truncate(24 * time.Hour)
    for from.Before(end) {
        to := from.Add(24 * time.Hour)
        if _, err := a.rollupDaily(ctx, from, to); err != nil {
            return err
        }
        from = to
    }
    rollupLagSeconds.WithLabelValues("daily").Set(time.Since(end).Seconds())
    return nil
}

// Start performs backfill synchronously and then arms the tickers.
func (a *Aggregator) Start(ctx context.Context) error {
    if err := a.backfill(ctx); err != nil {
        return err
    }
    aggCtx, cancel := context.WithCancel(ctx)
    a.cancel = cancel
    go a.loop(aggCtx)
    return nil
}

func (a *Aggregator) loop(ctx context.Context) {
    defer close(a.done)
    hourly := time.NewTicker(1 * time.Minute)
    daily := time.NewTicker(10 * time.Minute)
    defer hourly.Stop()
    defer daily.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-hourly.C:
            a.measureRollup(ctx, "hourly", a.catchUpHourly)
        case <-daily.C:
            a.measureRollup(ctx, "daily", a.catchUpDaily)
        }
    }
}

func (a *Aggregator) measureRollup(ctx context.Context, tier string, fn func(context.Context) error) {
    start := time.Now()
    if err := fn(ctx); err != nil {
        logger.Module("metrics").Warn().Str("tier", tier).Err(err).Msg("rollup failed")
    }
    rollupDurationSeconds.WithLabelValues(tier).Observe(time.Since(start).Seconds())
}

func (a *Aggregator) Stop(ctx context.Context) error {
    if a.cancel != nil {
        a.cancel()
    }
    select {
    case <-a.done:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

Add missing import: `xirang/backend/internal/logger`.

- [ ] **Step 3: Write Prometheus obs**

`obs.go`:
```go
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
    rollupDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
        Name:    "xirang_metric_rollup_duration_seconds",
        Help:    "Duration of one rollup tick per tier.",
        Buckets: prometheus.DefBuckets,
    }, []string{"tier"})
    rollupLagSeconds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "xirang_metric_rollup_lag_seconds",
        Help: "Seconds between the newest aggregated bucket and now.",
    }, []string{"tier"})
    SinkDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "xirang_metric_sink_dropped_total",
        Help: "Samples dropped by a metric sink due to overflow or fatal failure.",
    }, []string{"sink"})
)

func init() {
    prometheus.MustRegister(rollupDurationSeconds, rollupLagSeconds, SinkDropped)
}
```

- [ ] **Step 4: Run tests**

```
cd backend && go test ./internal/metrics/... -count=1
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/metrics/aggregator.go backend/internal/metrics/obs.go backend/internal/metrics/aggregator_test.go
git commit -m "feat(metrics): aggregator worker with backfill, ticker, and self-metrics"
```

---

## Task 11: Wire aggregator into `main.go`

**Files:**
- Modify: `backend/cmd/server/main.go`

- [ ] **Step 1: Start aggregator alongside prober**

After the prober is constructed and started, add:
```go
dialect := "sqlite"
if cfg.Database.Driver == "postgres" {
    dialect = "postgres"
}
aggregator := metrics.NewAggregator(db, dialect)
if err := aggregator.Start(ctx); err != nil {
    logger.Module("main").Error().Err(err).Msg("启动指标聚合器失败")
    // non-fatal: continue without aggregator
}
defer func() {
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    aggregator.Stop(shutdownCtx)
}()
```

Adapt config access to the actual project `config.Config` shape.

- [ ] **Step 2: Build and boot**

```
cd backend && go build ./... && ./xirang
```
Expected: boot logs show prober ticks and eventually a rollup log line (if there are nodes + samples).

- [ ] **Step 3: Commit**

```bash
git add backend/cmd/server/main.go
git commit -m "feat(metrics): wire aggregator lifecycle in main"
```

---

## Task 12: Disk forecast (linear regression, TDD)

**Files:**
- Create: `backend/internal/metrics/forecast.go`
- Create: `backend/internal/metrics/forecast_test.go`

- [ ] **Step 1: Write failing tests**

`forecast_test.go`:
```go
package metrics

import (
    "math"
    "testing"
)

func TestForecast_Insufficient(t *testing.T) {
    pts := []ForecastPoint{{1, 10}, {2, 11}, {3, 12}}
    f := DiskForecast(pts, 100)
    if f.Confidence != ConfidenceInsufficient {
        t.Fatalf("expected insufficient, got %v", f.Confidence)
    }
}

func TestForecast_StrongLinearGrowth(t *testing.T) {
    pts := make([]ForecastPoint, 21)
    for i := 0; i < 21; i++ {
        pts[i] = ForecastPoint{Day: float64(i), DiskGBUsed: 100 + float64(i)*2}
    }
    f := DiskForecast(pts, 200)
    if f.Confidence != ConfidenceHigh {
        t.Fatalf("expected high confidence, got %v", f.Confidence)
    }
    if f.DailyGrowthGB == nil || math.Abs(*f.DailyGrowthGB-2) > 1e-6 {
        t.Fatalf("expected growth ≈2, got %v", f.DailyGrowthGB)
    }
    if f.DaysToFull == nil || *f.DaysToFull <= 0 {
        t.Fatalf("expected positive days_to_full, got %v", f.DaysToFull)
    }
}

func TestForecast_NegativeSlope(t *testing.T) {
    pts := make([]ForecastPoint, 14)
    for i := 0; i < 14; i++ {
        pts[i] = ForecastPoint{Day: float64(i), DiskGBUsed: 200 - float64(i)*0.5}
    }
    f := DiskForecast(pts, 300)
    if f.DaysToFull != nil {
        t.Fatalf("expected nil days_to_full on negative slope, got %v", *f.DaysToFull)
    }
}

func TestForecast_NoisyLowConfidence(t *testing.T) {
    pts := []ForecastPoint{{1, 50}, {2, 52}, {3, 48}, {4, 51}, {5, 49}, {6, 53}, {7, 50}}
    f := DiskForecast(pts, 100)
    if f.Confidence != ConfidenceLow {
        t.Fatalf("expected low confidence for 7 noisy samples, got %v", f.Confidence)
    }
}
```

- [ ] **Step 2: Implement `DiskForecast`**

`forecast.go`:
```go
package metrics

type Confidence string

const (
    ConfidenceHigh         Confidence = "high"
    ConfidenceMedium       Confidence = "medium"
    ConfidenceLow          Confidence = "low"
    ConfidenceInsufficient Confidence = "insufficient"
)

type ForecastPoint struct {
    Day        float64 // days since first point (or any monotonic x)
    DiskGBUsed float64
}

type ForecastResult struct {
    DailyGrowthGB *float64
    DaysToFull    *float64
    Confidence    Confidence
    RSquared      float64
}

// DiskForecast runs a simple least-squares linear regression. Returns nil DaysToFull
// on non-positive slope or insufficient data.
func DiskForecast(points []ForecastPoint, diskGBTotal float64) ForecastResult {
    n := len(points)
    if n < 7 {
        return ForecastResult{Confidence: ConfidenceInsufficient}
    }
    var sumX, sumY, sumXY, sumXX, sumYY float64
    for _, p := range points {
        sumX += p.Day
        sumY += p.DiskGBUsed
        sumXY += p.Day * p.DiskGBUsed
        sumXX += p.Day * p.Day
        sumYY += p.DiskGBUsed * p.DiskGBUsed
    }
    fn := float64(n)
    denom := fn*sumXX - sumX*sumX
    if denom == 0 {
        return ForecastResult{Confidence: ConfidenceInsufficient}
    }
    slope := (fn*sumXY - sumX*sumY) / denom
    intercept := (sumY - slope*sumX) / fn
    // r²
    var ssTot, ssRes float64
    meanY := sumY / fn
    for _, p := range points {
        pred := slope*p.Day + intercept
        ssRes += (p.DiskGBUsed - pred) * (p.DiskGBUsed - pred)
        ssTot += (p.DiskGBUsed - meanY) * (p.DiskGBUsed - meanY)
    }
    var r2 float64
    if ssTot > 0 {
        r2 = 1 - ssRes/ssTot
    }

    conf := ConfidenceLow
    if n >= 14 && r2 >= 0.3 {
        conf = ConfidenceMedium
    }
    if n >= 21 && r2 >= 0.7 {
        conf = ConfidenceHigh
    }

    result := ForecastResult{DailyGrowthGB: &slope, Confidence: conf, RSquared: r2}
    if slope > 0 {
        lastY := points[n-1].DiskGBUsed
        days := (diskGBTotal - lastY) / slope
        result.DaysToFull = &days
    }
    return result
}
```

- [ ] **Step 3: Run tests — expect PASS**

```
cd backend && go test ./internal/metrics/... -count=1 -run Forecast
```

- [ ] **Step 4: Commit**

```bash
git add backend/internal/metrics/forecast.go backend/internal/metrics/forecast_test.go
git commit -m "feat(metrics): disk usage linear-regression forecast"
```

---

## Task 13: Granularity selector (pure function, TDD)

**Files:**
- Create: `backend/internal/metrics/granularity.go`
- Create: `backend/internal/metrics/granularity_test.go`

- [ ] **Step 1: Write failing tests**

`granularity_test.go`:
```go
package metrics

import (
    "testing"
    "time"
)

func TestSelectGranularity(t *testing.T) {
    cases := []struct {
        span time.Duration
        want Granularity
    }{
        {1 * time.Hour, GranularityRaw},
        {6 * time.Hour, GranularityRaw},
        {3 * 24 * time.Hour, GranularityRaw},
        {7 * 24 * time.Hour, GranularityHourly},
        {60 * 24 * time.Hour, GranularityHourly},
        {120 * 24 * time.Hour, GranularityDaily},
        {400 * 24 * time.Hour, GranularityDaily},
    }
    for _, c := range cases {
        got := SelectGranularity(c.span)
        if got != c.want {
            t.Errorf("span=%v want=%s got=%s", c.span, c.want, got)
        }
    }
}
```

- [ ] **Step 2: Implement**

`granularity.go`:
```go
package metrics

import "time"

type Granularity string

const (
    GranularityRaw    Granularity = "raw"
    GranularityHourly Granularity = "hourly"
    GranularityDaily  Granularity = "daily"
)

// SelectGranularity picks a tier based on the requested time span.
// Spec § "auto selection":
//   ≤ 6h       → raw
//   6h–3d      → raw (downsampled client-side if > 1500 points)
//   3d–90d     → hourly
//   > 90d      → daily
func SelectGranularity(span time.Duration) Granularity {
    day := 24 * time.Hour
    switch {
    case span <= 3*day:
        return GranularityRaw
    case span <= 90*day:
        return GranularityHourly
    default:
        return GranularityDaily
    }
}
```

- [ ] **Step 3: Run tests — PASS**

```
cd backend && go test ./internal/metrics/... -count=1 -run Granularity
```

- [ ] **Step 4: Commit**

```bash
git add backend/internal/metrics/granularity.go backend/internal/metrics/granularity_test.go
git commit -m "feat(metrics): granularity auto-selection function"
```

---

## Task 14: API handler — `GET /nodes/:id/status`

**Files:**
- Create: `backend/internal/api/handlers/node_metrics_handler.go` (first handler)
- Create: `backend/internal/api/handlers/node_metrics_handler_test.go`

- [ ] **Step 1: Integration test**

Follow existing pattern (see `overview_handler_test.go`). Seed a node + several `NodeMetricSample` rows + hourly/daily rows, hit `GET /api/v1/nodes/:id/status`, assert response shape.

Example test body (adapt to project's HTTP test harness):
```go
func TestGetNodeStatus_ReturnsCurrentAndTrends(t *testing.T) {
    env := newTestEnv(t)  // reuse project helper
    defer env.Close()
    node := seedNode(env, "web-01")
    now := time.Now().UTC()
    // seed 3 recent raw samples
    for i := 0; i < 3; i++ {
        env.DB.Create(&model.NodeMetricSample{
            NodeID: node.ID, CpuPct: 10 + float64(i), MemPct: 50, DiskPct: 40, Load1m: 0.3,
            ProbeOK: true, SampledAt: now.Add(-time.Duration(i) * time.Minute),
        })
    }
    w := env.GET(fmt.Sprintf("/api/v1/nodes/%d/status", node.ID))
    if w.Code != 200 {
        t.Fatalf("status %d body=%s", w.Code, w.Body.String())
    }
    var resp struct {
        Online  bool `json:"online"`
        Current struct {
            CPUPct float64 `json:"cpu_pct"`
        } `json:"current"`
    }
    json.NewDecoder(w.Body).Decode(&resp)
    if !resp.Online {
        t.Fatalf("expected online")
    }
    if resp.Current.CPUPct < 10 {
        t.Fatalf("current cpu_pct too low: %f", resp.Current.CPUPct)
    }
}
```

- [ ] **Step 2: Implement handler**

`node_metrics_handler.go`:
```go
package handlers

import (
    "net/http"
    "strconv"
    "time"

    "xirang/backend/internal/model"

    "github.com/gin-gonic/gin"
    "gorm.io/gorm"
)

type NodeMetricsHandler struct{ db *gorm.DB }

func NewNodeMetricsHandler(db *gorm.DB) *NodeMetricsHandler {
    return &NodeMetricsHandler{db: db}
}

type nodeStatusResponse struct {
    ProbedAt     *time.Time          `json:"probed_at"`
    Online       bool                `json:"online"`
    Current      map[string]float64  `json:"current"`
    Trend1h      map[string]float64  `json:"trend_1h"`
    Trend24h     map[string]float64  `json:"trend_24h"`
    OpenAlerts   int64               `json:"open_alerts"`
    RunningTasks int64               `json:"running_tasks"`
}

func (h *NodeMetricsHandler) Status(c *gin.Context) {
    id, err := strconv.ParseUint(c.Param("id"), 10, 64)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
        return
    }
    resp := nodeStatusResponse{Current: map[string]float64{}, Trend1h: map[string]float64{}, Trend24h: map[string]float64{}}

    var latest model.NodeMetricSample
    if err := h.db.Where("node_id = ?", id).Order("sampled_at desc").First(&latest).Error; err == nil {
        resp.ProbedAt = &latest.SampledAt
        resp.Online = latest.ProbeOK
        resp.Current["cpu_pct"] = latest.CpuPct
        resp.Current["mem_pct"] = latest.MemPct
        resp.Current["disk_pct"] = latest.DiskPct
        resp.Current["load1"] = latest.Load1m
        if latest.LatencyMs != nil {
            resp.Current["latency_ms"] = float64(*latest.LatencyMs)
        }
    }

    now := time.Now().UTC()
    h.trendFromHourly(uint(id), now.Add(-1*time.Hour), now, resp.Trend1h)
    h.trendFromHourly(uint(id), now.Add(-24*time.Hour), now, resp.Trend24h)

    h.db.Model(&model.Alert{}).Where("node_id = ? AND status IN ?", id, []string{"open", "unacknowledged"}).Count(&resp.OpenAlerts)
    h.db.Model(&model.TaskRun{}).Where("node_id = ? AND status = ?", id, "running").Count(&resp.RunningTasks)

    c.JSON(http.StatusOK, resp)
}

func (h *NodeMetricsHandler) trendFromHourly(nodeID uint, from, to time.Time, dst map[string]float64) {
    var rows []model.NodeMetricSampleHourly
    h.db.Where("node_id = ? AND bucket_start >= ? AND bucket_start < ?", nodeID, from, to).Find(&rows)
    if len(rows) == 0 {
        return
    }
    var cpu, mem, disk, load float64
    var okSum, totalSum int64
    for _, r := range rows {
        if r.CpuPctAvg != nil { cpu += *r.CpuPctAvg }
        if r.MemPctAvg != nil { mem += *r.MemPctAvg }
        if r.DiskPctAvg != nil { disk += *r.DiskPctAvg }
        if r.Load1Avg != nil { load += *r.Load1Avg }
        okSum += r.ProbeOK
        totalSum += r.SampleCount
    }
    n := float64(len(rows))
    dst["cpu_pct_avg"] = cpu / n
    dst["mem_pct_avg"] = mem / n
    dst["disk_pct_avg"] = disk / n
    dst["load1_avg"] = load / n
    if totalSum > 0 {
        dst["probe_ok_ratio"] = float64(okSum) / float64(totalSum)
    }
}
```

Field names for alert/task-run conditions (`"open"`, `"unacknowledged"`, `"running"`) — verify against `alert_handler.go` and `task_handler.go`. Adjust if those packages use enums.

- [ ] **Step 3: Register route in `router.go`**

Inside the `secured` group, add:
```go
nodeMetricsHandler := handlers.NewNodeMetricsHandler(db)
secured.GET("/nodes/:id/status", nodeMetricsHandler.Status)
```

- [ ] **Step 4: Run handler test**

```
cd backend && go test ./internal/api/handlers/... -count=1 -run NodeStatus
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/handlers/ backend/internal/api/router.go
git commit -m "feat(api): GET /nodes/:id/status endpoint"
```

---

## Task 15: API handler — `GET /nodes/:id/metrics`

**Files:**
- Modify: `backend/internal/api/handlers/node_metrics_handler.go`
- Modify: `backend/internal/api/handlers/node_metrics_handler_test.go`
- Modify: `backend/internal/api/router.go`

- [ ] **Step 1: Integration test — granularity=auto picks hourly for 7d span**

Append:
```go
func TestGetNodeMetrics_AutoPicksHourly_For7dSpan(t *testing.T) {
    env := newTestEnv(t)
    defer env.Close()
    node := seedNode(env, "web-01")
    // seed 7 hourly buckets
    now := time.Now().UTC().Truncate(time.Hour)
    for i := 0; i < 48; i++ {
        avg := 20.0 + float64(i)
        env.DB.Create(&model.NodeMetricSampleHourly{
            NodeID: node.ID, BucketStart: now.Add(-time.Duration(i) * time.Hour),
            CpuPctAvg: &avg, CpuPctMax: &avg,
            ProbeOK: 10, SampleCount: 10,
        })
    }
    from := now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
    to := now.Format(time.RFC3339)
    w := env.GET(fmt.Sprintf("/api/v1/nodes/%d/metrics?from=%s&to=%s&fields=cpu_pct", node.ID, from, to))
    if w.Code != 200 {
        t.Fatalf("status %d", w.Code)
    }
    var resp struct {
        Granularity   string `json:"granularity"`
        BucketSeconds int    `json:"bucket_seconds"`
    }
    json.NewDecoder(w.Body).Decode(&resp)
    if resp.Granularity != "hourly" {
        t.Fatalf("expected hourly, got %s", resp.Granularity)
    }
    if resp.BucketSeconds != 3600 {
        t.Fatalf("expected bucket_seconds=3600, got %d", resp.BucketSeconds)
    }
}
```

- [ ] **Step 2: Implement handler**

Append to `node_metrics_handler.go`:
```go
type metricPoint struct {
    T   time.Time `json:"t"`
    Avg *float64  `json:"avg,omitempty"`
    Max *float64  `json:"max,omitempty"`
    V   *float64  `json:"v,omitempty"`
}

type metricSeries struct {
    Metric string        `json:"metric"`
    Unit   string        `json:"unit"`
    Points []metricPoint `json:"points"`
}

type metricsResponse struct {
    Granularity   string         `json:"granularity"`
    BucketSeconds int            `json:"bucket_seconds"`
    Series        []metricSeries `json:"series"`
}

var unitOf = map[metrics.Field]string{
    metrics.FieldCPUPct: "percent", metrics.FieldMemPct: "percent", metrics.FieldDiskPct: "percent",
    metrics.FieldLoad1: "load", metrics.FieldLatencyMs: "ms", metrics.FieldDiskGBUsed: "gb",
    metrics.FieldProbeOKRatio: "ratio",
}

func (h *NodeMetricsHandler) Metrics(c *gin.Context) {
    id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
    from, errFrom := time.Parse(time.RFC3339, c.Query("from"))
    to, errTo := time.Parse(time.RFC3339, c.Query("to"))
    if errFrom != nil || errTo != nil || !to.After(from) {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid from/to"})
        return
    }
    fields := resolveFields(c.Query("fields"))

    gr := c.DefaultQuery("granularity", "auto")
    chosen := metrics.Granularity(gr)
    if gr == "auto" {
        chosen = metrics.SelectGranularity(to.Sub(from))
    }
    resp := metricsResponse{Granularity: string(chosen)}
    switch chosen {
    case metrics.GranularityRaw:
        resp.BucketSeconds = 0
        resp.Series = h.rawSeries(uint(id), from, to, fields)
    case metrics.GranularityHourly:
        resp.BucketSeconds = 3600
        resp.Series = h.bucketSeries(uint(id), from, to, fields, "node_metric_samples_hourly", "bucket_start")
    case metrics.GranularityDaily:
        resp.BucketSeconds = 86400
        resp.Series = h.bucketSeries(uint(id), from, to, fields, "node_metric_samples_daily", "bucket_start")
    default:
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid granularity"})
        return
    }
    c.JSON(http.StatusOK, resp)
}

func resolveFields(raw string) []metrics.Field {
    if raw == "" {
        return metrics.AllFields
    }
    out := []metrics.Field{}
    for _, s := range strings.Split(raw, ",") {
        if s = strings.TrimSpace(s); s != "" {
            out = append(out, metrics.Field(s))
        }
    }
    return out
}

// rawSeries and bucketSeries are internal helpers that fetch rows and shape
// them into metricPoint lists. bucketSeries selects avg/max columns based
// on field name; rawSeries returns a single value.
```

Add the helpers below (shape queries keyed by field → column mapping). Import `xirang/backend/internal/metrics` and `strings`.

Implement `rawSeries` with server-side downsampling (spec cap: 1500 points per series):
```go
const rawMaxPointsPerSeries = 1500

func (h *NodeMetricsHandler) rawSeries(nodeID uint, from, to time.Time, fields []metrics.Field) []metricSeries {
    var rows []model.NodeMetricSample
    h.db.Where("node_id = ? AND sampled_at >= ? AND sampled_at < ?", nodeID, from, to).
        Order("sampled_at ASC").Find(&rows)

    stride := 1
    if len(rows) > rawMaxPointsPerSeries {
        stride = (len(rows) + rawMaxPointsPerSeries - 1) / rawMaxPointsPerSeries
    }

    out := make([]metricSeries, 0, len(fields))
    for _, f := range fields {
        pts := make([]metricPoint, 0, len(rows)/stride+1)
        for i, r := range rows {
            if i%stride != 0 && i != len(rows)-1 { // keep last point
                continue
            }
            v := rawFieldValue(r, f)
            if v == nil {
                continue
            }
            val := *v
            pts = append(pts, metricPoint{T: r.SampledAt, V: &val})
        }
        out = append(out, metricSeries{Metric: string(f), Unit: unitOf[f], Points: pts})
    }
    return out
}

func rawFieldValue(r model.NodeMetricSample, f metrics.Field) *float64 {
    switch f {
    case metrics.FieldCPUPct:
        v := r.CpuPct; return &v
    case metrics.FieldMemPct:
        v := r.MemPct; return &v
    case metrics.FieldDiskPct:
        v := r.DiskPct; return &v
    case metrics.FieldLoad1:
        v := r.Load1m; return &v
    case metrics.FieldLatencyMs:
        if r.LatencyMs != nil {
            v := float64(*r.LatencyMs); return &v
        }
    case metrics.FieldDiskGBUsed:
        return r.DiskGBUsed
    case metrics.FieldProbeOKRatio:
        var v float64 = 0
        if r.ProbeOK { v = 1 }
        return &v
    }
    return nil
}
```

Implement `bucketSeries` using the same pattern against `NodeMetricSampleHourly` / `NodeMetricSampleDaily` (share a single function taking a row accessor); wire avg + max per field.

Register route in `router.go`:
```go
secured.GET("/nodes/:id/metrics", nodeMetricsHandler.Metrics)
```

- [ ] **Step 3: Run tests**

```
cd backend && go test ./internal/api/handlers/... -count=1 -run NodeMetrics
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/handlers/ backend/internal/api/router.go
git commit -m "feat(api): GET /nodes/:id/metrics with auto-granularity"
```

---

## Task 16: API handler — `GET /nodes/:id/disk-forecast`

**Files:**
- Modify: `backend/internal/api/handlers/node_metrics_handler.go`
- Modify: `backend/internal/api/handlers/node_metrics_handler_test.go`
- Modify: `backend/internal/api/router.go`

- [ ] **Step 1: Integration test**

Append: seed ≥ 21 daily rows with a clear slope; GET `/nodes/:id/disk-forecast`; assert `confidence=high` and `days_to_full > 0`.

- [ ] **Step 2: Implement `DiskForecast` handler**

```go
type diskForecastResponse struct {
    DiskGBTotal  float64 `json:"disk_gb_total"`
    DiskGBUsedNow float64 `json:"disk_gb_used_now"`
    DailyGrowthGB *float64 `json:"daily_growth_gb"`
    Forecast struct {
        DaysToFull *float64 `json:"days_to_full"`
        DateFull   *string  `json:"date_full"`
        Confidence string   `json:"confidence"`
    } `json:"forecast"`
}

func (h *NodeMetricsHandler) DiskForecast(c *gin.Context) {
    id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
    var rows []model.NodeMetricSampleDaily
    cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
    h.db.Where("node_id = ? AND bucket_start >= ?", id, cutoff).
        Order("bucket_start ASC").Find(&rows)

    if len(rows) == 0 {
        c.JSON(http.StatusOK, gin.H{"forecast": gin.H{"confidence": "insufficient"}})
        return
    }
    pts := make([]metrics.ForecastPoint, 0, len(rows))
    t0 := rows[0].BucketStart
    var total, lastUsed float64
    for _, r := range rows {
        if r.DiskGBUsedAvg == nil { continue }
        day := r.BucketStart.Sub(t0).Hours() / 24
        pts = append(pts, metrics.ForecastPoint{Day: day, DiskGBUsed: *r.DiskGBUsedAvg})
        lastUsed = *r.DiskGBUsedAvg
        if r.DiskGBTotal != nil { total = *r.DiskGBTotal }
    }
    f := metrics.DiskForecast(pts, total)

    resp := diskForecastResponse{DiskGBTotal: total, DiskGBUsedNow: lastUsed, DailyGrowthGB: f.DailyGrowthGB}
    resp.Forecast.Confidence = string(f.Confidence)
    if f.DaysToFull != nil && *f.DaysToFull > 0 {
        resp.Forecast.DaysToFull = f.DaysToFull
        when := time.Now().UTC().Add(time.Duration(*f.DaysToFull * 24) * time.Hour).Format("2006-01-02")
        resp.Forecast.DateFull = &when
    }
    c.JSON(http.StatusOK, resp)
}
```

Register route:
```go
secured.GET("/nodes/:id/disk-forecast", nodeMetricsHandler.DiskForecast)
```

- [ ] **Step 3: Run test + commit**

```
cd backend && go test ./internal/api/handlers/... -count=1 -run DiskForecast
git add backend/internal/api/handlers/ backend/internal/api/router.go
git commit -m "feat(api): GET /nodes/:id/disk-forecast endpoint"
```

---

## Task 17: API handler — `GET /admin/metrics/rollup-status`

**Files:**
- Create: `backend/internal/api/handlers/admin_metrics_handler.go`
- Modify: `backend/internal/api/router.go`

- [ ] **Step 1: Implement handler (diagnostic only, no write)**

```go
package handlers

import (
    "net/http"
    "time"

    "xirang/backend/internal/model"

    "github.com/gin-gonic/gin"
    "gorm.io/gorm"
)

type AdminMetricsHandler struct{ db *gorm.DB }

func NewAdminMetricsHandler(db *gorm.DB) *AdminMetricsHandler {
    return &AdminMetricsHandler{db: db}
}

func (h *AdminMetricsHandler) RollupStatus(c *gin.Context) {
    now := time.Now().UTC()
    var hourlyLatest, dailyLatest time.Time
    h.db.Model(&model.NodeMetricSampleHourly{}).Select("COALESCE(MAX(bucket_start), ?)", time.Time{}).Scan(&hourlyLatest)
    h.db.Model(&model.NodeMetricSampleDaily{}).Select("COALESCE(MAX(bucket_start), ?)", time.Time{}).Scan(&dailyLatest)

    c.JSON(http.StatusOK, gin.H{
        "hourly": gin.H{
            "latest_bucket": hourlyLatest,
            "lag_seconds":   int(now.Sub(hourlyLatest).Seconds()),
        },
        "daily": gin.H{
            "latest_bucket": dailyLatest,
            "lag_seconds":   int(now.Sub(dailyLatest).Seconds()),
        },
    })
}
```

Register inside the admin route group (follow existing admin route convention in `router.go`):
```go
adminGroup := secured.Group("/admin", middleware.RequireRole("admin"))
adminGroup.GET("/metrics/rollup-status", adminMetricsHandler.RollupStatus)
```

- [ ] **Step 2: Smoke-test via curl**

```
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/admin/metrics/rollup-status
```
Expected JSON with the two tiers.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/api/handlers/admin_metrics_handler.go backend/internal/api/router.go
git commit -m "feat(api): admin rollup-status diagnostic endpoint"
```

---

## Task 18: Optional `RemoteWriteSink`

This task is **independently deployable** — skip if shipping MVP without remote write.

**Files:**
- Create: `backend/internal/metrics/remote_write_sink.go`
- Create: `backend/internal/metrics/remote_write_sink_test.go`
- Modify: `backend/cmd/server/main.go` (wire based on settings)
- Modify: settings UI / handler (project-specific — extend existing `settings` page entries)

- [ ] **Step 1: Write the in-memory queue test**

```go
func TestRemoteWriteSink_QueueOverflowDrops(t *testing.T) {
    s := NewRemoteWriteSink(RemoteWriteConfig{URL: "http://example.invalid", QueueMax: 2, FlushInterval: time.Hour})
    s.Write(context.Background(), Sample{NodeID: 1, SampledAt: time.Now()})
    s.Write(context.Background(), Sample{NodeID: 2, SampledAt: time.Now()})
    s.Write(context.Background(), Sample{NodeID: 3, SampledAt: time.Now()}) // overflow
    if s.QueueLen() != 2 {
        t.Fatalf("queue should be 2 after drop, got %d", s.QueueLen())
    }
    if testutil.ToFloat64(SinkDropped.WithLabelValues("remote_write")) < 1 {
        t.Fatalf("expected dropped counter to be incremented")
    }
}
```

- [ ] **Step 2: Implement `RemoteWriteSink`**

`remote_write_sink.go`: minimal protobuf via `github.com/golang/snappy` + hand-rolled `prometheus_remote_write.proto` types OR use `github.com/prometheus/prometheus/prompb` if license permits. Key points:
- Non-blocking enqueue (channel or ring buffer with mutex)
- Background goroutine flushes when batch size or interval hits
- Exponential backoff with jitter (250ms → 30s cap)
- Drop-on-overflow counter via `SinkDropped` from `obs.go`

Keep the implementation ~150 lines; test coverage on the enqueue / drop path is what matters most.

- [ ] **Step 3: Wire in `main.go` behind settings**

```go
rwEnabled := settings.Get("metrics.remote_write.enabled") == "true"
var sinks []metrics.Sink = []metrics.Sink{metrics.NewDBSink(db)}
if rwEnabled {
    sinks = append(sinks, metrics.NewRemoteWriteSink(metrics.RemoteWriteConfig{
        URL:           settings.Get("metrics.remote_write.url"),
        AuthHeader:    settings.Get("metrics.remote_write.auth_header"),
        BatchSize:     500,
        FlushInterval: 15 * time.Second,
        QueueMax:      10000,
    }))
}
fanSink := metrics.NewFanSink(sinks...)
```

- [ ] **Step 4: Run tests + commit**

```
cd backend && go test ./internal/metrics/... -count=1 -run RemoteWrite
git add backend/internal/metrics/ backend/cmd/server/main.go
git commit -m "feat(metrics): optional RemoteWriteSink with queue/backoff"
```

---

## Task 19: Frontend — detail page skeleton & routing

**Files:**
- Create: `web/src/pages/nodes-detail-page.tsx`
- Create: `web/src/pages/nodes-detail-page.test.tsx`
- Modify: `web/src/router.tsx`

- [ ] **Step 1: Write skeleton test**

`nodes-detail-page.test.tsx`:
```tsx
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { vi } from "vitest";
import NodesDetailPage from "./nodes-detail-page";

vi.mock("@/context/auth-context", () => ({
    useAuth: () => ({ token: "test-token" }),
}));
vi.mock("@/features/nodes-detail/use-node-status", () => ({
    useNodeStatus: () => ({ data: { online: true }, isLoading: false }),
}));

function renderAt(path: string) {
    const qc = new QueryClient();
    return render(
        <QueryClientProvider client={qc}>
            <MemoryRouter initialEntries={[path]}>
                <Routes>
                    <Route path="/nodes/:id" element={<NodesDetailPage />} />
                </Routes>
            </MemoryRouter>
        </QueryClientProvider>,
    );
}

test("renders overview tab by default", () => {
    renderAt("/nodes/42");
    expect(screen.getByRole("tab", { name: /概览/ })).toHaveAttribute("data-state", "active");
});

test("switches to metrics tab via query param", () => {
    renderAt("/nodes/42?tab=metrics");
    expect(screen.getByRole("tab", { name: /指标/ })).toHaveAttribute("data-state", "active");
});
```

- [ ] **Step 2: Implement skeleton**

`nodes-detail-page.tsx`:
```tsx
import { useParams, useSearchParams } from "react-router-dom";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/primitives/tabs";  // or existing path
import PageHero from "@/components/page-hero";   // or actual path
import OverviewTab from "@/features/nodes-detail/overview-tab";
import MetricsTab from "@/features/nodes-detail/metrics-tab";
import TasksTab from "@/features/nodes-detail/tasks-tab";
import AlertsTab from "@/features/nodes-detail/alerts-tab";
import ProfileTab from "@/features/nodes-detail/profile-tab";
import { useNodeStatus } from "@/features/nodes-detail/use-node-status";

const VALID_TABS = ["overview", "metrics", "tasks", "alerts", "profile"] as const;
type TabId = typeof VALID_TABS[number];

export default function NodesDetailPage() {
    const { id } = useParams<{ id: string }>();
    const [params, setParams] = useSearchParams();
    const activeTab: TabId = (VALID_TABS.includes(params.get("tab") as TabId)
        ? (params.get("tab") as TabId)
        : "overview");
    const { data: status } = useNodeStatus(Number(id));

    const setTab = (t: TabId) => {
        const next = new URLSearchParams(params);
        next.set("tab", t);
        setParams(next, { replace: true });
    };

    return (
        <div className="flex flex-col gap-6">
            <PageHero
                title={`节点 · ${status?.node_name ?? id}`}
                subtitle={`状态：${status?.online ? "在线" : "离线"}`}
            />
            <Tabs value={activeTab} onValueChange={(v) => setTab(v as TabId)}>
                <TabsList>
                    <TabsTrigger value="overview">概览</TabsTrigger>
                    <TabsTrigger value="metrics">指标</TabsTrigger>
                    <TabsTrigger value="tasks">任务</TabsTrigger>
                    <TabsTrigger value="alerts">告警</TabsTrigger>
                    <TabsTrigger value="profile">属性</TabsTrigger>
                </TabsList>
                <TabsContent value="overview"><OverviewTab nodeId={Number(id)} /></TabsContent>
                <TabsContent value="metrics"><MetricsTab nodeId={Number(id)} /></TabsContent>
                <TabsContent value="tasks"><TasksTab nodeId={Number(id)} /></TabsContent>
                <TabsContent value="alerts"><AlertsTab nodeId={Number(id)} /></TabsContent>
                <TabsContent value="profile"><ProfileTab nodeId={Number(id)} /></TabsContent>
            </Tabs>
        </div>
    );
}
```

Replace primitive imports with actual project paths — run:
```
grep -rn "TabsList\|TabsTrigger" /Users/weibo/Code/xirang/.claude/worktrees/nervous-austin-10abea/web/src/components/primitives/ | head
```
and adjust.

Stub each tab file so imports resolve:
```tsx
// overview-tab.tsx
export default function OverviewTab({ nodeId }: { nodeId: number }) {
    return <div>Overview for {nodeId}</div>;
}
```
Repeat for `metrics-tab.tsx`, `tasks-tab.tsx`, `alerts-tab.tsx`, `profile-tab.tsx`.

Also stub `use-node-status.ts`:
```ts
import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";  // existing wrapper

export function useNodeStatus(nodeId: number) {
    return useQuery({
        queryKey: ["node-status", nodeId],
        queryFn: () => apiFetch<any>(`/nodes/${nodeId}/status`),
    });
}
```

- [ ] **Step 3: Register route in `router.tsx`**

Add (adjust to actual routing library — react-router-dom):
```tsx
{ path: "/nodes/:id", element: <NodesDetailPage /> },
```

- [ ] **Step 4: Run tests**

```
cd web && npm run check
```
Expected: `npm run check` completes (typecheck + tests + build) without errors.

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/nodes-detail-page.tsx web/src/pages/nodes-detail-page.test.tsx web/src/features/nodes-detail/ web/src/router.tsx
git commit -m "feat(web): node detail page skeleton with tab routing"
```

---

## Task 20: Frontend — `StatCard` and `TrendChart` primitives

**Files:**
- Create: `web/src/features/nodes-detail/stat-card.tsx` (+ test)
- Create: `web/src/features/nodes-detail/trend-chart.tsx` (+ test)

- [ ] **Step 1: `StatCard` test**

```tsx
import { render, screen } from "@testing-library/react";
import StatCard from "./stat-card";

test("renders label, value, and warn variant", () => {
    render(<StatCard label="CPU" value={85} unit="%" sparkline={[10, 20, 85]} warnAt={80} />);
    expect(screen.getByText("CPU")).toBeInTheDocument();
    expect(screen.getByText(/85/)).toBeInTheDocument();
    expect(screen.getByTestId("stat-card")).toHaveAttribute("data-variant", "warn");
});
```

- [ ] **Step 2: Implement `StatCard`**

```tsx
import { cn } from "@/lib/utils";
import { LineChart, Line, ResponsiveContainer } from "recharts";

export default function StatCard({
    label, value, unit, sparkline, warnAt,
}: {
    label: string;
    value: number;
    unit?: string;
    sparkline?: number[];
    warnAt?: number;
}) {
    const variant = warnAt !== undefined && value >= warnAt ? "warn" : "default";
    const data = sparkline?.map((v, i) => ({ i, v })) ?? [];
    return (
        <div data-testid="stat-card" data-variant={variant}
             className={cn("rounded-md p-4 border", variant === "warn" ? "border-sage-warning bg-sage-warning/10" : "border-sage-border")}>
            <div className="text-sm text-sage-muted">{label}</div>
            <div className="text-3xl font-medium">{value}{unit ? <span className="text-lg text-sage-muted ml-1">{unit}</span> : null}</div>
            {sparkline && sparkline.length > 0 && (
                <div className="h-10 mt-2">
                    <ResponsiveContainer><LineChart data={data}><Line type="monotone" dataKey="v" strokeWidth={1.5} dot={false} /></LineChart></ResponsiveContainer>
                </div>
            )}
        </div>
    );
}
```

Adjust `cn`, `sage-*` tokens, and primitive component path to match this project.

- [ ] **Step 3: `TrendChart` test**

Simple: renders a `<LineChart>` with the number of series equal to props.series length.

- [ ] **Step 4: Implement `TrendChart`**

Thin wrapper around Recharts `LineChart` + `XAxis` + `YAxis` + `Tooltip` with series toggleable via checkboxes and a time-range selector.

- [ ] **Step 5: Run tests + commit**

```
cd web && npx vitest run src/features/nodes-detail/stat-card.test.tsx src/features/nodes-detail/trend-chart.test.tsx
git add web/src/features/nodes-detail/stat-card.tsx web/src/features/nodes-detail/trend-chart.tsx web/src/features/nodes-detail/stat-card.test.tsx web/src/features/nodes-detail/trend-chart.test.tsx
git commit -m "feat(web): StatCard and TrendChart primitives for detail page"
```

---

## Task 21: Frontend — Overview tab composition

**Files:**
- Modify: `web/src/features/nodes-detail/overview-tab.tsx`
- Create: `web/src/features/nodes-detail/use-node-metrics.ts`
- Create: `web/src/features/nodes-detail/use-disk-forecast.ts`
- Create: `web/src/features/nodes-detail/disk-forecast-card.tsx` (+ test)

- [ ] **Step 1: Implement `use-node-metrics`**

```ts
import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";

export type MetricSeries = {
    metric: string;
    unit: string;
    points: Array<{ t: string; avg?: number; max?: number; v?: number }>;
};
export type MetricsResponse = { granularity: string; bucket_seconds: number; series: MetricSeries[] };

export function useNodeMetrics(nodeId: number, fromIso: string, toIso: string, fields?: string[]) {
    const qs = new URLSearchParams({ from: fromIso, to: toIso });
    if (fields) qs.set("fields", fields.join(","));
    return useQuery({
        queryKey: ["node-metrics", nodeId, fromIso, toIso, fields?.join(",")],
        queryFn: () => apiFetch<MetricsResponse>(`/nodes/${nodeId}/metrics?${qs}`),
    });
}
```

- [ ] **Step 2: Implement `use-disk-forecast`**

```ts
import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/lib/api";

export type DiskForecast = {
    disk_gb_total: number;
    disk_gb_used_now: number;
    daily_growth_gb: number | null;
    forecast: { days_to_full: number | null; date_full: string | null; confidence: "high"|"medium"|"low"|"insufficient" };
};

export function useDiskForecast(nodeId: number) {
    return useQuery({
        queryKey: ["node-disk-forecast", nodeId],
        queryFn: () => apiFetch<DiskForecast>(`/nodes/${nodeId}/disk-forecast`),
    });
}
```

- [ ] **Step 3: `DiskForecastCard` with confidence copy**

```tsx
// disk-forecast-card.tsx
import { useDiskForecast } from "./use-disk-forecast";

const confidenceCopy: Record<string, string> = {
    high: "预测置信度 · 高",
    medium: "预测置信度 · 中",
    low: "预测置信度 · 低，样本不足",
    insufficient: "样本不足（< 7 天），暂无预测",
};

export default function DiskForecastCard({ nodeId }: { nodeId: number }) {
    const { data } = useDiskForecast(nodeId);
    if (!data) return null;
    const { forecast, disk_gb_total, disk_gb_used_now, daily_growth_gb } = data;
    return (
        <div className="rounded-md border border-sage-border p-4">
            <div className="flex justify-between items-baseline">
                <h3 className="text-base font-medium">💾 磁盘增长预测</h3>
                <span className="text-xs text-sage-muted">{confidenceCopy[forecast.confidence]}</span>
            </div>
            <div className="mt-2 text-sm">当前 {disk_gb_used_now.toFixed(1)} / {disk_gb_total.toFixed(0)} GB</div>
            {daily_growth_gb !== null && daily_growth_gb <= 0 && (
                <div className="text-sm text-sage-muted">磁盘用量持平或下降中</div>
            )}
            {forecast.days_to_full !== null && (
                <div className="mt-1 text-sm">预计 <b>{Math.round(forecast.days_to_full)}</b> 天后满（{forecast.date_full}）</div>
            )}
        </div>
    );
}
```

Add a simple test that renders with a mocked hook and asserts the confidence copy.

- [ ] **Step 4: Compose `overview-tab.tsx`**

```tsx
import StatCard from "./stat-card";
import TrendChart from "./trend-chart";
import DiskForecastCard from "./disk-forecast-card";
import { useNodeStatus } from "./use-node-status";
import { useNodeMetrics } from "./use-node-metrics";
import { useState } from "react";

const RANGES = { "1h": 1, "6h": 6, "24h": 24, "7d": 24 * 7, "30d": 24 * 30 } as const;

export default function OverviewTab({ nodeId }: { nodeId: number }) {
    const [range, setRange] = useState<keyof typeof RANGES>("24h");
    const to = new Date().toISOString();
    const from = new Date(Date.now() - RANGES[range] * 3600_000).toISOString();

    const { data: status } = useNodeStatus(nodeId);
    const { data: metrics } = useNodeMetrics(nodeId, from, to, ["cpu_pct","mem_pct","disk_pct","load1"]);

    return (
        <div className="grid grid-cols-1 gap-6">
            <div className="grid grid-cols-4 gap-4">
                <StatCard label="CPU"  value={status?.current?.cpu_pct ?? 0} unit="%" warnAt={80} />
                <StatCard label="MEM"  value={status?.current?.mem_pct ?? 0} unit="%" warnAt={85} />
                <StatCard label="DISK" value={status?.current?.disk_pct ?? 0} unit="%" warnAt={85} />
                <StatCard label="LOAD" value={status?.current?.load1 ?? 0} />
            </div>
            <div className="grid grid-cols-3 gap-4">
                <div className="col-span-2"><TrendChart series={metrics?.series ?? []} range={range} onRangeChange={setRange} /></div>
                <div className="space-y-4">
                    {/* Alerts + tasks cards — reuse existing alert/task list primitives, filtered by nodeId */}
                </div>
            </div>
            <DiskForecastCard nodeId={nodeId} />
        </div>
    );
}
```

- [ ] **Step 5: Run tests + commit**

```
cd web && npm run check
git add web/src/features/nodes-detail/
git commit -m "feat(web): overview tab composition for node detail"
```

---

## Task 22: Frontend — Metrics tab

**Files:**
- Modify: `web/src/features/nodes-detail/metrics-tab.tsx`

- [ ] **Step 1: Implement with one chart per metric + time-range + granularity override + CSV export**

```tsx
import TrendChart from "./trend-chart";
import { useNodeMetrics } from "./use-node-metrics";
import { useState } from "react";

const FIELDS = ["cpu_pct","mem_pct","disk_pct","load1","latency_ms","probe_ok_ratio"] as const;

export default function MetricsTab({ nodeId }: { nodeId: number }) {
    const [range, setRange] = useState<"24h"|"7d"|"30d">("24h");
    const [granularity, setGranularity] = useState<"auto"|"raw"|"hourly"|"daily">("auto");
    const hours = { "24h": 24, "7d": 168, "30d": 720 }[range];
    const to = new Date().toISOString();
    const from = new Date(Date.now() - hours * 3600_000).toISOString();
    const { data } = useNodeMetrics(nodeId, from, to, [...FIELDS] as string[]);

    const exportCsv = () => {
        // build CSV from data.series; trigger download via Blob + anchor click
    };

    return (
        <div className="space-y-6">
            <div className="flex items-center gap-4">
                <select value={range} onChange={e=>setRange(e.target.value as any)}>
                    <option value="24h">24h</option><option value="7d">7d</option><option value="30d">30d</option>
                </select>
                <select value={granularity} onChange={e=>setGranularity(e.target.value as any)}>
                    <option>auto</option><option>raw</option><option>hourly</option><option>daily</option>
                </select>
                <button onClick={exportCsv} className="ml-auto">导出 CSV</button>
            </div>
            {data?.series.map(s => <TrendChart key={s.metric} series={[s]} range={range} onRangeChange={setRange as any} />)}
        </div>
    );
}
```

Test: render with a mocked metrics hook returning two series; assert two charts and one export button.

- [ ] **Step 2: Run tests + commit**

```
cd web && npm run check
git add web/src/features/nodes-detail/metrics-tab.tsx
git commit -m "feat(web): metrics tab with per-metric charts and CSV export"
```

---

## Task 23: Frontend — Tasks, Alerts, Profile tabs

**Files:**
- Modify: `web/src/features/nodes-detail/tasks-tab.tsx`
- Modify: `web/src/features/nodes-detail/alerts-tab.tsx`
- Modify: `web/src/features/nodes-detail/profile-tab.tsx`

- [ ] **Step 1: Tasks tab**

Reuse the existing TaskRuns table primitive (find with `grep -rn "task-runs" web/src/features/`) and pass `nodeId` filter. Provide filter chips (全部 / 运行中 / 近期失败).

- [ ] **Step 2: Alerts tab**

Reuse the existing alerts list; add `?nodeId=` filter. Each row gets a `查看关联指标` action that navigates to `/nodes/:id?tab=metrics&from=<alert_triggered_at - 15min>&to=<alert_triggered_at + 15min>`.

- [ ] **Step 3: Profile tab**

Fetch `/nodes/:id` (existing endpoint). Render two-column layout: basics (address/port/tag/owner), SSH key, backup dir; maintenance window, last self-check, timestamps.

- [ ] **Step 4: Run tests + commit**

```
cd web && npm run check
git add web/src/features/nodes-detail/
git commit -m "feat(web): tasks/alerts/profile tabs for node detail"
```

---

## Task 24: Frontend — Entry-point links

**Files:**
- Modify: `web/src/pages/overview-page.tsx` (or matrix component)
- Modify: `web/src/pages/nodes-page.tsx` (or list row)
- Modify: `web/src/pages/alerts-page.tsx` (or alert row)

- [ ] **Step 1: Overview matrix — dots link to detail**

Wrap each node dot in `<Link to={`/nodes/${node.id}`}>` (or use React Router's `useNavigate`).

- [ ] **Step 2: Node list — whole row linkable**

Easiest: add `role="link"` + `onClick={() => navigate(`/nodes/${n.id}`)}` on each row.

- [ ] **Step 3: Alert row — build time-windowed link**

```tsx
import { differenceInMinutes, subMinutes, addMinutes } from "date-fns"; // or manual math
const triggered = new Date(alert.triggered_at);
const from = subMinutes(triggered, 15).toISOString();
const to = addMinutes(triggered, 15).toISOString();
const href = `/nodes/${alert.node_id}?tab=metrics&from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`;
```

- [ ] **Step 4: Run tests + commit**

```
cd web && npm run check
git add web/src/pages/
git commit -m "feat(web): link overview matrix, node list, and alert rows to detail page"
```

---

## Final verification

- [ ] Run full project build
```
make check
```
Expected: lint + test + build all pass.

- [ ] Manual smoke
1. Start backend + frontend
2. Log in as `admin`
3. Open `/nodes/:id` for a known node — overview tab loads with stat cards and trend chart
4. Switch to metrics tab — individual charts render
5. Trigger an alert (e.g., induce high CPU) — row shows "查看关联指标" link that lands on metrics tab with time window populated
6. Hit `/api/v1/admin/metrics/rollup-status` — returns lag < 2 hours

- [ ] Verify Prometheus scraping shows `xirang_metric_rollup_lag_seconds`
```
curl http://localhost:8080/metrics | grep xirang_metric_rollup
```

- [ ] If RemoteWriteSink enabled: verify remote Prometheus has `xirang_node_cpu_pct{node="..."}` series

---

## Out-of-Plan Tasks (deferred)

- Prometheus remote_write protobuf implementation (Task 18) may be skipped for MVP — the sink interface is already pluggable, so the downstream delivery can be added later without touching prober or aggregator code.
- Alert rules referencing `metrics.Field` constants — handled in P5b.
- Log aggregation with alert-to-log deep-linking — handled in P5c.
- Custom dashboards reusing `StatCard` / `TrendChart` as promoted primitives — handled in P5d.
