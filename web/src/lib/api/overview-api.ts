import type { BackupHealthData, HealthTrendPoint, HookTemplate, OverviewSummary, OverviewTrafficSeries, OverviewTrafficWindow, StaleNode, StorageUsageData } from "@/types/domain";
import { getLocale } from "@/lib/utils";
import { request, type Envelope, unwrapData } from "./core";

type OverviewSummaryResponse = {
  totalNodes: number;
  healthyNodes: number;
  activePolicies: number;
  runningTasks: number;
  failedTasks24h: number;
};

type OverviewTrafficPointResponse = {
  timestamp: string;
  timestamp_ms: number;
  label: string;
  throughput_mbps: number;
  sample_count: number;
  active_task_count?: number;
  started_count?: number;
  failed_count?: number;
};

type OverviewTrafficSeriesResponse = {
  window: OverviewTrafficWindow;
  bucket_minutes: number;
  has_real_samples: boolean;
  generated_at: string;
  points: OverviewTrafficPointResponse[];
};

function formatOverviewTrafficLabel(timestampMs: number, timestamp: string, window: OverviewTrafficWindow): string {
  const date = Number.isFinite(timestampMs) && timestampMs > 0 ? new Date(timestampMs) : new Date(timestamp);
  if (window === "7d") {
    return date.toLocaleString(getLocale(), { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false });
  }
  return date.toLocaleTimeString(getLocale(), { hour: "2-digit", minute: "2-digit", hour12: false });
}

function mapOverviewSummary(payload?: OverviewSummaryResponse | null): OverviewSummary {
  return {
    totalNodes: Number(payload?.totalNodes || 0),
    healthyNodes: Number(payload?.healthyNodes || 0),
    activePolicies: Number(payload?.activePolicies || 0),
    runningTasks: Number(payload?.runningTasks || 0),
    failedTasks24h: Number(payload?.failedTasks24h || 0),
  };
}

function mapOverviewTraffic(payload?: OverviewTrafficSeriesResponse | null): OverviewTrafficSeries {
  return {
    window: payload?.window ?? "1h",
    bucketMinutes: Number(payload?.bucket_minutes || 5),
    hasRealSamples: Boolean(payload?.has_real_samples),
    generatedAt: payload?.generated_at ?? "",
    points: Array.isArray(payload?.points)
      ? payload.points.map((point) => ({
          timestamp: point.timestamp,
          timestampMs: Number(point.timestamp_ms || 0),
          label: formatOverviewTrafficLabel(Number(point.timestamp_ms || 0), point.timestamp, payload?.window ?? "1h"),
          throughputMbps: Number(point.throughput_mbps || 0),
          sampleCount: Number(point.sample_count || 0),
          activeTaskCount: Number(point.active_task_count || 0),
          startedCount: Number(point.started_count || 0),
          failedCount: Number(point.failed_count || 0),
        }))
      : [],
  };
}

type BackupHealthRaw = {
  stale_nodes?: { id: number; name: string; last_backup_at: string | null }[];
  degraded_policies?: { id: number; name: string; consecutive_failures?: number; last_failed_at?: string }[];
  trend?: { date: string; total: number; success: number }[];
  summary?: { total_nodes: number; total_policies: number; healthy_nodes: number };
};

type StorageUsageRaw = {
  mount_points?: { path: string; used_gb: number; total_gb: number; pct: number }[];
  per_node?: { node_id: number; node_name: string; path: string; used_gb: number }[];
};

