import React from "react";
import { useTranslation } from "react-i18next";
import { ServerCog } from "lucide-react";
import { Link } from "react-router-dom";
import { ExpiryCountdownBadge } from "@/components/expiry-countdown-badge";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { Badge } from "@/components/ui/badge";
import { getNodeStatusMeta } from "@/lib/status";
import { NodeActionsMenu } from "@/pages/nodes-page.actions-menu";
import type { NodesViewProps } from "@/pages/nodes-page.utils";

export const NodesTable = React.memo(function NodesTable({
  loading,
  requestFailed,
  inventoryEmpty,
  sortedNodes,
  sshKeys,
  selectedNodeSet,
  allVisibleSelected,
  testingNodeId,
  doctorNodeId,
  triggeringNodeId,
  emergencyNodeId,
  toggleNodeSelection,
  toggleSelectAllVisible,
  resetFilters,
  openCreateDialog,
  openEditDialog,
  onTestNode,
  onOpenDoctor,
  onDeleteNode,
  handleTriggerBackup,
  onEmergencyBackup,
  onOpenTerminal,
  onMigrate,
  onOpenFileBrowser,
  isAdmin,
  canOperateNodes,
  canBrowseNodeFiles,
}: NodesViewProps) {
  const { t } = useTranslation();

  return (
    <div
      role="region"
      aria-label={t("nodes.tableAriaLabel")}
      // Keyboard focus enables native horizontal scrolling.
      // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
      tabIndex={0}
      className="hidden overflow-x-auto rounded-lg border border-border bg-card md:block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
    >
      <table className="w-full min-w-[1280px] text-left text-sm">
        <thead>
          <tr className="border-b border-border bg-secondary text-mini uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="w-10 px-3 py-2.5">
              <input
                type="checkbox"
                aria-label={t("nodes.selectAllVisible")}
                className="size-4 accent-primary rounded-sm"
                checked={allVisibleSelected}
                disabled={!sortedNodes.length}
                onChange={(event) => toggleSelectAllVisible(event.target.checked)}
              />
            </th>
            <th scope="col" className="px-3 py-2.5 whitespace-nowrap">{t("nodes.colNode")}</th>
            <th scope="col" className="px-3 py-2.5">{t("nodes.colAddress")}</th>
            <th scope="col" className="px-3 py-2.5">{t("nodes.colAuth")}</th>
            <th scope="col" className="px-3 py-2.5 whitespace-nowrap">{t("nodes.colStatus")}</th>
            <th scope="col" className="px-3 py-2.5 whitespace-nowrap">{t("nodes.colLastConnection")}</th>
            <th scope="col" className="px-3 py-2.5 whitespace-nowrap">{t("nodes.colLastBackup")}</th>
            <th scope="col" className="px-3 py-2.5">{t("nodes.colTags")}</th>
            <th scope="col" className="px-3 py-2.5 text-right whitespace-nowrap">{t("nodes.colActions")}</th>
          </tr>
        </thead>
        <tbody>
          {loading ? (
            <tr>
              <td colSpan={9} className="px-3 py-4 text-muted-foreground">
                {t("nodes.tableLoading")}
              </td>
            </tr>
          ) : !requestFailed && !sortedNodes.length ? (
            <tr>
              <td colSpan={9} className="px-3 py-6">
                {inventoryEmpty ? (
                  <FilteredEmptyState
                    className="py-8"
                    title={t("nodes.inventoryEmptyTitle")}
                    description={t("nodes.inventoryEmptyDesc")}
                    onCreate={isAdmin ? openCreateDialog : undefined}
                    createLabel={t("nodes.emptyCreateLabel")}
                    createIcon={ServerCog}
                  />
                ) : (
                  <FilteredEmptyState
                    className="py-8"
                    title={t("nodes.emptyTitle")}
                    description={t("nodes.emptyDesc")}
                    onReset={resetFilters}
                    onCreate={isAdmin ? openCreateDialog : undefined}
                    createLabel={t("nodes.emptyCreateLabel")}
                    createIcon={ServerCog}
                  />
                )}
              </td>
            </tr>
          ) : (
            sortedNodes.map((node) => {
              const status = getNodeStatusMeta(node.status);
              const keyLabel = node.keyId
                ? sshKeys.find((key) => key.id === node.keyId)?.name || t("common.keyBound")
                : t("common.keyUnbound");
              const checked = selectedNodeSet.has(node.id);

              return (
                <tr
                  key={node.id}
                  data-selected={checked ? "true" : undefined}
                  className="border-b border-border transition-colors duration-200 ease-out hover:bg-accent data-[selected=true]:bg-primary/5"
                >
                  <td className="w-10 px-3 py-2.5">
                    <input
                      type="checkbox"
                      aria-label={t("nodes.selectNodeAriaLabel", { name: node.name })}
                      className="size-4 accent-primary rounded-sm"
                      checked={checked}
                      onChange={(event) => toggleNodeSelection(node.id, event.target.checked)}
                    />
                  </td>
                  <td className="px-3 py-2.5">
                    <Link
                      to={`/app/nodes/${node.id}`}
                      data-testid={`nodes-list-link-${node.id}`}
                      className="font-medium hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      {node.name}
                    </Link>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">
                    <p className="break-all">
                      {node.host}:{node.port}
                    </p>
                    <p className="text-xs">{node.username}</p>
                  </td>
                  <td className="px-3 py-2.5 text-xs text-muted-foreground">
                    <p>{node.authType === "key" ? t("nodes.authKey") : t("nodes.authPassword")}</p>
                    <p>{node.authType === "key" ? keyLabel : "-"}</p>
                  </td>
                  <td className="px-3 py-2.5">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge tone={status.variant}>{status.label}</Badge>
                      <ExpiryCountdownBadge expiryDate={node.expiryDate} archived={node.archived} />
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">
                    <p>{node.lastSeenAt || t("nodes.lastConnectionNever")}</p>
                    <p className="text-xs">
                      {t("nodes.connectionLatency", {
                        latency: node.connectionLatencyMs != null ? `${node.connectionLatencyMs} ms` : "-",
                      })}
                    </p>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">
                    {node.lastBackupAt}
                  </td>
                  <td className="px-3 py-2.5">
                    <div className="flex flex-wrap items-start gap-1">
                      {node.tags.map((tag) => (
                        <Badge key={tag} tone="neutral" className="max-w-48 rounded-md xl:max-w-64">
                          <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">{tag}</span>
                        </Badge>
                      ))}
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-right">
                    <NodeActionsMenu
                      layout="row"
                      node={node}
                      testing={testingNodeId === node.id}
                      doctorRunning={doctorNodeId === node.id}
                      triggering={triggeringNodeId === node.id}
                      emergencyRunning={emergencyNodeId === node.id}
                      isAdmin={isAdmin}
                      canOperateNodes={canOperateNodes}
                      canBrowseNodeFiles={canBrowseNodeFiles}
                      onTestNode={onTestNode}
                      onOpenDoctor={onOpenDoctor}
                      handleTriggerBackup={handleTriggerBackup}
                      onEmergencyBackup={onEmergencyBackup}
                      onOpenTerminal={onOpenTerminal}
                      onOpenFileBrowser={onOpenFileBrowser}
                      openEditDialog={openEditDialog}
                      onMigrate={onMigrate}
                      onDeleteNode={onDeleteNode}
                    />
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
