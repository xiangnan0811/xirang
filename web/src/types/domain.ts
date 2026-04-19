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

export type TaskRunTriggerType = "manual" | "cron" | "retry" | "restore" | "chain";

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
  error?: string;
  createdAt: string;
  // retry-related fields (added in P5b Task 4)
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

export interface SSHKeyRecord {
  id: string;
  name: string;
  username: string;
  keyType: SSHKeyType;
  privateKey?: string;
  publicKey?: string;
  fingerprint: string;
  createdAt: string;
  lastUsedAt?: string;
}

export interface NewSSHKeyInput {
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
