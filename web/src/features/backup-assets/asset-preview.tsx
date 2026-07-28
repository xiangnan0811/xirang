import { lazy, Suspense, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { Download, Eye, RefreshCw, Sparkles } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import type { AuthContextValue } from "@/context/auth-context.shared";
import type { AssetRef, BackupAsset, BackupContentTicket } from "@/types/domain";

import { selectProcessingRepresentation } from "./backup-assets-processing-state";
import type { ProcessingPreviewSource } from "./backup-asset-processing-panel";
import type { BackupAssetsValueResource } from "./use-backup-assets-state";

const RENEW_BEFORE_MS = 30_000;
const MEDIA_DESCRIPTION_ID = "backup-assets-preview-media-description";
const LazyBackupAssetProcessingPanel = lazy(() =>
  import("./backup-asset-processing-panel").then((module) => ({
    default: module.BackupAssetProcessingPanel,
  }))
);
const LazyArchiveMemberPanel = lazy(() =>
  import("./archive-member-panel").then((module) => ({
    default: module.ArchiveMemberPanel,
  }))
);

function archiveFocusFallback(trigger: HTMLElement | null): HTMLElement | null {
  const panel = trigger?.closest<HTMLElement>('[role="tabpanel"]');
  const inspector = panel?.parentElement;
  const inspectorHeading = inspector?.querySelector<HTMLElement>('h2[tabindex="-1"]');
  if (inspectorHeading?.isConnected) return inspectorHeading;
  const activeInspectorTab = inspector?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]');
  return activeInspectorTab?.isConnected ? activeInspectorTab : null;
}

function archiveWorkspaceFocusFallback(trigger: HTMLElement | null): HTMLElement | null {
  const workspace = trigger?.closest<HTMLElement>('[data-testid="backup-assets-workspace"]');
  const results = workspace?.querySelector<HTMLElement>('section[tabindex="-1"][aria-label]');
  return results?.isConnected ? results : null;
}

function restoreArchiveFocus(
  trigger: HTMLElement | null,
  inspectorFallback: HTMLElement | null,
  workspaceFallback: HTMLElement | null,
): void {
  if (trigger?.isConnected) {
    trigger.focus();
    return;
  }
  if (inspectorFallback?.isConnected) {
    inspectorFallback.focus();
    return;
  }
  workspaceFallback?.focus();
}

export interface AssetPreviewProps {
  asset: BackupAsset;
  resource: BackupAssetsValueResource<BackupContentTicket>;
  canPreview: boolean;
  canDownload: boolean;
  processingRuntime?: Pick<AuthContextValue, "token" | "role" | "ensureStepUpProof">;
  archiveContentAvailable?: boolean;
  archiveDownloadAllowed?: boolean;
  online?: boolean;
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
  processingRuntime,
  archiveContentAvailable = canDownload,
  archiveDownloadAllowed = canDownload,
  online,
  onLoadPreview,
  onRenew,
  onPrepareDownload,
  onDetach,
}: AssetPreviewProps) {
  const { t } = useTranslation();
  const contentNodeRef = useRef<HTMLIFrameElement | HTMLImageElement | HTMLMediaElement | null>(null);
  const detachRef = useRef(onDetach);
  const mediaRetryRef = useRef({ binding: "", count: 0 });
  const [processingOpen, setProcessingOpen] = useState(false);
  const archiveAssetKey = `${asset.ref.recoveryPointId}:${asset.ref.entryId}`;
  const canBrowseArchive = Boolean(
    processingRuntime?.token &&
      (processingRuntime.role === "admin" || processingRuntime.role === "operator")
  );
  const archiveContextKey = JSON.stringify([
    archiveAssetKey,
    processingRuntime?.token ?? null,
    processingRuntime?.role ?? null,
    canBrowseArchive,
  ]);

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
        {processingRuntime?.token ? (
          <Button type="button" variant="ghost" size="sm" onClick={() => setProcessingOpen(true)}>
            <Sparkles className="size-4" aria-hidden />
            {t("backupAssets.preview.processingStatus")}
          </Button>
        ) : null}
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
      {processingOpen && processingRuntime?.token ? (
        <Suspense
          fallback={(
            <p className="border-t border-border px-3 py-2 text-xs text-muted-foreground" role="status">
              {t("backupAssets.preview.processingLoading")}
            </p>
          )}
        >
          <ArchiveProcessingSession
            key={archiveContextKey}
            asset={asset}
            canNativePreview={canPreview}
            canDownload={canDownload}
            runtime={processingRuntime}
            canBrowseArchive={canBrowseArchive}
            contentAvailable={archiveContentAvailable}
            downloadAllowed={archiveDownloadAllowed}
            online={online}
            onOpenPreview={(source) => onLoadPreview(assetForProcessingPreview(asset, source))}
            onPrepareDownload={(ref) => {
              if (
                ref.recoveryPointId === asset.ref.recoveryPointId &&
                ref.entryId === asset.ref.entryId
              ) {
                return onPrepareDownload(asset);
              }
            }}
          />
        </Suspense>
      ) : null}
    </div>
  );
}

