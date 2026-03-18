import { useTranslation } from "react-i18next";
import { SelfBackupPanel } from "@/components/self-backup-panel";
import { ConfigExportImport } from "@/components/config-export-import";

export function MaintenanceTab() {
  const { t } = useTranslation();

  return (
    <div className="space-y-6">
      <h2 className="text-lg font-semibold">{t("settings.maintenance.title")}</h2>
      <SelfBackupPanel />
      <ConfigExportImport />
    </div>
  );
}
