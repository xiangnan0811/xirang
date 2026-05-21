export type NodeStatus = "online" | "offline" | "warning";
export type NodeAuthType = "key" | "password";

export type TaskStatus =
  | "running"
  | "pending"
  | "failed"
  | "success"
  | "retrying"
  | "canceled"
  | "warning"
  | "skipped";

export type TaskExecutorType = "rsync" | "command" | "restic" | "rclone";

export type AlertSeverity = "critical" | "warning" | "info";
export type AlertStatus = "open" | "acked" | "resolved";
export type IntegrationType = "email" | "slack" | "telegram" | "webhook" | "feishu" | "dingtalk" | "wecom";
export type SSHKeyType = "auto" | "rsa" | "ed25519" | "ecdsa";

const SSH_KEY_TYPES: ReadonlySet<string> = new Set<SSHKeyType>(["rsa", "ed25519", "ecdsa"]);

export function parseSSHKeyType(value: string): SSHKeyType {
  return SSH_KEY_TYPES.has(value) ? (value as SSHKeyType) : "auto";
}

export interface OverviewStats {
  totalNodes: number;
  healthyNodes: number;
  activePolicies: number;
  runningTasks: number;
  failedTasks24h: number;
  overallSuccessRate: number;
  avgSyncMbps: number;
}

export interface OverviewSummary {
  totalNodes: number;
  healthyNodes: number;
  activePolicies: number;
  runningTasks: number;
  failedTasks24h: number;
  currentThroughputMbps: number;
}

export type OverviewTrafficWindow = "1h" | "24h" | "7d";

export interface OverviewTrafficPoint {
  timestamp: string;
  timestampMs: number;
  label: string;
  throughputMbps: number;
  sampleCount: number;
  activeTaskCount: number;
  startedCount: number;
  failedCount: number;
}

export interface OverviewTrafficSeries {
  window: OverviewTrafficWindow;
  bucketMinutes: number;
  hasRealSamples: boolean;
  generatedAt: string;
  points: OverviewTrafficPoint[];
}

export type HealthIncidentSeverity = AlertSeverity;
export type HealthIncidentResourceType = "node" | "task" | "policy" | "platform";
export type HealthIncidentSourceType =
  | "alert"
  | "task_failure"
  | "notification_failure"
  | "anomaly"
  | "probe"
  | "metric"
  | "backup_stale"
  | "backup_degraded";

export interface HealthIncidentTimelineSummary {
  total: number;
  critical: number;
  warning: number;
  info: number;
}

export interface HealthIncidentResource {
  type: HealthIncidentResourceType;
  id?: number;
  name: string;
  nodeId?: number;
  nodeName?: string;
  policyId?: number;
  policyName?: string;
}

export interface HealthIncidentAction {
  code: string;
  label: string;
  href: string;
}

export interface HealthIncidentSignal {
  type: HealthIncidentSourceType;
  severity: HealthIncidentSeverity;
  occurredAt: string;
  message: string;
  alertId?: number;
  deliveryId?: number;
  taskId?: number;
  taskRunId?: number;
  nodeId?: number;
  policyId?: number;
}

export interface HealthIncidentGroup {
  id: string;
  severity: HealthIncidentSeverity;
  resource: HealthIncidentResource;
  lastSeenAt: string;
  eventCount: number;
  likelyCause: string;
  sourceTypes: HealthIncidentSourceType[];
  nextActions: HealthIncidentAction[];
  signals: HealthIncidentSignal[];
}

export interface HealthIncidentTimelineData {
  generatedAt: string;
  windowHours: number;
  summary: HealthIncidentTimelineSummary;
  groups: HealthIncidentGroup[];
}

export interface NodeRecord {
  id: number;
  name: string;
  host: string;
  address: string;
  ip: string;
  port: number;
  username: string;
  authType: NodeAuthType;
  keyId?: string | null;
  basePath?: string;
  status: NodeStatus;
  tags: string[];
  lastSeenAt: string;
  lastBackupAt: string;
  diskFreePercent: number;
  diskUsedGb: number;
  diskTotalGb: number;
  diskProbeAt?: string;
  connectionLatencyMs?: number;
  lastProbeAt?: string;
  maintenanceStart?: string;
  maintenanceEnd?: string;
  expiryDate?: string;
  archived?: boolean;
  backupDir?: string;
  useSudo?: boolean;
}

