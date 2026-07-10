import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Activity, AlertTriangle, CheckCircle2, Globe, HelpCircle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { DataSurface } from "@/components/ui/data-surface";
import { createServiceMonitorsApi } from "@/lib/api/service-monitors";
import { useVisibilityPolling } from "@/hooks/use-visibility-polling";
import { formatTime } from "@/lib/date-utils";
import type { StatusPageItem } from "@/types/domain";

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function StatusDot({ status }: { status: string }) {
  const base = "size-2.5 rounded-full shrink-0";
  if (status === "up") return <span className={`${base} bg-success`} aria-label="Up" />;
  if (status === "down") return <span className={`${base} bg-destructive`} aria-label="Down" />;
  return <span className={`${base} bg-muted-foreground/50`} aria-label="Unknown" />;
}

function StatusIcon({ status }: { status: string }) {
  if (status === "up") return <CheckCircle2 className="size-5 text-success" aria-hidden="true" />;
  if (status === "down") return <AlertTriangle className="size-5 text-destructive" aria-hidden="true" />;
  return <HelpCircle className="size-5 text-muted-foreground/50" aria-hidden="true" />;
}

export function StatusPage() {
  const { t } = useTranslation();
  const [items, setItems] = useState<StatusPageItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchStatus = useCallback(async (signal?: AbortSignal) => {
    try {
      const data = await createServiceMonitorsApi().getStatusPage(signal);
      if (signal?.aborted) return;
      setItems(data);
      setError(null);
    } catch (err) {
      if (isAbortError(err) || signal?.aborted) return;
      setError(t("serviceMonitor.statusPageLoadFailed"));
    } finally {
      if (!signal?.aborted) {
        setLoading(false);
      }
    }
  }, [t]);

  useEffect(() => {
    setLoading(true);
    const controller = new AbortController();
    void fetchStatus(controller.signal);
    return () => {
      controller.abort();
    };
  }, [fetchStatus]);

  // 每 30s 自动刷新；后台标签页不轮询，切回前台立即补拉一次。
  useVisibilityPolling(() => { void fetchStatus(); }, 30_000);

  const overallStatus = items.length === 0
    ? "unknown"
    : items.every((i) => i.status === "up")
      ? "up"
      : items.some((i) => i.status === "down")
        ? "down"
        : "unknown";

  return (
    <div className="min-h-screen bg-background">
      <div className="mx-auto max-w-4xl px-4 py-12">
        {/* Header */}
        <div className="mb-8 text-center">
          <div className="mb-3 inline-flex items-center gap-2.5">
            <Globe className="size-7 text-primary" aria-hidden="true" />
            <h1 className="text-2xl font-semibold tracking-tight">
              {t("serviceMonitor.statusPageTitle")}
            </h1>
          </div>
          <p className="text-sm text-muted-foreground">
            {t("serviceMonitor.statusPageDesc")}
          </p>
        </div>

        {/* Overall Banner */}
        <div
          className={`mb-6 rounded-lg border px-6 py-4 ${
            overallStatus === "up"
              ? "border-success/30 bg-success/5"
              : overallStatus === "down"
                ? "border-destructive/30 bg-destructive/5"
                : "border-muted-foreground/20 bg-muted/30"
          }`}
        >
          <div className="flex items-center gap-3">
            <StatusIcon status={overallStatus} />
            <div>
              <p className="font-medium">
                {overallStatus === "up"
                  ? t("serviceMonitor.allSystemsOperational")
                  : overallStatus === "down"
                    ? t("serviceMonitor.someSystemsDown")
                    : t("serviceMonitor.noMonitorsConfigured")}
              </p>
              <p className="text-xs text-muted-foreground">
                {t("serviceMonitor.lastChecked")}: {loading ? t("serviceMonitor.checkingNow") : t("common.justNow")}
              </p>
            </div>
          </div>
        </div>

        {/* Loading */}
        {loading && (
          <div className="flex items-center justify-center py-16">
            <div className="size-6 animate-spin rounded-full border-2 border-muted-foreground border-t-transparent" />
          </div>
        )}

        {/* Error */}
        {!loading && error && (
          <Card className="border-destructive/30 bg-destructive/5">
            <CardContent className="flex items-center gap-3 py-6">
              <AlertTriangle className="size-5 text-destructive" aria-hidden="true" />
              <p className="text-sm text-destructive">{error}</p>
            </CardContent>
          </Card>
        )}

        {/* Empty */}
        {!loading && !error && items.length === 0 && (
          <Card>
            <CardContent className="flex flex-col items-center gap-2 py-12 text-center">
              <Activity className="size-10 text-muted-foreground/30" aria-hidden="true" />
              <p className="text-sm text-muted-foreground">
                {t("serviceMonitor.noMonitors")}
              </p>
            </CardContent>
          </Card>
        )}

        {/* Monitor Grid */}
        {!loading && items.length > 0 && (
          <div className="grid gap-3 sm:grid-cols-2">
            {items.map((item) => (
              <DataSurface
                key={item.name}
                className="transition-colors hover:bg-muted/20"
              >
                <CardContent className="flex items-start gap-4 p-4">
                  <StatusDot status={item.status} />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <h3 className="truncate text-sm font-medium">{item.name}</h3>
                      <Badge tone="neutral" className="shrink-0 text-[10px]">
                        {item.type.toUpperCase()}
                      </Badge>
                    </div>
                    <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
                      <span>
                        {t("serviceMonitor.fieldUptime")}: {(item.uptimePct ?? 0).toFixed(1)}%
                      </span>
                      {item.lastCheckedAt && (
                        <span>
                          {t("serviceMonitor.lastChecked")}: {formatTime(item.lastCheckedAt)}
                        </span>
                      )}
                    </div>
                  </div>
                </CardContent>
              </DataSurface>
            ))}
          </div>
        )}

        {/* Footer */}
        <p className="mt-8 text-center text-xs text-muted-foreground/60">
          {t("serviceMonitor.poweredBy")} Xirang
        </p>
      </div>
    </div>
  );
}
