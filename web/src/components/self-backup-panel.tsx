import { useCallback, useEffect, useState } from "react";
import { DatabaseBackup, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import type { BackupEntry } from "@/lib/api/system-api";
import { formatBytes, getErrorMessage } from "@/lib/utils";
import { formatTime } from "@/lib/api/core";
import { toast } from "sonner";

export function SelfBackupPanel() {
  const { t } = useTranslation();
  const { token, role } = useAuth();
  const [backing, setBacking] = useState(false);
  const [backups, setBackups] = useState<BackupEntry[]>([]);
  const [loadingList, setLoadingList] = useState(false);

  const fetchBackups = useCallback(async (signal?: AbortSignal) => {
    if (!token) return;
    setLoadingList(true);
    try {
      const list = await apiClient.listBackups(token, { signal });
      if (!signal?.aborted) {
        setBackups(list);
      }
    } catch (err) {
      if (signal?.aborted) return;
      // 静默处理列表加载失败
      console.warn(t('selfBackup.listLoadFailed'), err);
    } finally {
      if (!signal?.aborted) {
        setLoadingList(false);
      }
    }
  }, [token, t]);

  useEffect(() => {
    if (!token || role !== "admin") return;
    const controller = new AbortController();
    void fetchBackups(controller.signal);
    return () => controller.abort();
  }, [token, role, fetchBackups]);

  if (!token || role !== "admin") return null;

  const handleBackup = async () => {
    setBacking(true);
    try {
      const result = await apiClient.backupDB(token);
      toast.success(t('selfBackup.backupSuccess', { path: result.path, size: formatBytes(result.size) }));
      void fetchBackups();
    } catch (err) {
      toast.error(getErrorMessage(err, t('selfBackup.backupFailed')));
    } finally {
      setBacking(false);
    }
  };

  return (
    <Card className="glass-panel border-border/70 relative overflow-hidden group">
      <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" />
      <CardHeader className="pb-3 z-10 relative">
        <CardTitle className="text-base">{t('selfBackup.title')}</CardTitle>
      </CardHeader>
      <CardContent>
        <p className="text-xs text-muted-foreground mb-3">
          {t('selfBackup.desc')}
        </p>
        <Button size="sm" variant="outline" onClick={handleBackup} disabled={backing}>
          {backing ? <Loader2 className="mr-1 size-3.5 animate-spin" /> : <DatabaseBackup className="mr-1 size-3.5" />}
          {t('selfBackup.backupNow')}
        </Button>

        {/* 备份历史列表 */}
        {loadingList ? (
          <p className="mt-4 text-xs text-muted-foreground">{t('selfBackup.loadingList')}</p>
        ) : backups.length > 0 ? (
          <div className="mt-4 overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="border-b border-border/50 text-muted-foreground">
                <tr>
                  <th scope="col" className="pb-1.5 pr-4 font-medium">{t('selfBackup.colFilename')}</th>
                  <th scope="col" className="pb-1.5 pr-4 font-medium">{t('selfBackup.colSize')}</th>
                  <th scope="col" className="pb-1.5 pr-4 font-medium">{t('selfBackup.colCreatedAt')}</th>
                  <th scope="col" className="pb-1.5 font-medium">{t('selfBackup.colSha256')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/30">
                {backups.map((entry) => (
                  <tr key={entry.filename} className="text-muted-foreground">
                    <td className="py-1.5 pr-4 font-mono">{entry.filename}</td>
                    <td className="py-1.5 pr-4">{formatBytes(entry.size)}</td>
                    <td className="py-1.5 pr-4">{formatTime(entry.created_at)}</td>
                    <td className="py-1.5 font-mono" title={entry.sha256}>
                      {entry.sha256 ? entry.sha256.slice(0, 16) + "..." : "-"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="mt-4 text-xs text-muted-foreground">{t('selfBackup.noRecords')}</p>
        )}
      </CardContent>
    </Card>
  );
}
