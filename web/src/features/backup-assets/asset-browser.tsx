import { useCallback, useMemo, useState, type ReactNode } from "react";
import { FolderSearch, LoaderCircle, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Select } from "@/components/ui/select";
import { ViewModeToggle } from "@/components/ui/view-mode-toggle";

import { AssetBulkBar } from "./asset-bulk-bar";
import { AssetGrid } from "./asset-grid";
import { AssetList } from "./asset-list";
import { AssetSearch } from "./asset-search";
import {
  assetRefKey,
  type BackupAssetResultRow,
  type BackupAssetsRestorationAnchor,
  type BackupAssetsState,
} from "./backup-assets-state";
import type { BackupAssetsRouteState, BackupAssetsScope } from "./backup-assets-route-state";

export interface AssetBrowserProps {
  state: BackupAssetsState;
  onRoutePatch: (patch: Partial<BackupAssetsRouteState>) => void;
  onSearch: (query: string, scope: BackupAssetsScope) => void;
  onSearchDraftChange: (value: string) => void;
  onToggleSelection: (ref: BackupAssetResultRow["ref"]) => void;
  onClearSelection: () => void;
  canExport?: boolean;
  onExport?: () => void;
  onOpen: (row: BackupAssetResultRow, position: Pick<BackupAssetsRestorationAnchor, "index" | "offset">) => void;
  onLoadMore: () => void;
  restorationAnchor?: BackupAssetsRestorationAnchor | null;
  onRestorationComplete?: () => void;
}

export function AssetBrowser({
  state,
  onRoutePatch,
  onSearch,
  onSearchDraftChange,
  onToggleSelection,
  onClearSelection,
  canExport = false,
  onExport,
  onOpen,
  onLoadMore,
  restorationAnchor = null,
  onRestorationComplete,
}: AssetBrowserProps) {
  const { t } = useTranslation();
  const [activeKey, setActiveKey] = useState<string | null>(() => routeActiveKey(state));
  const resolvedActiveKey = routeActiveKey(state) ?? activeKey;
  const finishRestoration = useCallback(() => {
    if (restorationAnchor) setActiveKey(assetRefKey(restorationAnchor.ref));
    onRestorationComplete?.();
  }, [onRestorationComplete, restorationAnchor]);

  const selectedKeys = useMemo(() => new Set(state.selection.keys()), [state.selection]);
  const selectedRow =
    state.selection.size === 1
      ? state.result.rows.find((row) => state.selection.has(assetRefKey(row.ref))) ?? null
      : null;

  const handleActiveChange = (row: BackupAssetResultRow) => setActiveKey(assetRefKey(row.ref));

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <div className="grid h-24 shrink-0 grid-cols-[minmax(0,1fr)_auto] grid-rows-[36px_36px] items-center gap-2 border-b border-border px-2 py-2">
        <AssetSearch
          draft={state.searchDraft}
          scope={state.route.scope}
          disabled={state.result.status === "loading" && state.result.rows.length === 0}
          onDraftChange={onSearchDraftChange}
          onScopeChange={(scope) => onRoutePatch({ scope })}
          onSearch={onSearch}
        />
        <Select
          aria-label={t("backupAssets.browser.sort")}
          value={sortValue(state.route)}
          containerClassName="w-40 max-w-full min-w-0"
          onChange={(event) => onRoutePatch(sortPatch(event.target.value))}
        >
          {state.route.view === "search" ? (
            <option value="relevance_desc">{t("backupAssets.sort.relevance")}</option>
          ) : null}
          <option value="name_asc">{t("backupAssets.sort.nameAsc")}</option>
          {state.route.view === "browse" ? (
            <option value="name_desc">{t("backupAssets.sort.nameDesc")}</option>
          ) : null}
          {state.route.view === "browse" ? (
            <option value="size_desc">{t("backupAssets.sort.sizeDesc")}</option>
          ) : null}
          <option value="modified_at_desc">{t("backupAssets.sort.modifiedDesc")}</option>
        </Select>
        <ViewModeToggle
          value={state.route.layout === "grid" ? "cards" : "list"}
          onChange={(mode) => onRoutePatch({ layout: mode === "cards" ? "grid" : "list" })}
          groupLabel={t("backupAssets.browser.layout")}
          cardsButtonLabel={t("backupAssets.browser.grid")}
          listButtonLabel={t("backupAssets.browser.list")}
          cardsText={t("backupAssets.browser.grid")}
          listText={t("backupAssets.browser.list")}
          className="shrink-0 justify-self-end"
        />
      </div>

      {state.result.coverage === "partial" ? (
        <InlineAlert tone="info" className="m-2 shrink-0">
          {t("backupAssets.states.partialCoverage")}
        </InlineAlert>
      ) : null}

      <ResultBody
        state={state}
        activeKey={resolvedActiveKey}
        selectedKeys={selectedKeys}
        onActiveChange={handleActiveChange}
        onSelectionToggle={onToggleSelection}
        onOpen={onOpen}
        restorationAnchor={restorationAnchor}
        onRestorationComplete={finishRestoration}
      />

      {state.result.nextCursor ? (
        <div className="flex h-11 shrink-0 items-center justify-center border-t border-border">
          <Button type="button" variant="ghost" size="sm" onClick={onLoadMore}>
            {state.result.status === "loading" ? (
              <LoaderCircle className="size-4 animate-spin" aria-hidden />
            ) : (
              <RefreshCw className="size-4" aria-hidden />
            )}
            {t("backupAssets.actions.loadMore")}
          </Button>
        </div>
      ) : null}

      <AssetBulkBar
        count={state.selection.size}
        canExport={canExport}
        onClear={onClearSelection}
        onExport={onExport}
        onInspect={() => {
          if (selectedRow) {
            onOpen(selectedRow, {
              index: state.result.rows.indexOf(selectedRow),
              offset: 0,
            });
          }
        }}
      />
      <span className="sr-only" aria-live="polite">
        {t("backupAssets.browser.resultSummary", { count: state.result.rows.length })}
      </span>
    </div>
  );
}

