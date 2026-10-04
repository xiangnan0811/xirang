/**
 * Wire payloads for the walkthrough mock.
 * Shapes follow the frontend mappers, not the older nested pagination fixtures.
 */

export const HTTP_ERROR_TEXT = "WALKTHROUGH_HTTP_500";
export const INCIDENT_CAUSE = "WALKTHROUGH_INCIDENT_CAUSE";
export const ANOMALY_METRIC = "WALKTHROUGH_ANOMALY_METRIC";
export const SILENCE_NAME = "WALKTHROUGH_SILENCE";
export const CREDENTIAL_AUDIT_PURPOSE = "WALKTHROUGH_CREDENTIAL_AUDIT";
export const FAILURE_SUMMARY_COUNT = 86421;
export const SELF_BACKUP_FILENAME = "walkthrough-self-backup.tar.gz";
export const LONG_NODE_MARKER = "超长命名字段测试";
export const LONG_TASK_MARKER = "名称超长文本折行测试";

export const LOGIN_CAPTCHA = {
  id: "walkthrough-captcha",
  question: "3 + 4 = ?",
  answer: "7",
} as const;

export const FILE_SOURCE_NODE_ID = 17;
export const FILE_SOURCE_SET_ID = "12121212121212121212121212121212";
export const ONLINE_REPOSITORY_ID = "11111111111111111111111111111111";
export const ONLINE_RECOVERY_POINT_ID = "33333333333333333333333333333333";

const AT = "2026-10-04T06:00:00Z";

export const standardNodes = [
  {
    id: 1,
    name: "北京生产主库-01",
    host: "10.30.1.7",
    port: 22,
    username: "root",
    auth_type: "key",
    ssh_key_id: 1,
    tags: "core,db,prod,critical",
    status: "online",
    base_path: "/data",
    last_seen_at: AT,
    last_backup_at: "2026-10-04T05:30:00Z",
    connection_latency_ms: 12,
  },
  {
    id: 2,
    name: "上海容灾热备-01",
    host: "10.30.2.8",
    port: 22,
    username: "root",
    auth_type: "key",
    ssh_key_id: 1,
    tags: "edge,db,staging",
    status: "warning",
    base_path: "/backup",
    last_seen_at: "2026-10-04T05:40:00Z",
    last_backup_at: "2026-10-04T04:10:00Z",
    connection_latency_ms: 48,
  },
  {
    id: 3,
    name: "广州归档存储-01",
    host: "10.30.3.9",
    port: 22,
    username: "ubuntu",
    auth_type: "password",
    tags: "archive",
    status: "offline",
    base_path: "/archive",
    last_seen_at: null,
    last_backup_at: null,
    connection_latency_ms: null,
  },
];

export const extremeLongNodes = [
  {
    id: 1,
    name: `北京核心高可用多活混合云集群异构存储容灾节点-${LONG_NODE_MARKER}-ABCDEFGHIJK-1234567890-XYZ-01`,
    host: "very-long-internal-subdomain-hostname-for-stress-testing-purposes.corp.internal.node-cluster.local",
    port: 22,
    username: "extremely_long_service_account_user_name_for_enterprise_ldap_integration",
    auth_type: "key",
    ssh_key_id: 1,
    tags: "super-critical-core-infrastructure-tag-1,extremely-long-secondary-domain-identifier-tag-2,third-tier-deeply-nested-category-prod-tag-3",
    status: "online",
    base_path: "/var/data/very/deeply/nested/directory/path/structure/that/never/ends/without/breaking/boundaries",
    last_seen_at: AT,
    last_backup_at: "2026-10-04T05:30:00Z",
    connection_latency_ms: 12,
  },
];

export const standardSSHKeys = [
  {
    id: 1,
    name: "ops-prod-rsa",
    username: "root",
    key_type: "rsa",
    public_key: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC8OpsProdKey123 root@prod",
    fingerprint: "SHA256:xi-rang-0101-7291a",
    disabled: false,
    created_at: "2026-09-01T00:00:00Z",
    last_used_at: "2026-10-04T05:30:00Z",
    allowed_purposes: "terminal,task_command,task_backup",
    allowed_node_tags: "prod,critical",
    broad_scope: true,
  },
  {
    id: 2,
    name: "ops-staging-ed25519",
    username: "ubuntu",
    key_type: "ed25519",
    public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHStagingKey456 ubuntu@staging",
    fingerprint: "SHA256:xi-rang-0102-1829f",
    disabled: false,
    created_at: "2026-09-15T00:00:00Z",
    last_used_at: "2026-10-03T12:00:00Z",
    allowed_purposes: "terminal,node_test,probe",
    allowed_node_tags: "staging",
    broad_scope: true,
  },
];

