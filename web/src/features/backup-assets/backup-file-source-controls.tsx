import { Layers3, Server, TimerReset } from "lucide-react";
import { useTranslation } from "react-i18next";

import { InlineAlert } from "@/components/ui/inline-alert";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import type { BackupFileSourceNode, BackupFileSourceSet, BackupFileSourceVersion } from "@/types/domain";

export interface BackupFileSourceControlsProps {
  status: "loading" | "ready" | "empty" | "partial" | "blocked" | "permission_denied";
  nodes: readonly BackupFileSourceNode[];
  sets: readonly BackupFileSourceSet[];
  versions: readonly BackupFileSourceVersion[];
  selectedNodeId?: number;
  selectedBackupSetId?: string;
  selectedRecoveryPointId?: string;
  onSelectNode: (nodeId: number | undefined) => void;
  onSelectSet: (backupSetId: string | undefined) => void;
  onSelectVersion: (version: BackupFileSourceVersion, backupSetId: string) => void;
  hasMoreNodes: boolean;
  hasMoreSets: boolean;
  hasMoreVersions: boolean;
  loadingMoreNodes: boolean;
  loadingMoreSets: boolean;
  loadingMoreVersions: boolean;
  onLoadMoreNodes: () => void | Promise<void>;
  onLoadMoreSets: () => void | Promise<void>;
  onLoadMoreVersions: () => void | Promise<void>;
}

