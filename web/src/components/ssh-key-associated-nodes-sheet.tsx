import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { ExternalLink, Server, Unplug, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { EmptyState } from "@/components/ui/empty-state";
import type { NodeRecord, SSHKeyRecord } from "@/types/domain";

interface SSHKeyAssociatedNodesSheetProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  sshKey: SSHKeyRecord | null;
  nodes: NodeRecord[];
}

/**
 * 侧滑面板：展示某个 SSH Key 关联的节点列表。
 * 基于 @radix-ui/react-dialog 构建，从右侧滑入。
 */
export function SSHKeyAssociatedNodesSheet({
  open,
  onOpenChange,
  sshKey,
  nodes,
}: SSHKeyAssociatedNodesSheetProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();

  // 过滤出使用此 SSH Key 的节点
  const associatedNodes = useMemo(() => {
    if (!sshKey) return [];
    return nodes.filter((node) => node.keyId === sshKey.id);
  }, [sshKey, nodes]);

  const handleNodeClick = () => {
    onOpenChange(false);
    navigate("/app/nodes");
  };

  if (!sshKey) return null;

  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        {/* 半透明遮罩 */}
        <DialogPrimitive.Overlay
          className="fixed inset-0 z-50 bg-background/60 backdrop-blur-sm data-[state=open]:animate-fade-in"
        />
        {/* 右侧滑入面板 */}
        <DialogPrimitive.Content
          className={cn(
            "fixed inset-y-0 right-0 z-50 flex w-full flex-col",
            "border-l border-border/60 bg-background/95 backdrop-blur-md shadow-panel",
            "sm:max-w-[420px]",
            "transition-transform duration-300 ease-out data-[state=open]:translate-x-0 data-[state=closed]:translate-x-full",
            "focus:outline-none"
          )}
          aria-describedby="sheet-description"
        >
          {/* 头部 */}
          <div className="flex items-center justify-between border-b border-border/40 px-5 py-4">
            <div className="min-w-0 flex-1">
              <DialogPrimitive.Title className="text-base font-semibold truncate">
                {t("sshKeys.associatedNodesTitle")}
              </DialogPrimitive.Title>
              <DialogPrimitive.Description
                id="sheet-description"
                className="mt-0.5 text-xs text-muted-foreground truncate"
              >
                {t("sshKeys.associatedNodesDesc", {
                  name: sshKey.name,
                  count: associatedNodes.length,
                })}
              </DialogPrimitive.Description>
            </div>
            <DialogPrimitive.Close
              className="ml-3 shrink-0 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              aria-label={t("common.close")}
            >
              <X className="size-4" />
            </DialogPrimitive.Close>
          </div>

          {/* 提示文字 */}
          {associatedNodes.length > 0 && (
            <p className="px-5 pt-3 text-mini text-muted-foreground/70">
              {t("sshKeys.clickToNavigate")}
            </p>
          )}

          {/* 节点列表 */}
          <div className="flex-1 overflow-y-auto px-5 py-3 thin-scrollbar">
            {associatedNodes.length === 0 ? (
              <EmptyState
                icon={Unplug}
                title={t("sshKeys.noAssociatedNodes")}
                className="mt-8 border-none bg-transparent shadow-none"
              />
            ) : (
              <ul className="space-y-2">
                {associatedNodes.map((node) => (
                  <li key={node.id}>
                    <button
                      type="button"
                      className={cn(
                        "interactive-surface w-full rounded-lg px-4 py-3 text-left",
                        "transition-colors hover:bg-accent/60",
                        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      )}
                      onClick={handleNodeClick}
                      aria-label={`${node.name} — ${node.host}:${node.port}`}
                    >
                      <div className="flex items-center justify-between gap-3">
                        <div className="flex items-center gap-2.5 min-w-0">
                          {/* 状态指示点 */}
                          <span
                            className={cn(
                              "size-2 shrink-0 rounded-full",
                              node.status === "online"
                                ? "bg-success shadow-[0_0_6px_hsl(var(--success)/0.4)]"
                                : "bg-destructive shadow-[0_0_6px_hsl(var(--destructive)/0.4)]"
                            )}
                            aria-label={node.status === "online" ? "online" : "offline"}
                          />
                          {/* 节点名称 */}
                          <span className="truncate text-sm font-medium text-foreground">
                            {node.name}
                          </span>
                        </div>
                        <ExternalLink className="size-3.5 shrink-0 text-muted-foreground/50" />
                      </div>
                      {/* 主机 + 用户名 */}
                      <div className="mt-1.5 flex items-center gap-1.5 pl-[18px] text-xs text-muted-foreground">
                        <Server className="size-3 shrink-0 opacity-50" />
                        <span className="truncate">
                          {node.host}:{node.port}
                        </span>
                        <span className="text-border">|</span>
                        <span className="truncate">{node.username}</span>
                      </div>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
