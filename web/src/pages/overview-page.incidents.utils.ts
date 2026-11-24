import type { useTranslation } from "react-i18next";
import { formatRelativeTime } from "@/lib/date-utils";
import { getLocale } from "@/lib/utils";
import type {
  HealthIncidentAction,
  HealthIncidentGroup,
  HealthIncidentSeverity,
  HealthIncidentTimelineData,
} from "@/types/domain";

export type TFunction = ReturnType<typeof useTranslation>["t"];

export function positiveIncidentID(value: number | undefined): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

export function resolveHealthIncidentAction(group: HealthIncidentGroup): HealthIncidentAction {
  for (const action of group.nextActions) {
    const href = action.href.trim();
    if (action.code === "view_node_metrics" || href.includes("tab=metrics") || /[\\\p{Cc}]/u.test(action.href)) continue;
    if (action.code === "view_backup_confidence" && href === "/app/backups") {
      return { ...action, href: "/app/backups/overview" };
    }
    if (/^\/app\/(?:tasks|policies|notifications|backups\/overview)$/.test(href)) {
      return { ...action, href };
    }
    const target = href.match(/^\/app\/(?:notifications\?alert=alert-(\d+)|logs\?task=(\d+)|nodes\/(\d+)\?tab=alerts)$/);
    if (target && positiveIncidentID(Number(target[1] ?? target[2] ?? target[3]))) {
      return { ...action, href };
    }
  }
  const { type, id } = group.resource;
  if (positiveIncidentID(id)) {
    if (type === "node") return { code: "view_node_alerts", label: "", href: `/app/nodes/${id}?tab=alerts` };
    if (type === "task") return { code: "view_task_logs", label: "", href: `/app/logs?task=${id}` };
    if (type === "policy") return { code: "view_policies", label: "", href: "/app/policies" };
  }
  return { code: "view_notifications", label: "", href: "/app/notifications" };
}

export function healthIncidentCauseSummary(group: HealthIncidentGroup, t: TFunction): string {
  const code = group.causeCode;
  let summary: string;
  switch (code) {
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
      summary = t(`overview.healthIncidentCause.${code}`);
      break;
    default:
      return t("overview.healthIncidentCause.health_attention_required");
  }
  const params = group.causeParams;
  if (code === "notification_delivery_issue") {
    if (params?.status === "failed") return `${summary} ${t("overview.healthIncidentCause.deliveryFailed")}`;
    if (params?.status === "retrying") return `${summary} ${t("overview.healthIncidentCause.deliveryRetrying")}`;
  }
  if (code === "snapshot_churn_anomaly" || code === "snapshot_ransomware_pattern" || code === "anomaly_detected") {
    const observed = params?.observedValue;
    const baseline = params?.baselineValue;
    if (
      typeof observed === "number" &&
      Number.isFinite(observed) &&
      Math.abs(observed) <= Number.MAX_SAFE_INTEGER &&
      typeof baseline === "number" &&
      Number.isFinite(baseline) &&
      Math.abs(baseline) <= Number.MAX_SAFE_INTEGER
    ) {
      return `${summary} ${t("overview.healthIncidentCause.measurements", {
        observed: observed.toFixed(1),
        baseline: baseline.toFixed(1),
      })}`;
    }
  }
  if (
    code === "verified_backup_stale" &&
    typeof params?.elapsedHours === "number" &&
    Number.isSafeInteger(params.elapsedHours) &&
    params.elapsedHours >= 0
  ) {
    return `${summary} ${t("overview.healthIncidentCause.elapsedHours", { hours: params.elapsedHours })}`;
  }
  return summary;
}

export function healthIncidentActionLabel(action: HealthIncidentAction, t: TFunction): string {
  switch (action.code) {
    case "view_alert":
    case "view_task_logs":
    case "view_tasks":
    case "view_node_alerts":
    case "view_backup_confidence":
    case "view_policies":
    case "view_notifications":
      return t(`overview.healthIncidentAction.${action.code}`);
    default:
      return t("overview.healthIncidentPrimaryAction");
  }
}

export function severityTone(severity: HealthIncidentSeverity): "destructive" | "warning" | "info" {
  if (severity === "critical") return "destructive";
  if (severity === "warning") return "warning";
  return "info";
}

export function severityLabel(t: TFunction, severity: HealthIncidentSeverity): string {
  return t(`overview.healthIncidentSeverity.${severity}`);
}

export function resourceLabel(t: TFunction, group: HealthIncidentGroup): string {
  const resourceName = group.resource.name || t("common.unknown");
  const typeLabel = t(`overview.healthIncidentResource.${group.resource.type}`);
  if ((group.resource.type === "task" || group.resource.type === "policy") && group.resource.nodeName) {
    return `${typeLabel} · ${resourceName} / ${group.resource.nodeName}`;
  }
  return `${typeLabel} · ${resourceName}`;
}

export function relativeIncidentTime(value: string): string {
  const locale = getLocale().startsWith("zh") ? "zh" : "en";
  return formatRelativeTime(value, locale);
}

export function normalizeIncidentTimeline(raw: HealthIncidentTimelineData): HealthIncidentTimelineData {
  const groups = Array.isArray(raw.groups) ? raw.groups : [];
  const windowHours =
    typeof raw.windowHours === "number" && Number.isFinite(raw.windowHours) && raw.windowHours > 0
      ? raw.windowHours
      : 72;
  const summary = raw.summary ?? {
    total: groups.length,
    critical: groups.filter((group) => group.severity === "critical").length,
    warning: groups.filter((group) => group.severity === "warning").length,
    info: groups.filter((group) => group.severity === "info").length,
  };
  return {
    generatedAt: typeof raw.generatedAt === "string" ? raw.generatedAt : "",
    windowHours,
    summary,
    groups,
  };
}