export const standardPolicies = [
  {
    id: 1,
    name: "核心数据库全量备份策略",
    source_path: "/var/lib/mysql",
    target_path: "/backup/mysql",
    cron_spec: "0 2 * * *",
    enabled: true,
    node_ids: [1, 2],
    verify_enabled: true,
    retention_days: 30,
    rpo_minutes: 60,
    rto_minutes: 120,
    drill_enabled: true,
    drill_cron: "0 4 * * 0",
    escalation_policy_id: 1,
    latest_drill: {
      task_run_id: 101,
      status: "success",
      duration_ms: 15420,
    },
  },
  {
    id: 2,
    name: "应用日志增量归档策略",
    source_path: "/var/log/app",
    target_path: "/backup/logs",
    cron_spec: "*/30 * * * *",
    enabled: true,
    node_ids: [1, 3],
    retention_days: 7,
  },
];

export const standardTasks = [
  {
    id: 1,
    name: "MySQL 日常全备",
    status: "success",
    rsync_source: "/var/lib/mysql",
    rsync_target: "/backup/mysql",
    executor_type: "rsync",
    cron_spec: "0 2 * * *",
    policy_id: 1,
    node_id: 1,
    node: { id: 1, name: "北京生产主库-01" },
    policy: { id: 1, name: "核心数据库全量备份策略" },
    retry_count: 0,
    enabled: true,
    created_at: "2026-09-01T00:00:00Z",
    updated_at: AT,
    last_run_at: "2026-10-04T05:30:00Z",
    next_run_at: "2026-10-05T02:00:00Z",
    source: "policy",
    verify_status: "passed",
  },
  {
    id: 2,
    name: "上海从库同步",
    status: "running",
    rsync_source: "/data/sh",
    rsync_target: "/backup/sh",
    executor_type: "rsync",
    cron_spec: "0 3 * * *",
    policy_id: 1,
    node_id: 2,
    node: { id: 2, name: "上海容灾热备-01" },
    policy: { id: 1, name: "核心数据库全量备份策略" },
    retry_count: 0,
    enabled: true,
    created_at: "2026-09-02T00:00:00Z",
    source: "policy",
  },
  {
    id: 3,
    name: "广州归档同步",
    status: "failed",
    command: "sync-archive",
    executor_type: "command",
    cron_spec: "0 4 * * *",
    policy_id: 2,
    node_id: 3,
    node: { id: 3, name: "广州归档存储-01" },
    policy: { id: 2, name: "应用日志增量归档策略" },
    last_error: "SSH connection timeout: dial tcp 10.30.3.9:22: i/o timeout",
    retry_count: 3,
    enabled: true,
    created_at: "2026-09-03T00:00:00Z",
    source: "manual",
  },
];

export const extremeLongTasks = [
  {
    id: 1,
    name: `深度分布式微服务数据库全量镜像与异地增量混合容灾多阶段执行长任务-${LONG_TASK_MARKER}`,
    status: "failed",
    rsync_source: "/mnt/vol1/storage/data/production/services/database/cluster/master/shards/shard_001/datafiles",
    rsync_target: "/mnt/remote_backup_nas_cluster/archive/volume_pool/snapshots/daily/2026/10/04/complete_mirror",
    executor_type: "rsync",
    cron_spec: "0 2 * * *",
    policy_id: 1,
    node_id: 1,
    node: { id: 1, name: "北京生产主库-01" },
    policy: { id: 1, name: "核心数据库全量备份策略" },
    last_error: "Fatal exception in remote sync daemon while transferring the nightly mirror",
    retry_count: 5,
    enabled: true,
    created_at: "2026-09-01T00:00:00Z",
    source: "policy",
  },
];

export const standardAlerts = [
  {
    id: 1,
    node_id: 1,
    node_name: "北京生产主库-01",
    task_id: 1,
    policy_name: "核心数据库全量备份策略",
    severity: "critical",
    status: "open",
    error_code: "ERR_SSH_TIMEOUT",
    message: "节点连通性超时，备份任务执行失败",
    retryable: true,
    triggered_at: "2026-10-04T05:00:00Z",
  },
  {
    id: 2,
    node_id: 2,
    node_name: "上海容灾热备-01",
    severity: "warning",
    status: "open",
    error_code: "WARN_DISK_SPACE",
    message: "备份磁盘空间使用率达到 85%",
    retryable: false,
    triggered_at: "2026-10-04T04:30:00Z",
  },
];

