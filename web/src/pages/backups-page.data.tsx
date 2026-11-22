import { useEffect, useState } from "react";
import { ClipboardList, FolderSearch } from "lucide-react";
import { Link, Navigate, useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";

import {
  readBackupAssetsPreferences,
  resolveBackupAssetsLayout,
  writeBackupAssetsPreferences,
} from "@/features/backup-assets/backup-assets-preferences";
import {
  parseBackupAssetsRoute,
  gateBackupAssetsBrowseRoute,
  updateBackupAssetsRoute,
  type BackupAssetsRouteState,
} from "@/features/backup-assets/backup-assets-route-state";
import { BackupAssetsWorkspace } from "@/features/backup-assets/backup-assets-workspace";
import { BackupFileSourceCompare } from "@/features/backup-assets/backup-file-source-compare";
import { BackupFileSourceControls } from "@/features/backup-assets/backup-file-source-controls";
import { useBackupFileSources } from "@/features/backup-assets/use-backup-file-sources";
import {
  useBackupAssetsState,
  type BackupAssetsSemanticIssue,
} from "@/features/backup-assets/use-backup-assets-state";
import { useAuth } from "@/context/auth-context.hooks";
import { canAccessBackupData, normalizeAppPathname } from "@/lib/backup-navigation";
import { StepUpPrerequisiteNotice } from "@/components/step-up-prerequisite-notice";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";

export function BackupsDataPage() {
  const location = useLocation();
  const { token, role } = useAuth();
  if (normalizeAppPathname(location.pathname) !== "/app/backups/data") {
    return null;
  }
  if (!canAccessBackupData(role)) {
    return <Navigate to="/app/backups/overview" replace />;
  }

  const route = parseBackupAssetsRoute(location.pathname, location.search);

  if (route.status === "invalid") {
    return <Navigate to={route.safePath} replace />;
  }

  return (
    <BackupsDataWorkspace
      key={`${token ?? ""}:${role ?? ""}`}
      route={route.state}
      routeHasExplicitLayout={new URLSearchParams(location.search).has("layout")}
    />
  );
}

function BackupsDataWorkspace({
  route,
  routeHasExplicitLayout,
}: {
  route: BackupAssetsRouteState;
  routeHasExplicitLayout: boolean;
}) {
  const { t } = useTranslation();
  const { token, role, userId, ensureStepUpProof, clearStepUpProof, totpEnabled, authTransitioning } = useAuth();
  const navigate = useNavigate();
  const [preferences, setPreferences] = useState(readBackupAssetsPreferences);
  const layout = resolveBackupAssetsLayout(
    routeHasExplicitLayout ? route.layout : undefined,
    preferences
  );
  const resolvedRoute = layout === route.layout ? route : { ...route, layout };

  useEffect(() => {
    if (routeHasExplicitLayout || layout === route.layout) return;
    const result = updateBackupAssetsRoute(route, { layout });
    navigate(result.status === "valid" ? result.href : result.safePath, { replace: true });
  }, [layout, navigate, route, routeHasExplicitLayout]);

  const handleRoutePatch = (patch: Partial<typeof resolvedRoute>, options?: { replace?: boolean }) => {
    const result = updateBackupAssetsRoute(resolvedRoute, patch);
    if (result.status === "valid" && patch.layout !== undefined) {
      const nextPreferences = { ...preferences, layout: patch.layout };
      setPreferences(nextPreferences);
      writeBackupAssetsPreferences(nextPreferences);
    }
    navigate(result.status === "valid" ? result.href : result.safePath, {
      replace: options?.replace === true || (result.status === "valid" && result.replace === true),
    });
  };

  const handleRouteRepair = (repair: BackupAssetsSemanticIssue) => {
    const result = updateBackupAssetsRoute(resolvedRoute, repair.patch);
    navigate(result.status === "valid" ? result.href : result.safePath, { replace: true });
  };

  const fileSources = useBackupFileSources({ token, route: resolvedRoute, onRoutePatch: handleRoutePatch });
  const browseState = fileSources.selectedVersion?.browseState ?? null;
  const controllerRoute = gateBackupAssetsBrowseRoute(resolvedRoute, browseState);
  const controller = useBackupAssetsState({
    token,
    role,
    route: controllerRoute,
    ensureStepUpProof,
    clearStepUpProof,
    totpEnabled,
    authTransitioning,
    onRouteRepair: handleRouteRepair,
  });
  const sourceWorkspaceUnavailable =
    fileSources.status === "blocked" || fileSources.status === "permission_denied" ||
    (browseState !== null && browseState !== "browsable");
  const showOnboarding = Boolean(token) &&
    fileSources.status === "empty" &&
    !fileSources.hasMoreNodes &&
    !fileSources.paginationError &&
    !fileSources.paginationPermissionDenied &&
    isOrdinaryBrowseRoute(resolvedRoute);

  return (
    <div className="flex min-h-[32rem] flex-col" aria-labelledby="backup-assets-data-title">
      <h2 id="backup-assets-data-title" className="sr-only">
        {t("backups.dataTitle")}
      </h2>
      {role === "admin" || role === "operator" ? <StepUpPrerequisiteNotice className="m-3" /> : null}
      {showOnboarding ? (
        <BackupFilesOnboarding canManage={role === "admin" && !authTransitioning} />
      ) : (
        <>
          <BackupFileSourceControls
            status={fileSources.status}
            nodes={fileSources.nodes}
            sets={fileSources.sets}
            versions={fileSources.versions}
            selectedNodeId={resolvedRoute.nodeId}
            selectedBackupSetId={resolvedRoute.backupSetId}
            selectedRecoveryPointId={resolvedRoute.recoveryPointId}
            onSelectNode={fileSources.selectNode}
            onSelectSet={fileSources.selectSet}
            onSelectVersion={fileSources.selectVersion}
            hasMoreNodes={fileSources.hasMoreNodes}
            hasMoreSets={fileSources.hasMoreSets}
            hasMoreVersions={fileSources.hasMoreVersions}
            loadingMoreNodes={fileSources.loadingMoreNodes}
            loadingMoreSets={fileSources.loadingMoreSets}
            loadingMoreVersions={fileSources.loadingMoreVersions}
            onLoadMoreNodes={fileSources.loadMoreNodes}
            onLoadMoreSets={fileSources.loadMoreSets}
            onLoadMoreVersions={fileSources.loadMoreVersions}
            paginationError={fileSources.paginationError}
            paginationPermissionDenied={fileSources.paginationPermissionDenied}
            canRetry={fileSources.canRetry}
            onRetry={fileSources.retry}
            versionAction={(
              <BackupFileSourceCompare
                token={token}
                role={role}
                backupSetId={fileSources.selectedBackupSetId}
                versions={fileSources.versions}
                selectedRecoveryPointId={resolvedRoute.recoveryPointId}
                hasMoreVersions={fileSources.hasMoreVersions}
                loadingMoreVersions={fileSources.loadingMoreVersions}
                versionsLoading={fileSources.loadingVersions}
                onLoadMoreVersions={fileSources.loadMoreVersions}
                onRefreshVersions={fileSources.refreshVersions}
                versionsFailure={fileSources.versionsFailure}
                onRetryVersions={fileSources.retryVersions}
              />
            )}
          />
          {sourceWorkspaceUnavailable ? null : (
            <BackupAssetsWorkspace
              controller={controller}
              preferences={preferences}
              processingRuntime={{ token, role, userId, ensureStepUpProof, totpEnabled, authTransitioning }}
              onRoutePatch={handleRoutePatch}
              onReturnOverview={() => navigate("/app/backups/overview")}
            />
          )}
        </>
      )}
    </div>
  );
}

/**
 * Plain Files entry: browse view with no source, repository, point, entry, search,
 * export or filter context. Layout and sort are presentation only.
 */
function isOrdinaryBrowseRoute(route: BackupAssetsRouteState): boolean {
  return route.view === "browse" &&
    route.nodeId === undefined &&
    route.backupSetId === undefined &&
    route.repositoryId === undefined &&
    route.taskId === undefined &&
    route.recoveryPointId === undefined &&
    route.parentEntryId === undefined &&
    route.entryId === undefined &&
    route.savedSearchId === undefined &&
    route.exportJobId === undefined &&
    route.scope === "current" &&
    route.types.length === 0 &&
    route.tagId === undefined &&
    !route.favoriteOnly;
}

function BackupFilesOnboarding({ canManage }: { canManage: boolean }) {
  const { t } = useTranslation();
  const linkClassName = "touch-target min-h-11 lg:min-h-9";
  return (
    <EmptyState
      className="m-3 flex-1"
      icon={FolderSearch}
      title={t("backupAssets.sources.onboarding.title")}
      description={t(canManage
        ? "backupAssets.sources.onboarding.adminDescription"
        : "backupAssets.sources.onboarding.operatorDescription")}
      action={(
        <div className="flex flex-wrap items-center justify-center gap-2">
          {canManage ? (
            <>
              <Button size="sm" className={linkClassName} asChild>
                <Link to="/app/tasks">
                  <ClipboardList className="size-4" aria-hidden />
                  {t("backupAssets.sources.onboarding.openTasks")}
                </Link>
              </Button>
              <Button size="sm" variant="outline" className={linkClassName} asChild>
                <Link to="/app/backups/overview#backup-assets-ga">
                  {t("backupAssets.sources.onboarding.openReadiness")}
                </Link>
              </Button>
            </>
          ) : (
            <Button size="sm" variant="outline" className={linkClassName} asChild>
              <Link to="/app/backups/overview">{t("backupAssets.actions.returnOverview")}</Link>
            </Button>
          )}
        </div>
      )}
    />
  );
}
