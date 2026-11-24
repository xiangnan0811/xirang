import { useEffect, useState } from "react";
import { HardDrive } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  BarChart,
} from "recharts";
import {
  DataSurface,
  DataSurfaceContent,
  DataSurfaceHeader,
} from "@/components/ui/data-surface";
import { LoadingState } from "@/components/ui/loading-state";
import { InlineAlert } from "@/components/ui/inline-alert";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { StorageUsageData } from "@/types/domain";

function formatPartialLowerBoundGB(usedGB: number): string | null {
  if (!Number.isFinite(usedGB) || usedGB <= 0) {
    return null;
  }
  // Truncate downward to 2 decimals (matching backend floor) so a `≥` lower bound
  // can never round upward past the confirmed measured bytes (e.g. 2.09 stays 2.09, not 2.1).
  const floored = Math.floor((usedGB + 1e-12) * 100) / 100;
  return floored > 0 ? floored.toFixed(2) : null;
}

function pctColor(pct: number): string {
  if (pct >= 90) return "bg-destructive";
  if (pct >= 70) return "bg-warning";
  return "bg-success";
}

function pctTextColor(pct: number): string {
  if (pct >= 90) return "text-[hsl(var(--destructive-text))]";
  if (pct >= 70) return "text-[hsl(var(--warning-text))]";
  return "text-[hsl(var(--success-text))]";
}

export function StorageUsagePanel() {
  const { token } = useAuth();
  const { i18n } = useTranslation();
  return <StorageUsagePanelContent key={`${token ?? ""}:${i18n.language}`} />;
}

