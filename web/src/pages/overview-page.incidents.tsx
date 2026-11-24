import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { ArrowRight, Loader2, ShieldAlert } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DataSurface, DataSurfaceContent, DataSurfaceHeader } from "@/components/ui/data-surface";
import { InlineAlert } from "@/components/ui/inline-alert";
import { formatTime } from "@/lib/date-utils";
import type { HealthIncidentTimelineData } from "@/types/domain";
import {
  healthIncidentActionLabel,
  healthIncidentCauseSummary,
  relativeIncidentTime,
  resolveHealthIncidentAction,
  resourceLabel,
  severityLabel,
  severityTone,
} from "@/pages/overview-page.incidents.utils";

const INCIDENT_PREVIEW_LIMIT = 3;

export type OverviewHealthIncidentsProps = {
  timeline: HealthIncidentTimelineData | null;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
};

export function OverviewHealthIncidents({
  timeline,
  loading,
  error,
  onRetry,
}: OverviewHealthIncidentsProps) {
  const { t } = useTranslation();

  const groups = timeline?.groups ?? [];
  const windowHours = timeline?.windowHours ?? 72;
  const summary = timeline?.summary ?? { total: 0, critical: 0, warning: 0, info: 0 };
  const isRefreshing = loading && timeline !== null;
  const visibleGroups = groups.slice(0, INCIDENT_PREVIEW_LIMIT);

  return (
    <DataSurface className="flex h-full flex-col">
      <DataSurfaceHeader
        title={
          <span className="inline-flex flex-wrap items-center gap-1.5">
            <ShieldAlert className="size-4 text-primary" aria-hidden />
            <span>{t("overview.healthIncidentTitle")}</span>
            <Badge tone="neutral" dot={false}>
              {t("overview.healthIncidentWindowBadge", { hours: windowHours })}
            </Badge>
            {timeline && !error && summary.critical > 0 ? (
              <Badge tone="destructive">
                {t("overview.healthIncidentSummaryCritical", { count: summary.critical })}
              </Badge>
            ) : null}
            {timeline && !error && summary.warning > 0 ? (
              <Badge tone="warning">
                {t("overview.healthIncidentSummaryWarning", { count: summary.warning })}
              </Badge>
            ) : null}
            {timeline && !error && summary.info > 0 ? (
              <Badge tone="info">
                {t("overview.healthIncidentSummaryInfo", { count: summary.info })}
              </Badge>
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
        actions={
          <Button asChild variant="ghost" size="sm">
            <Link to="/app/notifications">
              {t("overview.healthIncidentViewMore")}
              <ArrowRight className="size-3.5" aria-hidden />
            </Link>
          </Button>
        }
      />
      <DataSurfaceContent className="flex-1 p-0">
        {error ? (
          <div className="p-4">
            <InlineAlert tone="warning" title={t("overview.healthIncidentErrorTitle")}>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span>{error}</span>
                <Button type="button" size="sm" variant="outline" onClick={onRetry} disabled={loading}>
                  {t("common.refresh")}
                </Button>
              </div>
            </InlineAlert>
          </div>
        ) : null}

        {!error && groups.length > 0 ? (
          <div className="divide-y divide-border" role="list" aria-label={t("overview.healthIncidentListAriaLabel")}>
            {visibleGroups.map((group) => {
              const primaryAction = resolveHealthIncidentAction(group);
              const resourceTitle = resourceLabel(t, group);
              return (
                <div key={group.id} role="listitem" className="space-y-1.5 px-4 py-3">
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                      <Badge tone={severityTone(group.severity)}>
                        {severityLabel(t, group.severity)}
                      </Badge>
                      <span className="text-xs text-muted-foreground tabular-nums">
                        {t("overview.healthIncidentEvents", { count: group.eventCount })}
                      </span>
                      <time
                        dateTime={group.lastSeenAt}
                        className="text-xs text-muted-foreground tabular-nums"
                        title={formatTime(group.lastSeenAt)}
                      >
                        {relativeIncidentTime(group.lastSeenAt)}
                      </time>
                    </div>
                    <Link
                      to={primaryAction.href}
                      className="inline-flex shrink-0 items-center gap-1 rounded-sm text-xs font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                    >
                      <span>{healthIncidentActionLabel(primaryAction, t)}</span>
                      <ArrowRight className="size-3" aria-hidden />
                    </Link>
                  </div>
                  <h3 className="truncate text-sm font-medium text-foreground" title={resourceTitle}>
                    {resourceTitle}
                  </h3>
                  <p className="text-xs leading-relaxed text-muted-foreground">
                    {healthIncidentCauseSummary(group, t)}
                  </p>
                  {group.likelyCause.trim() ? (
                    <details className="text-xs text-muted-foreground">
                      <summary className="cursor-pointer hover:text-foreground">
                        {t("overview.healthIncidentSourceDiagnostic")}
                      </summary>
                      <p className="mt-1.5 whitespace-pre-wrap break-words rounded bg-secondary/40 p-2 font-mono text-[11px]">
                        {group.likelyCause}
                      </p>
                    </details>
                  ) : null}
                </div>
              );
            })}
          </div>
        ) : null}
      </DataSurfaceContent>
    </DataSurface>
  );
}
