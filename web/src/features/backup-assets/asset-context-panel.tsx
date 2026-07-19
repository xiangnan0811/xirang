import { Database, FolderClock, Heart, History, Search, Tags } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { Select } from "@/components/ui/select";
import { Tree, type TreeItemData } from "@/components/ui/tree";
import type {
  BackupRecoveryPoint,
  BackupRepository,
  CatalogProjection,
} from "@/types/domain";

import type { BackupAssetsRouteState } from "./backup-assets-route-state";
import type { BackupAssetResultRow } from "./backup-assets-state";
import {
  backupAssetsImmutabilityKey,
  backupAssetsProviderKey,
  backupAssetsVersionModeKey,
  presentBackupAssetsCode,
} from "./backup-assets-presenters";
import type { BackupAssetsCollectionResource } from "./use-backup-assets-state";

const ROOT_DIRECTORY_ID = "backup-assets-root";

export interface AssetContextOverlayCounts {
  savedSearches: number;
  favorites: number;
  tags: number;
  recent: number;
}

export interface AssetContextPanelProps {
  route: BackupAssetsRouteState;
  repositories: BackupAssetsCollectionResource<CatalogProjection<BackupRepository>>;
  recoveryPoints: BackupAssetsCollectionResource<CatalogProjection<BackupRecoveryPoint>>;
  selectedRepository: BackupRepository | null;
  selectedRecoveryPoint: BackupRecoveryPoint | null;
  directoryRows: BackupAssetResultRow[];
  overlayCounts: AssetContextOverlayCounts;
  onRoutePatch: (patch: Partial<BackupAssetsRouteState>) => void;
  onOverlaySectionChange: (
    section: "saved" | "favorites" | "tags" | "recent",
    trigger: HTMLButtonElement
  ) => void;
}

