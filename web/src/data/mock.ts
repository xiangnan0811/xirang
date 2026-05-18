import type {
  AlertRecord,
  BackupConfidenceData,
  BackupHealthData,
  IntegrationChannel,
  LogEvent,
  NodeDoctorResult,
  NodeRecord,
  OverviewSummary,
  OverviewTrafficSeries,
  OverviewTrafficWindow,
  HealthIncidentTimelineData,
  OverviewStats,
  PolicyRecord,
  SSHKeyRecord,
  StorageUsageData,
  TaskRecord
} from "@/types/domain";

const nodeNames = [
  "北京主库",
  "上海热备",
  "广州归档",
  "深圳边缘",
  "杭州对象",
  "成都日志",
  "武汉中转",
  "西安仓储",
  "青岛镜像",
  "南京主站",
  "苏州容灾",
  "天津网关"
];

const tagPool = ["core", "db", "edge", "archive", "cdn", "critical", "prod", "staging"];
const nowMinus = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60_000).toISOString();

function formatDate(minutesAgo: number): string {
  const date = new Date(Date.now() - minutesAgo * 60_000);
  const p = (n: number) => n.toString().padStart(2, "0");
  return `${date.getFullYear()}-${p(date.getMonth() + 1)}-${p(date.getDate())} ${p(date.getHours())}:${p(date.getMinutes())}:${p(date.getSeconds())}`;
}

function nodeStatusByIndex(index: number): NodeRecord["status"] {
  if (index % 11 === 0) {
    return "offline";
  }
  if (index % 6 === 0) {
    return "warning";
  }
  return "online";
}

function buildFingerprint(seed: number) {
  const base = `${seed}`.padStart(4, "0");
  return `SHA256:xi-rang-${base}-${(seed * 97).toString(16)}`;
}

export const mockSSHKeys: SSHKeyRecord[] = [
  {
    id: "key-ops-prod",
    name: "ops-prod-rsa",
    username: "root",
    keyType: "rsa",
    privateKey: "mock-only-key-redacted",
    fingerprint: buildFingerprint(101),
    createdAt: formatDate(60 * 24 * 7),
    lastUsedAt: formatDate(8)
  },
  {
    id: "key-ops-staging",
    name: "ops-staging-ed25519",
    username: "ubuntu",
    keyType: "ed25519",
    privateKey: "mock-only-key-redacted",
    fingerprint: buildFingerprint(102),
    createdAt: formatDate(60 * 24 * 3),
    lastUsedAt: formatDate(22)
  }
];

export const mockNodes: NodeRecord[] = Array.from({ length: 36 }, (_, idx) => {
  const id = idx + 1;
  const status = nodeStatusByIndex(id);
  const usedGb = 180 + (id * 13) % 420;
  const totalGb = 800;
  const host = `10.30.${Math.floor(id / 10) + 1}.${(id * 7) % 255}`;

  return {
    id,
    name: `${nodeNames[idx % nodeNames.length]}-${Math.floor(idx / nodeNames.length) + 1}`,
    host,
    address: host,
    ip: host,
    port: 22,
    username: id % 4 === 0 ? "ubuntu" : "root",
    authType: "key",
    keyId: id % 4 === 0 ? "key-ops-staging" : "key-ops-prod",
    basePath: "/",
    status,
    tags: [tagPool[idx % tagPool.length], tagPool[(idx + 2) % tagPool.length]],
    lastSeenAt: formatDate(status === "offline" ? 130 : 1 + (id % 4)),
    lastBackupAt: formatDate(status === "offline" ? 500 : 10 + (id % 40)),
    diskFreePercent: Math.max(4, Math.round(((totalGb - usedGb) / totalGb) * 100)),
    diskUsedGb: usedGb,
    diskTotalGb: totalGb,
    diskProbeAt: formatDate(5 + (id % 8)),
    connectionLatencyMs: 22 + (id * 9) % 78
  };
});

