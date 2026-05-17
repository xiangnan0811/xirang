# Research: code foundations for Health Incident Timeline MVP

- **Query**: Research existing code foundations and implementation constraints for `.trellis/tasks/05-17-health-incident-timeline` (Health Incident Timeline MVP), focusing on alerts, deliveries, grouping, node metrics/probes, TaskRun/logs, backup health/confidence integration points, frontend notification/overview UI, tests, and concrete gaps for read-only incident-like timeline aggregation.
- **Scope**: internal
- **Date**: 2026-05-17

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go` | Defines timeline source records: `Alert`, `AlertDelivery`, `TaskRun`, `TaskLog`, `NodeMetricSample`, `NodeMetricSampleHourly`, `NodeMetricSampleDaily`, `AnomalyEvent`, `AlertEscalationEvent`, `ServiceMonitor`, `ServiceUptimeSample`. |
| `backend/internal/api/router.go` | Registers existing read APIs for alerts, deliveries, escalation events, anomaly events, node metrics, node logs, task runs/logs, overview, backup health, and public status page. |
| `backend/internal/api/handlers/alert_handler.go` | Alert list/get, lifecycle mutations, deliveries, delivery stats, unread count, group-info, escalation events. |
| `backend/internal/alerting/dispatcher.go` | Central alert creation/dedup/silence/grouping/dispatch flow; raises task, verification, node probe, disk, retention, integrity, drill, storage, service/anomaly-like alerts. |
| `backend/internal/alerting/grouping.go` | In-memory progressive grouping keyed by category/error code, node ID, and sorted tags. |
| `backend/internal/alerting/retry.go` | Alert delivery retry worker and manual retry behavior; retry backoff and status transitions. |
| `backend/internal/api/handlers/alert_delivery_handler.go` | Admin retry endpoint for alert delivery records. |
| `backend/internal/task/manager.go` | Creates `TaskRun` records when tasks are triggered. |
| `backend/internal/task/runner.go` | Updates task-run status/progress/verification and raises or resolves task-related alerts. |
| `backend/internal/task/log_writer.go` | Batches persistent `TaskLog` writes and emits websocket log events. |
| `backend/internal/task/progress_writer.go` | Updates cached `TaskRun.progress` from execution progress events. |
| `backend/internal/api/handlers/task_run_handler.go` | Existing task-run detail and log read endpoints, with node ownership handling through task join. |
| `backend/internal/probe/prober.go` | Node probe success/failure paths, node status updates, node probe alerts, node alert auto-resolution, metric collection trigger. |
| `backend/internal/metrics/db_sink.go` | Persists metric samples into `node_metric_samples`. |
| `backend/internal/metrics/aggregator.go` | Rolls raw node metrics into hourly/daily aggregate tables with probe_ok/probe_fail counts. |
| `backend/internal/api/handlers/node_metrics_handler.go` | Node status, metric series, and disk forecast read APIs. |
| `backend/internal/api/handlers/overview_handler.go` | Overview summary stats used by dashboard. |
| `backend/internal/api/handlers/overview_backup_health_handler.go` | Computed backup health summary: stale nodes, degraded policies, 7-day success trend. |
| `backend/internal/anomaly/raise.go` | Persists anomaly events for findings and optionally raises alerts. |
| `backend/internal/api/handlers/anomaly_handler.go` | Lists anomaly events globally and per node with filters. |
| `backend/internal/uptime/prober.go` | Service monitor probe loop, uptime sample upsert, status transitions. |
| `backend/cmd/server/main.go` | Wires service monitor down/up transitions to platform alerts with `node_id = 0`. |
| `web/src/types/domain.ts` | Frontend contracts for alerts, deliveries, task runs/log events, backup health, node logs, anomaly events, escalation events, service monitors. |
| `web/src/lib/api/alerts-api.ts` | Frontend alert API client and response mapping. |
| `web/src/lib/api/task-runs-api.ts` | Frontend task-run/log API client and response mapping. |
| `web/src/lib/api/overview-api.ts` | Frontend overview, traffic, backup health, and storage API client. |
| `web/src/hooks/use-alert-bell.ts` | Polls unread counts and fetches recent open alerts for the notification bell. |
| `web/src/components/notification-bell.tsx` | Notification bell UI and alert deep-link navigation. |
| `web/src/pages/notifications-page.tsx` | Notifications page shell, stats, delivery stats, and alert center composition. |
| `web/src/pages/notifications/alert-center.tsx` | Alert list state, filters, pagination, delivery panel state, lazy group-info fetch, alert actions. |
| `web/src/pages/notifications/alert-list.tsx` | Alert table/card rendering, delivery panel, group badge, escalation timeline, anomaly context, log/metrics links. |
| `web/src/pages/notifications/alert-detail.tsx` | Escalation timeline and anomaly context subcomponents used from delivery panel. |
| `web/src/pages/overview-page.tsx` | Overview dashboard shell with stat cards, node/resource panels, traffic chart, recent tasks. |
| `web/src/components/backup-health-panel.tsx` | Backup health UI that consumes computed overview backup health API. |
| `web/src/pages/overview-page.recent-tasks.tsx` | Recent task summary list on overview. |
| `web/src/features/nodes-detail/alerts-tab.tsx` | Node detail alert tab and alert-to-metrics jump integration. |
| `backend/internal/api/handlers/alert_handler_test.go` | Alert bulk resolve and deliveries handler tests. |
| `backend/internal/api/handlers/alert_delivery_handler_test.go` | Alert delivery retry handler tests. |
| `backend/internal/api/handlers/node_metrics_handler_test.go` | Node metrics status/series/disk forecast tests. |
| `backend/internal/api/handlers/task_run_handler_test.go` | Task run and log handler tests. |
| `backend/internal/api/handlers/overview_backup_health_handler_test.go` | Backup health handler tests. |
| `backend/internal/alerting/dispatcher_test.go` | Dispatcher/dedup/delivery alerting tests. |
| `backend/internal/alerting/grouping_test.go` | Grouping behavior tests. |
| `backend/internal/alerting/retry_test.go` | Delivery retry worker tests. |
| `backend/internal/probe/prober_test.go` | Node probe behavior tests. |
| `backend/internal/metrics/aggregator_test.go` | Metric rollup tests. |
| `web/src/components/notification-bell.test.tsx` | Notification bell tests. |
| `web/src/pages/notifications-page.test.tsx` | Notifications page tests. |
| `web/src/pages/overview-page.test.tsx` | Overview page tests. |
| `web/src/features/nodes-detail/alerts-tab.test.tsx` | Node detail alerts tab tests. |
| `web/src/features/nodes-detail/alert-jump.test.ts` | Alert metrics jump helper tests. |
| `web/src/pages/logs/logs-page.alert.test.tsx` | Alert-focused logs page tests. |

### Code Patterns

#### Alert and delivery source records

`backend/internal/model/models.go` defines the main persisted alert timeline sources. `Alert` includes node, task, task-run, SLO, severity, status, error code, message, trigger time, tags, and escalation state:

```go
type Alert struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	NodeID         uint       `gorm:"not null;index:idx_alerts_dedup" json:"node_id"`
	NodeName       string     `gorm:"size:128;not null" json:"node_name"`
	TaskID         *uint      `gorm:"index" json:"task_id"`
	TaskRunID      *uint      `gorm:"index" json:"task_run_id,omitempty"`
	SLOID          *uint      `gorm:"index" json:"slo_id,omitempty"`
	PolicyName     string     `gorm:"size:128" json:"policy_name"`
	Severity       string     `gorm:"size:16;not null;index" json:"severity"`
	Status         string     `gorm:"size:16;not null;index" json:"status"`
	ErrorCode      string     `gorm:"size:64;not null;index:idx_alerts_dedup" json:"error_code"`
	Message        string     `gorm:"type:text;not null" json:"message"`
	Retryable      bool       `gorm:"not null;default:false" json:"retryable"`
	TriggeredAt    time.Time  `gorm:"index" json:"triggered_at"`
	LastNotifiedAt *time.Time `json:"last_notified_at"`
	Tags           string     `gorm:"type:text;not null;default:'[]'" json:"tags"`
	LastLevelFired int        `gorm:"not null;default:-1" json:"last_level_fired"`
	CreatedAt      time.Time  `gorm:"index:idx_alerts_dedup" json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}
```

