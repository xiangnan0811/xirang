import { useCallback, useState } from "react";
import { RotateCcw } from "lucide-react";
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
      setError(err instanceof Error ? err.message : "恢复失败");
    } finally {
      setSaving(false);
    }
  }, [token, taskId, targetPath, onOpenChange, onSuccess]);

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="从备份恢复"
      description={`恢复任务: ${taskName}`}
      icon={<RotateCcw className="size-5" />}
      size="md"
      saving={saving}
      onSubmit={handleSubmit}
      submitLabel="确认恢复"
      savingLabel="恢复中..."
    >
      {error && (
        <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </div>
      )}

      <div className="space-y-3">
        <div>
          <label className="mb-1 block text-sm font-medium text-muted-foreground">
            原始源路径（数据来源）
          </label>
          <div className="rounded-md bg-muted px-3 py-2 font-mono text-sm">
            {rsyncSource || "-"}
          </div>
        </div>

        <div>
          <label className="mb-1 block text-sm font-medium text-muted-foreground">
            备份目标路径（备份存储位置）
          </label>
          <div className="rounded-md bg-muted px-3 py-2 font-mono text-sm">
            {rsyncTarget || "-"}
          </div>
        </div>

        <div>
          <label className="mb-1 block text-sm font-medium">
            恢复目标路径
          </label>
          <input
            type="text"
            className="w-full rounded-md border border-border bg-background px-3 py-2 font-mono text-sm"
            placeholder={rsyncSource || "输入恢复目标路径"}
            value={targetPath}
            onChange={(e) => setTargetPath(e.target.value)}
          />
          <p className="mt-1 text-xs text-muted-foreground">
            留空则恢复到原始源路径。路径必须为绝对路径，禁止系统目录。
          </p>
        </div>
      </div>
    </FormDialog>
  );
}