function StorageUsagePanelContent() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [data, setData] = useState<StorageUsageData | null>(null);
  const [loading, setLoading] = useState(Boolean(token) || import.meta.env.VITE_ENABLE_DEMO_MODE === "true");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) {
      if (import.meta.env.VITE_ENABLE_DEMO_MODE === "true") {
        let cancelled = false;
        import("@/data/mock")
          .then((mocks) => {
            if (!cancelled) {
              setData(mocks.buildMockStorageUsage());
            }
          })
          .catch((err) => {
            if (!cancelled) {
              setError(getErrorMessage(err, t('storage.loadFailed')));
            }
          })
          .finally(() => {
            if (!cancelled) {
              setLoading(false);
            }
          });
        return () => {
          cancelled = true;
        };
      }
      return;
    }
    const controller = new AbortController();

    apiClient
      .getStorageUsage(token, { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted) {
          setData(result);
        }
      })
      .catch((err) => {
        if (!controller.signal.aborted) {
          setError(getErrorMessage(err, t('storage.loadFailed')));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setLoading(false);
        }
      });

    return () => controller.abort();
  }, [token, t]);

  if (loading) {
    return (
      <DataSurface>
        <DataSurfaceHeader title={t("storage.title")} />
        <DataSurfaceContent>
          <LoadingState title={t("storage.loadingTitle")} rows={2} />
        </DataSurfaceContent>
      </DataSurface>
    );
  }

  if (error || !data) {
    return (
      <DataSurface>
        <DataSurfaceHeader title={t("storage.title")} />
        <DataSurfaceContent>
          <InlineAlert tone="warning">
            {error ?? t("common.noData")}
          </InlineAlert>
        </DataSurfaceContent>
      </DataSurface>
    );
  }

  const { mountPoints, perNode } = data;
  const completePerNode = perNode.filter((item) => !item.incomplete);
  const incompletePerNode = perNode.filter((item) => item.incomplete);
  const mountPointsIncomplete = Boolean(data.mountPointsIncomplete);
  const hasIncompleteScan =
    Boolean(data.incomplete) || mountPointsIncomplete || incompletePerNode.length > 0;
  const incompleteMessage =
    mountPointsIncomplete && incompletePerNode.length > 0
      ? t("storage.scanIncompleteDesc")
      : mountPointsIncomplete
        ? t("storage.mountPointsIncompleteDesc")
        : incompletePerNode.length > 0
          ? t("storage.perNodeIncompleteDesc")
          : t("storage.scanIncompleteDesc");

  return (
    <DataSurface>
      <DataSurfaceHeader
        title={
          <span className="inline-flex items-center gap-2">
            <HardDrive className="size-4 text-primary" aria-hidden />
            {t("storage.title")}
          </span>
        }
      />
      <DataSurfaceContent className="space-y-4">
        {hasIncompleteScan ? (
          <InlineAlert tone="warning" title={t("storage.scanIncompleteTitle")}>
            {incompleteMessage}
          </InlineAlert>
        ) : null}

        {/* Mount points as progress bars */}
        {mountPoints.length > 0 && (
          <div className="space-y-3">
            <p className="text-xs font-medium text-muted-foreground">{t('storage.mountPointUsage')}</p>
            {mountPoints.map((mp) => (
              <div key={mp.path} className="space-y-1">
                <div className="flex items-center justify-between text-sm">
                  <span className="font-mono text-xs truncate">{mp.path}</span>
                  <span className={`text-xs font-medium ${pctTextColor(mp.pct)}`}>
                    {mp.usedGB.toFixed(1)} / {mp.totalGB.toFixed(1)} GB ({Math.round(mp.pct)}%)
                  </span>
                </div>
                <div className="h-2 w-full rounded-full bg-muted/50 overflow-hidden">
                  <div
                    className={`h-full rounded-full transition-[width] ${pctColor(mp.pct)}`}
                    style={{ width: `${Math.min(100, mp.pct)}%` }}
                  />
                </div>
              </div>
            ))}
          </div>
        )}

        {/* Per-node storage bar chart (complete measurements only) */}
        {completePerNode.length > 0 && (
          <div>
            <p className="mb-2 text-xs font-medium text-muted-foreground">{t('storage.perNodeUsage')}</p>
            <div role="img" aria-label={t('storage.perNodeChartAriaLabel')}>
              <ResponsiveContainer width="100%" height={Math.max(120, completePerNode.length * 28 + 40)}>
                <BarChart
                  data={completePerNode}
                  layout="vertical"
                  margin={{ top: 4, right: 4, left: 0, bottom: 4 }}
                >
                  <CartesianGrid strokeDasharray="4 3" stroke="hsl(var(--border))" strokeOpacity={0.25} horizontal={false} />
                  <XAxis
                    type="number"
                    tick={{ fontSize: 9, fill: "hsl(var(--muted-foreground))", opacity: 0.6 }}
                    stroke="transparent"
                    tickLine={false}
                    unit=" GB"
                  />
                  <YAxis
                    type="category"
                    dataKey="nodeName"
                    tick={{ fontSize: 10, fill: "hsl(var(--muted-foreground))" }}
                    stroke="transparent"
                    tickLine={false}
                    width={80}
                  />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: "hsl(var(--card))",
                      border: "1px solid hsl(var(--border))",
                      fontSize: 11,
                      borderRadius: 6,
                    }}
                    formatter={(value) => [`${Number(value).toFixed(1)} GB`, t('storage.used')]}
                    labelStyle={{ color: "hsl(var(--muted-foreground))" }}
                  />
                  <Bar dataKey="usedGB" name={t('storage.used')} fill="hsl(var(--chart-ingress))" radius={[0, 4, 4, 0]} maxBarSize={16} isAnimationActive={false} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          </div>
        )}

        {/* Incomplete per-node directory scans listed separately so partial/unscanned bytes never appear as complete totals */}
        {incompletePerNode.length > 0 && (
          <div className="space-y-2">
            <p className="text-xs font-medium text-muted-foreground">{t("storage.perNodeIncompleteHeading")}</p>
            <ul className="divide-y divide-border/60 rounded-md border border-warning/30 bg-warning/5">
              {incompletePerNode.map((item) => (
                <li
                  key={`${item.nodeId}:${item.path}`}
                  className="flex flex-wrap items-center justify-between gap-2 px-3 py-2 text-xs"
                >
                  <div className="min-w-0">
                    <span className="font-medium text-foreground">{item.nodeName || `#${item.nodeId}`}</span>
                    <span className="ml-2 font-mono text-muted-foreground break-all">{item.path}</span>
                  </div>
                  <span className="shrink-0 font-medium tabular-nums text-[hsl(var(--warning-text))]">
                    {(() => {
                      const lowerBound = formatPartialLowerBoundGB(item.usedGB);
                      return lowerBound
                        ? t("storage.partialSize", { gb: lowerBound })
                        : t("storage.scanNotCompleted");
                    })()}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {mountPoints.length === 0 && perNode.length === 0 && !hasIncompleteScan && (
          <p className="text-sm text-muted-foreground text-center py-4">{t('common.noData')}</p>
        )}
      </DataSurfaceContent>
    </DataSurface>
  );
}
