import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Terminal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FormDialog } from "@/components/ui/form-dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
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

const dangerousCommandPatterns = [
  { key: "fileRemoval", pattern: /\brm\s+-[^\n;|&]*[rf][^\n;|&]*\s+/i },
  { key: "diskFormatting", pattern: /\b(?:mkfs(?:\.\w+)?|wipefs)\b/i },
  { key: "diskWrite", pattern: /\bdd\s+[^\n;|&]*\bof=/i },
  { key: "powerAction", pattern: /\b(?:shutdown|reboot|poweroff|halt)\b/i },
  { key: "containerCleanup", pattern: /\bdocker\s+system\s+prune\b/i },
  { key: "clusterDelete", pattern: /\bkubectl\s+delete\b/i },
] as const;

function detectDangerousCommandKeys(command: string): string[] {
  return dangerousCommandPatterns
    .filter(({ pattern }) => pattern.test(command))
    .map(({ key }) => key);
}

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
  const [reviewing, setReviewing] = useState(false);
  const [acknowledgement, setAcknowledgement] = useState("");
  const withStepUp = useStepUpAction({ persist: false, reuseCached: false });

  useEffect(() => {
    if (open) {
      setSelectedNodeIds(defaultNodeIds?.length ? defaultNodeIds : []);
      setCommand("");
      setName("");
      setRetain(false);
      setSaving(false);
      setError("");
      setReviewing(false);
      setAcknowledgement("");
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const selectedNodes = useMemo(
    () => selectedNodeIds.map((id) => nodes.find((node) => node.id === id)).filter((node): node is NodeRecord => Boolean(node)),
    [nodes, selectedNodeIds],
  );
  const selectedNodeSample = selectedNodes.slice(0, 3);
  const omittedNodeCount = Math.max(0, selectedNodes.length - selectedNodeSample.length);
  const dangerousCommandKeys = useMemo(() => detectDangerousCommandKeys(command.trim()), [command]);
  const requiresAcknowledgement = selectedNodeIds.length > 1;
  const expectedAcknowledgement = String(selectedNodeIds.length);

  const toggleNode = useCallback((nodeId: number) => {
    setSelectedNodeIds((prev) =>
      prev.includes(nodeId) ? prev.filter((id) => id !== nodeId) : [...prev, nodeId]
    );
  }, []);

  const selectAll = useCallback(() => {
    setSelectedNodeIds(nodes.map((n) => n.id));
  }, [nodes]);

  const validateInputs = useCallback(() => {
    if (selectedNodeIds.length === 0) {
      setError(t("batchCommand.errorNoNodes"));
      return false;
    }
    if (!command.trim()) {
      setError(t("batchCommand.errorEmptyCommand"));
      return false;
    }
    if (command.length > 4096) {
      setError(t("batchCommand.errorCommandTooLong"));
      return false;
    }
    setError("");
    return true;
  }, [selectedNodeIds.length, command, t]);

  const openReview = useCallback(() => {
    if (!validateInputs()) return;
    setAcknowledgement("");
    setReviewing(true);
  }, [validateInputs]);

  const executeCommand = useCallback(async () => {
    if (!validateInputs()) return;
    if (requiresAcknowledgement && acknowledgement.trim() !== expectedAcknowledgement) {
      setError(t("batchCommand.reviewAckMismatch", { count: selectedNodeIds.length }));
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
  }, [
    validateInputs,
    requiresAcknowledgement,
    acknowledgement,
    expectedAcknowledgement,
    selectedNodeIds,
    command,
    name,
    retain,
    token,
    onOpenChange,
    onSuccess,
    t,
    withStepUp,
  ]);

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("batchCommand.title")}
      description={t(reviewing ? "batchCommand.reviewDesc" : "batchCommand.desc")}
      icon={<Terminal className="size-5" aria-hidden />}
      size="lg"
      saving={saving}
      onSubmit={reviewing ? executeCommand : openReview}
      submitLabel={t(reviewing ? "batchCommand.reviewConfirm" : "batchCommand.submitLabel")}
      savingLabel={t("batchCommand.savingLabel")}
      extraFooter={reviewing ? (
        <Button type="button" variant="outline" disabled={saving} onClick={() => setReviewing(false)}>
          {t("batchCommand.reviewBack")}
        </Button>
      ) : null}
    >
      {error && (
        <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </div>
      )}

      {reviewing ? (
        <div className="space-y-4">
          <InlineAlert tone={dangerousCommandKeys.length > 0 ? "critical" : "warning"} title={t("batchCommand.reviewTitle")}>
            {dangerousCommandKeys.length > 0
              ? t("batchCommand.reviewDangerHint")
              : t("batchCommand.reviewImpactHint")}
          </InlineAlert>

          <div className="space-y-2 rounded-md border border-border bg-muted/30 p-3 text-sm">
            <div className="flex items-center justify-between gap-3">
              <span className="text-muted-foreground">{t("batchCommand.reviewNodeCount")}</span>
              <span className="font-medium">{selectedNodeIds.length}</span>
            </div>
            <div className="space-y-1">
              <span className="text-muted-foreground">{t("batchCommand.reviewNodeSample")}</span>
              <div className="flex flex-wrap gap-1.5">
                {selectedNodeSample.map((node) => (
                  <span key={node.id} className="rounded bg-background px-2 py-0.5 text-xs">
                    {node.name}
                  </span>
                ))}
                {omittedNodeCount > 0 ? (
                  <span className="rounded bg-background px-2 py-0.5 text-xs">
                    {t("batchCommand.reviewNodeMore", { count: omittedNodeCount })}
                  </span>
                ) : null}
              </div>
            </div>
            <div className="flex items-center justify-between gap-3">
              <span className="text-muted-foreground">{t("batchCommand.reviewRetain")}</span>
              <span className="font-medium">{retain ? t("common.enabled") : t("common.disabled")}</span>
            </div>
          </div>

          {dangerousCommandKeys.length > 0 ? (
            <div className="space-y-1 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
              <p className="font-medium">{t("batchCommand.reviewDangerPatterns")}</p>
              <ul className="list-disc space-y-1 pl-5">
                {dangerousCommandKeys.map((key) => (
                  <li key={key}>{t(`batchCommand.danger.${key}`)}</li>
                ))}
              </ul>
            </div>
          ) : null}

          {requiresAcknowledgement ? (
            <div>
              <label htmlFor="batch-command-ack" className="mb-1.5 block text-sm font-medium">
                {t("batchCommand.reviewAckLabel", { count: selectedNodeIds.length })}
              </label>
              <input
                id="batch-command-ack"
                type="text"
                inputMode="numeric"
                className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                value={acknowledgement}
                onChange={(event) => setAcknowledgement(event.target.value)}
                placeholder={expectedAcknowledgement}
                autoComplete="off"
              />
              <p className="mt-1 text-xs text-muted-foreground">
                {t("batchCommand.reviewAckHint")}
              </p>
            </div>
          ) : null}
        </div>
      ) : (
        <>
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
        </>
      )}
    </FormDialog>
  );
}