`AlertDelivery` has retry-specific fields and backend status values documented as `pending|sent|retrying|failed`:

```go
type AlertDelivery struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	AlertID       uint       `gorm:"index;not null" json:"alert_id"`
	IntegrationID uint       `gorm:"index;not null" json:"integration_id"`
	Status        string     `gorm:"size:16;not null" json:"status"` // pending|sent|retrying|failed
	AttemptCount  int        `gorm:"not null;default:0" json:"attempt_count"`
	NextRetryAt   *time.Time `json:"next_retry_at"`
	LastError     string     `gorm:"type:text" json:"last_error"`
	CreatedAt     time.Time  `json:"created_at"`
}
```

#### Alert handler read surface

`backend/internal/api/handlers/alert_handler.go` already exposes paginated alert reads and related details. `List` supports filters for `status`, `node_id`, `task_id`, `severity`, `keyword`, and sorting. `status=unresolved` maps to `status != resolved`. Ownership is enforced through `ownershipNodeFilter`.

Existing handler methods include:

```go
func (h *AlertHandler) List(c *gin.Context)
func (h *AlertHandler) Get(c *gin.Context)
func (h *AlertHandler) GroupInfo(c *gin.Context)
func (h *AlertHandler) Deliveries(c *gin.Context)
func (h *AlertHandler) DeliveryStats(c *gin.Context)
func (h *AlertHandler) UnreadCount(c *gin.Context)
func (h *AlertHandler) EscalationEvents(c *gin.Context)
```

