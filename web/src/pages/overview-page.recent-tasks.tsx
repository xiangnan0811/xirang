import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { TrendingUp, Clock, AlertTriangle, CheckCircle2 } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { LoadingState } from "@/components/ui/loading-state";
import type { TaskRecord } from "@/types/domain";

export type OverviewRecentTasksProps = {
  tasks: TaskRecord[];
  recentTasks: TaskRecord[];
  loading: boolean;
};

export function OverviewRecentTasks({ tasks, recentTasks, loading }: OverviewRecentTasksProps) {
  const { t } = useTranslation();

  return (
    <section className="animate-slide-up [animation-delay:250ms]">
      <Card className="rounded-lg border border-border bg-card">
        <CardHeader className="flex flex-row items-center justify-between pb-2">
          <CardTitle className="text-base flex items-center gap-2">
            <Clock className="size-4 text-primary" />
            {t("overview.recentTasks")}
          </CardTitle>
          <Link to="/app/tasks" className="inline-flex items-center text-xs px-2 py-1 rounded-md text-muted-foreground hover:text-foreground transition-colors">
            {t("overview.viewMore")} &rarr;
          </Link>
        </CardHeader>
        <CardContent>
          {loading ? (
            <LoadingState className="py-6" rows={3} title={t("overview.recentTasksLoading")} />
          ) : tasks.length === 0 ? (
            <p className="py-6 text-center text-sm text-muted-foreground">{t("overview.noTaskData")}</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead className="border-b border-border text-xs text-muted-foreground uppercase bg-secondary">
                  <tr>
                    <th scope="col" className="px-4 py-2 font-medium">{t("overview.tableNodeName")}</th>
                    <th scope="col" className="px-4 py-2 font-medium">{t("overview.tableTaskName")}</th>
                    <th scope="col" className="px-4 py-2 font-medium">{t("overview.tableSyncStatus")}</th>
                    <th scope="col" className="px-4 py-2 font-medium">{t("overview.tableTransfer")}</th>
                    <th scope="col" className="px-4 py-2 font-medium text-right">{t("overview.tableCompletedAt")}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {recentTasks.map((task) => {
                    let transferData = "-";
                    if (task.speedMbps > 0) {
                      transferData = `≈ ${(task.speedMbps / 8).toFixed(1)} MB/s`;
                    }

                    let StatusIcon = Clock;
                    let statusColor = "text-muted-foreground";
                    let statusLabel = t("overview.taskStatusQueued");

                    switch (task.status) {
                      case "success":
                        StatusIcon = CheckCircle2;
                        statusColor = "text-success";
                        statusLabel = t("overview.taskStatusSuccess");
                        break;
                      case "failed":
                        StatusIcon = AlertTriangle;
                        statusColor = "text-destructive";
                        statusLabel = t("overview.taskStatusFailed");
                        break;
                      case "running":
                        StatusIcon = TrendingUp;
                        statusColor = "text-info";
                        statusLabel = t("overview.taskStatusRunning");
                        break;
                      case "retrying":
                        StatusIcon = AlertTriangle;
                        statusColor = "text-warning";
                        statusLabel = t("overview.taskStatusRetrying");
                        break;
                    }

                    return (
                      <tr key={task.id} className="hover:bg-accent transition-colors">
                        <td className="px-4 py-2.5 font-medium">{task.nodeName}</td>
                        <td className="px-4 py-2.5 text-muted-foreground">{task.name || task.policyName}</td>
                        <td className="px-4 py-2.5">
                          <span className={`inline-flex items-center gap-1.5 ${statusColor}`}>
                            <StatusIcon className="size-3.5" />
                            {statusLabel}
                          </span>
                        </td>
                        <td className="px-4 py-2.5 font-mono text-xs">{transferData}</td>
                        <td className="px-4 py-2.5 text-right text-muted-foreground text-xs">{task.updatedAt || "-"}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
    </section>
  );
}
