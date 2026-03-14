import { useState } from "react";
import { FileText } from "lucide-react";
import { createReportsApi, type NewReportConfigInput, type ReportConfig } from "@/lib/api/reports-api";
import { getErrorMessage } from "@/lib/utils";
import { AppSelect } from "@/components/ui/app-select";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toast";

const reportsApi = createReportsApi();

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (cfg: ReportConfig) => void;
};

const SCOPE_OPTIONS = [
  { value: "all", label: "全部节点" },
  { value: "tag", label: "按标签筛选" },
  { value: "node_ids", label: "指定节点 ID（JSON 数组）" },
];

const PERIOD_OPTIONS = [
  { value: "weekly", label: "每周" },
  { value: "monthly", label: "每月" },
];

type Draft = {
  name: string;
  scopeType: "all" | "tag" | "node_ids";
  scopeValue: string;
  period: "weekly" | "monthly";
  cron: string;
  integrationIds: string; // comma-separated IDs
  enabled: boolean;
};

const DEFAULT_DRAFT: Draft = {
  name: "",
  scopeType: "all",
  scopeValue: "",
  period: "weekly",
  cron: "0 8 * * 1",
  integrationIds: "",
  enabled: true,
};

function LabelRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <label className="text-sm font-medium text-foreground">{label}</label>
      {children}
    </div>
  );
}

export function ReportConfigDialog({ open, onOpenChange, onCreated }: Props) {
  const [draft, setDraft] = useState<Draft>(DEFAULT_DRAFT);
  const [saving, setSaving] = useState(false);

  const set = (patch: Partial<Draft>) => setDraft((prev) => ({ ...prev, ...patch }));

  const handleSubmit = async () => {
    if (!draft.name.trim()) {
      toast.error("配置名称不能为空");
      return;
    }
    if (!draft.cron.trim()) {
      toast.error("Cron 表达式不能为空");
      return;
    }

    const integrationIds = draft.integrationIds
      .split(",")
      .map((s) => Number(s.trim()))
      .filter((n) => Number.isFinite(n) && n > 0);

    const input: NewReportConfigInput = {
      name: draft.name.trim(),
      scope_type: draft.scopeType,
      scope_value: draft.scopeValue.trim(),
      period: draft.period,
      cron: draft.cron.trim(),
      integration_ids: integrationIds,
      enabled: draft.enabled,
    };

    setSaving(true);
    try {
      const cfg = await reportsApi.createConfig(input);
      toast.success("报告配置已创建");
      onCreated(cfg);
      onOpenChange(false);
      setDraft(DEFAULT_DRAFT);
    } catch (err) {
      toast.error("创建失败: " + getErrorMessage(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="新增报告配置"
      icon={<FileText className="size-5" />}
      saving={saving}
      onSubmit={() => void handleSubmit()}
      submitLabel="创建"
    >
      <div className="space-y-4">
        <LabelRow label="配置名称 *">
          <Input
            value={draft.name}
            onChange={(e) => set({ name: e.target.value })}
            placeholder="如：每周备份健康报告"
          />
        </LabelRow>

        <div className="grid grid-cols-2 gap-3">
          <LabelRow label="作用范围">
            <AppSelect
              value={draft.scopeType}
              onChange={(e) => set({ scopeType: e.target.value as Draft["scopeType"] })}
            >
              {SCOPE_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
            </AppSelect>
          </LabelRow>
          <LabelRow label="周期">
            <AppSelect
              value={draft.period}
              onChange={(e) => set({ period: e.target.value as Draft["period"] })}
            >
              {PERIOD_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
            </AppSelect>
          </LabelRow>
        </div>

        {draft.scopeType !== "all" && (
          <LabelRow label={draft.scopeType === "tag" ? "标签名" : "节点 ID（JSON 数组）"}>
            <Input
              value={draft.scopeValue}
              onChange={(e) => set({ scopeValue: e.target.value })}
              placeholder={draft.scopeType === "tag" ? "如：production" : "如：[1,2,3]"}
            />
          </LabelRow>
        )}

        <LabelRow label="Cron 表达式 *">
          <Input
            value={draft.cron}
            onChange={(e) => set({ cron: e.target.value })}
            placeholder="如：0 8 * * 1（每周一 8:00）"
            className="font-mono"
          />
        </LabelRow>

        <LabelRow label="通知渠道 ID（逗号分隔，可留空）">
          <Input
            value={draft.integrationIds}
            onChange={(e) => set({ integrationIds: e.target.value })}
            placeholder="如：1,2"
          />
        </LabelRow>

        <div className="flex items-center gap-2">
          <Switch
            checked={draft.enabled}
            onCheckedChange={(v) => set({ enabled: v })}
            id="report-enabled"
          />
          <label htmlFor="report-enabled" className="text-sm">启用配置</label>
        </div>
      </div>
    </FormDialog>
  );
}