`GroupInfo` is explicitly count-only for now:

```go
// AlertGroupInfo is the response shape for GET /alerts/:id/group-info.
// SiblingNodeIDs is intentionally empty for now: progressive in-memory
// grouping only tracks counts by key, not individual alert identity.
type AlertGroupInfo struct {
	Count          int    `json:"count"`
	SiblingNodeIDs []uint `json:"sibling_node_ids,omitempty"`
}
```

#### Routes already available for timeline source reads

`backend/internal/api/router.go` registers the relevant existing APIs:

```go
secured.GET("/overview", overviewHandler.Get)
secured.GET("/overview/traffic", middleware.RBAC("tasks:read"), overviewTrafficHandler.Get)
secured.GET("/overview/backup-health", middleware.RBAC("tasks:read"), backupHealthHandler.Get)

secured.GET("/nodes/:id/status", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), nodeMetricsHandler.Status)
secured.GET("/nodes/:id/metric-series", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), nodeMetricsHandler.Metrics)
secured.GET("/nodes/:id/disk-forecast", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), nodeMetricsHandler.DiskForecast)

secured.GET("/node-logs", middleware.RBAC("logs:read"), nodeLogsHandler.Query)
secured.GET("/alerts/:id/logs", middleware.RBAC("alerts:read"), nodeLogsHandler.AlertLogs)

secured.GET("/alerts/:id/escalation-events", middleware.RBAC("alerts:read"), alertHandler.EscalationEvents)
secured.GET("/anomaly-events", middleware.RBAC("nodes:read"), anomalyHandler.List)
secured.GET("/nodes/:id/anomaly-events", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), anomalyHandler.ListForNode)

secured.GET("/alerts", middleware.RBAC("alerts:read"), alertHandler.List)
secured.GET("/alerts/unread-count", middleware.RBAC("alerts:read"), alertHandler.UnreadCount)
secured.GET("/alerts/:id", middleware.RBAC("alerts:read"), alertHandler.Get)
secured.GET("/alerts/:id/group-info", middleware.RBAC("alerts:read"), alertHandler.GroupInfo)
secured.GET("/alerts/delivery-stats", middleware.RBAC("alerts:deliveries"), alertHandler.DeliveryStats)
secured.GET("/alerts/:id/deliveries", middleware.RBAC("alerts:deliveries"), alertHandler.Deliveries)

secured.GET("/tasks/:id/runs", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), taskRunHandler.ListByTask)
secured.GET("/task-runs/:id", middleware.RBAC("tasks:read"), taskRunHandler.Get)
secured.GET("/task-runs/:id/logs", middleware.RBAC("tasks:read"), taskRunHandler.Logs)

secured.POST("/alert-deliveries/:id/retry", middleware.RequireRole("admin"), alertDeliveryHandler.Retry)
```

Public service status page route:

```go
v1.GET("/status-page", serviceMonitorHandler.StatusPage)
```

#### Alert creation, dedup, grouping, and dispatch

