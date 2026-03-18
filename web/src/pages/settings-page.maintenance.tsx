import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { Button } from "@/components/ui/button";

export function MaintenanceTab() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [backupMsg, setBackupMsg] = useState("");
  const [backingUp, setBackingUp] = useState(false);

  const handleBackup = async () => {
    if (!token) return;
    setBackingUp(true);
    setBackupMsg("");
    try {
      await apiClient.backupDB(token);
      setBackupMsg(t("settings.maintenance.backupSuccess"));
    } catch {
      setBackupMsg(t("settings.maintenance.backupFailed"));
    } finally {
      setBackingUp(false);
    }
  };

  const handleExport = async () => {
    if (!token) return;
    try {
      const data = await apiClient.exportConfig(token);
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `xirang-config-${new Date().toISOString().slice(0, 10)}.json`;
      a.click();
      URL.revokeObjectURL(url);
    } catch { /* ignore */ }
  };

  return (
    <div className="space-y-6 max-w-2xl">
      <h2 className="text-lg font-semibold">{t("settings.maintenance.title")}</h2>

      {/* 数据库备份 */}
      <div className="rounded-lg border p-4 space-y-3">
        <h3 className="text-sm font-medium">{t("settings.maintenance.dbBackup")}</h3>
        <p className="text-xs text-muted-foreground">{t("settings.maintenance.dbBackupDesc")}</p>
        <div className="flex items-center gap-3">
          <Button onClick={handleBackup} disabled={backingUp}>
            {backingUp ? t("common.loading") : t("settings.maintenance.createBackup")}
          </Button>
          {backupMsg && <span className="text-xs text-muted-foreground">{backupMsg}</span>}
        </div>
      </div>

      {/* 配置导出 */}
      <div className="rounded-lg border p-4 space-y-3">
        <h3 className="text-sm font-medium">{t("settings.maintenance.configExport")}</h3>
        <p className="text-xs text-muted-foreground">{t("settings.maintenance.configExportDesc")}</p>
        <Button variant="outline" onClick={handleExport}>
          {t("common.export")}
        </Button>
      </div>
    </div>
  );
}
