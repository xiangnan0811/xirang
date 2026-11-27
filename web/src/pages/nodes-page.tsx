import { useLayoutEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { Layers, ServerCog } from "lucide-react";
import { NodesGrid } from "@/pages/nodes-page.grid";
import { NodesTable } from "@/pages/nodes-page.table";
import { StepUpPrerequisiteNotice } from "@/components/step-up-prerequisite-notice";
import { useNodesPageState } from "@/pages/nodes-page.state";
import { NodesPageDialogs } from "@/pages/nodes-page.dialogs";
import { NodesPageToolbar } from "@/pages/nodes-page.toolbar";
import { Button } from "@/components/ui/button";
import { InventoryRetryAlert } from "@/components/ui/inventory-retry-alert";
import { DataSurface, DataSurfaceContent, DataSurfaceToolbar } from "@/components/ui/data-surface";
import { Select } from "@/components/ui/select";
import { FilterPanel, FilterSummary } from "@/components/ui/filter-panel";
import { PageHero } from "@/components/ui/page-hero";
import { Pagination } from "@/components/ui/pagination";
import { SearchInput } from "@/components/ui/search-input";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { useClientPagination } from "@/hooks/use-client-pagination";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import { focusablePeerOf, restoreDialogOpenerOrFallback } from "@/components/ui/dialog-opener";
import { getAuthSessionGeneration, isAuthTransitionActive } from "@/lib/api/core";
import type { NodeRecord } from "@/types/domain";

type ConnectionFocusIntent = {
  nodeId: number;
  authGeneration: number;
  token: string | null;
};

export function NodesPage() {
  const { t } = useTranslation();
  const state = useNodesPageState();

  const {
    nodes,
    sshKeys,
    loading,
    requestFailed,
    nodesError,
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
    inventoryEmpty,
    selectedNodeIds,
    testingNodeId,
    doctorNodeId,
    triggeringNodeId,
    emergencyNodeId,
    isAdmin,
    totpEnabled,
    canOperateNodes,
    canBrowseNodeFiles,
    openCreateDialog,
    openEditDialog,
    onTestNode,
    onDeleteNode,
    toggleNodeSelection,
    toggleSelectAllVisible,
    handleBulkDelete,
    handleTriggerBackup,
    handleEmergencyBackup,
  } = state;

  const {
    pagedItems: pagedNodes,
    page,
    pageSize,
    total: filteredTotal,
    setPage,
    setPageSize,
  } = useClientPagination(sortedNodes);

  // 卡片/表头的全选只作用于其可见节点：平铺视图为当前页，分组视图为该分组。
  const allSelectedIn = (list: readonly NodeRecord[]) => list.length > 0
    && list.every((node) => selectedNodeSet.has(node.id));
  const toggleSelectionIn = (list: readonly NodeRecord[]) => (checked: boolean) => {
    const ids = list.map((node) => node.id);
    state.setSelectedNodeIds((prev: number[]) => {
      if (checked) {
        return Array.from(new Set([...prev, ...ids]));
      }
      const visibleIDs = new Set(ids);
      return prev.filter((id) => !visibleIDs.has(id));
    });
  };

  const nodesViewProps = {
    loading,
    requestFailed,
    inventoryEmpty,
    sortedNodes: groupView ? sortedNodes : pagedNodes,
    sshKeys,
    selectedNodeSet,
    selectedNodeIds,
    allVisibleSelected: allSelectedIn(pagedNodes),
    testingNodeId,
    doctorNodeId,
    triggeringNodeId,
    toggleNodeSelection,
    toggleSelectAllVisible: toggleSelectionIn(pagedNodes),
    handleBulkDelete,
    resetFilters,
    openCreateDialog,
    openEditDialog,
    onTestNode,
    onOpenDoctor: state.openDoctor,
    onDeleteNode,
    handleTriggerBackup,
    onEmergencyBackup: handleEmergencyBackup,
    emergencyNodeId,
    onOpenTerminal: state.openTerminal,
    onMigrate: state.openMigrate,
    onOpenFileBrowser: canBrowseNodeFiles ? state.openFileBrowser : undefined,
    isAdmin,
    canOperateNodes,
    canBrowseNodeFiles,
  };

  // Doctor → 连接测试导航：只关闭 Doctor 并聚焦当前视图里同一节点已有的连接测试按钮，
  // 绝不点击或执行测试。按钮可能因响应式/分组出现多次，按 data 属性选首个可见可用者。
  const inventoryRef = useRef<HTMLDivElement | null>(null);
  const connectionFocusIntentRef = useRef<ConnectionFocusIntent | null>(null);
  // 终端与 Doctor 只能从节点更多菜单打开。菜单带退出动画，弹窗挂载时焦点可能仍在
  // 即将卸载的菜单项上，因此记录最近一次打开菜单的触发按钮，关闭弹窗时回焦到它。
  const nodeActionsOpenerRef = useRef<HTMLElement | null>(null);
  const liveIdentityRef = useRef({ token: state.token, canOperateNodes });
  useLayoutEffect(() => {
    liveIdentityRef.current = { token: state.token, canOperateNodes };
    connectionFocusIntentRef.current = null;
    // 身份或权限变化后不再沿用旧会话记录的触发按钮，关闭时回退到当前清单。
    nodeActionsOpenerRef.current = null;
  }, [state.token, canOperateNodes, isAdmin, canBrowseNodeFiles]);

  const findConnectionTarget = (nodeId: number): HTMLButtonElement | null => {
    const root = inventoryRef.current;
    if (!root?.isConnected) return null;
    const candidates = root.querySelectorAll<HTMLButtonElement>(`button[data-node-test-connection="${nodeId}"]`);
    for (const button of candidates) {
      if (
        button.isConnected
        && !button.disabled
        && button.getClientRects().length > 0
        && window.getComputedStyle(button).visibility !== "hidden"
      ) {
        return button;
      }
    }
    return null;
  };

  const doctorNode = state.doctorNode;
  const connectionNavigationAvailable = canOperateNodes
    && doctorNode !== null
    && testingNodeId !== doctorNode.id
    && nodesViewProps.sortedNodes.some((node) => node.id === doctorNode.id);

  const navigateDoctorToConnection = (): boolean => {
    const live = liveIdentityRef.current;
    if (
      !doctorNode
      || !connectionNavigationAvailable
      || !live.canOperateNodes
      || live.token !== state.token
      || isAuthTransitionActive()
      || !findConnectionTarget(doctorNode.id)
    ) {
      return false;
    }
    connectionFocusIntentRef.current = {
      nodeId: doctorNode.id,
      authGeneration: getAuthSessionGeneration(),
      token: live.token,
    };
    state.handleDoctorOpenChange(false);
    return true;
  };

  const rememberNodeActionsOpener = (event: { target: EventTarget | null }) => {
    if (!(event.target instanceof Element)) return;
    const trigger = event.target.closest<HTMLElement>("[data-node-actions-trigger]");
    if (trigger) {
      nodeActionsOpenerRef.current = trigger;
    }
  };

  // 节点对话框关闭回焦：记录的触发按钮可能因跨越响应式断点而隐藏（桌面卡片/表格与移动卡片
  // 同时挂载），此时按触发按钮上的节点 ID 在当前清单内找同节点可见的“更多”按钮，再退回清单区域。
  const restoreNodeDialogFocus = (event: Parameters<DialogCloseAutoFocus>[0], opener: HTMLElement | null) => {
    restoreDialogOpenerOrFallback(event, opener, (staleOpener) => {
      const inventory = inventoryRef.current;
      return focusablePeerOf(staleOpener, "data-node-actions-trigger", inventory) ?? inventory;
    });
  };
  const nodeActionsOnCloseAutoFocus: DialogCloseAutoFocus = (event) => {
    restoreNodeDialogFocus(event, nodeActionsOpenerRef.current);
  };
  const nodeEditorOnCloseAutoFocus: DialogCloseAutoFocus = (event) => {
    restoreNodeDialogFocus(event, state.nodeEditorOpenerRef.current);
  };
  const fileBrowserOnCloseAutoFocus: DialogCloseAutoFocus = (event) => {
    restoreNodeDialogFocus(event, state.fileBrowserOpenerRef.current);
  };

  const doctorOnCloseAutoFocus: DialogCloseAutoFocus = (event) => {
    const intent = connectionFocusIntentRef.current;
    if (!intent) {
      nodeActionsOnCloseAutoFocus(event);
      return;
    }
    connectionFocusIntentRef.current = null;
    const live = liveIdentityRef.current;
    if (
      intent.authGeneration !== getAuthSessionGeneration()
      || intent.token !== live.token
      || !live.canOperateNodes
      || isAuthTransitionActive()
    ) {
      return;
    }
    const target = findConnectionTarget(intent.nodeId);
    if (target) {
      event.preventDefault();
      target.focus();
      target.scrollIntoView?.({ block: "nearest" });
      return;
    }
    const inventory = inventoryRef.current;
    if (inventory?.isConnected) {
      event.preventDefault();
      inventory.focus();
    }
  };

  return (
    <div className="animate-fade-in space-y-5">
      <PageHero
        title={t("nav.nodes")}
        subtitle={t("nodes.totalNodesDesc")}
        meta={
          <>
            <span>{t("nodes.onlineNodes")}: {nodeStats.online}</span>
            <span>{t("nodes.warningOffline")}: {nodeStats.warning + nodeStats.offline}</span>
            <span>{t("nodes.selectedCount", { count: selectedNodeIds.length })}</span>
          </>
        }
        actions={
          isAdmin ? (
            <Button size="sm" onClick={openCreateDialog}>
              <ServerCog className="size-4" aria-hidden />
              {t("nodes.addNode")}
            </Button>
          ) : undefined
        }
      />

      {canOperateNodes && totpEnabled === false ? <StepUpPrerequisiteNotice /> : null}

      <StatCardsSection
        compact
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

      <div
        ref={inventoryRef}
        tabIndex={-1}
        onPointerDownCapture={rememberNodeActionsOpener}
        onKeyDownCapture={rememberNodeActionsOpener}
      >
      <DataSurface>
        <DataSurfaceToolbar className="space-y-3">
          {/* 工具栏：左侧操作按钮 + 右侧视图/批量/重置 */}
          <NodesPageToolbar
            viewMode={state.viewMode}
            setViewMode={state.setViewMode}
            groupView={state.groupView}
            setGroupView={state.setGroupView}
            selectedNodeIds={state.selectedNodeIds}
            setSelectedNodeIds={state.setSelectedNodeIds}
            allVisibleSelected={allVisibleSelected}
            csvInputRef={state.csvInputRef}
            toggleSelectAllVisible={toggleSelectAllVisible}
            handleBulkDelete={state.handleBulkDelete}
            handleImportCSV={state.handleImportCSV}
            handleExportCSV={state.handleExportCSV}
            handleDownloadTemplate={state.handleDownloadTemplate}
            setBatchCmdOpen={state.openBatchCommand}
            hasActiveFilters={state.hasActiveFilters}
            resetFilters={state.resetFilters}
            isAdmin={state.isAdmin}
            canOperateNodes={state.canOperateNodes}
          />

          <FilterPanel sticky={false} className="grid grid-cols-2 items-center gap-3 border-0 bg-transparent p-0 md:grid-cols-3 xl:grid-cols-[2fr_1fr_1fr_1fr]">
            <SearchInput
              containerClassName="w-full col-span-2 md:col-span-3 xl:col-span-1"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
              placeholder={t("nodes.searchPlaceholder")}
              aria-label={t("nodes.keywordAriaLabel")}
            />
            <Select
              containerClassName="w-full"
              aria-label={t("nodes.statusAriaLabel")}
              value={statusFilter}
              onChange={(event) =>
                setStatusFilter(event.target.value as typeof statusFilter)
              }
            >
              <option value="all">{t("nodes.allStatus")}</option>
              <option value="online">{t("status.node.online")}</option>
              <option value="warning">{t("status.node.warning")}</option>
              <option value="offline">{t("status.node.offline")}</option>
            </Select>
            <Select
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
            </Select>
            <Select
              containerClassName="w-full col-span-2 md:col-span-1"
              aria-label={t("nodes.sortAriaLabel")}
              value={sortBy === "disk-low" ? "status" : sortBy}
              onChange={(event) =>
                setSortBy(event.target.value as typeof sortBy)
              }
            >
              <option value="status">{t("nodes.sortStatus")}</option>
              <option value="name-asc">{t("nodes.sortNameAsc")}</option>
              <option value="name-desc">{t("nodes.sortNameDesc")}</option>
              <option value="backup-recent">{t("nodes.sortBackupRecent")}</option>
            </Select>
          </FilterPanel>

          <FilterSummary filtered={sortedNodes.length} total={nodes.length} unit={t("nodes.nodeUnit")} />
        </DataSurfaceToolbar>

        <DataSurfaceContent className="space-y-4">
          {nodesError ? (
            <InventoryRetryAlert error={nodesError} onRetry={() => { void state.refreshNodes(); }} />
          ) : null}
          {/* 分组视图；没有分组时仍交给 NodesGrid 呈现加载与空状态 */}
          {groupView && groupedNodes && groupedNodes.length > 0 ? (
            <div className="space-y-4">
              {groupedNodes.map(([tag, tagNodes]) => (
                <details key={tag} open className="group">
                  <summary className="flex cursor-pointer items-center gap-2 rounded-md bg-muted/40 px-3 py-2 text-sm font-medium hover:bg-muted/60">
                    <Layers className="size-4 text-muted-foreground" aria-hidden />
                    {tag}
                    <span className="ml-auto text-xs text-muted-foreground">{t("nodes.groupNodeCount", { count: tagNodes.length })}</span>
                  </summary>
                  <div className="mt-2">
                    <NodesGrid
                      {...nodesViewProps}
                      sortedNodes={tagNodes}
                      allVisibleSelected={allSelectedIn(tagNodes)}
                      toggleSelectAllVisible={toggleSelectionIn(tagNodes)}
                    />
                  </div>
                </details>
              ))}
            </div>
          ) : groupView ? (
            <NodesGrid {...nodesViewProps} />
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
        </DataSurfaceContent>
      </DataSurface>
      </div>

      <NodesPageDialogs
        token={state.token}
        nodes={state.nodes}
        sshKeys={state.sshKeys}
        editorOpen={state.editorOpen}
        handleEditorOpenChange={state.handleEditorOpenChange}
        nodeEditorOnCloseAutoFocus={nodeEditorOnCloseAutoFocus}
        editingNode={state.editingNode}
        terminalNode={state.terminalNode}
        setTerminalNode={state.setTerminalNode}
        terminalKey={state.terminalKey}
        fileBrowserNode={state.fileBrowserNode}
        setFileBrowserNode={state.setFileBrowserNode}
        fileBrowserOnCloseAutoFocus={fileBrowserOnCloseAutoFocus}
        fileBrowserTab={state.fileBrowserTab}
        canBrowseNodeFiles={state.canBrowseNodeFiles}
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
        doctorNode={state.doctorNode}
        doctorResult={state.doctorResult}
        doctorLoading={state.doctorLoading}
        doctorError={state.doctorError}
        handleDoctorOpenChange={state.handleDoctorOpenChange}
        runDoctorForNode={state.runDoctorForNode}
        doctorConnectionNavigationAvailable={connectionNavigationAvailable}
        onDoctorNavigateConnection={navigateDoctorToConnection}
        doctorOnCloseAutoFocus={doctorOnCloseAutoFocus}
        terminalOnCloseAutoFocus={nodeActionsOnCloseAutoFocus}
        isAdmin={state.isAdmin}
        canOperateNodes={state.canOperateNodes}
        hostKeyIssue={state.hostKeyIssue}
        hostKeyTrusting={state.hostKeyTrusting}
        hostKeyError={state.hostKeyError}
        trustHostKey={state.trustHostKey}
        openHostKeySettings={state.openHostKeySettings}
        handleHostKeyOpenChange={state.handleHostKeyOpenChange}
      />
    </div>
  );
}
