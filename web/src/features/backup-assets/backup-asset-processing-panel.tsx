import { Download, Eye, Loader2, Sparkles, Square } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import type { BackupAsset, BackupProcessingProductState } from "@/types/domain";

import { selectProcessingRepresentation } from "./backup-assets-processing-state";
import { useBackupAssetProcessing } from "./use-backup-asset-processing";

export interface BackupAssetProcessingPanelProps {
  token: string | null;
  asset: BackupAsset;
  canNativePreview: boolean;
  canDownload: boolean;
  onOpenPreview: () => void;
  onPrepareDownload: () => void;
}

export function BackupAssetProcessingPanel({
  token,
  asset,
  canNativePreview,
  canDownload,
  onOpenPreview,
  onPrepareDownload,
}: BackupAssetProcessingPanelProps) {
  const { t } = useTranslation();
  const representation = selectProcessingRepresentation(asset);
  const { state, request, cancel } = useBackupAssetProcessing({ token, ref: asset.ref });
  const current = state.active?.representation === representation
    ? state.active
    : state.products.find((product) => product.representation === representation) ?? null;

  if (state.status === "error") {
    return (
      <div className="border-t border-border p-2">
        <InlineAlert tone="critical" className="rounded-md py-2">
          {t("backupAssets.processing.error")}
        </InlineAlert>
      </div>
    );
  }

  const status = current?.state ?? (state.status === "loading" ? "loading" : "unavailable");
  const queued = current?.state === "queued";
  const ready = current?.state === "derived" || current?.state === "partial";
  const canGenerate = current?.state === "native";
  const retryable = current?.state === "failed" && current.retryable;
  const nativeAllowed = Boolean(
    current?.fallbackActions.includes("native_preview") && canNativePreview
  );
  const downloadAllowed = Boolean(current?.fallbackActions.includes("download") && canDownload);

  return (
    <section
      aria-label={t("backupAssets.processing.title")}
      className="shrink-0 border-t border-border bg-card/40 px-2 py-2"
    >
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <Sparkles className="size-4 shrink-0 text-primary" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="truncate text-xs font-medium">{t("backupAssets.processing.title")}</p>
          <p className="truncate text-[11px] text-muted-foreground">
            {t(`backupAssets.processing.representation.${representation}`)}
          </p>
        </div>
        <Badge tone={processingTone(status)}>
          {state.status === "loading" && current === null ? (
            <Loader2 className="size-3 animate-spin" aria-hidden />
          ) : null}
          {t(`backupAssets.processing.state.${status}`)}
        </Badge>
      </div>

      <div className="mt-2 flex min-h-6 flex-wrap items-center gap-1.5" aria-live="polite">
        <span className="sr-only">{t(`backupAssets.processing.state.${status}`)}</span>
        {current?.coverage ? (
          <Badge tone={current.coverage === "complete" ? "success" : "warning"}>
            {t(`backupAssets.processing.coverage.${current.coverage}`)}
          </Badge>
        ) : null}
        {current?.freshness ? (
          <Badge tone={current.freshness === "current" ? "neutral" : "warning"}>
            {t(`backupAssets.processing.freshness.${current.freshness}`)}
          </Badge>
        ) : null}
        {current?.scanStatus ? (
          <Badge tone={current.scanStatus === "finding" ? "destructive" : "neutral"}>
            {t(`backupAssets.processing.scan.${current.scanStatus}`)}
          </Badge>
        ) : null}
        {current?.sensitivityStatus ? (
          <Badge tone={current.sensitivityStatus === "secret" ? "destructive" : "neutral"}>
            {t(`backupAssets.processing.sensitivity.${current.sensitivityStatus}`)}
          </Badge>
        ) : null}
      </div>

      <div className="mt-2 flex min-h-8 flex-wrap items-center justify-end gap-1.5">
        {canGenerate ? (
          <Button type="button" variant="outline" size="sm" onClick={() => void request(representation)}>
            <Sparkles className="size-4" aria-hidden />
            {t("backupAssets.processing.actions.generate")}
          </Button>
        ) : null}
        {ready ? (
          <Button type="button" variant="outline" size="sm" onClick={onOpenPreview}>
            <Sparkles className="size-4" aria-hidden />
            {t("backupAssets.processing.actions.open")}
          </Button>
        ) : null}
        {queued ? (
          <Button type="button" variant="outline" size="sm" onClick={() => void cancel()}>
            <Square className="size-3.5" aria-hidden />
            {t("backupAssets.processing.actions.cancel")}
          </Button>
        ) : null}
        {retryable ? (
          <Button type="button" variant="outline" size="sm" onClick={() => void request(representation)}>
            <Sparkles className="size-4" aria-hidden />
            {t("backupAssets.processing.actions.retry")}
          </Button>
        ) : null}
        {nativeAllowed ? (
          <Button type="button" variant="ghost" size="sm" onClick={onOpenPreview}>
            <Eye className="size-4" aria-hidden />
            {t("backupAssets.processing.actions.native")}
          </Button>
        ) : null}
        {downloadAllowed ? (
          <Button type="button" variant="ghost" size="sm" onClick={onPrepareDownload}>
            <Download className="size-4" aria-hidden />
            {t("backupAssets.processing.actions.download")}
          </Button>
        ) : null}
      </div>
    </section>
  );
}

function processingTone(state: BackupProcessingProductState | "loading" | "unavailable") {
  switch (state) {
    case "derived":
      return "success" as const;
    case "partial":
    case "failed":
      return "warning" as const;
    case "queued":
    case "native":
    case "loading":
      return "info" as const;
    default:
      return "neutral" as const;
  }
}
