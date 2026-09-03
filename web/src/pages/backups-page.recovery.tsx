import { useCallback } from "react";
import { Link, Navigate, useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ClipboardList } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DataSurface,
  DataSurfaceContent,
  DataSurfaceHeader,
} from "@/components/ui/data-surface";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Reveal, Stagger } from "@/components/ui/reveal";
import { useAuth } from "@/context/auth-context.hooks";
import {
  backupAssetsTaskContextHref,
  defaultBackupAssetsRouteState,
  parseBackupAssetsRoute,
  serializeBackupAssetsRoute,
  type BackupAssetsRouteState,
} from "@/features/backup-assets/backup-assets-route-state";
import { RecoveryPlanWizard } from "@/features/backup-assets/recovery-plan-wizard";
import { useBackupRecovery } from "@/features/backup-assets/use-backup-recovery";
import { normalizeAppPathname } from "@/lib/backup-navigation";

export function BackupsRecoveryPage() {
  const location = useLocation();
  if (normalizeAppPathname(location.pathname) !== "/app/backups/recovery") {
    return null;
  }

  const route = parseBackupAssetsRoute(location.pathname, location.search);

  if (route.status === "invalid") {
    return <Navigate to={route.safePath} replace />;
  }

  return <BackupsRecoveryContent route={route.state} />;
}

function BackupsRecoveryContent({ route }: { route: BackupAssetsRouteState }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { token, role, userId, ensureStepUpProof } = useAuth();
  const routeToRecovery = useCallback((planId: string | null, jobId: string | null, replace: boolean) => {
    navigate(serializeBackupAssetsRoute({
      ...defaultBackupAssetsRouteState("recovery"),
      taskId: route.taskId,
      recoveryPointId: route.recoveryPointId,
      inspectorTab: route.inspectorTab,
      planId: planId ?? undefined,
      jobId: jobId ?? undefined,
    }), { replace });
  }, [navigate, route.inspectorTab, route.recoveryPointId, route.taskId]);
  const handleDownloadTicket = useCallback((contentUrl: string) => {
    window.location.assign(contentUrl);
  }, []);
  const handleRouteChange = useCallback((
    handles: { planId: string | null; jobId: string | null },
    options: { replace: boolean },
  ) => routeToRecovery(handles.planId, handles.jobId, options.replace), [routeToRecovery]);
  const handleTicket = useCallback(
    (ticket: { contentUrl: string }) => handleDownloadTicket(ticket.contentUrl),
    [handleDownloadTicket],
  );
  const recovery = useBackupRecovery({
    token,
    role,
    sessionKey: userId === null || userId === undefined ? null : String(userId),
    contextKey: JSON.stringify([route.recoveryPointId ?? "", route.taskId ?? ""]),
    planId: route.planId,
    jobId: route.jobId,
    ensureStepUpProof,
    onRouteChange: handleRouteChange,
    onDownloadTicket: handleTicket,
  });

  if (route.planId !== undefined && (token === null || role !== "admin")) {
    return (
      <Stagger>
        <Reveal>
          <DataSurface aria-labelledby="backup-assets-recovery-title">
            <DataSurfaceHeader
              title={<span id="backup-assets-recovery-title">{t("backups.recoveryTitle")}</span>}
              headingLevel="h2"
            />
            <DataSurfaceContent>
              <InlineAlert tone="critical" live={false} title={t("backupAssets.recovery.unavailable.title")}>
                {t("backupAssets.recovery.unavailable.body")}
              </InlineAlert>
            </DataSurfaceContent>
          </DataSurface>
        </Reveal>
      </Stagger>
    );
  }

  if (route.planId !== undefined) {
    return (
      <div className="min-h-[24rem]" aria-labelledby="backup-assets-recovery-title">
        <h2 id="backup-assets-recovery-title" className="sr-only">{t("backups.recoveryTitle")}</h2>
        <RecoveryPlanWizard
          open
          recovery={recovery}
          authRole={role}
          onOpenChange={(open) => {
            if (open) return;
            routeToRecovery(null, null, true);
          }}
        />
      </div>
    );
  }

  const { recoveryPointId, taskId } = route;

  return (
    <Stagger>
      <Reveal>
        <DataSurface aria-labelledby="backup-assets-recovery-title">
          <DataSurfaceHeader
            title={<span id="backup-assets-recovery-title">{t("backups.recoveryTitle")}</span>}
            headingLevel="h2"
          />
          <DataSurfaceContent className="space-y-4">
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
            {taskId ? (
              <Button variant="outline" size="lg" asChild>
                <Link to={backupAssetsTaskContextHref(taskId)}>
                  <ClipboardList className="size-4" aria-hidden />
                  {t("backups.taskContext")}
                </Link>
              </Button>
            ) : null}
          </DataSurfaceContent>
        </DataSurface>
      </Reveal>
    </Stagger>
  );
}

function shortOpaqueId(value: string): string {
  return `${value.slice(0, 8)}…${value.slice(-8)}`;
}
