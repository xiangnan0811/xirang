import React from "react";
import { useTranslation } from "react-i18next";
import { Copy, KeyRound, Plug } from "lucide-react";
import { SSHKeyActionsMenu } from "@/components/ssh-key-actions-menu";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { EmptyState } from "@/components/ui/empty-state";
import { LoadingState } from "@/components/ui/loading-state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import { formatTime } from "@/lib/api/core";
import { cn } from "@/lib/utils";
import type { NodeRecord, SSHKeyRecord } from "@/types/domain";

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface SSHKeysGridProps {
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

export const SSHKeysGrid = React.memo(function SSHKeysGrid({
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
}: SSHKeysGridProps) {
  const { t } = useTranslation();

  const handleCopyPublicKey = async (key: SSHKeyRecord) => {
    if (!key.publicKey) {
      toast.error(t("sshKeys.noPublicKey"));
      return;
    }
    try {
      await navigator.clipboard.writeText(key.publicKey);
      toast.success(t("sshKeys.publicKeyCopied"));
    } catch {
      toast.error(t("sshKeys.copyFailed"));
    }
  };

  // 空 / 加载状态的渲染
  const emptyOrLoading = (colSpanClass: string) => (
    <>
      {loading ? (
        <LoadingState
          className={colSpanClass}
          title={t("sshKeys.loadingTitle")}
          description={t("sshKeys.loadingDesc")}
          rows={3}
        />
      ) : null}

      {!loading && !pagedItems.length ? (
        isFiltered ? (
          <FilteredEmptyState
            className={colSpanClass}
            title={t("sshKeys.emptyFilteredTitle")}
            description={t("sshKeys.emptyFilteredDesc")}
            onReset={resetFilters}
            onCreate={openCreateDialog}
            createLabel={t("sshKeys.addKey")}
            createIcon={KeyRound}
          />
        ) : (
          <EmptyState
            className={colSpanClass}
            title={t("sshKeys.emptyTitle")}
            description={t("sshKeys.emptyDesc")}
          />
        )
      ) : null}
    </>
  );

  return (
    <>
      {/* ---------- 移动端列表 ---------- */}
      <div className="space-y-3 p-2 md:hidden">
        <div className="flex items-center gap-2 justify-between rounded-xl border border-border/75 bg-muted/20 px-3 py-2 text-xs text-muted-foreground">
          <div className="inline-flex items-center gap-2">
            <input
              type="checkbox"
              aria-label={t("sshKeys.selectAllVisible")}
              className="size-4"
              checked={allVisibleSelected}
              onChange={(e) => toggleSelectAllVisible(e.target.checked)}
            />
            <span>{t("common.selectAll")}</span>
          </div>
        </div>

        {emptyOrLoading("")}

        {pagedItems.map((key) => {
          const nodeCount = keyUsageMap.get(key.id)?.length ?? 0;
          const isUnused = nodeCount === 0;

          return (
            <div
              key={key.id}
              className={cn(
                "rounded-xl border border-border/75 bg-background/70 p-3 shadow-sm",
                isUnused && "opacity-60",
              )}
            >
              {/* 顶部：选择框 + 类型 Badge */}
              <div className="flex items-start justify-between gap-2">
                <label className="inline-flex items-center gap-2 text-xs text-muted-foreground">
                  <input
                    type="checkbox"
                    aria-label={t("sshKeys.selectKeyAriaLabel", { name: key.name })}
                    className="size-4"
                    checked={selectedIds.has(key.id)}
                    onChange={(e) => toggleSelection(key.id, e.target.checked)}
                  />
                </label>
                <div className="inline-flex items-center gap-1.5">
                  <Badge variant={keyTypeBadgeVariant(key.keyType)}>
                    {key.keyType.toUpperCase()}
                  </Badge>
                  <SSHKeyActionsMenu
                    sshKey={key}
                    nodeCount={nodeCount}
                    onEdit={openEditDialog}
                    onDelete={handleDelete}
                    onTestConnection={(k) => setTestConnectionKey(k)}
                    onViewAssociatedNodes={(k) => setAssociatedNodesKey(k)}
                    onRotate={openRotationWizard}
                  />
                </div>
              </div>

              {/* 名称 + 用户名 */}
              <div className="mt-2">
                <div className="flex items-center gap-2">
                  <span className="rounded-md border border-primary/20 bg-primary/10 p-1 text-primary">
                    <KeyRound className="size-3.5" />
                  </span>
                  <p className="font-medium">{key.name}</p>
                </div>
                <p className="mt-0.5 text-xs text-muted-foreground">{key.username}</p>
              </div>

              {/* 指纹 + 最后使用 */}
              <div className="mt-2 grid grid-cols-2 gap-2 text-xs text-muted-foreground">
                <div>
                  <p className="text-[10px] uppercase tracking-wide">{t("sshKeys.colFingerprint")}</p>
                  <code className="mt-0.5 block truncate font-mono">{key.fingerprint}</code>
                </div>
                <div>
                  <p className="text-[10px] uppercase tracking-wide">{t("sshKeys.colLastUsed")}</p>
                  <p className="mt-0.5">
                    {key.lastUsedAt ? formatTime(key.lastUsedAt) : (
                      <span className="italic">{t("sshKeys.neverUsed")}</span>
                    )}
                  </p>
                </div>
              </div>

              {/* 底部：使用状态 + 快捷操作 */}
              <div className="mt-3 flex flex-wrap items-center justify-between gap-2 border-t border-border/40 pt-3">
                <Badge variant={nodeCount > 0 ? "success" : "secondary"}>
                  {nodeCount > 0
                    ? t("sshKeys.nodesInUse", { count: nodeCount })
                    : t("sshKeys.unusedLabel")}
                </Badge>
                <div className="flex items-center gap-1">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                    aria-label={t("sshKeys.copyPublicKey")}
                    title={t("sshKeys.copyPublicKey")}
                    onClick={() => void handleCopyPublicKey(key)}
                  >
                    <Copy className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                    aria-label={t("sshKeys.testConnection")}
                    title={t("sshKeys.testConnection")}
                    onClick={() => setTestConnectionKey(key)}
                  >
                    <Plug className="size-4" />
                  </Button>
                </div>
              </div>
            </div>
          );
        })}
      </div>

      {/* ---------- 桌面端卡片网格 ---------- */}
      <div className="hidden gap-3 md:grid md:grid-cols-2 lg:grid-cols-3">
        {emptyOrLoading("md:col-span-2 lg:col-span-3")}

        {pagedItems.map((key) => {
          const nodeCount = keyUsageMap.get(key.id)?.length ?? 0;
          const isUnused = nodeCount === 0;

          return (
            <div
              key={key.id}
              className={cn(
                "interactive-surface p-3",
                isUnused && "opacity-60",
              )}
            >
              {/* 顶部：选择框 + 名称 + 用户名 + 类型 + 操作菜单 */}
              <div className="flex items-start justify-between gap-2">
                <div className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    aria-label={t("sshKeys.selectKeyAriaLabel", { name: key.name })}
                    className="size-4 accent-primary rounded-sm"
                    checked={selectedIds.has(key.id)}
                    onChange={(e) => toggleSelection(key.id, e.target.checked)}
                  />
                  <span className="rounded-md border border-primary/20 bg-primary/10 p-1 text-primary">
                    <KeyRound className="size-3.5" />
                  </span>
                  <div className="min-w-0">
                    <p className="font-medium truncate">{key.name}</p>
                    <p className="text-xs text-muted-foreground truncate">{key.username}</p>
                  </div>
                </div>
                <div className="flex items-center gap-1.5 shrink-0">
                  <Badge variant={keyTypeBadgeVariant(key.keyType)}>
                    {key.keyType.toUpperCase()}
                  </Badge>
                  <SSHKeyActionsMenu
                    sshKey={key}
                    nodeCount={nodeCount}
                    onEdit={openEditDialog}
                    onDelete={handleDelete}
                    onTestConnection={(k) => setTestConnectionKey(k)}
                    onViewAssociatedNodes={(k) => setAssociatedNodesKey(k)}
                    onRotate={openRotationWizard}
                  />
                </div>
              </div>

              {/* 中部：指纹 + 最后使用 */}
              <div className="mt-3 grid grid-cols-2 gap-2 text-xs text-muted-foreground">
                <div>
                  <p className="text-[10px] uppercase tracking-wide">{t("sshKeys.colFingerprint")}</p>
                  <code className="mt-0.5 block truncate font-mono">{key.fingerprint}</code>
                </div>
                <div>
                  <p className="text-[10px] uppercase tracking-wide">{t("sshKeys.colLastUsed")}</p>
                  <p className="mt-0.5">
                    {key.lastUsedAt ? formatTime(key.lastUsedAt) : (
                      <span className="italic">{t("sshKeys.neverUsed")}</span>
                    )}
                  </p>
                </div>
              </div>

              {/* 底部：使用状态 + 快捷操作 */}
              <div className="mt-3 flex flex-wrap items-center justify-between gap-2 border-t border-border/40 pt-3">
                <Badge variant={nodeCount > 0 ? "success" : "secondary"}>
                  {nodeCount > 0
                    ? t("sshKeys.nodesInUse", { count: nodeCount })
                    : t("sshKeys.unusedLabel")}
                </Badge>
                <div className="flex items-center gap-1">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                    aria-label={t("sshKeys.copyPublicKey")}
                    title={t("sshKeys.copyPublicKey")}
                    onClick={() => void handleCopyPublicKey(key)}
                  >
                    <Copy className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                    aria-label={t("sshKeys.testConnection")}
                    title={t("sshKeys.testConnection")}
                    onClick={() => setTestConnectionKey(key)}
                  >
                    <Plug className="size-4" />
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
