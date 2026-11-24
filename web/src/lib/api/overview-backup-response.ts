import type {
  BackupConfidenceData,
  BackupConfidenceItem,
  BackupConfidenceSeverity,
  BackupConfidenceStatus,
  BackupHealthData,
  HealthTrendPoint,
  StaleNode,
  StorageUsageData,
} from "@/types/domain";
import { finiteNumber, positiveNumberOrUndefined } from "./number-utils";

export type BackupHealthRaw = {
  generated_at?: string;
  stale_threshold_hours?: number;
  latest_verified_backup_at?: string | null;
  stale_nodes?: { id: number; name: string; last_backup_at: string | null }[];
  degraded_policies?: { id: number; name: string; consecutive_failures?: number; last_failed_at?: string }[];
  trend?: { date: string; total: number; success: number }[];
  summary?: { total_nodes: number; total_policies: number; healthy_nodes: number };
};

export type StorageUsageRaw = {
  mount_points?: { path: string; used_gb: number; total_gb: number; pct: number }[];
  per_node?: { node_id: number; node_name: string; path: string; used_gb: number; incomplete?: boolean }[];
  incomplete?: boolean;
  mount_points_incomplete?: boolean;
};

export type BackupConfidenceRaw = {
  generated_at?: string;
  summary?: {
    healthy?: number | string;
    warning?: number | string;
    at_risk?: number | string;
    insufficient?: number | string;
    total?: number | string;
  };
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
  evidence?: {
    type?: string;
    status?: string;
    message?: string;
    observed_at?: string | null;
    task_id?: number | string;
    task_run_id?: number | string;
    alert_id?: number | string;
  }[];
  next_steps?: { code?: string; label?: string }[];
  targets?: { node_id?: number | string; node_name?: string; last_backup_at?: string | null }[];
};

export function mapBackupHealth(raw: BackupHealthRaw | null | undefined): BackupHealthData {
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
  const trendTotals = healthTrend.reduce(
    (acc: { t: number; s: number }, p: HealthTrendPoint) => ({ t: acc.t + p.total, s: acc.s + p.success }),
    { t: 0, s: 0 },
  );

  const staleThresholdHours = finiteNumber(raw?.stale_threshold_hours);
  const latestVerifiedBackupAt =
    typeof raw?.latest_verified_backup_at === "string" && raw.latest_verified_backup_at.trim()
      ? raw.latest_verified_backup_at
      : raw?.latest_verified_backup_at === null
        ? null
        : undefined;

  return {
    ...(typeof raw?.generated_at === "string" && raw.generated_at ? { generatedAt: raw.generated_at } : {}),
    ...(staleThresholdHours > 0 ? { staleThresholdHours: Math.round(staleThresholdHours) } : {}),
    ...(latestVerifiedBackupAt !== undefined ? { latestVerifiedBackupAt } : {}),
    staleNodes,
    degradedPolicies,
    healthTrend,
    summary: {
      totalNodes,
      neverBackedUp,
      stale48h: staleNodes.length - neverBackedUp,
      policiesHealthy: Math.max(0, totalPolicies - degradedPolicies.length),
      policiesDegraded: degradedPolicies.length,
      successRate7d: trendTotals.t > 0 ? Math.round((trendTotals.s / trendTotals.t) * 1000) / 10 : null,
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

export function mapBackupConfidence(raw: BackupConfidenceRaw | null | undefined): BackupConfidenceData {
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

export function mapStorageUsage(raw: StorageUsageRaw | null | undefined): StorageUsageData {
  const mountPoints = Array.isArray(raw?.mount_points)
    ? raw.mount_points.map((m) => ({
        path: String(m.path || ""),
        usedGB: Number(m.used_gb || 0),
        totalGB: Number(m.total_gb || 0),
        pct: Number(m.pct || 0),
      }))
    : [];
  const perNode = Array.isArray(raw?.per_node)
    ? raw.per_node.map((n) => ({
        nodeId: Number(n.node_id || 0),
        nodeName: String(n.node_name || ""),
        path: String(n.path || ""),
        usedGB: Number(n.used_gb || 0),
        ...(n.incomplete === true ? { incomplete: true } : {}),
      }))
    : [];
  const mountPointsIncomplete = raw?.mount_points_incomplete === true;
  const incomplete =
    raw?.incomplete === true ||
    mountPointsIncomplete ||
    perNode.some((entry) => entry.incomplete === true);

  return {
    mountPoints,
    perNode,
    ...(incomplete ? { incomplete: true } : {}),
    ...(mountPointsIncomplete ? { mountPointsIncomplete: true } : {}),
  };
}
