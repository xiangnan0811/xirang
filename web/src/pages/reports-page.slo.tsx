import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, Pencil, Target, Trash2 } from "lucide-react";
import { useAuth } from "@/context/auth-context";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { TagChips } from "@/components/ui/tag-chips";
import { FormDialog } from "@/components/ui/form-dialog";
import { toast } from "@/components/ui/toast";
import {
  listSLOs,
  createSLO,
  updateSLO,
  deleteSLO,
  getSLOCompliance,
  getSLOSummary,
  parseSLOTags,
  type SLOInput,
} from "@/lib/api/slo";
import { listEscalationPolicies } from "@/lib/api/escalation";
import type { EscalationPolicy, SLODefinition, SLOComplianceResult, SLOSummary } from "@/types/domain";

const METRIC_TYPES = [
  { value: "availability", i18nKey: "slo.metricType.availability" },
  { value: "success_rate", i18nKey: "slo.metricType.successRate" },
] as const;

export function SLOPanel() {
  const { t } = useTranslation();
  const { token, role } = useAuth();
  const isAdmin = role === "admin";
  const [rows, setRows] = useState<SLODefinition[]>([]);
  const [compliance, setCompliance] = useState<Record<number, SLOComplianceResult>>({});
  const [summary, setSummary] = useState<SLOSummary | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<SLODefinition | null>(null);

  const refresh = async () => {
    if (!token) return;
    const [list, sum] = await Promise.all([listSLOs(token), getSLOSummary(token)]);
    setRows(list);
    setSummary(sum);
    const comps: Record<number, SLOComplianceResult> = {};
    await Promise.all(
      list.filter((r) => r.enabled).map(async (r) => {
        try {
          comps[r.id] = await getSLOCompliance(token, r.id);
        } catch {
          /* ignore per-row errors */
        }
      })
    );
    setCompliance(comps);
  };

  useEffect(() => {
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token]);

  const handleDelete = async (row: SLODefinition) => {
    if (!token) return;
    if (!window.confirm(t("slo.deleteConfirm", { name: row.name }))) return;
    try {
      await deleteSLO(token, row.id);
      toast.success(t("common.success"));
      await refresh();
    } catch (err) {
      toast.error((err as Error).message);
    }
  };

  return (
    <>
      {/* Header row: title + subtitle + actions */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">{t("slo.tabSLO")}</h1>
          {summary && (
            <p className="mt-0.5 text-sm text-muted-foreground">
              {t("slo.summary.total")}: {summary.total} · {t("slo.summary.healthy")}: {summary.healthy} · {t("slo.summary.warning")}: {summary.warning} · {t("slo.summary.breached")}: {summary.breached}
            </p>
          )}
        </div>
        {isAdmin && (
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <Plus className="mr-1.5 size-4" />
            {t("slo.new")}
          </Button>
        )}
      </div>

      {/* Empty state or table */}
      {rows.length === 0 ? (
        <EmptyState
          icon={Target}
          title={t("slo.panelEmpty")}
        />
      ) : (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border/40 text-left text-xs uppercase text-muted-foreground">
                  <th className="px-4 py-2 font-medium">{t("slo.columns.name")}</th>
                  <th className="px-4 py-2 font-medium">{t("slo.columns.type")}</th>
                  <th className="px-4 py-2 font-medium">{t("slo.columns.threshold")}</th>
                  <th className="px-4 py-2 font-medium">{t("slo.columns.observed")}</th>
                  <th className="px-4 py-2 font-medium">{t("slo.columns.budget")}</th>
                  <th className="px-4 py-2 font-medium">{t("slo.columns.status")}</th>
                  {isAdmin && <th className="px-4 py-2 font-medium">{t("slo.columns.actions")}</th>}
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => {
                  const c = compliance[row.id];
                  return (
                    <tr key={row.id} className="border-b border-border/20 last:border-0">
                      <td className="px-4 py-2">{row.name}</td>
                      <td className="px-4 py-2 text-muted-foreground">
                        {t(METRIC_TYPES.find((m) => m.value === row.metric_type)?.i18nKey ?? "")}
                      </td>
                      <td className="px-4 py-2 tabular-nums">{(row.threshold * 100).toFixed(2)}%</td>
                      <td className="px-4 py-2 tabular-nums">
                        {c ? `${(c.observed * 100).toFixed(2)}%` : "—"}
                      </td>
                      <td className="px-4 py-2 tabular-nums">
                        {c ? `${c.error_budget_remaining_pct.toFixed(0)}%` : "—"}
                      </td>
                      <td className="px-4 py-2">
                        <StatusBadge status={c?.status ?? "insufficient_data"} />
                      </td>
                      {isAdmin && (
                        <td className="px-4 py-2">
                          <div className="flex items-center gap-1">
                            <Button
                              variant="ghost"
                              size="icon"
                              className="size-8 text-muted-foreground hover:text-foreground"
                              title={t("common.edit")}
                              aria-label={t("common.edit")}
                              onClick={() => setEditing(row)}
                            >
                              <Pencil className="size-4" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="size-8 text-destructive/80 hover:text-destructive"
                              title={t("slo.delete")}
                              aria-label={t("slo.delete")}
                              onClick={() => void handleDelete(row)}
                            >
                              <Trash2 className="size-4" />
                            </Button>
                          </div>
                        </td>
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {/* Dialogs */}
      <SLODialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onSubmitted={() => {
          setCreateOpen(false);
          void refresh();
        }}
      />
      <SLODialog
        open={editing !== null}
        onOpenChange={(open) => {
          if (!open) setEditing(null);
        }}
        existing={editing}
        onSubmitted={() => {
          setEditing(null);
          void refresh();
        }}
      />
    </>
  );
}

function StatusBadge({ status }: { status: string }) {
  const { t } = useTranslation();
  const label = t(
    `slo.status.${status === "insufficient_data" ? "insufficient" : status}`
  );
  const tone =
    status === "healthy"
      ? "success"
      : status === "warning"
        ? "warning"
        : status === "breached"
          ? "destructive"
          : "neutral";
  return <Badge tone={tone as "success" | "warning" | "destructive" | "neutral"}>{label}</Badge>;
}

function SLODialog({
  open,
  onOpenChange,
  existing,
  onSubmitted,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  existing?: SLODefinition | null;
  onSubmitted: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [name, setName] = useState("");
  const [metricType, setMetricType] = useState<"availability" | "success_rate">("availability");
  const [tags, setTags] = useState<string[]>([]);
  const [threshold, setThreshold] = useState("99");
  const [windowDays, setWindowDays] = useState(28);
  const [enabled, setEnabled] = useState(true);
  const [escalationPolicyId, setEscalationPolicyId] = useState<number | null>(null);
  const [escalationPolicies, setEscalationPolicies] = useState<EscalationPolicy[]>([]);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open || !token) return;
    listEscalationPolicies(token)
      .then((list) => setEscalationPolicies(list.filter((p) => p.enabled)))
      .catch(() => {});
  }, [open, token]);

  useEffect(() => {
    if (existing) {
      setName(existing.name);
      setMetricType(existing.metric_type);
      setTags(parseSLOTags(existing));
      setThreshold((existing.threshold * 100).toString());
      setWindowDays(existing.window_days);
      setEnabled(existing.enabled);
      setEscalationPolicyId(existing.escalation_policy_id ?? null);
    } else {
      setName("");
      setMetricType("availability");
      setTags([]);
      setThreshold("99");
      setWindowDays(28);
      setEnabled(true);
      setEscalationPolicyId(null);
    }
  }, [existing, open]);

  const handleSubmit = async () => {
    if (!token) return;
    const n = parseFloat(threshold);
    if (!isFinite(n) || n <= 0 || n >= 100) {
      toast.error(t("slo.validationThreshold"));
      return;
    }
    const input: SLOInput = {
      name,
      metric_type: metricType,
      match_tags: tags,
      threshold: n / 100,
      window_days: windowDays,
      enabled,
      escalation_policy_id: escalationPolicyId,
    };
    setSaving(true);
    try {
      if (existing) {
        await updateSLO(token, existing.id, input);
      } else {
        await createSLO(token, input);
      }
      toast.success(t("common.success"));
      onSubmitted();
    } catch (err) {
      toast.error((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={existing ? t("slo.edit") : t("slo.new")}
      size="md"
      saving={saving}
      onSubmit={handleSubmit}
      submitLabel={existing ? t("slo.dialog.save") : t("slo.dialog.create")}
      savingLabel={existing ? t("slo.dialog.saving") : t("slo.dialog.creating")}
    >
      <div className="space-y-1">
        <label htmlFor="slo-name" className="text-sm font-medium">
          {t("slo.dialog.name")}
        </label>
        <Input
          id="slo-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </div>
      <div className="space-y-1">
        <label htmlFor="slo-type" className="text-sm font-medium">
          {t("slo.dialog.metricType")}
        </label>
        <Select
          id="slo-type"
          value={metricType}
          onChange={(e) => setMetricType(e.target.value as "availability" | "success_rate")}
        >
          {METRIC_TYPES.map((m) => (
            <option key={m.value} value={m.value}>
              {t(m.i18nKey)}
            </option>
          ))}
        </Select>
      </div>
      <div className="space-y-1">
        <label id="slo-tags-label" className="text-sm font-medium">
          {t("slo.dialog.tags")}
        </label>
        <TagChips
          value={tags}
          onChange={setTags}
          placeholder={t("slo.dialog.tagsPlaceholder")}
          aria-labelledby="slo-tags-label"
        />
        <p className="mt-1 text-xs text-muted-foreground">{t("slo.dialog.tagsHint")}</p>
      </div>
      <div className="space-y-1">
        <label htmlFor="slo-threshold" className="text-sm font-medium">
          {t("slo.dialog.threshold")} ({t("slo.dialog.thresholdSuffix")})
        </label>
        <Input
          id="slo-threshold"
          type="number"
          step="0.01"
          value={threshold}
          onChange={(e) => setThreshold(e.target.value)}
        />
      </div>
      <div className="space-y-1">
        <label htmlFor="slo-escalation" className="text-sm font-medium">
          {t("escalation.tabTitle")}
        </label>
        <Select
          id="slo-escalation"
          value={escalationPolicyId == null ? "" : String(escalationPolicyId)}
          onChange={(e) =>
            setEscalationPolicyId(e.target.value === "" ? null : Number(e.target.value))
          }
        >
          <option value="">无升级策略 / None</option>
          {escalationPolicies.map((p) => (
            <option key={p.id} value={String(p.id)}>
              {p.name}
            </option>
          ))}
        </Select>
      </div>
      <div className="flex items-center gap-2">
        <Switch id="slo-enabled" checked={enabled} onCheckedChange={setEnabled} />
        <label htmlFor="slo-enabled" className="text-sm font-medium">
          {t("slo.dialog.enabled")}
        </label>
      </div>
    </FormDialog>
  );
}
