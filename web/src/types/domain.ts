export type NodeStatus = "online" | "offline" | "warning";
export type NodeAuthType = "key" | "password";

export type TaskStatus =
  | "running"
  | "pending"
  | "failed"
  | "success"
  | "retrying"
  | "canceled"
  | "warning";

export type TaskExecutorType = "rsync";

export type AlertSeverity = "critical" | "warning" | "info";
export type AlertStatus = "open" | "acked" | "resolved";
export type IntegrationType = "email" | "slack" | "telegram" | "webhook";
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
}

export interface NewPolicyInput {
  name: string;
  sourcePath: string;
  targetPath: string;
  cron: string;
  criticalThreshold: number;
  enabled: boolean;
  nodeIds: number[];
  verifyEnabled: boolean;
  verifySampleRate: number;
}

export interface TaskRecord {
  id: number;
  name?: string;
  policyName: string;
  policyId?: number | null;
  nodeName: string;
  nodeId: number;
  createdAt?: string;
  status: TaskStatus;
  progress: number;
  startedAt: string;
  nextRunAt?: string;
  errorCode?: string;
  lastError?: string;
  retryCount?: number;
  command?: string;
  rsyncSource?: string;
  rsyncTarget?: string;
  executorType?: TaskExecutorType;
  cronSpec?: string;
  updatedAt?: string;
  speedMbps: number;
  source?: string;
  verifyStatus?: "none" | "passed" | "warning" | "failed";
}

export interface NewTaskInput {
  name: string;
  nodeId: number;
  policyId?: number | null;
  rsyncSource?: string;
  rsyncTarget?: string;
  executorType?: TaskExecutorType;
  cronSpec?: string;
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
  errorCode?: string;
  progress?: number;
  status?: TaskStatus;
}

export interface AlertRecord {
  id: string;
  nodeName: string;
  nodeId: number;
  taskId?: number | null;
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
  enabled: boolean;
  failThreshold: number;
  cooldownMinutes: number;
}

export interface NewIntegrationInput {
  type: IntegrationType;
  name: string;
  endpoint: string;
  failThreshold: number;
  cooldownMinutes: number;
  enabled: boolean;
}

export interface SSHKeyRecord {
  id: string;
  name: string;
  username: string;
  keyType: SSHKeyType;
  privateKey?: string;
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
}

export interface LoginResponse {
  token: string;
  user: {
    id: number;
    username: string;
    role: "admin" | "operator" | "viewer";
  };
}

export interface UserRecord {
  id: number;
  username: string;
  role: "admin" | "operator" | "viewer";
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
