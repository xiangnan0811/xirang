import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { ArrowRight, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
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
  onGeneratedAtChange?: (generatedAt: string) => void;
};

type BreakdownDot = {
  key: string;
  label: string;
  count: number;
  dotClass: string;
  textClass: string;
};

export function OverviewPosture({
  activePolicies,
  refreshVersion,
  onGeneratedAtChange,
}: OverviewPostureProps) {
  const { t } = useTranslation();
  const { token } = useAuth();
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

  const generatedAt = getPostureGeneratedAt(confidence, health);
  useEffect(() => {
    onGeneratedAtChange?.(generatedAt ?? "");
  }, [generatedAt, onGeneratedAtChange]);

  const hasAnyData = Boolean(confidence || health);
  const hasError = Boolean(confidenceError || healthError);
  const allErrored = Boolean(confidenceError && healthError);
  const isInitialLoading = loading && !hasAnyData && !hasError;
  const isRefreshing = loading && hasAnyData;

  const latestVerifiedAt = getLatestVerifiedBackupTime(confidence, health);
  const locale = getLocale().startsWith("zh") ? "zh" : "en";
  const combinedErrors = [confidenceError, healthError].filter(Boolean).join(" · ");

  const confidenceBreakdown: BreakdownDot[] = confidence
    ? [
        {
          key: "at_risk",
          label: t("backupConfidence.status.at_risk"),
          count: confidence.summary.atRisk,
          dotClass: "bg-destructive",
          textClass: "text-[hsl(var(--destructive-text))]",
        },
        {
          key: "warning",
          label: t("backupConfidence.status.warning"),
          count: confidence.summary.warning,
          dotClass: "bg-warning",
          textClass: "text-[hsl(var(--warning-text))]",
        },
        {
          key: "insufficient",
          label: t("backupConfidence.status.insufficient"),
          count: confidence.summary.insufficient,
          dotClass: "bg-info",
          textClass: "text-[hsl(var(--info-text))]",
        },
      ].filter((item) => item.count > 0)
    : [];

  const staleFooter = (() => {
    if (!health) return "—";
    if (health.summary.totalNodes === 0) return t("overview.postureNoNodes");
    if (health.staleNodes.length === 0) return t("overview.postureAllNodesFresh");
    if (health.summary.neverBackedUp > 0 && health.summary.stale48h > 0) {
      return t("overview.postureStaleBreakdown", {
        never: health.summary.neverBackedUp,
        stale: health.summary.stale48h,
      });
    }
    if (health.summary.neverBackedUp > 0) {
      return t("overview.postureStaleNeverOnly", { never: health.summary.neverBackedUp });
    }
    return t("overview.postureStale48hOnly", { stale: health.summary.stale48h });
  })();

  const handleRetry = () => {
    setReloadKey((current) => current + 1);
  };

  const unverified = confidence ? isConfidenceUnverified(confidence) : false;

  return (
    <section aria-label={t("overview.postureTitle")} className="space-y-3">
      <h2 className="sr-only">{t("overview.postureTitle")}</h2>

      {hasError ? (
        <InlineAlert tone="warning" title={t("overview.postureErrorTitle")}>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span>{combinedErrors}</span>
            <Button type="button" size="sm" variant="outline" onClick={handleRetry} disabled={loading}>
              {t("common.retry")}
            </Button>
          </div>
        </InlineAlert>
      ) : null}

      {isInitialLoading ? (
        <div
          className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4"
          role="status"
          aria-label={t("overview.postureLoading")}
        >
          {[0, 1, 2, 3].map((slot) => (
            <div
              key={slot}
              className="h-[104px] animate-pulse rounded-lg border border-border bg-card p-4 shadow-sm"
            >
              <div className="h-3 w-20 rounded bg-secondary" />
              <div className="mt-4 h-6 w-24 rounded bg-secondary" />
              <div className="mt-3 h-3 w-32 rounded bg-secondary/70" />
            </div>
          ))}
        </div>
      ) : !allErrored ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          {/* Card 1: Backup Confidence */}
          <Link
            to="/app/backups/overview"
            className="group flex flex-col justify-between rounded-lg border border-border bg-card p-4 shadow-sm transition-colors hover:border-primary/40 hover:bg-accent/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          >
            <div className="flex items-center justify-between gap-2 text-xs font-medium text-muted-foreground">
              <span className="inline-flex items-center gap-1.5">
                <span>{t("overview.postureConfidenceTitle")}</span>
                {isRefreshing ? <Loader2 className="size-3 animate-spin" aria-hidden /> : null}
              </span>
              <ArrowRight
                className="size-3.5 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 group-hover:text-primary"
                aria-hidden
              />
            </div>

            <div className="mt-3 flex items-baseline gap-1.5">
              {!confidence ? (
                <span className="text-2xl font-semibold tabular-nums leading-none text-muted-foreground">—</span>
              ) : unverified ? (
                <span className="text-xl font-semibold leading-none text-foreground">
                  {t("overview.postureUnverifiedTitle")}
                </span>
              ) : (
                <>
                  <span className="text-2xl font-semibold tabular-nums leading-none text-foreground">
                    {confidence.summary.healthy}
                  </span>
                  <span className="text-xs font-medium tabular-nums text-muted-foreground">
                    / {confidence.summary.total} {t("overview.postureConfidenceTrustedUnit")}
                  </span>
                </>
              )}
            </div>

            <div className="mt-2.5 flex min-h-4 flex-wrap items-center gap-x-2.5 gap-y-1 text-xs">
              {!confidence ? (
                <span className="text-muted-foreground">{t("overview.unavailable")}</span>
              ) : confidenceBreakdown.length > 0 ? (
                confidenceBreakdown.map((item) => (
                  <span key={item.key} className={cn("inline-flex items-center gap-1 tabular-nums", item.textClass)}>
                    <span className={cn("size-1.5 rounded-full", item.dotClass)} aria-hidden />
                    {item.label} {item.count}
                  </span>
                ))
              ) : confidence.summary.total > 0 ? (
                <span className="text-[hsl(var(--success-text))]">
                  {t("overview.postureConfidenceAllTrusted")}
                </span>
              ) : (
                <span className="text-muted-foreground">—</span>
              )}
            </div>
          </Link>

          {/* Card 2: Latest Verified Backup */}
          <div className="flex flex-col justify-between rounded-lg border border-border bg-card p-4 shadow-sm">
            <div className="text-xs font-medium text-muted-foreground">
              {t("overview.postureLatestVerifiedBackup")}
            </div>
            <div className="mt-3 text-xl font-semibold leading-none text-foreground tabular-nums">
              {latestVerifiedAt ? formatRelativeTime(latestVerifiedAt, locale) : t("overview.postureNoVerifiedBackup")}
            </div>
            <div className="mt-2.5 min-h-4 text-xs tabular-nums text-muted-foreground">
              {latestVerifiedAt ? (
                <time dateTime={latestVerifiedAt} title={formatTime(latestVerifiedAt)}>
                  {formatTime(latestVerifiedAt)}
                </time>
              ) : (
                "—"
              )}
            </div>
          </div>

          {/* Card 3: Stale / Unverified Nodes */}
          <div className="flex flex-col justify-between rounded-lg border border-border bg-card p-4 shadow-sm">
            <div className="text-xs font-medium text-muted-foreground">
              {t("overview.postureStaleNodes")}
            </div>
            <div className="mt-3 flex items-baseline gap-1">
              {health ? (
                <>
                  <span
                    className={cn(
                      "text-2xl font-semibold tabular-nums leading-none",
                      health.staleNodes.length > 0
                        ? "text-[hsl(var(--warning-text))]"
                        : "text-foreground",
                    )}
                  >
                    {health.staleNodes.length}
                  </span>
                  {health.summary.totalNodes > 0 ? (
                    <span className="text-xs font-medium tabular-nums text-muted-foreground">
                      / {health.summary.totalNodes}
                    </span>
                  ) : null}
                </>
              ) : (
                <span className="text-2xl font-semibold tabular-nums leading-none text-muted-foreground">—</span>
              )}
            </div>
            <div
              className={cn(
                "mt-2.5 min-h-4 text-xs tabular-nums",
                health && health.staleNodes.length > 0
                  ? "text-[hsl(var(--warning-text))]"
                  : "text-muted-foreground",
              )}
            >
              {staleFooter}
            </div>
          </div>

          {/* Card 4: Active Policies (+ degraded alert count when > 0) */}
          <Link
            to="/app/policies"
            className="group flex flex-col justify-between rounded-lg border border-border bg-card p-4 shadow-sm transition-colors hover:border-primary/40 hover:bg-accent/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          >
            <div className="flex items-center justify-between gap-2 text-xs font-medium text-muted-foreground">
              <span>{t("overview.postureActivePolicies")}</span>
              <ArrowRight
                className="size-3.5 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 group-hover:text-primary"
                aria-hidden
              />
            </div>
            <div className="mt-3 text-2xl font-semibold tabular-nums leading-none text-foreground">
              {activePolicies}
            </div>
            <div className="mt-2.5 min-h-4 text-xs">
              {health && health.summary.policiesDegraded > 0 ? (
                <span className="font-medium text-[hsl(var(--destructive-text))] tabular-nums">
                  {t("overview.postureDegradedCount", { count: health.summary.policiesDegraded })}
                </span>
              ) : (
                <span className="text-muted-foreground group-hover:text-primary transition-colors">
                  {t("overview.postureActivePoliciesHint")}
                </span>
              )}
            </div>
          </Link>
        </div>
      ) : null}
    </section>
  );
}
