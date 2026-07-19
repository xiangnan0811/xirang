import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Clock, Play, RotateCcw, ShieldCheck, Timer } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { LoadingState } from "@/components/ui/loading-state";
import { apiClient } from "@/lib/api/client";
import { getTaskStatusMeta } from "@/lib/status";
import type { LogEvent, RestoreDrillStatus, TaskRunRecord } from "@/types/domain";
import { BackupAssetsTaskContextLink } from "@/features/backup-assets/backup-assets-task-context-link";

function getTriggerIcon(type: TaskRunRecord["triggerType"]) {
  switch (type) {
    case "cron":
      return <Clock className="size-4" aria-hidden="true" />;
    case "retry":
      return <RotateCcw className="size-4" aria-hidden="true" />;
    case "restore":
      return <Timer className="size-4" aria-hidden="true" />;
    case "drill":
      return <ShieldCheck className="size-4" aria-hidden="true" />;
    default:
      return <Play className="size-4" aria-hidden="true" />;
  }
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainSec = seconds % 60;
  if (minutes < 60) return `${minutes}m${remainSec}s`;
  const hours = Math.floor(minutes / 60);
  const remainMin = minutes % 60;
  return `${hours}h${remainMin}m`;
}

function drillStatusTone(status: RestoreDrillStatus): "success" | "destructive" | "warning" | "neutral" {
  if (status === "success") return "success";
  if (status === "failed") return "destructive";
  if (status === "running" || status === "pending") return "warning";
  return "neutral";
}

function logLevelClass(level: LogEvent["level"]) {
  switch (level) {
    case "error":
      return "text-destructive";
    case "warn":
      return "text-warning";
    default:
      return "text-muted-foreground";
  }
}

type Props = {
  run: TaskRunRecord;
  token: string;
  onBack: () => void;
};

