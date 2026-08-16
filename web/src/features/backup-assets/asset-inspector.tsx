import { useEffect, useRef } from "react";
import { ChevronLeft, ChevronRight, Heart, HeartOff, RotateCcw, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import type { BackupAsset, BackupAssetFavorite, BackupRecoveryPoint } from "@/types/domain";

import type { BackupAssetsInspectorTab } from "./backup-assets-route-state";
import { AssetVersions } from "./asset-versions";

const INSPECTOR_TABS: BackupAssetsInspectorTab[] = [
  "preview",
  "metadata",
  "versions",
  "security",
  "evidence",
  "diff",
];

export interface AssetInspectorProps {
  asset: BackupAsset;
  recoveryPoint: BackupRecoveryPoint;
  activeTab: BackupAssetsInspectorTab;
  preview: React.ReactNode;
  evidence: React.ReactNode;
  diff: React.ReactNode;
  canManageFavorite?: boolean;
  favoriteState?: BackupAssetFavorite["state"] | null;
  favoritePending?: boolean;
  canRecover?: boolean;
  onToggleFavorite?: () => void;
  onRecover?: () => void;
  onTabChange: (tab: BackupAssetsInspectorTab) => void;
  onPrevious: () => void;
  onNext: () => void;
  hasPrevious: boolean;
  hasNext: boolean;
  onClose: () => void;
}

export function AssetInspector({
  asset,
  recoveryPoint,
  activeTab,
  preview,
  evidence,
  diff,
  canManageFavorite = false,
  favoriteState,
  favoritePending = false,
  canRecover = false,
  onToggleFavorite,
  onRecover,
  onTabChange,
  onPrevious,
  onNext,
  hasPrevious,
  hasNext,
  onClose,
}: AssetInspectorProps) {
  const { t } = useTranslation();
  const titleRef = useRef<HTMLHeadingElement | null>(null);
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const activeLabel = t(`backupAssets.inspector.tabs.${activeTab}`);
  const favoriteMembershipKnown = favoriteState !== undefined;
  const isFavorite = favoriteState !== null && favoriteState !== undefined;
  const favoriteLabel = t(
    isFavorite ? "backupAssets.actions.removeFavorite" : "backupAssets.actions.addFavorite"
  );

  useEffect(() => {
    titleRef.current?.focus();
  }, [asset.ref.entryId, asset.ref.recoveryPointId]);

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <header className="flex h-11 shrink-0 items-center gap-1 border-b border-border px-2">
        <div className="min-w-0 flex-1 px-1">
          <h2 ref={titleRef} className="truncate text-sm font-medium" title={asset.name} tabIndex={-1}>
            {asset.name}
          </h2>
        </div>
        {canManageFavorite && favoriteMembershipKnown && onToggleFavorite ? (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="size-8 shrink-0"
            aria-label={favoriteLabel}
            title={favoriteLabel}
            disabled={favoritePending}
            onClick={onToggleFavorite}
          >
            {isFavorite ? (
              <HeartOff className="size-4" aria-hidden />
            ) : (
              <Heart className="size-4" aria-hidden />
            )}
          </Button>
        ) : null}
        {canRecover && onRecover ? (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="size-8 shrink-0"
            aria-label={t("backupAssets.actions.recoverThisItem")}
            title={t("backupAssets.actions.recoverThisItem")}
            onClick={onRecover}
          >
            <RotateCcw className="size-4" aria-hidden />
          </Button>
        ) : null}
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-8 shrink-0"
          aria-label={t("backupAssets.actions.previousAsset")}
          title={t("backupAssets.actions.previousAsset")}
          disabled={!hasPrevious}
          onClick={onPrevious}
        >
          <ChevronLeft className="size-4" aria-hidden />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-8 shrink-0"
          aria-label={t("backupAssets.actions.nextAsset")}
          title={t("backupAssets.actions.nextAsset")}
          disabled={!hasNext}
          onClick={onNext}
        >
          <ChevronRight className="size-4" aria-hidden />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-8 shrink-0"
          aria-label={t("backupAssets.actions.closeInspector")}
          title={t("backupAssets.actions.closeInspector")}
          onClick={onClose}
        >
          <X className="size-4" aria-hidden />
        </Button>
      </header>

      <div
        role="tablist"
        aria-label={t("backupAssets.inspector.tabList")}
        className="flex h-10 shrink-0 overflow-x-auto border-b border-border px-1"
      >
        {INSPECTOR_TABS.map((tab, index) => (
          <button
            key={tab}
            ref={(node) => {
              tabRefs.current[index] = node;
            }}
            id={`backup-assets-inspector-tab-${tab}`}
            type="button"
            role="tab"
            aria-selected={activeTab === tab}
            aria-controls={`backup-assets-inspector-panel-${tab}`}
            tabIndex={activeTab === tab ? 0 : -1}
            className="h-10 shrink-0 border-b-2 border-transparent px-2 text-xs text-muted-foreground outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 aria-selected:border-primary aria-selected:text-foreground"
            onClick={() => onTabChange(tab)}
            onKeyDown={(event) =>
              handleTabKeyDown(event, tab, onTabChange, (targetIndex) => {
                tabRefs.current[targetIndex]?.focus();
              })
            }
          >
            {t(`backupAssets.inspector.tabs.${tab}`)}
          </button>
        ))}
      </div>

      {INSPECTOR_TABS.map((tab) => (
        <section
          key={tab}
          id={`backup-assets-inspector-panel-${tab}`}
          role="tabpanel"
          aria-labelledby={`backup-assets-inspector-tab-${tab}`}
          aria-label={t(`backupAssets.inspector.tabs.${tab}`)}
          hidden={activeTab !== tab}
          className="min-h-0 flex-1 overflow-y-auto"
        >
          {activeTab === tab ? panelFor(tab, asset, recoveryPoint, preview, evidence, diff, t) : null}
        </section>
      ))}

      <span className="sr-only" aria-live="polite">
        {activeLabel}
      </span>
    </div>
  );
}

