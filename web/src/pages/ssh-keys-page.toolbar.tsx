import { useTranslation } from "react-i18next";
import {
  Download,
  LayoutGrid,
  List,
  Plus,
  RefreshCw,
  Trash2,
  Upload,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { SSHKeysPageState, ViewMode } from "@/pages/ssh-keys-page.state";

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export type SSHKeysToolbarProps = Pick<
  SSHKeysPageState,
  | "viewMode"
  | "setViewMode"
  | "selectedIds"
  | "allVisibleSelected"
  | "toggleSelectAllVisible"
  | "clearSelection"
  | "openCreateDialog"
  | "setBatchImportOpen"
  | "setExportOpen"
  | "openRotationWizard"
>;

export interface SSHKeysToolbarExtraProps extends SSHKeysToolbarProps {
  /** 批量删除回调，由父组件注入 */
  onBulkDelete?: () => void;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function SSHKeysToolbar({
  viewMode,
  setViewMode,
  selectedIds,
  openCreateDialog,
  setBatchImportOpen,
  setExportOpen,
  openRotationWizard,
  onBulkDelete,
}: SSHKeysToolbarExtraProps) {
  const { t } = useTranslation();
  const selectedCount = selectedIds.size;

  return (
    <div className="flex flex-wrap items-center justify-between gap-2">
      {/* ---------- 左侧操作按钮 ---------- */}
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" shape="pill" className="shrink-0" onClick={openCreateDialog}>
          <Plus className="size-4" aria-hidden="true" />
          {t("sshKeys.addKey")}
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="shrink-0"
          onClick={() => setBatchImportOpen(true)}
        >
          <Upload className="mr-1 size-3.5" />
          {t("sshKeys.batchImport")}
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="shrink-0"
          onClick={() => setExportOpen(true)}
        >
          <Download className="mr-1 size-3.5" />
          {t("sshKeys.exportPublicKeys")}
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="shrink-0"
          onClick={() => openRotationWizard()}
        >
          <RefreshCw className="mr-1 size-3.5" />
          {t("sshKeys.rotateKeys")}
        </Button>

        {/* 选中项操作 */}
        {selectedCount > 0 && (
          <>
            <div className="hidden h-6 w-px bg-border md:block" aria-hidden="true" />
            <Button
              variant="destructive"
              size="sm"
              className="shrink-0"
              onClick={onBulkDelete}
            >
              <Trash2 className="mr-1 size-3.5" />
              {t("sshKeys.batchDelete")}
            </Button>
            <span className="text-xs text-muted-foreground">
              {t("sshKeys.selectedCount", { count: selectedCount })}
            </span>
          </>
        )}
      </div>

      {/* ---------- 右侧视图切换 ---------- */}
      <div
        className="hidden items-center gap-1 rounded-lg border border-border bg-background p-1 md:inline-flex"
        role="radiogroup"
        aria-label={t("sshKeys.viewToggleGroup")}
      >
        <Button
          type="button"
          size="sm"
          variant={viewMode === "table" ? "default" : "ghost"}
          role="radio"
          aria-checked={viewMode === "table"}
          aria-label={t("sshKeys.viewTable")}
          onClick={() => setViewMode("table" as ViewMode)}
        >
          <List className={cn("size-4", viewMode === "table" ? "" : "mr-1")} />
          {viewMode !== "table" && t("sshKeys.viewTable")}
        </Button>
        <Button
          type="button"
          size="sm"
          variant={viewMode === "cards" ? "default" : "ghost"}
          role="radio"
          aria-checked={viewMode === "cards"}
          aria-label={t("sshKeys.viewCards")}
          onClick={() => setViewMode("cards" as ViewMode)}
        >
          <LayoutGrid className={cn("size-4", viewMode === "cards" ? "" : "mr-1")} />
          {viewMode !== "cards" && t("sshKeys.viewCards")}
        </Button>
      </div>
    </div>
  );
}