export const mockPolicies: PolicyRecord[] = [
  {
    id: 1,
    name: "核心 MySQL 增量（演示）",
    sourcePath: "/demo/mysql",
    targetPath: "/demo-backup/core/mysql",
    cron: "0 */2 * * *",
    naturalLanguage: "每隔两小时同步一次",
    enabled: true,
    criticalThreshold: 2,
    nodeIds: [1, 2],
    verifyEnabled: true,
    verifySampleRate: 100,
    drill_enabled: true,
    drill_cron: "30 3 * * *",
    drill_target_node_id: 11,
    drill_restore_path: "/tmp/xirang-demo-drill/mysql",
    drill_pre_verify: "test -d /tmp/xirang-demo-drill/mysql",
    drill_verify: "mysqlcheck --defaults-file=/tmp/xirang-demo-drill/mysql/.demo.cnf --all-databases",
    drill_post_verify: "test -f /tmp/xirang-demo-drill/mysql/xirang-demo-proof.json",
    drill_auto_cleanup: true,
    latestDrill: {
      taskRunId: 9101,
      status: "success",
      confidenceEligible: true,
      startedAt: formatDate(92),
      finishedAt: formatDate(86),
      durationMs: 366000
    }
  },
  {
    id: 2,
    name: "Nginx 日志归档",
    sourcePath: "/var/log/nginx",
    targetPath: "/backup/logs/nginx",
    cron: "*/30 * * * *",
    naturalLanguage: "每 30 分钟同步一次",
    enabled: true,
    criticalThreshold: 3,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drill_enabled: false,
    drill_cron: "",
    drill_restore_path: "/tmp/xirang-drill",
    drill_pre_verify: "",
    drill_verify: "",
    drill_post_verify: "",
    drill_auto_cleanup: true
  },
  {
    id: 3,
    name: "订单服务快照",
    sourcePath: "/srv/order-data",
    targetPath: "/backup/order/snapshot",
    cron: "15 */6 * * *",
    naturalLanguage: "每 6 小时在第 15 分钟执行",
    enabled: true,
    criticalThreshold: 1,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drill_enabled: false,
    drill_cron: "",
    drill_restore_path: "/tmp/xirang-drill",
    drill_pre_verify: "",
    drill_verify: "",
    drill_post_verify: "",
    drill_auto_cleanup: true
  },
  {
    id: 4,
    name: "周度全量归档",
    sourcePath: "/srv/archive",
    targetPath: "/backup/full/weekly",
    cron: "0 3 * * 0",
    naturalLanguage: "每周日凌晨 3 点执行",
    enabled: false,
    criticalThreshold: 1,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drill_enabled: false,
    drill_cron: "",
    drill_restore_path: "/tmp/xirang-drill",
    drill_pre_verify: "",
    drill_verify: "",
    drill_post_verify: "",
    drill_auto_cleanup: true
  },
  {
    id: 5,
    name: "对象存储元数据",
    sourcePath: "/data/object-meta",
    targetPath: "/backup/object/meta",
    cron: "*/10 * * * *",
    naturalLanguage: "每 10 分钟同步一次",
    enabled: true,
    criticalThreshold: 4,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drill_enabled: false,
    drill_cron: "",
    drill_restore_path: "/tmp/xirang-drill",
    drill_pre_verify: "",
    drill_verify: "",
    drill_post_verify: "",
    drill_auto_cleanup: true
  },
  {
    id: 6,
    name: "监控指标备份",
    sourcePath: "/data/prometheus",
    targetPath: "/backup/metrics/prom",
    cron: "5 */4 * * *",
    naturalLanguage: "每 4 小时第 5 分钟执行",
    enabled: true,
    criticalThreshold: 2,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drill_enabled: false,
    drill_cron: "",
    drill_restore_path: "/tmp/xirang-drill",
    drill_pre_verify: "",
    drill_verify: "",
    drill_post_verify: "",
    drill_auto_cleanup: true
  },
  {
    id: 7,
    name: "Redis RDB 归档",
    sourcePath: "/data/redis",
    targetPath: "/backup/cache/rdb",
    cron: "*/20 * * * *",
    naturalLanguage: "每 20 分钟同步一次",
    enabled: true,
    criticalThreshold: 2,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drill_enabled: false,
    drill_cron: "",
    drill_restore_path: "/tmp/xirang-drill",
    drill_pre_verify: "",
    drill_verify: "",
    drill_post_verify: "",
    drill_auto_cleanup: true
  },
  {
    id: 8,
    name: "静态资源镜像",
    sourcePath: "/srv/static",
    targetPath: "/backup/static/mirror",
    cron: "0 */3 * * *",
    naturalLanguage: "每 3 小时同步一次",
    enabled: true,
    criticalThreshold: 3,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drill_enabled: false,
    drill_cron: "",
    drill_restore_path: "/tmp/xirang-drill",
    drill_pre_verify: "",
    drill_verify: "",
    drill_post_verify: "",
    drill_auto_cleanup: true
  },
  {
    id: 9,
    name: "审计日志冷存",
    sourcePath: "/var/log/audit",
    targetPath: "/backup/audit/cold",
    cron: "45 */1 * * *",
    naturalLanguage: "每小时第 45 分钟同步",
    enabled: true,
    criticalThreshold: 2,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drill_enabled: false,
    drill_cron: "",
    drill_restore_path: "/tmp/xirang-drill",
    drill_pre_verify: "",
    drill_verify: "",
    drill_post_verify: "",
    drill_auto_cleanup: true
  },
  {
    id: 10,
    name: "容器镜像元数据",
    sourcePath: "/var/lib/registry",
    targetPath: "/backup/registry/meta",
    cron: "*/15 * * * *",
    naturalLanguage: "每 15 分钟同步一次",
    enabled: true,
    criticalThreshold: 3,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drill_enabled: false,
    drill_cron: "",
    drill_restore_path: "/tmp/xirang-drill",
    drill_pre_verify: "",
    drill_verify: "",
    drill_post_verify: "",
    drill_auto_cleanup: true
  },
  {
    id: 11,
    name: "消息队列快照（演示故障）",
    sourcePath: "/demo/kafka",
    targetPath: "/demo-backup/mq/snapshot",
    cron: "30 */2 * * *",
    naturalLanguage: "每 2 小时第 30 分钟执行",
    enabled: true,
    criticalThreshold: 2,
    nodeIds: [24],
    verifyEnabled: true,
    verifySampleRate: 50,
    drill_enabled: true,
    drill_cron: "15 4 * * *",
    drill_target_node_id: 23,
    drill_restore_path: "/tmp/xirang-demo-drill/kafka",
    drill_pre_verify: "test -d /tmp/xirang-demo-drill/kafka",
    drill_verify: "test -f /tmp/xirang-demo-drill/kafka/meta.properties",
    drill_post_verify: "test -f /tmp/xirang-demo-drill/kafka/xirang-demo-proof.json",
    drill_auto_cleanup: true,
    latestDrill: {
      taskRunId: 9114,
      status: "failed",
      failedStep: "sandbox_precheck",
      confidenceEligible: false,
      startedAt: formatDate(48),
      finishedAt: formatDate(45),
      durationMs: 185000
    }
  },
  {
    id: 12,
    name: "配置中心备份",
    sourcePath: "/etc/xirang",
    targetPath: "/backup/config",
    cron: "0 */8 * * *",
    naturalLanguage: "每 8 小时同步一次",
    enabled: true,
    criticalThreshold: 1,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drill_enabled: false,
    drill_cron: "",
    drill_restore_path: "/tmp/xirang-drill",
    drill_pre_verify: "",
    drill_verify: "",
    drill_post_verify: "",
    drill_auto_cleanup: true
  }
];

