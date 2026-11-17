import { Mail, MessageSquare, Send, Webhook } from "lucide-react";
import i18n from "@/i18n";
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
      return { label: i18n.t("notifications.alertStatusOpen"), variant: "destructive" as const };
    case "acked":
      return { label: i18n.t("notifications.alertStatusAcked"), variant: "warning" as const };
    default:
      return { label: i18n.t("notifications.alertStatusResolved"), variant: "success" as const };
  }
}

