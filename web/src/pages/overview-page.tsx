import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import i18n from "@/i18n";
import { Stagger, Reveal } from "@/components/ui/reveal";
import { useSharedContext } from "@/context/shared-context.hooks";
import { useTasksContext } from "@/context/tasks-context.hooks";
import { useAuth } from "@/context/auth-context.hooks";
import { getErrorMessage } from "@/lib/utils";
import type {
  HealthIncidentTimelineData,
  OverviewTrafficSeries,
  OverviewTrafficWindow,
} from "@/types/domain";
import { OverviewHero } from "@/pages/overview-page.hero";
import { OverviewPosture } from "@/pages/overview-page.posture";
import { useOverviewPosture } from "@/pages/hooks/use-overview-posture";
import { OverviewTrafficChart } from "@/pages/overview-page.traffic";
import { computeThroughputMax } from "@/pages/overview-page.traffic.utils";
import { OverviewRecentTasks } from "@/pages/overview-page.recent-tasks";
import { sortRecentTasks } from "@/pages/overview-page.recent-tasks.utils";
import { OverviewHealthIncidents } from "@/pages/overview-page.incidents";
import { normalizeIncidentTimeline } from "@/pages/overview-page.incidents.utils";

const RECENT_TASKS_LIMIT = 8;

export function OverviewPage() {
  const { t } = useTranslation();
  const { token, role, username } = useAuth();
  const { overview, refreshVersion, fetchOverviewTraffic, fetchHealthIncidentTimeline } = useSharedContext();
  const { tasks, tasksLoading, tasksError, refreshTasks } = useTasksContext();
  const posture = useOverviewPosture({ token, refreshVersion });

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
    setIncidentError(null);
    setTrafficData(null);
    setTrafficError(null);
  }
  if (previousIncident.scope !== incidentScope || previousIncident.fetch !== fetchHealthIncidentTimeline) {
    setPreviousIncident({ scope: incidentScope, fetch: fetchHealthIncidentTimeline });
    setIncidentLoading(true);
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
          setIncidentError(getErrorMessage(error, i18n.t("overview.healthIncidentLoadFailed")));
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
          setTrafficError(getErrorMessage(error, i18n.t("overview.trafficLoadFailed")));
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
  }, [fetchOverviewTraffic, refreshVersion, trafficWindow, trafficReloadKey, token]);

  const recentTasks = useMemo(() => sortRecentTasks(tasks, RECENT_TASKS_LIMIT), [tasks]);

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
  const incidentCount = incidentTimeline?.groups.length ?? 0;
  const showIncidentPanel = Boolean(incidentError) || incidentCount > 0;

  return (
    <Stagger className="space-y-5">
      <Reveal>
        <OverviewHero username={username} scopeLabel={scopeLabel} updatedAt={posture.generatedAt} />
      </Reveal>

      <Reveal>
        <OverviewPosture
          activePolicies={overview.activePolicies}
          confidence={posture.confidence}
          health={posture.health}
          loading={posture.loading}
          confidenceError={posture.confidenceError}
          healthError={posture.healthError}
          onRetry={posture.retry}
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
        <div className={showIncidentPanel ? "grid grid-cols-1 gap-5 xl:grid-cols-12 xl:items-stretch" : ""}>
          <div className={showIncidentPanel ? "flex min-w-0 flex-col xl:col-span-7" : "min-w-0"}>
            <OverviewRecentTasks
              tasks={tasks}
              recentTasks={recentTasks}
              loading={tasksLoading}
              error={tasksError}
              onRetry={() => { void refreshTasks(); }}
              compact={showIncidentPanel}
            />
          </div>
          {showIncidentPanel ? (
            <div className="flex min-w-0 flex-col xl:col-span-5">
              <OverviewHealthIncidents
                timeline={incidentTimeline}
                loading={incidentLoading}
                error={incidentError}
                onRetry={() => setIncidentReloadKey((current) => current + 1)}
              />
            </div>
          ) : null}
        </div>
      </Reveal>
    </Stagger>
  );
}
