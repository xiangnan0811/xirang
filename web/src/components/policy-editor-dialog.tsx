import { useState } from "react";
import { Clock3 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
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

type PolicyTemplate = {
  label: string;
  cron: string;
  hint: string;
};

const policyTemplates: PolicyTemplate[] = [
  { label: "每 2 小时", cron: "0 */2 * * *", hint: "两小时整点执行" },
  { label: "每天 02:30", cron: "30 2 * * *", hint: "每日凌晨执行" },
  { label: "每周日 03:00", cron: "0 3 * * 0", hint: "周级归档策略" },
];

function cronToNatural(cron: string) {
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return `按表达式 ${cron} 执行`;
  }
  const [minute, hour] = parts;
  if (minute.startsWith("*/")) {
    return `每隔 ${minute.replace("*/", "")} 分钟同步一次`;
  }
  if (hour.startsWith("*/")) {
    const hours = hour.replace("*/", "");
    return minute === "0"
      ? `每隔 ${hours} 小时整点同步一次`
      : `每隔 ${hours} 小时在 ${minute} 分执行`;
  }
  if (parts[4] !== "*") {
    return `每周 ${parts[4]} ${hour.padStart(2, "0")}:${minute.padStart(2, "0")} 执行`;
  }
  return `每天 ${hour.padStart(2, "0")}:${minute.padStart(2, "0")} 执行`;
}

function nextRunPreview(cron: string) {
  const parts = cron.trim().split(/\s+/);
  if (parts.length < 5) {
    return "无法预估下次执行时间";
  }
  const now = new Date();
  const minute = parts[0];
  const hour = parts[1];
  const weekday = parts[4];

  if (minute.startsWith("*/") && hour === "*") {
    const interval = Number(minute.replace("*/", ""));
    if (Number.isFinite(interval) && interval > 0) {
      const next = new Date(now.getTime() + interval * 60 * 1000);
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}`;
    }
  }
  if (hour.startsWith("*/")) {
    const interval = Number(hour.replace("*/", ""));
    const minuteValue = Number(minute);
    if (
      Number.isFinite(interval) &&
      interval > 0 &&
      Number.isFinite(minuteValue)
    ) {
      const next = new Date(now);
      next.setMinutes(minuteValue, 0, 0);
      while (next <= now) {
        next.setHours(next.getHours() + interval);
      }
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}`;
    }
  }
  const minuteValue = Number(minute);
  const hourValue = Number(hour);
  if (Number.isFinite(minuteValue) && Number.isFinite(hourValue)) {
    const next = new Date(now);
    next.setHours(hourValue, minuteValue, 0, 0);
    if (weekday === "*") {
      if (next <= now) {
        next.setDate(next.getDate() + 1);
      }
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}`;
    }
    const targetWeekday = Number(weekday);
    if (Number.isFinite(targetWeekday)) {
      const normalizedWeekday = ((targetWeekday % 7) + 7) % 7;
      let dayOffset = normalizedWeekday - next.getDay();
      if (dayOffset < 0 || (dayOffset === 0 && next <= now)) {
        dayOffset += 7;
      }
      next.setDate(next.getDate() + dayOffset);
      const weekdayMap: Record<string, string> = {
        "0": "周日",
        "1": "周一",
        "2": "周二",
        "3": "周三",
        "4": "周四",
        "5": "周五",
        "6": "周六",
        "7": "周日",
      };
      return `预计下次：${next.toLocaleString("zh-CN", { hour12: false })}（${weekdayMap[weekday] ?? `周${weekday}`}）`;
    }
  }
  return "无法预估下次执行时间";
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
        <Input
          id="policy-edit-cron"
          placeholder="例如：0 */2 * * *"
          value={draft.cron}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, cron: event.target.value }))
          }
        />
      </div>

      <div className="flex flex-wrap gap-2">
        {policyTemplates.map((template) => (
          <Button
            key={template.label}
            size="sm"
            variant={draft.cron === template.cron ? "default" : "outline"}
            onClick={() =>
              setDraft((prev) => ({ ...prev, cron: template.cron }))
            }
            title={template.hint}
          >
            {template.label}
          </Button>
        ))}
      </div>

      <div className="rounded-md border bg-muted/30 p-3 text-sm">
        <p className="text-xs text-muted-foreground">自然语言</p>
        <p className="mt-1">{cronToNatural(draft.cron)}</p>
        <p className="mt-1 text-xs text-info">
          {nextRunPreview(draft.cron)}
        </p>
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
