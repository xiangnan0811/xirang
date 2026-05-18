import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, CheckCircle2, HelpCircle, ShieldCheck, ShieldQuestion } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { BackupConfidenceData, BackupConfidenceItem, BackupConfidenceStatus } from "@/types/domain";

const statusTone: Record<BackupConfidenceStatus, "success" | "warning" | "destructive" | "info"> = {
  healthy: "success",
  warning: "warning",
  at_risk: "destructive",
  insufficient: "info",
};

function StatusIcon({ status }: { status: BackupConfidenceStatus }) {
  switch (status) {
    case "healthy":
      return <CheckCircle2 className="size-4" aria-hidden="true" />;
    case "warning":
      return <AlertTriangle className="size-4" aria-hidden="true" />;
    case "at_risk":
      return <ShieldQuestion className="size-4" aria-hidden="true" />;
    case "insufficient":
      return <HelpCircle className="size-4" aria-hidden="true" />;
  }
}

function itemTitle(item: BackupConfidenceItem): string {
  if (item.policyName && item.nodeName) return `${item.policyName} · ${item.nodeName}`;
  return item.policyName || item.nodeName || item.id;
}

export function BackupConfidencePanel() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [data, setData] = useState<BackupConfidenceData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) {
      if (import.meta.env.VITE_ENABLE_DEMO_MODE === "true") {
        let cancelled = false;
        setLoading(true);
        setError(null);
        import("@/data/mock")
          .then((mocks) => {
            if (!cancelled) {
              setData(mocks.buildMockBackupConfidence());
            }
          })
          .catch((err) => {
            if (!cancelled) {
              setError(getErrorMessage(err, t("backupConfidence.loadFailed")));
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
      setData(null);
      setLoading(false);
      setError(null);
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    apiClient
      .getBackupConfidence(token, { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted) {
          setData(result);
        }
      })
      .catch((err) => {
        if (!controller.signal.aborted) {
          setError(getErrorMessage(err, t("backupConfidence.loadFailed")));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [token, t]);

  const visibleItems = useMemo(() => data?.items.slice(0, 6) ?? [], [data?.items]);

  if (loading) {
    return (
      <Card className="glass-panel border-border/70">
        <CardHeader className="pb-3">
          <CardTitle className="text-base flex items-center gap-2">
            <ShieldCheck className="size-4 text-primary" aria-hidden="true" />
            {t("backupConfidence.title")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <LoadingState title={t("backupConfidence.loadingTitle")} rows={3} />
        </CardContent>
      </Card>
    );
  }

  if (error || !data) {
    return (
      <Card className="glass-panel border-border/70">
        <CardHeader className="pb-3">
          <CardTitle className="text-base flex items-center gap-2">
            <ShieldCheck className="size-4 text-primary" aria-hidden="true" />
            {t("backupConfidence.title")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <InlineAlert tone="warning">{error ?? t("common.noData")}</InlineAlert>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card className="glass-panel border-border/70">
      <CardHeader className="pb-3 border-b border-border/40">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div>
            <CardTitle className="text-base flex items-center gap-2">
              <ShieldCheck className="size-4 text-primary" aria-hidden="true" />
              {t("backupConfidence.title")}
            </CardTitle>
            <p className="mt-1 text-xs text-muted-foreground">{t("backupConfidence.description")}</p>
          </div>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4" aria-label={t("backupConfidence.summaryAriaLabel")}>
            <SummaryBadge label={t("backupConfidence.status.healthy")} value={data.summary.healthy} tone="success" />
            <SummaryBadge label={t("backupConfidence.status.warning")} value={data.summary.warning} tone="warning" />
            <SummaryBadge label={t("backupConfidence.status.at_risk")} value={data.summary.atRisk} tone="destructive" />
            <SummaryBadge label={t("backupConfidence.status.insufficient")} value={data.summary.insufficient} tone="info" />
          </div>
        </div>
      </CardHeader>
      <CardContent className="p-3">
        {visibleItems.length > 0 ? (
          <div className="grid grid-cols-1 xl:grid-cols-2 gap-3">
            {visibleItems.map((item) => (
              <ConfidenceItemCard key={item.id} item={item} />
            ))}
          </div>
        ) : (
          <div className="min-h-[120px] flex flex-col items-center justify-center text-center gap-2 text-muted-foreground">
            <CheckCircle2 className="size-8 text-success" aria-hidden="true" />
            <p className="text-sm font-medium text-success">{t("backupConfidence.empty")}</p>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function SummaryBadge({ label, value, tone }: { label: string; value: number; tone: "success" | "warning" | "destructive" | "info" }) {
  return (
    <div className="rounded-xl border border-border/60 bg-muted/20 px-3 py-2 text-center min-w-20">
      <div className="text-lg font-semibold leading-none">{value}</div>
      <Badge tone={tone} className="mt-1 justify-center" dot={false}>{label}</Badge>
    </div>
  );
}

function ConfidenceItemCard({ item }: { item: BackupConfidenceItem }) {
  const { t } = useTranslation();
  const mainReason = item.reasons[0];
  const primaryNextStep = item.nextSteps[0];

  return (
    <article className="rounded-2xl border border-border/60 bg-card/70 p-4 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <h3 className="truncate text-sm font-semibold text-foreground">{itemTitle(item)}</h3>
          <p className="text-xs text-muted-foreground">
            {t("backupConfidence.score", { score: item.score })}
            {item.targets.length > 0 ? ` · ${t("backupConfidence.targets", { count: item.targets.length })}` : ""}
          </p>
        </div>
        <Badge tone={statusTone[item.status]} className="shrink-0">
          <StatusIcon status={item.status} />
          {t(`backupConfidence.status.${item.status}`)}
        </Badge>
      </div>

      <div className="mt-3 space-y-2">
        {mainReason ? (
          <InlineAlert tone={mainReason.severity === "critical" ? "critical" : "warning"}>
            {mainReason.message}
          </InlineAlert>
        ) : (
          <InlineAlert tone="success">{t("backupConfidence.noIssues")}</InlineAlert>
        )}

        {item.evidence.length > 0 ? (
          <div className="space-y-1.5" aria-label={t("backupConfidence.evidenceLabel")}>
            {item.evidence.slice(0, 3).map((evidence, index) => (
              <div key={`${evidence.type}-${evidence.taskRunId ?? evidence.alertId ?? index}`} className="flex items-start gap-2 text-xs text-muted-foreground">
                <span className="mt-1 size-1.5 rounded-full bg-primary/70" aria-hidden="true" />
                <span className="line-clamp-2">
                  <span className="font-medium text-foreground/80">{t(`backupConfidence.evidence.${evidence.type}`, { defaultValue: evidence.type })}</span>
                  {" · "}{evidence.message}
                </span>
              </div>
            ))}
          </div>
        ) : null}

        {primaryNextStep ? (
          <div className="rounded-xl border border-dashed border-border/70 bg-muted/20 px-3 py-2 text-xs">
            <span className="font-medium text-foreground">{t("backupConfidence.nextStep")}</span>
            <span className="ml-1 text-muted-foreground">{primaryNextStep.label}</span>
          </div>
        ) : null}
      </div>
    </article>
  );
}
