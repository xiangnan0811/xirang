import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { ArrowRight, Loader2, ShieldCheck } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DataSurface, DataSurfaceContent, DataSurfaceHeader } from "@/components/ui/data-surface";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import { formatRelativeTime, formatTime } from "@/lib/date-utils";
import { cn, getErrorMessage, getLocale } from "@/lib/utils";
import type { BackupConfidenceData, BackupHealthData } from "@/types/domain";
import {
  getLatestVerifiedBackupTime,
  getPostureGeneratedAt,
  isConfidenceUnverified,
} from "@/pages/overview-page.posture.utils";

export type OverviewPostureProps = {
  activePolicies: number;
  refreshVersion: number;
};

type ConfidenceTone = "success" | "warning" | "destructive" | "info" | "neutral";

function ConfidenceCountPill({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone: ConfidenceTone;
}) {
  return (
    <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-background/60 px-3 py-2">
      <Badge tone={tone} dot={false}>
        {label}
      </Badge>
      <span className="text-base font-semibold tabular-nums text-foreground">{value}</span>
    </div>
  );
}

export function OverviewPosture({ activePolicies, refreshVersion }: OverviewPostureProps) {
  const { t } = useTranslation();
  const { token, role } = useAuth();
  const demoModeEnabled = import.meta.env.VITE_ENABLE_DEMO_MODE === "true";

  const [confidence, setConfidence] = useState<BackupConfidenceData | null>(null);
  const [health, setHealth] = useState<BackupHealthData | null>(null);
  const [loading, setLoading] = useState(Boolean(token) || demoModeEnabled);
  const [confidenceError, setConfidenceError] = useState<string | null>(null);
  const [healthError, setHealthError] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);

  const requestRef = useRef(0);
  const [trackedToken, setTrackedToken] = useState(token);
  const fetchScope = `${token ?? ""}:${refreshVersion}:${reloadKey}`;
  const [trackedScope, setTrackedScope] = useState(fetchScope);

  if (trackedToken !== token) {
    setTrackedToken(token);
    setTrackedScope(fetchScope);
    setConfidence(null);
    setHealth(null);
    setConfidenceError(null);
    setHealthError(null);
    setLoading(Boolean(token) || demoModeEnabled);
  } else if (trackedScope !== fetchScope) {
    setTrackedScope(fetchScope);
    setConfidenceError(null);
    setHealthError(null);
    setLoading(Boolean(token) || demoModeEnabled);
  }

  useEffect(() => {
    const requestId = requestRef.current + 1;
    requestRef.current = requestId;

    if (!token) {
      if (!demoModeEnabled) {
        return;
      }
      let cancelled = false;
      import("@/data/mock")
        .then((mocks) => {
          if (!cancelled && requestRef.current === requestId) {
            setConfidence(mocks.buildMockBackupConfidence());
            setHealth(mocks.buildMockBackupHealth());
            setConfidenceError(null);
            setHealthError(null);
          }
        })
        .catch((error) => {
          if (!cancelled && requestRef.current === requestId) {
            const msg = getErrorMessage(error, t("overview.postureLoadFailed"));
            setConfidenceError(msg);
            setHealthError(msg);
          }
        })
        .finally(() => {
          if (!cancelled && requestRef.current === requestId) {
            setLoading(false);
          }
        });

      return () => {
        cancelled = true;
      };
    }

    const controller = new AbortController();
    void Promise.allSettled([
      apiClient.getBackupConfidence(token, { signal: controller.signal }),
      apiClient.getBackupHealth(token, { signal: controller.signal }),
    ])
      .then(([confidenceResult, healthResult]) => {
        if (controller.signal.aborted || requestRef.current !== requestId) {
          return;
        }
        if (confidenceResult.status === "fulfilled") {
          setConfidence(confidenceResult.value);
          setConfidenceError(null);
        } else {
          setConfidence(null);
          setConfidenceError(getErrorMessage(confidenceResult.reason, t("backupConfidence.loadFailed")));
        }

        if (healthResult.status === "fulfilled") {
          setHealth(healthResult.value);
          setHealthError(null);
        } else {
          setHealth(null);
          setHealthError(getErrorMessage(healthResult.reason, t("backupHealth.loadFailed")));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted && requestRef.current === requestId) {
          setLoading(false);
        }
      });

    return () => {
      controller.abort();
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps -- t is stable from react-i18next
  }, [token, demoModeEnabled, refreshVersion, reloadKey]);

  const scopeLabel = role === "operator" ? t("overview.scopeMyNodes") : t("overview.scopeAllNodes");
  const hasAnyData = Boolean(confidence || health);
  const hasError = Boolean(confidenceError || healthError);
  const allErrored = Boolean(confidenceError && healthError);
  const isInitialLoading = loading && !hasAnyData && !hasError;
  const isRefreshing = loading && hasAnyData;

  const generatedAt = getPostureGeneratedAt(confidence, health);
  const latestVerifiedAt = getLatestVerifiedBackupTime(confidence, health);
  const locale = getLocale().startsWith("zh") ? "zh" : "en";
  const combinedErrors = [confidenceError, healthError].filter(Boolean).join(" · ");

  const staleDescription = (() => {
    if (!health) return t("overview.unavailable");
    if (health.summary.totalNodes === 0) return t("overview.postureNoNodes");
    if (health.staleNodes.length === 0) return t("overview.postureAllNodesFresh");
    return t("overview.postureStaleBreakdown", {
      never: health.summary.neverBackedUp,
      stale: health.summary.stale48h,
    });
  })();

  const degradedDescription = (() => {
    if (!health) return t("overview.unavailable");
    if (health.summary.policiesDegraded > 0) return t("overview.postureDegradedWarning");
    if (health.summary.policiesHealthy === 0 && activePolicies === 0) return t("overview.postureNoActivePolicies");
    return t("overview.postureDegradedNone");
  })();

  const handleRetry = () => {
    setReloadKey((current) => current + 1);
  };

  return (
    <DataSurface>
      <DataSurfaceHeader
        title={
          <span className="inline-flex flex-wrap items-center gap-2">
            <ShieldCheck className="size-4 text-primary" aria-hidden />
            <span>{t("overview.postureTitle")}</span>
            <Badge tone="neutral" dot={false}>
              {scopeLabel}
            </Badge>
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
            <span>{t("overview.postureDesc")}</span>
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
          <Button asChild variant="outline" size="sm">
            <Link to="/app/backups/overview">
              {t("overview.viewBackupHealth")}
              <ArrowRight className="size-3.5" aria-hidden />
            </Link>
          </Button>
        }
      />

      <DataSurfaceContent className="space-y-4">
        {isInitialLoading ? (
          <LoadingState
            className="rounded-lg border border-border bg-background/60"
            title={t("overview.postureLoading")}
            rows={2}
          />
        ) : null}

        {hasError ? (
          <InlineAlert tone="warning" title={t("overview.postureErrorTitle")}>
            <div className="space-y-2">
              <p>{combinedErrors}</p>
              <Button type="button" size="sm" variant="outline" onClick={handleRetry} disabled={loading}>
                {t("common.retry")}
              </Button>
            </div>
          </InlineAlert>
        ) : null}

        {!isInitialLoading && !allErrored ? (
          <>
            {confidence ? (
              <div className="space-y-3">
                {isConfidenceUnverified(confidence) ? (
                  <InlineAlert tone="info" live={false} title={t("overview.postureUnverifiedTitle")}>
                    <span>
                      {confidence.summary.total === 0
                        ? t("overview.postureUnverifiedNoPolicies")
                        : t("overview.postureUnverifiedInsufficientOnly")}
                    </span>
                  </InlineAlert>
                ) : null}
                <div
                  className="grid grid-cols-2 gap-2 sm:grid-cols-4"
                  aria-label={t("overview.postureConfidenceAriaLabel")}
                >
                  <ConfidenceCountPill
                    label={t("backupConfidence.status.healthy")}
                    value={confidence.summary.healthy}
                    tone={confidence.summary.healthy > 0 ? "success" : "neutral"}
                  />
                  <ConfidenceCountPill
                    label={t("backupConfidence.status.warning")}
                    value={confidence.summary.warning}
                    tone={confidence.summary.warning > 0 ? "warning" : "neutral"}
                  />
                  <ConfidenceCountPill
                    label={t("backupConfidence.status.at_risk")}
                    value={confidence.summary.atRisk}
                    tone={confidence.summary.atRisk > 0 ? "destructive" : "neutral"}
                  />
                  <ConfidenceCountPill
                    label={t("backupConfidence.status.insufficient")}
                    value={confidence.summary.insufficient}
                    tone={confidence.summary.insufficient > 0 ? "info" : "neutral"}
                  />
                </div>
              </div>
            ) : null}

            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <Link
                to="/app/policies"
                className="group flex flex-col justify-between rounded-lg border border-border bg-background/60 p-3 transition-colors hover:border-primary/40 hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              >
                <div className="flex items-center justify-between gap-2 text-mini font-medium text-muted-foreground">
                  <span>{t("overview.postureActivePolicies")}</span>
                  <ArrowRight
                    className="size-3.5 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 group-hover:text-primary"
                    aria-hidden
                  />
                </div>
                <div className="mt-2 text-xl font-semibold tabular-nums leading-none text-foreground">
                  {activePolicies}
                </div>
                <div className="mt-2 text-xs text-primary">{t("overview.postureActivePoliciesHint")}</div>
              </Link>

              <div className="flex flex-col justify-between rounded-lg border border-border bg-background/60 p-3">
                <div className="text-mini font-medium text-muted-foreground">
                  {t("overview.postureLatestVerifiedBackup")}
                </div>
                <div className="mt-2 text-sm font-semibold text-foreground">
                  {latestVerifiedAt ? (
                    <time dateTime={latestVerifiedAt} title={formatTime(latestVerifiedAt)} className="tabular-nums">
                      {formatTime(latestVerifiedAt)}
                    </time>
                  ) : (
                    <span>{t("overview.postureNoVerifiedBackup")}</span>
                  )}
                </div>
                <div className="mt-2 text-xs text-muted-foreground">
                  {latestVerifiedAt
                    ? formatRelativeTime(latestVerifiedAt, locale)
                    : t("overview.postureNoVerifiedBackupHint")}
                </div>
              </div>

              <div className="flex flex-col justify-between rounded-lg border border-border bg-background/60 p-3">
                <div className="text-mini font-medium text-muted-foreground">
                  {t("overview.postureStaleNodes")}
                </div>
                <div className="mt-2 text-xl font-semibold tabular-nums leading-none text-foreground">
                  {health ? (
                    <>
                      {health.staleNodes.length}
                      {health.summary.totalNodes > 0 ? (
                        <span className="ml-1 text-xs font-normal text-muted-foreground">
                          / {health.summary.totalNodes}
                        </span>
                      ) : null}
                    </>
                  ) : (
                    "—"
                  )}
                </div>
                <div
                  className={cn(
                    "mt-2 text-xs",
                    health && health.staleNodes.length > 0
                      ? "text-[hsl(var(--warning-text))]"
                      : "text-muted-foreground",
                  )}
                >
                  {staleDescription}
                </div>
              </div>

              <div className="flex flex-col justify-between rounded-lg border border-border bg-background/60 p-3">
                <div className="text-mini font-medium text-muted-foreground">
                  {t("overview.postureDegradedPolicies")}
                </div>
                <div className="mt-2 text-xl font-semibold tabular-nums leading-none text-foreground">
                  {health ? health.summary.policiesDegraded : "—"}
                </div>
                <div
                  className={cn(
                    "mt-2 text-xs",
                    health && health.summary.policiesDegraded > 0
                      ? "text-[hsl(var(--destructive-text))]"
                      : "text-muted-foreground",
                  )}
                >
                  {degradedDescription}
                </div>
              </div>
            </div>
          </>
        ) : null}
      </DataSurfaceContent>
    </DataSurface>
  );
}
