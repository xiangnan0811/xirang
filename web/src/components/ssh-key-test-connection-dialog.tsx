import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { CheckCircle2, Plug, XCircle } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { createSSHKeysApi, type TestConnectionResult } from "@/lib/api/ssh-keys-api";
import type { NodeRecord, SSHKeyRecord } from "@/types/domain";

interface SSHKeyTestConnectionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  sshKey: SSHKeyRecord | null;
  associatedNodes: NodeRecord[];
  token: string;
}

export function SSHKeyTestConnectionDialog({
  open,
  onOpenChange,
  sshKey,
  associatedNodes,
  token,
}: SSHKeyTestConnectionDialogProps) {
  const { t } = useTranslation();
  const [selectedNodeIds, setSelectedNodeIds] = useState<Set<number>>(new Set());
  const [results, setResults] = useState<TestConnectionResult[]>([]);
  const [loading, setLoading] = useState(false);

  // 对话框打开时重置状态，默认选中所有关联节点
  useEffect(() => {
    if (open) {
      setSelectedNodeIds(new Set(associatedNodes.map((n) => n.id)));
      setResults([]);
      setLoading(false);
    }
  }, [open, associatedNodes]);

  const allSelected = useMemo(
    () =>
      associatedNodes.length > 0 &&
      associatedNodes.every((n) => selectedNodeIds.has(n.id)),
    [associatedNodes, selectedNodeIds],
  );

  const toggleNode = useCallback((nodeId: number, checked: boolean) => {
    setSelectedNodeIds((prev) => {
      const next = new Set(prev);
      if (checked) {
        next.add(nodeId);
      } else {
        next.delete(nodeId);
      }
      return next;
    });
  }, []);

  const toggleAll = useCallback(
    (checked: boolean) => {
      if (checked) {
        setSelectedNodeIds(new Set(associatedNodes.map((n) => n.id)));
      } else {
        setSelectedNodeIds(new Set());
      }
    },
    [associatedNodes],
  );

  const handleTest = useCallback(async () => {
    if (!sshKey || selectedNodeIds.size === 0) return;
    setLoading(true);
    setResults([]);
    try {
      // NodeRecord.id 是 number，API 期望 "node-{id}" 格式的字符串
      const nodeIdStrings = Array.from(selectedNodeIds).map((id) => `node-${id}`);
      const data = await createSSHKeysApi().testConnection(token, sshKey.id, nodeIdStrings);
      setResults(data);
    } catch {
      // 网络或服务端错误时，对所有选中节点生成失败结果
      const fallback: TestConnectionResult[] = Array.from(selectedNodeIds).map((id) => {
        const node = associatedNodes.find((n) => n.id === id);
        return {
          nodeId: `node-${id}`,
          name: node?.name ?? String(id),
          host: node?.host ?? "",
          port: node?.port ?? 22,
          success: false,
          latencyMs: 0,
          error: t("common.networkError"),
        };
      });
      setResults(fallback);
    } finally {
      setLoading(false);
    }
  }, [sshKey, selectedNodeIds, token, associatedNodes, t]);

  const hasResults = results.length > 0;

  if (!sshKey) return null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="sm">
        <DialogHeader>
          <div className="flex items-center gap-2">
            <Plug className="size-5 text-primary" />
            <DialogTitle>{t("sshKeys.testConnectionTitle")}</DialogTitle>
          </div>
          <DialogDescription>
            {t("sshKeys.testConnectionDesc", { name: sshKey.name })}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-4">
          {/* 节点多选列表 */}
          <div>
            <p className="mb-2 text-sm font-medium">
              {t("sshKeys.selectTestNodes")}
            </p>

            {associatedNodes.length > 0 && (
              <label className="mb-2 flex cursor-pointer items-center gap-2 text-sm text-muted-foreground">
                <input
                  type="checkbox"
                  className="accent-primary size-4 rounded"
                  checked={allSelected}
                  onChange={(e) => toggleAll(e.target.checked)}
                  aria-label={t("common.selectAll")}
                />
                {t("common.selectAll")}
              </label>
            )}

            <ul
              className="max-h-48 space-y-1 overflow-y-auto thin-scrollbar"
            >
              {associatedNodes.map((node) => (
                <li key={node.id}>
                  <label
                    className={cn(
                      "flex cursor-pointer items-center gap-2.5 rounded-lg px-3 py-2 text-sm transition-colors",
                      "hover:bg-accent/60",
                      selectedNodeIds.has(node.id) && "bg-accent/40",
                    )}
                  >
                    <input
                      type="checkbox"
                      className="accent-primary size-4 rounded"
                      checked={selectedNodeIds.has(node.id)}
                      onChange={(e) => toggleNode(node.id, e.target.checked)}
                      aria-label={node.name}
                    />
                    <div className="min-w-0 flex-1">
                      <span className="truncate font-medium">{node.name}</span>
                      <span className="ml-2 text-xs text-muted-foreground">
                        {node.host}:{node.port}
                      </span>
                    </div>
                  </label>
                </li>
              ))}
            </ul>
          </div>

          {/* 测试结果 */}
          {hasResults && (
            <ul className="space-y-1.5">
              {results.map((r) => (
                <li
                  key={r.nodeId}
                  className={cn(
                    "flex items-center justify-between rounded-lg px-3 py-2 text-sm",
                    r.success
                      ? "bg-success/10 text-success"
                      : "bg-destructive/10 text-destructive",
                  )}
                >
                  <div className="flex items-center gap-2 min-w-0">
                    {r.success ? (
                      <CheckCircle2 className="size-4 shrink-0" />
                    ) : (
                      <XCircle className="size-4 shrink-0" />
                    )}
                    <span className="truncate font-medium">{r.name}</span>
                    <span className="text-xs opacity-70">
                      {r.host}:{r.port}
                    </span>
                  </div>
                  <span className="shrink-0 text-xs">
                    {r.success
                      ? `${r.latencyMs}ms`
                      : r.error ?? t("sshKeys.connectionFailed")}
                  </span>
                </li>
              ))}
            </ul>
          )}

          {/* 提示信息 */}
          <InlineAlert tone="info">{t("sshKeys.testHint")}</InlineAlert>
        </DialogBody>

        <DialogFooter>
          <Button
            variant="secondary"
            onClick={() => onOpenChange(false)}
          >
            {t("common.close")}
          </Button>
          <Button
            onClick={handleTest}
            loading={loading}
            disabled={selectedNodeIds.size === 0}
          >
            {hasResults ? t("sshKeys.retest") : t("sshKeys.startTest")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
