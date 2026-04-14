import { useCallback, useState } from "react";
import { RotateCcw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { FormDialog } from "@/components/ui/form-dialog";
import { apiClient } from "@/lib/api/client";

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
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const handleSubmit = useCallback(async () => {
    setSaving(true);
    setError("");
    try {
      const result = await apiClient.restoreTask(
        token,
        taskId,
        targetPath.trim() || undefined
      );
      onOpenChange(false);
      if (result.runId) onSuccess?.(result.runId);
    } catch (err) {
      setError(err instanceof Error ? err.message : t('restore.failed'));
    } finally {
      setSaving(false);
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- t is stable from react-i18next
  }, [token, taskId, targetPath, onOpenChange, onSuccess]);

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('restore.title')}
      description={t('restore.description', { taskName })}
      icon={<RotateCcw className="size-5" />}
      size="md"
      saving={saving}
      onSubmit={handleSubmit}
      submitLabel={t('restore.submit')}
      savingLabel={t('restore.saving')}
    >
      {error && (
        <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </div>
      )}

      <div className="space-y-3">
        <div>
          <label className="mb-1 block text-sm font-medium text-muted-foreground">
            {t('restore.sourcePathLabel')}
          </label>
          <div className="rounded-md bg-muted px-3 py-2 font-mono text-sm">
            {rsyncSource || "-"}
          </div>
        </div>

        <div>
          <label className="mb-1 block text-sm font-medium text-muted-foreground">
            {t('restore.backupTargetLabel')}
          </label>
          <div className="rounded-md bg-muted px-3 py-2 font-mono text-sm">
            {rsyncTarget || "-"}
          </div>
        </div>

        <div>
          <label className="mb-1 block text-sm font-medium">
            {t('restore.restoreTargetLabel')}
          </label>
          <input
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
      </div>
    </FormDialog>
  );
}
