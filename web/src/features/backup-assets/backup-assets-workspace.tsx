import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { FolderTree, PanelRight } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import type { AuthContextValue } from "@/context/auth-context.shared";
import type { AssetRef, CatalogProjection } from "@/types/domain";

import { AssetContextPanel } from "./asset-context-panel";
import { RepositoryManagementPanel } from "./repository-management-panel";
import { RetentionPolicyPanel } from "./retention-policy-panel";
import {
  DEFAULT_BACKUP_ASSETS_PREFERENCES,
  type BackupAssetsPreferencesV1,
} from "./backup-assets-preferences";
import type { BackupAssetsRouteState } from "./backup-assets-route-state";
import type { BackupAssetsController } from "./use-backup-assets-state";
import { useBackupRecovery } from "./use-backup-recovery";
import { AssetBrowser } from "./asset-browser";
import { AssetEvidence } from "./asset-evidence";
import { AssetInspector } from "./asset-inspector";
import { AssetOverlays } from "./asset-overlays";
import type { BackupAssetsOverlaySection } from "./use-backup-assets-state";
import { AssetPreview } from "./asset-preview";
import { selectBackupAssetPreviewProduct } from "./asset-preview-model";
import {
  assetRefKey,
  createBackupAssetsRestorationRegistry,
  type BackupAssetResultRow,
  type BackupAssetsRestorationAnchor,
  type BackupAssetsRestorationRegistry,
} from "./backup-assets-state";

const LazyProcessingCoveragePanel = lazy(() =>
  import("./processing-coverage-panel").then((module) => ({
    default: module.ProcessingCoveragePanel,
  }))
);
const LazyExportJobPanel = lazy(() =>
  import("./export-job-panel").then((module) => ({
    default: module.ExportJobPanel,
  }))
);
const LazyRecoveryPlanWizard = lazy(() =>
  import("./recovery-plan-wizard").then((module) => ({
    default: module.RecoveryPlanWizard,
  }))
);

type BackupAssetsProcessingRuntime = Pick<
  AuthContextValue,
  "token" | "role" | "ensureStepUpProof"
> & Partial<Pick<AuthContextValue, "userId">>;

export interface BackupAssetsWorkspaceProps {
  controller: BackupAssetsController;
  preferences?: BackupAssetsPreferencesV1;
  processingRuntime?: BackupAssetsProcessingRuntime;
  onRoutePatch: (patch: Partial<BackupAssetsRouteState>, options?: { replace?: boolean }) => void;
  onReturnOverview: () => void;
}

type BackupAssetsViewport = "desktop" | "intermediate" | "mobile";

type ExportReviewSnapshot = {
  selection: Array<{ ref: AssetRef; logicalBytes: number }>;
};

