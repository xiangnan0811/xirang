import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { ArrowRight, ChevronDown, ChevronUp, Loader2, ShieldAlert } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { DataSurface, DataSurfaceContent, DataSurfaceHeader } from "@/components/ui/data-surface";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Stagger, Reveal } from "@/components/ui/reveal";
import { Button } from "@/components/ui/button";
import { LoadingState } from "@/components/ui/loading-state";
import { useSharedContext } from "@/context/shared-context.hooks";
import { useTasksContext } from "@/context/tasks-context.hooks";
import { useAuth } from "@/context/auth-context.hooks";
import { formatRelativeTime, formatTime } from "@/lib/date-utils";
import { getErrorMessage, getLocale } from "@/lib/utils";
import type {
  HealthIncidentAction,
  HealthIncidentGroup,
  HealthIncidentSeverity,
  HealthIncidentSourceType,
  HealthIncidentTimelineData,
  OverviewTrafficSeries,
  OverviewTrafficWindow,
} from "@/types/domain";
import { OverviewHero } from "@/pages/overview-page.hero";
import { OverviewPosture } from "@/pages/overview-page.posture";
import { OverviewTrafficChart } from "@/pages/overview-page.traffic";
import { computeThroughputMax } from "@/pages/overview-page.traffic.utils";
import { OverviewRecentTasks } from "@/pages/overview-page.recent-tasks";
import { sortRecentTasks } from "@/pages/overview-page.recent-tasks.utils";

const INCIDENT_PREVIEW_LIMIT = 4;

function positiveIncidentID(value: number | undefined): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function resolveHealthIncidentAction(group: HealthIncidentGroup): HealthIncidentAction {
  for (const action of group.nextActions) {
    const href = action.href.trim();
    if (action.code === "view_node_metrics" || href.includes("tab=metrics") || /[\\\p{Cc}]/u.test(action.href)) continue;
    if (action.code === "view_backup_confidence" && href === "/app/backups") {
      return { ...action, href: "/app/backups/overview" };
    }
    if (/^\/app\/(?:tasks|policies|notifications|backups\/overview)$/.test(href)) {
      return { ...action, href };
    }
    const target = href.match(/^\/app\/(?:notifications\?alert=alert-(\d+)|logs\?task=(\d+)|nodes\/(\d+)\?tab=alerts)$/);
    if (target && positiveIncidentID(Number(target[1] ?? target[2] ?? target[3]))) {
      return { ...action, href };
    }
  }
  const { type, id } = group.resource;
  if (positiveIncidentID(id)) {
    if (type === "node") return { code: "view_node_alerts", label: "", href: `/app/nodes/${id}?tab=alerts` };
    if (type === "task") return { code: "view_task_logs", label: "", href: `/app/logs?task=${id}` };
    if (type === "policy") return { code: "view_policies", label: "", href: "/app/policies" };
  }
  return { code: "view_notifications", label: "", href: "/app/notifications" };
}

type TFunction = ReturnType<typeof useTranslation>["t"];

function healthIncidentCauseSummary(group: HealthIncidentGroup, t: TFunction): string {
  const code = group.causeCode;
  let summary: string;
  switch (code) {
    case "alert_triggered":
    case "task_execution_failed":
    case "notification_delivery_issue":
    case "snapshot_churn_anomaly":
    case "snapshot_ransomware_pattern":
    case "anomaly_detected":
    case "verified_backup_missing":
    case "verified_backup_stale":
    case "policy_backup_failures":
    case "health_attention_required":
      summary = t(`overview.healthIncidentCause.${code}`);
      break;
    default:
      return t("overview.healthIncidentCause.health_attention_required");
  }
  const params = group.causeParams;
  if (code === "notification_delivery_issue") {
    if (params?.status === "failed") return `${summary} ${t("overview.healthIncidentCause.deliveryFailed")}`;
    if (params?.status === "retrying") return `${summary} ${t("overview.healthIncidentCause.deliveryRetrying")}`;
  }
  if (code === "snapshot_churn_anomaly" || code === "snapshot_ransomware_pattern" || code === "anomaly_detected") {
    const observed = params?.observedValue;
    const baseline = params?.baselineValue;
    if (typeof observed === "number" && Number.isFinite(observed) && Math.abs(observed) <= Number.MAX_SAFE_INTEGER &&
        typeof baseline === "number" && Number.isFinite(baseline) && Math.abs(baseline) <= Number.MAX_SAFE_INTEGER) {
      return `${summary} ${t("overview.healthIncidentCause.measurements", { observed: observed.toFixed(1), baseline: baseline.toFixed(1) })}`;
    }
  }
  if (code === "verified_backup_stale" && typeof params?.elapsedHours === "number" && Number.isSafeInteger(params.elapsedHours) && params.elapsedHours >= 0) {
    return `${summary} ${t("overview.healthIncidentCause.elapsedHours", { hours: params.elapsedHours })}`;
  }
  return summary;
}

