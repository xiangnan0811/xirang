import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, ShieldAlert } from "lucide-react";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast-sonner";
import { formatTime } from "@/lib/date-utils";
import { cn, getErrorMessage } from "@/lib/utils";
import type { SecurityRiskItem, SecurityRiskSummary, SettingDef, ResolvedSetting } from "@/lib/api/settings-api";

const CATEGORY_ORDER = ["security", "node_monitor", "retention", "storage", "alert", "anomaly"];

const riskToneClasses: Record<SecurityRiskItem["severity"], string> = {
  info: "border-info/30 bg-info/5 text-info",
  warning: "border-warning/30 bg-warning/5 text-warning-foreground dark:text-warning",
  critical: "border-destructive/30 bg-destructive/5 text-destructive",
};

const riskSeverityOrder: Record<SecurityRiskItem["severity"], number> = {
  critical: 0,
  warning: 1,
  info: 2,
};

function sortSecurityRiskItems(items: SecurityRiskItem[]): SecurityRiskItem[] {
  return items
    .map((item, index) => ({ item, index }))
    .sort((a, b) => {
      const severityDiff = riskSeverityOrder[a.item.severity] - riskSeverityOrder[b.item.severity];
      if (severityDiff !== 0) return severityDiff;
      const countDiff = b.item.count - a.item.count;
      if (countDiff !== 0) return countDiff;
      return a.index - b.index;
    })
    .map(({ item }) => item);
}