export type NodeDoctorCheckStatus = "pass" | "warn" | "fail" | "skip";

export interface NodeDoctorCheckResult {
  check: string;
  status: NodeDoctorCheckStatus;
  evidence: string;
  suggestion: string;
}

export interface NodeDoctorResult {
  nodeId: number;
  nodeName: string;
  generatedAt: string;
  checks: NodeDoctorCheckResult[];
}

export type RestoreDrillStatus = "pending" | "running" | "success" | "failed" | "skipped" | "canceled";

export interface PolicyLatestDrillSummary {
  taskRunId: number;
  status: RestoreDrillStatus;
  failedStep?: string;
  confidenceEligible: boolean;
  startedAt?: string;
  finishedAt?: string;
  durationMs: number;
}

export interface PolicyRecord {
  id: number;
  name: string;
  sourcePath: string;
  targetPath: string;
  cron: string;
  naturalLanguage: string;
  enabled: boolean;
  criticalThreshold: number;
  nodeIds: number[];
  verifyEnabled: boolean;
  verifySampleRate: number;
  isTemplate?: boolean;
  preHook?: string;
  postHook?: string;
  hookTimeoutSeconds?: number;
  maxRetries?: number;
  retryBaseSeconds?: number;
  bandwidthSchedule?: string;
  escalation_policy_id?: number | null;
  app_profile?: string;
  app_credential_id?: number | null;

  // Retention & SLA
  retention_days?: number;
  retention_mode?: string;
  keep_daily?: number;
  keep_weekly?: number;
  keep_monthly?: number;
  keep_yearly?: number;
  rpo_minutes?: number;
  rto_minutes?: number;

  // Recovery drill
  drill_enabled: boolean;
  drill_cron: string;
  drill_target_node_id?: number | null;
  drill_restore_path: string;
  drill_pre_verify: string;
  drill_verify: string;
  drill_post_verify: string;
  drill_auto_cleanup: boolean;
  latestDrill?: PolicyLatestDrillSummary | null;
}

export interface NewPolicyInput {
  name: string;
  sourcePath: string;
  targetPath?: string;
  cron: string;
  criticalThreshold: number;
  enabled: boolean;
  nodeIds: number[];
  verifyEnabled: boolean;
  verifySampleRate: number;
  preHook?: string;
  postHook?: string;
  hookTimeoutSeconds?: number;
  maxRetries?: number;
  retryBaseSeconds?: number;
  bandwidthSchedule?: string;
  escalation_policy_id?: number | null;
  app_profile?: string;
  app_credential_id?: number | null;

  // Retention & SLA
  retention_days?: number;
  retention_mode?: string;
  keep_daily?: number;
  keep_weekly?: number;
  keep_monthly?: number;
  keep_yearly?: number;
  rpo_minutes?: number;
  rto_minutes?: number;

  // Recovery drill
  drill_enabled?: boolean;
  drill_cron?: string;
  drill_target_node_id?: number | null;
  drill_restore_path?: string;
  drill_pre_verify?: string;
  drill_verify?: string;
  drill_post_verify?: string;
  drill_auto_cleanup?: boolean;
}

export interface TaskRecord {
  id: number;
  name?: string;
  policyName: string;
  policyId?: number | null;
  nodeName: string;
  nodeId: number;
  dependsOnTaskId?: number | null;
  createdAt?: string;
  status: TaskStatus;
  progress: number;
  hasActiveRun?: boolean;
  startedAt: string;
  nextRunAt?: string;
  errorCode?: string;
  lastError?: string;
  retryCount?: number;
  command?: string;
  rsyncSource?: string;
  rsyncTarget?: string;
  executorType?: TaskExecutorType;
  executorConfig?: string;
  cronSpec?: string;
  updatedAt?: string;
  speedMbps: number;
  source?: string;
  verifyStatus?: "none" | "passed" | "warning" | "failed";
  enabled: boolean;
  skipNext?: boolean;
}

export interface NewTaskInput {
  name: string;
  nodeId: number;
  policyId?: number | null;
  dependsOnTaskId?: number | null;
  command?: string;
  rsyncSource?: string;
  rsyncTarget?: string;
  executorType?: TaskExecutorType;
  executorConfig?: string;
  cronSpec?: string;
}