function mapBackupHealth(raw: BackupHealthRaw | null | undefined): BackupHealthData {
  const staleNodes = Array.isArray(raw?.stale_nodes)
    ? raw.stale_nodes.map((n) => {
        const lastBackup = n.last_backup_at ? new Date(n.last_backup_at).getTime() : null;
        const hoursSince = lastBackup ? (Date.now() - lastBackup) / 3600000 : Infinity;
        return {
          nodeId: Number(n.id || 0),
          nodeName: String(n.name || ""),
          lastBackupAt: n.last_backup_at ?? null,
          hoursSince: Number.isFinite(hoursSince) ? hoursSince : 0,
        };
      })
    : [];

  const degradedPolicies = Array.isArray(raw?.degraded_policies)
    ? raw.degraded_policies.map((p) => ({
        policyId: Number(p.id || 0),
        policyName: String(p.name || ""),
        consecutiveFailures: Number(p.consecutive_failures ?? 0),
        lastFailedAt: String(p.last_failed_at || ""),
      }))
    : [];

  const healthTrend = Array.isArray(raw?.trend)
    ? raw.trend.map((t) => ({
        date: String(t.date || ""),
        total: Number(t.total || 0),
        success: Number(t.success || 0),
        rate: t.total > 0 ? Math.round((t.success / t.total) * 1000) / 10 : 0,
      }))
    : [];

  const totalNodes = Number(raw?.summary?.total_nodes || 0);
  const totalPolicies = Number(raw?.summary?.total_policies || 0);
  const neverBackedUp = staleNodes.filter((n: StaleNode) => !n.lastBackupAt).length;
  const trendTotals = healthTrend.reduce((acc: { t: number; s: number }, p: HealthTrendPoint) => ({ t: acc.t + p.total, s: acc.s + p.success }), { t: 0, s: 0 });

  return {
    staleNodes,
    degradedPolicies,
    healthTrend,
    summary: {
      totalNodes,
      neverBackedUp,
      stale48h: staleNodes.length - neverBackedUp,
      policiesHealthy: Math.max(0, totalPolicies - degradedPolicies.length),
      policiesDegraded: degradedPolicies.length,
      successRate7d: trendTotals.t > 0 ? Math.round((trendTotals.s / trendTotals.t) * 1000) / 10 : 100,
    },
  };
}

function mapStorageUsage(raw: StorageUsageRaw | null | undefined): StorageUsageData {
  return {
    mountPoints: Array.isArray(raw?.mount_points)
      ? raw.mount_points.map((m) => ({
          path: String(m.path || ""),
          usedGB: Number(m.used_gb || 0),
          totalGB: Number(m.total_gb || 0),
          pct: Number(m.pct || 0),
        }))
      : [],
    perNode: Array.isArray(raw?.per_node)
      ? raw.per_node.map((n) => ({
          nodeId: Number(n.node_id || 0),
          nodeName: String(n.node_name || ""),
          path: String(n.path || ""),
          usedGB: Number(n.used_gb || 0),
        }))
      : [],
  };
}

export function createOverviewApi() {
  return {
    async getOverviewSummary(token: string, options?: { signal?: AbortSignal }): Promise<OverviewSummary> {
      const payload = await request<Envelope<OverviewSummaryResponse>>("/overview", { token, signal: options?.signal });
      return mapOverviewSummary(unwrapData(payload));
    },

    async getOverviewTraffic(token: string, options?: { window?: OverviewTrafficWindow; signal?: AbortSignal }): Promise<OverviewTrafficSeries> {
      const query = new URLSearchParams();
      if (options?.window) {
        query.set("window", options.window);
      }
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const payload = await request<Envelope<OverviewTrafficSeriesResponse>>(`/overview/traffic${suffix}`, { token, signal: options?.signal });
      return mapOverviewTraffic(unwrapData(payload));
    },

    async getBackupHealth(token: string, options?: { signal?: AbortSignal }): Promise<BackupHealthData> {
      const payload = await request<Envelope<BackupHealthRaw>>("/overview/backup-health", { token, signal: options?.signal });
      return mapBackupHealth(unwrapData(payload));
    },

    async getStorageUsage(token: string, options?: { signal?: AbortSignal }): Promise<StorageUsageData> {
      const payload = await request<Envelope<StorageUsageRaw>>("/overview/storage-usage", { token, signal: options?.signal });
      return mapStorageUsage(unwrapData(payload));
    },

    async getHookTemplates(token: string): Promise<HookTemplate[]> {
      const payload = await request<Envelope<HookTemplate[]>>("/hook-templates", { token });
      return unwrapData(payload) ?? [];
    },
  };
}
