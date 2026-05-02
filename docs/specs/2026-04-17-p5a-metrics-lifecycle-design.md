# P5a: Metrics Lifecycle & Node Detail Page

> Historical note: This dated design snapshot documents the plan at the time it was written. Treat it as implementation history, not current operating documentation; verify commands, paths, and workflow behavior against the current repo before acting.

First sub-project of the P5 Observability Evolution series. Adds retention/downsampling for node metrics, a pluggable metric sink with optional Prometheus remote_write, and the previously-missing `/nodes/:id` detail page.

Depends on P4 (Prometheus /metrics endpoint, performance indexes). Blocks P5b (alerting baselines need historical aggregates), P5d (custom dashboards query the rollup tables).

## Scope

One coherent deliverable with three concerns that share the same data model:

1. **Metrics lifecycle** — 3-tier storage (raw 7d / hourly 90d / daily 2y), aggregation worker, pluggable sink abstraction
2. **Optional Prometheus remote_write export** — opt-in fan-out alongside the DB sink, configurable via `system_settings`
3. **Node detail page** — `/nodes/:id` with five tabs (overview / metrics / tasks / alerts / profile), URL-synced state, deep-link support

Explicit out-of-scope items listed at the end.

## Constraints

- Must work on both **SQLite** (single-binary deploy) and **PostgreSQL** (production). No TimescaleDB, no PG partitioning, no materialized views.
- Must not regress existing probe/alerting behavior — the change is additive (insert a fan before the existing DB write).
- Must be zero-downtime; any commit should be safe to deploy standalone.
- Follow existing project conventions: migrations in `backend/internal/database/migrations/{sqlite,postgres}/`, handlers in `backend/internal/api/handlers/`, frontend in `web/src/pages/` + `web/src/features/`, Sage tokens + Wave 3 primitives.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│  Prober (existing, backend/internal/probe/prober.go)        │
│   └─ SSH probeNode() → metrics.Sample                       │
└────────────┬────────────────────────────────────────────────┘
             │ sinkFan.Write(ctx, sample)
             ▼
┌─────────────────────────────────────────────────────────────┐
│  FanSink (new, backend/internal/metrics/)                   │
│  ├─ DBSink            → node_metric_samples (raw, 7d TTL)  │
│  └─ RemoteWriteSink   → Prometheus remote_write (opt-in)    │
└────────────┬────────────────────────────────────────────────┘
             │ periodic schedule
             ▼
┌─────────────────────────────────────────────────────────────┐
│  AggregationWorker (new, backend/internal/metrics/)         │
│   ├─ RollupHourly: raw → node_metric_samples_hourly (90d)  │
│   └─ RollupDaily:  hourly → node_metric_samples_daily (2y) │
└─────────────────────────────────────────────────────────────┘
```

Only two insertion points touch existing code:
- `Prober.probeNode` replaces the direct `db.Create(&sample)` call with `sinkFan.Write(ctx, sample)`.
- `main.go` wires up the aggregator goroutine alongside the existing prober lifecycle.

## Data Model

### Existing: `node_metric_samples` (raw)
Unchanged schema. The prober's existing `cleanupOldMetrics()` enforces 7-day TTL via a `24 * time.Hour` ticker in `Prober.run`.

**Pre-requisite verification:** Commit 1 must start by confirming `cleanupOldMetrics` actually deletes rows older than 7 days (the P4 audit flagged this as uncertain). If the cleanup is broken, fix it as part of Commit 1; the rest of the aggregation design assumes raw table size is bounded.

### New: `node_metric_samples_hourly` (migration 000033)

```sql
CREATE TABLE node_metric_samples_hourly (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  node_id           INTEGER NOT NULL,
  bucket_start      DATETIME NOT NULL,   -- hour-truncated UTC
  cpu_pct_avg       REAL,
  cpu_pct_max       REAL,
  mem_pct_avg       REAL,
  mem_pct_max       REAL,
  disk_pct_avg      REAL,
  disk_pct_max      REAL,
  load1_avg         REAL,
  load1_max         REAL,
  latency_ms_avg    REAL,
  latency_ms_max    REAL,
  disk_gb_used_avg  REAL,
  disk_gb_total     REAL,    -- time-window terminal value
  probe_ok          INTEGER NOT NULL,
  probe_fail        INTEGER NOT NULL,
  sample_count      INTEGER NOT NULL,
  created_at        DATETIME NOT NULL,
  UNIQUE (node_id, bucket_start)
);
CREATE INDEX idx_nmsh_node_bucket ON node_metric_samples_hourly(node_id, bucket_start);
CREATE INDEX idx_nmsh_bucket      ON node_metric_samples_hourly(bucket_start);
```

PG equivalent uses `BIGSERIAL`, `TIMESTAMPTZ`, and `DOUBLE PRECISION` per project conventions; SQL logic is otherwise identical. Both dialects are delivered as separate `.up.sql` / `.down.sql` files.

### New: `node_metric_samples_daily` (migration 000034)
Same column shape as hourly; `bucket_start` is day-truncated UTC (00:00:00). `UNIQUE (node_id, bucket_start)` enforced.

### Design notes
- Both `avg` and `max` per metric — max for incident forensics (spikes), avg for capacity planning.
- `probe_ok` / `probe_fail` makes each daily row a ready-made availability record.
- `UNIQUE (node_id, bucket_start)` enables idempotent replay via `INSERT ... ON CONFLICT DO UPDATE`.
- `disk_gb_total` is not aggregated (hardware capacity rarely changes within a window); we store the terminal value so size change shows up in the bucket it happened.

### Size estimate (100 nodes, 30s probe interval)
| Tier | Rows | Disk |
|------|------|------|
| Raw 7d | ~2,016,000 | ~300 MB |
| Hourly 90d | ~216,000 | ~30 MB |
| Daily 2y | ~73,000 | ~10 MB |

## Metric Sink Layer

New package `backend/internal/metrics/`. Three key types:

```go
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