export type TaskRunTriggerType = "manual" | "cron" | "retry" | "restore" | "chain" | "drill";

export interface RestoreDrillEvidence {
  id: number;
  policyId: number;
  taskId: number;
  taskRunId: number;
  sourceTaskRunId?: number | null;
  snapshotRef?: string;
  sandboxNodeId: number;
  sandboxNodeName: string;
  sandboxPath: string;
  status: RestoreDrillStatus;
  failedStep?: string;
  confidenceEligible: boolean;
  startedAt?: string;
  finishedAt?: string;
  durationMs: number;
  restoreStatus: RestoreDrillStatus;
  restoreStartedAt?: string;
  restoreFinishedAt?: string;
  restoreError?: string;
  verifyStatus: RestoreDrillStatus;
  verifyStartedAt?: string;
  verifyFinishedAt?: string;
  verifyError?: string;
  postVerifyStatus: RestoreDrillStatus;
  postVerifyFinishedAt?: string;
  postVerifyError?: string;
  cleanupStatus: RestoreDrillStatus;
  cleanupStartedAt?: string;
  cleanupFinishedAt?: string;
  cleanupError?: string;
  createdAt: string;
  updatedAt?: string;
}

export interface TaskRunRecord {
  id: number;
  taskId: number;
  triggerType: TaskRunTriggerType;
  status: TaskStatus;
  chainRunId?: string;
  upstreamTaskRunId?: number | null;
  skipReason?: string;
  startedAt?: string;
  finishedAt?: string;
  durationMs: number;
  verifyStatus: "none" | "passed" | "warning" | "failed";
  throughputMbps: number;
  progress: number;
  lastError?: string;
  createdAt: string;
  drillEvidence?: RestoreDrillEvidence | null;
}

export interface LogEvent {
  id: string;
  logId?: number;
  timestamp: string;
  timestampMs?: number;
  level: "info" | "warn" | "error";
  message: string;
  nodeName?: string;
  taskId?: number;
  taskRunId?: number;
  errorCode?: string;
  progress?: number;
  status?: TaskStatus;
}

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

export interface IntegrationProbeResult {
  ok: boolean;
  message: string;
  latencyMs: number;
}

export interface AlertDeliveryRetryResult {
  ok: boolean;
  message: string;
  delivery: AlertDeliveryRecord;
}

export interface AlertBulkRetryResult {
  ok: boolean;
  message: string;
  totalFailed: number;
  successCount: number;
  failedCount: number;
  newDeliveries: AlertDeliveryRecord[];
}

export interface AlertBulkResolveResult {
  resolvedCount: number;
  skippedCount: number;
}

export interface AlertDeliveryIntegrationStat {
  integrationId: string;
  name: string;
  type: string;
  sent: number;
  failed: number;
  successRate: number;
}

export interface AlertDeliveryStats {
  windowHours: number;
  totalSent: number;
  totalFailed: number;
  successRate: number;
  byIntegration: AlertDeliveryIntegrationStat[];
}

export interface IntegrationChannel {
  id: string;
  type: IntegrationType;
  name: string;
  endpoint: string;
  hasSecret: boolean;
  enabled: boolean;
  failThreshold: number;
  cooldownMinutes: number;
  proxyUrl: string;
}

export interface NewIntegrationInput {
  type: IntegrationType;
  name: string;
  endpoint: string;
  failThreshold: number;
  cooldownMinutes: number;
  enabled: boolean;
  secret?: string;
  skipEndpointHint?: boolean;
  botToken?: string;
  chatId?: string;
  accessToken?: string;
  hookId?: string;
  webhookKey?: string;
  proxyUrl?: string;
}

export interface SSHKeyScopeFields {
  disabled: boolean;
  expiresAt?: string;
  allowedPurposes: string;
  allowedNodeIds: string;
  allowedNodeTags: string;
}

export interface SSHKeyRecord extends SSHKeyScopeFields {
  id: string;
  name: string;
  username: string;
  keyType: SSHKeyType;
  privateKey?: string;
  publicKey?: string;
  fingerprint: string;
  broadScope: boolean;
  createdAt: string;
  lastUsedAt?: string;
}

export interface NewSSHKeyInput extends SSHKeyScopeFields {
  name: string;
  username: string;
  keyType: SSHKeyType;
  privateKey: string;
}

