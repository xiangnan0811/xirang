import { Link, Navigate, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ClipboardList } from "lucide-react";

import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { parseBackupAssetsRoute } from "@/features/backup-assets/backup-assets-route-state";

export function BackupsRecoveryPage() {
  const { t } = useTranslation();
  const location = useLocation();
  const route = parseBackupAssetsRoute(location.pathname, location.search);

  if (route.status === "invalid") {
    return <Navigate to={route.safePath} replace />;
  }

  const { recoveryPointId, taskId } = route.state;

  return (
    <div className="min-h-[24rem] space-y-4" aria-labelledby="backup-assets-recovery-title">
      <h2 id="backup-assets-recovery-title" className="text-base font-semibold">
        {t("backups.recoveryTitle")}
      </h2>
      <InlineAlert tone="info" title={t("backups.recoveryEvidenceTitle")}>
        {recoveryPointId ? (
          <div className="space-y-1.5">
            <p>{t("backups.recoverySelectedPoint")}</p>
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
              <span>{t("backups.recoveryPointLabel")}</span>
              <code className="font-mono text-foreground" title={recoveryPointId}>
                {shortOpaqueId(recoveryPointId)}
              </code>
              {taskId ? <span>{t("backups.recoveryTaskLabel", { id: taskId })}</span> : null}
            </div>
          </div>
        ) : (
          t("backups.recoveryNoPoint")
        )}
      </InlineAlert>
      <Button variant="outline" asChild>
        <Link to="/app/tasks">
          <ClipboardList className="size-4" aria-hidden />
          {t("backups.taskContext")}
        </Link>
      </Button>
    </div>
  );
}

function shortOpaqueId(value: string): string {
  return `${value.slice(0, 8)}…${value.slice(-8)}`;
}