export function AssetContextPanel({
  route,
  repositories,
  recoveryPoints,
  selectedRepository,
  selectedRecoveryPoint,
  directoryRows,
  overlayCounts,
  onRoutePatch,
  onOverlaySectionChange,
}: AssetContextPanelProps) {
  const { t } = useTranslation();

  if (repositories.status === "loading" || repositories.status === "idle") {
    return <LoadingState title={t("backupAssets.context.loadingRepositories")} rows={4} />;
  }
  if (repositories.status === "blocked" || repositories.status === "error") {
    return (
      <InlineAlert tone={repositories.status === "blocked" ? "warning" : "critical"}>
        {t(repositories.error?.translationKey ?? "backupAssets.errors.unknown")}
      </InlineAlert>
    );
  }

  const availableRepositories = availableValues(repositories.items);
  const availableRecoveryPoints = availableValues(recoveryPoints.items);
  const catalog = selectedRecoveryPoint?.catalog.status === "available" ? selectedRecoveryPoint.catalog.value : null;
  const contentReason = catalog?.contentAvailability.reason
    ? presentBackupAssetsCode("capability", catalog.contentAvailability.reason.code)
    : null;
  const directoryTree = buildDirectoryTree(
    directoryRows,
    route.parentEntryId,
    t("backupAssets.context.rootDirectory"),
    t("backupAssets.context.currentDirectory")
  );

  return (
    <div className="flex min-h-0 flex-col gap-4 p-3">
      <div className="space-y-1.5">
        <label htmlFor="backup-assets-repository" className="text-xs font-medium text-muted-foreground">
          {t("backupAssets.context.repository")}
        </label>
        <Select
          id="backup-assets-repository"
          aria-label={t("backupAssets.context.repository")}
          value={route.repositoryId ?? ""}
          onChange={(event) =>
            onRoutePatch({ repositoryId: event.target.value === "" ? undefined : event.target.value })
          }
        >
          <option value="">{t("backupAssets.context.selectRepository")}</option>
          {availableRepositories.map((repository) => (
            <option key={repository.id} value={repository.id}>
              {repository.displayName}
            </option>
          ))}
        </Select>
      </div>

      {selectedRepository ? (
        <div className="min-w-0 space-y-2 border-y border-border py-3">
          <div className="flex min-w-0 items-center gap-2">
            <Database className="size-4 shrink-0 text-primary" aria-hidden />
            <span className="truncate text-sm font-medium" title={selectedRepository.displayName}>
              {selectedRepository.displayName}
            </span>
          </div>
          <div className="flex flex-wrap gap-1.5">
            <Badge tone="neutral">{t(backupAssetsProviderKey(selectedRepository.providerKind))}</Badge>
            <Badge tone={selectedRepository.status === "online" ? "success" : "warning"}>
              {t(`backupAssets.codes.repositoryStatus.${selectedRepository.status}`)}
            </Badge>
            <Badge tone="info">{t(backupAssetsVersionModeKey(selectedRepository.versionMode))}</Badge>
            <Badge tone="neutral">{t(backupAssetsImmutabilityKey(selectedRepository.immutabilityLevel))}</Badge>
          </div>
        </div>
      ) : null}

      {selectedRepository ? (
        <div className="space-y-1.5">
          <label htmlFor="backup-assets-recovery-point" className="text-xs font-medium text-muted-foreground">
            {t("backupAssets.context.recoveryPoint")}
          </label>
          <Select
            id="backup-assets-recovery-point"
            aria-label={t("backupAssets.context.recoveryPoint")}
            value={route.recoveryPointId ?? ""}
            disabled={recoveryPoints.status === "loading"}
            onChange={(event) =>
              onRoutePatch({ recoveryPointId: event.target.value === "" ? undefined : event.target.value })
            }
          >
            <option value="">{t("backupAssets.context.selectRecoveryPoint")}</option>
            {availableRecoveryPoints.map((point) => (
              <option key={point.id} value={point.id}>
                {point.producingTaskName} · {formatTimestamp(point.capturedAt)}
              </option>
            ))}
          </Select>
        </div>
      ) : null}

      {selectedRecoveryPoint ? (
        <div className="space-y-2 border-b border-border pb-3">
          <div className="flex items-center gap-2 text-sm font-medium">
            <FolderClock className="size-4 text-primary" aria-hidden />
            <span className="min-w-0 truncate" title={selectedRecoveryPoint.producingTaskName}>
              {selectedRecoveryPoint.producingTaskName}
            </span>
          </div>
          <dl className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-1 text-xs">
            <dt className="text-muted-foreground">{t("backupAssets.context.lifecycle")}</dt>
            <dd>{t(`backupAssets.codes.recoveryPoint.${selectedRecoveryPoint.state}`)}</dd>
            <dt className="text-muted-foreground">{t("backupAssets.context.physical")}</dt>
            <dd>{t(`backupAssets.codes.physical.${selectedRecoveryPoint.physicalAvailability}`)}</dd>
            <dt className="text-muted-foreground">{t("backupAssets.context.catalog")}</dt>
            <dd>{t(`backupAssets.codes.coverage.${catalog?.coverage.status ?? "unavailable"}`)}</dd>
            <dt className="text-muted-foreground">{t("backupAssets.context.content")}</dt>
            <dd className="text-right">
              <span>
                {catalog?.contentAvailability.available
                  ? t("backupAssets.context.contentAvailable")
                  : t("backupAssets.context.contentUnavailable")}
              </span>
              {!catalog?.contentAvailability.available && contentReason ? (
                <span className="mt-0.5 block text-muted-foreground">{t(contentReason.translationKey)}</span>
              ) : null}
            </dd>
          </dl>
        </div>
      ) : null}

      {selectedRecoveryPoint && route.view === "browse" ? (
        <div className="min-w-0 border-b border-border pb-3">
          <Tree
            items={directoryTree}
            selected={route.parentEntryId ?? ROOT_DIRECTORY_ID}
            expanded={expandedTreeIds(directoryTree)}
            onSelect={(item) =>
              onRoutePatch({
                parentEntryId: item.id === ROOT_DIRECTORY_ID ? undefined : item.id,
                entryId: undefined,
              })
            }
          />
        </div>
      ) : null}

      <div className="grid gap-1" aria-label={t("backupAssets.context.personalViews")}>
        <ContextSectionButton
          icon={Search}
          label={t("backupAssets.context.savedSearches")}
          count={overlayCounts.savedSearches}
          onClick={(trigger) => onOverlaySectionChange("saved", trigger)}
        />
        <ContextSectionButton
          icon={Heart}
          label={t("backupAssets.context.favorites")}
          count={overlayCounts.favorites}
          onClick={(trigger) => onOverlaySectionChange("favorites", trigger)}
        />
        <ContextSectionButton
          icon={Tags}
          label={t("backupAssets.context.tags")}
          count={overlayCounts.tags}
          onClick={(trigger) => onOverlaySectionChange("tags", trigger)}
        />
        <ContextSectionButton
          icon={History}
          label={t("backupAssets.context.recent")}
          count={overlayCounts.recent}
          onClick={(trigger) => onOverlaySectionChange("recent", trigger)}
        />
      </div>
    </div>
  );
}