export interface NewNodeInput {
  name: string;
  host: string;
  port: number;
  username: string;
  authType: NodeAuthType;
  keyId?: string | null;
  password?: string;
  tags: string;
  basePath?: string;
  inlineKeyName?: string;
  inlineKeyType?: SSHKeyType;
  inlinePrivateKey?: string;
  maintenanceStart?: string;
  maintenanceEnd?: string;
  expiryDate?: string;
  backupDir?: string;
  useSudo?: boolean;
}

export interface LoginResponse {
  token?: string;
  user?: {
    id: number;
    username: string;
    role: "admin" | "operator" | "viewer";
    totp_enabled?: boolean;
  };
  requires_2fa?: boolean;
  login_token?: string;
}

export interface UserRecord {
  id: number;
  username: string;
  role: "admin" | "operator" | "viewer";
  totpEnabled?: boolean;
}

export interface AuditLogRecord {
  id: number;
  userId: number;
  username: string;
  role: string;
  method: string;
  path: string;
  statusCode: number;
  clientIP: string;
  userAgent: string;
  createdAt: string;
}

export type CredentialAuditOutcome = "success" | "failure" | "blocked" | "unknown";

export type CredentialAuditAction =
  | "ssh_key.test_connection"
  | "ssh_key.export"
  | "node.credential.test_connection"
  | "auth.step_up"
  | "terminal.open"
  | "terminal.failure"
  | "terminal.close"
  | "task.manual_trigger"
  | "task.restore_trigger"
  | "task.batch_trigger"
  | "snapshot.restore"
  | "batch_command.create"
  | "task.credential.use"
  | "drill.trigger"
  | "drill.phase"
  | "file_browser.list"
  | "file_browser.preview"
  | "docker_volumes.discover"
  | "config.export"
  | "config.import"
  | "node.doctor.run"
  | "node_migration.preflight"
  | "probe.ssh"
  | "probe.metrics"
  | "node_logs.collect"
  | "other";

export type CredentialAuditMetadataValue = string | number | boolean | string[];

export type CredentialAccessGrantAction = "terminal.open" | "config.import" | "unknown";
export type CredentialAccessGrantPurpose = "terminal" | "config_import" | "unknown";
export type CredentialAccessGrantStatus = "requested" | "approved" | "active" | "denied" | "expired" | "revoked";

export interface CredentialAccessGrant {
  id: number;
  requesterUserId: number;
  requesterUsername: string;
  requesterRole: string;
  action: CredentialAccessGrantAction;
  purpose: CredentialAccessGrantPurpose;
  nodeId?: number;
  taskId?: number;
  policyId?: number;
  reason: string;
  status: CredentialAccessGrantStatus;
  requestedTtlSeconds: number;
  requestedAt: string;
  approvedAt?: string;
  approverUserId?: number;
  approverUsername?: string;
  expiresAt: string;
  revokedAt?: string;
  revokedByUserId?: number;
  createdAt: string;
  updatedAt: string;
}

export interface CredentialAuditEventRecord {
  id: number;
  userId: number;
  username: string;
  role: string;
  action: CredentialAuditAction;
  rawAction: string;
  purpose: string;
  credentialKind: string;
  credentialSource: string;
  sshKeyId?: number;
  nodeId?: number;
  taskId?: number;
  taskRunId?: number;
  policyId?: number;
  outcome: CredentialAuditOutcome;
  errorMessage: string;
  metadata: Record<string, CredentialAuditMetadataValue>;
  clientIP: string;
  userAgent: string;
  createdAt: string;
}

export interface StaleNode {
  nodeId: number;
  nodeName: string;
  lastBackupAt: string | null;
  hoursSince: number;
}

export interface DegradedPolicy {
  policyId: number;
  policyName: string;
  consecutiveFailures: number;
  lastFailedAt: string;
}

export interface HealthTrendPoint {
  date: string;
  total: number;
  success: number;
  rate: number;
}

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

export type BackupConfidenceStatus = "healthy" | "warning" | "at_risk" | "insufficient";
export type BackupConfidenceSeverity = "info" | "warning" | "critical";

export interface BackupConfidenceReason {
  code: string;
  severity: BackupConfidenceSeverity;
  message: string;
}

