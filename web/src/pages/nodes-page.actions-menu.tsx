import { useRef } from "react";
import { useTranslation } from "react-i18next";
import {
  Activity,
  ArrowRightLeft,
  FolderOpen,
  Loader2,
  MonitorPlay,
  MoreHorizontal,
  ShieldAlert,
  Stethoscope,
  Terminal,
  Trash2,
  Wrench,
} from "lucide-react";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import type { NodeRecord } from "@/types/domain";

export type NodeActionsMenuProps = {
  node: NodeRecord;
  testing: boolean;
  doctorRunning: boolean;
  triggering: boolean;
  emergencyRunning: boolean;
  isAdmin: boolean;
  canOperateNodes: boolean;
  canBrowseNodeFiles: boolean;
  onTestNode: (node: NodeRecord) => void;
  onOpenDoctor?: (node: NodeRecord) => void;
  handleTriggerBackup: (id: number, name: string) => void;
  onEmergencyBackup?: (nodeId: number, nodeName: string) => void;
  onOpenTerminal?: (node: NodeRecord) => void;
  onOpenFileBrowser?: (node: NodeRecord, opener?: EventTarget | null) => void;
  openEditDialog: (node: NodeRecord, opener?: EventTarget | null) => void;
  onMigrate?: (node: NodeRecord) => void;
  onDeleteNode: (node: NodeRecord) => void;
  /** 表格行保持单行；卡片允许换行。 */
  layout: "card" | "row";
};

/**
 * 节点卡片与列表行共用的操作区。连接测试、日志、紧急备份与手动备份直出；
 * 诊断、终端、文件、编辑、迁移与删除收进同一更多菜单。编辑与文件浏览把菜单
 * 触发按钮作为 opener 传入；触发按钮带 data-node-actions-trigger，页面据此为
 * 终端与 Doctor 回焦。连接测试按钮保留 data-node-test-connection，供 Doctor 的
 * “前往连接测试”定位。
 */
export function NodeActionsMenu({
  node,
  testing,
  doctorRunning,
  triggering,
  emergencyRunning,
  isAdmin,
  canOperateNodes,
  canBrowseNodeFiles,
  onTestNode,
  onOpenDoctor,
  handleTriggerBackup,
  onEmergencyBackup,
  onOpenTerminal,
  onOpenFileBrowser,
  openEditDialog,
  onMigrate,
  onDeleteNode,
  layout,
}: NodeActionsMenuProps) {
  const { t } = useTranslation();
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const showFileBrowser = canBrowseNodeFiles && Boolean(onOpenFileBrowser);
  const showOverflowMenu = canOperateNodes || isAdmin || showFileBrowser;
  const iconButtonClass = "size-8 text-muted-foreground hover:bg-accent hover:text-foreground";

  return (
    <div
      className={cn(
        "flex items-center gap-1",
        layout === "row" ? "flex-nowrap justify-end whitespace-nowrap" : "flex-wrap justify-between",
      )}
    >
      <div className="flex items-center gap-1">
        {canOperateNodes ? (
          <Button
            variant="ghost"
            size="icon"
            className={iconButtonClass}
            aria-label={t("nodes.testConnectionAriaLabel", { name: node.name })}
            title={t("nodes.testConnection")}
            data-node-test-connection={node.id}
            onClick={() => void onTestNode(node)}
            disabled={testing}
          >
            {testing ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <Activity className="size-4" aria-hidden />}
          </Button>
        ) : null}
        <Button
          variant="ghost"
          size="icon"
          className={iconButtonClass}
          aria-label={t("nodes.viewLogsAriaLabel", { name: node.name })}
          title={t("nodes.viewLogs")}
          asChild
        >
          <Link to={`/app/logs?node=${encodeURIComponent(node.name)}`}>
            <Terminal className="size-4" aria-hidden />
          </Link>
        </Button>
      </div>

      <div className="flex items-center gap-1">
        {canOperateNodes ? (
          <Button
            size="sm"
            variant="destructive"
            className="text-xs"
            disabled={emergencyRunning}
            onClick={() => onEmergencyBackup?.(node.id, node.name)}
            title={t("nodes.emergencyBackup")}
          >
            {emergencyRunning
              ? <Loader2 className="mr-1 size-3.5 animate-spin" aria-hidden />
              : <ShieldAlert className="mr-1 size-3.5" aria-hidden />}
            {t("nodes.emergencyBackup")}
          </Button>
        ) : null}
        {canOperateNodes ? (
          <Button
            size="sm"
            variant="outline"
            className="text-xs"
            disabled={triggering}
            onClick={() => void handleTriggerBackup(node.id, node.name)}
          >
            {triggering ? <Loader2 className="mr-1 size-3.5 animate-spin" aria-hidden /> : null}
            {t("nodes.manualBackup")}
          </Button>
        ) : null}
        {showOverflowMenu ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                ref={triggerRef}
                data-node-actions-trigger={node.id}
                variant="ghost"
                size="icon"
                className={iconButtonClass}
                aria-label={t("nodes.moreActionsAriaLabel", { name: node.name })}
                title={t("common.more")}
              >
                <MoreHorizontal className="size-4" aria-hidden />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-48">
              {canOperateNodes ? (
                <DropdownMenuItem disabled={doctorRunning} onClick={() => onOpenDoctor?.(node)}>
                  {doctorRunning
                    ? <Loader2 className="mr-2 size-4 animate-spin" aria-hidden />
                    : <Stethoscope className="mr-2 size-4" aria-hidden />}
                  {t("nodes.doctor")}
                </DropdownMenuItem>
              ) : null}
              {isAdmin ? (
                <DropdownMenuItem onClick={() => onOpenTerminal?.(node)}>
                  <MonitorPlay className="mr-2 size-4" aria-hidden />
                  {t("nodes.webTerminal")}
                </DropdownMenuItem>
              ) : null}
              {showFileBrowser ? (
                <DropdownMenuItem onClick={() => onOpenFileBrowser?.(node, triggerRef.current)}>
                  <FolderOpen className="mr-2 size-4" aria-hidden />
                  {t("nodes.fileBrowser")}
                </DropdownMenuItem>
              ) : null}
              {isAdmin ? (
                <DropdownMenuItem onClick={() => openEditDialog(node, triggerRef.current)}>
                  <Wrench className="mr-2 size-4" aria-hidden />
                  {t("nodes.editNode")}
                </DropdownMenuItem>
              ) : null}
              {isAdmin && onMigrate ? (
                <DropdownMenuItem onClick={() => onMigrate(node)}>
                  <ArrowRightLeft className="mr-2 size-4" aria-hidden />
                  {t("nodes.migrateShort")}
                </DropdownMenuItem>
              ) : null}
              {isAdmin ? <DropdownMenuSeparator /> : null}
              {isAdmin ? (
                <DropdownMenuItem
                  className="text-destructive focus:text-destructive"
                  onClick={() => onDeleteNode(node)}
                >
                  <Trash2 className="mr-2 size-4" aria-hidden />
                  {t("nodes.deleteNode")}
                </DropdownMenuItem>
              ) : null}
            </DropdownMenuContent>
          </DropdownMenu>
        ) : null}
      </div>
    </div>
  );
}
