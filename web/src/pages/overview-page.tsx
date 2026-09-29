import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { ArrowRight, ShieldAlert } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { DataSurface, DataSurfaceContent, DataSurfaceHeader } from "@/components/ui/data-surface";
import { InlineAlert } from "@/components/ui/inline-alert";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { Stagger, Reveal } from "@/components/ui/reveal";
import { Button } from "@/components/ui/button";
import { LoadingState } from "@/components/ui/loading-state";
import { useSharedContext } from "@/context/shared-context.hooks";
import { useTasksContext } from "@/context/tasks-context.hooks";
import { useAuth } from "@/context/auth-context.hooks";
import { formatRelativeTime, formatTime } from "@/lib/date-utils";
import { getErrorMessage, getLocale } from "@/lib/utils";
import type { HealthIncidentAction, HealthIncidentGroup, HealthIncidentSeverity, HealthIncidentSourceType, OverviewTrafficSeries, OverviewTrafficWindow } from "@/types/domain";
import { OverviewTrafficChart } from "@/pages/overview-page.traffic";
import { OverviewRecentTasks } from "@/pages/overview-page.recent-tasks";
import { OverviewHero } from "@/pages/overview-page.hero";

const INCIDENT_PREVIEW_LIMIT = 4;

function isRetiredIncidentAction(action: HealthIncidentAction): boolean {
  return action.code === "view_node_metrics" || action.href.includes("tab=metrics");
}

type TFunction = ReturnType<typeof useTranslation>["t"];

function severityTone(severity: HealthIncidentSeverity): "destructive" | "warning" | "info" {
  if (severity === "critical") return "destructive";
  if (severity === "warning") return "warning";
  return "info";
}

function severityLabel(t: TFunction, severity: HealthIncidentSeverity) {
  return t(`overview.healthIncidentSeverity.${severity}`);
}

function sourceLabel(t: TFunction, source: HealthIncidentSourceType) {
  return t(`overview.healthIncidentSource.${source}`);
}

function resourceLabel(t: TFunction, group: HealthIncidentGroup) {
  const resourceName = group.resource.name || t("common.unknown");
  const typeLabel = t(`overview.healthIncidentResource.${group.resource.type}`);
  if (group.resource.type === "task" && group.resource.nodeName) {
    return `${typeLabel} · ${resourceName} / ${group.resource.nodeName}`;
  }
  if (group.resource.type === "policy" && group.resource.nodeName) {
    return `${typeLabel} · ${resourceName} / ${group.resource.nodeName}`;
  }
  return `${typeLabel} · ${resourceName}`;
}

function relativeIncidentTime(value: string) {
  const locale = getLocale().startsWith("zh") ? "zh" : "en";
  return formatRelativeTime(value, locale);
}

