import { BackupConfidencePanel } from "@/components/backup-confidence-panel";
import { BackupHealthPanel } from "@/components/backup-health-panel";
import { StorageGuideCard } from "@/components/storage-guide-card";
import { StorageUsagePanel } from "@/components/storage-usage-panel";
import { useAuth } from "@/context/auth-context.hooks";
import { GaReadinessPanel } from "@/features/backup-assets/ga-readiness-panel";
import { useTranslation } from "react-i18next";

export function BackupsOverviewPage() {
  const { t } = useTranslation();
  const { token, role } = useAuth();

  return (
    <div className="flex min-h-0 flex-col space-y-5">
      <h2 className="sr-only">{t("backups.tabs.overview")}</h2>
      {role === "admin" && token ? (
        <section className="flex min-h-0 shrink-0 flex-col">
          <GaReadinessPanel token={token} role={role} />
        </section>
      ) : null}
      <section className="flex min-h-0 shrink-0 flex-col">
        <BackupConfidencePanel />
      </section>

      <section className="flex min-h-0 shrink-0 flex-col">
        <BackupHealthPanel />
      </section>

      <section className="grid shrink-0 grid-cols-1 items-start gap-5 xl:grid-cols-3">
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