export const standardIntegrations = [
  {
    id: 1,
    type: "webhook",
    name: "运维监控告警 Webhook",
    endpoint: "https://alerts.example.com/webhook",
    has_secret: true,
    enabled: true,
    fail_threshold: 3,
    cooldown_minutes: 15,
  },
  {
    id: 2,
    type: "dingtalk",
    name: "钉钉生产值班群机器人",
    endpoint: "https://oapi.dingtalk.com/robot/send",
    has_secret: true,
    enabled: true,
    fail_threshold: 5,
    cooldown_minutes: 30,
  },
];

export const standardUsers = [
  { id: 1, username: "admin", role: "admin", totp_enabled: true },
  { id: 2, username: "operator", role: "operator", totp_enabled: false },
];

export const automationRules = [
  {
    id: 1,
    name: "自动暂停故障策略",
    description: "连续失败后暂停关联策略",
    event_type: "backup_failed",
    event_filter: {},
    action_type: "disable_policy",
    action_config: { policy_id: "1" },
    enabled: true,
    created_at: "2026-09-01T00:00:00Z",
    updated_at: AT,
  },
];

export const automationRuleLogs = [
  { id: 3, rule_id: 1, event_type: "backup_failed", action_type: "trigger_task", result: "success", created_at: AT, error_code: null, target_task_id: 1, target_task_run_id: 101 },
  { id: 2, rule_id: 99, event_type: "anomaly_detected", action_type: "pause_policy", result: "error", created_at: AT, error_code: "ACTION_FAILED", target_task_id: null, target_task_run_id: null },
  { id: 1, rule_id: 1, event_type: "backup_succeeded", action_type: "send_notification", result: "success", created_at: AT, error_code: null, target_task_id: null, target_task_run_id: null },
];

export const auditLogs = [
  {
    id: 1,
    user_id: 1,
    username: "admin",
    role: "admin",
    method: "POST",
    path: "/api/v1/tasks/1/trigger",
    status_code: 200,
    client_ip: "10.0.0.8",
    user_agent: "walkthrough",
    created_at: "2026-10-04T05:00:00Z",
  },
];

export const credentialAuditEvents = [
  {
    id: 1,
    user_id: 1,
    username: "admin",
    role: "admin",
    action: "use",
    purpose: CREDENTIAL_AUDIT_PURPOSE,
    credential_kind: "app_credential",
    credential_source: "app",
    ssh_key_id: 1,
    node_id: 1,
    task_id: 1,
    outcome: "success",
    error_message: "",
    metadata: {},
    client_ip: "10.0.0.8",
    user_agent: "walkthrough",
    created_at: "2026-10-04T05:00:00Z",
  },
];

export const credentialAccessGrants = [
  {
    id: 1,
    requester_user_id: 2,
    requester_username: "operator",
    requester_role: "operator",
    action: "terminal.open",
    purpose: "terminal",
    node_id: 1,
    reason: "生产节点日常巡检与维护",
    status: "approved",
    requested_ttl_seconds: 3600,
    requested_at: "2026-10-04T05:00:00Z",
    approved_at: "2026-10-04T05:01:00Z",
    approver_user_id: 1,
    approver_username: "admin",
    expires_at: "2026-10-04T06:00:00Z",
    created_at: "2026-10-04T05:00:00Z",
    updated_at: "2026-10-04T05:01:00Z",
  },
];

export const appCredentials = [
  {
    id: 1,
    name: "生产主库凭据",
    type: "mysql",
    description: "北京生产主库应用账号",
    config: { host: "10.30.1.7", port: "3306", user: "backup" },
    has_password: true,
    reference_count: 1,
    created_at: "2026-09-01T00:00:00Z",
    updated_at: AT,
  },
];

export const profileSchemas = [
  {
    id: "mysql",
    name: "MySQL",
    description: "MySQL 应用备份配置",
    credential_type: "mysql",
    is_docker: false,
    config_schema: [],
  },
];

export const reportConfigs = [
  {
    id: 1,
    name: "每周备份可用性综合报告",
    scope_type: "all",
    scope_value: "",
    period: "weekly",
    cron: "0 8 * * 1",
    integration_ids: [1],
    enabled: true,
    created_at: "2026-09-01T00:00:00Z",
    updated_at: AT,
  },
];

