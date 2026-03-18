import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { SettingDef, ResolvedSetting } from "@/lib/api/settings-api";

const CATEGORY_ORDER = ["security", "node_monitor", "retention", "storage", "alert"];

export function SystemTab() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [definitions, setDefinitions] = useState<SettingDef[]>([]);
  const [values, setValues] = useState<Record<string, ResolvedSetting>>({});
  const [editValues, setEditValues] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ type: "success" | "error"; text: string } | null>(null);

  const loadSettings = useCallback(async () => {
    if (!token) return;
    try {
      const res = await apiClient.getSettings(token);
      setDefinitions(res.definitions);
      setValues(res.values);
      const edits: Record<string, string> = {};
      for (const [key, val] of Object.entries(res.values)) {
        edits[key] = val.value;
      }
      setEditValues(edits);
    } catch {
      // ignore
    } finally {
      setLoading(false);
    }
  }, [token]);

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
  };

  const sourceBadge = (source: string) => {
    const colors: Record<string, string> = {
      db: "bg-blue-500/10 text-blue-600",
      env: "bg-amber-500/10 text-amber-600",
      default: "bg-zinc-500/10 text-zinc-500",
    };
    return (
      <span className={cn("rounded px-1.5 py-0.5 text-[10px] font-medium uppercase", colors[source] || colors.default)}>
        {source}
      </span>
    );
  };

  return (
    <div className="space-y-6 max-w-3xl">
      <h2 className="text-lg font-semibold">{t("settings.system.title")}</h2>

      {grouped.map(({ category, items }) => (
        <div key={category} className="rounded-lg border p-4 space-y-4">
          <h3 className="text-sm font-semibold">{categoryLabels[category] || category}</h3>
          {items.map((def) => {
            const resolved = values[def.key];
            return (
              <div key={def.key} className="flex items-center gap-4">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <p className="text-sm font-medium truncate">{def.description}</p>
                    {def.requires_restart && (
                      <span className="inline-flex items-center gap-0.5 text-[10px] text-amber-600" title={t("settings.system.requiresRestart")}>
                        <AlertTriangle className="size-3" />
                        {t("settings.system.restart")}
                      </span>
                    )}
                  </div>
                  <div className="flex items-center gap-2 mt-0.5">
                    <code className="text-[11px] text-muted-foreground">{def.key}</code>
                    {resolved && sourceBadge(resolved.source)}
                  </div>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {def.type === "bool" ? (
                    <select
                      className="h-8 rounded-md border border-input bg-background px-2 text-sm w-24"
                      value={editValues[def.key] || ""}
                      onChange={(e) => setEditValues((prev) => ({ ...prev, [def.key]: e.target.value }))}
                    >
                      <option value="true">{t("common.enabled")}</option>
                      <option value="false">{t("common.disabled")}</option>
                    </select>
                  ) : (
                    <input
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
    </div>
  );
}
