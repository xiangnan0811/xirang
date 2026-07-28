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
  /** True when server hit sample/task-run row cap; series may under-count. */
  truncated?: boolean;
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

export type RsyncPublicationMode =
  | "legacy_mutable"
  | "versioned_hardlink"
  | "versioned_full_copy";

export type RsyncVersionedPublicationMode = Exclude<RsyncPublicationMode, "legacy_mutable">;

export type RsyncPublicationState =
  | "legacy"
  | "preflight_required"
  | "ready"
  | "preparing"
  | "verifying"
  | "committed"
  | "failed"
  | "blocked"
  | "rollback_prepared";

export type RsyncPublicationReasonCode =
  | "legacy"
  | "preflight_required"
  | "ready"
  | "preflight_expired"
  | "task_revision_changed"
  | "preflight_mismatch"
  | "root_drift"
  | "unsupported"
  | "admission_blocked"
  | "rollback_prepared";

export type RsyncVersioningMigrationChoice = "imported_baseline" | "first_new_point";

export type RsyncVersioningEstimateBucket = "unknown" | "constrained" | "available";

export interface RsyncPublicationSummary {
  mode: RsyncPublicationMode;
  state: RsyncPublicationState;
  reasonCode: RsyncPublicationReasonCode;
  capabilityRevision: number;
  /** Exact decimal CAS token. It is intentionally not a JavaScript number. */
  taskRevision: string;
  seedFullCopyRequired: boolean;
}

export interface RsyncVersioningPreflightResult {
  preflightId: string;
  mode: RsyncVersionedPublicationMode;
  state: RsyncPublicationState;
  reasonCode: RsyncPublicationReasonCode;
  capabilityRevision: number;
  expiresAt: string;
  capacityEstimate: RsyncVersioningEstimateBucket;
  inodeEstimate: RsyncVersioningEstimateBucket;
}

export interface RsyncVersioningActivationResult {
  summary: RsyncPublicationSummary;
  migrationChoice: RsyncVersioningMigrationChoice;
}

export interface RsyncVersioningRollbackPreparationResult {
  summary: RsyncPublicationSummary;
}

export type RclonePublicationMode =
  | "legacy_mutable"
  | "versioned_prefix"
  | "native_object_versions";

export type RcloneVersionedPublicationMode = Exclude<RclonePublicationMode, "legacy_mutable">;

export type RclonePublicationState =
  | "legacy"
  | "preflight_required"
  | "credential_setup_required"
  | "capability_settling"
  | "ready"
  | "preparing"
  | "verifying"
  | "committed"
  | "degraded"
  | "at_risk"
  | "failed"
  | "blocked"
  | "rollback_prepared";

export type RclonePublicationReasonCode =
  | "legacy"
  | "preflight_required"
  | "ready"
  | "credential_setup_required"
  | "capability_settling"
  | "preflight_expired"
  | "task_revision_changed"
  | "binding_revision_changed"
  | "preflight_mismatch"
  | "feature_disabled"
  | "unsupported_profile"
  | "repository_offline"
  | "provider_unavailable"
  | "provider_timeout"
  | "provider_resource_limit"
  | "session_too_short"
  | "versioning_disabled"
  | "lifecycle_conflict"
  | "encryption_unsupported"
  | "kms_key_unavailable"
  | "kms_permission_denied"
  | "kms_key_ring_limit"
  | "identity_mismatch"
  | "credential_invalid"
  | "verification_cost_limit"
  | "source_drift"
  | "external_writer_detected"
  | "unexpected_version"
  | "manifest_mismatch"
  | "marker_mismatch"
  | "admission_blocked"
  | "outcome_unknown"
  | "rollback_prepared";

export type RcloneConsistencyClass = "not_evaluated" | "observationally_stable" | "provider_strong";
export type RcloneHashFidelity = "not_evaluated" | "provider_strong_checksum" | "download_verified_bytes";
export type RcloneCostClass = "not_evaluated" | "none" | "low" | "moderate" | "high";
export type RcloneEncryptionProfile = "none" | "sse_s3" | "sse_kms_cmk";
export type RcloneKmsKeyStatus = "not_applicable" | "ready" | "degraded" | "at_risk" | "blocked";
export type RcloneRollbackCapability = "clean_available" | "preparation_only" | "prepared";
export type RcloneVersioningMigrationChoice = "imported_baseline" | "first_new_point";