export interface BackupConfidenceEvidence {
  type: string;
  status: string;
  message: string;
  observedAt?: string;
  taskId?: number;
  taskRunId?: number;
  alertId?: number;
}

export interface BackupConfidenceNextStep {
  code: string;
  label: string;
}

export interface BackupConfidenceTarget {
  nodeId: number;
  nodeName: string;
  lastBackupAt?: string;
}

export interface BackupConfidenceItem {
  id: string;
  scope: "policy" | "node";
  policyId?: number;
  policyName?: string;
  nodeId?: number;
  nodeName?: string;
  status: BackupConfidenceStatus;
  score: number;
  reasons: BackupConfidenceReason[];
  evidence: BackupConfidenceEvidence[];
  nextSteps: BackupConfidenceNextStep[];
  targets: BackupConfidenceTarget[];
}

export interface BackupConfidenceData {
  generatedAt: string;
  summary: {
    healthy: number;
    warning: number;
    atRisk: number;
    insufficient: number;
    total: number;
  };
  items: BackupConfidenceItem[];
}

export interface MountPointInfo {
  path: string;
  usedGB: number;
  totalGB: number;
  pct: number;
}

export interface NodeStorageInfo {
  nodeId: number;
  nodeName: string;
  path: string;
  usedGB: number;
}

export interface StorageUsageData {
  mountPoints: MountPointInfo[];
  perNode: NodeStorageInfo[];
}

export interface HookTemplate {
  id: string;
  name: string;
  preHook: string;
  postHook: string;
  description: string;
}

export interface AppCredential {
  id: number;
  name: string;
  type: string;
  description: string;
  config: Record<string, string>;
  has_password: boolean;
  reference_count: number;
  created_at: string;
  updated_at: string;
}

export interface ConfigField {
  key: string;
  label: string;
  type: string;
  required: boolean;
  placeholder?: string;
}

export interface ProfileSchema {
  id: string;
  name: string;
  description: string;
  credential_type: string;
  is_docker: boolean;
  config_schema: ConfigField[];
}

export type SLOMetricType = "availability" | "success_rate";
export type SLOStatus = "healthy" | "warning" | "breached" | "insufficient_data";

export type SLODefinition = {
  id: number;
  name: string;
  metric_type: SLOMetricType;
  match_tags: string | string[] | null;
  threshold: number;
  window_days: number;
  enabled: boolean;
  created_by: number;
  created_at: string;
  updated_at: string;
  escalation_policy_id?: number | null;
};

export type SLOComplianceResult = {
  slo_id: number;
  name: string;
  metric_type: SLOMetricType;
  window_start: string;
  window_end: string;
  threshold: number;
  observed: number;
  sample_count: number;
  error_budget_remaining_pct: number;
  burn_rate_1h: number;
  status: SLOStatus;
};

export type SLOSummary = {
  total: number;
  healthy: number;
  warning: number;
  breached: number;
  insufficient: number;
};

export type NodeLogSource = "journalctl" | "file";
export type NodeLogPriority =
  | "emerg"
  | "alert"
  | "crit"
  | "err"
  | "warning"
  | "notice"
  | "info"
  | "debug"
  | "";

export type NodeLogEntry = {
  id: number;
  node_id: number;
  source: NodeLogSource;
  path: string;
  timestamp: string;
  priority: NodeLogPriority;
  message: string;
  created_at: string;
};

export type NodeLogQueryResult = {
  data: NodeLogEntry[];
  total: number;
  has_more: boolean;
};

export type NodeLogConfig = {
  log_paths: string[];
  log_journalctl_enabled: boolean;
  log_retention_days: number;
};

export type AlertLogsResult = {
  data: NodeLogEntry[];
  node_id: number;
  window_start: string;
  window_end: string;
  hint?: string;
};

export type NodeLogsSettings = {
  default_retention_days: number;
};

export type DashboardTimeRange = "1h" | "6h" | "24h" | "7d" | "custom";
export type ChartType = "line" | "area" | "bar" | "number" | "table";
export type Aggregation = "avg" | "max" | "min" | "sum" | "p50" | "p95" | "p99";

export type PanelFilters = {
  node_ids?: number[];
  task_ids?: number[];
};

