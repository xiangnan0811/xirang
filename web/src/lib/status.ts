import i18n from "@/i18n";
import type { AlertSeverity, NodeStatus, TaskStatus } from "@/types/domain";

export function getNodeStatusMeta(status: NodeStatus) {
  switch (status) {
    case "online":
      return { label: i18n.t("status.node.online"), variant: "success" as const };
    case "warning":
      return { label: i18n.t("status.node.warning"), variant: "warning" as const };
    case "offline":
      return { label: i18n.t("status.node.offline"), variant: "destructive" as const };
    default:
      return { label: i18n.t("status.node.unknown"), variant: "neutral" as const };
  }
}

export function getTaskStatusMeta(status: TaskStatus) {
  switch (status) {
    case "running":
      return { label: i18n.t("status.task.running"), variant: "neutral" as const };
    case "pending":
      return { label: i18n.t("status.task.pending"), variant: "neutral" as const };
    case "retrying":
      return { label: i18n.t("status.task.retrying"), variant: "warning" as const };
    case "success":
      return { label: i18n.t("status.task.success"), variant: "success" as const };
    case "failed":
      return { label: i18n.t("status.task.failed"), variant: "destructive" as const };
    case "canceled":
      return { label: i18n.t("status.task.canceled"), variant: "neutral" as const };
    case "warning":
      return { label: i18n.t("status.task.verifyWarning"), variant: "warning" as const };
    default:
      return { label: i18n.t("status.task.unknown"), variant: "neutral" as const };
  }
}

export function getSeverityMeta(severity: AlertSeverity) {
  switch (severity) {
    case "critical":
      return { label: i18n.t("status.alert.critical"), variant: "destructive" as const };
    case "warning":
      return { label: i18n.t("status.alert.warning"), variant: "warning" as const };
    default:
      return { label: i18n.t("status.alert.info"), variant: "neutral" as const };
  }
}