export interface RclonePublicationSummary {
  mode: RclonePublicationMode;
  state: RclonePublicationState;
  reasonCode: RclonePublicationReasonCode;
  taskRevision: string;
  bindingRevision: string;
  capabilityRevision: string;
  consistencyClass: RcloneConsistencyClass;
  hashFidelity: RcloneHashFidelity;
  estimatedReadBytes: string;
  apiCostClass: RcloneCostClass;
  storageCostClass: RcloneCostClass;
  egressCostClass: RcloneCostClass;
  credentialExpiresAt?: string;
  encryptionProfile: RcloneEncryptionProfile;
  kmsKeyStatus: RcloneKmsKeyStatus;
  kmsReadKeyCount: number;
  rollbackLocatorPresent: boolean;
  rollbackCapability: RcloneRollbackCapability;
}

export interface RcloneBindingSetupResult {
  setupId: string;
  expiresAt: string;
  externalId?: string;
}

export type RcloneNativeBootstrapInput =
  | { mode: "workload_chain" }
  | { mode: "static_sts_bootstrap"; accessKeyId: string; secretAccessKey: string };

export interface RclonePortableBindingInput {
  expectedTaskRevision: string;
  expectedBindingRevision: string;
  setupId: string;
  targetRemote: string;
  managedRootLocator: string;
  boundConfig: string;
}

export interface RcloneNativeBindingInput {
  expectedTaskRevision: string;
  expectedBindingRevision: string;
  setupId: string;
  region: string;
  bucket: string;
  managedPrefix: string;
  roleArn: string;
  bootstrap: RcloneNativeBootstrapInput;
  encryptionProfile: Exclude<RcloneEncryptionProfile, "none">;
  kmsKeyArn?: string;
}

export interface RcloneVersioningPreflightResult {
  preflightId: string;
  expiresAt: string;
  summary: RclonePublicationSummary;
}

export interface RcloneVersioningActivationResult {
  summary: RclonePublicationSummary;
  migrationChoice: RcloneVersioningMigrationChoice;
}

