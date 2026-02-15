import type { AlertSeverity, NodeStatus, TaskStatus } from "@/types/domain";

export function getNodeStatusMeta(status: NodeStatus) {
  switch (status) {
    case "online":
      return { label: "在线", variant: "success" as const };
    case "warning":
      return { label: "告警", variant: "warning" as const };
    case "offline":
      return { label: "离线", variant: "danger" as const };
    default:
      return { label: "未知", variant: "outline" as const };
  }
}

export function getTaskStatusMeta(status: TaskStatus) {
  switch (status) {
    case "running":
      return { label: "执行中", variant: "secondary" as const };
    case "pending":
      return { label: "排队中", variant: "outline" as const };
    case "retrying":
      return { label: "重试中", variant: "warning" as const };
    case "success":
      return { label: "成功", variant: "success" as const };
    case "failed":
      return { label: "失败", variant: "danger" as const };
    case "canceled":
      return { label: "已取消", variant: "outline" as const };
    default:
      return { label: "未知", variant: "outline" as const };
  }
}

export function getSeverityMeta(severity: AlertSeverity) {
  switch (severity) {
    case "critical":
      return { label: "严重", variant: "danger" as const };
    case "warning":
      return { label: "警告", variant: "warning" as const };
    default:
      return { label: "信息", variant: "secondary" as const };
  }
}