type Sink interface {
    Name() string
    Write(ctx context.Context, s Sample) error
}

type FanSink struct {
    sinks []Sink   // each sink's failure is isolated; no sink blocks others
}
```

**Metric field enum** lives in `backend/internal/metrics/fields.go` as exported constants (`FieldCPUPct`, `FieldMemPct`, …). P5b alert rules will consume the same enum.

### DBSink (always on)
Thin wrapper around the current prober DB write. Moves the `db.Create(&NodeMetricSample{…})` call out of the prober into the sink. Behavior-preserving refactor — tests should show no functional difference.

### RemoteWriteSink (opt-in)
Implements the Prometheus remote_write protocol (snappy-compressed protobuf HTTP POST to a configured endpoint). Minimal wire format — we embed the protobuf definitions rather than pulling in the full Prometheus client library.

Configuration in `system_settings` (keys):
| Key | Purpose | Default |
|-----|---------|---------|
| `metrics.remote_write.enabled` | Feature toggle | `false` |
| `metrics.remote_write.url` | Remote endpoint | empty |
| `metrics.remote_write.auth_header` | `Authorization` header value | empty |
| `metrics.remote_write.batch_size` | Samples per POST | `500` |
| `metrics.remote_write.flush_interval_ms` | Max flush delay | `15000` |
| `metrics.remote_write.queue_max` | In-memory queue cap before drop | `10000` |

Label schema for each sample (fixed; P5b may extend):
```
node="<node_name>"
node_id="<numeric id>"
```

Metric names: `xirang_node_cpu_pct`, `xirang_node_mem_pct`, `xirang_node_disk_pct`, `xirang_node_load1`, `xirang_node_latency_ms`, `xirang_node_disk_gb_used`, `xirang_node_disk_gb_total`, `xirang_node_probe_ok` (0/1 gauge).

Resilience:
- Bounded in-memory queue; overflow drops oldest and increments `xirang_metric_sink_dropped_total{sink="remote_write"}` counter.
- Exponential backoff with jitter (250ms → 30s) on non-2xx responses.
- `Write()` never blocks the prober on network; it enqueues and returns.

## Aggregation Worker

New file `backend/internal/metrics/aggregator.go`. Runs in its own goroutine, started from `main.go` after the prober.

### Scheduling
- **Hourly rollup ticker**: every 1 minute, queries `MAX(bucket_start)` from `node_metric_samples_hourly`. If there are complete hour-windows between that and `now - 5min` (buffer), aggregates them in order, one bucket per transaction.
- **Daily rollup ticker**: every 10 minutes, same pattern against `node_metric_samples_daily`, source is `node_metric_samples_hourly`.

The 5-minute buffer prevents aggregating partial hours where late-arriving probe samples would cause `ON CONFLICT UPDATE` churn.

### Concurrency
Single-instance deployment is the only supported topology for now. Aggregator uses an in-process `sync.Mutex` to guard its own tick handlers. The `Aggregator` type exposes a `Lock interface` (default `sync.Mutex`, pluggable) so a DB-lease implementation can be added later without changing call sites — no new table in this phase.

### Aggregation SQL (hourly, SQLite dialect)

```sql
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
  datetime(strftime('%Y-%m-%d %H:00:00', sampled_at)) AS bucket_start,
  AVG(cpu_pct),        MAX(cpu_pct),
  AVG(mem_pct),        MAX(mem_pct),
  AVG(disk_pct),       MAX(disk_pct),
  AVG(load1),          MAX(load1),
  AVG(latency_ms),     MAX(latency_ms),
  AVG(disk_gb_used),
  -- terminal value of total:
  (SELECT disk_gb_total FROM node_metric_samples
     WHERE node_id = nms.node_id
       AND sampled_at >= ? AND sampled_at < ?
     ORDER BY sampled_at DESC LIMIT 1),
  SUM(CASE WHEN probe_ok THEN 1 ELSE 0 END),
  SUM(CASE WHEN probe_ok THEN 0 ELSE 1 END),
  COUNT(*),
  CURRENT_TIMESTAMP
