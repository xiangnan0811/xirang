import i18n from "@/i18n";
import type { NodeRecord, OverviewStats, OverviewSummary, PolicyRecord, TaskRecord } from "@/types/domain";

export function deriveOverview(
  nodes: NodeRecord[],
  policies: PolicyRecord[],
  tasks: TaskRecord[],
  summary?: OverviewSummary | null
): OverviewStats {
  const localHealthy = nodes.filter((node) => node.status === "online").length;
  const localFailed = tasks.filter((task) => task.status === "failed").length;
  const successCount = tasks.filter((task) => task.status === "success").length;
  const successRate = tasks.length > 0 ? Number(((successCount / tasks.length) * 100).toFixed(1)) : 100;
  const avgSyncMbps = summary?.currentThroughputMbps ?? 0;

  return {
    totalNodes: summary?.totalNodes ?? nodes.length,
    healthyNodes: summary?.healthyNodes ?? localHealthy,
    activePolicies: summary?.activePolicies ?? policies.filter((policy) => policy.enabled).length,
    runningTasks: summary?.runningTasks ?? tasks.filter((task) => task.status === "running" || task.status === "retrying").length,
    failedTasks24h: summary?.failedTasks24h ?? localFailed,
    overallSuccessRate: successRate,
    avgSyncMbps
  };
}

export function describeCron(cron: string) {
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return i18n.t("cron.byCronExpression", { cron });
  }
  const [minute, hour, , , weekday] = parts;
  if (minute.startsWith("*/")) {
    return i18n.t("cron.everyNMinutes", { n: minute.replace("*/", "") });
  }
  if (hour.startsWith("*/")) {
    const interval = hour.replace("*/", "");
    return minute === "0"
      ? i18n.t("cron.everyNHoursOnTheHour", { n: interval })
      : i18n.t("cron.everyNHoursAtMinute", { n: interval, minute });
  }
  if (weekday !== "*") {
    return i18n.t("cron.weeklyDaysAtTime", {
      days: weekday,
      time: `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}`
    });
  }
  return i18n.t("cron.dailyAtTime", { time: `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}` });
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
  return `DEMO:${checksum.toString(16).padStart(6, "0")}`;
}

export function createKeyId(name: string) {
  return `key-${name.toLowerCase().replace(/\s+/g, "-")}-${Date.now().toString(36)}`;
}

export function createIntegrationId(name: string) {
  return `int-${name.toLowerCase().replace(/\s+/g, "-")}-${Date.now().toString(36)}`;
}
