import type {
  AlertRecord,
  IntegrationChannel,
  LogEvent,
  NodeRecord,
  OverviewSummary,
  OverviewTrafficSeries,
  OverviewTrafficWindow,
  OverviewStats,
  PolicyRecord,
  SSHKeyRecord,
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
    privateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\\n...demo...\\n-----END OPENSSH PRIVATE KEY-----",
    fingerprint: buildFingerprint(101),
    createdAt: formatDate(60 * 24 * 7),
    lastUsedAt: formatDate(8)
  },
  {
    id: "key-ops-staging",
    name: "ops-staging-ed25519",
    username: "ubuntu",
    keyType: "ed25519",
    privateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\\n...demo...\\n-----END OPENSSH PRIVATE KEY-----",
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
    name: "核心 MySQL 增量",
    sourcePath: "/data/mysql",
    targetPath: "/backup/core/mysql",
    cron: "0 */2 * * *",
    naturalLanguage: "每隔两小时同步一次",
    enabled: true,
    criticalThreshold: 2,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0
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
    verifySampleRate: 0
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
    verifySampleRate: 0
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
    verifySampleRate: 0
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
    verifySampleRate: 0
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
    verifySampleRate: 0
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
    verifySampleRate: 0
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
    verifySampleRate: 0
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
    verifySampleRate: 0
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
    verifySampleRate: 0
  },
  {
    id: 11,
    name: "消息队列快照",
    sourcePath: "/data/kafka",
    targetPath: "/backup/mq/snapshot",
    cron: "30 */2 * * *",
    naturalLanguage: "每 2 小时第 30 分钟执行",
    enabled: true,
    criticalThreshold: 2,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0
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
    verifySampleRate: 0
  }
];

export const mockTasks: TaskRecord[] = Array.from({ length: 18 }, (_, idx) => {
  const id = 3000 + idx + 1;
  const node = mockNodes[idx % mockNodes.length];
  const policy = mockPolicies[idx % mockPolicies.length];
  const status: TaskRecord["status"] =
    idx % 7 === 0
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
    policyName: policy.name,
    nodeName: node.name,
    nodeId: node.id,
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
    startedAt: formatDate(2 + idx * 3),
    errorCode: status === "failed" ? `XR-EXEC-${900 + idx}` : undefined,
    speedMbps: 40 + (idx * 11) % 180,
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
    timestamp: formatDate(1),
    level: "info",
    message: "(node:北京主库-1) sending incremental file list",
    nodeName: "北京主库-1",
    taskId: 3001,
    progress: 17
  },
  {
    id: "log-2",
    logId: 1002,
    timestamp: formatDate(1),
    level: "warn",
    message: "(node:广州归档-1) 网络抖动，进入退避重试",
    nodeName: "广州归档-1",
    taskId: 3007,
    errorCode: "XR-NODE-421",
    progress: 48
  },
  {
    id: "log-3",
    logId: 1003,
    timestamp: formatDate(2),
    level: "error",
    message: "(task:3008) rsync returned code 23, file vanished",
    nodeName: "深圳边缘-1",
    taskId: 3008,
    errorCode: "XR-EXEC-923",
    progress: 52
  },
  {
    id: "log-4",
    logId: 1004,
    timestamp: formatDate(3),
    level: "info",
    message: "(task:3010) sent 1.8GB in 73s, speed 201MB/s",
    nodeName: "杭州对象-1",
    taskId: 3010,
    progress: 100
  },
  {
    id: "log-5",
    logId: 1005,
    timestamp: formatDate(5),
    level: "error",
    message: "(node:天津网关-2) SSH 握手失败 XR-AUTH-011",
    nodeName: "天津网关-2",
    taskId: 3014,
    errorCode: "XR-AUTH-011",
    progress: 0
  }
];

export const mockIntegrations: IntegrationChannel[] = [];

export const mockAlerts: AlertRecord[] = [
  {
    id: "alert-001",
    nodeName: "天津网关-2",
    nodeId: 24,
    taskId: 3014,
    policyName: "消息队列快照",
    severity: "critical",
    status: "open",
    errorCode: "XR-AUTH-011",
    message: "SSH 认证失败，私钥可能过期",
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
