import { useTranslation } from "react-i18next";
import { useSSHKeysPageState } from "@/pages/ssh-keys-page.state";
import { SSHKeysToolbar } from "@/pages/ssh-keys-page.toolbar";
import { SSHKeysTable } from "@/pages/ssh-keys-page.table";
import { SSHKeysGrid } from "@/pages/ssh-keys-page.grid";
import { SSHKeyEditorDialog, type SSHKeyDraft } from "@/components/ssh-key-editor-dialog";
import { SSHKeyTestConnectionDialog } from "@/components/ssh-key-test-connection-dialog";
import { SSHKeyAssociatedNodesSheet } from "@/components/ssh-key-associated-nodes-sheet";
import { SSHKeyBatchImportDialog } from "@/components/ssh-key-batch-import-dialog";
import { SSHKeyExportDialog } from "@/components/ssh-key-export-dialog";
import { SSHKeyRotationWizard } from "@/components/ssh-key-rotation-wizard";
import { Card, CardContent } from "@/components/ui/card";
import { AppSelect } from "@/components/ui/app-select";
import { FilterPanel, FilterSummary } from "@/components/ui/filter-panel";
import { Pagination } from "@/components/ui/pagination";
import { SearchInput } from "@/components/ui/search-input";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { toast } from "@/components/ui/toast";
import { useAuth } from "@/context/auth-context";
import { createSSHKeysApi } from "@/lib/api/ssh-keys-api";
import { getErrorMessage } from "@/lib/utils";

