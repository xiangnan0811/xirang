import React from "react";
import { useTranslation } from "react-i18next";
import { Activity, FolderOpen, Loader2, MonitorPlay, ServerCog, Terminal, Trash2, Wrench } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { StatusPulse } from "@/components/status-pulse";
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
  triggeringNodeId,
  toggleNodeSelection,
  toggleSelectAllVisible,
  resetFilters,
  openCreateDialog,
  openEditDialog,
  onTestNode,
  onDeleteNode,
  handleTriggerBackup,
  onOpenTerminal,
  onOpenFileBrowser,
  isAdmin,
}: NodesViewProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();

  return (
    <div className="hidden glass-panel overflow-x-auto md:block">
      <table className="min-w-[1280px] text-left text-sm">
        <thead>
          <tr className="border-b border-border/70 bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
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
            <th scope="col" className="px-3 py-2.5">{t("nodes.colStatus")}</th>
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
                <tr key={node.id} className="border-b border-border/60 transition-colors duration-200 ease-out hover:bg-muted/40">
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
                    <p className="font-medium">{node.name}</p>
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
                  <td className="px-3 py-2.5">
                    <div className="inline-flex items-center gap-1.5">
                      <StatusPulse tone={node.status} />
                      <Badge variant={status.variant}>
                        {status.label}
                      </Badge>
                    </div>
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
                      <p className="mt-1 text-[11px] text-muted-foreground">
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
                        <Badge key={tag} variant="outline">
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
                        {testingNodeId === node.id ? <Loader2 className="size-4 animate-spin" /> : <Activity className="size-4" />}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t("nodes.viewLogsAriaLabel", { name: node.name })} title={t("nodes.viewLogs")}
                        onClick={() =>
                          navigate(`/app/logs?node=${encodeURIComponent(node.name)}`)
                        }
                      >
                        <Terminal className="size-4" />
                      </Button>
                      {isAdmin && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                          aria-label={t("nodes.webTerminalAriaLabel", { name: node.name })} title={t("nodes.webTerminal")}
                          onClick={() => onOpenTerminal?.(node)}
                        >
                          <MonitorPlay className="size-4" />
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t("nodes.fileBrowserAriaLabel", { name: node.name })} title={t("nodes.fileBrowser")}
                        onClick={() => onOpenFileBrowser?.(node)}
                      >
                        <FolderOpen className="size-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t("nodes.editNodeAriaLabel", { name: node.name })} title={t("nodes.editNode")}
                        onClick={() => openEditDialog(node)}
                      >
                        <Wrench className="size-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-destructive/80 hover:bg-destructive/10 hover:text-destructive"
                        aria-label={t("nodes.deleteNodeAriaLabel", { name: node.name })} title={t("nodes.deleteNode")}
                        onClick={() => onDeleteNode(node)}
                      >
                        <Trash2 className="size-4" />
                      </Button>
                      <Button
                        size="sm"
                        className="ml-2"
                        disabled={triggeringNodeId === node.id}
                        onClick={() => void handleTriggerBackup(node.id, node.name)}
                      >
                        {triggeringNodeId === node.id && <Loader2 className="mr-1 size-4 animate-spin" />}
                        {t("nodes.manualBackup")}
                      </Button>
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