function HealthIncidentTimelinePanel({
  groups,
  loading,
  error,
  onRetry,
}: {
  groups: HealthIncidentGroup[];
  loading: boolean;
  error: string | null;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  const visibleGroups = groups.slice(0, INCIDENT_PREVIEW_LIMIT);

  return (
    <DataSurface>
      <DataSurfaceHeader
        title={
          <span className="inline-flex items-center gap-2">
            <ShieldAlert className="size-4 text-primary" aria-hidden />
            {t("overview.healthIncidentTitle")}
          </span>
        }
        description={t("overview.healthIncidentDesc")}
        actions={
          <Button variant="outline" size="sm" onClick={onRetry} disabled={loading}>
            {t("common.refresh")}
          </Button>
        }
      />
      <DataSurfaceContent className="space-y-3">
        {loading ? (
          <LoadingState
            className="rounded-lg border border-border bg-background/60"
            title={t("overview.healthIncidentLoading")}
            description={t("overview.healthIncidentLoadingDesc")}
            rows={2}
          />
        ) : null}

        {!loading && error ? (
          <InlineAlert tone="warning" title={t("overview.healthIncidentErrorTitle")}>
            <span>{error}</span>
          </InlineAlert>
        ) : null}

        {!loading && !error && groups.length === 0 ? (
          <InlineAlert tone="success" title={t("overview.healthIncidentEmptyTitle")}>
            <span>{t("overview.healthIncidentEmptyDesc")}</span>
          </InlineAlert>
        ) : null}

        {!loading && !error && groups.length > 0 ? (
          <div className="space-y-3" role="list" aria-label={t("overview.healthIncidentListAriaLabel")}>
            {visibleGroups.map((group) => {
              const primaryAction = group.nextActions.find((action) => action.href && !isRetiredIncidentAction(action));
              return (
                <article
                  key={group.id}
                  role="listitem"
                  className="rounded-lg border border-border bg-background/60 p-3"
                >
                  <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
                    <div className="min-w-0 space-y-2">
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge tone={severityTone(group.severity)}>
                          {severityLabel(t, group.severity)}
                        </Badge>
                        <span className="text-xs text-muted-foreground">
                          {t("overview.healthIncidentEvents", { count: group.eventCount })}
                        </span>
                        <time
                          dateTime={group.lastSeenAt}
                          className="text-xs text-muted-foreground"
                          title={formatTime(group.lastSeenAt)}
                        >
                          {relativeIncidentTime(group.lastSeenAt)}
                        </time>
                      </div>
                      <h2 className="text-sm font-semibold text-foreground">
                        {resourceLabel(t, group)}
                      </h2>
                      <p className="text-sm leading-relaxed text-muted-foreground">
                        {group.likelyCause || t("overview.healthIncidentUnknownCause")}
                      </p>
                      <div className="flex flex-wrap gap-1.5">
                        {group.sourceTypes.map((source) => (
                          <Badge key={source} tone="neutral" dot={false} className="text-[11px]">
                            {sourceLabel(t, source)}
                          </Badge>
                        ))}
                      </div>
                    </div>
                    {primaryAction ? (
                      <Button asChild variant="ghost" size="sm" className="shrink-0 justify-start">
                        <Link to={primaryAction.href}>
                          {primaryAction.label || t("overview.healthIncidentPrimaryAction")}
                          <ArrowRight className="size-3.5" aria-hidden />
                        </Link>
                      </Button>
                    ) : null}
                  </div>
                </article>
              );
            })}
          </div>
        ) : null}

        {!loading && !error && groups.length > INCIDENT_PREVIEW_LIMIT ? (
          <p className="text-xs text-muted-foreground">
            {t("overview.healthIncidentMore", { count: groups.length - INCIDENT_PREVIEW_LIMIT })}
          </p>
        ) : null}
      </DataSurfaceContent>
    </DataSurface>
  );
}

function parseDateValue(value?: string) {
  if (!value) {
    return 0;
  }

  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? 0 : parsed;
}

export function OverviewPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { overview, loading, refreshVersion, fetchOverviewTraffic, fetchHealthIncidentTimeline } = useSharedContext();
  const { tasks, refreshTasks } = useTasksContext();

  useEffect(() => {
    void refreshTasks();
  }, [refreshTasks]);

  const [trafficWindow, setTrafficWindow] = useState<OverviewTrafficWindow>("1h");
  const [trafficData, setTrafficData] = useState<OverviewTrafficSeries | null>(null);
  const [trafficLoading, setTrafficLoading] = useState(true);
  const [trafficError, setTrafficError] = useState<string | null>(null);
  const [incidentGroups, setIncidentGroups] = useState<HealthIncidentGroup[]>([]);
  const [incidentLoading, setIncidentLoading] = useState(true);
  const [incidentError, setIncidentError] = useState<string | null>(null);
  const [incidentReloadKey, setIncidentReloadKey] = useState(0);
  const [visibleLayers, setVisibleLayers] = useState({ throughput: true, activity: true, failures: true });
  const trafficRequestRef = useRef(0);
  const incidentRequestRef = useRef(0);
  const incidentScope = `${token}:${refreshVersion}:${incidentReloadKey}`;
  const trafficScope = `${token}:${refreshVersion}:${trafficWindow}`;
  const [previousIncident, setPreviousIncident] = useState({ scope: incidentScope, fetch: fetchHealthIncidentTimeline });
  const [previousTraffic, setPreviousTraffic] = useState({ scope: trafficScope, fetch: fetchOverviewTraffic });
  if (previousIncident.scope !== incidentScope || previousIncident.fetch !== fetchHealthIncidentTimeline) {
    setPreviousIncident({ scope: incidentScope, fetch: fetchHealthIncidentTimeline });
    setIncidentLoading(true);
    setIncidentError(null);
    setIncidentGroups([]);
  }
  if (previousTraffic.scope !== trafficScope || previousTraffic.fetch !== fetchOverviewTraffic) {
    setPreviousTraffic({ scope: trafficScope, fetch: fetchOverviewTraffic });
    setTrafficLoading(true);
    setTrafficError(null);
    setTrafficData(null);
  }
  useEffect(() => {
    const controller = new AbortController();
    const requestId = incidentRequestRef.current + 1;
    incidentRequestRef.current = requestId;
    void fetchHealthIncidentTimeline({ windowHours: 72, signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted && incidentRequestRef.current === requestId) {
          setIncidentGroups(result.groups);
        }
      })
      .catch((error) => {
        if (controller.signal.aborted) {
          return;
        }
        if (incidentRequestRef.current === requestId) {
          setIncidentError(getErrorMessage(error, t("overview.healthIncidentLoadFailed")));
          setIncidentGroups([]);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted && incidentRequestRef.current === requestId) {
          setIncidentLoading(false);
        }
      });

    return () => {
      controller.abort();
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps -- t is stable from react-i18next
  }, [fetchHealthIncidentTimeline, refreshVersion, incidentReloadKey, token]);

  useEffect(() => {
    const controller = new AbortController();
    const requestId = trafficRequestRef.current + 1;
    trafficRequestRef.current = requestId;
    void fetchOverviewTraffic(trafficWindow, { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted && trafficRequestRef.current === requestId) {
          setTrafficData(result);
        }
      })
      .catch((error) => {
        if (controller.signal.aborted) {
          return;
        }
        if (trafficRequestRef.current === requestId) {
          setTrafficError(getErrorMessage(error, t("overview.trafficLoadFailed")));
          setTrafficData(null);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted && trafficRequestRef.current === requestId) {
          setTrafficLoading(false);
        }
      });

    return () => {
      controller.abort();
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps -- t is stable from react-i18next
  }, [fetchOverviewTraffic, refreshVersion, trafficWindow, token]);

  const recentTasks = useMemo(
    () => [...tasks]
      .sort((first, second) => {
        const timeGap = parseDateValue(second.createdAt) - parseDateValue(first.createdAt);
        if (timeGap !== 0) {
          return timeGap;
        }
        return second.id - first.id;
      })
      .slice(0, 5),
    [tasks]
  );

  const chartMetrics = useMemo(() => {
    const points = trafficData?.points ?? [];
    const totalStartedCount = points.reduce((sum, point) => sum + point.startedCount, 0);
    const totalFailedCount = points.reduce((sum, point) => sum + point.failedCount, 0);
    const peakThroughput = points.reduce((max, point) => Math.max(max, point.throughputMbps), 0);

    const chartData = points.map((point) => ({
      label: point.label,
      throughput: point.throughputMbps,
      activity: Math.max(point.startedCount, point.activeTaskCount),
      failed: point.failedCount,
    }));

    return {
      chartData,
      hasRealSamples: Boolean(trafficData?.hasRealSamples),
      truncated: Boolean(trafficData?.truncated),
      peakThroughput,
      totalStartedCount,
      totalFailedCount,
    };
  }, [trafficData]);

  const { yMaxLeft, yMaxRight } = useMemo(() => {
    const points = chartMetrics.chartData;
    let maxThroughput = 0;
    let maxCount = 0;
    for (const point of points) {
      if (visibleLayers.throughput) maxThroughput = Math.max(maxThroughput, point.throughput);
      if (visibleLayers.activity) maxCount = Math.max(maxCount, point.activity);
      if (visibleLayers.failures) maxCount = Math.max(maxCount, point.failed);
    }
    return {
      yMaxLeft: maxThroughput > 0 ? Math.ceil((maxThroughput * 1.1) / 50) * 50 : 100,
      yMaxRight: maxCount > 0 ? Math.max(1, Math.ceil(maxCount * 1.2)) : 5,
    };
  }, [chartMetrics.chartData, visibleLayers]);

  return (
    <Stagger className="space-y-5">
      <Reveal><OverviewHero /></Reveal>
      <StatCardsSection
        compact
        className="max-w-md animate-slide-up [animation-delay:150ms] sm:grid-cols-1 xl:grid-cols-1"
        items={[
          {
            title: t("overview.policyCoverage"),
            value: overview.activePolicies,
            description: t("overview.policyCoverageDesc", { count: tasks.length }),
            tone: "primary",
          },
        ]}
      />

      <Reveal>
        <HealthIncidentTimelinePanel
          groups={incidentGroups}
          loading={incidentLoading}
          error={incidentError}
          onRetry={() => setIncidentReloadKey((current) => current + 1)}
        />
      </Reveal>

      <Reveal>
        <OverviewTrafficChart
          trafficWindow={trafficWindow}
          setTrafficWindow={setTrafficWindow}
          trafficLoading={trafficLoading}
          trafficError={trafficError}
          chartMetrics={chartMetrics}
          visibleLayers={visibleLayers}
          setVisibleLayers={setVisibleLayers}
          yMaxLeft={yMaxLeft}
          yMaxRight={yMaxRight}
        />
      </Reveal>

      <Reveal>
        <OverviewRecentTasks tasks={tasks} recentTasks={recentTasks} loading={loading} />
      </Reveal>
    </Stagger>
  );
}