export function SystemTab() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [definitions, setDefinitions] = useState<SettingDef[]>([]);
  const [values, setValues] = useState<Record<string, ResolvedSetting>>({});
  const [editValues, setEditValues] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ type: "success" | "error"; text: string } | null>(null);

  const [logRetentionDays, setLogRetentionDays] = useState(30);
  const [logRetentionSaving, setLogRetentionSaving] = useState(false);
  const [securityRisk, setSecurityRisk] = useState<SecurityRiskSummary | null>(null);
  const [securityRiskError, setSecurityRiskError] = useState<string | null>(null);

  const loadSettings = useCallback(async () => {
    if (!token) return;
    try {
      const [settingsResult, logSettingsResult, riskResult] = await Promise.allSettled([
        apiClient.getSettings(token),
        apiClient.getLogsSettings(token),
        apiClient.getSecurityRiskSummary(token),
      ]);

      if (settingsResult.status === "fulfilled") {
        const res = settingsResult.value;
        setDefinitions(res.definitions);
        setValues(res.values);
        const edits: Record<string, string> = {};
        for (const [key, val] of Object.entries(res.values)) {
          edits[key] = val.value;
        }
        setEditValues(edits);
      }
      if (logSettingsResult.status === "fulfilled") {
        setLogRetentionDays(logSettingsResult.value.default_retention_days);
      }
      if (riskResult.status === "fulfilled") {
        setSecurityRisk(riskResult.value);
        setSecurityRiskError(null);
      } else {
        setSecurityRisk(null);
        setSecurityRiskError(getErrorMessage(riskResult.reason));
      }
    } catch {
      // ignore
    } finally {
      setLoading(false);
    }
  }, [token]);

  const handleSaveLogRetention = async () => {
    if (!token) return;
    setLogRetentionSaving(true);
    try {
      const updated = await apiClient.updateLogsSettings(token, { default_retention_days: logRetentionDays });
      setLogRetentionDays(updated.default_retention_days);
      toast.success(t("settings.system.saved"));
    } catch (err: unknown) {
      toast.error(getErrorMessage(err));
    } finally {
      setLogRetentionSaving(false);
    }
  };

  useEffect(() => { loadSettings(); }, [loadSettings]);

  const handleSave = async () => {
    if (!token) return;
    setSaving(true);
    setMessage(null);
    const changes: Record<string, string> = {};
    for (const [key, val] of Object.entries(editValues)) {
      if (values[key]?.value !== val) {
        changes[key] = val;
      }
    }
    if (Object.keys(changes).length === 0) {
      setMessage({ type: "success", text: t("settings.system.noChanges") });
      setSaving(false);
      return;
    }
    try {
      await apiClient.updateSettings(token, changes);
      setMessage({ type: "success", text: t("settings.system.saved") });
      await loadSettings();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : t("common.operationFailed");
      setMessage({ type: "error", text: msg });
    } finally {
      setSaving(false);
    }
  };

  const handleReset = async (key: string) => {
    if (!token) return;
    try {
      await apiClient.resetSetting(token, key);
      await loadSettings();
    } catch { /* ignore */ }
  };

  if (loading) {
    return <div className="py-8 text-center text-muted-foreground">{t("common.loading")}</div>;
  }

  const grouped = CATEGORY_ORDER.map((cat) => ({
    category: cat,
    items: definitions.filter((d) => d.category === cat),
  })).filter((g) => g.items.length > 0);
  const sortedSecurityRiskItems = securityRisk ? sortSecurityRiskItems(securityRisk.items) : [];
  const securityRiskGeneratedAtText = securityRisk?.generatedAt ? formatTime(securityRisk.generatedAt) : null;

  const categoryLabels: Record<string, string> = {
    security: t("settings.system.catSecurity"),
    node_monitor: t("settings.system.catNodeMonitor"),
    retention: t("settings.system.catRetention"),
    storage: t("settings.system.catStorage"),
    alert: t("settings.system.catAlert"),
    anomaly: t("anomaly.settings.sectionTitle"),
  };

  const sourceBadge = (source: string) => {
    const colors: Record<string, string> = {
      db: "bg-info/10 text-info",
      env: "bg-warning/10 text-warning-foreground dark:text-warning",
      default: "bg-muted text-muted-foreground",
    };
    return (
      <span className={cn("rounded px-1.5 py-0.5 text-micro font-medium uppercase", colors[source] || colors.default)}>
        {source}
      </span>
    );
  };

  return (
    <div className="space-y-6 max-w-3xl">
      <h2 className="text-lg font-semibold">{t("settings.system.title")}</h2>

      <section
        aria-labelledby="security-risk-summary-title"
        className="rounded-lg border border-border bg-card shadow-sm relative overflow-hidden p-5 space-y-4"
      >
        <div className="absolute top-0 left-0 w-1 h-full bg-warning/60" />
        <div className="flex items-start gap-3">
          <span className="mt-0.5 rounded-full bg-warning/10 p-2 text-warning-foreground dark:text-warning">
            <ShieldAlert className="size-4" aria-hidden />
          </span>
          <div className="space-y-1">
            <h3 id="security-risk-summary-title" className="text-sm font-semibold">
              {t("settings.system.securityRisk.title")}
            </h3>
            <p className="text-xs text-muted-foreground">
              {t("settings.system.securityRisk.description")}
            </p>
          </div>
        </div>

        {securityRiskError ? (
          <p className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive" role="status">
            {t("settings.system.securityRisk.loadFailed")}: {securityRiskError}
          </p>
        ) : null}

        {securityRisk ? (
          <>
            <p className="text-xs text-muted-foreground" role="status">
              {t("settings.system.securityRisk.summary", {
                total: securityRisk.summary.totalRisks,
                categories: securityRisk.summary.categories,
              })}
              {securityRiskGeneratedAtText ? ` · ${t("settings.system.securityRisk.generatedAt", { time: securityRiskGeneratedAtText })}` : null}
            </p>
            <div className="grid gap-3 md:grid-cols-2">
              {sortedSecurityRiskItems.map((item) => (
                <article key={item.code} className="rounded-md border border-border bg-background/60 p-3 space-y-2">
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <h4 className="text-sm font-medium">
                        {t(`settings.system.securityRisk.items.${item.code}.title`, { defaultValue: item.title })}
                      </h4>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {t(`settings.system.securityRisk.items.${item.code}.description`, { defaultValue: item.description })}
                      </p>
                    </div>
                    <span className={cn("shrink-0 rounded-full border px-2 py-0.5 text-xs font-medium", riskToneClasses[item.severity])}>
                      {t("settings.system.securityRisk.count", { count: item.count })}
                    </span>
                  </div>
                  {item.examples.length > 0 ? (
                    <ul className="space-y-1 text-xs text-muted-foreground" aria-label={t("settings.system.securityRisk.examplesLabel")}>
                      {item.examples.map((example) => (
                        <li key={example} className="truncate">{example}</li>
                      ))}
                    </ul>
                  ) : (
                    <p className="text-xs text-muted-foreground">{t("settings.system.securityRisk.noExamples")}</p>
                  )}
                </article>
              ))}
            </div>
          </>
        ) : null}
      </section>

      {grouped.map(({ category, items }) => (
        <div key={category} className="rounded-lg border border-border bg-card shadow-sm relative overflow-hidden p-5 space-y-4">
          <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" />
          <h3 className="text-sm font-semibold">{categoryLabels[category] || category}</h3>
          {items.map((def) => {
            const resolved = values[def.key];
            return (
              <div key={def.key} className="flex items-center gap-4">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <p className="text-sm font-medium truncate">{def.description}</p>
                    {def.requires_restart && (
                      <span className="inline-flex items-center gap-0.5 text-micro text-warning-foreground dark:text-warning" title={t("settings.system.requiresRestart")}>
                        <AlertTriangle className="size-3" aria-hidden />
                        {t("settings.system.restart")}
                      </span>
                    )}
                  </div>
                  <div className="flex items-center gap-2 mt-0.5">
                    <code className="text-mini text-muted-foreground">{def.key}</code>
                    {resolved && sourceBadge(resolved.source)}
                  </div>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {def.type === "bool" ? (
                    <select
                      id={def.key}
                      aria-label={def.description}
                      className="h-8 rounded-md border border-input bg-background px-2 text-sm w-24"
                      value={editValues[def.key] || ""}
                      onChange={(e) => setEditValues((prev) => ({ ...prev, [def.key]: e.target.value }))}
                    >
                      <option value="true">{t("common.enabled")}</option>
                      <option value="false">{t("common.disabled")}</option>
                    </select>
                  ) : (
                    <input
                      id={def.key}
                      aria-label={def.description}
                      className="h-8 w-28 rounded-md border border-input bg-background px-2 text-sm"
                      value={editValues[def.key] || ""}
                      onChange={(e) => setEditValues((prev) => ({ ...prev, [def.key]: e.target.value }))}
                    />
                  )}
                  {resolved?.source === "db" && (
                    <Button variant="ghost" size="sm" className="h-8 text-xs" onClick={() => handleReset(def.key)}>
                      {t("settings.system.reset")}
                    </Button>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      ))}

      {message && (
        <p className={cn("text-sm", message.type === "error" ? "text-destructive" : "text-success")}>
          {message.text}
        </p>
      )}

      <Button onClick={handleSave} disabled={saving}>
        {saving ? t("common.loading") : t("common.save")}
      </Button>

      <div className="rounded-lg border border-border bg-card shadow-sm relative overflow-hidden p-5 space-y-4">
        <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" />
        <h3 className="text-sm font-semibold">{t("nodeLogs.settings.defaultRetention")}</h3>
        <div className="space-y-1.5">
          <Input
            id="log-default-retention"
            type="number"
            min={1}
            max={365}
            value={logRetentionDays}
            onChange={(e) => setLogRetentionDays(Number(e.target.value))}
            className="w-32"
            aria-label={t("nodeLogs.settings.defaultRetention")}
          />
          <p className="text-xs text-muted-foreground">{t("nodeLogs.settings.defaultRetentionHint")}</p>
        </div>
        <Button size="sm" onClick={handleSaveLogRetention} disabled={logRetentionSaving}>
          {logRetentionSaving ? t("common.loading") : t("common.save")}
        </Button>
      </div>
    </div>
  );
}