function buildDirectoryTree(
  rows: BackupAssetResultRow[],
  parentEntryId: string | undefined,
  rootLabel: string,
  currentDirectoryLabel: string
): TreeItemData[] {
  const directories: TreeItemData[] = rows
    .filter((row) => row.asset.entryType === "directory")
    .map((row) => ({ id: row.ref.entryId, label: row.asset.name, isDir: true }));

  if (!parentEntryId) {
    return [{ id: ROOT_DIRECTORY_ID, label: rootLabel, isDir: true, children: directories }];
  }

  const breadcrumb = rows.find((row) => row.asset.breadcrumb.length > 0)?.asset.breadcrumb ?? [];
  const chain: TreeItemData[] = breadcrumb.map((item) => ({
    id: item.ref.entryId,
    label: item.name,
    isDir: true,
  }));
  if (!chain.some((item) => item.id === parentEntryId)) {
    chain.push({ id: parentEntryId, label: currentDirectoryLabel, isDir: true });
  }

  let children = directories.filter((directory) => !chain.some((item) => item.id === directory.id));
  for (let index = chain.length - 1; index >= 0; index -= 1) {
    children = [{ ...chain[index], children }];
  }
  return [{ id: ROOT_DIRECTORY_ID, label: rootLabel, isDir: true, children }];
}

function expandedTreeIds(items: TreeItemData[]): Set<string> {
  const expanded = new Set<string>();
  const visit = (nodes: TreeItemData[]) => {
    for (const node of nodes) {
      if (node.children && node.children.length > 0) {
        expanded.add(node.id);
        visit(node.children);
      }
    }
  };
  visit(items);
  return expanded;
}

interface ContextSectionButtonProps {
  icon: typeof Search;
  label: string;
  count: number;
  onClick: (trigger: HTMLButtonElement) => void;
}

function ContextSectionButton({ icon: Icon, label, count, onClick }: ContextSectionButtonProps) {
  return (
    <Button
      variant="ghost"
      className="h-9 w-full justify-start gap-2 px-2"
      aria-label={`${label} ${count}`}
      onClick={(event) => onClick(event.currentTarget)}
    >
      <Icon className="size-4" aria-hidden />
      <span className="min-w-0 flex-1 truncate text-left">{label}</span>
      <span className="w-7 text-right text-xs tabular-nums text-muted-foreground">{count}</span>
    </Button>
  );
}

function availableValues<T>(items: Array<CatalogProjection<T>>): T[] {
  return items.flatMap((item) => (item.status === "available" ? [item.value] : []));
}

function formatTimestamp(value: string | null): string {
  return value ? value.slice(0, 16).replace("T", " ") : "-";
}
