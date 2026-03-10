import { useState } from "react";
import { Clock3 } from "lucide-react";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { CronGenerator } from "@/components/cron-generator";
import type { NewPolicyInput, PolicyRecord } from "@/types/domain";

type PolicyDraft = NewPolicyInput & {
  id?: number;
};

const emptyDraft: PolicyDraft = {
  name: "",
  sourcePath: "",
  targetPath: "",
  cron: "0 */2 * * *",
  criticalThreshold: 2,
  enabled: true,
};

function toBoundedInt(value: string, fallback: number, min: number, max: number): number {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isFinite(parsed)) {
    return fallback;
  }
  return Math.min(max, Math.max(min, parsed));
}

function toDraft(policy: PolicyRecord): PolicyDraft {
  return {
    id: policy.id,
    name: policy.name,
    sourcePath: policy.sourcePath,
    targetPath: policy.targetPath,
    cron: policy.cron,
    criticalThreshold: policy.criticalThreshold,
    enabled: policy.enabled,
  };
}

type PolicyEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  editingPolicy?: PolicyRecord | null;
  onSave: (draft: PolicyDraft) => Promise<void>;
};

export function PolicyEditorDialog({
  open,
  onOpenChange,
  editingPolicy,
  onSave,
}: PolicyEditorDialogProps) {
  const [draft, setDraft] = useDialogDraft<PolicyDraft, PolicyRecord>(open, emptyDraft, editingPolicy, toDraft);
  const [saving, setSaving] = useState(false);

  const isEditing = Boolean(draft.id);

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave(draft);
    } finally {
      setSaving(false);
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      icon={<Clock3 className="size-5 text-primary" />}
      title={isEditing ? `编辑策略 - ${draft.name}` : "新增策略"}
      description={isEditing
        ? `修改策略 ${draft.name} 的调度规则和路径配置。`
        : "创建新的备份策略，配置 Cron 调度和同步路径。"}
      saving={saving}
      onSubmit={handleSave}
      submitLabel={isEditing ? "更新策略" : "保存策略"}
    >
      <div>
        <label htmlFor="policy-edit-name" className="mb-1 block text-sm font-medium">策略名称</label>
        <Input id="policy-edit-name" placeholder="例如：每日全量备份"
          value={draft.name}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, name: event.target.value }))
          }
        />
      </div>

      <div>
        <label htmlFor="policy-edit-cron" className="mb-1 block text-sm font-medium">
          Cron 表达式
        </label>
        <CronGenerator
          id="policy-edit-cron"
          value={draft.cron}
          onChange={(val) => setDraft((prev) => ({ ...prev, cron: val }))}
          disabled={saving}
        />
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="policy-edit-source" className="mb-1 block text-sm font-medium">源路径</label>
          <Input id="policy-edit-source" placeholder="/data/source"
            value={draft.sourcePath}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                sourcePath: event.target.value,
              }))
            }
          />
        </div>
        <div>
          <label htmlFor="policy-edit-target" className="mb-1 block text-sm font-medium">目标路径</label>
          <Input id="policy-edit-target" placeholder="/backup/target"
            value={draft.targetPath}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                targetPath: event.target.value,
              }))
            }
          />
        </div>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="policy-edit-threshold" className="mb-1 block text-sm font-medium">
            失败阈值（连续失败次数触发告警）
          </label>
          <Input
            id="policy-edit-threshold"
            type="number"
            min={1}
            max={10}
            value={draft.criticalThreshold}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                criticalThreshold: toBoundedInt(event.target.value, 2, 1, 10),
              }))
            }
          />
        </div>
        <div>
          <div id="policy-status-label" className="mb-1 text-sm font-medium">策略状态</div>
          <div className="glass-panel flex h-10 items-center gap-2 px-3 text-sm">
            <Switch
              aria-labelledby="policy-status-label"
              checked={draft.enabled}
              onCheckedChange={(checked) =>
                setDraft((prev) => ({ ...prev, enabled: checked }))
              }
            />
            <span className="text-muted-foreground">{draft.enabled ? "启用" : "停用"}</span>
          </div>
        </div>
      </div>
    </FormDialog>
  );
}

export type { PolicyDraft };