export const reports = [
  {
    id: 11,
    config_id: 1,
    period_start: "2026-09-28T00:00:00Z",
    period_end: "2026-10-04T00:00:00Z",
    total_runs: 14,
    success_runs: 13,
    failed_runs: 1,
    success_rate: 0.928,
    avg_duration_ms: 15420,
    top_failures: [
      { node_name: "广州归档存储-01", task_name: "广州归档同步", count: 1, last_err: "SSH timeout" },
    ],
    actual_rpo_minutes: 40,
    actual_rto_minutes: 90,
    rpo_compliant: true,
    rto_compliant: true,
    generated_at: AT,
    created_at: AT,
  },
];

export const slos = [
  {
    id: 1,
    name: "生产核心数据库备份成功率",
    metric_type: "success_rate",
    match_tags: ["db", "prod"],
    threshold: 0.99,
    window_days: 28,
    enabled: true,
    created_by: 1,
    created_at: "2026-09-01T00:00:00Z",
    updated_at: AT,
    escalation_policy_id: 1,
  },
];

export const sloCompliance = {
  slo_id: 1,
  name: "生产核心数据库备份成功率",
  metric_type: "success_rate",
  window_start: "2026-09-06T00:00:00Z",
  window_end: "2026-10-04T00:00:00Z",
  threshold: 0.99,
  observed: 0.995,
  sample_count: 120,
  error_budget_remaining_pct: 50,
  burn_rate_1h: 0,
  status: "healthy",
};

export const sloSummary = {
  total: 1,
  healthy: 1,
  warning: 0,
  breached: 0,
  insufficient: 0,
};

export const silences = [
  {
    id: 1,
    name: SILENCE_NAME,
    match_node_id: 1,
    match_category: "backup",
    match_tags: ["db"],
    starts_at: "2026-10-04T00:00:00Z",
    ends_at: "2026-10-05T00:00:00Z",
    created_by: 1,
    note: "walkthrough silence",
    created_at: "2026-10-04T00:00:00Z",
    updated_at: AT,
  },
];

export const escalationPolicies = [
  {
    id: 1,
    name: "生产 P0 故障升级流",
    description: "关键备份失败后通知值班通道",
    min_severity: "critical",
    enabled: true,
    levels: [
      { delay_seconds: 0, integration_ids: [1], severity_override: "", tags: ["p0"] },
    ],
    created_at: "2026-09-01T00:00:00Z",
    updated_at: AT,
  },
];

export const nodeSummary = { open_alerts: 0, running_tasks: 0 };

export const anomalyEvents = [
  {
    id: 1,
    node_id: 1,
    detector: "snapshot_diff",
    metric: ANOMALY_METRIC,
    severity: "warning",
    observed_value: 12,
    baseline_value: 4,
    sigma: 3.1,
    alert_id: null,
    raised_alert: false,
    details: "snapshot size diverged",
    fired_at: "2026-10-04T05:10:00Z",
  },
];

export const overviewSummary = { activePolicies: 2 };

export function overviewTraffic(window: string) {
  return {
    window: window || "1h",
    bucket_minutes: 5,
    has_real_samples: false,
    truncated: false,
    generated_at: AT,
    points: [],
  };
}

export const healthIncidentTimeline = {
  generated_at: AT,
  window_hours: 72,
  summary: { total: 1, critical: 0, warning: 1, info: 0 },
  groups: [
    {
      id: "walkthrough-incident",
      severity: "warning",
      resource: {
        type: "node",
        id: 1,
        name: "北京生产主库-01",
        node_id: 1,
        node_name: "北京生产主库-01",
      },
      last_seen_at: "2026-10-04T05:00:00Z",
      event_count: 1,
      likely_cause: INCIDENT_CAUSE,
      source_types: ["alert"],
      next_actions: [],
      signals: [
        {
          type: "alert",
          severity: "warning",
          occurred_at: "2026-10-04T05:00:00Z",
          message: INCIDENT_CAUSE,
          node_id: 1,
          alert_id: 1,
        },
      ],
    },
  ],
};

export const taskStatistics = {
  series: [
    {
      name: "all",
      points: [{ ts: "2026-10-04T05:00:00.000Z", value: 1 }],
    },
  ],
  step_seconds: 900,
  truncated: false,
};

export const failureSummary = {
  failed_tasks: FAILURE_SUMMARY_COUNT,
  window_hours: 24,
};