export function BackupFileSourceControls({
  status, nodes, sets, versions, selectedNodeId, selectedBackupSetId, selectedRecoveryPointId,
  onSelectNode, onSelectSet, onSelectVersion,
  hasMoreNodes, hasMoreSets, hasMoreVersions, loadingMoreNodes, loadingMoreSets, loadingMoreVersions,
  onLoadMoreNodes, onLoadMoreSets, onLoadMoreVersions,
}: BackupFileSourceControlsProps) {
  const { t, i18n } = useTranslation();
  const selectedSet = selectedBackupSetId === undefined && !hasMoreSets && sets.length === 1
    ? sets[0]
    : sets.find((item) => item.backupSetId === selectedBackupSetId) ?? null;
  const controlsBlocked = status === "loading" || status === "blocked" || status === "permission_denied";
  const stateLabel = (state: BackupFileSourceNode["browseState"]) => t(`backupAssets.sources.states.${state}`);

  return (
    <section aria-label={t("backupAssets.sources.title")} className="border-y border-border bg-card/45 px-3 py-3">
      <div className="grid gap-3 lg:grid-cols-[minmax(12rem,1fr)_minmax(12rem,1fr)_minmax(15rem,1.35fr)]">
        <SourceSelect icon={Server} label={t("backupAssets.sources.node")}>
          <Select
            aria-label={t("backupAssets.sources.node")}
            className="touch-target h-11 lg:h-9"
            value={selectedNodeId === undefined ? "" : String(selectedNodeId)}
            disabled={controlsBlocked}
            onChange={(event) => onSelectNode(event.target.value === "" ? undefined : Number(event.target.value))}
          >
            <option value="">{t("backupAssets.sources.selectNode")}</option>
            {nodes.map((node) => <option key={node.nodeId} value={node.nodeId}>{node.displayName} · {stateLabel(node.browseState)}</option>)}
          </Select>
          <SourcePaginationButton
            visible={hasMoreNodes}
            loading={loadingMoreNodes}
            label={t("backupAssets.sources.loadMoreNodes")}
            loadingLabel={t("backupAssets.sources.loadingMore")}
            onLoadMore={onLoadMoreNodes}
          />
        </SourceSelect>

        {sets.length > 1 || hasMoreSets ? (
          <SourceSelect icon={Layers3} label={t("backupAssets.sources.set")}>
            <Select
              aria-label={t("backupAssets.sources.set")}
              className="touch-target h-11 lg:h-9"
              value={selectedBackupSetId ?? ""}
              disabled={controlsBlocked || selectedNodeId === undefined}
              onChange={(event) => onSelectSet(event.target.value || undefined)}
            >
              <option value="">{t("backupAssets.sources.selectSet")}</option>
              {sets.map((set) => <option key={set.backupSetId} value={set.backupSetId}>{set.displayLabel} · {stateLabel(set.browseState)}</option>)}
            </Select>
            <SourcePaginationButton
              visible={hasMoreSets}
              loading={loadingMoreSets}
              label={t("backupAssets.sources.loadMoreSets")}
              loadingLabel={t("backupAssets.sources.loadingMore")}
              onLoadMore={onLoadMoreSets}
            />
          </SourceSelect>
        ) : (
          <div className="min-w-0 border-l-2 border-primary/40 pl-3">
            <div className="text-[11px] font-medium uppercase tracking-[0.12em] text-muted-foreground">{t("backupAssets.sources.set")}</div>
            <div className="mt-1 truncate text-sm font-medium">
              {selectedSet ? `${selectedSet.displayLabel} · ${stateLabel(selectedSet.browseState)}` : t("backupAssets.sources.noSet")}
            </div>
          </div>
        )}

        <SourceSelect icon={TimerReset} label={t("backupAssets.sources.version")}>
          <Select
            aria-label={t("backupAssets.sources.version")}
            className="touch-target h-11 lg:h-9"
            value={selectedRecoveryPointId ?? ""}
            disabled={controlsBlocked || selectedSet === null || versions.length === 0}
            onChange={(event) => {
              const selected = versions.find((item) => item.recoveryPointId === event.target.value);
              if (selected?.browseState === "browsable" && selectedSet) onSelectVersion(selected, selectedSet.backupSetId);
            }}
          >
            <option value="">{t("backupAssets.sources.selectVersion")}</option>
            {versions.map((version) => (
              <option key={version.recoveryPointId} value={version.recoveryPointId} disabled={version.browseState !== "browsable"}>
                {new Intl.DateTimeFormat(i18n.language, { dateStyle: "medium", timeStyle: "short" }).format(new Date(version.capturedAt ?? version.committedAt ?? version.createdAt))}
                {` · ${stateLabel(version.browseState)}`}
              </option>
            ))}
          </Select>
          <SourcePaginationButton
            visible={hasMoreVersions}
            loading={loadingMoreVersions}
            label={t("backupAssets.sources.loadMoreVersions")}
            loadingLabel={t("backupAssets.sources.loadingMore")}
            onLoadMore={onLoadMoreVersions}
          />
        </SourceSelect>
      </div>
      {status === "loading" ? <p role="status" className="mt-2 text-xs text-muted-foreground">{t("backupAssets.sources.loading")}</p> : null}
      {status === "empty" ? <p role="status" className="mt-2 text-xs text-muted-foreground">{t("backupAssets.sources.emptyNodes")}</p> : null}
      {status === "ready" && selectedNodeId !== undefined && sets.length === 0 && !hasMoreSets ? (
        <p role="status" className="mt-2 text-xs text-muted-foreground">{t("backupAssets.sources.emptySets")}</p>
      ) : null}
      {status === "ready" && selectedSet !== null && versions.length === 0 && !hasMoreVersions ? (
        <p role="status" className="mt-2 text-xs text-muted-foreground">{t("backupAssets.sources.emptyVersions")}</p>
      ) : null}
      {selectedSet?.browseState === "indexing" ? <p role="status" className="mt-2 text-xs text-muted-foreground">{t("backupAssets.sources.indexing")}</p> : null}
      {selectedSet?.browseState === "unavailable" ? <InlineAlert tone="warning" className="mt-2">{t("backupAssets.sources.unavailable")}</InlineAlert> : null}
      {status === "partial" ? <p role="status" className="mt-2 text-xs text-warning">{t("backupAssets.sources.partial")}</p> : null}
      {status === "permission_denied" ? <InlineAlert tone="warning" className="mt-2">{t("backupAssets.sources.permissionDenied")}</InlineAlert> : null}
      {status === "blocked" ? <InlineAlert tone="warning" className="mt-2">{t("backupAssets.sources.blocked")}</InlineAlert> : null}
    </section>
  );
}

function SourceSelect({ icon: Icon, label, children }: { icon: typeof Server; label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0 space-y-1.5">
      <span className="flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
        <Icon className="size-3.5" aria-hidden />{label}
      </span>
      {children}
    </div>
  );
}

function SourcePaginationButton({
  visible,
  loading,
  label,
  loadingLabel,
  onLoadMore,
}: {
  visible: boolean;
  loading: boolean;
  label: string;
  loadingLabel: string;
  onLoadMore: () => void | Promise<void>;
}) {
  if (!visible) return null;
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      className="touch-target min-h-11 w-full lg:min-h-8"
      disabled={loading}
      onClick={() => { void onLoadMore(); }}
    >
      {loading ? loadingLabel : label}
    </Button>
  );
}
