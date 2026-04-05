import React from "react";
import { useTranslation } from "react-i18next";
import { KeyRound } from "lucide-react";
import { SSHKeyActionsMenu } from "@/components/ssh-key-actions-menu";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { EmptyState } from "@/components/ui/empty-state";
import { Badge } from "@/components/ui/badge";
import { formatTime } from "@/lib/api/core";
import { cn } from "@/lib/utils";
import type { NodeRecord, SSHKeyRecord } from "@/types/domain";

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface SSHKeysTableProps {
  loading: boolean;
  pagedItems: SSHKeyRecord[];
  keyUsageMap: Map<string, NodeRecord[]>;
  selectedIds: Set<string>;
  allVisibleSelected: boolean;
  isFiltered: boolean;
  toggleSelection: (keyId: string, checked: boolean) => void;
  toggleSelectAllVisible: (checked: boolean) => void;
  resetFilters: () => void;
  openCreateDialog: () => void;
  openEditDialog: (key: SSHKeyRecord) => void;
  handleDelete: (key: SSHKeyRecord) => void;
  setTestConnectionKey: (key: SSHKeyRecord | null) => void;
  setAssociatedNodesKey: (key: SSHKeyRecord | null) => void;
  openRotationWizard: (key: SSHKeyRecord) => void;
}

// ---------------------------------------------------------------------------
// 密钥类型 → Badge variant 映射
// ---------------------------------------------------------------------------

function keyTypeBadgeVariant(keyType: string): "default" | "warning" | "secondary" {
  switch (keyType.toLowerCase()) {
    case "ed25519":
      return "default";
    case "rsa":
      return "warning";
    case "ecdsa":
      return "secondary";
    default:
      return "secondary";
  }
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export const SSHKeysTable = React.memo(function SSHKeysTable({
  loading,
  pagedItems,
  keyUsageMap,
  selectedIds,
  allVisibleSelected,
  isFiltered,
  toggleSelection,
  toggleSelectAllVisible,
  resetFilters,
  openCreateDialog,
  openEditDialog,
  handleDelete,
  setTestConnectionKey,
  setAssociatedNodesKey,
  openRotationWizard,
}: SSHKeysTableProps) {
  const { t } = useTranslation();

  return (
    <div className="hidden glass-panel overflow-x-auto md:block">
      <table className="min-w-[960px] text-left text-sm">
        <thead>
          <tr className="border-b border-border/70 bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="px-3 py-2.5">
              <input
                type="checkbox"
                aria-label={t("sshKeys.selectAllVisible")}
                className="size-4 accent-primary rounded-sm"
                checked={allVisibleSelected}
                onChange={(e) => toggleSelectAllVisible(e.target.checked)}
              />
            </th>
            <th scope="col" className="px-3 py-2.5">{t("sshKeys.colName")}</th>
            <th scope="col" className="px-3 py-2.5">{t("sshKeys.colUsername")}</th>
            <th scope="col" className="px-3 py-2.5">{t("sshKeys.colType")}</th>
            <th scope="col" className="px-3 py-2.5">{t("sshKeys.colFingerprint")}</th>
            <th scope="col" className="px-3 py-2.5">{t("sshKeys.colLastUsed")}</th>
            <th scope="col" className="px-3 py-2.5">{t("sshKeys.colNodes")}</th>
            <th scope="col" className="px-3 py-2.5 text-right">{t("sshKeys.colActions")}</th>
          </tr>
        </thead>
        <tbody>
          {loading ? (
            <tr>
              <td colSpan={8} className="px-3 py-4 text-muted-foreground">
                {t("common.loading")}
              </td>
            </tr>
          ) : !pagedItems.length ? (
            <tr>
              <td colSpan={8} className="px-3 py-6">
                {isFiltered ? (
                  <FilteredEmptyState
                    className="py-8"
                    title={t("sshKeys.emptyFilteredTitle")}
                    description={t("sshKeys.emptyFilteredDesc")}
                    onReset={resetFilters}
                    onCreate={openCreateDialog}
                    createLabel={t("sshKeys.addKey")}
                    createIcon={KeyRound}
                  />
                ) : (
                  <EmptyState
                    title={t("sshKeys.emptyTitle")}
                    description={t("sshKeys.emptyDesc")}
                  />
                )}
              </td>
            </tr>
          ) : (
            pagedItems.map((key) => {
              const nodeCount = keyUsageMap.get(key.id)?.length ?? 0;
              const isUnused = nodeCount === 0;

              return (
                <tr
                  key={key.id}
                  className={cn(
                    "border-b border-border/60 transition-colors duration-200 ease-out hover:bg-muted/40",
                    isUnused && "opacity-60",
                  )}
                >
                  <td className="px-3 py-2.5">
                    <input
                      type="checkbox"
                      aria-label={t("sshKeys.selectKeyAriaLabel", { name: key.name })}
                      className="size-4 accent-primary rounded-sm"
                      checked={selectedIds.has(key.id)}
                      onChange={(e) => toggleSelection(key.id, e.target.checked)}
                    />
                  </td>
                  <td className="px-3 py-2.5">
                    <div className="flex items-center gap-2">
                      <span className="rounded-md border border-primary/20 bg-primary/10 p-1 text-primary">
                        <KeyRound className="size-3.5" />
                      </span>
                      <span className="font-medium">{key.name}</span>
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">
                    {key.username}
                  </td>
                  <td className="px-3 py-2.5">
                    <Badge variant={keyTypeBadgeVariant(key.keyType)}>
                      {key.keyType.toUpperCase()}
                    </Badge>
                  </td>
                  <td className="px-3 py-2.5">
                    <code className="max-w-[200px] truncate block font-mono text-xs text-muted-foreground">
                      {key.fingerprint}
                    </code>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">
                    {key.lastUsedAt ? (
                      formatTime(key.lastUsedAt)
                    ) : (
                      <span className="italic">{t("sshKeys.neverUsed")}</span>
                    )}
                  </td>
                  <td className="px-3 py-2.5">
                    <Badge variant={nodeCount > 0 ? "success" : "secondary"}>
                      {nodeCount}
                    </Badge>
                  </td>
                  <td className="px-3 py-2.5 text-right">
                    <SSHKeyActionsMenu
                      sshKey={key}
                      nodeCount={nodeCount}
                      onEdit={openEditDialog}
                      onDelete={handleDelete}
                      onTestConnection={(k) => setTestConnectionKey(k)}
                      onViewAssociatedNodes={(k) => setAssociatedNodesKey(k)}
                      onRotate={openRotationWizard}
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
