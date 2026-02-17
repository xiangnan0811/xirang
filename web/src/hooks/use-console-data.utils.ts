import type { NodeRecord, OverviewStats, PolicyRecord, TaskRecord } from "@/types/domain";

export function deriveOverview(nodes: NodeRecord[], policies: PolicyRecord[], tasks: TaskRecord[]): OverviewStats {
  const healthy = nodes.filter((node) => node.status === "online").length;
  const failed = tasks.filter((task) => task.status === "failed").length;
  const successCount = tasks.filter((task) => task.status === "success").length;
  const successRate = tasks.length > 0 ? Number(((successCount / tasks.length) * 100).toFixed(1)) : 100;
  const avgSyncMbps =
    tasks.length > 0
      ? Math.round(tasks.reduce((sum, task) => sum + task.speedMbps, 0) / tasks.length)
      : 0;

  return {
    totalNodes: nodes.length,
    healthyNodes: healthy,
    activePolicies: policies.filter((policy) => policy.enabled).length,
    runningTasks: tasks.filter((task) => task.status === "running" || task.status === "retrying").length,
    failedTasks24h: failed,
    overallSuccessRate: successRate,
    avgSyncMbps
  };
}

export function describeCron(cron: string) {
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return `按表达式 ${cron} 调度`;
  }
  const [minute, hour, , , weekday] = parts;
  if (minute.startsWith("*/")) {
    return `每 ${minute.replace("*/", "")} 分钟执行`;
  }
  if (hour.startsWith("*/")) {
    const interval = hour.replace("*/", "");
    return minute === "0" ? `每 ${interval} 小时整点执行` : `每 ${interval} 小时第 ${minute} 分执行`;
  }
  if (weekday !== "*") {
    return `每周 ${weekday} ${hour.padStart(2, "0")}:${minute.padStart(2, "0")} 执行`;
  }
  return `每天 ${hour.padStart(2, "0")}:${minute.padStart(2, "0")} 执行`;
}

export function parseTags(raw: string) {
  return raw
    .split(",")
    .map((tag) => tag.trim())
    .filter(Boolean);
}

export function buildFingerprint(privateKey: string) {
  const raw = privateKey.trim();
  let checksum = 0;
  for (let idx = 0; idx < raw.length; idx += 1) {
    checksum = (checksum + raw.charCodeAt(idx) * (idx + 3)) % 1_000_000;
  }
  return `SHA256:${checksum.toString(16).padStart(6, "0")}`;
}

export function createKeyId(name: string) {
  return `key-${name.toLowerCase().replace(/\s+/g, "-")}-${Date.now().toString(36)}`;
}

export function createIntegrationId(name: string) {
  return `int-${name.toLowerCase().replace(/\s+/g, "-")}-${Date.now().toString(36)}`;
}