`backend/internal/alerting/dispatcher.go` centralizes alert creation. Important raising functions found:

```go
func RaiseTaskFailure(db *gorm.DB, task model.Task, taskRunID *uint, message string) error
func RaiseVerificationFailure(db *gorm.DB, task model.Task, taskRunID *uint, message string) error
func ResolveTaskAlerts(db *gorm.DB, taskID uint, note string) error
func RaiseNodeProbeFailure(db *gorm.DB, node model.Node, message string) error
func RaiseDiskUsageAlert(db *gorm.DB, node model.Node, diskPct float64) error
func RaiseNodeExpiryWarning(db *gorm.DB, node model.Node, message string) error
func RaiseRetentionFailure(db *gorm.DB, policyID uint, policyName string, nodeName string, nodeID uint, message string) error
func RaiseIntegrityCheckFailure(db *gorm.DB, policyID uint, policyName string, nodeName string, nodeID uint, message string) error
func RaiseDrillFailure(db *gorm.DB, policyID uint, policyName string, nodeName string, nodeID uint, errorCode string, message string) error
func ResolveAlertsByErrorCode(db *gorm.DB, errorCode string, note string) error
func RaiseStorageSpaceAlert(db *gorm.DB, targetPath string, freeGB float64, totalGB float64, usagePct float64) error
func ResolveNodeAlerts(db *gorm.DB, nodeID uint, note string) error
```

The core `raiseAndDispatch` flow checks deduplication before insert, then may defer dispatch to escalation, applies silences, applies progressive grouping, and persists delivery rows for integrations.

Dedup window source: settings key `alert.dedup_window`, then environment `ALERT_DEDUP_WINDOW`, default `10 * time.Minute`.

Grouping key and window live in `backend/internal/alerting/grouping.go`:

```go
// GroupKey 构建用于分组的规范化 key：category + nodeID + sorted(tags)。
func GroupKey(category string, nodeID uint, nodeTags []string) string {
	tags := append([]string(nil), nodeTags...)
	sort.Strings(tags)
	raw := category + "|" + strconv.FormatUint(uint64(nodeID), 10) + "|" + strings.Join(tags, ",")
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])
}
```

Shared grouping initializes to a 5-minute window:

```go
func init() {
	sharedGrouping.Store(NewGrouping(5 * time.Minute))
}
```

#### Delivery retry behavior

`backend/internal/alerting/retry.go` defines retry cadence and maximum attempts:

```go
var backoffTable = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	8 * time.Minute,
	30 * time.Minute,
}

const maxAttempts = 4
```

The retry worker scans `status='retrying' AND next_retry_at <= now`. Success marks a delivery `sent`, clears `next_retry_at` and `last_error`; failures before max attempts remain `retrying` with a future `next_retry_at`; failures at max attempts become `failed`.

#### TaskRun and TaskLog timeline sources

`backend/internal/model/models.go` defines task-run and task-log records. `TaskRun` has task-level identity, trigger type, status, chain/upstream metadata, progress, verify status, throughput, and error fields, but no direct `node_id`:

```go
type TaskRun struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	TaskID            uint       `gorm:"not null;index" json:"task_id"`
	Task              Task       `gorm:"foreignKey:TaskID" json:"-"`
	TriggerType       string     `gorm:"size:32;not null;default:manual" json:"trigger_type"`
	Status            string     `gorm:"size:32;not null;default:pending;index" json:"status"`
	ChainRunID        string     `gorm:"size:64;index" json:"chain_run_id,omitempty"`
	UpstreamTaskRunID *uint      `gorm:"index" json:"upstream_task_run_id,omitempty"`
	SkipReason        string     `gorm:"type:text" json:"skip_reason,omitempty"`
	StartedAt         *time.Time `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
	DurationMs        int64      `gorm:"not null;default:0" json:"duration_ms"`
	VerifyStatus      string     `gorm:"size:16;not null;default:none" json:"verify_status"`
	ThroughputMbps    float64    `gorm:"not null;default:0" json:"throughput_mbps"`
	Progress          int        `gorm:"not null;default:0" json:"progress"`
	LastError         string     `gorm:"type:text" json:"last_error"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}
```

`TaskLog` provides timestamped log entries by task and optional task-run ID:

