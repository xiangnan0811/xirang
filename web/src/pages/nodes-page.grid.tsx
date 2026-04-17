import React from "react";
import { useTranslation } from "react-i18next";
import { ServerCog } from "lucide-react";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { LoadingState } from "@/components/ui/loading-state";
import { NodeCard, NodeCardMobile } from "@/components/node-card";
import type { NodesViewProps } from "@/pages/nodes-page.utils";


export const NodesGrid = React.memo(function NodesGrid({
  loading,
  sortedNodes,
  sshKeys,
  selectedNodeSet,
  selectedNodeId,
  allVisibleSelected,
  testingNodeId,
  triggeringNodeId,
  toggleNodeSelection,
  toggleSelectAllVisible,
  setSelectedNodeId,
  resetFilters,
  openCreateDialog,
  openEditDialog,
  onTestNode,
  onDeleteNode,
  handleTriggerBackup,
  onOpenTerminal,
  onOpenFileBrowser,
  onEmergencyBackup,
  onMigrate,
  emergencyNodeId,
  isAdmin,
}: NodesViewProps) {
  const { t } = useTranslation();

  return (
    <>
      {/* Mobile card list */}
      <div className="space-y-3 p-2 md:hidden">
        <div className="flex items-center gap-2 justify-between rounded-lg border border-border bg-secondary px-3 py-2 text-xs text-muted-foreground">
          <div className="inline-flex items-center gap-2">
            <input
              type="checkbox"
              aria-label={t("nodes.selectAllVisible")}
              className="size-4"
              checked={allVisibleSelected}
              onChange={(event) => toggleSelectAllVisible(event.target.checked)}
            />
            <span>{t("common.selectAll")}</span>
          </div>
        </div>

        {loading ? (
          <LoadingState
            title={t("nodes.loadingTitle")}
            description={t("nodes.loadingDesc")}
            rows={3}
          />
        ) : null}

        {!loading && !sortedNodes.length ? (
          <FilteredEmptyState
            title={t("nodes.emptyTitle")}
            description={t("nodes.emptyDesc")}
            onReset={resetFilters}
            onCreate={openCreateDialog}
            createLabel={t("nodes.emptyCreateLabel")}
            createIcon={ServerCog}
          />
        ) : null}

        {sortedNodes.map((node) => (
          <NodeCardMobile
            key={node.id}
            node={node}
            checked={selectedNodeSet.has(node.id)}
            testingNodeId={testingNodeId}
            triggeringNodeId={triggeringNodeId}
            isAdmin={isAdmin ?? false}
            onToggleSelection={toggleNodeSelection}
            onTestNode={onTestNode}
            onDeleteNode={onDeleteNode}
            onEditNode={openEditDialog}
            onTriggerBackup={handleTriggerBackup}
            onOpenTerminal={onOpenTerminal}
            onOpenFileBrowser={onOpenFileBrowser}
          />
        ))}
      </div>

      {/* Desktop grid */}
      <div className="hidden gap-3 md:grid md:grid-cols-2 lg:grid-cols-3">
        {loading ? (
          <LoadingState
            className="md:col-span-2 lg:col-span-3"
            title={t("nodes.loadingTitle")}
            description={t("nodes.loadingDesc")}
            rows={4}
          />
        ) : null}

        {!loading && !sortedNodes.length ? (
          <FilteredEmptyState
            title={t("nodes.emptyTitle")}
            description={t("nodes.emptyDesc")}
            onReset={resetFilters}
            onCreate={openCreateDialog}
            createLabel={t("nodes.emptyCreateLabel")}
            createIcon={ServerCog}
          />
        ) : null}

        {sortedNodes.map((node) => (
          <NodeCard
            key={node.id}
            node={node}
            sshKeys={sshKeys}
            checked={selectedNodeSet.has(node.id)}
            isSelected={selectedNodeId === node.id}
            testingNodeId={testingNodeId}
            triggeringNodeId={triggeringNodeId}
            emergencyNodeId={emergencyNodeId ?? null}
            isAdmin={isAdmin ?? false}
            onSelect={setSelectedNodeId}
            onToggleSelection={toggleNodeSelection}
            onTestNode={onTestNode}
            onDeleteNode={onDeleteNode}
            onEditNode={openEditDialog}
            onTriggerBackup={handleTriggerBackup}
            onEmergencyBackup={onEmergencyBackup}
            onOpenTerminal={onOpenTerminal}
            onOpenFileBrowser={onOpenFileBrowser}
            onMigrate={onMigrate}
          />
        ))}
      </div>
    </>
  );
});
