import { Mail, MessageSquare, Send, Webhook } from "lucide-react";
import type { AlertRecord, IntegrationChannel } from "@/types/domain";

export function integrationIcon(type: IntegrationChannel["type"]) {
  switch (type) {
    case "email":
      return Mail;
    case "slack":
      return MessageSquare;
    case "telegram":
      return Send;
    default:
      return Webhook;
  }
}

export function alertStatusMeta(status: AlertRecord["status"]) {
  switch (status) {
    case "open":
      return { label: "待处理", variant: "danger" as const };
    case "acked":
      return { label: "已确认", variant: "warning" as const };
    default:
      return { label: "已恢复", variant: "success" as const };
  }
}

export function severityWeight(severity: AlertRecord["severity"]) {
  switch (severity) {
    case "critical":
      return 3;
    case "warning":
      return 2;
    default:
      return 1;
  }
}

export function statusWeight(status: AlertRecord["status"]) {
  switch (status) {
    case "open":
      return 3;
    case "acked":
      return 2;
    default:
      return 1;
  }
}

export function severityToTone(severity: AlertRecord["severity"]) {
  if (severity === "critical") {
    return "offline" as const;
  }
  if (severity === "warning") {
    return "warning" as const;
  }
  return "online" as const;
}