export const mockTasks: TaskRecord[] = Array.from({ length: 18 }, (_, idx) => {
  const id = 3000 + idx + 1;
  const node = mockNodes[idx % mockNodes.length];
  const policy = mockPolicies[idx % mockPolicies.length];
  const storySuccess = id === 3001;
  const storyFailure = id === 3014;
  const storyPolicy = storyFailure ? mockPolicies.find((item) => item.id === 11) ?? policy : policy;
  const status: TaskRecord["status"] = storyFailure
    ? "failed"
    : storySuccess
      ? "success"
      : idx % 7 === 0
        ? "failed"
        : idx % 5 === 0
          ? "retrying"
          : idx % 4 === 0
            ? "pending"
            : idx % 3 === 0
              ? "running"
              : "success";

  return {
    id,
    name: storySuccess ? "演示可信路径：核心 MySQL 增量" : storyFailure ? "演示故障路径：消息队列快照" : policy.name,
    policyName: storyPolicy.name,
    policyId: storyPolicy.id,
    nodeName: storySuccess ? "北京主库-1" : storyFailure ? "天津网关-2" : node.name,
    nodeId: storySuccess ? 1 : storyFailure ? 24 : node.id,
    status,
    progress:
      status === "success"
        ? 100
        : status === "running"
          ? 22 + (idx * 9) % 65
          : status === "failed"
            ? 43
            : status === "retrying"
              ? 12
              : 0,
    startedAt: formatDate(storySuccess ? 24 : storyFailure ? 9 : 2 + idx * 3),
    updatedAt: formatDate(storySuccess ? 18 : storyFailure ? 5 : 1 + idx * 3),
    errorCode: storyFailure ? "XR-AUTH-011" : status === "failed" ? `XR-EXEC-${900 + idx}` : undefined,
    lastError: storyFailure ? "SSH 认证失败，演示私钥已标记为过期" : undefined,
    verifyStatus: storySuccess ? "passed" : storyFailure ? "failed" : undefined,
    speedMbps: storySuccess ? 168 : storyFailure ? 0 : 40 + (idx * 11) % 180,
    enabled: true
  };
});

