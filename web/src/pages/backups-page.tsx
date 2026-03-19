import { useTranslation } from "react-i18next";
import { BackupHealthPanel } from "@/components/backup-health-panel";
import { StorageUsagePanel } from "@/components/storage-usage-panel";
import { StorageGuideCard } from "@/components/storage-guide-card";

export function BackupsPage() {
  const { t } = useTranslation();

  return (
    <div className="animate-fade-in flex flex-col space-y-5 min-h-0">
      <h1 className="text-xl font-semibold shrink-0">{t("backups.title")}</h1>

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
