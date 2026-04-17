import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { FileText, Loader2, Pencil } from "lucide-react";
import {
  createReportsApi,
  type NewReportConfigInput,
  type ReportConfig,
} from "@/lib/api/reports-api";
import { createIntegrationsApi } from "@/lib/api/integrations-api";
import { integrationIcon } from "@/pages/notifications-page.utils";
import { getErrorMessage } from "@/lib/utils";
import type { IntegrationChannel } from "@/types/domain";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { Select } from "@/components/ui/select";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toast";
import { CronGenerator } from "@/components/cron-generator";

const reportsApi = createReportsApi();
const integrationsApi = createIntegrationsApi();

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSaved: (cfg: ReportConfig) => void;
  token: string;
  editingConfig?: ReportConfig | null;
};

const SCOPE_VALUES = ["all", "tag", "node_ids"] as const;
const PERIOD_VALUES = ["weekly", "monthly"] as const;

type Draft = {
  name: string;
  scopeType: "all" | "tag" | "node_ids";
  scopeValue: string;
  period: "weekly" | "monthly";
  cron: string;
  selectedChannelIds: number[];
  enabled: boolean;
};

const DEFAULT_DRAFT: Draft = {
  name: "",
  scopeType: "all",
  scopeValue: "",
  period: "weekly",
  cron: "0 8 * * 1",
  selectedChannelIds: [],
  enabled: true,
};

/** Module-level for referential stability with useDialogDraft */
function toDraft(cfg: ReportConfig): Draft {
  let channelIds: number[] = [];
  try {
    const parsed = JSON.parse(cfg.integration_ids);
    if (Array.isArray(parsed)) channelIds = parsed;
  } catch {
    /* ignore */
  }
  return {
    name: cfg.name,
    scopeType: cfg.scope_type,
    scopeValue: cfg.scope_value,
    period: cfg.period,
    cron: cfg.cron,
    selectedChannelIds: channelIds,
    enabled: cfg.enabled,
  };
}

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

/** Extract numeric id from "int-{n}" format */
function numericId(ch: IntegrationChannel): number {
  return parseInt(ch.id.replace("int-", ""), 10);
}

export function ReportConfigDialog({
  open,
  onOpenChange,
  onSaved,
  token,
  editingConfig,
}: Props) {
  const { t } = useTranslation();
  const [draft, setDraft] = useDialogDraft(open, DEFAULT_DRAFT, editingConfig, toDraft);
  const [saving, setSaving] = useState(false);
  const isEditing = Boolean(editingConfig);

  // Integration channels state
  const [channels, setChannels] = useState<IntegrationChannel[]>([]);
  const [channelsLoading, setChannelsLoading] = useState(false);
  const [channelsError, setChannelsError] = useState(false);

  useEffect(() => {
    if (open) {
      setChannelsLoading(true);
      setChannelsError(false);
      integrationsApi
        .getIntegrations(token)
        .then((all) => setChannels(all.filter((ch) => ch.enabled)))
        .catch((err) => {
          setChannelsError(true);
          toast.error(t("reportConfig.channelsLoadError") + ": " + getErrorMessage(err));
        })
        .finally(() => setChannelsLoading(false));
    }
  }, [open, token, t]);

  const set = (patch: Partial<Draft>) =>
    setDraft((prev) => ({ ...prev, ...patch }));

  const toggleChannel = (id: number) => {
    setDraft((prev) => ({
      ...prev,
      selectedChannelIds: prev.selectedChannelIds.includes(id)
        ? prev.selectedChannelIds.filter((x) => x !== id)
        : [...prev.selectedChannelIds, id],
    }));
  };

  const handleSubmit = async () => {
    if (!draft.name.trim()) {
      toast.error(t("reportConfig.errorNameRequired"));
      return;
    }
    if (!draft.cron.trim()) {
      toast.error(t("reportConfig.errorCronRequired"));
      return;
    }

    const input: NewReportConfigInput = {
      name: draft.name.trim(),
      scope_type: draft.scopeType,
      scope_value: draft.scopeValue.trim(),
      period: draft.period,
      cron: draft.cron.trim(),
      integration_ids: draft.selectedChannelIds,
      enabled: draft.enabled,
    };

    setSaving(true);
    try {
      const cfg = isEditing
        ? await reportsApi.updateConfig(token, editingConfig!.id, input)
        : await reportsApi.createConfig(token, input);
      toast.success(t(isEditing ? "reportConfig.updateSuccess" : "reportConfig.createSuccess"));
      onSaved(cfg);
      onOpenChange(false);
    } catch (err) {
      toast.error(
        t(isEditing ? "reportConfig.updateFailed" : "reportConfig.createFailed") +
          ": " +
          getErrorMessage(err),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t(isEditing ? "reportConfig.titleEdit" : "reportConfig.title")}
      icon={isEditing ? <Pencil className="size-5" /> : <FileText className="size-5" />}
      size="lg"
      saving={saving}
      onSubmit={() => void handleSubmit()}
      submitLabel={t(isEditing ? "reportConfig.submitEdit" : "reportConfig.submitLabel")}
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
            <Select
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
            </Select>
          </LabelRow>
          <LabelRow label={t("reportConfig.period")}>
            <Select
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
            </Select>
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
          <CronGenerator
            value={draft.cron}
            onChange={(val) => set({ cron: val })}
            disabled={saving}
          />
        </LabelRow>

        <LabelRow label={t("reportConfig.channelIds")}>
          <div className="rounded-md border bg-muted/30 p-3 max-h-[200px] overflow-y-auto thin-scrollbar">
            {channelsLoading && (
              <div className="flex items-center gap-2 text-sm text-muted-foreground py-1">
                <Loader2 className="size-4 animate-spin" />
                {t("reportConfig.channelsLoading")}
              </div>
            )}
            {channelsError && !channelsLoading && (
              <p className="text-sm text-destructive py-1">
                {t("reportConfig.channelsLoadError")}
              </p>
            )}
            {!channelsLoading && !channelsError && channels.length === 0 && (
              <p className="text-sm text-muted-foreground py-1">
                {t("reportConfig.channelsEmpty")}
              </p>
            )}
            {!channelsLoading && !channelsError && channels.length > 0 && (
              <div className="space-y-1">
                {channels.map((ch) => {
                  const nid = numericId(ch);
                  const checked = draft.selectedChannelIds.includes(nid);
                  const Icon = integrationIcon(ch.type);
                  return (
                    <label
                      key={ch.id}
                      className={`flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm cursor-pointer transition-colors ${
                        checked
                          ? "bg-primary/10 text-foreground"
                          : "text-muted-foreground hover:bg-muted/60 hover:text-foreground"
                      }`}
                    >
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() => toggleChannel(nid)}
                        className="accent-primary size-3.5 shrink-0"
                      />
                      <Icon className="size-4 shrink-0" />
                      <span className="truncate">{ch.name}</span>
                      <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                        {ch.type}
                      </span>
                    </label>
                  );
                })}
              </div>
            )}
          </div>
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
