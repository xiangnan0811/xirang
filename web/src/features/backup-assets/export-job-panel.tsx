import { useEffect, useMemo, useState } from "react";
import { Archive, Download, RefreshCw, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import type { AuthContextValue } from "@/context/auth-context.shared";
import { formatBytes } from "@/lib/utils";
import type { AssetRef, BackupExportArchiveFormat, BackupExportArchiveProfile } from "@/types/domain";

import {
  useBackupAssetExport,
  type BackupAssetExportApi,
  type BackupAssetExportCreateOptions,
} from "./use-backup-asset-export";
import { ContentTransportGuidance } from "./content-transport-guidance";

export interface ExportJobPanelSelection {
  ref: AssetRef;
  logicalBytes: number;
}

export interface ExportJobPanelProps {
  open: boolean;
  selection: readonly ExportJobPanelSelection[];
  exportJobId?: string;
  runtime: Pick<AuthContextValue, "token" | "role" | "ensureStepUpProof">;
  onRouteChange: (exportJobId: string | null, options: { replace: boolean }) => void;
  onDismiss: () => void;
  api?: BackupAssetExportApi;
}

const PAGE_SIZE = 200;

export function ExportJobPanel({
  open,
  selection,
  exportJobId,
  runtime,
  onRouteChange,
  onDismiss,
  api,
}: ExportJobPanelProps) {
  const { t } = useTranslation();
  const [archiveFormat, setArchiveFormat] = useState<BackupExportArchiveFormat>("zip");
  const [archiveProfile, setArchiveProfile] = useState<BackupExportArchiveProfile>("zip_deflate_v1");
  const estimate = useMemo(() => ({
    count: selection.length,
    logicalBytes: selection.reduce((total, item) => total + Math.max(0, item.logicalBytes), 0),
  }), [selection]);
  const refs = useMemo(() => selection.map((item) => item.ref), [selection]);
  const controller = useBackupAssetExport({
    token: runtime.token,
    role: runtime.role,
    ensureStepUpProof: runtime.ensureStepUpProof,
    exportJobId,
    onRouteChange,
    api,
  });
  const job = controller.state.job;
  const remainingTTL = useExportTTL(
    job?.expiresAt ?? null,
    open && (job?.executionState === "ready" || job?.executionState === "expiring"),
  );

  useEffect(() => {
    if (open && !exportJobId && controller.state.phase === "closed" && refs.length > 0) {
      controller.open(refs, estimate);
    }
  }, [controller, estimate, exportJobId, open, refs]);

  if (!open) return null;

  const createOptions: BackupAssetExportCreateOptions = { archiveFormat, archiveProfile };
  const authoritative = job !== null;
  const items = job?.items.slice(0, PAGE_SIZE) ?? [];
  const canCreate = runtime.role === "admin" && controller.state.phase === "review" && controller.state.selection.length > 0;

  const changeFormat = (format: BackupExportArchiveFormat) => {
    setArchiveFormat(format);
    setArchiveProfile(format === "zip" ? "zip_deflate_v1" : "tar_gzip_v1");
  };

  return (
    <section aria-labelledby="backup-export-title" className="flex min-h-0 flex-col" data-testid="export-job-panel">
      <header className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
        <div className="min-w-0">
          <h3 id="backup-export-title" className="text-sm font-semibold">{t("backupAssets.export.title")}</h3>
          <p className="mt-1 text-xs text-muted-foreground">{t("backupAssets.export.description")}</p>
        </div>
        <Button type="button" variant="ghost" size="icon" aria-label={t("backupAssets.export.close")} onClick={() => {
          controller.dismiss();
          onDismiss();
        }}>
          <X className="size-4" aria-hidden />
        </Button>
      </header>

      <div className="min-h-0 space-y-4 overflow-y-auto px-5 py-4">
        {!authoritative ? (
          <section className="grid gap-3 rounded-md border border-border bg-muted/50 p-3" aria-label={t("backupAssets.export.estimate")}>
            <div className="flex items-center justify-between gap-3">
              <span className="text-xs font-medium text-muted-foreground">{t("backupAssets.export.estimate")}</span>
              <Badge tone="info">{t("backupAssets.export.itemCount", { count: controller.state.estimate.count })}</Badge>
            </div>
            <output data-testid="export-estimate" className="font-mono text-sm tabular-nums">
              {formatBytes(controller.state.estimate.logicalBytes)}
            </output>
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="grid gap-1 text-xs font-medium">
                {t("backupAssets.export.format")}
                <select
                  value={archiveFormat}
                  onChange={(event) => changeFormat(event.target.value === "tar" ? "tar" : "zip")}
                  className="h-9 rounded-md border border-input bg-background px-2 text-sm"
                  disabled={controller.state.phase === "creating"}
                >
                  <option value="zip">{t("backupAssets.export.formatZip")}</option>
                  <option value="tar">{t("backupAssets.export.formatTar")}</option>
                </select>
              </label>
              <label className="grid gap-1 text-xs font-medium">
                {t("backupAssets.export.profile")}
                <select
                  value={archiveProfile}
                  onChange={(event) => setArchiveProfile(event.target.value as BackupExportArchiveProfile)}
                  className="h-9 rounded-md border border-input bg-background px-2 text-sm"
                  disabled={controller.state.phase === "creating"}
                >
                  {archiveFormat === "zip" ? <option value="zip_deflate_v1">{t("backupAssets.export.profileZip")}</option> : null}
                  {archiveFormat === "tar" ? <option value="tar_gzip_v1">{t("backupAssets.export.profileTarGzip")}</option> : null}
                  {archiveFormat === "tar" ? <option value="tar_none_v1">{t("backupAssets.export.profileTarNone")}</option> : null}
                </select>
              </label>
            </div>
          </section>
        ) : (
          <section className="grid gap-3 rounded-md border border-border bg-muted/50 p-3" data-testid="export-authoritative" aria-label={t("backupAssets.export.authoritative")}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <Badge tone={job.executionState === "ready" ? "success" : job.executionState === "failed" ? "destructive" : "info"}>
                {t(`backupAssets.export.status.${job.executionState}`)}
              </Badge>
              {job.resultKind ? (
                <span className="text-xs font-medium">{t(`backupAssets.export.${job.resultKind}`)}</span>
              ) : null}
            </div>
            <p className="text-xs text-muted-foreground" data-testid="export-outcome-counts">
              {t("backupAssets.export.outcomeCounts", {
                packed: job.packedCount,
                skipped: job.skippedCount,
                failed: job.failedCount,
              })}
            </p>
            {remainingTTL !== null ? (
              <p className="font-mono text-xs tabular-nums text-muted-foreground" data-testid="export-ttl">
                {t("backupAssets.export.expiresIn", { duration: formatTTL(remainingTTL) })}
              </p>
            ) : null}
            <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs sm:grid-cols-4">
              <div><dt className="text-muted-foreground">{t("backupAssets.export.itemCount", { count: job.itemCount })}</dt><dd className="mt-0.5 font-mono tabular-nums">{job.itemCount}</dd></div>
              <div><dt className="text-muted-foreground">{t("backupAssets.export.logicalBytes")}</dt><dd className="mt-0.5 font-mono tabular-nums">{formatBytes(job.logicalBytes)}</dd></div>
              <div><dt className="text-muted-foreground">{t("backupAssets.export.providerBytes")}</dt><dd className="mt-0.5 font-mono tabular-nums">{formatBytes(job.providerBytes)}</dd></div>
              <div><dt className="text-muted-foreground">{t("backupAssets.export.artifactBytes")}</dt><dd className="mt-0.5 font-mono tabular-nums">{formatBytes(job.artifactBytes)}</dd></div>
            </dl>
            <p className="break-all text-[11px] text-muted-foreground" data-testid="export-selection-digest">
              <span className="mr-2">{t("backupAssets.export.selectionDigest")}</span>
              <span className="font-mono">{job.selectionDigest}</span>
            </p>
          </section>
        )}

        {job ? (
          <section aria-labelledby="backup-export-items-title" className="space-y-2">
            <div className="flex items-center justify-between gap-3">
              <h4 id="backup-export-items-title" className="text-xs font-semibold">{t("backupAssets.export.items")}</h4>
              <span className="text-xs text-muted-foreground">{job.packedCount}/{job.itemCount}</span>
            </div>
            <div
              role="progressbar"
              aria-label={t("backupAssets.export.progress")}
              aria-valuemin={0}
              aria-valuemax={job.itemCount}
              aria-valuenow={Math.min(job.itemCount, job.packedCount + job.skippedCount + job.failedCount)}
              className="h-1.5 overflow-hidden rounded-full bg-muted"
            >
              <span
                aria-hidden
                className="block h-full rounded-full bg-primary transition-[width]"
                style={{
                  width: `${job.itemCount === 0 ? 0 : Math.min(100, ((job.packedCount + job.skippedCount + job.failedCount) / job.itemCount) * 100)}%`,
                }}
              />
            </div>
            {items.length > 0 ? (
              <ul
                aria-labelledby="backup-export-items-title"
                className="max-h-64 divide-y divide-border overflow-y-auto rounded-md border border-border"
              >
                {items.map((item) => (
                  <li key={item.id} className="flex items-center gap-2 px-3 py-2 text-xs">
                    <span className="font-mono text-muted-foreground">{item.ordinal + 1}</span>
                    <span className="min-w-0 flex-1">
                      <span className="block">{t(`backupAssets.export.itemState.${item.state}`)}</span>
                      {item.errorCategory ? (
                        <span className="block truncate text-[11px] text-muted-foreground">
                          {t(`backupAssets.export.errorCategory.${item.errorCategory}`)}
                        </span>
                      ) : null}
                    </span>
                    <span className="font-mono tabular-nums text-muted-foreground">{formatBytes(item.logicalBytes)}</span>
                  </li>
                ))}
              </ul>
            ) : <p className="text-xs text-muted-foreground">{t("backupAssets.export.noItems")}</p>}
            {job.nextCursor ? (
              <div className="flex justify-center">
                <Button type="button" variant="outline" size="sm" onClick={() => void controller.loadMoreItems()}>
                  {t("backupAssets.export.loadMoreItems")}
                </Button>
              </div>
            ) : null}
          </section>
        ) : null}

        {controller.state.error === "secure_transport_required" ? (
          <InlineAlert tone="critical">
            <span>{t("backupAssets.errors.secureTransportRequired")}</span>
            <ContentTransportGuidance authRole={runtime.role} />
          </InlineAlert>
        ) : controller.state.error ? (
          <p role="alert" className="text-xs text-destructive">{t("backupAssets.export.error")}</p>
        ) : null}
        <p className="sr-only" aria-live="polite">{announcementText(controller.state.announcement, t)}</p>
      </div>

      <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-border bg-secondary px-5 py-3">
        {!authoritative ? (
          <Button type="button" size="sm" loading={controller.state.phase === "creating"} disabled={!canCreate} onClick={() => void controller.create(createOptions)}>
            <Archive className="size-4" aria-hidden />
            {t("backupAssets.export.create")}
          </Button>
        ) : (
          <>
            <Button type="button" variant="ghost" size="sm" onClick={() => void controller.reload()}>
              <RefreshCw className="size-4" aria-hidden />
              {t("backupAssets.export.reload")}
            </Button>
            {job.canCancel ? (
              <Button type="button" variant="outline" size="sm" onClick={() => void controller.cancel()}>
                {t("backupAssets.export.cancel")}
              </Button>
            ) : null}
            {job.canDownload ? (
              <Button type="button" size="sm" onClick={() => void controller.download()}>
                <Download className="size-4" aria-hidden />
                {t("backupAssets.export.download")}
              </Button>
            ) : null}
          </>
        )}
      </footer>
    </section>
  );
}

function announcementText(announcement: string | null, translate: (key: string) => string): string {
  if (!announcement) return "";
  if (announcement.startsWith("state:")) return translate(`backupAssets.export.status.${announcement.slice("state:".length)}`);
  return translate(`backupAssets.export.announcement.${announcement}`);
}

function useExportTTL(expiresAt: string | null, enabled: boolean): number | null {
  const [clockNow, setClockNow] = useState(() => Date.now());

  useEffect(() => {
    if (!enabled || !expiresAt) return;
    const update = () => setClockNow(Date.now());
    const immediate = window.setTimeout(update, 0);
    const timer = window.setInterval(update, 1_000);
    return () => {
      window.clearTimeout(immediate);
      window.clearInterval(timer);
    };
  }, [enabled, expiresAt]);

  if (!enabled || !expiresAt) return null;
  const expiry = Date.parse(expiresAt);
  return Number.isFinite(expiry)
    ? Math.max(0, Math.ceil((expiry - clockNow) / 1_000))
    : null;
}

function formatTTL(seconds: number): string {
  const hours = Math.floor(seconds / 3_600);
  const minutes = Math.floor((seconds % 3_600) / 60);
  const rest = seconds % 60;
  return hours > 0
    ? `${hours}:${minutes.toString().padStart(2, "0")}:${rest.toString().padStart(2, "0")}`
    : `${minutes}:${rest.toString().padStart(2, "0")}`;
}
