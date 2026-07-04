import type { BackupConfidenceData, BackupConfidenceItem, BackupConfidenceSeverity, BackupConfidenceStatus, BackupHealthData, HealthIncidentAction, HealthIncidentGroup, HealthIncidentResource, HealthIncidentResourceType, HealthIncidentSeverity, HealthIncidentSignal, HealthIncidentSourceType, HealthIncidentTimelineData, HealthTrendPoint, HookTemplate, OverviewSummary, OverviewTrafficSeries, OverviewTrafficWindow, StaleNode, StorageUsageData } from "@/types/domain";
import { getLocale } from "@/lib/utils";
import { request } from "./core";
import { finiteNumber, positiveNumberOrUndefined } from "./number-utils";

type OverviewSummaryResponse = {
  totalNodes: number;
  healthyNodes: number;
  activePolicies: number;
  runningTasks: number;
  failedTasks24h: number;
  currentThroughputMbps?: number;
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
    currentThroughputMbps: Number(payload?.currentThroughputMbps ?? 0),
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

type HealthIncidentResourceRaw = {
  type?: string;
  id?: number | string;
  name?: string;
  node_id?: number | string;
  node_name?: string;
  policy_id?: number | string;
  policy_name?: string;
};

type HealthIncidentActionRaw = {
  code?: string;
  label?: string;
  href?: string;
};

type HealthIncidentSignalRaw = {
  type?: string;
  severity?: string;
  occurred_at?: string;
  message?: string;
  alert_id?: number | string;
  delivery_id?: number | string;
  task_id?: number | string;
  task_run_id?: number | string;
  node_id?: number | string;
  policy_id?: number | string;
};

type HealthIncidentGroupRaw = {
  id?: string;
  severity?: string;
  resource?: HealthIncidentResourceRaw;
  last_seen_at?: string;
  event_count?: number | string;
  likely_cause?: string;
  source_types?: string[];
  next_actions?: HealthIncidentActionRaw[];
  signals?: HealthIncidentSignalRaw[];
};

type HealthIncidentTimelineRaw = {
  generated_at?: string;
  window_hours?: number | string;
  summary?: {
    total?: number | string;
    critical?: number | string;
    warning?: number | string;
    info?: number | string;
  };
  groups?: HealthIncidentGroupRaw[];
};

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

type BackupConfidenceRaw = {
  generated_at?: string;
  summary?: { healthy?: number | string; warning?: number | string; at_risk?: number | string; insufficient?: number | string; total?: number | string };
  items?: BackupConfidenceItemRaw[];
};

type BackupConfidenceItemRaw = {
  id?: string;
  scope?: string;
  policy_id?: number | string;
  policy_name?: string;
  node_id?: number | string;
  node_name?: string;
  status?: string;
  score?: number | string;
  reasons?: { code?: string; severity?: string; message?: string }[];
  evidence?: { type?: string; status?: string; message?: string; observed_at?: string | null; task_id?: number | string; task_run_id?: number | string; alert_id?: number | string }[];
  next_steps?: { code?: string; label?: string }[];
  targets?: { node_id?: number | string; node_name?: string; last_backup_at?: string | null }[];
};

function mapHealthIncidentSeverity(raw?: string): HealthIncidentSeverity {
  switch (raw) {
    case "critical":
    case "warning":
    case "info":
      return raw;
    default:
      return "warning";
  }
}

function mapHealthIncidentResourceType(raw?: string): HealthIncidentResourceType {
  switch (raw) {
    case "node":
    case "task":
    case "policy":
    case "platform":
      return raw;
    default:
      return "platform";
  }
}

function mapHealthIncidentSourceType(raw?: string): HealthIncidentSourceType {
  switch (raw) {
    case "alert":
    case "task_failure":
    case "notification_failure":
    case "anomaly":
    case "probe":
    case "metric":
    case "backup_stale":
    case "backup_degraded":
      return raw;
    default:
      return "alert";
  }
}

function mapHealthIncidentResource(raw?: HealthIncidentResourceRaw | null): HealthIncidentResource {
  const type = mapHealthIncidentResourceType(raw?.type);
  const id = positiveNumberOrUndefined(raw?.id);
  return {
    type,
    id,
    name: String(raw?.name || (type === "platform" ? "platform" : "")),
    nodeId: positiveNumberOrUndefined(raw?.node_id),
    nodeName: raw?.node_name ? String(raw.node_name) : undefined,
    policyId: positiveNumberOrUndefined(raw?.policy_id),
    policyName: raw?.policy_name ? String(raw.policy_name) : undefined,
  };
}

function mapHealthIncidentAction(raw: HealthIncidentActionRaw): HealthIncidentAction {
  return {
    code: String(raw.code || "unknown"),
    label: String(raw.label || raw.code || ""),
    href: String(raw.href || ""),
  };
}

function mapHealthIncidentSignal(raw: HealthIncidentSignalRaw): HealthIncidentSignal {
  return {
    type: mapHealthIncidentSourceType(raw.type),
    severity: mapHealthIncidentSeverity(raw.severity),
    occurredAt: String(raw.occurred_at || ""),
    message: String(raw.message || ""),
    alertId: positiveNumberOrUndefined(raw.alert_id),
    deliveryId: positiveNumberOrUndefined(raw.delivery_id),
    taskId: positiveNumberOrUndefined(raw.task_id),
    taskRunId: positiveNumberOrUndefined(raw.task_run_id),
    nodeId: positiveNumberOrUndefined(raw.node_id),
    policyId: positiveNumberOrUndefined(raw.policy_id),
  };
}

function mapHealthIncidentGroup(raw: HealthIncidentGroupRaw): HealthIncidentGroup {
  return {
    id: String(raw.id || "incident-unknown"),
    severity: mapHealthIncidentSeverity(raw.severity),
    resource: mapHealthIncidentResource(raw.resource),
    lastSeenAt: String(raw.last_seen_at || ""),
    eventCount: finiteNumber(raw.event_count),
    likelyCause: String(raw.likely_cause || ""),
    sourceTypes: Array.isArray(raw.source_types) ? raw.source_types.map(mapHealthIncidentSourceType) : [],
    nextActions: Array.isArray(raw.next_actions) ? raw.next_actions.map(mapHealthIncidentAction).filter((action) => action.href) : [],
    signals: Array.isArray(raw.signals) ? raw.signals.map(mapHealthIncidentSignal) : [],
  };
}

function mapHealthIncidentTimeline(raw: HealthIncidentTimelineRaw | null | undefined): HealthIncidentTimelineData {
  return {
    generatedAt: raw?.generated_at ?? "",
    windowHours: finiteNumber(raw?.window_hours),
    summary: {
      total: finiteNumber(raw?.summary?.total),
      critical: finiteNumber(raw?.summary?.critical),
      warning: finiteNumber(raw?.summary?.warning),
      info: finiteNumber(raw?.summary?.info),
    },
    groups: Array.isArray(raw?.groups) ? raw.groups.map(mapHealthIncidentGroup) : [],
  };
}

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

function mapConfidenceStatus(raw?: string): BackupConfidenceStatus {
  switch (raw) {
    case "healthy":
    case "warning":
    case "at_risk":
    case "insufficient":
      return raw;
    default:
      return "insufficient";
  }
}

function mapConfidenceSeverity(raw?: string): BackupConfidenceSeverity {
  switch (raw) {
    case "info":
    case "warning":
    case "critical":
      return raw;
    default:
      return "info";
  }
}

function mapBackupConfidenceItem(raw: BackupConfidenceItemRaw): BackupConfidenceItem {
  return {
    id: String(raw.id || `policy-${raw.policy_id ?? raw.node_id ?? "unknown"}`),
    scope: raw.scope === "node" ? "node" : "policy",
    policyId: positiveNumberOrUndefined(raw.policy_id),
    policyName: raw.policy_name || undefined,
    nodeId: positiveNumberOrUndefined(raw.node_id),
    nodeName: raw.node_name || undefined,
    status: mapConfidenceStatus(raw.status),
    score: finiteNumber(raw.score),
    reasons: Array.isArray(raw.reasons)
      ? raw.reasons.map((reason) => ({
          code: String(reason.code || "unknown"),
          severity: mapConfidenceSeverity(reason.severity),
          message: String(reason.message || ""),
        }))
      : [],
    evidence: Array.isArray(raw.evidence)
      ? raw.evidence.map((evidence) => ({
          type: String(evidence.type || "unknown"),
          status: String(evidence.status || "unknown"),
          message: String(evidence.message || ""),
          observedAt: evidence.observed_at || undefined,
          taskId: positiveNumberOrUndefined(evidence.task_id),
          taskRunId: positiveNumberOrUndefined(evidence.task_run_id),
          alertId: positiveNumberOrUndefined(evidence.alert_id),
        }))
      : [],
    nextSteps: Array.isArray(raw.next_steps)
      ? raw.next_steps.map((step) => ({
          code: String(step.code || "unknown"),
          label: String(step.label || ""),
        }))
      : [],
    targets: Array.isArray(raw.targets)
      ? raw.targets.map((target) => ({
          nodeId: finiteNumber(target.node_id),
          nodeName: String(target.node_name || ""),
          lastBackupAt: target.last_backup_at || undefined,
        }))
      : [],
  };
}

function mapBackupConfidence(raw: BackupConfidenceRaw | null | undefined): BackupConfidenceData {
  return {
    generatedAt: raw?.generated_at ?? "",
    summary: {
      healthy: finiteNumber(raw?.summary?.healthy),
      warning: finiteNumber(raw?.summary?.warning),
      atRisk: finiteNumber(raw?.summary?.at_risk),
      insufficient: finiteNumber(raw?.summary?.insufficient),
      total: finiteNumber(raw?.summary?.total),
    },
    items: Array.isArray(raw?.items) ? raw.items.map(mapBackupConfidenceItem) : [],
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
      return mapHealthIncidentTimeline(payload);
    },

    async getBackupHealth(token: string, options?: { signal?: AbortSignal }): Promise<BackupHealthData> {
      const payload = await request<BackupHealthRaw>("/overview/backup-health", { token, signal: options?.signal });
      return mapBackupHealth(payload);
    },

    async getBackupConfidence(token: string, options?: { signal?: AbortSignal }): Promise<BackupConfidenceData> {
      const payload = await request<BackupConfidenceRaw>("/overview/backup-confidence", { token, signal: options?.signal });
      return mapBackupConfidence(payload);
    },

    async getStorageUsage(token: string, options?: { signal?: AbortSignal }): Promise<StorageUsageData> {
      const payload = await request<StorageUsageRaw>("/overview/storage-usage", { token, signal: options?.signal });
      return mapStorageUsage(payload);
    },

    async getHookTemplates(token: string): Promise<HookTemplate[]> {
      return (await request<HookTemplate[]>("/hook-templates", { token })) ?? [];
    },
  };
}