const mockTrafficTotals = [308, 348, 330, 390, 430, 415, 451, 419, 473, 495, 479, 508];

export const mockOverview: OverviewStats = {
  totalNodes: mockNodes.length,
  healthyNodes: mockNodes.filter((node) => node.status === "online").length,
  activePolicies: mockPolicies.filter((policy) => policy.enabled).length,
  runningTasks: mockTasks.filter((task) => task.status === "running").length,
  failedTasks24h: mockTasks.filter((task) => task.status === "failed").length,
  overallSuccessRate: 95.7,
  avgSyncMbps: 182
};

export const mockOverviewSummary: OverviewSummary = {
  totalNodes: mockOverview.totalNodes,
  healthyNodes: mockOverview.healthyNodes,
  activePolicies: mockOverview.activePolicies,
  runningTasks: mockOverview.runningTasks,
  failedTasks24h: mockOverview.failedTasks24h,
  currentThroughputMbps: 318
};

function generateTrafficValues(count: number, base: number, step: number) {
  return Array.from({ length: count }, (_, index) => {
    const wave = Math.sin(index / 3) * step;
    const drift = (index % 5) * 4;
    return Math.max(0, Math.round(base + wave + drift));
  });
}

export function buildMockHealthIncidentTimeline(): HealthIncidentTimelineData {
  const now = new Date();
  const recent = new Date(now.getTime() - 9 * 60_000).toISOString();
  const earlier = new Date(now.getTime() - 34 * 60_000).toISOString();
  return {
    generatedAt: now.toISOString(),
    windowHours: 72,
    summary: { total: 2, critical: 1, warning: 1, info: 0 },
    groups: [
      {
        id: "task-3014-demo-auth",
        severity: "critical",
        resource: {
          type: "task",
          id: 3014,
          name: "消息队列快照（演示故障）",
          nodeId: 24,
          nodeName: "天津网关-2",
          policyId: 11,
          policyName: "消息队列快照（演示故障）"
        },
        lastSeenAt: recent,
        eventCount: 3,
        likelyCause: "演示故障：天津网关-2 的 SSH Key 已过期，备份任务与恢复演练的沙箱预检均无法完成。",
        sourceTypes: ["alert", "task_failure", "backup_degraded"],
        nextActions: [
          { code: "view_task_logs", label: "查看任务日志", href: "/app/logs?task=3014" },
          { code: "view_alert", label: "查看告警", href: "/app/notifications?alert=alert-001" }
        ],
        signals: [
          { type: "task_failure", severity: "critical", occurredAt: recent, message: "演示数据：rsync 因 SSH 认证失败退出，未传输真实文件。", taskId: 3014, taskRunId: 9114, nodeId: 24, policyId: 11 },
          { type: "alert", severity: "critical", occurredAt: earlier, message: "演示数据：XR-AUTH-011，私钥过期或未授权。", alertId: 1, taskId: 3014, nodeId: 24, policyId: 11 },
          { type: "backup_degraded", severity: "critical", occurredAt: recent, message: "演示数据：最近恢复演练停在沙箱预检，可信度降为有风险。", taskId: 3014, taskRunId: 9114, nodeId: 24, policyId: 11 }
        ]
      },
      {
        id: "node-3-demo-disk",
        severity: "warning",
        resource: { type: "node", id: 3, name: "广州归档-1", nodeId: 3, nodeName: "广州归档-1" },
        lastSeenAt: earlier,
        eventCount: 1,
        likelyCause: "演示数据：磁盘使用率 91.5% 超过阈值，建议扩容或调整保留策略。",
        sourceTypes: ["metric"],
        nextActions: [{ code: "view_node_metrics", label: "查看节点指标", href: "/app/nodes/3?tab=metrics" }],
        signals: [{ type: "metric", severity: "warning", occurredAt: earlier, message: "演示数据：磁盘使用率 91.5% 超过阈值。", nodeId: 3 }]
      }
    ]
  };
}