export function BackupAssetsWorkspace({
  controller,
  preferences = DEFAULT_BACKUP_ASSETS_PREFERENCES,
  processingRuntime,
  onRoutePatch,
  onReturnOverview,
}: BackupAssetsWorkspaceProps) {
  const { t } = useTranslation();
  const viewport = useBackupAssetsViewport();
  const online = useBackupAssetsOnline();
  const [overlaySection, setOverlaySection] = useState<BackupAssetsOverlaySection | null>(null);
  const [exportReviewSnapshot, setExportReviewSnapshot] = useState<ExportReviewSnapshot | null>(null);
  const [recoveryDialogOpen, setRecoveryDialogOpen] = useState(false);
  const overlayTriggerRef = useRef<HTMLButtonElement | null>(null);
  const exportTriggerRef = useRef<HTMLElement | null>(null);
  const resultsRegionRef = useRef<HTMLElement | null>(null);
  const restorationRegistryRef = useRef<BackupAssetsRestorationRegistry | null>(null);
  const lastRestorationContextRef = useRef<string | null>(null);
  const [restorationAnchor, setRestorationAnchor] = useState<BackupAssetsRestorationAnchor | null>(null);
  const recovery = useBackupRecovery({
    token: processingRuntime?.token ?? null,
    role: processingRuntime?.role ?? null,
    sessionKey: processingRuntime?.userId === null || processingRuntime?.userId === undefined
      ? null
      : String(processingRuntime.userId),
    contextKey: `${controller.state.route.repositoryId ?? ""}:${controller.state.route.recoveryPointId ?? ""}`,
    ensureStepUpProof: processingRuntime?.ensureStepUpProof,
    onRouteChange: (handles, options) => {
      onRoutePatch({
        page: "recovery",
        recoveryPointId: controller.state.route.recoveryPointId,
        planId: handles.planId ?? undefined,
        jobId: handles.jobId ?? undefined,
      }, options);
    },
  });
  if (restorationRegistryRef.current === null) {
    restorationRegistryRef.current = createBackupAssetsRestorationRegistry();
  }
  const handleRestorationComplete = useCallback(() => setRestorationAnchor(null), []);
  const selectedRepository = useMemo(
    () => findAvailable(controller.repositories.items, controller.state.route.repositoryId),
    [controller.repositories.items, controller.state.route.repositoryId]
  );
  const availableRecoveryPoints = useMemo(
    () => controller.recoveryPoints.items.flatMap((point) => (point.status === "available" ? [point.value] : [])),
    [controller.recoveryPoints.items]
  );
  const selectedAsset = controller.selectedEntry.status === "ready" ? controller.selectedEntry.value : null;
  const selectedRowIndex = selectedAsset
    ? controller.state.result.rows.findIndex(
        (row) =>
          row.ref.recoveryPointId === selectedAsset.ref.recoveryPointId &&
          row.ref.entryId === selectedAsset.ref.entryId
      )
    : -1;
  const selectedCatalog =
    controller.selectedRecoveryPoint?.catalog.status === "available"
      ? controller.selectedRecoveryPoint.catalog.value
      : null;
  const previewProduct = selectedAsset ? selectBackupAssetPreviewProduct(selectedAsset) : null;
  const previewNeedsRange =
    previewProduct !== null &&
    previewProduct.renderer !== "escaped_text" &&
    previewProduct.renderer !== "metadata_hex";
  const canPreview = Boolean(
    selectedCatalog?.permissions.preview &&
      selectedCatalog.contentAvailability.available &&
      controller.selectedRecoveryPoint &&
      (previewNeedsRange
        ? controller.selectedRecoveryPoint.capabilities.openRange
        : controller.selectedRecoveryPoint.capabilities.openSequential)
  );
  const canDownload = Boolean(
    selectedCatalog?.permissions.download &&
      selectedCatalog.contentAvailability.available &&
      controller.selectedRecoveryPoint?.capabilities.download
  );
  const canManageFavorite = Boolean(selectedCatalog?.permissions.list);
  const exportSelection = useMemo(() => {
    const rowsByRef = new Map(controller.state.result.rows.map((row) => [assetRefKey(row.ref), row]));
    return [...controller.state.selection.values()].flatMap((ref) => {
      const row = rowsByRef.get(assetRefKey(ref));
      return row ? [{ ref: { ...row.ref }, logicalBytes: row.asset.size }] : [];
    });
  }, [controller.state.result.rows, controller.state.selection]);
  const canExport = Boolean(
    processingRuntime?.role === "admin" &&
      exportSelection.length > 0 &&
      exportSelection.length === controller.state.selection.size
  );
  const recoveryGenerationId = selectedCatalog?.generation?.state === "complete"
    ? selectedCatalog.generation.id
    : null;
  const canRecoverRefs = (refs: readonly AssetRef[]) => Boolean(
    refs.length > 0 &&
      processingRuntime?.token &&
      processingRuntime.role === "admin" &&
      selectedRepository?.accessActive &&
      selectedRepository.capabilities.restore &&
      controller.selectedRecoveryPoint?.state === "committed" &&
      controller.selectedRecoveryPoint.physicalAvailability === "online" &&
      recoveryGenerationId &&
      refs.every(
        (ref) => ref.recoveryPointId === controller.selectedRecoveryPoint?.id,
      )
  );
  const canRecoverSelection = canRecoverRefs([...controller.state.selection.values()]);
  const openRecovery = (refs: readonly AssetRef[]) => {
    if (!canRecoverRefs(refs) || selectedRepository === null || recoveryGenerationId === null) return;
    recovery.open(
      refs.map((ref) => ({ recoveryPointId: ref.recoveryPointId, entryId: ref.entryId })),
      { repositoryId: selectedRepository.id, catalogGenerationId: recoveryGenerationId },
    );
    setRecoveryDialogOpen(true);
  };
  const exportDialogOpen = exportReviewSnapshot !== null || Boolean(controller.state.route.exportJobId);
  const favoriteMembershipComplete =
    controller.overlays.favorites.status === "ready" &&
    controller.overlays.favorites.nextCursor === null;
  const selectedFavorite = selectedAsset
    ? controller.overlays.favorites.items.find(
        (favorite) => assetRefKey(favorite.ref) === assetRefKey(selectedAsset.ref)
      ) ?? null
    : null;
  const favoritePending =
    controller.state.overlay.status === "pending" || controller.state.overlay.status === "reconciling";

  useEffect(() => {
    if (
      selectedAsset &&
      canManageFavorite &&
      controller.overlays.favorites.status === "idle"
    ) {
      controller.actions.loadOverlaySection("favorites");
    }
  }, [
    canManageFavorite,
    controller.actions,
    controller.overlays.favorites.status,
    selectedAsset,
  ]);

  useEffect(() => {
    if (controller.state.route.exportJobId && processingRuntime?.role !== "admin") {
      onRoutePatch({ exportJobId: undefined }, { replace: true });
    }
  }, [controller.state.route.exportJobId, onRoutePatch, processingRuntime?.role]);

  const recordResultAnchor = (
    row: BackupAssetResultRow,
    position: Pick<BackupAssetsRestorationAnchor, "index" | "offset">,
    contextKey = restorationContextKey(controller.state.route)
  ) => {
    if (contextKey === null) return;
    restorationRegistryRef.current?.record({
      contextKey,
      ref: row.ref,
      index: position.index,
      offset: position.offset,
    });
    lastRestorationContextRef.current = contextKey;
  };

  const recordAdjacentAnchor = (row: BackupAssetResultRow, index: number) => {
    const contextKey = lastRestorationContextRef.current ?? restorationContextKey(controller.state.route);
    if (contextKey === null) return;
    const previous = restorationRegistryRef.current?.read(contextKey);
    recordResultAnchor(row, { index, offset: previous?.offset ?? 0 }, contextKey);
  };

  const closeInspector = () => {
    const contextKey = lastRestorationContextRef.current;
    const recorded = contextKey === null ? null : restorationRegistryRef.current?.read(contextKey) ?? null;
    const exactRecorded =
      selectedAsset && recorded && assetRefKey(recorded.ref) === assetRefKey(selectedAsset.ref) ? recorded : null;
    const fallbackContextKey = restorationContextKey(controller.state.route);
    const fallback =
      selectedAsset && fallbackContextKey
        ? {
            contextKey: fallbackContextKey,
            ref: selectedAsset.ref,
            index: Math.max(0, selectedRowIndex),
            offset: 0,
          }
        : null;
    setRestorationAnchor(exactRecorded ?? fallback);
    onRoutePatch({ entryId: undefined });
  };

  if (controller.repositories.status === "blocked" || controller.repositories.status === "error") {
    return (
      <div className="min-h-[24rem] py-4">
        <InlineAlert tone="warning">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span>{t(controller.repositories.error?.translationKey ?? "backupAssets.errors.unknown")}</span>
            {controller.repositories.error?.action === "return_overview" ? (
              <Button variant="outline" size="sm" onClick={onReturnOverview}>
                {t("backupAssets.actions.returnOverview")}
              </Button>
            ) : null}
          </div>
        </InlineAlert>
      </div>
    );
  }
  if (controller.repositories.status === "loading" || controller.repositories.status === "idle") {
    return <LoadingState title={t("backupAssets.context.loadingRepositories")} rows={6} />;
  }
  if (controller.state.route.view === "repositories") {
    const refreshLifecycle = () => {
      controller.actions.refreshRepositories();
      controller.actions.refreshRecoveryPoints();
    };
    return (
      <div className="space-y-0">
        <RepositoryManagementPanel
          repositories={controller.repositories.items}
          selectedRepositoryId={controller.state.route.repositoryId}
          viewport={viewport}
          onBrowse={(repositoryId) => onRoutePatch({ view: "browse", repositoryId })}
          runtime={processingRuntime}
          onRefresh={refreshLifecycle}
        />
        <RetentionPolicyPanel
          repositories={controller.repositories.items}
          recoveryPoints={controller.recoveryPoints.items}
          selectedRepositoryId={controller.state.route.repositoryId}
          selectedRecoveryPointId={controller.state.route.recoveryPointId}
          runtime={processingRuntime}
          onRefresh={refreshLifecycle}
        />
      </div>
    );
  }

  const contextPanel = (
    <>
      {processingRuntime?.token && processingRuntime.role === "admin" ? (
        <ProcessingCoverageDialog runtime={processingRuntime} />
      ) : null}
      <AssetContextPanel
        route={controller.state.route}
        repositories={controller.repositories}
        recoveryPoints={controller.recoveryPoints}
        selectedRepository={selectedRepository}
        selectedRecoveryPoint={controller.selectedRecoveryPoint}
        directoryRows={controller.state.route.view === "browse" ? controller.state.result.rows : []}
        overlayCounts={{
          savedSearches: controller.overlays.savedSearches.items.length,
          favorites: controller.overlays.favorites.items.length,
          tags: controller.overlays.tags.items.length,
          recent: controller.overlays.recent.items.length,
        }}
        onRoutePatch={onRoutePatch}
        onOverlaySectionChange={(section, trigger) => {
          overlayTriggerRef.current = trigger;
          setOverlaySection(section);
          controller.actions.loadOverlaySection(section);
        }}
      />
    </>
  );

  const inspector =
    selectedAsset && controller.selectedRecoveryPoint ? (
      <AssetInspector
        asset={selectedAsset}
        recoveryPoint={controller.selectedRecoveryPoint}
        activeTab={controller.state.route.inspectorTab}
        canManageFavorite={canManageFavorite}
        favoriteState={favoriteMembershipComplete ? selectedFavorite?.state ?? null : undefined}
        favoritePending={favoritePending}
        canRecover={canRecoverRefs([selectedAsset.ref])}
        onToggleFavorite={() => controller.actions.toggleFavorite(selectedAsset.ref, selectedAsset.name)}
        onRecover={() => openRecovery([selectedAsset.ref])}
        preview={
          <AssetPreview
            asset={selectedAsset}
            resource={controller.content}
            canPreview={canPreview}
            canDownload={canDownload}
            processingRuntime={processingRuntime}
            archiveContentAvailable={Boolean(
              selectedCatalog?.contentAvailability.available &&
                controller.selectedRecoveryPoint.capabilities.download
            )}
            archiveDownloadAllowed={Boolean(selectedCatalog?.permissions.download)}
            online={online}
            onLoadPreview={controller.actions.loadPreview}
            onRenew={controller.actions.renewPreview}
            onPrepareDownload={controller.actions.prepareDownload}
            onDetach={controller.actions.detachContent}
          />
        }
        evidence={
          <AssetEvidence
            mode="evidence"
            recoveryPoints={availableRecoveryPoints}
            selectedRecoveryPoint={controller.selectedRecoveryPoint}
            evidence={controller.evidence}
            diff={controller.diff}
            onCompare={controller.actions.compareRecoveryPoints}
          />
        }
        diff={
          <AssetEvidence
            mode="diff"
            recoveryPoints={availableRecoveryPoints}
            selectedRecoveryPoint={controller.selectedRecoveryPoint}
            evidence={controller.evidence}
            diff={controller.diff}
            onCompare={controller.actions.compareRecoveryPoints}
          />
        }
        onTabChange={(inspectorTab) => onRoutePatch({ inspectorTab })}
        onPrevious={() =>
          openAdjacentResult(controller, selectedRowIndex - 1, onRoutePatch, recordAdjacentAnchor)
        }
        onNext={() => openAdjacentResult(controller, selectedRowIndex + 1, onRoutePatch, recordAdjacentAnchor)}
        hasPrevious={selectedRowIndex > 0}
        hasNext={selectedRowIndex >= 0 && selectedRowIndex < controller.state.result.rows.length - 1}
        onClose={closeInspector}
      />
    ) : null;
  const compactInspector = viewport !== "desktop" && inspector !== null;

  return (
    <>
      <div
        data-testid="backup-assets-workspace"
        data-viewport={viewport}
        className={
          viewport === "desktop"
            ? "grid min-h-[36rem] overflow-hidden border-y border-border"
            : viewport === "mobile"
              ? "flex min-h-[20rem] flex-col overflow-hidden border-y border-border"
              : "flex min-h-[36rem] flex-col overflow-hidden border-y border-border"
        }
        style={{
          height:
            viewport === "mobile"
              ? "calc(100dvh - 20.5rem)"
              : "calc(100dvh - 14.25rem)",
          ...(viewport === "desktop"
            ? {
                gridTemplateColumns:
                  `minmax(224px, ${preferences.contextWidth}px) minmax(420px, 1fr) minmax(300px, ${preferences.inspectorWidth}px)`,
              }
            : {}),
        }}
      >
      {compactInspector ? (
        <section
          data-testid="backup-assets-mobile-inspector"
          aria-label={t("backupAssets.regions.inspector")}
          className="flex min-h-0 flex-1 flex-col overflow-hidden"
        >
          {inspector}
        </section>
      ) : (
        <>
      {viewport === "desktop" ? (
        <aside
          aria-label={t("backupAssets.regions.context")}
          className="min-w-0 overflow-y-auto border-r border-border"
        >
          {contextPanel}
        </aside>
      ) : (
        <div className="flex h-11 shrink-0 items-center justify-between border-b border-border px-2">
          <ContextDialog triggerLabel={t("backupAssets.actions.openContext")} title={t("backupAssets.regions.context")}>
            {contextPanel}
          </ContextDialog>
          <span className="min-w-0 truncate px-2 text-xs text-muted-foreground">
            {selectedRepository?.displayName ?? t("backupAssets.context.selectRepository")}
          </span>
        </div>
      )}

      <section
        ref={resultsRegionRef}
        tabIndex={-1}
        aria-label={t("backupAssets.regions.results")}
        className="flex min-w-0 flex-1 flex-col overflow-hidden"
      >
        {controller.semanticIssue ? (
          <RecoveryPointBlockedState
            issue={controller.semanticIssue}
            recoveryPoint={controller.selectedRecoveryPoint}
            onReturn={() =>
              onRoutePatch({
                recoveryPointId: undefined,
                parentEntryId: undefined,
                entryId: undefined,
              })
            }
          />
        ) : controller.filterIssue ? (
          <FilterBlockedState
            issue={controller.filterIssue}
            onClear={() => onRoutePatch(controller.filterIssue?.patch ?? {})}
          />
        ) : (
          <AssetBrowser
            state={controller.state}
            onRoutePatch={onRoutePatch}
            onSearch={(query, scope) => {
              controller.actions.setSearchDraft(query);
              if (controller.state.route.view === "search" && controller.state.route.scope === scope) {
                controller.actions.executeSearch(query);
              } else {
                onRoutePatch({ view: "search", scope });
              }
            }}
            onSearchDraftChange={controller.actions.setSearchDraft}
            onToggleSelection={controller.actions.toggleSelection}
            onClearSelection={controller.actions.clearSelection}
            canExport={canExport}
            canRecover={canRecoverSelection}
            onExport={() => {
              const selection = exportSelection.map((item) => ({
                ref: {
                  recoveryPointId: item.ref.recoveryPointId,
                  entryId: item.ref.entryId,
                },
                logicalBytes: Number.isSafeInteger(item.logicalBytes) && item.logicalBytes >= 0
                  ? item.logicalBytes
                  : 0,
              }));
              exportTriggerRef.current = document.activeElement instanceof HTMLElement
                ? document.activeElement
                : null;
              setExportReviewSnapshot({ selection });
            }}
            onRecover={() => openRecovery([...controller.state.selection.values()])}
            onOpen={(row, position) => {
              if (row.asset.entryType === "directory") {
                onRoutePatch({ parentEntryId: row.ref.entryId, entryId: undefined });
                return;
              }
              recordResultAnchor(row, position);
              onRoutePatch({ entryId: row.ref.entryId });
            }}
            onLoadMore={controller.actions.loadMore}
            restorationAnchor={restorationAnchor}
            onRestorationComplete={handleRestorationComplete}
          />
        )}
      </section>

      {viewport === "desktop" ? (
        <aside
          aria-label={t("backupAssets.regions.inspector")}
          className="min-w-0 overflow-y-auto border-l border-border"
        >
          {inspector ?? <WorkspacePendingState icon={PanelRight} text={t("backupAssets.states.selectAsset")} />}
        </aside>
      ) : null}
        </>
      )}
      </div>
      <AssetOverlays
        section={overlaySection}
        savedSearches={controller.overlays.savedSearches}
        favorites={controller.overlays.favorites}
        tags={controller.overlays.tags}
        recent={controller.overlays.recent}
        pending={controller.state.overlay.status === "pending" || controller.state.overlay.status === "reconciling"}
        error={controller.overlayError}
        canSaveCurrent={controller.state.route.view === "search" && controller.state.searchDraft.trim() !== ""}
        selectedRef={selectedAssetRef(controller)}
        onClose={() => {
          setOverlaySection(null);
          queueMicrotask(() => overlayTriggerRef.current?.focus());
        }}
        onCreateSaved={controller.actions.createSavedSearch}
        onUpdateSaved={controller.actions.updateSavedSearch}
        onDeleteSaved={controller.actions.deleteSavedSearch}
        onExecuteSaved={(savedSearchId) => {
          setOverlaySection(null);
          onRoutePatch({ savedSearchId });
        }}
        onToggleFavorite={controller.actions.toggleFavorite}
        onCreateTag={controller.actions.createTag}
        onUpdateTag={controller.actions.updateTag}
        onDeleteTag={controller.actions.deleteTag}
        onAssignTag={controller.actions.assignTag}
        onClearRecent={controller.actions.clearRecent}
        onOpenRef={(ref) => {
          setOverlaySection(null);
          lastRestorationContextRef.current = null;
          setRestorationAnchor(null);
          onRoutePatch({
            view: "browse",
            repositoryId: undefined,
            taskId: undefined,
            recoveryPointId: ref.recoveryPointId,
            parentEntryId: undefined,
            entryId: ref.entryId,
            scope: "current",
          });
        }}
      />
      {processingRuntime?.role === "admin" && recoveryDialogOpen ? (
        <Suspense fallback={<LoadingState title={t("backupAssets.recovery.title")} rows={6} />}>
          <LazyRecoveryPlanWizard
            open={recoveryDialogOpen}
            recovery={recovery}
            onOpenChange={setRecoveryDialogOpen}
          />
        </Suspense>
      ) : null}
      {processingRuntime?.role === "admin" ? (
        <Dialog open={exportDialogOpen} onOpenChange={(nextOpen) => {
          if (!nextOpen) {
            setExportReviewSnapshot(null);
            if (controller.state.route.exportJobId) {
              onRoutePatch({ exportJobId: undefined }, { replace: true });
            }
          }
        }}>
          <DialogContent
            size="lg"
            aria-label={t("backupAssets.export.title")}
            aria-describedby={undefined}
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              const trigger = exportTriggerRef.current;
              if (trigger?.isConnected) trigger.focus();
              else resultsRegionRef.current?.focus();
            }}
          >
            <DialogHeader className="sr-only">
              <DialogTitle>{t("backupAssets.export.title")}</DialogTitle>
            </DialogHeader>
            <Suspense fallback={<LoadingState title={t("backupAssets.export.title")} rows={5} />}>
              <LazyExportJobPanel
                open={exportDialogOpen}
                selection={exportReviewSnapshot?.selection ?? []}
                exportJobId={controller.state.route.exportJobId}
                runtime={processingRuntime}
                onRouteChange={(exportJobId, options) => {
                  setExportReviewSnapshot(null);
                  onRoutePatch({ exportJobId: exportJobId ?? undefined }, options);
                }}
                onDismiss={() => setExportReviewSnapshot(null)}
              />
            </Suspense>
          </DialogContent>
        </Dialog>
      ) : null}
    </>
  );
}

