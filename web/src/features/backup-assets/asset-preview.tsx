import { useCallback, useEffect, useRef } from "react";
import { Download, Eye, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import type { BackupAsset, BackupContentTicket } from "@/types/domain";

import type { BackupAssetsValueResource } from "./use-backup-assets-state";

const RENEW_BEFORE_MS = 30_000;
const MEDIA_DESCRIPTION_ID = "backup-assets-preview-media-description";

export interface AssetPreviewProps {
  asset: BackupAsset;
  resource: BackupAssetsValueResource<BackupContentTicket>;
  canPreview: boolean;
  canDownload: boolean;
  onLoadPreview: (asset: BackupAsset) => void;
  onRenew: () => void;
  onPrepareDownload: (asset: BackupAsset) => void;
  onDetach: () => void;
}

export function AssetPreview({
  asset,
  resource,
  canPreview,
  canDownload,
  onLoadPreview,
  onRenew,
  onPrepareDownload,
  onDetach,
}: AssetPreviewProps) {
  const { t } = useTranslation();
  const contentNodeRef = useRef<HTMLIFrameElement | HTMLImageElement | HTMLMediaElement | null>(null);
  const detachRef = useRef(onDetach);
  const mediaRetryRef = useRef({ binding: "", count: 0 });

  useEffect(() => {
    detachRef.current = onDetach;
  }, [onDetach]);

  const rememberContentNode = useCallback(
    (node: HTMLIFrameElement | HTMLImageElement | HTMLMediaElement | null) => {
      if (node) contentNodeRef.current = node;
    },
    []
  );

  const ticket = resource.status === "ready" ? resource.value : null;
  const contentUrl = ticket?.contentUrl ?? "";

  useEffect(() => {
    const node = contentNodeRef.current;
    return () => {
      if (!node || node.getAttribute("src") !== contentUrl) return;
      node.removeAttribute("src");
      if (node instanceof HTMLMediaElement) node.load();
    };
  }, [contentUrl]);

  useEffect(() => () => detachRef.current(), []);

  useEffect(() => {
    if (!ticket || ticket.action !== "preview") return;
    const expiry = Math.min(Date.parse(ticket.expiresAt), Date.parse(ticket.idleExpiresAt));
    if (!Number.isFinite(expiry)) return;
    const delay = Math.max(0, expiry - Date.now() - RENEW_BEFORE_MS);
    const timer = window.setTimeout(onRenew, delay);
    return () => window.clearTimeout(timer);
  }, [onRenew, ticket]);

  const handleMediaError = () => {
    if (!ticket) return;
    const binding = `${asset.ref.recoveryPointId}:${asset.ref.entryId}`;
    if (mediaRetryRef.current.binding !== binding) {
      mediaRetryRef.current = { binding, count: 0 };
    }
    if (mediaRetryRef.current.count >= 1) return;
    mediaRetryRef.current.count += 1;
    onRenew();
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <div className="flex min-h-11 shrink-0 flex-wrap items-center justify-end gap-2 border-b border-border px-2 py-1.5">
        {canPreview && resource.status !== "loading" && ticket?.action !== "preview" ? (
          <Button type="button" variant="ghost" size="sm" onClick={() => onLoadPreview(asset)}>
            <Eye className="size-4" aria-hidden />
            {t("backupAssets.preview.load")}
          </Button>
        ) : null}
        {canPreview && ticket?.action === "preview" ? (
          <Button type="button" variant="ghost" size="sm" onClick={onRenew}>
            <RefreshCw className="size-4" aria-hidden />
            {t("backupAssets.preview.refresh")}
          </Button>
        ) : null}
        {canDownload && ticket?.action !== "download" ? (
          <Button type="button" variant="ghost" size="sm" onClick={() => onPrepareDownload(asset)}>
            <Download className="size-4" aria-hidden />
            {t("backupAssets.preview.prepareDownload")}
          </Button>
        ) : null}
      </div>

      <div
        data-testid="asset-preview-viewport"
        className="flex h-[18rem] min-h-[18rem] max-h-[18rem] min-w-0 shrink-0 items-center justify-center overflow-auto bg-muted/20 p-3"
      >
        <PreviewBody
          asset={asset}
          resource={resource}
          ticket={ticket}
          canPreview={canPreview}
          canDownload={canDownload}
          rememberContentNode={rememberContentNode}
          onMediaError={handleMediaError}
        />
      </div>
    </div>
  );
}

function PreviewBody({
  asset,
  resource,
  ticket,
  canPreview,
  canDownload,
  rememberContentNode,
  onMediaError,
}: {
  asset: BackupAsset;
  resource: BackupAssetsValueResource<BackupContentTicket>;
  ticket: BackupContentTicket | null;
  canPreview: boolean;
  canDownload: boolean;
  rememberContentNode: (node: HTMLIFrameElement | HTMLImageElement | HTMLMediaElement | null) => void;
  onMediaError: () => void;
}) {
  const { t } = useTranslation();
  if (resource.status === "loading") {
    return <LoadingState title={t("backupAssets.preview.loading")} rows={4} />;
  }
  if (resource.status === "blocked" || resource.status === "error") {
    return (
      <InlineAlert tone={resource.status === "blocked" ? "warning" : "critical"}>
        {t(resource.error?.translationKey ?? "backupAssets.preview.unavailable")}
      </InlineAlert>
    );
  }
  if (!ticket) {
    return (
      <p className="max-w-sm text-center text-sm text-muted-foreground" role="status">
        {canPreview || canDownload
          ? t("backupAssets.preview.notLoaded")
          : t("backupAssets.preview.actionsUnavailable")}
      </p>
    );
  }
  if (ticket.action === "download" && ticket.renderer === "attachment") {
    return (
      <a
        href={ticket.contentUrl}
        download={asset.name}
        className="inline-flex h-9 max-w-full items-center gap-2 rounded-md border border-input bg-card px-3 text-sm font-medium focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/35"
      >
        <Download className="size-4 shrink-0" aria-hidden />
        <span className="truncate">{t("backupAssets.preview.download", { name: asset.name })}</span>
      </a>
    );
  }
  if (ticket.action !== "preview") {
    return <InlineAlert tone="warning">{t("backupAssets.preview.unavailable")}</InlineAlert>;
  }

  if (ticket.renderer === "safe_raster") {
    return (
      <img
        ref={rememberContentNode}
        src={ticket.contentUrl}
        alt={asset.name}
        className="max-h-full max-w-full object-contain"
        onError={onMediaError}
      />
    );
  }
  if (ticket.renderer === "native_audio") {
    return (
      <div className="w-full max-w-xl text-center">
        {/* Broker tickets do not expose a caption-track URL. */}
        {/* eslint-disable-next-line jsx-a11y/media-has-caption */}
        <audio
          ref={rememberContentNode}
          src={ticket.contentUrl}
          controls
          aria-label={asset.name}
          aria-describedby={MEDIA_DESCRIPTION_ID}
          className="w-full"
          onError={onMediaError}
        >
          {t("backupAssets.preview.mediaUnsupported")}
        </audio>
        <p id={MEDIA_DESCRIPTION_ID} className="mt-2 text-xs text-muted-foreground">
          {t("backupAssets.preview.captionsUnavailable")}
        </p>
      </div>
    );
  }
  if (ticket.renderer === "native_video") {
    return (
      <div className="max-w-full text-center">
        {/* Broker tickets do not expose a caption-track URL. */}
        {/* eslint-disable-next-line jsx-a11y/media-has-caption */}
        <video
          ref={rememberContentNode}
          src={ticket.contentUrl}
          controls
          aria-label={asset.name}
          aria-describedby={MEDIA_DESCRIPTION_ID}
          className="max-h-full max-w-full"
          onError={onMediaError}
        >
          {t("backupAssets.preview.mediaUnsupported")}
        </video>
        <p id={MEDIA_DESCRIPTION_ID} className="mt-2 text-xs text-muted-foreground">
          {t("backupAssets.preview.captionsUnavailable")}
        </p>
      </div>
    );
  }
  const title =
    ticket.renderer === "same_origin_pdf"
      ? t("backupAssets.preview.pdfTitle")
      : ticket.renderer === "metadata_hex"
        ? t("backupAssets.preview.hexTitle")
        : t("backupAssets.preview.frameTitle");
  return (
    <iframe
      ref={rememberContentNode}
      src={ticket.contentUrl}
      title={title}
      sandbox=""
      className="h-full max-h-full w-full border-0 bg-card"
      onError={onMediaError}
    />
  );
}