export function buildMockOverviewTrafficSeries(window: OverviewTrafficWindow): OverviewTrafficSeries {
  const now = new Date();
  const config = window === "1h"
    ? {
        count: 12,
        bucketMinutes: 5,
        format: (date: Date) => `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`,
        values: mockTrafficTotals
      }
    : window === "24h"
      ? {
          count: 48,
          bucketMinutes: 30,
          format: (date: Date) => `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`,
          values: generateTrafficValues(48, 180, 42)
        }
      : {
          count: 56,
          bucketMinutes: 180,
          format: (date: Date) => `${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")} ${String(date.getHours()).padStart(2, "0")}:00`,
          values: generateTrafficValues(56, 210, 55)
        };

  return {
    window,
    bucketMinutes: config.bucketMinutes,
    hasRealSamples: true,
    generatedAt: now.toISOString(),
    points: Array.from({ length: config.count }, (_, index) => {
      const pointTime = new Date(now.getTime() - (config.count - 1 - index) * config.bucketMinutes * 60_000);
      return {
        timestamp: pointTime.toISOString(),
        timestampMs: pointTime.getTime(),
        label: config.format(pointTime),
        throughputMbps: config.values[index] ?? config.values[config.values.length - 1] ?? 0,
        sampleCount: 1,
        activeTaskCount: 1,
        startedCount: index % 6 == 0 ? 1 : 0,
        failedCount: index % 11 == 0 ? 1 : 0
      };
    })
  };
}

export const mockSeedLogs: LogEvent[] = [
  {
    id: "log-1",
    logId: 1001,
    timestamp: formatDate(24),
    level: "info",
    message: "演示数据：(node:北京主库-1) 增量备份完成，恢复演练证据 taskRun=9101 已通过。",
    nodeName: "北京主库-1",
    taskId: 3001,
    progress: 100
  },
  {
    id: "log-1b",
    logId: 1002,
    timestamp: formatDate(18),
    level: "info",
    message: "演示数据：(drill:9101) 沙箱 苏州容灾-1 校验通过，RTO=366s，可信度可作为恢复证据。",
    nodeName: "苏州容灾-1",
    taskId: 3001,
    progress: 100
  },
  {
    id: "log-2",
    logId: 1003,
    timestamp: formatDate(1),
    level: "warn",
    message: "演示数据：(node:广州归档-1) 网络抖动，进入退避重试",
    nodeName: "广州归档-1",
    taskId: 3007,
    errorCode: "XR-NODE-421",
    progress: 48
  },
  {
    id: "log-3",
    logId: 1004,
    timestamp: formatDate(2),
    level: "error",
    message: "演示数据：(task:3008) rsync returned code 23, file vanished",
    nodeName: "深圳边缘-1",
    taskId: 3008,
    errorCode: "XR-EXEC-923",
    progress: 52
  },
  {
    id: "log-4",
    logId: 1005,
    timestamp: formatDate(3),
    level: "info",
    message: "演示数据：(task:3010) sent 1.8GB in 73s, speed 201MB/s",
    nodeName: "杭州对象-1",
    taskId: 3010,
    progress: 100
  },
  {
    id: "log-5",
    logId: 1006,
    timestamp: formatDate(9),
    level: "error",
    message: "演示数据：(node:天津网关-2) SSH 握手失败 XR-AUTH-011，私钥已过期，未连接真实主机。",
    nodeName: "天津网关-2",
    taskId: 3014,
    errorCode: "XR-AUTH-011",
    progress: 0
  },
  {
    id: "log-6",
    logId: 1007,
    timestamp: formatDate(8),
    level: "warn",
    message: "演示数据：(drill:9114) 恢复演练停在 sandbox_precheck，Doctor 建议轮换 SSH Key 后重试。",
    nodeName: "天津网关-2",
    taskId: 3014,
    errorCode: "XR-AUTH-011",
    progress: 0
  }
];