FROM node_metric_samples nms
WHERE sampled_at >= ? AND sampled_at < ?
GROUP BY node_id, bucket_start
ON CONFLICT (node_id, bucket_start) DO UPDATE SET
  cpu_pct_avg      = excluded.cpu_pct_avg,
  cpu_pct_max      = excluded.cpu_pct_max,
  mem_pct_avg      = excluded.mem_pct_avg,
  mem_pct_max      = excluded.mem_pct_max,
  disk_pct_avg     = excluded.disk_pct_avg,
  disk_pct_max     = excluded.disk_pct_max,
  load1_avg        = excluded.load1_avg,
  load1_max        = excluded.load1_max,
  latency_ms_avg   = excluded.latency_ms_avg,
  latency_ms_max   = excluded.latency_ms_max,
  disk_gb_used_avg = excluded.disk_gb_used_avg,
  disk_gb_total    = excluded.disk_gb_total,
  probe_ok         = excluded.probe_ok,
  probe_fail       = excluded.probe_fail,
  sample_count     = excluded.sample_count;
```

PostgreSQL dialect uses `date_trunc('hour', sampled_at)`; both shapes live in `aggregator.go` behind a dialect switch.

### First-deployment backfill
On startup, the aggregator performs backfill **synchronously and in order before starting either tick loop**:

1. If `node_metric_samples_hourly` is empty but raw has rows, walk forward from the oldest raw row and fill every hour up to `now - 5min`. For 100 nodes × 7 days raw this produces ~16,800 buckets; measured locally in the sub-second range for SQLite and ~1s for PG.
2. After step 1 completes, if `node_metric_samples_daily` is empty and hourly has rows, fill daily from hourly the same way (~720 rows, millisecond range).
3. Only after both backfills return "no more buckets" does the aggregator arm the 1min hourly ticker and 10min daily ticker.

This ordering prevents daily rollup from running against an empty hourly table during cold boot.

**No manual backfill script is required.** No migration flag, no feature gate — empty target + non-empty source triggers the correct behavior automatically.

### Self-observability (Prometheus metrics)
Registered on startup in the existing metrics registry (P4 infra):
- `xirang_metric_rollup_duration_seconds{tier}` histogram
- `xirang_metric_rollup_lag_seconds{tier}` gauge — current lag between newest bucket and `now`
- `xirang_metric_sink_dropped_total{sink}` counter

## API Surface

All routes under the existing `secured` group (JWT + RBAC + audit middleware).

### `GET /api/v1/nodes/:id/metrics`

Query params:
- `from`, `to` — required, ISO8601 UTC
- `granularity` — `auto` (default) | `raw` | `hourly` | `daily`
- `fields` — comma-separated subset of `{cpu_pct, mem_pct, disk_pct, load1, latency_ms, disk_gb_used, probe_ok_ratio}`; defaults to all.
  - `probe_ok_ratio` is **derived server-side** from `probe_ok / (probe_ok + probe_fail)` on the hourly/daily tiers; on the raw tier it is the boolean `probe_ok` rendered as 0/1.

**`auto` selection (server-side):**
| Span | Tier | Max points |
|------|------|-----------|
| ≤ 6h | raw | ≤ 720 |
| 6h–3d | raw with equidistant downsampling | ≤ 1500 |
| 3d–14d | hourly | ≤ 336 |
| 14d–90d | hourly | ≤ 2160 |
| > 90d | daily | ≤ 730 |

Response shape:
```json
{
  "granularity": "hourly",
  "bucket_seconds": 3600,
  "series": [
    {
      "metric": "cpu_pct",
      "unit": "percent",
      "points": [
        {"t": "2026-04-17T06:00:00Z", "avg": 23.4, "max": 81.2}
      ]
    }
  ]
}
```

Raw tier points omit `avg`/`max` and use `{"t": "...", "v": 23.4}` — front end reads `bucket_seconds` to decide rendering (line only vs line + max band).

### `GET /api/v1/nodes/:id/status`

Returns the latest probe sample plus 1h / 24h aggregates for the overview cards.

```json
{
  "probed_at": "2026-04-17T12:34:56Z",
  "online": true,
  "current":   {"cpu_pct": 23.4, "mem_pct": 67.8, "disk_pct": 52.1, "load1": 0.8, "latency_ms": 14},
  "trend_1h":  {"cpu_pct_avg": 18.2, "mem_pct_avg": 65.1, "disk_pct_avg": 52.0, "load1_avg": 0.6, "latency_ms_avg": 12},
  "trend_24h": {"cpu_pct_avg": 15.6, "mem_pct_avg": 64.0, "disk_pct_avg": 51.8, "load1_avg": 0.5, "latency_ms_avg": 13, "probe_ok_ratio": 0.998},
  "open_alerts": 2,
  "running_tasks": 1
}
```

Single round-trip for the page header and top-of-overview cards.

### `GET /api/v1/nodes/:id/disk-forecast`

Linear regression over the last 30 days of `disk_gb_used_avg` from the daily tier:

```json
{
  "disk_gb_total": 500.0,
  "disk_gb_used_now": 312.5,
  "daily_growth_gb": 1.8,
  "forecast": {
    "days_to_full": 104,
    "date_full": "2026-07-30",
    "confidence": "medium"
  }
}
```

Confidence tiers:
- `high` — ≥ 21 daily samples AND r² ≥ 0.7
- `medium` — ≥ 14 daily samples AND r² ≥ 0.3
- `low` — ≥ 7 daily samples
- `insufficient` — fewer than 7 daily samples

**Always shown** on the overview tab; the front end renders a contextual message based on `confidence`. When `daily_growth_gb <= 0`, `days_to_full` is `null` and the UI shows "磁盘用量持平或下降".

### `GET /api/v1/admin/metrics/rollup-status`

Admin-only diagnostic. Returns latest `bucket_start` per tier, current lag, and last error (if any).

```json
{
  "hourly": {"latest_bucket": "2026-04-17T12:00:00Z", "lag_seconds": 2100, "last_error": null},
  "daily":  {"latest_bucket": "2026-04-16T00:00:00Z", "lag_seconds": 90000, "last_error": null}
}
```

## Frontend

### Route
`/nodes/:id?tab=<overview|metrics|tasks|alerts|profile>&from=...&to=...`

All tab and time-range state is URL-synced so browser back/forward and share-link work. Default tab is `overview`.

### Page skeleton
Reuses Wave 2 `PageHero` and Wave 3 primitives.

```
┌─────────────────────────────────────────────────────────────┐
│ 节点 › production-web-01              ●在线   [⋯ actions] │
│ 172.16.8.12:22 · prod · 12s 前采集                          │
│ [快捷 SSH] [进入维护] [触发自检] [编辑]                     │
├─────────────────────────────────────────────────────────────┤
│ [概览*] [指标] [任务] [告警] [属性]                         │
├─────────────────────────────────────────────────────────────┤
│  === tab content ===                                        │
└─────────────────────────────────────────────────────────────┘
```

Status pill has three states (在线 / 离线 / 维护中) using Sage tokens (`sage-success`, `sage-warning`, `sage-muted`). On narrow viewports, header actions collapse into an overflow menu.

### Tab 1 · Overview (default)
- **Top row** — four `StatCard` (CPU/MEM/DISK/LOAD): large current value, 1h sparkline, warn color when above threshold
- **Middle row** — `TrendChart` (multi-series toggleable, time range selector: 1h/6h/24h/7d/30d) on the left, two cards on the right:
  - Open alerts (max 5, link to alerts tab)
  - Recent tasks (max 5, link to tasks tab)
- **Bottom row** — `DiskForecastCard` with confidence badge and "查看 30d 历史 →" deep-link back to metrics tab

All three blocks (trend chart, alerts card, tasks card) share the same time window. Changing the time selector refreshes all three.

### Tab 2 · Metrics
- One large chart per metric (CPU / MEM / DISK / LOAD / LATENCY / PROBE_OK_RATIO)
- Shared time range selector + granularity override dropdown (`auto` / `raw` / `hourly` / `daily`)
- Each chart has a `[导出 CSV]` button (exports current window's aggregated data)

### Tab 3 · Tasks
- Filters: all / running / recent failures / by policy
- Columns: task name, policy, trigger type, status, duration, last run, actions
- Empty state: "该节点暂无关联任务" + "去策略页添加" CTA

### Tab 4 · Alerts
- Filters: `open` / `acknowledged` / `resolved`, by severity
- Row actions: acknowledge / resolve / "查看关联指标" (jumps to metrics tab with `from`/`to` set to alert time ± 15min — this wire-level behavior is what P5c later extends to jump to logs)

### Tab 5 · Profile
- Left column: basics (address, port, tag, owner, notes), SSH key reference, backup directory
- Right column: maintenance window status, most recent self-check result, created/updated timestamps

### Entry points (no new navigation)
1. Overview page node matrix dots → click to detail page
2. Node list rows → whole row linkable
3. Alert rows → "查看节点" with pre-populated time window

### New front-end files
```
web/src/pages/
  nodes-detail-page.tsx              (route + tab router)
