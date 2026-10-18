import { Download } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { formatTime } from "@/lib/api/core";
import type { BackupEntry } from "@/lib/api/system-api";
import { formatBytes } from "@/lib/utils";

// Must match the panel's focus-return lookup: `${source}:${filename}`.
function backupDownloadKey(entry: Pick<BackupEntry, "source" | "filename">): string {
  return `${entry.source}:${entry.filename}`;
}

export function BackupTable({
  entries,
  downloadDisabled,
  onDownload,
}: {
  entries: BackupEntry[];
  downloadDisabled: boolean;
  onDownload: (entry: BackupEntry) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-xs">
        <thead className="border-b border-border/50 text-muted-foreground">
          <tr>
            <th scope="col" className="pb-1.5 pr-4 font-medium">{t("selfBackup.colFilename")}</th>
            <th scope="col" className="pb-1.5 pr-4 font-medium">{t("selfBackup.colSize")}</th>
            <th scope="col" className="pb-1.5 pr-4 font-medium">{t("selfBackup.colCreatedAt")}</th>
            <th scope="col" className="pb-1.5 pr-4 font-medium">{t("selfBackup.colSha256")}</th>
            <th scope="col" className="pb-1.5 font-medium text-right">{t("selfBackup.colDownload")}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-border/30">
          {entries.map((entry) => (
            <tr key={backupDownloadKey(entry)} data-backup-row={entry.filename} className="text-muted-foreground">
              <td className="py-1.5 pr-4 font-mono break-all">{entry.filename}</td>
              <td className="py-1.5 pr-4 whitespace-nowrap">{formatBytes(entry.size)}</td>
              <td className="py-1.5 pr-4 whitespace-nowrap">{formatTime(entry.createdAt)}</td>
              <td className="py-1.5 pr-4 font-mono" title={entry.sha256 || undefined}>
                {entry.sha256 ? entry.sha256.slice(0, 16) + "..." : "-"}
              </td>
              <td className="py-1 text-right">
                {entry.downloadable ? (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    className="h-7 px-2"
                    data-backup-download-key={backupDownloadKey(entry)}
                    aria-label={t("selfBackup.download.rowAction", { filename: entry.filename })}
                    disabled={downloadDisabled}
                    onClick={() => onDownload(entry)}
                  >
                    <Download className="mr-1 size-3.5" aria-hidden="true" />
                    {t("selfBackup.download.action")}
                  </Button>
                ) : (
                  <span className="inline-block max-w-56 text-left text-[11px] leading-snug text-muted-foreground/90">
                    {/* A non-downloadable row without a recognised reason reads as unsafe, never as available. */}
                    {t(`selfBackup.download.unavailable.${entry.unavailableReason || "unsafe"}`)}
                  </span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