```go
type TaskLog struct {
	ID        uint      `gorm:"primaryKey;index:idx_tasklog_task_cursor,priority:2,sort:desc" json:"id"`
	TaskID    uint      `gorm:"not null;index;index:idx_tasklog_task_cursor,priority:1" json:"task_id"`
	TaskRunID *uint     `gorm:"index" json:"task_run_id,omitempty"`
	Level     string    `gorm:"size:16;not null" json:"level"`
	Message   string    `gorm:"type:text;not null" json:"message"`
	CreatedAt time.Time `json:"created_at"`
}
```

`backend/internal/task/manager.go` creates `TaskRun` records when tasks are triggered:

```go
run := model.TaskRun{
	TaskID:            taskID,
	TriggerType:       reason,
	Status:            "pending",
	ChainRunID:        chainRunID,
	UpstreamTaskRunID: upstreamRunID,
}
```

`backend/internal/task/runner.go` updates task-run status and raises alerts. Success updates run fields and resolves task alerts; verification warnings raise verification alerts; final failures raise task failure alerts. Downstream skipped tasks are persisted with `Status: "skipped"`, `TriggerType: "chain"`, `ChainRunID`, `UpstreamTaskRunID`, and `SkipReason`.

`backend/internal/api/handlers/task_run_handler.go` reads run details/logs. It ownership-checks through related `Task.NodeID`, so node-scoped timeline aggregation must join task runs to tasks. Existing logs endpoint queries:

```go
query := h.db.Model(&model.TaskLog{}).Where("task_run_id = ?", runID)
```

#### Node probes and metrics

`backend/internal/probe/prober.go` failure path updates node status and raises probe alerts after a consecutive failure threshold:

```go
updates := map[string]interface{}{
	"status":               "offline",
	"connection_latency":   0,
	"last_probe_at":        now,
	"consecutive_failures": newFailures,
}
```

On success it updates node status, latency, disk, last seen/probe fields, clears consecutive failures, resolves node alerts, and starts metric collection:

```go
updates := map[string]interface{}{
	"status":               "online",
	"connection_latency":   result.Latency,
	"disk_used_gb":         diskUsed,
	"disk_total_gb":        diskTotal,
	"last_probe_at":        now,
	"last_seen_at":         now,
	"consecutive_failures": 0,
}
```

Metric collection writes successful metric samples with `ProbeOK: true`:

```go
ms := metrics.Sample{
	NodeID:    node.ID,
	NodeName:  node.Name,
	SampledAt: time.Now().UTC(),
	CPUPct:    &cpuPct,
	MemPct:    &memPct,
	DiskPct:   &diskPct,
	Load1:     &load1,
	LatencyMs: &latency,
	ProbeOK:   true,
}
```

`backend/internal/model/models.go` defines raw node metrics with sampled timestamp and probe flag:

```go
type NodeMetricSample struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	NodeID      uint      `gorm:"not null;index:idx_node_metric_node_sampled,priority:1" json:"node_id"`
	CpuPct      float64   `gorm:"not null;default:0" json:"cpu_pct"`
	MemPct      float64   `gorm:"not null;default:0" json:"mem_pct"`
	DiskPct     float64   `gorm:"not null;default:0" json:"disk_pct"`
	Load1m      float64   `gorm:"column:load_1m;not null;default:0" json:"load_1m"`
	LatencyMs   *int64    `gorm:"column:latency_ms" json:"latency_ms,omitempty"`
	DiskGBUsed  *float64  `gorm:"column:disk_gb_used" json:"disk_gb_used,omitempty"`
	DiskGBTotal *float64  `gorm:"column:disk_gb_total" json:"disk_gb_total,omitempty"`
	ProbeOK     bool      `gorm:"not null" json:"probe_ok"`
	SampledAt   time.Time `gorm:"not null;index:idx_node_metric_node_sampled,priority:2;index:idx_node_metric_sampled_at" json:"sampled_at"`
	CreatedAt   time.Time `json:"created_at"`
}
```

`backend/internal/metrics/aggregator.go` retains raw metrics for 7 days, hourly rollups for 90 days, and daily rollups for 730 days. Rollups aggregate probe success/failure counts with SQL like:

```sql
SUM(CASE WHEN probe_ok THEN 1 ELSE 0 END),
SUM(CASE WHEN probe_ok THEN 0 ELSE 1 END),
COUNT(*)
```

