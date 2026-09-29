import { Bell, ListChecks } from "lucide-react";
import { useTranslation } from "react-i18next";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import type { OverviewTabProps } from "./types";

export default function OverviewTab({ summary, summaryError, summaryLoading }: OverviewTabProps) {
  const { t } = useTranslation();
  const ready = !summaryError && !summaryLoading && summary != null;
  const openAlerts = summary?.openAlerts ?? 0;
  const runningTasks = summary?.runningTasks ?? 0;

  return (
    <div className="flex flex-col gap-4" data-testid="overview-tab">
      {summaryLoading ? (
        <p className="text-sm text-muted-foreground">{t("nodes.nodeDetail.loading")}</p>
      ) : null}
      {summaryError ? (
        <p className="text-sm text-destructive" role="alert">
          {t("nodes.nodeDetail.loadFailed")}
        </p>
      ) : null}
      <StatCardsSection
        className="xl:grid-cols-2"
        items={[
          {
            id: "open-alerts",
            title: t("nodes.nodeDetail.openAlertsTitle"),
            value: ready ? openAlerts : "—",
            description: ready
              ? (openAlerts === 0 ? t("nodes.nodeDetail.openAlertsEmpty") : t("nodes.nodeDetail.openAlertsHint"))
              : "—",
            tone: !ready ? "info" : openAlerts > 0 ? "warning" : "success",
            icon: <Bell className="size-4" aria-hidden />,
          },
          {
            id: "running-tasks",
            title: t("nodes.nodeDetail.runningTasksTitle"),
            value: ready ? runningTasks : "—",
            description: ready
              ? (runningTasks === 0 ? t("nodes.nodeDetail.runningTasksEmpty") : t("nodes.nodeDetail.runningTasksHint"))
              : "—",
            tone: !ready ? "info" : runningTasks > 0 ? "primary" : "info",
            icon: <ListChecks className="size-4" aria-hidden />,
          },
        ]}
      />
    </div>
  );
}
