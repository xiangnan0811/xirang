import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import i18n from "@/i18n";
import { Stagger, Reveal } from "@/components/ui/reveal";
import { useSharedContext } from "@/context/shared-context.hooks";
import { useTasksContext } from "@/context/tasks-context.hooks";
import { useAuth } from "@/context/auth-context.hooks";
import { getAuthSessionGeneration } from "@/lib/api/core";
import { getErrorMessage } from "@/lib/utils";
import type {
  HealthIncidentTimelineData,
  OverviewTrafficSeries,
  OverviewTrafficWindow,
} from "@/types/domain";
import { OverviewHero } from "@/pages/overview-page.hero";
import { OverviewPosture } from "@/pages/overview-page.posture";
import { useOverviewPosture } from "@/pages/hooks/use-overview-posture";
import {
  isCurrentOverviewRequest,
  useOverviewAuthScope,
  useOverviewRequestKey,
} from "@/pages/hooks/use-overview-request-key";
import { OverviewTrafficChart } from "@/pages/overview-page.traffic";
import { buildTrafficChartMetrics, computeTrafficAxisMax } from "@/pages/overview-page.traffic.utils";
import { OverviewRecentTasks } from "@/pages/overview-page.recent-tasks";
import { sortRecentTasks } from "@/pages/overview-page.recent-tasks.utils";
import { OverviewHealthIncidents } from "@/pages/overview-page.incidents";
import { normalizeIncidentTimeline } from "@/pages/overview-page.incidents.utils";

const RECENT_TASKS_LIMIT = 8;

export function OverviewPage() {
  const { t } = useTranslation();
  const { token, role, username } = useAuth();
  const {
    overview,
    refreshVersion,
    overviewPollVersion = 0,
    fetchOverviewTraffic,
    fetchHealthIncidentTimeline,
  } = useSharedContext();
  const { tasks, tasksLoading, tasksError, refreshTasks } = useTasksContext();
  const posture = useOverviewPosture({ token, refreshVersion, pollVersion: overviewPollVersion });
  const auth = useOverviewAuthScope(token);

  // The tasks domain coalesces same-token in-flight refreshes, so scheduled pulses cannot pile up here.
  useEffect(() => {
    void refreshTasks();
  }, [refreshTasks, refreshVersion, overviewPollVersion]);

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
  const incidentRequestKey = useOverviewRequestKey(
    `${auth.scope}:${refreshVersion}:${incidentReloadKey}`,
    overviewPollVersion,
    incidentLoading,
  );
  const trafficRequestKey = useOverviewRequestKey(
    `${auth.scope}:${refreshVersion}:${trafficWindow}:${trafficReloadKey}`,
    overviewPollVersion,
    trafficLoading,
  );
  const [trackedIdentity, setTrackedIdentity] = useState(auth.identity);
  const [previousIncident, setPreviousIncident] = useState({ key: incidentRequestKey, fetch: fetchHealthIncidentTimeline });
  const [previousTraffic, setPreviousTraffic] = useState({ key: trafficRequestKey, fetch: fetchOverviewTraffic });

  if (trackedIdentity !== auth.identity) {
    setTrackedIdentity(auth.identity);
    setIncidentTimeline(null);
    setIncidentError(null);
    setTrafficData(null);
    setTrafficError(null);
  }
  if (previousIncident.key !== incidentRequestKey || previousIncident.fetch !== fetchHealthIncidentTimeline) {
    setPreviousIncident({ key: incidentRequestKey, fetch: fetchHealthIncidentTimeline });
    setIncidentLoading(true);
  }
  if (previousTraffic.key !== trafficRequestKey || previousTraffic.fetch !== fetchOverviewTraffic) {
    setPreviousTraffic({ key: trafficRequestKey, fetch: fetchOverviewTraffic });
    setTrafficLoading(true);
    setTrafficError(null);
  }

  useEffect(() => {
    if (auth.transitioning) {
      return;
    }
    const controller = new AbortController();
    const generation = getAuthSessionGeneration();
    void fetchHealthIncidentTimeline({ windowHours: 72, signal: controller.signal })
      .then(
        (result) => {
          if (isCurrentOverviewRequest(controller.signal, generation)) {
            setIncidentTimeline(normalizeIncidentTimeline(result));
            setIncidentError(null);
          }
        },
        (error: unknown) => {
          if (isCurrentOverviewRequest(controller.signal, generation)) {
            setIncidentError(getErrorMessage(error, i18n.t("overview.healthIncidentLoadFailed")));
            setIncidentTimeline(null);
          }
        },
      )
      .finally(() => {
        if (isCurrentOverviewRequest(controller.signal, generation)) {
          setIncidentLoading(false);
        }
      });

    return () => {
      controller.abort();
    };
  }, [auth.transitioning, fetchHealthIncidentTimeline, incidentRequestKey]);

  useEffect(() => {
    if (auth.transitioning) {
      return;
    }
    const controller = new AbortController();
    const generation = getAuthSessionGeneration();
    void fetchOverviewTraffic(trafficWindow, { signal: controller.signal })
      .then(
        (result) => {
          if (isCurrentOverviewRequest(controller.signal, generation)) {
            setTrafficData(result);
            setTrafficError(null);
          }
        },
        (error: unknown) => {
          if (isCurrentOverviewRequest(controller.signal, generation)) {
            setTrafficError(getErrorMessage(error, i18n.t("overview.trafficLoadFailed")));
            setTrafficData(null);
          }
        },
      )
      .finally(() => {
        if (isCurrentOverviewRequest(controller.signal, generation)) {
          setTrafficLoading(false);
        }
      });

    return () => {
      controller.abort();
    };
  }, [auth.transitioning, fetchOverviewTraffic, trafficRequestKey, trafficWindow]);

  const recentTasks = useMemo(() => sortRecentTasks(tasks, RECENT_TASKS_LIMIT), [tasks]);

  const chartMetrics = useMemo(() => buildTrafficChartMetrics(trafficData), [trafficData]);
  const { yMaxLeft, yMaxRight } = useMemo(
    () => computeTrafficAxisMax(chartMetrics.chartData, visibleLayers),
    [chartMetrics.chartData, visibleLayers],
  );

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
          confidenceLoading={posture.confidenceLoading}
          healthLoading={posture.healthLoading}
          confidenceError={posture.confidenceError}
          healthError={posture.healthError}
          onRetry={posture.retry}
        />
      </Reveal>

      <Reveal>
        <OverviewTrafficChart
          trafficWindow={trafficWindow}
          displayedWindow={trafficData?.window}
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
