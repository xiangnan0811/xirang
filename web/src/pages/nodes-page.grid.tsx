import React, { useId } from "react";
import { useTranslation } from "react-i18next";
import { ServerCog } from "lucide-react";
import { Link } from "react-router-dom";
import { ExpiryCountdownBadge } from "@/components/expiry-countdown-badge";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { LoadingState } from "@/components/ui/loading-state";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { getNodeStatusMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import { NodeActionsMenu } from "@/pages/nodes-page.actions-menu";
import type { NodesViewProps } from "@/pages/nodes-page.utils";
import type { NodeRecord } from "@/types/domain";

type NodeCardProps = Omit<
  NodesViewProps,
  | "loading"
  | "requestFailed"
  | "inventoryEmpty"
  | "sortedNodes"
  | "selectedNodeSet"
  | "selectedNodeIds"
  | "allVisibleSelected"
  | "toggleSelectAllVisible"
  | "handleBulkDelete"
  | "resetFilters"
  | "openCreateDialog"
> & {
  node: NodeRecord;
  checked: boolean;
  /** 桌面卡片沿用既有可见名称；移动卡片以节点名链接命名，避免同名重复。 */
  labelledByName: boolean;
};

const NodeCard = React.memo(function NodeCard({
  node,
  checked,
  labelledByName,
  sshKeys,
  testingNodeId,
  doctorNodeId,
  triggeringNodeId,
  emergencyNodeId,
  toggleNodeSelection,
  openEditDialog,
  onTestNode,
  onOpenDoctor,
  onDeleteNode,
  handleTriggerBackup,
  onOpenTerminal,
  onOpenFileBrowser,
  onEmergencyBackup,
  onMigrate,
  isAdmin,
  canOperateNodes,
  canBrowseNodeFiles,
}: NodeCardProps) {
  const { t } = useTranslation();
  const nameId = useId();
  const status = getNodeStatusMeta(node.status);
  const keyLabel = node.keyId
    ? sshKeys.find((key) => key.id === node.keyId)?.name || t("common.keyBound")
    : t("common.keyUnbound");

  return (
    <article
      aria-label={labelledByName ? undefined : t("nodes.nodeCardAriaLabel", { name: node.name })}
      aria-labelledby={labelledByName ? nameId : undefined}
      className={cn(
        "rounded-lg border border-border bg-card p-3 shadow-sm transition-shadow",
        checked && "border-primary/45 ring-1 ring-primary/40",
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <label className="inline-flex items-center gap-2 text-xs text-muted-foreground">
          <input
            type="checkbox"
            aria-label={t("nodes.selectNodeAriaLabel", { name: node.name })}
            className="size-4 accent-primary rounded-sm"
            checked={checked}
            onChange={(event) => toggleNodeSelection(node.id, event.target.checked)}
          />
          {t("nodes.selectLabel")}
        </label>
        <div className="inline-flex flex-wrap items-center justify-end gap-1.5">
          <Badge tone={status.variant}>{status.label}</Badge>
          <ExpiryCountdownBadge expiryDate={node.expiryDate} archived={node.archived} />
        </div>
      </div>

      <div className="mt-2 min-w-0">
        <Link
          id={nameId}
          to={`/app/nodes/${node.id}`}
          data-testid={`nodes-list-link-${node.id}`}
          className="break-all font-medium hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {node.name}
        </Link>
        <p className="text-xs text-muted-foreground">
          <span className="break-all">{node.host}:{node.port} · {node.username}</span>
        </p>
      </div>

      <div className="mt-2 space-y-1 text-xs text-muted-foreground">
        <p>
          {t("nodes.authLabel")}
          {node.authType === "key"
            ? t("nodes.authKeyWithLabel", { label: keyLabel })
            : t("nodes.authPassword")}
        </p>
        <p>
          {t("nodes.lastConnectionLabel", { time: node.lastSeenAt || t("nodes.lastConnectionNever") })}
        </p>
        <p>
          {t("nodes.connectionLatency", { latency: node.connectionLatencyMs != null ? `${node.connectionLatencyMs} ms` : "-" })}
        </p>
        <p>{t("nodes.lastBackupLabel", { time: node.lastBackupAt })}</p>
        <p className="break-words">
          {t("nodes.tagsLabel", { tags: node.tags.length ? node.tags.join(" / ") : "-" })}
        </p>
      </div>

      <div className="mt-4 border-t border-border pt-3">
        <NodeActionsMenu
          layout="card"
          node={node}
          testing={testingNodeId === node.id}
          doctorRunning={doctorNodeId === node.id}
          triggering={triggeringNodeId === node.id}
          emergencyRunning={emergencyNodeId === node.id}
          isAdmin={isAdmin}
          canOperateNodes={canOperateNodes}
          canBrowseNodeFiles={canBrowseNodeFiles}
          onTestNode={onTestNode}
          onOpenDoctor={onOpenDoctor}
          handleTriggerBackup={handleTriggerBackup}
          onEmergencyBackup={onEmergencyBackup}
          onOpenTerminal={onOpenTerminal}
          onOpenFileBrowser={onOpenFileBrowser}
          openEditDialog={openEditDialog}
          onMigrate={onMigrate}
          onDeleteNode={onDeleteNode}
        />
      </div>
    </article>
  );
});

export const NodesGrid = React.memo(function NodesGrid({
  loading,
  requestFailed,
  inventoryEmpty,
  sortedNodes,
  selectedNodeSet,
  selectedNodeIds,
  allVisibleSelected,
  toggleSelectAllVisible,
  handleBulkDelete,
  resetFilters,
  openCreateDialog,
  ...cardProps
}: NodesViewProps) {
  const { t } = useTranslation();
  const { isAdmin } = cardProps;

  const renderEmpty = (className?: string) => {
    if (loading || requestFailed || sortedNodes.length) {
      return null;
    }
    return inventoryEmpty ? (
      <FilteredEmptyState
        className={className}
        title={t("nodes.inventoryEmptyTitle")}
        description={t("nodes.inventoryEmptyDesc")}
        onCreate={isAdmin ? openCreateDialog : undefined}
        createLabel={t("nodes.emptyCreateLabel")}
        createIcon={ServerCog}
      />
    ) : (
      <FilteredEmptyState
        className={className}
        title={t("nodes.emptyTitle")}
        description={t("nodes.emptyDesc")}
        onReset={resetFilters}
        onCreate={isAdmin ? openCreateDialog : undefined}
        createLabel={t("nodes.emptyCreateLabel")}
        createIcon={ServerCog}
      />
    );
  };

  return (
    <>
      <div className="space-y-3 p-2 md:hidden">
        <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-secondary px-3 py-2 text-xs text-muted-foreground">
          <label className="inline-flex items-center gap-2">
            <input
              type="checkbox"
              aria-label={t("nodes.selectAllVisible")}
              className="size-4 accent-primary rounded-sm"
              checked={allVisibleSelected}
              disabled={!sortedNodes.length}
              onChange={(event) => toggleSelectAllVisible(event.target.checked)}
            />
            <span>{t("common.selectAll")}</span>
          </label>
          {isAdmin ? (
            <Button
              size="sm"
              variant="destructive"
              disabled={!selectedNodeIds.length}
              onClick={() => void handleBulkDelete()}
            >
              {t("nodes.deleteCount", { count: selectedNodeIds.length })}
            </Button>
          ) : null}
        </div>

        {loading ? (
          <LoadingState
            title={t("nodes.loadingTitle")}
            description={t("nodes.loadingDesc")}
            rows={3}
          />
        ) : null}
        {renderEmpty()}

        {sortedNodes.map((node) => (
          <NodeCard
            key={node.id}
            node={node}
            checked={selectedNodeSet.has(node.id)}
            labelledByName
            {...cardProps}
          />
        ))}
      </div>

      <div className="hidden gap-3 md:grid md:grid-cols-2 lg:grid-cols-3">
        {loading ? (
          <LoadingState
            className="md:col-span-2 lg:col-span-3"
            title={t("nodes.loadingTitle")}
            description={t("nodes.loadingDesc")}
            rows={4}
          />
        ) : null}
        {renderEmpty("md:col-span-2 lg:col-span-3")}

        {sortedNodes.map((node) => (
          <NodeCard
            key={node.id}
            node={node}
            checked={selectedNodeSet.has(node.id)}
            labelledByName={false}
            {...cardProps}
          />
        ))}
      </div>
    </>
  );
});