export function buildMockBackupConfidence(): BackupConfidenceData {
  const generatedAt = new Date().toISOString();
  return {
    generatedAt,
    summary: { healthy: 1, warning: 0, atRisk: 1, insufficient: 0, total: 2 },
    items: [
      {
        id: "policy-1-demo-trusted",
        scope: "policy",
        policyId: 1,
        policyName: "核心 MySQL 增量（演示）",
        nodeId: 1,
        nodeName: "北京主库-1",
        status: "healthy",
        score: 96,
        reasons: [],
        evidence: [
          { type: "backup", status: "success", message: "演示数据：最近备份成功，传输 42.8GB，校验通过。", observedAt: nowMinus(18), taskId: 3001, taskRunId: 9001 },
          { type: "drill", status: "success", message: "演示数据：最近恢复演练 taskRun=9101 在沙箱苏州容灾-1 通过，RTO 366s。", observedAt: nowMinus(86), taskId: 3001, taskRunId: 9101 },
          { type: "rpo", status: "ok", message: "演示数据：当前 RPO 18 分钟，低于 120 分钟目标。", observedAt: nowMinus(18) }
        ],
        nextSteps: [
          { code: "keep_schedule", label: "保持当前调度，继续观察下一次演练结果。" }
        ],
        targets: [
          { nodeId: 1, nodeName: "北京主库-1", lastBackupAt: formatDate(18) },
          { nodeId: 2, nodeName: "上海热备-1", lastBackupAt: formatDate(21) }
        ]
      },
      {
        id: "policy-11-demo-failure",
        scope: "policy",
        policyId: 11,
        policyName: "消息队列快照（演示故障）",
        nodeId: 24,
        nodeName: "天津网关-2",
        status: "at_risk",
        score: 41,
        reasons: [
          { code: "ssh_auth_failed", severity: "critical", message: "演示故障：SSH Key 已过期，最近备份和恢复演练均无法建立连接。" }
        ],
        evidence: [
          { type: "backup", status: "failed", message: "演示数据：任务 3014 因 XR-AUTH-011 失败，未产生可用快照。", observedAt: nowMinus(9), taskId: 3014, taskRunId: 9014, alertId: 1 },
          { type: "drill", status: "failed", message: "演示数据：恢复演练 9114 停在 sandbox_precheck。", observedAt: nowMinus(45), taskId: 3014, taskRunId: 9114 },
          { type: "alert", status: "open", message: "演示数据：告警 alert-001 仍未解决，建议先运行 Fleet Doctor。", observedAt: nowMinus(2), taskId: 3014, alertId: 1 }
        ],
        nextSteps: [
          { code: "run_node_doctor", label: "打开节点页运行 Fleet Doctor，确认 SSH Key 与 known_hosts。" },
          { code: "rotate_ssh_key", label: "轮换演示节点的 SSH Key 后重试备份与恢复演练。" }
        ],
        targets: [{ nodeId: 24, nodeName: "天津网关-2", lastBackupAt: formatDate(60 * 54) }]
      }
    ]
  };
}

export function buildMockBackupHealth(): BackupHealthData {
  return {
    staleNodes: [
      { nodeId: 24, nodeName: "天津网关-2", lastBackupAt: formatDate(60 * 54), hoursSince: 54 }
    ],
    degradedPolicies: [
      { policyId: 11, policyName: "消息队列快照（演示故障）", consecutiveFailures: 2, lastFailedAt: formatDate(9) }
    ],
    healthTrend: Array.from({ length: 7 }, (_, index) => {
      const total = 18 + index;
      const success = index === 6 ? total - 2 : total - 1;
      return {
        date: formatDate((6 - index) * 24 * 60).slice(5, 10),
        total,
        success,
        rate: Math.round((success / total) * 1000) / 10
      };
    }),
    summary: {
      totalNodes: mockNodes.length,
      neverBackedUp: 0,
      stale48h: 1,
      policiesHealthy: 1,
      policiesDegraded: 1,
      successRate7d: 94.2
    }
  };
}

