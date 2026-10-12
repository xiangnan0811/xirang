import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { FormDialog } from "@/components/ui/form-dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import type { BatchRetryFailureKind } from "@/lib/api/batch-api";

export type BatchRetryRowSnapshot = {
  taskId: number;
  runId: number;
  nodeId: number;
  nodeName: string;
  failureKind: BatchRetryFailureKind;
};

type BatchCommandRetryDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  rows: readonly BatchRetryRowSnapshot[];
  sourceBatchId: string;
  retain: boolean;
  saving: boolean;
  error: string | null;
  onConfirm: () => void | Promise<void>;
};

export function BatchCommandRetryDialog({
  open,
  onOpenChange,
  rows,
  sourceBatchId,
  retain,
  saving,
  error,
  onConfirm,
}: BatchCommandRetryDialogProps) {
  const { t } = useTranslation();
  const acknowledgementId = useId();
  const acknowledgementErrorId = useId();
  const [acknowledgement, setAcknowledgement] = useState("");
  const [acknowledgementError, setAcknowledgementError] = useState("");
  const requiresAcknowledgement = rows.length > 1;
  const expectedAcknowledgement = String(rows.length);
  const rowSignature = rows.map((row) => `${row.taskId}:${row.runId}:${row.nodeId}:${row.failureKind}`).join("|");
  const acknowledgementKey = `${open}:${rowSignature}`;
  const [seenAcknowledgementKey, setSeenAcknowledgementKey] = useState(acknowledgementKey);
  if (seenAcknowledgementKey !== acknowledgementKey) {
    setSeenAcknowledgementKey(acknowledgementKey);
    if (open) {
      setAcknowledgement("");
      setAcknowledgementError("");
    }
  }

  function handleOpenChange(next: boolean) {
    if (!next && saving) return;
    onOpenChange(next);
  }

  function handleSubmit() {
    if (saving) return;
    if (requiresAcknowledgement && acknowledgement.trim() !== expectedAcknowledgement) {
      setAcknowledgementError(t("batch.retryAckMismatch", { count: rows.length }));
      return;
    }
    setAcknowledgementError("");
    void onConfirm();
  }

  return (
    <FormDialog
      open={open}
      onOpenChange={handleOpenChange}
      title={t("batch.retryConfirmTitle")}
      description={t("batch.retryConfirmDescription")}
      size="lg"
      saving={saving}
      onSubmit={handleSubmit}
      submitLabel={t("batch.retryConfirm")}
      savingLabel={t("batch.retrySavingLabel")}
    >
      <InlineAlert tone="warning" title={t("batch.retryConfirmTitle")}>
        {t("batch.retryPartialEffects")}
      </InlineAlert>
      {saving ? (
        <InlineAlert tone="warning">{t("batch.retrySubmitting")}</InlineAlert>
      ) : null}
      {error ? (
        <p role="alert" className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive break-words">
          {error}
        </p>
      ) : null}
      {acknowledgementError ? (
        <p id={acknowledgementErrorId} role="alert" className="text-sm text-destructive break-words">
          {acknowledgementError}
        </p>
      ) : null}

      <div className="space-y-2 rounded-md border border-border bg-muted/30 p-3 text-sm">
        <p className="break-all">
          <span className="text-muted-foreground">{t("batch.retrySourceBatchLabel")}</span>
          {" "}
          <span className="font-medium">{sourceBatchId}</span>
        </p>
        <p>
          <span className="text-muted-foreground">{t("batch.retryRetainLabel")}</span>
          {" "}
          <span className="font-medium">{retain ? t("common.enabled") : t("common.disabled")}</span>
        </p>
        <p>
          <span className="text-muted-foreground">{t("batch.retryNodeList")}</span>
          {" "}
          <span className="font-medium">{rows.length}</span>
        </p>
        <ul className="max-h-48 space-y-1.5 overflow-y-auto">
          {rows.map((row) => (
            <li key={row.taskId} className="break-words rounded bg-background px-2 py-1">
              {t("batch.retrySnapshot", {
                name: row.nodeName,
                nodeId: row.nodeId,
                taskId: row.taskId,
                runId: row.runId,
                failure: t(row.failureKind === "dispatch" ? "batch.retryFailureDispatch" : "batch.retryFailureExecution"),
              })}
            </li>
          ))}
        </ul>
      </div>

      {requiresAcknowledgement ? (
        <div>
          <label htmlFor={acknowledgementId} className="mb-1.5 block text-sm font-medium">
            {t("batch.retryAckLabel", { count: rows.length })}
          </label>
          <input
            id={acknowledgementId}
            type="text"
            inputMode="numeric"
            className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
            value={acknowledgement}
            onChange={(event) => setAcknowledgement(event.target.value)}
            placeholder={expectedAcknowledgement}
            autoComplete="off"
            disabled={saving}
            aria-invalid={acknowledgementError ? true : undefined}
            aria-describedby={acknowledgementError ? acknowledgementErrorId : undefined}
          />
          <p className="mt-1 text-xs text-muted-foreground break-words">{t("batch.retryAckHint")}</p>
        </div>
      ) : null}
    </FormDialog>
  );
}
