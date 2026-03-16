import type { BackupHealthData, HookTemplate, OverviewSummary, OverviewTrafficSeries, OverviewTrafficWindow, StorageUsageData } from "@/types/domain";
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
    return date.toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false });
  }
  return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });
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
      const payload = await request<Envelope<BackupHealthData>>("/overview/backup-health", { token, signal: options?.signal });
      return unwrapData(payload);
    },

    async getStorageUsage(token: string, options?: { signal?: AbortSignal }): Promise<StorageUsageData> {
      const payload = await request<Envelope<StorageUsageData>>("/overview/storage-usage", { token, signal: options?.signal });
      return unwrapData(payload);
    },

    async getHookTemplates(token: string): Promise<HookTemplate[]> {
      const payload = await request<Envelope<HookTemplate[]>>("/hook-templates", { token });
      return unwrapData(payload) ?? [];
    },
  };
}