export function buildMockStorageUsage(): StorageUsageData {
  return {
    mountPoints: [
      { path: "/demo-backup", usedGB: 1280, totalGB: 4096, pct: 31.25 },
      { path: "/demo-backup/mq", usedGB: 730, totalGB: 1024, pct: 71.3 }
    ],
    perNode: [
      { nodeId: 1, nodeName: "北京主库-1", path: "/demo-backup/core/mysql", usedGB: 420 },
      { nodeId: 24, nodeName: "天津网关-2", path: "/demo-backup/mq/snapshot", usedGB: 118 }
    ]
  };
}

export function buildMockNodeDoctorResult(node: NodeRecord): NodeDoctorResult {
  const isFailureStory = node.id === 24 || node.name.includes("天津网关");
  return {
    nodeId: node.id,
    nodeName: node.name,
    generatedAt: new Date().toISOString(),
    checks: isFailureStory
      ? [
          {
            check: "ssh_auth",
            status: "fail",
            evidence: "演示数据：SSH 握手返回 XR-AUTH-011，当前密钥指纹与节点授权不匹配。",
            suggestion: "轮换或重新绑定 SSH Key，然后重试备份任务与恢复演练。"
          },
          {
            check: "known_hosts",
            status: "warn",
            evidence: "演示数据：known_hosts 中存在旧指纹记录，未连接真实基础设施。",
            suggestion: "核对主机指纹来源，确认后更新 known_hosts。"
          },
          {
            check: "backup_path",
            status: "skip",
            evidence: "演示数据：认证失败，未执行远端路径探测。",
            suggestion: "先修复 SSH 认证，再检查 /demo/kafka 与 /demo-backup/mq/snapshot。"
          }
        ]
      : [
          {
            check: "ssh_auth",
            status: "pass",
            evidence: "演示数据：SSH 握手成功，使用 mock 密钥 key-ops-prod，未连接真实主机。",
            suggestion: "保持密钥轮换策略并继续记录审计日志。"
          },
          {
            check: "backup_path",
            status: "pass",
            evidence: "演示数据：/demo-backup/core/mysql 可写，剩余空间充足。",
            suggestion: "可继续保留当前恢复演练配置。"
          },
          {
            check: "restore_drill",
            status: "pass",
            evidence: "演示数据：最近恢复演练 taskRun=9101 已通过，可信度证据完整。",
            suggestion: "下一次演练按 30 3 * * * 自动执行。"
          }
        ]
  };
}

export const mockIntegrations: IntegrationChannel[] = [];

export const mockAlerts: AlertRecord[] = [
  {
    id: "alert-001",
    nodeName: "天津网关-2",
    nodeId: 24,
    taskId: 3014,
    policyName: "消息队列快照（演示故障）",
    severity: "critical",
    status: "open",
    errorCode: "XR-AUTH-011",
    message: "演示故障：SSH 认证失败，私钥可能过期",
    triggeredAt: formatDate(2),
    retryable: true
  },
  {
    id: "alert-002",
    nodeName: "广州归档-1",
    nodeId: 3,
    taskId: 3007,
    policyName: "审计日志冷存",
    severity: "warning",
    status: "open",
    errorCode: "XR-NODE-421",
    message: "网络波动导致重试次数接近阈值",
    triggeredAt: formatDate(6),
    retryable: true
  },
  {
    id: "alert-003",
    nodeName: "深圳边缘-1",
    nodeId: 4,
    taskId: 3008,
    policyName: "对象存储元数据",
    severity: "warning",
    status: "acked",
    errorCode: "XR-EXEC-923",
    message: "文件句柄异常，已自动回退",
    triggeredAt: formatDate(11),
    retryable: true
  },
  {
    id: "alert-004",
    nodeName: "北京主库-2",
    nodeId: 13,
    taskId: 3016,
    policyName: "核心 MySQL 增量",
    severity: "info",
    status: "resolved",
    errorCode: "XR-INFO-101",
    message: "短时延迟抖动，任务已恢复",
    triggeredAt: formatDate(16),
    retryable: false
  }
];
