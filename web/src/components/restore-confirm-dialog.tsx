import { useCallback, useEffect, useState } from "react";
import { RotateCcw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { FormDialog } from "@/components/ui/form-dialog";
import { Textarea } from "@/components/ui/textarea";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";

const TASK_RESTORE_GRANT_REASON_MAX_LENGTH = 240;
const TASK_RESTORE_GRANT_TTL_SECONDS = 600;
const RESTORE_ERROR_MAX_LENGTH = 500;

function boundedErrorMessage(err: unknown, fallback: string): string {
  const message = err instanceof Error ? err.message : fallback;
  return message.length > RESTORE_ERROR_MAX_LENGTH ? `${message.slice(0, RESTORE_ERROR_MAX_LENGTH)}…` : message;
}

type RestoreConfirmDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  taskId: number;
  taskName: string;
  rsyncSource?: string;
  rsyncTarget?: string;
  token: string;
  onSuccess?: (runId: number) => void;
};

export function RestoreConfirmDialog({
  open,
  onOpenChange,
  taskId,
  taskName,
  rsyncSource,
  rsyncTarget,
  token,
  onSuccess,
}: RestoreConfirmDialogProps) {
  const { t } = useTranslation();
  const [targetPath, setTargetPath] = useState(rsyncSource ?? "");
  const [grantReason, setGrantReason] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const { ensureStepUpProof } = useAuth();

  useEffect(() => {
    setGrantReason("");
    setError("");
    setSaving(false);
    if (open) {
      setTargetPath(rsyncSource ?? "");
    }
  }, [open, rsyncSource, taskId]);

  const handleSubmit = useCallback(async () => {
    const reason = grantReason.trim();
    if (!reason) {
      setError(t('restore.grantReasonRequired'));
      return;
    }
    if ([...reason].length > TASK_RESTORE_GRANT_REASON_MAX_LENGTH) {
      setError(t('restore.grantReasonTooLong', { max: TASK_RESTORE_GRANT_REASON_MAX_LENGTH }));
      return;
    }

    setSaving(true);
    setError("");
    try {
      const proof = await ensureStepUpProof(
        STEP_UP_ACTIONS.taskRestoreTrigger,
        { persist: false, reuseCached: false },
      );
      await apiClient.requestTaskRestoreCredentialGrant(
        token,
        { taskId, reason, requestedTtlSeconds: TASK_RESTORE_GRANT_TTL_SECONDS },
        proof,
      );
      const result = await apiClient.restoreTask(
        token,
        taskId,
        targetPath.trim() || undefined,
        proof,
      );
      onOpenChange(false);
      if (result.runId) onSuccess?.(result.runId);
    } catch (err) {
      setError(boundedErrorMessage(err, t('restore.failed')));
    } finally {
      setSaving(false);
    }
  }, [ensureStepUpProof, grantReason, onOpenChange, onSuccess, t, targetPath, taskId, token]);

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('restore.title')}
      description={t('restore.description', { taskName })}
      icon={<RotateCcw className="size-5" aria-hidden="true" />}
      size="md"
      saving={saving}
      onSubmit={handleSubmit}
      submitLabel={t('restore.submit')}
      savingLabel={t('restore.saving')}
    >
      {error && (
        <div role="alert" className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </div>
      )}

      <div className="space-y-3">
        <div>
          <span className="mb-1 block text-sm font-medium text-muted-foreground">
            {t('restore.sourcePathLabel')}
          </span>
          <div className="rounded-md bg-muted px-3 py-2 font-mono text-sm">
            {rsyncSource || "-"}
          </div>
        </div>

        <div>
          <span className="mb-1 block text-sm font-medium text-muted-foreground">
            {t('restore.backupTargetLabel')}
          </span>
          <div className="rounded-md bg-muted px-3 py-2 font-mono text-sm">
            {rsyncTarget || "-"}
          </div>
        </div>

        <div>
          <label htmlFor="restore-target-path" className="mb-1 block text-sm font-medium">
            {t('restore.restoreTargetLabel')}
          </label>
          <input
            id="restore-target-path"
            type="text"
            className="w-full rounded-md border border-border bg-background px-3 py-2 font-mono text-sm"
            placeholder={rsyncSource || t('restore.restoreTargetPlaceholder')}
            value={targetPath}
            onChange={(e) => setTargetPath(e.target.value)}
          />
          <p className="mt-1 text-xs text-muted-foreground">
            {t('restore.restoreTargetHint')}
          </p>
        </div>

        <div>
          <label htmlFor="restore-grant-reason" className="mb-1 block text-sm font-medium">
            {t('restore.grantReasonLabel')}
          </label>
          <Textarea
            id="restore-grant-reason"
            className="min-h-24"
            placeholder={t('restore.grantReasonPlaceholder')}
            value={grantReason}
            maxLength={TASK_RESTORE_GRANT_REASON_MAX_LENGTH + 1}
            onChange={(e) => setGrantReason(e.target.value)}
            aria-describedby="restore-grant-reason-hint"
            aria-invalid={error ? true : undefined}
          />
          <p id="restore-grant-reason-hint" className="mt-1 text-xs text-muted-foreground">
            {t('restore.grantReasonHint', { max: TASK_RESTORE_GRANT_REASON_MAX_LENGTH })}
          </p>
        </div>
      </div>
    </FormDialog>
  );
}