function handleTabKeyDown(
  event: React.KeyboardEvent<HTMLButtonElement>,
  tab: BackupAssetsInspectorTab,
  onTabChange: (tab: BackupAssetsInspectorTab) => void,
  onFocusTab: (index: number) => void
) {
  const currentIndex = INSPECTOR_TABS.indexOf(tab);
  let nextIndex: number | null = null;
  if (event.key === "ArrowRight") nextIndex = (currentIndex + 1) % INSPECTOR_TABS.length;
  else if (event.key === "ArrowLeft") nextIndex = (currentIndex - 1 + INSPECTOR_TABS.length) % INSPECTOR_TABS.length;
  else if (event.key === "Home") nextIndex = 0;
  else if (event.key === "End") nextIndex = INSPECTOR_TABS.length - 1;
  if (nextIndex === null) return;
  event.preventDefault();
  onTabChange(INSPECTOR_TABS[nextIndex]);
  requestAnimationFrame(() => onFocusTab(nextIndex));
}

function panelFor(
  tab: BackupAssetsInspectorTab,
  asset: BackupAsset,
  recoveryPoint: BackupRecoveryPoint,
  preview: React.ReactNode,
  evidence: React.ReactNode,
  diff: React.ReactNode,
  t: (key: string, options?: Record<string, unknown>) => string
) {
  if (tab === "preview") return preview;
  if (tab === "versions") return <AssetVersions recoveryPoint={recoveryPoint} />;
  if (tab === "evidence") return evidence;
  if (tab === "diff") return diff;
  if (tab === "security") {
    return (
      <div className="p-3">
        <div className="border-y border-border py-3 text-sm" role="status">
          {t("backupAssets.inspector.securityUnknown")}
        </div>
      </div>
    );
  }
  return <AssetMetadata asset={asset} t={t} />;
}

function AssetMetadata({
  asset,
  t,
}: {
  asset: BackupAsset;
  t: (key: string, options?: Record<string, unknown>) => string;
}) {
  const rows = [
    [t("backupAssets.inspector.metadata.mimeType"), asset.mimeType || "-"],
    [t("backupAssets.inspector.metadata.entryType"), t(`backupAssets.inspector.entryType.${asset.entryType}`)],
    [t("backupAssets.inspector.metadata.size"), String(asset.size)],
    [t("backupAssets.inspector.metadata.modified"), asset.modifiedAt?.slice(0, 16).replace("T", " ") ?? "-"],
    [t("backupAssets.inspector.metadata.mode"), asset.mode || "-"],
    [t("backupAssets.inspector.metadata.owner"), asset.owner || "-"],
    [t("backupAssets.inspector.metadata.fingerprint"), asset.fingerprintStrength],
  ];
  return (
    <dl className="divide-y divide-border px-3">
      {rows.map(([label, value]) => (
        <div key={label} className="grid grid-cols-[minmax(7rem,0.8fr)_minmax(0,1.2fr)] gap-3 py-2 text-xs">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="min-w-0 break-words text-right">{value}</dd>
        </div>
      ))}
    </dl>
  );
}
