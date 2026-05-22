import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Terminal } from "lucide-react";
import { FormDialog } from "@/components/ui/form-dialog";
import { useStepUpAction } from "@/hooks/use-step-up-action";
import { apiClient } from "@/lib/api/client";
import type { NodeRecord } from "@/types/domain";

export type BatchCommandResult = {
  batchId: string;
  retain: boolean;
};

type BatchCommandDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  nodes: NodeRecord[];
  token: string;
  defaultNodeIds?: number[];
  onSuccess?: (result: BatchCommandResult) => void;
};

export function BatchCommandDialog({
  open,
  onOpenChange,
  nodes,
  token,
  defaultNodeIds,
  onSuccess,
}: BatchCommandDialogProps) {
  const { t } = useTranslation();
  const [selectedNodeIds, setSelectedNodeIds] = useState<number[]>([]);
  const [command, setCommand] = useState("");
  const [name, setName] = useState("");
  const [retain, setRetain] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const withStepUp = useStepUpAction({ persist: false, reuseCached: false });

  useEffect(() => {
    if (open) {
      setSelectedNodeIds(defaultNodeIds?.length ? defaultNodeIds : []);
      setCommand("");
      setName("");
      setRetain(false);
      setError("");
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
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
      setError(t("batchCommand.errorNoNodes"));
      return;
    }
    if (!command.trim()) {
      setError(t("batchCommand.errorEmptyCommand"));
      return;
    }
    if (command.length > 4096) {
      setError(t("batchCommand.errorCommandTooLong"));
      return;
    }

    setSaving(true);
    setError("");
    try {
      const nodeIds = [...selectedNodeIds];
      const result = await withStepUp(async (proof) => {
        await apiClient.requestBatchCommandCredentialGrant(token, {
          nodeIds,
          reason: t("batchCommand.grantReason", { count: nodeIds.length }),
          requestedTtlSeconds: 600,
        }, proof);
        return apiClient.createBatchCommand(
          token,
          nodeIds,
          command.trim(),
          name.trim() || undefined,
          retain,
          proof
        );
      });
      onOpenChange(false);
      onSuccess?.({ batchId: result.batchId, retain: result.retain });
    } catch (err) {
      setError(err instanceof Error ? err.message : t("batchCommand.errorExecutionFailed"));
    } finally {
      setSaving(false);
    }
  }, [selectedNodeIds, command, name, retain, token, onOpenChange, onSuccess, t, withStepUp]);

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("batchCommand.title")}
      description={t("batchCommand.desc")}
      icon={<Terminal className="size-5" />}
      size="lg"
      saving={saving}
      onSubmit={handleSubmit}
      submitLabel={t("batchCommand.submitLabel")}
      savingLabel={t("batchCommand.savingLabel")}
    >
      {error && (
        <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </div>
      )}

      <div>
        <label htmlFor="batch-command-name" className="mb-1.5 block text-sm font-medium">{t("batchCommand.taskNameOptional")}</label>
        <input
          id="batch-command-name"
          type="text"
          className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
          placeholder={t("batchCommand.batchNamePlaceholder")}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </div>

      <div>
        <div className="mb-1.5 flex items-center justify-between">
          <label className="text-sm font-medium">
            {t("batchCommand.selectNodes")} ({selectedNodeIds.length}/{nodes.length})
          </label>
          <button
            type="button"
            className="text-xs text-primary hover:underline"
            onClick={selectAll}
          >
            {t("common.selectAll")}
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
            <p className="py-2 text-center text-sm text-muted-foreground">{t("batchCommand.noAvailableNodes")}</p>
          )}
        </div>
      </div>

      <div>
        <label htmlFor="batch-command-command" className="mb-1.5 block text-sm font-medium">{t("batchCommand.command")}</label>
        <textarea
          id="batch-command-command"
          className="w-full rounded-md border border-border bg-background px-3 py-2 font-mono text-sm"
          rows={3}
          placeholder={t("batchCommand.commandPlaceholder")}
          value={command}
          onChange={(e) => setCommand(e.target.value)}
        />
        <p className="mt-1 text-xs text-muted-foreground">
          {t("batchCommand.commandMaxHint")}
        </p>
      </div>

      <label className="flex cursor-pointer items-center gap-2">
        <input
          type="checkbox"
          checked={retain}
          onChange={(e) => setRetain(e.target.checked)}
          className="size-4 rounded"
        />
        <span className="text-sm">{t("batchCommand.retainRecord")}</span>
        <span className="text-xs text-muted-foreground">
          {t("batchCommand.retainHint")}
        </span>
      </label>
    </FormDialog>
  );
}
