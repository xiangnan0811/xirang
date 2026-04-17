import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { BackupHealthPanel } from "@/components/backup-health-panel";
import { StorageUsagePanel } from "@/components/storage-usage-panel";
import { StorageGuideCard } from "@/components/storage-guide-card";
import { PageHero } from "@/components/ui/page-hero";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import type { BackupHealthData } from "@/types/domain";

export function BackupsPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [healthData, setHealthData] = useState<BackupHealthData | null>(null);
  const [healthLoading, setHealthLoading] = useState(true);

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setHealthLoading(true);
    apiClient
      .getBackupHealth(token, { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted) setHealthData(result);
      })
      .catch(() => {
        // non-critical — BackupHealthPanel handles its own error display
      })
      .finally(() => {
        if (!controller.signal.aborted) setHealthLoading(false);
      });
    return () => controller.abort();
  }, [token]);

  const subtitle =
    healthLoading ? (
      <Skeleton className="h-4 w-48" />
    ) : healthData ? (
      t("backups.pageSubtitle", {
        count: healthData.summary.policiesHealthy + healthData.summary.policiesDegraded,
        healthy: healthData.summary.policiesHealthy,
      })
    ) : null;

  return (
    <div className="animate-fade-in flex flex-col space-y-5 min-h-0">
      <PageHero
        title={t("backups.pageTitle")}
        subtitle={subtitle}
        actions={
          <Button shape="pill" onClick={() => {}}>
            + {t("backups.newBackup")}
          </Button>
        }
      />

      <section className="shrink-0 flex flex-col min-h-0">
        <BackupHealthPanel />
      </section>

      <section className="grid grid-cols-1 xl:grid-cols-3 gap-5 shrink-0 items-start">
        <div className="xl:col-span-2">
          <StorageUsagePanel />
        </div>
        <div className="xl:col-span-1">
          <StorageGuideCard />
        </div>
      </section>
    </div>
  );
}
