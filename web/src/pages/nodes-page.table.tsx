import React from "react";
import { useTranslation } from "react-i18next";
import { Activity, ArrowRightLeft, FolderOpen, Loader2, MonitorPlay, MoreHorizontal, ServerCog, Stethoscope, Terminal, Trash2, Wrench } from "lucide-react";
import { Link } from "react-router-dom";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { getNodeStatusMeta } from "@/lib/status";
import { getDiskBarToneClass } from "@/pages/nodes-page.utils";
import { cn } from "@/lib/utils";
import type { NodesViewProps } from "@/pages/nodes-page.utils";

export const NodesTable = React.memo(function NodesTable({
  loading,
  sortedNodes,
  sshKeys,
  selectedNodeSet,
  allVisibleSelected,
  testingNodeId,
  doctorNodeId,
  triggeringNodeId,
  toggleNodeSelection,
  toggleSelectAllVisible,
  resetFilters,
  openCreateDialog,
  openEditDialog,
  onTestNode,
  onOpenDoctor,
  onDeleteNode,
  handleTriggerBackup,
  onOpenTerminal,
  onMigrate,
  onOpenFileBrowser,
  isAdmin,
  canBrowseNodeFiles,
}: NodesViewProps) {
  const { t } = useTranslation();

  return (
    <div className="hidden overflow-x-auto rounded-lg border border-border bg-card md:block">
      <table className="min-w-[1280px] text-left text-sm">
        <thead>
          <tr className="border-b border-border bg-secondary text-mini uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="px-3 py-2.5">
              <input
                type="checkbox"
                aria-label={t("nodes.selectAllVisible")}
                className="size-4 accent-primary rounded-sm"
                checked={allVisibleSelected}
                onChange={(event) =>
                  toggleSelectAllVisible(event.target.checked)
                }
              />
            </th>
            <th scope="col" className="px-3 py-2.5">{t("nodes.colNode")}</th>
            <th scope="col" className="px-3 py-2.5">{t("nodes.colAddress")}</th>
            <th scope="col" className="px-3 py-2.5">{t("nodes.colAuth")}</th>
            <th scope="col" className="px-3 py-2.5 whitespace-nowrap">{t("nodes.colStatus")}</th>
            <th scope="col" className="px-3 py-2.5">{t("nodes.colDiskProbe")}</th>
            <th scope="col" className="px-3 py-2.5">{t("nodes.colLastBackup")}</th>
            <th scope="col" className="px-3 py-2.5">{t("nodes.colTags")}</th>
            <th scope="col" className="px-3 py-2.5 text-right">{t("nodes.colActions")}</th>
          </tr>
        </thead>
        <tbody>
          {loading ? (
            <tr>
              <td colSpan={9} className="px-3 py-4 text-muted-foreground">
                {t("nodes.tableLoading")}
              </td>
            </tr>
          ) : !sortedNodes.length ? (
            <tr>
              <td colSpan={9} className="px-3 py-6">
                <FilteredEmptyState
                  className="py-8"
                  title={t("nodes.emptyTitle")}
                  description={t("nodes.emptyDesc")}
                  onReset={resetFilters}
                  onCreate={openCreateDialog}
                  createLabel={t("nodes.emptyCreateLabel")}
                  createIcon={ServerCog}
                />
              </td>
            </tr>
          ) : (
            sortedNodes.map((node) => {
              const status = getNodeStatusMeta(node.status);
              const keyLabel = node.keyId
                ? sshKeys.find((key) => key.id === node.keyId)?.name || t("common.keyBound")
                : t("common.keyUnbound");

              return (
                <tr key={node.id} className="border-b border-border transition-colors duration-200 ease-out hover:bg-accent">
                  <td className="px-3 py-2.5">
                    <input
                      type="checkbox"
                      aria-label={t("nodes.selectNodeAriaLabel", { name: node.name })}
                      className="size-4 accent-primary rounded-sm"
                      checked={selectedNodeSet.has(node.id)}
                      onChange={(event) =>
                        toggleNodeSelection(node.id, event.target.checked)
                      }
                    />
                  </td>
                  <td className="px-3 py-2.5">
                    <Link
                      to={`/app/nodes/${node.id}`}
                      data-testid={`nodes-list-link-${node.id}`}
                      className="font-medium hover:underline"
                    >
                      {node.name}
                    </Link>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">
                    <p>
                      {node.host}:{node.port}
                    </p>
                    <p className="text-xs">{node.username}</p>
                  </td>
                  <td className="px-3 py-2.5 text-xs text-muted-foreground">
                    <p>
                      {node.authType === "key" ? t("nodes.authKey") : t("nodes.authPassword")}
                    </p>
                    <p>
                      {node.authType === "key" ? keyLabel : "-"}
                    </p>
                  </td>
                  <td className="px-3 py-2.5 whitespace-nowrap">
                    <Badge tone={status.variant}>
                      {status.label}
                    </Badge>
                  </td>
                  <td className="px-3 py-2.5">
                    <div className="w-44">
                      <div className="mb-1 flex items-center justify-between text-xs text-muted-foreground">
                        <span>{t("nodes.diskFreePercent", { pct: node.diskFreePercent })}</span>
                        <span>
                          {node.connectionLatencyMs
                            ? `${node.connectionLatencyMs} ms`
                            : "-"}
                        </span>
                      </div>
                      <div className="h-2 rounded-full bg-muted">
                        <div
                          className={cn(
                            "h-2 rounded-full",
                            getDiskBarToneClass(node.diskFreePercent)
                          )}
                          style={{
                            width: `${Math.max(4, node.diskFreePercent)}%`,
                          }}
                        />
                      </div>
                      <p className="mt-1 text-xs text-foreground/70">
                        {t("nodes.probeLabel", { time: node.diskProbeAt || t("nodes.probeNever") })}
                      </p>
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">
                    {node.lastBackupAt}
                  </td>
                  <td className="px-3 py-2.5">
                    <div className="flex flex-wrap gap-1">
                      {node.tags.map((tag) => (
                        <Badge key={tag} tone="neutral">
                          {tag}
                        </Badge>
                      ))}
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-right">
                    <div className="flex items-center justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t("nodes.testConnectionAriaLabel", { name: node.name })} title={t("nodes.testConnection")}
                        onClick={() => void onTestNode(node)}
                        disabled={testingNodeId === node.id}
                      >
                        {testingNodeId === node.id ? <Loader2 className="size-4 animate-spin" aria-hidden /> : <Activity className="size-4" aria-hidden />}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t("nodes.viewLogsAriaLabel", { name: node.name })} title={t("nodes.viewLogs")}
                        asChild
                      >
                        <Link to={`/app/logs?node=${encodeURIComponent(node.name)}`}>
                          <Terminal className="size-4" aria-hidden />
                        </Link>
                      </Button>
                      <Button
                        size="sm"
                        disabled={triggeringNodeId === node.id}
                        onClick={() => void handleTriggerBackup(node.id, node.name)}
                      >
                        {triggeringNodeId === node.id && <Loader2 className="mr-1 size-4 animate-spin" aria-hidden />}
                        {t("nodes.manualBackup")}
                      </Button>
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                            aria-label={t("nodes.moreActionsAriaLabel", { name: node.name })}
                            title={t("common.more")}
                          >
                            <MoreHorizontal className="size-4" aria-hidden />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end" className="w-48">
                          <DropdownMenuItem
                            disabled={doctorNodeId === node.id}
                            onClick={() => onOpenDoctor?.(node)}
                          >
                            {doctorNodeId === node.id
                              ? <Loader2 className="mr-2 size-4 animate-spin" aria-hidden />
                              : <Stethoscope className="mr-2 size-4" aria-hidden />}
                            {t("nodes.doctor")}
                          </DropdownMenuItem>
                          {isAdmin && (
                            <DropdownMenuItem onClick={() => onOpenTerminal?.(node)}>
                              <MonitorPlay className="mr-2 size-4" aria-hidden />
                              {t("nodes.webTerminal")}
                            </DropdownMenuItem>
                          )}
                          {canBrowseNodeFiles && (
                            <DropdownMenuItem onClick={() => onOpenFileBrowser?.(node)}>
                              <FolderOpen className="mr-2 size-4" aria-hidden />
                              {t("nodes.fileBrowser")}
                            </DropdownMenuItem>
                          )}
                          <DropdownMenuItem onClick={() => openEditDialog(node)}>
                            <Wrench className="mr-2 size-4" aria-hidden />
                            {t("nodes.editNode")}
                          </DropdownMenuItem>
                          {onMigrate && (
                            <DropdownMenuItem onClick={() => onMigrate(node)}>
                              <ArrowRightLeft className="mr-2 size-4" aria-hidden />
                              {t("nodes.migrateShort")}
                            </DropdownMenuItem>
                          )}
                          <DropdownMenuSeparator />
                          <DropdownMenuItem
                            className="text-destructive focus:text-destructive"
                            onClick={() => onDeleteNode(node)}
                          >
                            <Trash2 className="mr-2 size-4" aria-hidden />
                            {t("nodes.deleteNode")}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  </td>
                </tr>
              );
            })
          )}
        </tbody>
      </table>
    </div>
  );
});