export type Panel = {
  id: number;
  dashboard_id: number;
  title: string;
  chart_type: ChartType;
  metric: string;
  filters: PanelFilters;
  aggregation: Aggregation;
  layout_x: number;
  layout_y: number;
  layout_w: number;
  layout_h: number;
};

export type Dashboard = {
  id: number;
  owner_id: number;
  name: string;
  description: string;
  time_range: DashboardTimeRange;
  custom_start?: string | null;
  custom_end?: string | null;
  auto_refresh_seconds: number;
  created_at: string;
  updated_at: string;
  panels?: Panel[];
};

export type MetricDescriptor = {
  key: string;
  label: string;
  family: "node" | "task";
  default_aggregation: Aggregation;
  supported_aggregations: Aggregation[];
};

export type PanelQueryPoint = { ts: string; value: number };
export type PanelQuerySeries = { name: string; points: PanelQueryPoint[] };
export type PanelQueryResult = {
  series: PanelQuerySeries[];
  step_seconds: number;
  /** Set when the backend fetch hit MaxRowsPerQuery. UI should warn the user
   *  their series is incomplete and suggest narrowing the range/filters. */
  truncated?: boolean;
};

/** Silence matches the backend shape at `/silences`. match_tags arrives from
 *  older records as a JSON-encoded string, from newer ones as an array, and
 *  from either as null. parseSilenceTags (lib/api/silences) normalizes. */
export interface Silence {
  id: number;
  name: string;
  match_node_id: number | null;
  match_category: string;
  match_tags: string | string[] | null;
  starts_at: string;
  ends_at: string;
  created_by: number;
  note: string;
  created_at: string;
  updated_at: string;
}

export interface SilenceInput {
  name: string;
  match_node_id: number | null;
  match_category: string;
  match_tags: string[];
  starts_at: string;
  ends_at: string;
  note?: string;
}

export type EscalationLevel = {
  delay_seconds: number;
  integration_ids: number[];
  severity_override: "" | "info" | "warning" | "critical";
  tags: string[];
};

export type EscalationPolicy = {
  id: number;
  name: string;
  description: string;
  min_severity: "info" | "warning" | "critical";
  enabled: boolean;
  levels: EscalationLevel[];
  created_at: string;
  updated_at: string;
};

export type EscalationEvent = {
  id: number;
  alert_id: number;
  escalation_policy_id: number | null;
  level_index: number;
  integration_ids: number[];
  severity_before: "info" | "warning" | "critical";
  severity_after: "info" | "warning" | "critical";
  tags_added: string[];
  fired_at: string;
};

export type AnomalyDetector = "ewma" | "disk_forecast";

export type AnomalyEvent = {
  id: number;
  node_id: number;
  detector: AnomalyDetector;
  metric: string;
  severity: "warning" | "critical";
  observed_value: number;
  baseline_value: number;
  sigma?: number | null;
  forecast_days?: number | null;
  alert_id?: number | null;
  raised_alert: boolean;
  details?: string;
  fired_at: string;
};

export type AnomalyListResult = {
  data: AnomalyEvent[];
  total: number;
  has_more: boolean;
};

export interface AutomationRule {
  id: number;
  name: string;
  description: string;
  event_type: string;
  event_filter: Record<string, string>;
  action_type: string;
  action_config: Record<string, string>;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface AutomationRuleInput {
  name: string;
  description?: string;
  event_type: string;
  event_filter?: Record<string, string>;
  action_type: string;
  action_config?: Record<string, string>;
  enabled?: boolean;
}

export interface ServiceMonitor {
  id: number;
  name: string;
  description: string;
  type: "http" | "tcp";
  target: string;
  interval_seconds: number;
  timeout_seconds: number;
  http_method: string;
  http_expected_status: number;
  http_headers: string; // JSON string from backend, parsed on client side
  enabled: boolean;
  last_status: "up" | "down" | "unknown";
  uptime_pct: number;
  last_checked_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface NewServiceMonitorInput {
  name: string;
  description?: string;
  type: "http" | "tcp";
  target: string;
  interval_seconds?: number;
  timeout_seconds?: number;
  http_method?: string;
  http_expected_status?: number;
  http_headers?: string; // JSON.stringify(headersObject)
  enabled?: boolean;
}

export interface StatusPageItem {
  name: string;
  type: string;
  status: "up" | "down" | "unknown";
  uptime_pct: number;
  last_checked_at: string | null;
}
