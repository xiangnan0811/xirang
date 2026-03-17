import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { FileText } from "lucide-react";
import {
  createReportsApi,
  type NewReportConfigInput,
  type ReportConfig,
} from "@/lib/api/reports-api";
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
  token: string;
};

// Labels are rendered via t() inside the component; these are placeholder values only
const SCOPE_VALUES = ["all", "tag", "node_ids"] as const;
const PERIOD_VALUES = ["weekly", "monthly"] as const;

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

function LabelRow({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <label className="text-sm font-medium text-foreground">{label}</label>
      {children}
    </div>
  );
}

export function ReportConfigDialog({
  open,
  onOpenChange,
  onCreated,
  token,
}: Props) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<Draft>(DEFAULT_DRAFT);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (open) setDraft(DEFAULT_DRAFT);
  }, [open]);

  const set = (patch: Partial<Draft>) =>
    setDraft((prev) => ({ ...prev, ...patch }));

  const handleSubmit = async () => {
    if (!draft.name.trim()) {
      toast.error(t("reportConfig.errorNameRequired"));
      return;
    }
    if (!draft.cron.trim()) {
      toast.error(t("reportConfig.errorCronRequired"));
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
      const cfg = await reportsApi.createConfig(token, input);
      toast.success(t("reportConfig.createSuccess"));
      onCreated(cfg);
      onOpenChange(false);
      setDraft(DEFAULT_DRAFT);
    } catch (err) {
      toast.error(
        t("reportConfig.createFailed") + ": " + getErrorMessage(err),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("reportConfig.title")}
      icon={<FileText className="size-5" />}
      saving={saving}
      onSubmit={() => void handleSubmit()}
      submitLabel={t("reportConfig.submitLabel")}
    >
      <div className="space-y-4">
        <LabelRow label={t("reportConfig.configName")}>
          <Input
            value={draft.name}
            onChange={(e) => set({ name: e.target.value })}
            placeholder={t("reportConfig.configNamePlaceholder")}
          />
        </LabelRow>

        <div className="grid grid-cols-2 gap-3">
          <LabelRow label={t("reportConfig.scope")}>
            <AppSelect
              value={draft.scopeType}
              onChange={(e) =>
                set({ scopeType: e.target.value as Draft["scopeType"] })
              }
            >
              {SCOPE_VALUES.map((v) => (
                <option key={v} value={v}>
                  {t(`reports.scopeLabels.${v}`)}
                </option>
              ))}
            </AppSelect>
          </LabelRow>
          <LabelRow label={t("reportConfig.period")}>
            <AppSelect
              value={draft.period}
              onChange={(e) =>
                set({ period: e.target.value as Draft["period"] })
              }
            >
              {PERIOD_VALUES.map((v) => (
                <option key={v} value={v}>
                  {t(`reports.periodLabels.${v}`)}
                </option>
              ))}
            </AppSelect>
          </LabelRow>
        </div>

        {draft.scopeType !== "all" && (
          <LabelRow
            label={
              draft.scopeType === "tag"
                ? t("reportConfig.tagName")
                : t("reportConfig.nodeIds")
            }
          >
            <Input
              value={draft.scopeValue}
              onChange={(e) => set({ scopeValue: e.target.value })}
              placeholder={
                draft.scopeType === "tag"
                  ? t("reportConfig.tagPlaceholder")
                  : t("reportConfig.nodeIdsPlaceholder")
              }
            />
          </LabelRow>
        )}

        <LabelRow label={t("reportConfig.cronLabel")}>
          <Input
            value={draft.cron}
            onChange={(e) => set({ cron: e.target.value })}
            placeholder={t("reportConfig.cronPlaceholder")}
            className="font-mono"
          />
        </LabelRow>

        <LabelRow label={t("reportConfig.channelIds")}>
          <Input
            value={draft.integrationIds}
            onChange={(e) => set({ integrationIds: e.target.value })}
            placeholder={t("reportConfig.channelIdsPlaceholder")}
          />
        </LabelRow>

        <div className="flex items-center gap-2">
          <Switch
            checked={draft.enabled}
            onCheckedChange={(v) => set({ enabled: v })}
            id="report-enabled"
          />
          <label htmlFor="report-enabled" className="text-sm">
            {t("reportConfig.enabledConfig")}
          </label>
        </div>
      </div>
    </FormDialog>
  );
}