function healthIncidentActionLabel(action: HealthIncidentAction, t: TFunction): string {
  switch (action.code) {
    case "view_alert":
    case "view_task_logs":
    case "view_tasks":
    case "view_node_alerts":
    case "view_backup_confidence":
    case "view_policies":
    case "view_notifications":
      return t(`overview.healthIncidentAction.${action.code}`);
    default:
      return t("overview.healthIncidentPrimaryAction");
  }
}

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

function normalizeIncidentTimeline(raw: HealthIncidentTimelineData): HealthIncidentTimelineData {
  const groups = Array.isArray(raw.groups) ? raw.groups : [];
  const windowHours =
    typeof raw.windowHours === "number" && Number.isFinite(raw.windowHours) && raw.windowHours > 0
      ? raw.windowHours
      : 72;
  const summary = raw.summary ?? {
    total: groups.length,
    critical: groups.filter((group) => group.severity === "critical").length,
    warning: groups.filter((group) => group.severity === "warning").length,
    info: groups.filter((group) => group.severity === "info").length,
  };
  return {
    generatedAt: typeof raw.generatedAt === "string" ? raw.generatedAt : "",
    windowHours,
    summary,
    groups,
  };
}

function HealthIncidentTimelinePanel({
  timeline,
  loading,
  error,
  scopeLabel,
  onRetry,
}: {
  timeline: HealthIncidentTimelineData | null;
  loading: boolean;
  error: string | null;
  scopeLabel: string;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);

  const groups = timeline?.groups ?? [];
  const windowHours = timeline?.windowHours ?? 72;
  const generatedAt = timeline?.generatedAt ?? "";
  const summary = timeline?.summary ?? { total: 0, critical: 0, warning: 0, info: 0 };
  const isInitialLoading = loading && timeline === null && !error;
  const isRefreshing = loading && timeline !== null;
  const visibleGroups = expanded ? groups : groups.slice(0, INCIDENT_PREVIEW_LIMIT);

  return (
    <DataSurface>
      <DataSurfaceHeader
        title={
          <span className="inline-flex flex-wrap items-center gap-2">
            <ShieldAlert className="size-4 text-primary" aria-hidden />
            <span>{t("overview.healthIncidentTitle")}</span>
            <Badge tone="neutral" dot={false}>
              {t("overview.healthIncidentWindowBadge", { hours: windowHours })}
            </Badge>
            {timeline && !error ? (
              <>
                <Badge tone={summary.critical > 0 ? "destructive" : "neutral"} dot={summary.critical > 0}>
                  {t("overview.healthIncidentSummaryCritical", { count: summary.critical })}
                </Badge>
                <Badge tone={summary.warning > 0 ? "warning" : "neutral"} dot={summary.warning > 0}>
                  {t("overview.healthIncidentSummaryWarning", { count: summary.warning })}
                </Badge>
                <Badge tone={summary.info > 0 ? "info" : "neutral"} dot={summary.info > 0}>
                  {t("overview.healthIncidentSummaryInfo", { count: summary.info })}
                </Badge>
              </>
            ) : null}
            {isRefreshing ? (
              <span
                className="inline-flex items-center gap-1 text-xs font-normal text-muted-foreground"
                role="status"
                aria-live="polite"
              >
                <Loader2 className="size-3 animate-spin" aria-hidden />
                {t("overview.refreshing")}
              </span>
            ) : null}
          </span>
        }
        description={
          <span className="inline-flex flex-wrap items-center gap-x-3 gap-y-1">
            <span>{t("overview.healthIncidentDesc", { hours: windowHours })}</span>
            {generatedAt ? (
              <span>
                {t("overview.generatedAtLabel")}
                <time dateTime={generatedAt} title={formatTime(generatedAt)}>
                  {formatTime(generatedAt)}
                </time>
              </span>
            ) : null}
          </span>
        }
        actions={
          <Button variant="outline" size="sm" onClick={onRetry} disabled={loading}>
            {t("common.refresh")}
          </Button>
        }
      />
      <DataSurfaceContent className="space-y-3">
        {isInitialLoading ? (
          <LoadingState
            className="rounded-lg border border-border bg-background/60"
            title={t("overview.healthIncidentLoading")}
            description={t("overview.healthIncidentLoadingDesc")}
            rows={2}
          />
        ) : null}

        {!isInitialLoading && error ? (
          <InlineAlert tone="warning" title={t("overview.healthIncidentErrorTitle")}>
            <div className="space-y-2">
              <p>{error}</p>
              <Button type="button" size="sm" variant="outline" onClick={onRetry} disabled={loading}>
                {t("common.retry")}
              </Button>
            </div>
          </InlineAlert>
        ) : null}

        {!isInitialLoading && !error && groups.length === 0 ? (
          <InlineAlert tone="info" title={t("overview.healthIncidentEmptyTitle")}>
            <div className="space-y-2">
              <p>{t("overview.healthIncidentEmptyDesc", { hours: windowHours, scope: scopeLabel })}</p>
              <div className="flex flex-wrap items-center gap-2">
                <Button asChild variant="outline" size="sm">
                  <Link to="/app/backups/overview">{t("overview.viewBackupHealth")}</Link>
                </Button>
                <Button asChild variant="ghost" size="sm">
                  <Link to="/app/policies">{t("overview.healthIncidentAction.view_policies")}</Link>
                </Button>
              </div>
            </div>
          </InlineAlert>
        ) : null}

        {!isInitialLoading && !error && groups.length > 0 ? (
          <div className="space-y-3" role="list" aria-label={t("overview.healthIncidentListAriaLabel")}>
            {visibleGroups.map((group) => {
              const primaryAction = resolveHealthIncidentAction(group);
              return (
                <div
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
                      <h3 className="text-sm font-semibold text-foreground">
                        {resourceLabel(t, group)}
                      </h3>
                      <p className="text-sm leading-relaxed text-muted-foreground">
                        {healthIncidentCauseSummary(group, t)}
                      </p>
                      {group.likelyCause.trim() ? (
                        <details className="text-sm text-muted-foreground">
                          <summary className="cursor-pointer">{t("overview.healthIncidentSourceDiagnostic")}</summary>
                          <p className="mt-2 whitespace-pre-wrap break-words">{group.likelyCause}</p>
                        </details>
                      ) : null}
                      <div className="flex flex-wrap gap-1.5">
                        {group.sourceTypes.map((source) => (
                          <Badge key={source} tone="neutral" dot={false} className="text-[11px]">
                            {sourceLabel(t, source)}
                          </Badge>
                        ))}
                      </div>
                    </div>
                    <Button asChild variant="ghost" size="sm" className="shrink-0 justify-start">
                      <Link to={primaryAction.href}>
                        {healthIncidentActionLabel(primaryAction, t)}
                        <ArrowRight className="size-3.5" aria-hidden />
                      </Link>
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>
        ) : null}

        {!isInitialLoading && !error && groups.length > INCIDENT_PREVIEW_LIMIT ? (
          <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
            <Button
              type="button"
              variant="outline"
              size="sm"
              aria-expanded={expanded}
              onClick={() => setExpanded((current) => !current)}
            >
              {expanded ? (
                <>
                  <ChevronUp className="size-3.5" aria-hidden />
                  {t("overview.healthIncidentCollapse")}
                </>
              ) : (
                <>
                  <ChevronDown className="size-3.5" aria-hidden />
                  {t("overview.healthIncidentExpandAll", { count: groups.length })}
                </>
              )}
            </Button>
            <Button asChild variant="ghost" size="sm">
              <Link to="/app/notifications">
                {t("overview.healthIncidentAction.view_notifications")}
                <ArrowRight className="size-3.5" aria-hidden />
              </Link>
            </Button>
          </div>
        ) : null}
      </DataSurfaceContent>
    </DataSurface>
  );
}

export function OverviewPage() {
  const { t } = useTranslation();
  const { token, role } = useAuth();
  const { overview, refreshVersion, fetchOverviewTraffic, fetchHealthIncidentTimeline } = useSharedContext();
  const { tasks, tasksLoading, tasksError, refreshTasks } = useTasksContext();

  useEffect(() => {
    void refreshTasks();
  }, [refreshTasks, refreshVersion]);

  const [trafficWindow, setTrafficWindow] = useState<OverviewTrafficWindow>("24h");
  const [trafficData, setTrafficData] = useState<OverviewTrafficSeries | null>(null);
  const [trafficLoading, setTrafficLoading] = useState(true);
  const [trafficError, setTrafficError] = useState<string | null>(null);
  const [trafficReloadKey, setTrafficReloadKey] = useState(0);

  const [incidentTimeline, setIncidentTimeline] = useState<HealthIncidentTimelineData | null>(null);
  const [incidentLoading, setIncidentLoading] = useState(true);
  const [incidentError, setIncidentError] = useState<string | null>(null);
  const [incidentReloadKey, setIncidentReloadKey] = useState(0);

  const [visibleLayers, setVisibleLayers] = useState({ throughput: true, activity: true, failures: true });
  const trafficRequestRef = useRef(0);
  const incidentRequestRef = useRef(0);

  const incidentScope = `${token}:${refreshVersion}:${incidentReloadKey}`;
  const trafficScope = `${token}:${refreshVersion}:${trafficWindow}:${trafficReloadKey}`;
  const [trackedToken, setTrackedToken] = useState(token);
  const [previousIncident, setPreviousIncident] = useState({ scope: incidentScope, fetch: fetchHealthIncidentTimeline });
  const [previousTraffic, setPreviousTraffic] = useState({ scope: trafficScope, fetch: fetchOverviewTraffic });

  if (trackedToken !== token) {
    setTrackedToken(token);
    setIncidentTimeline(null);
    setTrafficData(null);
  }
  if (previousIncident.scope !== incidentScope || previousIncident.fetch !== fetchHealthIncidentTimeline) {
    setPreviousIncident({ scope: incidentScope, fetch: fetchHealthIncidentTimeline });
    setIncidentLoading(true);
    setIncidentError(null);
  }
  if (previousTraffic.scope !== trafficScope || previousTraffic.fetch !== fetchOverviewTraffic) {
    setPreviousTraffic({ scope: trafficScope, fetch: fetchOverviewTraffic });
    setTrafficLoading(true);
    setTrafficError(null);
  }

  useEffect(() => {
    const controller = new AbortController();
    const requestId = incidentRequestRef.current + 1;
    incidentRequestRef.current = requestId;
    void fetchHealthIncidentTimeline({ windowHours: 72, signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted && incidentRequestRef.current === requestId) {
          setIncidentTimeline(normalizeIncidentTimeline(result));
          setIncidentError(null);
        }
      })
      .catch((error) => {
        if (controller.signal.aborted) {
          return;
        }
        if (incidentRequestRef.current === requestId) {
          setIncidentError(getErrorMessage(error, t("overview.healthIncidentLoadFailed")));
          setIncidentTimeline(null);
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
          setTrafficError(null);
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
  }, [fetchOverviewTraffic, refreshVersion, trafficWindow, trafficReloadKey, token]);

  const recentTasks = useMemo(() => sortRecentTasks(tasks, 5), [tasks]);

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
      ...(point.partial ? { partial: true } : {}),
    }));

    return {
      chartData,
      hasRealSamples: Boolean(trafficData?.hasRealSamples),
      truncated: Boolean(trafficData?.truncated),
      hasPartialBucket: points.some((point) => point.partial === true),
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
      yMaxLeft: computeThroughputMax(maxThroughput),
      yMaxRight: maxCount,
    };
  }, [chartMetrics.chartData, visibleLayers]);

  const scopeLabel = role === "operator" ? t("overview.scopeMyNodes") : t("overview.scopeAllNodes");

  return (
    <Stagger className="space-y-5">
      <Reveal><OverviewHero /></Reveal>

      <Reveal>
        <OverviewPosture
          activePolicies={overview.activePolicies}
          refreshVersion={refreshVersion}
        />
      </Reveal>

      <Reveal>
        <HealthIncidentTimelinePanel
          timeline={incidentTimeline}
          loading={incidentLoading}
          error={incidentError}
          scopeLabel={scopeLabel}
          onRetry={() => setIncidentReloadKey((current) => current + 1)}
        />
      </Reveal>

      <Reveal>
        <OverviewTrafficChart
          trafficWindow={trafficWindow}
          setTrafficWindow={setTrafficWindow}
          trafficLoading={trafficLoading}
          trafficError={trafficError}
          hasLoadedData={trafficData !== null}
          onRetry={() => setTrafficReloadKey((current) => current + 1)}
          chartMetrics={chartMetrics}
          visibleLayers={visibleLayers}
          setVisibleLayers={setVisibleLayers}
          yMaxLeft={yMaxLeft}
          yMaxRight={yMaxRight}
        />
      </Reveal>

      <Reveal>
        <OverviewRecentTasks
          tasks={tasks}
          recentTasks={recentTasks}
          loading={tasksLoading}
          error={tasksError}
          onRetry={() => { void refreshTasks(); }}
        />
      </Reveal>
    </Stagger>
  );
}