export function TaskRunDetail({ run, token, onBack }: Props) {
  const { t } = useTranslation();
  const [detailRun, setDetailRun] = useState<TaskRunRecord>(run);
  const [logs, setLogs] = useState<LogEvent[]>([]);
  const [logsLoading, setLogsLoading] = useState(true);
  const [logsError, setLogsError] = useState<string | null>(null);

  useEffect(() => {
    setDetailRun(run);
  }, [run]);

  const fetchDetail = useCallback(async () => {
    try {
      const result = await apiClient.getTaskRun(token, run.id);
      setDetailRun(result);
    } catch {
      // 执行历史列表可作为降级数据；详情接口失败时仍展示基础记录与日志。
    }
  }, [token, run.id]);

  const fetchLogs = useCallback(async () => {
    setLogsLoading(true);
    setLogsError(null);
    try {
      const result = await apiClient.getTaskRunLogs(token, run.id, { limit: 500 });
      setLogs(result);
    } catch (err) {
      setLogsError(err instanceof Error ? err.message : t('tasks.fetchLogsFailed'));
    } finally {
      setLogsLoading(false);
    }
  }, [token, run.id, t]);

  useEffect(() => {
    void fetchDetail();
    void fetchLogs();
  }, [fetchDetail, fetchLogs]);

  const statusMeta = getTaskStatusMeta(detailRun.status);
  const triggerKey = detailRun.triggerType === "cron" || detailRun.triggerType === "retry" || detailRun.triggerType === "restore" || detailRun.triggerType === "drill"
    ? detailRun.triggerType
    : "manual";
  const drillEvidence = detailRun.drillEvidence;
  const drillPhases = drillEvidence ? [
    {
      key: "restore",
      status: drillEvidence.restoreStatus,
      startedAt: drillEvidence.restoreStartedAt,
      finishedAt: drillEvidence.restoreFinishedAt,
      error: drillEvidence.restoreError,
    },
    {
      key: "verify",
      status: drillEvidence.verifyStatus,
      startedAt: drillEvidence.verifyStartedAt,
      finishedAt: drillEvidence.verifyFinishedAt,
      error: drillEvidence.verifyError,
    },
    {
      key: "postVerify",
      status: drillEvidence.postVerifyStatus,
      finishedAt: drillEvidence.postVerifyFinishedAt,
      error: drillEvidence.postVerifyError,
    },
    {
      key: "cleanup",
      status: drillEvidence.cleanupStatus,
      startedAt: drillEvidence.cleanupStartedAt,
      finishedAt: drillEvidence.cleanupFinishedAt,
      error: drillEvidence.cleanupError,
    },
  ] : [];

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <Button variant="ghost" size="icon" className="size-7" onClick={onBack} aria-label={t('taskRunDetail.backAriaLabel')}>
          <ArrowLeft className="size-4" aria-hidden="true" />
        </Button>
        <span className="text-sm font-medium">{t('taskRunDetail.recordTitle', { id: detailRun.id })}</span>
        <BackupAssetsTaskContextLink taskId={detailRun.taskId} className="ml-auto" />
      </div>

      <div className="glass-panel p-5 space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="flex items-center gap-1.5 text-sm">
            {getTriggerIcon(detailRun.triggerType)}
            {t(`tasks.triggerTypeDetail.${triggerKey}`)}
          </span>
          <Badge tone={statusMeta.variant}>{statusMeta.label}</Badge>
          {detailRun.verifyStatus !== "none" && (
            <Badge tone={detailRun.verifyStatus === "passed" ? "success" : "warning"}>
              {detailRun.verifyStatus === "passed" ? t('taskRunHistory.verifyPassed') : detailRun.verifyStatus === "warning" ? t('taskRunHistory.verifyWarning') : t('taskRunHistory.verifyFailed')}
            </Badge>
          )}
        </div>

        <div className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
          <div>
            <span className="text-muted-foreground">{t('taskRunDetail.createdAt')}</span>
            <p>{detailRun.createdAt}</p>
          </div>
          <div>
            <span className="text-muted-foreground">{t('taskRunDetail.startedAt')}</span>
            <p>{detailRun.startedAt}</p>
          </div>
          <div>
            <span className="text-muted-foreground">{t('taskRunDetail.finishedAt')}</span>
            <p>{detailRun.finishedAt}</p>
          </div>
          <div>
            <span className="text-muted-foreground">{t('taskRunDetail.duration')}</span>
            <p>{detailRun.durationMs > 0 ? formatDuration(detailRun.durationMs) : "-"}</p>
          </div>
          {detailRun.throughputMbps > 0 && (
            <div>
              <span className="text-muted-foreground">{t('taskRunDetail.avgSpeed')}</span>
              <p>{detailRun.throughputMbps.toFixed(2)} MB/s</p>
            </div>
          )}
        </div>

        {detailRun.lastError && (
          <div className="rounded border border-destructive/30 bg-destructive/5 p-2">
            <p className="text-xs font-medium text-destructive">{t('taskRunDetail.errorInfo')}</p>
            <p className="mt-1 text-sm text-destructive/90 break-all">{detailRun.lastError}</p>
          </div>
        )}
      </div>

      {drillEvidence ? (
        <div className="glass-panel p-5 space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h4 className="text-sm font-medium">{t('taskRunDetail.drillEvidence.title')}</h4>
              <p className="mt-1 text-xs text-muted-foreground">{t('taskRunDetail.drillEvidence.description')}</p>
            </div>
            <Badge tone={drillStatusTone(drillEvidence.status)}>
              {t(`taskRunDetail.drillEvidence.status.${drillEvidence.status}`)}
            </Badge>
          </div>

          <div className="grid gap-3 text-sm sm:grid-cols-2">
            <div>
              <span className="text-muted-foreground">{t('taskRunDetail.drillEvidence.sandboxNode')}</span>
              <p>{drillEvidence.sandboxNodeName || `#${drillEvidence.sandboxNodeId}`}</p>
            </div>
            <div>
              <span className="text-muted-foreground">{t('taskRunDetail.drillEvidence.sandboxPath')}</span>
              <p className="break-all font-mono text-xs">{drillEvidence.sandboxPath}</p>
            </div>
            <div>
              <span className="text-muted-foreground">{t('taskRunDetail.drillEvidence.sourceTaskRun')}</span>
              <p>{drillEvidence.sourceTaskRunId ? `#${drillEvidence.sourceTaskRunId}` : "-"}</p>
            </div>
            <div>
              <span className="text-muted-foreground">{t('taskRunDetail.drillEvidence.snapshotRef')}</span>
              <p className="break-all font-mono text-xs">{drillEvidence.snapshotRef || "-"}</p>
            </div>
            <div>
              <span className="text-muted-foreground">{t('taskRunDetail.drillEvidence.failedStep')}</span>
              <p>{drillEvidence.failedStep ? t(`taskRunDetail.drillEvidence.failedSteps.${drillEvidence.failedStep}`, { defaultValue: drillEvidence.failedStep }) : "-"}</p>
            </div>
            <div>
              <span className="text-muted-foreground">{t('taskRunDetail.drillEvidence.confidenceEligible')}</span>
              <p>{drillEvidence.confidenceEligible ? t('common.success') : t('common.failed')}</p>
            </div>
          </div>

          <div className="grid gap-2 sm:grid-cols-2">
            {drillPhases.map((phase) => (
              <div key={phase.key} className="rounded-md border border-border/70 bg-secondary/40 p-3 text-xs">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-medium text-foreground">{t(`taskRunDetail.drillEvidence.phases.${phase.key}`)}</span>
                  <Badge tone={drillStatusTone(phase.status)} className="text-micro px-1.5 py-0">
                    {t(`taskRunDetail.drillEvidence.status.${phase.status}`)}
                  </Badge>
                </div>
                <div className="mt-2 space-y-1 text-muted-foreground">
                  {phase.startedAt ? <p>{t('taskRunDetail.startedAt')}: {phase.startedAt}</p> : null}
                  {phase.finishedAt ? <p>{t('taskRunDetail.finishedAt')}: {phase.finishedAt}</p> : null}
                  {phase.error ? <p className="break-all text-destructive">{phase.error}</p> : null}
                </div>
              </div>
            ))}
          </div>
        </div>
      ) : null}

      <div>
        <h4 className="mb-2 text-sm font-medium">{t('taskRunDetail.executionLogs')}</h4>
        {logsLoading ? (
          <LoadingState title={t('taskRunDetail.loadingLogs')} description={t('taskRunDetail.loadingLogsDesc')} rows={3} />
        ) : logsError ? (
          <div className="py-4 text-center text-sm text-muted-foreground">
            <p>{logsError}</p>
            <Button variant="outline" size="sm" className="mt-2" onClick={() => void fetchLogs()}>
              {t('common.retry')}
            </Button>
          </div>
        ) : logs.length === 0 ? (
          <div className="py-4 text-center text-sm text-muted-foreground">{t('taskRunDetail.noLogs')}</div>
        ) : (
          <div className="max-h-80 overflow-y-auto rounded-lg border border-border/60 bg-card/30 p-3 thin-scrollbar">
            {logs.map((log) => (
              <div key={log.id} className="flex gap-2 py-0.5 text-xs font-mono">
                <span className="shrink-0 text-muted-foreground/60">{log.timestamp}</span>
                <span className={logLevelClass(log.level)}>{log.message}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
