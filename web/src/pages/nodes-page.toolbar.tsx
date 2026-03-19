import { useTranslation } from "react-i18next";
import {
  CheckSquare,
  Download,
  FileUp,
  Layers,
  MoreHorizontal,
  ServerCog,
  Terminal,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ViewModeToggle } from "@/components/ui/view-mode-toggle";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";
import type { NodesPageState } from "@/pages/nodes-page.state";

export type NodesPageToolbarProps = Pick<
  NodesPageState,
  | "viewMode"
  | "setViewMode"
  | "groupView"
  | "setGroupView"
  | "selectedNodeIds"
  | "setSelectedNodeIds"
  | "allVisibleSelected"
  | "csvInputRef"
  | "openCreateDialog"
  | "toggleSelectAllVisible"
  | "handleBulkDelete"
  | "handleImportCSV"
  | "handleExportCSV"
  | "handleDownloadTemplate"
  | "setBatchCmdOpen"
  | "resetFilters"
>;

export function NodesPageToolbar({
  viewMode,
  setViewMode,
  groupView,
  setGroupView,
  selectedNodeIds,
  setSelectedNodeIds,
  allVisibleSelected,
  csvInputRef,
  openCreateDialog,
  toggleSelectAllVisible,
  handleBulkDelete,
  handleImportCSV,
  handleExportCSV,
  handleDownloadTemplate,
  setBatchCmdOpen,
  resetFilters,
}: NodesPageToolbarProps) {
  const { t } = useTranslation();

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button size="sm" className="shrink-0" onClick={openCreateDialog}>
        <ServerCog className="mr-1 size-3.5" />
        {t("nodes.addNode")}
      </Button>
      {/* 移动端：收纳导入/模板/导出到下拉菜单 */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline" size="sm" className="shrink-0 md:hidden">
            <MoreHorizontal className="mr-1 size-3.5" />
            {t("nodes.more")}
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          <DropdownMenuItem onClick={() => csvInputRef.current?.click()}>
            <FileUp className="mr-2 size-3.5" />
            {t("nodes.csvImport")}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={handleDownloadTemplate}>
            <Download className="mr-2 size-3.5" />
            {t("nodes.downloadTemplate")}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={handleExportCSV}>
            <Download className="mr-2 size-3.5" />
            {t("nodes.exportNodes")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {/* 平板/桌面端：展示独立按钮 */}
      <Button
        variant="outline"
        size="sm"
        className="hidden shrink-0 md:inline-flex"
        onClick={() => {
          csvInputRef.current?.click();
        }}
      >
        <FileUp className="mr-1 size-3.5" />
        {t("nodes.csvImport")}
      </Button>
      <Button
        variant="outline"
        size="sm"
        className="hidden shrink-0 md:inline-flex"
        onClick={handleDownloadTemplate}
      >
        <Download className="mr-1 size-3.5" />
        {t("nodes.templateShort")}
      </Button>
      <Button variant="outline" size="sm" className="hidden shrink-0 md:inline-flex" onClick={handleExportCSV}>
        <Download className="mr-1 size-3.5" />
        {t("nodes.exportShort")}
      </Button>
      <input
        ref={csvInputRef}
        type="file"
        accept=".csv,text/csv"
        className="hidden"
        onChange={(event) => {
          const file = event.target.files?.[0];
          if (!file) {
            return;
          }
          void file
            .text()
            .then((content) => handleImportCSV(content))
            .catch((error) =>
              toast.error(getErrorMessage(error))
            );
          event.target.value = "";
        }}
      />
      {/* 分隔线：区分操作与视图/工具 */}
      <div className="hidden h-6 w-px bg-border/60 md:block" aria-hidden="true" />
      <ViewModeToggle
        className="hidden md:inline-flex"
        value={viewMode}
        onChange={setViewMode}
        groupLabel={t("nodes.viewToggleGroup")}
        cardsButtonLabel={t("nodes.viewCards")}
        listButtonLabel={t("nodes.viewList")}
      />
      <Button
        size="sm"
        variant={groupView ? "default" : "outline"}
        className="hidden shrink-0 md:inline-flex"
        onClick={() => setGroupView(!groupView)}
        aria-label={t("nodes.groupByTag")}
      >
        <Layers className="mr-1 size-3.5" />
        {t("nodes.groupLabel")}
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" variant="outline" aria-label={t("nodes.batchLabel")}>
            <MoreHorizontal className="mr-1 size-4" />
            {selectedNodeIds.length > 0 ? t("nodes.batchWithCount", { count: selectedNodeIds.length }) : t("nodes.batchLabel")}
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onClick={() => toggleSelectAllVisible(!allVisibleSelected)}>
            <CheckSquare className="mr-2 size-4" />
            {allVisibleSelected ? t("nodes.deselectAll") : t("nodes.selectAllFiltered")}
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={!selectedNodeIds.length}
            onClick={() => setSelectedNodeIds([])}
          >
            {t("nodes.clearSelection")}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            disabled={!selectedNodeIds.length}
            onClick={() => setBatchCmdOpen(true)}
          >
            <Terminal className="mr-2 size-3.5" />
            {t("nodes.batchCommandCount", { count: selectedNodeIds.length })}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            disabled={!selectedNodeIds.length}
            className="text-destructive focus:text-destructive"
            onClick={() => void handleBulkDelete()}
          >
            <Trash2 className="mr-2 size-3.5" />
            {t("nodes.deleteCount", { count: selectedNodeIds.length })}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <Button size="sm" variant="outline" onClick={resetFilters}>
        {t("nodes.reset")}
      </Button>
    </div>
  );
}