web/src/features/nodes-detail/
  use-node-status.ts                 (TanStack Query hook)
  use-node-metrics.ts                (granularity auto handling)
  use-disk-forecast.ts
  overview-tab.tsx
  metrics-tab.tsx
  tasks-tab.tsx
  alerts-tab.tsx
  profile-tab.tsx
  stat-card.tsx                      (sparkline-bearing primitive; candidate for promotion to primitives/)
  trend-chart.tsx                    (shared Recharts wrapper)
  disk-forecast-card.tsx             (overview tab's disk-forecast block)
```

### Modified existing files (frontend)
- Router config (historical design correction: implementation confirmed routes live in `web/src/router.tsx`, not `web/src/app/`) — add `/nodes/:id` route pointing at `nodes-detail-page.tsx`.
- Overview page node matrix — make dots link to `/nodes/:id`.
- Node list page — make rows link to `/nodes/:id`.
- Alerts page row — add "查看节点" action building `/nodes/:id?tab=metrics&from=<alert_ts - 15min>&to=<alert_ts + 15min>`.

`stat-card.tsx` and `trend-chart.tsx` start under `features/nodes-detail/` and are promoted to `web/src/components/primitives/` once P5d (custom dashboards) needs to reuse them.

## Implementation Order

Delivered as a sequence of commits; each is independently deployable.

1. **schema & model** — migrations 000033, 000034 + Go structs + repository methods
2. **sink interface + DBSink** — extract prober's direct `db.Create` into a behavior-preserving sink; wire `FanSink{DBSink}`
3. **Aggregator worker** — start-time backfill, scheduled rollup, self-observability metrics
4. **API endpoints** — `/nodes/:id/metrics`, `/status`, `/disk-forecast`, `/admin/metrics/rollup-status`
5. **RemoteWriteSink** (optional toggle) — `system_settings` keys, settings UI entry, sink implementation
6. **Detail page skeleton + overview tab** — route, PageHero reuse, `StatCard` + `TrendChart` primitives, overview tab
7. **Remaining tabs** — metrics, tasks, alerts, profile
8. **Entry points** — link updates in overview matrix, node list, alert rows

Commits 1–5 are backend-only and each is deployable standalone (behavior never regresses). Commits 6–8 require 1–4 merged.

## Testing Strategy

| Layer | Target | Key cases |
|-------|--------|-----------|
| Unit | `DBSink` | Normal write, UNIQUE conflict, NULL handling |
| Unit | `RemoteWriteSink` | Queue backpressure, exponential backoff, batch flush, drop-on-overflow counter |
| Unit | `AggregationWorker.rollupHourly` | Empty raw, cross-boundary, ON CONFLICT idempotency, dialect difference (SQLite + PG both exercised) |
| Unit | `AggregationWorker.rollupDaily` | Same as hourly |
| Unit | `diskForecast` | Insufficient samples, strong-linear fit, strong noise, negative slope (disk shrinking), missing daily rows |
| Unit | `metrics.SelectGranularity` | Every span bucket from the table, edge cases at boundaries |
| Integration | `/nodes/:id/metrics` | One case per `auto` span bucket, asserting returned tier |
| Integration | First-deployment backfill | Fixture raw dataset → start aggregator → assert hourly row count |
| Integration | `/nodes/:id/disk-forecast` | Each confidence tier |
| Frontend | Detail page | RTL + vitest: tab switching, URL sync, confidence copy mapping, time-window deep-link parsing |
| Frontend | `StatCard`, `TrendChart` | Independent tests (continuing Wave 3 primitive coverage discipline) |

Coverage targets follow project convention: backend ≥ 70% overall, new packages ≥ 85%; front-end primitives 100%.

## Verification Checklist

- [ ] `make check` passes (lint + test + build, both backend and web)
- [ ] Manual regression: create a node, wait two probe ticks, visit `/nodes/:id` — data visible
- [ ] Restart after 10-minute outage; aggregator resumes from last `bucket_start`
- [ ] `/metrics` endpoint shows `xirang_metric_rollup_lag_seconds` ticking
- [ ] Configure RemoteWriteSink against a local Prometheus; confirm `xirang_node_cpu_pct{node="..."}` is queryable remotely
- [ ] Click "查看关联" on an alert row — detail page opens with metrics tab active and time window matching alert ± 15min
- [ ] `disk-forecast` returns `insufficient` when daily samples < 7; returns `null` `days_to_full` when growth ≤ 0

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Aggregator SQL locks table on large raw set | Medium | Medium | One hour-bucket per transaction; schedule offset from prober tick; PG has MVCC, SQLite transactions are short |
| RemoteWrite endpoint unreachable → memory grows | Low | Medium | Hard queue cap (default 10k) + overflow drop + `xirang_metric_sink_dropped_total` counter exposed |
| Backfill triggers I/O spike at boot | Low | Low | One-time, ~1–2s; affects only cold start |
| `auto` granularity selects wrong tier at boundary | Low | Low | Span table is explicit; each bucket unit-tested |
| Metric-name enum couples with P5b alert rules | Medium | Medium | Centralize in `metrics/fields.go`; P5b must import same constants |
| disk-forecast shows "已爆满" when disk was cleaned | Low | Low | Slope ≤ 0 → `days_to_full = null`, UI reads "磁盘用量持平或下降" |
| First-deployment backfill races with live probes | Low | Low | Backfill only fills `< now - 5min`; live prober writes go through sink as usual; no conflict |

## Out of Scope (deferred to future sub-projects)

- Alert grouping / silencing / escalation / baseline anomaly detection — **P5b**
- Centralized log aggregation and full-text search — **P5c**
- Custom dashboard grid layout, panel builder — **P5d**
- SLO definitions, burn-rate alerts, error budgets — **P5d**
- Node-side agent (replacement for SSH polling) — future; current design leaves a clean extension point (`Sink` interface + source side is pluggable)
- OpenTelemetry distributed tracing — not aligned with ops-platform core use case
- Extra metric-label dimensions beyond `node`/`node_id` — defer until P5b actually needs policy-level dimensions