export interface RcloneVersioningRollbackResult {
  summary: RclonePublicationSummary;
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
  rsyncPublication?: RsyncPublicationSummary;
  rclonePublication?: RclonePublicationSummary;
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

export type CredentialAccessGrantAction = "terminal.open" | "config.import" | "config.export" | "snapshot.restore" | "task.restore_trigger" | "task.manual_trigger" | "task.batch_trigger" | "batch_command.create" | "unknown";
export type CredentialAccessGrantPurpose = "terminal" | "config_import" | "config_export" | "snapshot" | "task_restore" | "task_command" | "batch_command" | "unknown";
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

/** Domain shape for app credentials (API maps wire snake_case → camelCase). */
export interface AppCredential {
  id: number;
  name: string;
  type: string;
  description: string;
  config: Record<string, string>;
  hasPassword: boolean;
  referenceCount: number;
  createdAt: string;
  updatedAt: string;
}

/** Create/update form input for app credentials (camelCase; API maps to wire). */
export interface AppCredentialInput {
  type: string;
  name: string;
  description?: string;
  host?: string;
  port?: string;
  user?: string;
  password?: string;
  containerName?: string;
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
  credentialType: string;
  isDocker: boolean;
  configSchema: ConfigField[];
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

/** Domain silence after API mapping. Wire match_tags (string|array|null) is
 *  normalized to string[] in mapSilence. */
export interface Silence {
  id: number;
  name: string;
  matchNodeId: number | null;
  matchCategory: string;
  matchTags: string[];
  startsAt: string;
  endsAt: string;
  createdBy: number;
  note: string;
  createdAt: string;
  updatedAt: string;
}

/** Create silence form input (camelCase; API maps to wire snake_case). */
export interface SilenceInput {
  name: string;
  matchNodeId: number | null;
  matchCategory: string;
  matchTags: string[];
  startsAt: string;
  endsAt: string;
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

export type HttpMethod = "GET" | "POST" | "HEAD";

/** Backend wire shape for ServiceMonitorView. Never consumed by components — mapped at the API boundary (lib/api/service-monitors.ts). */
export interface RawServiceMonitor {
  id: number;
  name: string;
  description: string;
  type: string;
  target: string;
  interval_seconds: number;
  timeout_seconds: number;
  http_method: string;
  http_expected_status: number;
  http_headers: string;
  enabled: boolean;
  last_status: string;
  uptime_pct: number;
  last_checked_at: string | null;
  created_at: string;
  updated_at: string;
}

/** Backend wire shape for NewServiceMonitorInput (outbound). */
export interface RawNewServiceMonitorInput {
  name: string;
  description?: string;
  type: "http" | "tcp";
  target: string;
  interval_seconds?: number;
  timeout_seconds?: number;
  http_method?: string;
  http_expected_status?: number;
  http_headers?: string;
  enabled?: boolean;
}

/** Backend wire shape for StatusPageItem. */
export interface RawStatusPageItem {
  name: string;
  type: string;
  status: string;
  uptime_pct: number;
  last_checked_at: string | null;
}

export interface ServiceMonitorView {
  id: number;
  /** Stable monitor identifier. */
  name: string;
  description: string;
  type: "http" | "tcp";
  target: string;
  intervalSeconds: number;
  timeoutSeconds: number;
  httpMethod: HttpMethod;
  httpExpectedStatus: number;
  enabled: boolean;
  httpHeaderList: HeaderKV[]; // parsed form; raw JSON string lives only in API boundary
  lastStatus: "up" | "down" | "unknown";
  uptimePct: number;
  lastCheckedAt: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface NewServiceMonitorInput {
  name: string;
  description?: string;
  type: "http" | "tcp";
  target: string;
  intervalSeconds?: number;
  timeoutSeconds?: number;
  httpMethod?: HttpMethod;
  httpExpectedStatus?: number;
  httpHeaderList?: HeaderKV[];
  enabled?: boolean;
}

/** Key/value pair used for HTTP monitor headers (UI editing + API boundary transport). */
export interface HeaderKV {
  key: string;
  value: string;
}

export interface StatusPageItem {
  name: string;
  type: string;
  status: "up" | "down" | "unknown";
  uptimePct: number;
  lastCheckedAt: string | null;
}

// Backup asset Catalog boundary. Raw snake_case DTOs remain private to
// web/src/lib/api; consumers receive only these closed, camelCase projections.
export type CatalogCapabilityCode =
  | "feature_disabled"
  | "task_artifact_contract_missing"
  | "repository_offline"
  | "repository_disconnected"
  | "provider_unavailable"
  | "repository_identity_unavailable"
  | "provider_protocol_incompatible"
  | "provider_operation_timeout"
  | "provider_resource_limit"
  | "point_not_committed"
  | "mutable_source_changed"
  | "catalog_unavailable"
  | "sequential_read_unavailable"
  | "range_unavailable"
  | "download_unavailable"
  | "restore_unavailable"
  | "diff_unavailable"
  | "unknown_internal_state";

export interface CatalogCapabilityReason {
  code: CatalogCapabilityCode;
  params: Record<string, string>;
}

export type CatalogProjection<T> =
  | { status: "available"; value: T }
  | { status: "blocked"; reason: CatalogCapabilityReason };

export type BackupProviderKind = "restic" | "rsync" | "rclone" | "command" | "verified_import";
export type BackupVersionMode =
  | "native_snapshot"
  | "hardlink_tree"
  | "full_copy_tree"
  | "versioned_prefix"
  | "native_object_versions"
  | "mutable_head";
export type BackupPublicationMode =
  | "legacy_mutable"
  | "versioned_hardlink"
  | "versioned_full_copy"
  | "versioned_prefix"
  | "native_object_versions"
  | "native_snapshot";
export type BackupRepositoryStatus =
  | "connecting"
  | "online"
  | "degraded"
  | "offline"
  | "disconnected"
  | "purging"
  | "purge_blocked";
export type BackupImmutabilityLevel = "mutable" | "xirang_managed" | "backend_versioned" | "storage_worm";
export type RecoveryPointSemantics = "native_snapshot" | "xirang_manifest" | "imported_baseline" | "mutable_head";
export type RecoveryPointState =
  | "observed"
  | "retired"
  | "preparing"
  | "verifying"
  | "committed"
  | "degraded"
  | "expiring"
  | "expired"
  | "failed"
  | "purge_blocked";
export type RecoveryPointPhysicalAvailability = "online" | "offline" | "missing" | "unknown";
export type RecoveryPointHoldState = "none" | "active" | "released";

export interface CatalogCapabilitySet {
  list: boolean;
  searchPath: boolean;
  openSequential: boolean;
  openRange: boolean;
  download: boolean;
  restore: boolean;
  diff: boolean;
  nativeHistory: boolean;
  reason: CatalogCapabilityReason | null;
}

export type CatalogGenerationState = "building" | "complete" | "partial" | "failed" | "superseded";
export type CatalogGenerationErrorCode =
  | ""
  | "catalog_build_abandoned"
  | "catalog_build_failed"
  | "catalog_build_incomplete"
  | "catalog_build_limit"
  | "catalog_build_timeout"
  | "catalog_identity_key_unavailable"
  | "catalog_invalid_record"
  | "catalog_projection_mismatch"
  | "catalog_proof_mismatch"
  | "catalog_provider_resource_limit"
  | "catalog_provider_timeout"
  | "catalog_provider_unavailable"
  | "catalog_source_changed";
export type CatalogCoverageStatus = "building" | "complete" | "partial" | "failed" | "unavailable";
export type CatalogStalenessStatus = "fresh" | "stale" | "unknown";

export interface CatalogGeneration {
  id: string;
  sequence: number;
  state: CatalogGenerationState;
  startedAt: string;
  finishedAt: string | null;
  errorCode: CatalogGenerationErrorCode;
  correlationId: string;
}

export interface CatalogCoverage {
  status: CatalogCoverageStatus;
  indexedEntries: number;
  expectedEntries: number | null;
  manifestDigest: string;
  observedAt: string;
}

export interface CatalogStaleness {
  status: CatalogStalenessStatus;
  observedAt: string | null;
  reason: CatalogCapabilityReason | null;
}

export interface CatalogContentAvailability {
  available: boolean;
  reason: CatalogCapabilityReason | null;
}

export interface CatalogPermissions {
  list: boolean;
  preview: boolean;
  download: boolean;
}

export interface CatalogStatus {
  generation: CatalogGeneration | null;
  latestBuild: CatalogGeneration | null;
  coverage: CatalogCoverage;
  staleness: CatalogStaleness;
  contentAvailability: CatalogContentAvailability;
  permissions: CatalogPermissions;
}

export interface BackupRepositoryCatalogSummary {
  recoveryPointCount: number;
  completeCatalogCount: number;
  coverage: CatalogCoverageStatus;
  contentAvailability: CatalogContentAvailability;
  permissions: CatalogPermissions;
}

export type BackupRepositoryLineageSource = "task_link" | "recovery_point";

export interface BackupRepositoryLineage {
  source: BackupRepositoryLineageSource;
  taskId?: number;
  taskName: string;
  nodeId: number;
  nodeName: string;
  publicationMode?: BackupPublicationMode;
  recoveryPointId?: string;
  recoveryPointState?: RecoveryPointState;
  pointSemantics?: RecoveryPointSemantics;
  active: boolean;
}

export interface BackupRepository {
  id: string;
  providerKind: BackupProviderKind;
  displayName: string;
  description: string;
  versionMode: BackupVersionMode;
  status: BackupRepositoryStatus;
  capabilityRevision: number;
  capabilities: CatalogCapabilitySet;
  immutabilityLevel: BackupImmutabilityLevel;
  lastSeenAt: string | null;
  lastReconciledAt: string | null;
  createdAt: string;
  updatedAt: string;
  accessActive: boolean;
  lineages: BackupRepositoryLineage[];
  catalog: BackupRepositoryCatalogSummary;
}

export interface BackupRepositoryPage {
  items: Array<CatalogProjection<BackupRepository>>;
  nextCursor: string | null;
}

export interface RecoveryPointLineage {
  producingTaskId?: number;
  producingTaskRunId?: number;
  sourceRecoveryPointId?: string;
}

export interface BackupRecoveryPoint {
  id: string;
  repositoryId: string;
  lineage: RecoveryPointLineage;
  semantics: RecoveryPointSemantics;
  state: RecoveryPointState;
  physicalAvailability: RecoveryPointPhysicalAvailability;
  holdState: RecoveryPointHoldState;
  immutabilityLevel: BackupImmutabilityLevel;
  manifestDigest: string;
  entryCount: number;
  logicalBytes: number;
  capturedAt: string | null;
  committedAt: string | null;
  observedAt: string | null;
  capabilityRevision: number;
  capabilities: CatalogCapabilitySet;
  createdAt: string;
  updatedAt: string;
  producingTaskName: string;
  producingNodeId: number;
  producingNodeName: string;
  catalog: CatalogProjection<CatalogStatus>;
}

export interface RecoveryPointPage {
  items: Array<CatalogProjection<BackupRecoveryPoint>>;
  nextCursor: string | null;
}

export type EvidenceLayerStatus = "recorded" | "unavailable" | "not_recorded" | "invalid";
export type ManifestCompleteness = "complete" | "partial" | "unavailable";
export type ProviderCompletionClass = "known_exit_zero" | "known_nonzero" | "outcome_unknown";
export type CatalogPublicationFailureCode =
  | "publication_precondition_missing"
  | "publication_in_progress"
  | "publication_session_abandoned"
  | "evidence_missing_summary"
  | "evidence_malformed_stream"
  | "evidence_duplicate_summary"
  | "evidence_non_final_summary"
  | "evidence_invalid_native_id"
  | "provider_nonzero_exit"
  | "provider_timeout"
  | "provider_canceled"
  | "provider_resource_limit"
  | "provider_outcome_unknown"
  | "provider_completion_unproven"
  | "provider_snapshot_rewritten"
  | "repository_identity_drift"
  | "run_tag_missing"
  | "ambiguous_run_tags"
  | "native_point_conflict"
  | "manifest_partial"
  | "manifest_unavailable"
  | "lease_fence_lost"
  | "publication_deadline_exceeded"
  | "snapshot_missing_at_deadline"
  | "legacy_fallback_blocked"
  | "legacy_operation_blocked"
  | "source_drift"
  | "external_writer_detected"
  | "unexpected_version"
  | "marker_mismatch"
  | "manifest_mismatch";

export interface RecoveryPointLineageEvidence {
  status: EvidenceLayerStatus;
  taskId?: number;
  taskRunId?: number;
  taskName: string;
  nodeId: number;
  nodeName: string;
  trigger: TaskRunTriggerType | null;
  runStatus: TaskStatus | null;
  startedAt: string | null;
  finishedAt: string | null;
}

export interface RecoveryPointManifestEvidence {
  status: EvidenceLayerStatus;
  id: string;
  revision: number;
  digestAlgorithm: string;
  digest: string;
  entryCount: number;
  logicalBytes: number;
  generator: string;
  generatorVersion: string;
  completeness: ManifestCompleteness | null;
  createdAt: string | null;
  updatedAt: string | null;
}

export interface RecoveryPointPublicationEvidence {
  status: EvidenceLayerStatus;
  provider: BackupProviderKind | null;
  completion: ProviderCompletionClass | null;
  failureCode: CatalogPublicationFailureCode | null;
  captureStartedAt: string | null;
  captureFinishedAt: string | null;
  filesProcessed: number;
  logicalBytes: number;
  commitRecorded: boolean;
}

export interface RecoveryPointRestoreDrillSummary {
  taskRunId: number;
  status: RestoreDrillStatus;
  failedStep: string;
  confidenceEligible: boolean;
  startedAt: string | null;
  finishedAt: string | null;
  durationMs: number;
}

export interface RecoveryPointRestoreDrillEvidence {
  status: EvidenceLayerStatus;
  items: RecoveryPointRestoreDrillSummary[];
}

export interface RecoveryPointEvidence {
  recoveryPointId: string;
  lineage: RecoveryPointLineageEvidence;
  manifest: RecoveryPointManifestEvidence;
  publicationVerification: RecoveryPointPublicationEvidence;
  restoreDrills: RecoveryPointRestoreDrillEvidence;
}

export type CatalogEntryType = "file" | "directory" | "symlink" | "hardlink" | "special" | "unknown";
export type CatalogFingerprintStrength = "strong" | "weak" | "none";

export interface AssetRef {
  recoveryPointId: string;
  entryId: string;
}

export interface BackupAssetBreadcrumb {
  ref: AssetRef;
  name: string;
}

export interface BackupAsset {
  ref: AssetRef;
  parentRef: AssetRef | null;
  name: string;
  entryType: CatalogEntryType;
  size: number;
  modifiedAt: string | null;
  mode: string;
  owner: string;
  mimeType: string;
  fingerprintStrength: CatalogFingerprintStrength;
  breadcrumb: BackupAssetBreadcrumb[];
}

export interface BackupAssetPage {
  items: Array<CatalogProjection<BackupAsset>>;
  nextCursor: string | null;
}

export type BackupContentAction = "preview" | "download";
export type BackupContentRenderer =
  | "escaped_text"
  | "safe_raster"
  | "same_origin_pdf"
  | "native_audio"
  | "native_video"
  | "metadata_hex"
  | "attachment";
export type BackupContentProfile =
  | "text_v1"
  | "raster_v1"
  | "pdf_v1"
  | "audio_v1"
  | "video_v1"
  | "hex_v1"
  | "original_v1";
export type BackupContentRangePolicy = "none" | "single";
export type BackupContentClassification = "non_secret" | "secret" | "unknown";

export interface BackupContentTicket {
  schemaVersion: 1;
  contentUrl: string;
  action: BackupContentAction;
  renderer: BackupContentRenderer;
  profile: BackupContentProfile;
  contentType: string;
  contentLength: number;
  etag: string;
  lastModified: string | null;
  range: BackupContentRangePolicy;
  classification: BackupContentClassification;
  expiresAt: string;
  idleExpiresAt: string;
  capabilityReason: CatalogCapabilityReason | null;
  fallbackActions: BackupContentAction[];
}

export type BackupExportArchiveFormat = "zip" | "tar";
export type BackupExportArchiveProfile = "zip_deflate_v1" | "tar_none_v1" | "tar_gzip_v1";
export type BackupExportExecutionState =
  | "queued"
  | "running"
  | "retry_wait"
  | "sealing"
  | "ready"
  | "cancel_requested"
  | "failed"
  | "source_expired"
  | "canceled"
  | "expiring"
  | "expired";
export type BackupExportCleanupState = "none" | "revoking" | "purging" | "purged" | "purge_failed";
export type BackupExportItemState = "pending" | "read" | "packed" | "skipped" | "failed";
export type BackupExportResultKind = "complete" | "partial";
export type BackupExportAttemptState = "active" | "sealing" | "sealed" | "failed" | "canceled" | "superseded";
export type BackupExportErrorCategory =
  | "source_changed"
  | "source_expired"
  | "link_metadata_unavailable"
  | "special_file_skipped"
  | "artifact_missing"
  | "artifact_tampered"
  | "key_unavailable"
  | "quota_exceeded"
  | "deadline"
  | "canceled"
  | "internal_failure"
  | "worker_unavailable"
  | "provider_unavailable";

export interface BackupExportItemStatus {
  id: string;
  ordinal: number;
  state: BackupExportItemState;
  logicalBytes: number;
  providerBytes: number;
  errorCategory: BackupExportErrorCategory | null;
}

export interface BackupExportAttemptProgress {
  attemptNumber: number;
  state: BackupExportAttemptState;
  itemCount: number;
  logicalBytes: number;
  providerBytes: number;
  leaseExpiresAt: string;
}

export interface BackupExportJob {
  schemaVersion: 1;
  id: string;
  selectionDigest: string;
  archiveFormat: BackupExportArchiveFormat;
  archiveProfile: BackupExportArchiveProfile;
  executionState: BackupExportExecutionState;
  resultKind: BackupExportResultKind | null;
  cleanupState: BackupExportCleanupState;
  itemCount: number;
  packedCount: number;
  skippedCount: number;
  failedCount: number;
  logicalBytes: number;
  providerBytes: number;
  artifactBytes: number;
  errorCategory: BackupExportErrorCategory | null;
  createdAt: string;
  absoluteDeadline: string;
  readyAt: string | null;
  expiresAt: string | null;
  attempt: BackupExportAttemptProgress | null;
  items: BackupExportItemStatus[];
  nextCursor: string | null;
  pollAfterSeconds: number;
  canCancel: boolean;
  canDownload: boolean;
}

export interface BackupExportCreateResult {
  job: BackupExportJob;
  replay: boolean;
}

export interface BackupExportDownloadTicket {
  schemaVersion: 1;
  contentUrl: string;
  contentType: string;
  contentLength: number;
  etag: string;
  range: BackupContentRangePolicy;
  expiresAt: string;
  idleExpiresAt: string;
}

export type BackupArchiveMemberState = "queued" | "running" | "ready" | "failed" | "canceled" | "expired";
export type BackupArchiveFailureProduct = "encrypted" | "unsupported" | "limit" | "unsafe" | "unavailable";
export type BackupArchiveFallbackAction = "download_original";
export type BackupArchiveFallbackReason = "original_download_unavailable";
export type BackupArchiveEntryWarning = "none";

export interface BackupArchiveIndexEntry {
  id: string;
  parentId: string | null;
  displayName: string;
  type: "file";
  size: number;
  mediaType: string;
  warning: BackupArchiveEntryWarning;
}

export interface BackupArchiveIndex {
  schemaVersion: 1;
  indexRevision: string;
  expiresAt: string;
  entries: BackupArchiveIndexEntry[];
}

export interface BackupArchiveMemberCreateResult {
  schemaVersion: 1;
  requestId: string;
  state: "queued";
}

export interface BackupArchiveFallback {
  action: BackupArchiveFallbackAction | null;
  reason: BackupArchiveFallbackReason | null;
}

export interface BackupArchiveMemberStatus {
  schemaVersion: 1;
  requestId: string;
  state: BackupArchiveMemberState;
  failureProduct: BackupArchiveFailureProduct | null;
  fallback: BackupArchiveFallback;
  retryable: boolean;
  terminal: boolean;
}

export type BackupProcessingRepresentation =
  | "thumbnail"
  | "text"
  | "document_pages"
  | "media_preview"
  | "archive_index";
export type BackupProcessingProductState =
  | "native"
  | "derived"
  | "partial"
  | "unsupported"
  | "not_deployed"
  | "queued"
  | "failed";
export type BackupProcessingFallbackAction = "native_preview" | "download";
export type BackupProcessingFreshness = "current" | "stale";
export type BackupProcessingCoverage = "complete" | "partial";
export type BackupProcessingScanStatus = "not_scanned" | "no_finding" | "finding" | "stale";
export type BackupProcessingSensitivityStatus = "non_secret" | "secret" | "unknown" | "stale";

export interface BackupProcessingProduct {
  schemaVersion: 1;
  jobId: string | null;
  state: BackupProcessingProductState;
  representation: BackupProcessingRepresentation;
  capability: string | null;
  profile: string | null;
  coverage: BackupProcessingCoverage | null;
  freshness: BackupProcessingFreshness | null;
  scanStatus: BackupProcessingScanStatus | null;
  sensitivityStatus: BackupProcessingSensitivityStatus | null;
  reason: string | null;
  retryable: boolean;
  fallbackActions: BackupProcessingFallbackAction[];
  pollAfterSeconds: number;
  terminal: boolean;
}

export interface BackupAssetProcessingState {
  schemaVersion: 1;
  representations: BackupProcessingProduct[];
}

export interface BackupProcessingCoverageBucket {
  capability: string;
  profile: string;
  eligible: number;
  completed: number;
  partial: number;
  queued: number;
  failed: number;
  unsupported: number;
  notDeployed: number;
  stale: number;
}

export interface BackupProcessingCoverageSummary extends Omit<BackupProcessingCoverageBucket, "capability" | "profile"> {
  schemaVersion: 1;
  generatedAt: string;
  backlogAgeBucket: string;
  estimatedSeconds: number | null;
  byCapability: BackupProcessingCoverageBucket[];
}

export interface BackupProcessingUpdaterCapabilityChange {
  capability: string;
  capabilitySchema: string;
  profiles: string[];
}

export type BackupProcessingUpdaterCandidateState =
  | "registered"
  | "verified"
  | "active"
  | "superseded"
  | "failed";

export interface BackupProcessingUpdaterCandidate {
  candidateId: string;
  sourceKind: "builtin" | "admin_registered";
  sourceId: string;
  version: string;
  manifestDigest: string;
  signingKeyFingerprint: string;
  bundleFingerprint: string;
  state: BackupProcessingUpdaterCandidateState;
  reason: string | null;
  verifiedAt: string | null;
  activatedAt: string | null;
  capabilityChanges: BackupProcessingUpdaterCapabilityChange[];
}

export interface BackupProcessingUpdaterStatus {
  schemaVersion: 1;
  enabled: boolean;
  onlineEnabled: boolean;
  active: BackupProcessingUpdaterCandidate | null;
}

export interface BackupProcessingBackfillPolicy {
  schemaVersion: 1;
  revision: string;
  paused: boolean;
  batchSize: number;
  jobsPerHour: number;
  bytesPerHour: number;
  providerConcurrency: number;
  capabilityConcurrency: number;
}

export interface BackupProcessingAdminControl {
  schemaVersion: 1;
  configured: boolean;
  localEnabled: boolean;
  remoteEnabled: boolean;
  backfillPolicy: BackupProcessingBackfillPolicy;
}

export type AssetSearchField = "any" | "name" | "path" | "extension" | "tag" | "content" | "ocr";
export type AssetSearchHitField = Exclude<AssetSearchField, "any"> | "type" | "modified_time";
export type AssetSearchSort = "relevance" | "name_asc" | "modified_desc";
export type AssetSearchScopeMode = "current" | "all_retained" | "exact_points";
export type AssetSearchCoverageStatus = "complete" | "partial" | "building" | "failed" | "unavailable";
export type AssetSearchStalenessStatus = "fresh" | "stale" | "unknown";
export type AssetSearchTotalRelation = "exact" | "lower_bound" | "unavailable";

export type AssetSearchQueryNode =
  | { op: "and" | "or"; children: AssetSearchQueryNode[] }
  | { op: "not"; children: [AssetSearchQueryNode] }
  | { op: "term"; field: AssetSearchField; text: string }
  | { op: "type"; values: CatalogEntryType[] }
  | { op: "modified_time"; from: string | null; to: string | null };

export interface AssetSearchScope {
  mode: AssetSearchScopeMode;
  repositoryIds: string[];
  taskIds: number[];
  recoveryPointIds: string[];
}

export interface AssetSearchRequest {
  schemaVersion: 1;
  root: AssetSearchQueryNode;
  scope: AssetSearchScope;
  sort: AssetSearchSort;
  limit: number;
  cursor: string | null;
}

export interface AssetSearchIndexStatus {
  recoveryPointId: string;
  catalogGenerationId: string | null;
  searchGenerationId: string | null;
  projectionRevision: number;
  coverage: AssetSearchCoverageStatus;
  staleness: AssetSearchStalenessStatus;
}

export interface AssetSearchSnippet {
  field: "content" | "ocr";
  text: string;
}

export interface AssetSearchHit {
  ref: AssetRef;
  asset: BackupAsset;
  hitFields: AssetSearchHitField[];
  score: number;
  snippet: AssetSearchSnippet | null;
}

export interface AssetSearchSuggestion {
  field: AssetSearchHitField;
  value: string;
}

export interface AssetSearchResponse {
  queryGeneration: string;
  indexes: AssetSearchIndexStatus[];
  items: AssetSearchHit[];
  nextCursor: string | null;
  total: number | null;
  totalRelation: AssetSearchTotalRelation;
  authoritativeEmpty: boolean;
  coverage: { status: AssetSearchCoverageStatus };
  suggestions: AssetSearchSuggestion[];
  capabilities: { metadata: boolean; content: boolean };
  permissions: { list: boolean; secretReveal: boolean };
}

export type SavedSearchState = "active" | "broken" | "blocked";
export type SavedSearchReason =
  | "point_retired"
  | "point_expiring"
  | "point_expired"
  | "point_failed"
  | "point_purge_blocked"
  | "point_missing"
  | "scope_unauthorized"
  | "ast_schema_unsupported";

export interface SavedAssetSearch {
  id: string;
  query: AssetSearchRequest;
  version: number;
  state: SavedSearchState;
  stateReason: SavedSearchReason | null;
  brokenAt: string | null;
  createdAt: string;
  updatedAt: string;
}

export type AssetOverlayState = "active" | "tombstone";
export type AssetOverlayTombstoneReason =
  | "source_retired"
  | "source_expiring"
  | "source_expired"
  | "source_failed"
  | "source_purge_blocked"
  | "source_missing";

export interface BackupAssetFavorite {
  id: string;
  ref: AssetRef;
  label: string;
  state: AssetOverlayState;
  tombstoneReason: AssetOverlayTombstoneReason | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface BackupAssetTag {
  id: string;
  name: string;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface BackupAssetTagAssignment {
  id: string;
  tagId: string;
  ref: AssetRef;
  state: AssetOverlayState;
  tombstoneReason: AssetOverlayTombstoneReason | null;
  version: number;
}

export interface BackupAssetRecentAccess {
  id: string;
  ref: AssetRef;
  accessCount: number;
  lastAccessedAt: string;
  expiresAt: string;
  version: number;
}

export interface BackupAssetOverlayPage<T> {
  items: T[];
  nextCursor: string | null;
}

export type RecoveryPointDiffKind = "added" | "removed" | "modified" | "type_changed";
export type RecoveryPointDiffContentEquality = "equal" | "different" | "unknown";
export type RecoveryPointDiffProviderStatus = "supported" | "unavailable" | "not_applicable";
export type RecoveryPointDiffChangedField =
  | "name"
  | "entry_type"
  | "size"
  | "modified_at"
  | "mode"
  | "owner"
  | "mime_type"
  | "fingerprint_strength"
  | "content";

export interface RecoveryPointDiffSide {
  ref: AssetRef;
  name: string;
  entryType: CatalogEntryType;
  size: number;
  modifiedAt: string | null;
  mode: string;
  owner: string;
  mimeType: string;
  fingerprintStrength: CatalogFingerprintStrength;
}

export interface RecoveryPointDiffItem {
  kind: RecoveryPointDiffKind;
  base: RecoveryPointDiffSide | null;
  compare: RecoveryPointDiffSide | null;
  changedFields: RecoveryPointDiffChangedField[];
  contentEquality: RecoveryPointDiffContentEquality;
}

export interface RecoveryPointDiffProviderEvidence {
  status: RecoveryPointDiffProviderStatus;
  reason: CatalogCapabilityReason | null;
}

export interface RecoveryPointDiff {
  items: RecoveryPointDiffItem[];
  nextCursor: string | null;
  providerEvidence: RecoveryPointDiffProviderEvidence;
}