export const deliveryStats = {
  window_hours: 24,
  total_sent: 4,
  total_failed: 0,
  success_rate: 100,
  by_integration: [
    { integration_id: 1, name: "运维监控告警 Webhook", type: "webhook", sent: 4, failed: 0 },
  ],
};

export const unreadCount = { total: 2, critical: 1, warning: 1 };

export const settingsPayload = {
  definitions: [
    {
      key: "backup_assets.content_allow_insecure_private_network",
      env_var: "BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK",
      code_default: "false",
      type: "bool",
      category: "backup_assets",
      description: "Allow HTTP backup content delivery on trusted private networks",
      requires_restart: false,
      sensitive: false,
    },
  ],
  values: {
    "backup_assets.content_allow_insecure_private_network": {
      value: "false",
      source: "default",
      updated_at: null,
    },
  },
};

export const securityRiskSummary = {
  generated_at: AT,
  summary: { total_risks: 0, categories: 0 },
  items: [],
};

export const gaReadiness = {
  schema_version: 1,
  class: "existing",
  status: "ready",
  inventory_complete: true,
  inventory_digest: "a".repeat(64),
  acknowledged_digest: "b".repeat(64),
  export_root_valid: true,
  key_domains_ready: true,
  worker_optional: true,
  counts: { candidates: 1, conflicts: 0, unsupported: 0, capability_gaps: 0 },
  conflicts: [],
};

export const systemBackups = [
  {
    filename: SELF_BACKUP_FILENAME,
    size: 4096,
    created_at: AT,
    sha256: "ab".repeat(32),
  },
];

export const versionInfo = {
  version: "walkthrough",
  build_time: AT,
  git_commit: "bad12b3f",
};

export const versionCheck = {
  update_available: false,
  current_version: "walkthrough",
  latest_version: "walkthrough",
  release_url: "",
};

export const taskRuns = [
  {
    id: 101,
    task_id: 1,
    trigger_type: "cron",
    status: "success",
    started_at: "2026-10-04T05:00:00Z",
    finished_at: "2026-10-04T05:30:00Z",
    duration_ms: 15420,
    verify_status: "passed",
    throughput_mbps: 12,
    created_at: "2026-10-04T05:00:00Z",
    task: { id: 1, name: "MySQL 日常全备", node_id: 1, executor_type: "rsync" },
  },
];

export const fileSourceNodesPage = {
  items: [
    {
      node_id: FILE_SOURCE_NODE_ID,
      display_name: "synthetic-node-17",
      backup_set_count: 1,
      retained_version_count: 1,
      latest_retained_at: "2026-07-19T00:05:00Z",
      catalog_coverage: "complete",
      browse_state: "browsable",
      unavailable_reason: null,
    },
  ],
  next_cursor: null,
};

export const fileSourceSetsPage = {
  items: [
    {
      backup_set_id: FILE_SOURCE_SET_ID,
      node_id: FILE_SOURCE_NODE_ID,
      display_label: "合成夜间备份 · Synthetic nightly archive",
      lineage_kind: "task",
      version_count: 1,
      latest_retained_at: "2026-07-19T00:05:00Z",
      catalog_coverage: "complete",
      browse_state: "browsable",
      unavailable_reason: null,
    },
  ],
  next_cursor: null,
};

export const fileSourceVersionsPage = {
  items: [
    {
      recovery_point_id: ONLINE_RECOVERY_POINT_ID,
      repository_id: ONLINE_REPOSITORY_ID,
      producing_task_id: 1,
      captured_at: "2026-07-19T00:00:00Z",
      committed_at: "2026-07-19T00:05:00Z",
      created_at: "2026-07-19T00:00:00Z",
      lifecycle_state: "committed",
      catalog_coverage: "complete",
      browse_state: "browsable",
      unavailable_reason: null,
      content_availability: { available: true, reason: null },
      entry_count: 240,
      logical_bytes: 8388608,
      permissions: { list: true, preview: false, download: false },
    },
  ],
  next_cursor: null,
};

export const fileSourceRecoveryPoint = {
  node_id: FILE_SOURCE_NODE_ID,
  backup_set_id: FILE_SOURCE_SET_ID,
  recovery_point_id: ONLINE_RECOVERY_POINT_ID,
  repository_id: ONLINE_REPOSITORY_ID,
  producing_task_id: 1,
  browse_state: "browsable",
  unavailable_reason: null,
};

export const emptyCursorPage = { items: [], next_cursor: null };
