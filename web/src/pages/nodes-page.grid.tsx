import React from "react";
import { useTranslation } from "react-i18next";
import { Activity, ArrowRightLeft, FolderOpen, Loader2, MonitorPlay, ServerCog, ShieldAlert, Terminal, Trash2, Wrench } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { ExpiryCountdownBadge } from "@/components/expiry-countdown-badge";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { LoadingState } from "@/components/ui/loading-state";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { getNodeStatusMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { NodesViewProps } from "@/pages/nodes-page.utils";


export const NodesGrid = React.memo(function NodesGrid({
  loading,
  sortedNodes,
  sshKeys,
  selectedNodeSet,
  selectedNodeId,
  selectedNodeIds,
  allVisibleSelected,
  testingNodeId,
  triggeringNodeId,
  toggleNodeSelection,
  toggleSelectAllVisible,
  setSelectedNodeId,
  handleBulkDelete,
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
  const navigate = useNavigate();

  return (
    <>
      <div className="space-y-3 p-2 md:hidden">
        <div className="flex items-center gap-2 justify-between rounded-lg border border-border bg-secondary px-3 py-2 text-xs text-muted-foreground">
          <div className="inline-flex items-center gap-2">
            <input
              type="checkbox"
              aria-label={t("nodes.selectAllVisible")}
              className="size-4"
              checked={allVisibleSelected}
              onChange={(event) =>
                toggleSelectAllVisible(event.target.checked)
              }
            />
            <span>{t("common.selectAll")}</span>
          </div>
          <Button
            size="sm"
            variant="destructive"
            disabled={!selectedNodeIds.length}
            onClick={() => void handleBulkDelete()}
          >
            {t("nodes.deleteCount", { count: selectedNodeIds.length })}
          </Button>
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

        {sortedNodes.map((node) => {
          const status = getNodeStatusMeta(node.status);
          const checked = selectedNodeSet.has(node.id);
          return (
            <div
              key={node.id}
              className="rounded-lg border border-border bg-card p-3 shadow-sm"
            >
              <div className="flex items-start justify-between gap-2">
                <label className="inline-flex items-center gap-2 text-xs text-muted-foreground">
                  <input
                    type="checkbox"
                    aria-label={t("nodes.selectNodeAriaLabel", { name: node.name })}
                    className="size-4"
                    checked={checked}
                    onChange={(event) =>
                      toggleNodeSelection(node.id, event.target.checked)
                    }
                  />
                  {t("nodes.selectLabel")}
                </label>
                <div className="inline-flex items-center gap-1.5">
                  <Badge variant={status.variant}>{status.label}</Badge>
                  <ExpiryCountdownBadge expiryDate={node.expiryDate} archived={node.archived} />
                </div>
              </div>

              <div className="mt-2">
                <p className="font-medium">{node.name}</p>
                <p className="text-xs text-muted-foreground">
                  <span className="break-all">{node.host}:{node.port} · {node.username}</span>
                </p>
              </div>

              <div className="mt-2 space-y-1 text-xs text-muted-foreground">
                <p>
                  {t("nodes.diskFreeLabel", { pct: node.diskFreePercent, probe: node.diskProbeAt || t("nodes.probeNever") })}
                </p>
                <p>{t("nodes.lastBackupLabel", { time: node.lastBackupAt })}</p>
                <p className="break-words">{t("nodes.tagsLabel", { tags: node.tags.join(" / ") || "-" })}</p>
              </div>

              <div className="mt-4 flex flex-wrap-reverse items-center justify-between gap-2 border-t border-border pt-3">
                <div className="flex flex-wrap items-center gap-1">
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
                </div>
                <Button
                  size="sm"
                  disabled={triggeringNodeId === node.id}
                  onClick={() => void handleTriggerBackup(node.id, node.name)}
                >
                  {triggeringNodeId === node.id && <Loader2 className="mr-1 size-4 animate-spin" />}
                  {t("nodes.manualBackup")}
                </Button>
              </div>
            </div>
          );
        })}
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

        {sortedNodes.map((node) => {
          const status = getNodeStatusMeta(node.status);
          const keyLabel = node.keyId
            ? sshKeys.find((key) => key.id === node.keyId)?.name ||
            t("common.keyBound")
            : t("common.keyUnbound");
          const checked = selectedNodeSet.has(node.id);
          const isSelected = selectedNodeId === node.id;

          return (
            <div
              key={node.id}
              tabIndex={0}
              aria-label={t("nodes.nodeCardAriaLabel", { name: node.name })}
              className={cn(
                "rounded-lg border border-border bg-card shadow-sm hover:shadow-md transition-shadow p-3 outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:border-transparent",
                isSelected && "border-primary/45 ring-1 ring-primary/40"
              )}
              onClick={(e) => {
                if (
                  e.target instanceof HTMLElement &&
                  e.target.closest("button, input, a, label, select, textarea")
                ) {
                  return;
                }
                setSelectedNodeId(node.id);
              }}
              onKeyDown={(e) => {
                if (e.target !== e.currentTarget) {
                  return;
                }
                if (e.key !== "Enter" && e.key !== " ") {
                  return;
                }
                e.preventDefault();
                setSelectedNodeId(node.id);
              }}
            >
              <div className="flex items-start justify-between gap-2">
                <label className="inline-flex items-center gap-2 text-xs text-muted-foreground">
                  <input
                    type="checkbox"
                    aria-label={t("nodes.selectNodeAriaLabel", { name: node.name })}
                    className="size-4"
                    checked={checked}
                    onChange={(event) =>
                      toggleNodeSelection(node.id, event.target.checked)
                    }
                  />
                  {t("nodes.selectLabel")}
                </label>
                <div className="inline-flex items-center gap-1.5">
                  <Badge variant={status.variant}>{status.label}</Badge>
                  <ExpiryCountdownBadge expiryDate={node.expiryDate} archived={node.archived} />
                </div>
              </div>

              <div className="mt-2">
                <p className="font-medium">{node.name}</p>
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
                  {t("nodes.diskFreeDetail", { pct: node.diskFreePercent, latency: node.connectionLatencyMs ? `${node.connectionLatencyMs}ms` : "-" })}
                </p>
                <p>{t("nodes.probeLabel", { time: node.diskProbeAt || t("nodes.probeNever") })}</p>
                <p>{t("nodes.lastBackupLabel", { time: node.lastBackupAt })}</p>
                <p className="break-words">
                  {t("nodes.tagsLabel", { tags: node.tags.length ? node.tags.join(" / ") : "-" })}
                </p>
              </div>

              <div className="mt-4 flex flex-wrap-reverse items-center justify-between gap-2 border-t border-border pt-3">
                <div className="flex flex-wrap items-center gap-1">
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
                </div>
                <div className="flex items-center gap-1">
                  <Button
                    size="sm"
                    variant="destructive"
                    className="text-xs"
                    disabled={emergencyNodeId === node.id}
                    onClick={() => onEmergencyBackup?.(node.id, node.name)}
                    title={t("nodes.emergencyBackup")}
                  >
                    {emergencyNodeId === node.id ? <Loader2 className="mr-1 size-3.5 animate-spin" /> : <ShieldAlert className="mr-1 size-3.5" />}
                    {t("nodes.emergencyBackup")}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    className="text-xs"
                    onClick={() => onMigrate?.(node)}
                    title={t("nodes.migrateTo")}
                  >
                    <ArrowRightLeft className="mr-1 size-3.5" />
                    {t("nodes.migrateShort")}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={triggeringNodeId === node.id}
                    onClick={() => void handleTriggerBackup(node.id, node.name)}
                  >
                    {triggeringNodeId === node.id && <Loader2 className="mr-1 size-4 animate-spin" />}
                    {t("nodes.manualBackup")}
                  </Button>
                </div>
              </div>
            </div>
          );
        })}
      </div>
    </>
  );
});