`backend/internal/api/handlers/node_metrics_handler.go` exposes node status and historical series. Its running task count demonstrates the required join through `tasks`:

```go
h.db.Table("task_runs").
	Joins("JOIN tasks ON tasks.id = task_runs.task_id").
	Where("tasks.node_id = ? AND task_runs.status = ?", id, "running").
	Count(&resp.RunningTasks)
```

Disk forecast response includes confidence:

```go
type diskForecastResponse struct {
	DiskGBTotal   float64  `json:"disk_gb_total"`
	DiskGBUsedNow float64  `json:"disk_gb_used_now"`
	DailyGrowthGB *float64 `json:"daily_growth_gb"`
	Forecast      struct {
		DaysToFull *float64 `json:"days_to_full"`
		DateFull   *string  `json:"date_full"`
		Confidence string   `json:"confidence"`
	} `json:"forecast"`
}
```

#### Backup health / confidence integration points

`backend/internal/api/handlers/overview_backup_health_handler.go` computes backup health from existing task/node state. It identifies stale nodes using `last_backup_at IS NULL OR last_backup_at < ?`, with default threshold 48 hours and environment override `BACKUP_STALE_THRESHOLD_HOURS`.

Degraded policies are computed from recent task runs joined through tasks and policies; a policy is degraded if its latest three relevant runs are failed. The response includes `stale_nodes`, `stale_node_count`, `degraded_policies`, `degraded_count`, `trend`, `summary`, and `generated_at`.

Frontend backup health type in `web/src/types/domain.ts`:

```ts
export interface BackupHealthData {
  staleNodes: StaleNode[];
  degradedPolicies: DegradedPolicy[];
  healthTrend: HealthTrendPoint[];
  summary: {
    totalNodes: number;
    neverBackedUp: number;
    stale48h: number;
    policiesHealthy: number;
    policiesDegraded: number;
    successRate7d: number;
  };
}
```

`web/src/lib/api/overview-api.ts` maps `/overview/backup-health` into this shape and derives values such as `neverBackedUp`, `stale48h`, and `successRate7d`.

#### Anomaly and service monitor events

`backend/internal/anomaly/raise.go` persists an `AnomalyEvent` for each finding whether or not an alert is created:

```go
evt := model.AnomalyEvent{
	NodeID:        f.NodeID,
	Detector:      f.Detector,
	Metric:        f.Metric,
	Severity:      f.Severity,
	ObservedValue: f.ObservedValue,
	BaselineValue: f.BaselineValue,
	Sigma:         f.Sigma,
	ForecastDays:  f.ForecastDays,
	RaisedAlert:   raisedNew,
	Details:       string(detailsJSON),
	FiredAt:       time.Now().UTC(),
}
```

`backend/internal/uptime/prober.go` tracks service monitors and uptime samples. `backend/cmd/server/main.go` converts service monitor down transitions into platform alerts with `node_id = 0`:

```go
_, _, alertErr := raiser.RaiseAnomalyAlert(alerting.AnomalyAlertInput{
	NodeID:    0, // service monitors are not node-scoped
	NodeName:  monitor.Name,
	Severity:  "critical",
	ErrorCode: fmt.Sprintf("XR-SERVICE-DOWN-%d", monitor.ID),
	Message:   fmt.Sprintf("服务 %s 不可达 (%s)", monitor.Name, monitor.Target),
})
```

Recovery resolves matching open alerts by error code:

```go
db.Model(&model.Alert{}).Where("error_code = ? AND status = 'open'",
	fmt.Sprintf("XR-SERVICE-DOWN-%d", monitor.ID)).
	Updates(map[string]interface{}{
		"status":     "resolved",
		"updated_at": time.Now(),
	})
```

#### Frontend alert and notification foundations

`web/src/types/domain.ts` defines alert and delivery contracts:

```ts
export interface AlertRecord {
  id: string;
  nodeName: string;
  nodeId: number;
  taskId?: number | null;
  taskRunId?: number | null;
  sloId?: number | null;
  policyName: string;
  severity: AlertSeverity;
  status: AlertStatus;
  errorCode: string;
  message: string;
  triggeredAt: string;
  retryable: boolean;
}

export interface AlertDeliveryRecord {
  id: string;
  alertId: string;
  integrationId: string;
  status: "sent" | "failed";
  createdAt: string;
  attemptCount?: number;
  nextRetryAt?: string | null;
  lastError?: string | null;
}
```