function ResultBody({
  state,
  activeKey,
  selectedKeys,
  onActiveChange,
  onSelectionToggle,
  onOpen,
  restorationAnchor,
  onRestorationComplete,
}: {
  state: BackupAssetsState;
  activeKey: string | null;
  selectedKeys: ReadonlySet<string>;
  onActiveChange: (row: BackupAssetResultRow) => void;
  onSelectionToggle: (ref: BackupAssetResultRow["ref"]) => void;
  onOpen: (row: BackupAssetResultRow, position: Pick<BackupAssetsRestorationAnchor, "index" | "offset">) => void;
  restorationAnchor: BackupAssetsRestorationAnchor | null;
  onRestorationComplete?: () => void;
}) {
  const { t } = useTranslation();
  if (state.result.status === "loading" && state.result.rows.length === 0) {
    return <BrowserState icon={<LoaderCircle className="size-5 animate-spin" />} text={t("backupAssets.states.loadingAssets")} />;
  }
  if (state.result.status === "failed") {
    return (
      <div className="p-3">
        <InlineAlert tone="critical">{t("backupAssets.errors.unknown")}</InlineAlert>
      </div>
    );
  }
  if (state.result.rows.length === 0) {
    const authoritative = state.result.status === "ready" && state.result.authoritativeEmpty;
    return (
      <BrowserState
        icon={<FolderSearch className="size-5" />}
        text={
          authoritative
            ? t("backupAssets.states.noMatchingAssets")
            : state.route.recoveryPointId
              ? t("backupAssets.states.noIndexedResults")
              : t("backupAssets.states.selectRecoveryPoint")
        }
      />
    );
  }
  const props = {
    rows: state.result.rows,
    selectedKeys,
    activeKey,
    onActiveChange,
    onSelectionToggle,
    onOpen,
    restorationAnchor,
    onRestorationComplete,
  };
  return state.route.layout === "grid" ? <AssetGrid {...props} /> : <AssetList {...props} />;
}

function BrowserState({ icon, text }: { icon: ReactNode; text: string }) {
  return (
    <div className="flex min-h-56 flex-1 flex-col items-center justify-center gap-2 px-4 text-center text-sm text-muted-foreground">
      <span aria-hidden>{icon}</span>
      <span>{text}</span>
    </div>
  );
}

function routeActiveKey(state: BackupAssetsState): string | null {
  return state.route.recoveryPointId && state.route.entryId
    ? assetRefKey({ recoveryPointId: state.route.recoveryPointId, entryId: state.route.entryId })
    : null;
}

function sortValue(route: BackupAssetsRouteState): string {
  return `${route.sort}_${route.direction}`;
}

function sortPatch(value: string): Pick<BackupAssetsRouteState, "sort" | "direction"> {
  if (value === "name_desc") return { sort: "name", direction: "desc" };
  if (value === "size_desc") return { sort: "size", direction: "desc" };
  if (value === "modified_at_desc") return { sort: "modified_at", direction: "desc" };
  if (value === "relevance_desc") return { sort: "relevance", direction: "desc" };
  return { sort: "name", direction: "asc" };
}
