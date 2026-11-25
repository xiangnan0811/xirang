import type {
  BackupConfidenceData,
  BackupHealthData,
  HealthIncidentTimelineData,
  OverviewSummary,
  OverviewTrafficSeries,
  OverviewTrafficWindow,
  StorageUsageData,
} from "@/types/domain";
import { getLocale } from "@/lib/utils";
import { request } from "./core";
import type { HealthIncidentTimelineRaw } from "./health-incident-response";
import type { BackupConfidenceRaw, BackupHealthRaw, StorageUsageRaw } from "./overview-backup-response";
import { finiteNumber } from "./number-utils";

type OverviewSummaryResponse = {
  activePolicies?: number;
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
  partial?: boolean;
  incomplete?: boolean;
  samples_incomplete?: boolean;
  started_incomplete?: boolean;
  failed_incomplete?: boolean;
};

type OverviewTrafficSeriesResponse = {
  window: OverviewTrafficWindow;
  bucket_minutes: number;
  has_real_samples: boolean;
  truncated?: boolean;
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
    activePolicies: finiteNumber(payload?.activePolicies),
  };
}

function mapOverviewTraffic(payload?: OverviewTrafficSeriesResponse | null): OverviewTrafficSeries {
  const bucketMinutes = finiteNumber(payload?.bucket_minutes);
  return {
    window: payload?.window ?? "1h",
    bucketMinutes: bucketMinutes > 0 ? bucketMinutes : 5,
    hasRealSamples: Boolean(payload?.has_real_samples),
    truncated: Boolean(payload?.truncated),
    generatedAt: payload?.generated_at ?? "",
    points: Array.isArray(payload?.points)
      ? payload.points.map((point) => {
          const timestampMs = finiteNumber(point.timestamp_ms);
          return {
            timestamp: point.timestamp,
            timestampMs,
            label: formatOverviewTrafficLabel(timestampMs, point.timestamp, payload?.window ?? "1h"),
            throughputMbps: finiteNumber(point.throughput_mbps),
            sampleCount: finiteNumber(point.sample_count),
            activeTaskCount: finiteNumber(point.active_task_count),
            startedCount: finiteNumber(point.started_count),
            failedCount: finiteNumber(point.failed_count),
            ...(point.partial === true ? { partial: true } : {}),
            ...(point.incomplete === true ? { incomplete: true } : {}),
            ...(point.samples_incomplete === true ? { samplesIncomplete: true as const } : {}),
            ...(point.started_incomplete === true ? { startedIncomplete: true as const } : {}),
            ...(point.failed_incomplete === true ? { failedIncomplete: true as const } : {}),
          };
        })
      : [],
  };
}

export function createOverviewApi() {
  return {
    async getOverviewSummary(token: string, options?: { signal?: AbortSignal }): Promise<OverviewSummary> {
      const payload = await request<OverviewSummaryResponse>("/overview", { token, signal: options?.signal });
      return mapOverviewSummary(payload);
    },

    async getOverviewTraffic(token: string, options?: { window?: OverviewTrafficWindow; signal?: AbortSignal }): Promise<OverviewTrafficSeries> {
      const query = new URLSearchParams();
      if (options?.window) {
        query.set("window", options.window);
      }
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const payload = await request<OverviewTrafficSeriesResponse>(`/overview/traffic${suffix}`, { token, signal: options?.signal });
      return mapOverviewTraffic(payload);
    },

    async getHealthIncidentTimeline(token: string, options?: { windowHours?: number; signal?: AbortSignal }): Promise<HealthIncidentTimelineData> {
      const query = new URLSearchParams();
      if (options?.windowHours) {
        query.set("window_hours", String(options.windowHours));
      }
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const payload = await request<HealthIncidentTimelineRaw>(`/overview/health-incident-timeline${suffix}`, { token, signal: options?.signal });
      // Defer this mapper out of the startup bundle without delaying request/auth handling.
      const { mapHealthIncidentTimeline } = await import("./health-incident-response");
      return mapHealthIncidentTimeline(payload);
    },

    async getBackupHealth(token: string, options?: { signal?: AbortSignal }): Promise<BackupHealthData> {
      const payload = await request<BackupHealthRaw>("/overview/backup-health", { token, signal: options?.signal });
      const { mapBackupHealth } = await import("./overview-backup-response");
      return mapBackupHealth(payload);
    },

    async getBackupConfidence(token: string, options?: { signal?: AbortSignal }): Promise<BackupConfidenceData> {
      const payload = await request<BackupConfidenceRaw>("/overview/backup-confidence", { token, signal: options?.signal });
      const { mapBackupConfidence } = await import("./overview-backup-response");
      return mapBackupConfidence(payload);
    },

    async getStorageUsage(token: string, options?: { signal?: AbortSignal }): Promise<StorageUsageData> {
      const payload = await request<StorageUsageRaw>("/overview/storage-usage", { token, signal: options?.signal });
      const { mapStorageUsage } = await import("./overview-backup-response");
      return mapStorageUsage(payload);
    },
  };
}