`web/src/lib/api/alerts-api.ts` maps backend alert/delivery APIs. Alert IDs and delivery IDs are prefixed strings (`alert-<id>`, `delivery-<id>`). The delivery mapper currently collapses every non-`failed` backend status to `sent`:

```ts
status: row.status === "failed" ? "failed" : "sent",
```

`getRecentAlerts` sets `limit`, while backend alert list pagination uses `page_size`:

```ts
query.set("status", "open");
if (options?.limit) {
  query.set("limit", String(options.limit));
}
```

`web/src/hooks/use-alert-bell.ts` polls unread counts every 30 seconds and fetches recent open alerts on demand. `web/src/components/notification-bell.tsx` deep-links alerts to `/app/notifications?alert=${alert.id}`.

`web/src/pages/notifications/alert-center.tsx` owns alert filters, pagination, sorting, selected IDs, delivery panel state, and lazy group count cache. Group count is fetched only when a delivery panel opens:

```ts
void apiClient.getAlertGroupInfo(token, alertId)
  .then((gi) => setGroupInfoMap((prev) => ({ ...prev, [alertId]: { count: gi.count } })))
  .catch(() => { /* non-critical; badge simply doesn't render */ });
```

`web/src/pages/notifications/alert-list.tsx` renders cards/tables, delivery panels, group badges, escalation timeline, anomaly context, logs and metric links. Delivery panel includes:

```tsx
<AlertEscalationTimeline token={token} alertId={Number(alert.id)} />
<AnomalyAlertContext token={token} errorCode={alert.errorCode} nodeId={alert.nodeId} />
```

Because frontend alert IDs are strings like `alert-123`, `Number(alert.id)` evaluates to `NaN`.

#### Frontend task-run/log mapping constraints

`web/src/lib/api/task-runs-api.ts` maps task-run APIs. The response type read did not include `chain_run_id`, `upstream_task_run_id`, or `skip_reason`, although `TaskRunRecord` has corresponding fields. `mapTriggerType` maps `manual`, `cron`, `retry`, and `restore`, but not `chain` or `drill`. `mapRunStatus` does not map `retrying` or `skipped`, even though `TaskStatus` includes them.

### External References

None. The requested research was internal codebase research only.

### Related Specs

No `.trellis/spec/**/*.md` files were read during this research pass. The task-specific target is `.trellis/tasks/05-17-health-incident-timeline`.

## Caveats / Not Found

- No persisted `Incident` model, `IncidentTimeline` model, or dedicated incident/timeline aggregation endpoint was found in the inspected code.
- Current progressive alert grouping is in memory, process-local, and count-only. It does not persist group membership and `GroupInfo.SiblingNodeIDs` is intentionally empty.
- Alert dedup prevents some repeated alerts from being persisted at all within the dedup window, so a read-only timeline built only from `alerts` cannot show suppressed duplicates.
- `TaskRun` has no direct `node_id`; node-scoped timeline aggregation must join `task_runs -> tasks` and apply the same ownership constraints used by existing handlers.
- Normal node probe failure path updates node status and can raise alerts, but the read prober path inspected only writes `NodeMetricSample` rows on successful metric collection with `ProbeOK: true`; failed probe samples may therefore be absent from raw metrics unless another writer exists outside the inspected path.
- Platform/service/SLO-style alerts use `node_id = 0`; node-specific timelines need explicit handling so these are not incorrectly treated as node-owned events.
- Frontend `AlertDeliveryRecord.status` only allows `sent | failed`, while backend delivery records can be `pending | sent | retrying | failed`.
- `web/src/lib/api/alerts-api.ts#getRecentAlerts` sends `limit`, but the backend alert list handler uses `page_size`; the limit query appears ineffective based on inspected code.
- `web/src/pages/notifications/alert-list.tsx` passes `Number(alert.id)` to escalation timeline even though IDs are prefixed strings like `alert-123`, yielding `NaN`.
- Backup health frontend degraded policy fields (`consecutiveFailures`, `lastFailedAt`) may map to defaults when backend degraded policy rows only include ID/name in the current handler shape.
