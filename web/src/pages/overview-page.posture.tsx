import { useTranslation } from "react-i18next";
import { ArrowRight, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { StatCardsSection, type StatCardItem } from "@/components/ui/stat-cards-section";
import { formatRelativeTime, formatTime } from "@/lib/date-utils";
import { cn, getLocale } from "@/lib/utils";
import type { BackupConfidenceData, BackupHealthData } from "@/types/domain";
import {
  getLatestVerifiedBackupTime,
  isConfidenceUnverified,
} from "@/pages/overview-page.posture.utils";

export type OverviewPostureProps = {
  activePolicies: number;
  confidence: BackupConfidenceData | null;
  health: BackupHealthData | null;
  loading: boolean;
  confidenceError: string | null;
  healthError: string | null;
  onRetry: () => void;
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
  confidence,
  health,
  loading,
  confidenceError,
  healthError,
  onRetry,
}: OverviewPostureProps) {
  const { t } = useTranslation();

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

  const unverified = confidence ? isConfidenceUnverified(confidence) : false;

  const postureItems: StatCardItem[] = [
    {
      id: "confidence",
      title: t("overview.postureConfidenceTitle"),
      href: "/app/backups/overview",
      icon: (
        <span className="inline-flex items-center gap-1.5">
          {isRefreshing ? <Loader2 className="size-3 animate-spin" aria-hidden /> : null}
          <ArrowRight
            className="size-3.5 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 group-hover:text-primary"
            aria-hidden
          />
        </span>
      ),
      value: !confidence
        ? "—"
        : unverified
          ? t("overview.postureUnverifiedTitle")
          : confidence.summary.healthy,
      unit:
        confidence && !unverified
          ? `/ ${confidence.summary.total} ${t("overview.postureConfidenceTrustedUnit")}`
          : undefined,
      valueClassName: !confidence
        ? "text-muted-foreground"
        : unverified
          ? "text-xl"
          : undefined,
      tone:
        confidence && confidenceBreakdown.length === 0 && confidence.summary.total > 0
          ? "success"
          : undefined,
      description: !confidence ? (
        t("overview.unavailable")
      ) : confidenceBreakdown.length > 0 ? (
        <span className="flex flex-wrap items-center gap-x-2.5 gap-y-1 font-normal">
          {confidenceBreakdown.map((item) => (
            <span key={item.key} className={cn("inline-flex items-center gap-1 tabular-nums", item.textClass)}>
              <span className={cn("size-1.5 rounded-full", item.dotClass)} aria-hidden />
              {item.label} {item.count}
            </span>
          ))}
        </span>
      ) : confidence.summary.total > 0 ? (
        t("overview.postureConfidenceAllTrusted")
      ) : (
        "—"
      ),
    },
    {
      id: "latest-verified",
      title: t("overview.postureLatestVerifiedBackup"),
      value: latestVerifiedAt
        ? formatRelativeTime(latestVerifiedAt, locale)
        : t("overview.postureNoVerifiedBackup"),
      valueClassName: "text-xl",
      description: latestVerifiedAt ? (
        <time dateTime={latestVerifiedAt} title={formatTime(latestVerifiedAt)} className="font-normal tabular-nums">
          {formatTime(latestVerifiedAt)}
        </time>
      ) : (
        "—"
      ),
    },
    {
      id: "stale-nodes",
      title: t("overview.postureStaleNodes"),
      value: health ? health.staleNodes.length : "—",
      unit: health && health.summary.totalNodes > 0 ? `/ ${health.summary.totalNodes}` : undefined,
      valueClassName: !health
        ? "text-muted-foreground"
        : health.staleNodes.length > 0
          ? "text-[hsl(var(--warning-text))]"
          : undefined,
      tone: health && health.staleNodes.length > 0 ? "warning" : undefined,
      description: <span className="tabular-nums">{staleFooter}</span>,
    },
    {
      id: "active-policies",
      title: t("overview.postureActivePolicies"),
      href: "/app/policies",
      icon: (
        <ArrowRight
          className="size-3.5 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 group-hover:text-primary"
          aria-hidden
        />
      ),
      value: activePolicies,
      tone: health && health.summary.policiesDegraded > 0 ? "destructive" : undefined,
      description:
        health && health.summary.policiesDegraded > 0 ? (
          <span className="tabular-nums">
            {t("overview.postureDegradedCount", { count: health.summary.policiesDegraded })}
          </span>
        ) : (
          <span className="font-normal transition-colors group-hover:text-primary">
            {t("overview.postureActivePoliciesHint")}
          </span>
        ),
    },
  ];

  return (
    <section aria-label={t("overview.postureTitle")} className="space-y-3">
      <h2 className="sr-only">{t("overview.postureTitle")}</h2>

      {hasError ? (
        <InlineAlert tone="warning" title={t("overview.postureErrorTitle")}>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span>{combinedErrors}</span>
            <Button type="button" size="sm" variant="outline" onClick={onRetry} disabled={loading}>
              {t("common.retry")}
            </Button>
          </div>
        </InlineAlert>
      ) : null}

      {isInitialLoading ? (
        <LoadingState
          rows={2}
          title={t("overview.postureLoading")}
          className="rounded-lg border border-border bg-card shadow-sm"
        />
      ) : !allErrored ? (
        <StatCardsSection
          as="div"
          items={postureItems}
          className="gap-3"
          cardClassName="group flex flex-col justify-between"
        />
      ) : null}
    </section>
  );
}
