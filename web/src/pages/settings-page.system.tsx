import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import { cn, getErrorMessage } from "@/lib/utils";
import type { SettingDef, ResolvedSetting } from "@/lib/api/settings-api";

const CATEGORY_ORDER = ["security", "node_monitor", "retention", "storage", "alert", "anomaly"];

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

  const loadSettings = useCallback(async () => {
    if (!token) return;
    try {
      const [res, logSettings] = await Promise.all([
        apiClient.getSettings(token),
        apiClient.getLogsSettings(token),
      ]);
      setDefinitions(res.definitions);
      setValues(res.values);
      const edits: Record<string, string> = {};
      for (const [key, val] of Object.entries(res.values)) {
        edits[key] = val.value;
      }
      setEditValues(edits);
      setLogRetentionDays(logSettings.default_retention_days);
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
                        <AlertTriangle className="size-3" />
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