function ProcessingCoverageDialog({ runtime }: { runtime: BackupAssetsProcessingRuntime }) {
  const { t } = useTranslation();
  const title = t("backupAssets.adminProcessing.title");
  return (
    <div className="flex min-h-11 items-center border-b border-border px-2">
      <Dialog>
        <DialogTrigger asChild>
          <Button type="button" variant="ghost" size="sm">
            <PanelRight className="size-4" aria-hidden />
            {title}
          </Button>
        </DialogTrigger>
        <DialogContent size="lg" aria-label={title} aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogCloseButton aria-label={title} />
          </DialogHeader>
          <DialogBody className="max-h-[75dvh] overflow-y-auto p-0">
            <Suspense fallback={<LoadingState title={t("backupAssets.adminProcessing.loading")} rows={7} />}>
              <LazyProcessingCoveragePanel
                token={runtime.token}
                role={runtime.role}
                ensureStepUpProof={runtime.ensureStepUpProof}
              />
            </Suspense>
          </DialogBody>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function openAdjacentResult(
  controller: BackupAssetsController,
  index: number,
  onRoutePatch: (patch: Partial<BackupAssetsRouteState>) => void,
  onBeforeOpen: (row: BackupAssetResultRow, index: number) => void
) {
  const row = controller.state.result.rows[index];
  if (!row) return;
  onBeforeOpen(row, index);
  onRoutePatch({ recoveryPointId: row.ref.recoveryPointId, entryId: row.ref.entryId });
}

function selectedAssetRef(controller: BackupAssetsController) {
  if (controller.state.selection.size === 1) return controller.state.selection.values().next().value ?? null;
  const { recoveryPointId, entryId } = controller.state.route;
  return recoveryPointId && entryId ? { recoveryPointId, entryId } : null;
}

function restorationContextKey(route: BackupAssetsRouteState): string | null {
  const parts = [route.repositoryId, route.recoveryPointId, route.parentEntryId, route.savedSearchId].filter(
    (value): value is string => value !== undefined
  );
  return parts.length === 0 ? null : parts.join(":");
}

function ContextDialog({
  triggerLabel,
  title,
  children,
}: {
  triggerLabel: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="ghost" size="sm" aria-label={triggerLabel}>
          <FolderTree className="size-4" aria-hidden />
          <span className="hidden sm:inline">{triggerLabel}</span>
        </Button>
      </DialogTrigger>
      <DialogContent size="sm" aria-label={title} aria-describedby={undefined}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogCloseButton aria-label={title} />
        </DialogHeader>
        <DialogBody className="max-h-[70dvh] overflow-y-auto p-0">{children}</DialogBody>
      </DialogContent>
    </Dialog>
  );
}

function WorkspacePendingState({ icon: Icon, text }: { icon: typeof FolderTree; text: string }) {
  return (
    <div className="flex min-h-56 flex-1 flex-col items-center justify-center gap-2 px-4 text-center text-sm text-muted-foreground">
      <Icon className="size-5" aria-hidden />
      <span>{text}</span>
    </div>
  );
}

function RecoveryPointBlockedState({
  issue,
  recoveryPoint,
  onReturn,
}: {
  issue: NonNullable<BackupAssetsController["semanticIssue"]>;
  recoveryPoint: BackupAssetsController["selectedRecoveryPoint"];
  onReturn: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex min-h-56 flex-1 flex-col justify-center gap-3 p-4">
      <InlineAlert tone="warning">{t(issue.translationKey)}</InlineAlert>
      {recoveryPoint ? (
        <div className="min-w-0 border-y border-border py-3">
          <p className="truncate text-sm font-medium" title={recoveryPoint.producingTaskName}>
            {recoveryPoint.producingTaskName}
          </p>
          <div className="mt-2 flex flex-wrap gap-1.5">
            <Badge tone="warning">
              {t(`backupAssets.codes.recoveryPoint.${recoveryPoint.state}`)}
            </Badge>
            <Badge tone="neutral">
              {t(`backupAssets.codes.physical.${recoveryPoint.physicalAvailability}`)}
            </Badge>
          </div>
        </div>
      ) : null}
      <div>
        <Button type="button" variant="outline" size="sm" onClick={onReturn}>
          {t("backupAssets.actions.returnRepositoryContext")}
        </Button>
      </div>
    </div>
  );
}

function FilterBlockedState({
  issue,
  onClear,
}: {
  issue: NonNullable<BackupAssetsController["filterIssue"]>;
  onClear: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex min-h-56 flex-1 flex-col justify-center gap-3 p-4">
      <InlineAlert tone="warning">{t(issue.translationKey)}</InlineAlert>
      <div>
        <Button type="button" variant="outline" size="sm" onClick={onClear}>
          {t("backupAssets.actions.clearUnavailableFilter")}
        </Button>
      </div>
    </div>
  );
}

function findAvailable<T extends { id: string }>(
  items: Array<CatalogProjection<T>>,
  id: string | undefined
): T | null {
  if (!id) return null;
  const item = items.find((candidate) => candidate.status === "available" && candidate.value.id === id);
  return item?.status === "available" ? item.value : null;
}

function useBackupAssetsViewport(): BackupAssetsViewport {
  const [viewport, setViewport] = useState<BackupAssetsViewport>(() => viewportFromWidth(window.innerWidth));
  useEffect(() => {
    const handleResize = () => setViewport(viewportFromWidth(window.innerWidth));
    window.addEventListener("resize", handleResize);
    return () => window.removeEventListener("resize", handleResize);
  }, []);
  return viewport;
}

function useBackupAssetsOnline(): boolean {
  const [online, setOnline] = useState(() => typeof navigator === "undefined" || navigator.onLine !== false);

  useEffect(() => {
    if (typeof window === "undefined") return undefined;
    const update = () => setOnline(typeof navigator === "undefined" || navigator.onLine !== false);
    window.addEventListener("online", update);
    window.addEventListener("offline", update);
    return () => {
      window.removeEventListener("online", update);
      window.removeEventListener("offline", update);
    };
  }, []);

  return online;
}

function viewportFromWidth(width: number): BackupAssetsViewport {
  if (width >= 1280) return "desktop";
  if (width >= 640) return "intermediate";
  return "mobile";
}
