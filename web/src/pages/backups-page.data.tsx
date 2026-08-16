import { useEffect, useState } from "react";
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";

import {
  readBackupAssetsPreferences,
  resolveBackupAssetsLayout,
  writeBackupAssetsPreferences,
} from "@/features/backup-assets/backup-assets-preferences";
import {
  parseBackupAssetsRoute,
  updateBackupAssetsRoute,
  type BackupAssetsRouteState,
} from "@/features/backup-assets/backup-assets-route-state";
import { BackupAssetsWorkspace } from "@/features/backup-assets/backup-assets-workspace";
import {
  useBackupAssetsState,
  type BackupAssetsSemanticIssue,
} from "@/features/backup-assets/use-backup-assets-state";
import { useAuth } from "@/context/auth-context.hooks";

export function BackupsDataPage() {
  const location = useLocation();
  const route = parseBackupAssetsRoute(location.pathname, location.search);

  if (route.status === "invalid") {
    return <Navigate to={route.safePath} replace />;
  }

  return (
    <BackupsDataWorkspace
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
  const { token, role, userId, ensureStepUpProof } = useAuth();
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

  const controller = useBackupAssetsState({
    token,
    route: resolvedRoute,
    ensureStepUpProof,
    onRouteRepair: handleRouteRepair,
  });

  return (
    <div className="flex min-h-[32rem] flex-col" aria-labelledby="backup-assets-data-title">
      <h2 id="backup-assets-data-title" className="sr-only">
        {t("backups.dataTitle")}
      </h2>
      <BackupAssetsWorkspace
        controller={controller}
        preferences={preferences}
        processingRuntime={{ token, role, userId, ensureStepUpProof }}
        onRoutePatch={handleRoutePatch}
        onReturnOverview={() => navigate("/app/backups/overview")}
      />
    </div>
  );
}
