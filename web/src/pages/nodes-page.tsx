import { useTranslation } from "react-i18next";
import { Layers } from "lucide-react";
import { NodesGrid } from "@/pages/nodes-page.grid";
import { NodesTable } from "@/pages/nodes-page.table";
import { useNodesPageState } from "@/pages/nodes-page.state";
import { NodesPageDialogs } from "@/pages/nodes-page.dialogs";
import { NodesPageToolbar } from "@/pages/nodes-page.toolbar";
import {
  Card,
  CardContent,
} from "@/components/ui/card";
import { AppSelect } from "@/components/ui/app-select";
import { FilterPanel, FilterSummary } from "@/components/ui/filter-panel";
import { Pagination } from "@/components/ui/pagination";
import { SearchInput } from "@/components/ui/search-input";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { useClientPagination } from "@/hooks/use-client-pagination";
import type { NodeRecord } from "@/types/domain";

export function NodesPage() {
  const { t } = useTranslation();
  const state = useNodesPageState();

  const {
    nodes,
    sshKeys,
    loading,
    keyword,
    setKeyword,
    statusFilter,
    setStatusFilter,
    tagFilter,
    setTagFilter,
    sortBy,
    setSortBy,
    tags,
    resetFilters,
    viewMode,
    groupView,
    nodeStats,
    sortedNodes,
    groupedNodes,
    selectedNodeSet,
    allVisibleSelected,
    selectedNodeId,
    setSelectedNodeId,
    selectedNodeIds,
    testingNodeId,
    triggeringNodeId,
    emergencyNodeId,
    isAdmin,
    openCreateDialog,
    openEditDialog,
    onTestNode,
    onDeleteNode,
    toggleNodeSelection,
    toggleSelectAllVisible,
    handleBulkDelete,
    handleTriggerBackup,
    handleEmergencyBackup,
    setTerminalNode,
    setTerminalKey,
    setMigrateSourceNode,
    setFileBrowserNode,
  } = state;

  const onOpenTerminal = (node: NodeRecord) => {
    setTerminalNode(node);
    setTerminalKey((k) => k + 1);
  };

  const {
    pagedItems: pagedNodes,
    page,
    pageSize,
    total: filteredTotal,
    setPage,
    setPageSize,
  } = useClientPagination(sortedNodes);

  // 分页后的全选仅作用于当前页可见节点
  const pagedAllVisibleSelected = pagedNodes.length > 0
    && pagedNodes.every((node) => selectedNodeSet.has(node.id));
  const pagedToggleSelectAllVisible = (checked: boolean) => {
    state.setSelectedNodeIds((prev: number[]) => {
      if (checked) {
        return Array.from(new Set([...prev, ...pagedNodes.map((node) => node.id)]));
      }
      const visibleIDs = new Set(pagedNodes.map((node) => node.id));
      return prev.filter((id) => !visibleIDs.has(id));
    });
  };

  const nodesViewProps = {
    loading,
    sortedNodes: groupView ? sortedNodes : pagedNodes,
    sshKeys,
    selectedNodeSet,
    selectedNodeId,
    selectedNodeIds,
    allVisibleSelected: groupView ? allVisibleSelected : pagedAllVisibleSelected,
    testingNodeId,
    triggeringNodeId,
    toggleNodeSelection,
    toggleSelectAllVisible: groupView ? toggleSelectAllVisible : pagedToggleSelectAllVisible,
    setSelectedNodeId,
    handleBulkDelete,
    resetFilters,
    openCreateDialog,
    openEditDialog,
    onTestNode,
    onDeleteNode,
    handleTriggerBackup,
    onEmergencyBackup: handleEmergencyBackup,
    emergencyNodeId,
    onOpenTerminal,
    onMigrate: setMigrateSourceNode,
    onOpenFileBrowser: setFileBrowserNode,
    isAdmin,
  };

  return (
    <div className="animate-fade-in space-y-5">
      <StatCardsSection
        className="animate-slide-up [animation-delay:150ms]"
        items={[
          {
            title: t("nodes.totalNodes"),
            value: nodes.length,
            description: t("nodes.totalNodesDesc"),
            tone: "info",
          },
          {
            title: t("nodes.onlineNodes"),
            value: nodeStats.online,
            description: t("nodes.onlineNodesDesc", {
              rate: nodes.length ? Math.round((nodeStats.online / nodes.length) * 100) : 0,
            }),
            tone: "success",
          },
          {
            title: t("nodes.warningOffline"),
            value: nodeStats.warning + nodeStats.offline,
            description: t("nodes.warningOfflineDesc", { warning: nodeStats.warning, offline: nodeStats.offline }),
            tone: "warning",
          },
          {
            title: t("nodes.filterSelection"),
            value: sortedNodes.length,
            description: t("nodes.selectedCount", { count: selectedNodeIds.length }),
            tone: "primary",
          },
        ]}
      />

      <Card className="overflow-hidden rounded-lg border border-border bg-card">
        <CardContent className="space-y-4 pt-6">
          {/* 工具栏：左侧操作按钮 + 右侧视图/批量/重置 */}
          <NodesPageToolbar
            viewMode={state.viewMode}
            setViewMode={state.setViewMode}
            groupView={state.groupView}
            setGroupView={state.setGroupView}
            selectedNodeIds={state.selectedNodeIds}
            setSelectedNodeIds={state.setSelectedNodeIds}
            allVisibleSelected={groupView ? state.allVisibleSelected : pagedAllVisibleSelected}
            csvInputRef={state.csvInputRef}
            openCreateDialog={state.openCreateDialog}
            toggleSelectAllVisible={groupView ? state.toggleSelectAllVisible : pagedToggleSelectAllVisible}
            handleBulkDelete={state.handleBulkDelete}
            handleImportCSV={state.handleImportCSV}
            handleExportCSV={state.handleExportCSV}
            handleDownloadTemplate={state.handleDownloadTemplate}
            setBatchCmdOpen={state.setBatchCmdOpen}
            resetFilters={state.resetFilters}
          />

          <FilterPanel sticky={false} className="grid gap-3 grid-cols-2 md:grid-cols-3 xl:grid-cols-[2fr_1fr_1fr_1fr] items-center">
            <SearchInput
              containerClassName="w-full col-span-2 md:col-span-3 xl:col-span-1"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
              placeholder={t("nodes.searchPlaceholder")}
              aria-label={t("nodes.keywordAriaLabel")}
            />
            <AppSelect
              containerClassName="w-full"
              aria-label={t("nodes.statusAriaLabel")}
              value={statusFilter}
              onChange={(event) =>
                setStatusFilter(event.target.value as typeof statusFilter)
              }
            >
              <option value="all">{t("nodes.allStatus")}</option>
              <option value="online">{t("nodes.statusOnline")}</option>
              <option value="warning">{t("nodes.statusWarning")}</option>
              <option value="offline">{t("nodes.statusOffline")}</option>
            </AppSelect>
            <AppSelect
              containerClassName="w-full"
              aria-label={t("nodes.tagAriaLabel")}
              value={tagFilter}
              onChange={(event) => setTagFilter(event.target.value)}
            >
              {tags.map((tag) => (
                <option key={tag} value={tag}>
                  {tag === "all" ? t("nodes.allTags") : tag}
                </option>
              ))}
            </AppSelect>
            <AppSelect
              containerClassName="w-full col-span-2 md:col-span-1"
              aria-label={t("nodes.sortAriaLabel")}
              value={sortBy}
              onChange={(event) =>
                setSortBy(event.target.value as typeof sortBy)
              }
            >
              <option value="status">{t("nodes.sortStatus")}</option>
              <option value="name-asc">{t("nodes.sortNameAsc")}</option>
              <option value="name-desc">{t("nodes.sortNameDesc")}</option>
              <option value="disk-low">{t("nodes.sortDiskLow")}</option>
              <option value="backup-recent">{t("nodes.sortBackupRecent")}</option>
            </AppSelect>
          </FilterPanel>

          <FilterSummary filtered={sortedNodes.length} total={nodes.length} unit={t("nodes.nodeUnit")} />

          {/* 分组视图 */}
          {groupView && groupedNodes ? (
            <div className="space-y-4">
              {groupedNodes.map(([tag, tagNodes]) => (
                <details key={tag} open className="group">
                  <summary className="flex cursor-pointer items-center gap-2 rounded-md bg-muted/40 px-3 py-2 text-sm font-medium hover:bg-muted/60">
                    <Layers className="size-4 text-muted-foreground" />
                    {tag}
                    <span className="ml-auto text-xs text-muted-foreground">{t("nodes.groupNodeCount", { count: tagNodes.length })}</span>
                  </summary>
                  <div className="mt-2">
                    <NodesGrid
                      {...nodesViewProps}
                      sortedNodes={tagNodes}
                    />
                  </div>
                </details>
              ))}
            </div>
          ) : (
          <>
          {/* 移动端始终显示卡片视图（viewMode 可能从桌面端持久化为 list） */}
          <div className={viewMode === "list" ? "md:hidden" : undefined}>
            <NodesGrid {...nodesViewProps} />
          </div>
          {viewMode === "list" && (
            <NodesTable {...nodesViewProps} />
          )}
          <Pagination
            page={page}
            pageSize={pageSize}
            total={filteredTotal}
            onPageChange={setPage}
            onPageSizeChange={setPageSize}
          />
          </>
          )}
        </CardContent>
      </Card>

      <NodesPageDialogs
        token={state.token}
        nodes={state.nodes}
        sshKeys={state.sshKeys}
        editorOpen={state.editorOpen}
        handleEditorOpenChange={state.handleEditorOpenChange}
        editingNode={state.editingNode}
        terminalNode={state.terminalNode}
        setTerminalNode={state.setTerminalNode}
        terminalKey={state.terminalKey}
        fileBrowserNode={state.fileBrowserNode}
        setFileBrowserNode={state.setFileBrowserNode}
        fileBrowserTab={state.fileBrowserTab}
        setFileBrowserTab={state.setFileBrowserTab}
        batchCmdOpen={state.batchCmdOpen}
        setBatchCmdOpen={state.setBatchCmdOpen}
        batchResultId={state.batchResultId}
        setBatchResultId={state.setBatchResultId}
        batchRetain={state.batchRetain}
        setBatchRetain={state.setBatchRetain}
        migrateSourceNode={state.migrateSourceNode}
        setMigrateSourceNode={state.setMigrateSourceNode}
        selectedNodeIds={state.selectedNodeIds}
        dialog={state.dialog}
        refreshNodes={state.refreshNodes}
        handleSaveNode={state.handleSaveNode}
        handleTestConnection={state.handleTestConnection}
      />
    </div>
  );
}
