import type { HealthIncidentAction, HealthIncidentCauseCode, HealthIncidentCauseParams, HealthIncidentGroup, HealthIncidentResource, HealthIncidentResourceType, HealthIncidentSeverity, HealthIncidentSignal, HealthIncidentSourceType, HealthIncidentTimelineData } from "@/types/domain";
import { finiteNumber, positiveNumberOrUndefined } from "./number-utils";

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
  cause_code?: unknown;
  cause_params?: unknown;
  source_types?: string[];
  next_actions?: HealthIncidentActionRaw[];
  signals?: HealthIncidentSignalRaw[];
};

export type HealthIncidentTimelineRaw = {
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

function mapHealthIncidentSourceType(raw?: string): HealthIncidentSourceType | null {
  switch (raw) {
    case "alert":
    case "task_failure":
    case "notification_failure":
    case "anomaly":
    case "backup_stale":
    case "backup_degraded":
      return raw;
    case "probe":
    case "metric":
      return null;
    default:
      return "alert";
  }
}

function mapHealthIncidentCauseCode(raw: unknown): HealthIncidentCauseCode | undefined {
  switch (raw) {
    case "alert_triggered":
    case "task_execution_failed":
    case "notification_delivery_issue":
    case "snapshot_churn_anomaly":
    case "snapshot_ransomware_pattern":
    case "anomaly_detected":
    case "verified_backup_missing":
    case "verified_backup_stale":
    case "policy_backup_failures":
    case "health_attention_required":
      return raw;
    default:
      return undefined;
  }
}

function isHealthIncidentMeasurement(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && Math.abs(value) <= Number.MAX_SAFE_INTEGER;
}

function mapHealthIncidentCauseParams(code: HealthIncidentCauseCode | undefined, raw: unknown): HealthIncidentCauseParams | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  if (code === "notification_delivery_issue" && "status" in raw && (raw.status === "failed" || raw.status === "retrying")) {
    return { status: raw.status };
  }
  if (code === "verified_backup_stale" && "elapsed_hours" in raw &&
      typeof raw.elapsed_hours === "number" && Number.isSafeInteger(raw.elapsed_hours) && raw.elapsed_hours >= 0) {
    return { elapsedHours: raw.elapsed_hours };
  }
  if (code === "snapshot_churn_anomaly" || code === "snapshot_ransomware_pattern" || code === "anomaly_detected") {
    const observedValue = "observed_value" in raw && isHealthIncidentMeasurement(raw.observed_value) ? raw.observed_value : undefined;
    const baselineValue = "baseline_value" in raw && isHealthIncidentMeasurement(raw.baseline_value) ? raw.baseline_value : undefined;
    if (observedValue !== undefined || baselineValue !== undefined) {
      return {
        ...(observedValue !== undefined ? { observedValue } : {}),
        ...(baselineValue !== undefined ? { baselineValue } : {}),
      };
    }
  }
  return undefined;
}

function isRetiredHealthIncidentAction(code: string, href: string): boolean {
  return code === "view_node_metrics" || href.includes("tab=metrics");
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

function mapHealthIncidentAction(raw: HealthIncidentActionRaw): HealthIncidentAction | null {
  const href = String(raw.href || "");
  const code = String(raw.code || "unknown");
  if (!href || isRetiredHealthIncidentAction(code, href)) return null;
  return {
    code,
    label: String(raw.label || raw.code || ""),
    href,
  };
}

function mapHealthIncidentSignal(raw: HealthIncidentSignalRaw): HealthIncidentSignal | null {
  const type = mapHealthIncidentSourceType(raw.type);
  if (!type) return null;
  return {
    type,
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

function mapHealthIncidentGroup(raw: HealthIncidentGroupRaw): HealthIncidentGroup | null {
  const sourceTypes = Array.isArray(raw.source_types)
    ? raw.source_types.map(mapHealthIncidentSourceType).filter((source): source is HealthIncidentSourceType => source != null)
    : [];
  const signals = Array.isArray(raw.signals)
    ? raw.signals.map(mapHealthIncidentSignal).filter((signal): signal is HealthIncidentSignal => signal != null)
    : [];
  if (sourceTypes.length === 0 && signals.length === 0) return null;
  const causeCode = mapHealthIncidentCauseCode(raw.cause_code);
  return {
    id: String(raw.id || "incident-unknown"),
    severity: mapHealthIncidentSeverity(raw.severity),
    resource: mapHealthIncidentResource(raw.resource),
    lastSeenAt: String(raw.last_seen_at || ""),
    eventCount: finiteNumber(raw.event_count),
    likelyCause: String(raw.likely_cause || ""),
    causeCode,
    causeParams: mapHealthIncidentCauseParams(causeCode, raw.cause_params),
    sourceTypes,
    nextActions: Array.isArray(raw.next_actions)
      ? raw.next_actions.map(mapHealthIncidentAction).filter((action): action is HealthIncidentAction => action != null)
      : [],
    signals,
  };
}

export function mapHealthIncidentTimeline(raw: HealthIncidentTimelineRaw | null | undefined): HealthIncidentTimelineData {
  return {
    generatedAt: raw?.generated_at ?? "",
    windowHours: finiteNumber(raw?.window_hours),
    summary: {
      total: finiteNumber(raw?.summary?.total),
      critical: finiteNumber(raw?.summary?.critical),
      warning: finiteNumber(raw?.summary?.warning),
      info: finiteNumber(raw?.summary?.info),
    },
    groups: Array.isArray(raw?.groups)
      ? raw.groups.map(mapHealthIncidentGroup).filter((group): group is HealthIncidentGroup => group != null)
      : [],
  };
}