export function SSHKeysPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const state = useSSHKeysPageState();

  const {
    // 数据
    sshKeys,
    nodes,
    loading,
    // 筛选
    keyword,
    setKeyword,
    keyTypeFilter,
    setKeyTypeFilter,
    usageStatusFilter,
    setUsageStatusFilter,
    sortBy,
    setSortBy,
    resetFilters,
    isFiltered,
    // 视图
    viewMode,
    // 选中
    selectedIds,
    toggleSelection,
    // 计算数据
    keyUsageMap,
    stats,
    filteredKeys,
    // 分页
    pagination,
    // 对话框
    editorOpen,
    handleEditorOpenChange,
    editingKey,
    testConnectionKey,
    setTestConnectionKey,
    associatedNodesKey,
    setAssociatedNodesKey,
    rotationOpen,
    setRotationOpen,
    rotationKey,
    batchImportOpen,
    setBatchImportOpen,
    exportOpen,
    setExportOpen,
    // 确认对话框
    dialog,
    // 刷新
    refreshSSHKeys,
    // handlers
    openCreateDialog,
    openEditDialog,
    handleSave,
    handleDelete,
    openRotationWizard,
  } = state;

  const { pagedItems, page, pageSize, total, setPage, setPageSize } = pagination;

  // 分页后全选仅作用于当前页
  const pagedAllVisibleSelected =
    pagedItems.length > 0 && pagedItems.every((k) => selectedIds.has(k.id));
  const pagedToggleSelectAllVisible = (checked: boolean) => {
    if (checked) {
      for (const k of pagedItems) {
        if (!selectedIds.has(k.id)) toggleSelection(k.id, true);
      }
    } else {
      for (const k of pagedItems) {
        if (selectedIds.has(k.id)) toggleSelection(k.id, false);
      }
    }
  };

  // 编辑器保存适配：SSHKeyDraft → handleSave(draft, keyId?)
  const handleEditorSave = async (draft: SSHKeyDraft) => {
    const { id, ...input } = draft;
    await handleSave(input, id);
  };

  // 批量删除
  const handleBulkDelete = async () => {
    if (!token || selectedIds.size === 0) return;
    const ok = await state.confirm({
      title: t("sshKeys.confirmDeleteTitle"),
      description: t("sshKeys.batchDeleteConfirm", { count: selectedIds.size }),
    });
    if (!ok) return;
    try {
      const api = createSSHKeysApi();
      const result = await api.deleteSSHKeys(token, Array.from(selectedIds));
      if (result.skippedInUse.length > 0) {
        toast.warning(
          t("sshKeys.bulkDeletePartial", {
            deleted: result.deleted,
            skipped: result.skippedInUse.length,
          }),
        );
      } else {
        toast.success(t("sshKeys.bulkDeleteSuccess", { count: result.deleted }));
      }
      state.clearSelection();
      void refreshSSHKeys();
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  // 共享的视图 props
  const viewProps = {
    loading,
    pagedItems,
    keyUsageMap,
    selectedIds,
    allVisibleSelected: pagedAllVisibleSelected,
    isFiltered,
    toggleSelection,
    toggleSelectAllVisible: pagedToggleSelectAllVisible,
    resetFilters,
    openCreateDialog,
    openEditDialog,
    handleDelete,
    setTestConnectionKey,
    setAssociatedNodesKey,
    openRotationWizard,
  };

  // 测试连接对话框所需的关联节点
  const testConnectionNodes = testConnectionKey
    ? keyUsageMap.get(testConnectionKey.id) ?? []
    : [];

  return (
    <div className="animate-fade-in space-y-5">
      {/* ── 统计卡片 ── */}
      <StatCardsSection
        className="animate-slide-up [animation-delay:150ms]"
        items={[
          {
            title: t("sshKeys.statsTotal"),
            value: stats.total,
            description: t("sshKeys.statsTotalDesc"),
            tone: "info",
          },
          {
            title: t("sshKeys.statsInUse"),
            value: stats.inUse,
            description: t("sshKeys.statsInUseDesc", {
              rate: stats.total
                ? Math.round((stats.inUse / stats.total) * 100)
                : 0,
            }),
            tone: "success",
          },
          {
            title: t("sshKeys.statsUnused"),
            value: stats.unused,
            description: t("sshKeys.statsUnusedDesc"),
            tone: "warning",
          },
          {
            title: t("sshKeys.statsNodes"),
            value: stats.totalNodes,
            description: t("sshKeys.statsNodesDesc"),
            tone: "primary",
          },
        ]}
      />

      {/* ── 主内容区 ── */}
      <Card className="overflow-hidden rounded-lg border border-border bg-card">
        <CardContent className="space-y-4 pt-6">
          {/* 工具栏 */}
          <SSHKeysToolbar
            viewMode={state.viewMode}
            setViewMode={state.setViewMode}
            selectedIds={state.selectedIds}
            allVisibleSelected={pagedAllVisibleSelected}
            toggleSelectAllVisible={pagedToggleSelectAllVisible}
            clearSelection={state.clearSelection}
            openCreateDialog={state.openCreateDialog}
            setBatchImportOpen={state.setBatchImportOpen}
            setExportOpen={state.setExportOpen}
            openRotationWizard={state.openRotationWizard}
            onBulkDelete={handleBulkDelete}
          />

          {/* 筛选面板 */}
          <FilterPanel
            sticky={false}
            className="grid gap-3 grid-cols-2 md:grid-cols-4 items-center"
          >
            <SearchInput
              containerClassName="w-full col-span-2 md:col-span-1"
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
              placeholder={t("sshKeys.searchPlaceholder")}
              aria-label={t("sshKeys.searchAriaLabel")}
            />
            <AppSelect
              containerClassName="w-full"
              aria-label={t("sshKeys.keyTypeAriaLabel")}
              value={keyTypeFilter}
              onChange={(e) => setKeyTypeFilter(e.target.value)}
            >
              <option value="all">{t("sshKeys.allKeyTypes")}</option>
              <option value="rsa">RSA</option>
              <option value="ed25519">ED25519</option>
              <option value="ecdsa">ECDSA</option>
            </AppSelect>
            <AppSelect
              containerClassName="w-full"
              aria-label={t("sshKeys.usageStatusAriaLabel")}
              value={usageStatusFilter}
              onChange={(e) => setUsageStatusFilter(e.target.value)}
            >
              <option value="all">{t("sshKeys.allUsageStatus")}</option>
              <option value="in-use">{t("sshKeys.filterInUse")}</option>
              <option value="unused">{t("sshKeys.filterUnused")}</option>
            </AppSelect>
            <AppSelect
              containerClassName="w-full"
              aria-label={t("sshKeys.sortAriaLabel")}
              value={sortBy}
              onChange={(e) => setSortBy(e.target.value)}
            >
              <option value="name-asc">{t("sshKeys.sortNameAsc")}</option>
              <option value="name-desc">{t("sshKeys.sortNameDesc")}</option>
              <option value="created">{t("sshKeys.sortCreated")}</option>
              <option value="last-used">{t("sshKeys.sortLastUsed")}</option>
            </AppSelect>
          </FilterPanel>

          <FilterSummary
            filtered={filteredKeys.length}
            total={sshKeys.length}
            unit={t("sshKeys.keyUnit")}
          />

          {/* 卡片视图：移动端始终显示，桌面端按 viewMode 切换 */}
          <div className={viewMode === "table" ? "md:hidden" : undefined}>
            <SSHKeysGrid {...viewProps} />
          </div>

          {/* 表格视图：仅桌面端 viewMode=table 时显示 */}
          {viewMode === "table" && <SSHKeysTable {...viewProps} />}

          {/* 分页 */}
          <Pagination
            page={page}
            pageSize={pageSize}
            total={total}
            onPageChange={setPage}
            onPageSizeChange={setPageSize}
          />
        </CardContent>
      </Card>

      {/* ── 对话框 ── */}
      <SSHKeyEditorDialog
        open={editorOpen}
        onOpenChange={handleEditorOpenChange}
        editingKey={editingKey}
        onSave={handleEditorSave}
      />

      <SSHKeyTestConnectionDialog
        open={!!testConnectionKey}
        onOpenChange={(open) => {
          if (!open) setTestConnectionKey(null);
        }}
        sshKey={testConnectionKey}
        associatedNodes={testConnectionNodes}
        token={token ?? ""}
      />

      <SSHKeyAssociatedNodesSheet
        open={!!associatedNodesKey}
        onOpenChange={(open) => {
          if (!open) setAssociatedNodesKey(null);
        }}
        sshKey={associatedNodesKey}
        nodes={nodes}
      />

      <SSHKeyBatchImportDialog
        open={batchImportOpen}
        onOpenChange={setBatchImportOpen}
        existingKeyNames={sshKeys.map((k) => k.name)}
        token={token ?? ""}
        onImportComplete={() => void refreshSSHKeys()}
      />

      <SSHKeyExportDialog
        open={exportOpen}
        onOpenChange={setExportOpen}
        sshKeys={sshKeys}
        selectedKeyIds={Array.from(selectedIds)}
        stats={{ total: stats.total, inUse: stats.inUse }}
        token={token ?? ""}
      />

      <SSHKeyRotationWizard
        open={rotationOpen}
        onOpenChange={setRotationOpen}
        sshKeys={sshKeys}
        keyUsageMap={keyUsageMap}
        preselectedKey={rotationKey}
        token={token ?? ""}
        onComplete={() => void refreshSSHKeys()}
      />

      {/* 确认对话框（useConfirm） */}
      {dialog}
    </div>
  );
}
