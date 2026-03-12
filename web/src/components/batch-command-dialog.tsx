import { useCallback, useEffect, useState } from "react";
import { Terminal } from "lucide-react";
import { FormDialog } from "@/components/ui/form-dialog";
import { apiClient } from "@/lib/api/client";
import type { NodeRecord } from "@/types/domain";

type BatchCommandDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  nodes: NodeRecord[];
  token: string;
  onSuccess?: (batchId: string) => void;
};

const TEMPLATES_KEY = "xirang:batch-cmd-templates";

function loadTemplates(): string[] {
  try {
    const raw = localStorage.getItem(TEMPLATES_KEY);
    return raw ? JSON.parse(raw) : [];
  } catch {
    return [];
  }
}

function saveTemplate(cmd: string) {
  const templates = loadTemplates().filter((t) => t !== cmd);
  templates.unshift(cmd);
  localStorage.setItem(TEMPLATES_KEY, JSON.stringify(templates.slice(0, 10)));
}

export function BatchCommandDialog({
  open,
  onOpenChange,
  nodes,
  token,
  onSuccess,
}: BatchCommandDialogProps) {
  const [selectedNodeIds, setSelectedNodeIds] = useState<number[]>([]);
  const [command, setCommand] = useState("");
  const [name, setName] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [templates] = useState(loadTemplates);

  useEffect(() => {
    if (open) {
      setSelectedNodeIds([]);
      setCommand("");
      setName("");
      setError("");
    }
  }, [open]);

  const toggleNode = useCallback((nodeId: number) => {
    setSelectedNodeIds((prev) =>
      prev.includes(nodeId) ? prev.filter((id) => id !== nodeId) : [...prev, nodeId]
    );
  }, []);

  const selectAll = useCallback(() => {
    setSelectedNodeIds(nodes.map((n) => n.id));
  }, [nodes]);

  const handleSubmit = useCallback(async () => {
    if (selectedNodeIds.length === 0) {
      setError("请至少选择一个节点");
      return;
    }
    if (!command.trim()) {
      setError("命令不能为空");
      return;
    }
    if (command.length > 4096) {
      setError("命令长度不能超过 4096 字符");
      return;
    }

    setSaving(true);
    setError("");
    try {
      const result = await apiClient.createBatchCommand(
        token,
        selectedNodeIds,
        command.trim(),
        name.trim() || undefined
      );
      saveTemplate(command.trim());
      onOpenChange(false);
      onSuccess?.(result.batchId);
    } catch (err) {
      setError(err instanceof Error ? err.message : "执行失败");
    } finally {
      setSaving(false);
    }
  }, [selectedNodeIds, command, name, token, onOpenChange, onSuccess]);

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="批量命令执行"
      description="选择节点并输入要执行的命令"
      icon={<Terminal className="size-5" />}
      size="lg"
      saving={saving}
      onSubmit={handleSubmit}
      submitLabel="执行"
      savingLabel="执行中..."
    >
      {error && (
        <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </div>
      )}

      <div>
        <label className="mb-1.5 block text-sm font-medium">任务名称（可选）</label>
        <input
          type="text"
          className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
          placeholder="默认自动生成"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </div>

      <div>
        <div className="mb-1.5 flex items-center justify-between">
          <label className="text-sm font-medium">
            选择节点 ({selectedNodeIds.length}/{nodes.length})
          </label>
          <button
            type="button"
            className="text-xs text-primary hover:underline"
            onClick={selectAll}
          >
            全选
          </button>
        </div>
        <div className="max-h-32 space-y-1 overflow-y-auto rounded-md border border-border p-2">
          {nodes.map((node) => (
            <label
              key={node.id}
              className="flex cursor-pointer items-center gap-2 rounded px-2 py-1 text-sm hover:bg-muted"
            >
              <input
                type="checkbox"
                checked={selectedNodeIds.includes(node.id)}
                onChange={() => toggleNode(node.id)}
                className="rounded"
              />
              <span>{node.name}</span>
              <span className="text-xs text-muted-foreground">({node.host})</span>
            </label>
          ))}
          {nodes.length === 0 && (
            <p className="py-2 text-center text-sm text-muted-foreground">暂无可用节点</p>
          )}
        </div>
      </div>

      <div>
        <label className="mb-1.5 block text-sm font-medium">命令</label>
        {templates.length > 0 && (
          <div className="mb-1.5 flex flex-wrap gap-1">
            {templates.slice(0, 5).map((tpl) => (
              <button
                key={tpl}
                type="button"
                className="rounded border border-border px-2 py-0.5 text-xs text-muted-foreground hover:bg-muted"
                onClick={() => setCommand(tpl)}
              >
                {tpl.length > 30 ? tpl.slice(0, 30) + "..." : tpl}
              </button>
            ))}
          </div>
        )}
        <textarea
          className="w-full rounded-md border border-border bg-background px-3 py-2 font-mono text-sm"
          rows={3}
          placeholder="输入要在所有选中节点上执行的命令，如: df -h"
          value={command}
          onChange={(e) => setCommand(e.target.value)}
        />
        <p className="mt-1 text-xs text-muted-foreground">
          命令将通过 SSH 在每个节点上执行，最大 4096 字符
        </p>
      </div>
    </FormDialog>
  );
}