interface ArchiveProcessingSessionProps {
  asset: BackupAsset;
  canNativePreview: boolean;
  canDownload: boolean;
  runtime: NonNullable<AssetPreviewProps["processingRuntime"]>;
  canBrowseArchive: boolean;
  contentAvailable: boolean;
  downloadAllowed: boolean;
  online?: boolean;
  onOpenPreview: (source: ProcessingPreviewSource) => void;
  onPrepareDownload: (ref: AssetRef) => void | Promise<void>;
}

function ArchiveProcessingSession({
  asset,
  canNativePreview,
  canDownload,
  runtime,
  canBrowseArchive,
  contentAvailable,
  downloadAllowed,
  online,
  onOpenPreview,
  onPrepareDownload,
}: ArchiveProcessingSessionProps) {
  const { t } = useTranslation();
  const archiveTriggerRef = useRef<HTMLElement | null>(null);
  const archiveFocusFallbackRef = useRef<HTMLElement | null>(null);
  const archiveWorkspaceFocusFallbackRef = useRef<HTMLElement | null>(null);
  const archiveDismissRef = useRef<(() => void) | null>(null);
  const [archiveOpen, setArchiveOpen] = useState(false);
  const archiveOpenRef = useRef(false);

  useLayoutEffect(() => {
    archiveOpenRef.current = archiveOpen;
  }, [archiveOpen]);

  const registerArchiveDismissHandler = useCallback((handler: () => void) => {
    archiveDismissRef.current = handler;
    return () => {
      if (archiveDismissRef.current === handler) archiveDismissRef.current = null;
    };
  }, []);

  useEffect(() => () => {
    if (!archiveOpenRef.current) return;
    archiveDismissRef.current?.();
    queueMicrotask(() => restoreArchiveFocus(
      archiveTriggerRef.current,
      archiveFocusFallbackRef.current,
      archiveWorkspaceFocusFallbackRef.current,
    ));
  }, []);

  return (
    <>
      <LazyBackupAssetProcessingPanel
        token={runtime.token}
        asset={asset}
        canNativePreview={canNativePreview}
        canDownload={canDownload}
        onOpenPreview={onOpenPreview}
        onPrepareDownload={() => onPrepareDownload(asset.ref)}
        onBrowseArchive={canBrowseArchive ? () => {
          const trigger = document.activeElement instanceof HTMLElement
            ? document.activeElement
            : null;
          archiveTriggerRef.current = trigger;
          archiveFocusFallbackRef.current = archiveFocusFallback(trigger);
          archiveWorkspaceFocusFallbackRef.current = archiveWorkspaceFocusFallback(trigger);
          setArchiveOpen(true);
        } : undefined}
      />
      {canBrowseArchive ? (
        <Dialog
          open={archiveOpen}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) archiveDismissRef.current?.();
            setArchiveOpen(nextOpen);
          }}
        >
          <DialogContent
            size="lg"
            aria-describedby={undefined}
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              queueMicrotask(() => restoreArchiveFocus(
                archiveTriggerRef.current,
                archiveFocusFallbackRef.current,
                archiveWorkspaceFocusFallbackRef.current,
              ));
            }}
          >
            <DialogHeader>
              <DialogTitle>{t("backupAssets.archive.title")}</DialogTitle>
              <DialogCloseButton aria-label={t("backupAssets.archive.close")} />
            </DialogHeader>
            <Suspense fallback={<LoadingState title={t("backupAssets.archive.loading")} rows={6} />}>
              <LazyArchiveMemberPanel
                refValue={asset.ref}
                runtime={runtime}
                contentAvailable={contentAvailable}
                downloadAllowed={downloadAllowed}
                online={online}
                onDismissHandlerRegister={registerArchiveDismissHandler}
                onPrepareDownload={onPrepareDownload}
              />
            </Suspense>
          </DialogContent>
        </Dialog>
      ) : null}
    </>
  );
}

function assetForProcessingPreview(asset: BackupAsset, source: ProcessingPreviewSource): BackupAsset {
  if (source === "native") return asset;
  switch (selectProcessingRepresentation(asset)) {
    case "document_pages":
      return { ...asset, mimeType: "image/png" };
    case "archive_index":
      return { ...asset, mimeType: "text/plain" };
    default:
      return asset;
  }
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
