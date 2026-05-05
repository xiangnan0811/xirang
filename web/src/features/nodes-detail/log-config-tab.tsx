import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";

const DENIED_PREFIXES = ["/etc/", "/proc/", "/sys/", "/dev/", "/boot/", "/root/"];

function validatePaths(paths: string[], t: (k: string) => string): string | null {
  if (paths.length > 20) return t("nodeLogs.nodeConfig.validation.tooMany");
  for (const p of paths) {
    if (!p.startsWith("/")) return t("nodeLogs.nodeConfig.validation.notAbsolute");
    if (DENIED_PREFIXES.some((prefix) => p.startsWith(prefix))) {
      return t("nodeLogs.nodeConfig.validation.deniedPrefix");
    }
    if (p.includes("*") || p.includes("?")) {
      return t("nodeLogs.nodeConfig.validation.wildcardNotAllowed");
    }
  }
  return null;
}

export default function LogConfigTab({ nodeId }: { nodeId: number }) {
  const { t } = useTranslation();
  const [logPaths, setLogPaths] = useState("");
  const [journalctlEnabled, setJournalctlEnabled] = useState(false);
  const [retentionDays, setRetentionDays] = useState(0);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [retentionError, setRetentionError] = useState<string | null>(null);
  const [pathsError, setPathsError] = useState<string | null>(null);

  const fetchConfig = useCallback(async (signal: AbortSignal) => {
    const token = sessionStorage.getItem("xirang-auth-token");
    if (!token || nodeId <= 0) return;
    setLoading(true);
    try {
      const cfg = await apiClient.getNodeLogConfig(token, nodeId, { signal });
      if (!signal.aborted) {
        setLogPaths((cfg.log_paths ?? []).join("\n"));
        setJournalctlEnabled(cfg.log_journalctl_enabled);
        setRetentionDays(cfg.log_retention_days);
      }
    } catch {
      // ignore aborts and network errors on load
    } finally {
      if (!signal.aborted) setLoading(false);
    }
  }, [nodeId]);

  useEffect(() => {
    const controller = new AbortController();
    void fetchConfig(controller.signal);
    return () => controller.abort();
  }, [fetchConfig]);

  const handleSave = async () => {
    const token = sessionStorage.getItem("xirang-auth-token");
    if (!token) return;

    setRetentionError(null);
    setPathsError(null);

    if (retentionDays < 0 || retentionDays > 365) {
      setRetentionError(t("nodeLogs.nodeConfig.validation.retentionOutOfRange"));
      return;
    }

    const paths = logPaths
      .split("\n")
      .map((p) => p.trim())
      .filter(Boolean);

    const validationError = validatePaths(paths, t);
    if (validationError) {
      setPathsError(validationError);
      return;
    }

    setSaving(true);
    try {
      const updated = await apiClient.updateNodeLogConfig(token, nodeId, {
        log_paths: paths,
        log_journalctl_enabled: journalctlEnabled,
        log_retention_days: retentionDays,
      });
      setLogPaths((updated.log_paths ?? []).join("\n"));
      setJournalctlEnabled(updated.log_journalctl_enabled);
      setRetentionDays(updated.log_retention_days);
      toast.success(t("nodeLogs.nodeConfig.saved"));
    } catch (err: unknown) {
      toast.error(getErrorMessage(err));
    } finally {
      setSaving(false);
    }
  };

  if (loading) return <p className="text-sm text-muted-foreground">{t("common.loading")}</p>;

  return (
    <div className="space-y-6 max-w-2xl" data-testid="log-config-tab">
      <div className="flex items-center gap-3">
        <Switch
          id="journalctl-enabled"
          checked={journalctlEnabled}
          onCheckedChange={setJournalctlEnabled}
        />
        <label htmlFor="journalctl-enabled" className="text-sm font-medium cursor-pointer">
          {t("nodeLogs.nodeConfig.journalctlEnabled")}
        </label>
      </div>

      <div className="space-y-1.5">
        <label htmlFor="log-paths" className="text-sm font-medium">
          {t("nodeLogs.nodeConfig.logPaths")}
        </label>
        <Textarea
          id="log-paths"
          value={logPaths}
          onChange={(e) => {
            setLogPaths(e.target.value);
            if (pathsError) setPathsError(null);
          }}
          rows={6}
          placeholder="/var/log/nginx/access.log"
          aria-invalid={pathsError ? true : undefined}
          aria-describedby={pathsError ? "log-paths-error" : "log-paths-hint"}
        />
        {pathsError ? (
          <p id="log-paths-error" role="alert" className="text-xs text-destructive">{pathsError}</p>
        ) : (
          <p id="log-paths-hint" className="text-xs text-muted-foreground">{t("nodeLogs.nodeConfig.logPathsHint")}</p>
        )}
      </div>

      <div className="space-y-1.5">
        <label htmlFor="retention-days" className="text-sm font-medium">
          {t("nodeLogs.nodeConfig.retentionDays")}
        </label>
        <Input
          id="retention-days"
          type="number"
          min={0}
          max={365}
          value={retentionDays}
          onChange={(e) => {
            setRetentionDays(Number(e.target.value));
            if (retentionError) setRetentionError(null);
          }}
          className="w-32"
          aria-invalid={retentionError ? true : undefined}
          aria-describedby={retentionError ? "retention-days-error" : "retention-days-hint"}
        />
        {retentionError ? (
          <p id="retention-days-error" role="alert" className="text-xs text-destructive">{retentionError}</p>
        ) : (
          <p id="retention-days-hint" className="text-xs text-muted-foreground">{t("nodeLogs.nodeConfig.retentionDaysHint")}</p>
        )}
      </div>

      <Button size="sm" onClick={handleSave} disabled={saving}>
        {saving ? t("common.loading") : t("nodeLogs.nodeConfig.save")}
      </Button>
    </div>
  );
}
