import { useTranslation } from "react-i18next";
import { BackupHealthPanel } from "@/components/backup-health-panel";
import { StorageUsagePanel } from "@/components/storage-usage-panel";
import { StorageGuideCard } from "@/components/storage-guide-card";

export function BackupsPage() {
  const { t } = useTranslation();

  return (
    <div className="animate-fade-in space-y-5">
      <h1 className="text-xl font-semibold">{t("backups.title")}</h1>

      <section>
        <BackupHealthPanel />
      </section>

      <section>
        <StorageUsagePanel />
      </section>

      <section>
        <StorageGuideCard />
      </section>
    </div>
  );
}
