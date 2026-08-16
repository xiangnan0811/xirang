import { useState } from "react";
import { Download, ShieldCheck, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Pagination } from "@/components/ui/pagination";
import type {
  RecoveryJob,
  RecoveryJobItem,
  RecoveryPage,
  RecoveryResultPage,
} from "@/lib/api/backup-recovery-api";
import { formatBytes } from "@/lib/utils";

export interface RecoveryJobPanelProps {
  job: RecoveryJob;
  itemPage: RecoveryPage<RecoveryJobItem> | null;
  resultPage: RecoveryResultPage | null;
  onLoadItems: (page: number, pageSize: number) => void;
  onLoadResults: (page: number, pageSize: number) => void;
  onDownloadResult: (resultId: string) => void;
  onRetainResults: (deadline: string) => void;
  onCleanupResults: () => void;
}

const PAGE_SIZE = 25;

function toLocalDateTimeInput(utc: string): string {
  const date = new Date(utc);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
}

function badgeTone(outcome: RecoveryJob["outcome"]): "info" | "success" | "warning" | "destructive" {
  if (outcome === "succeeded") return "success";
  if (outcome === "degraded" || outcome === "needs_attention" || outcome === "cancel_requested") return "warning";
  if (outcome === "failed" || outcome === "canceled") return "destructive";
  return "info";
}

export function RecoveryJobPanel({
  job,
  itemPage,
  resultPage,
  onLoadItems,
  onLoadResults,
  onDownloadResult,
  onRetainResults,
  onCleanupResults,
}: RecoveryJobPanelProps) {
  const { t } = useTranslation();
  const resultSet = job.resultSet;
  const completeReadyResults = job.targetMode === "isolated" &&
    (job.outcome === "succeeded" || job.outcome === "degraded") &&
    job.failureCategory === null && job.progress.completedItems === job.progress.totalItems &&
    job.progress.failedItems === 0 && resultSet?.lifecycle === "ready";
  const completed = Math.min(job.progress.totalItems, job.progress.completedItems);

  return (
    <section aria-labelledby="recovery-job-title" className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 id="recovery-job-title" className="text-sm font-semibold">{t("backupAssets.recovery.job.title")}</h3>
          <p className="mt-1 font-mono text-[11px] text-muted-foreground">{job.id}</p>
        </div>
        <Badge tone={badgeTone(job.outcome)} data-testid="recovery-job-outcome">
          {t(`backupAssets.recovery.job.outcome.${job.outcome}`)}
        </Badge>
      </div>

      <div className="space-y-2">
        <div className="flex items-center justify-between gap-3 text-xs">
          <span>{t("backupAssets.recovery.job.progress")}</span>
          <span className="font-mono tabular-nums">{completed}/{job.progress.totalItems}</span>
        </div>
        <div
          role="progressbar"
          aria-label={t("backupAssets.recovery.job.progress")}
          aria-valuemin={0}
          aria-valuemax={job.progress.totalItems}
          aria-valuenow={completed}
          className="h-1.5 overflow-hidden rounded-full bg-muted"
        >
          <span
            aria-hidden
            className="block h-full rounded-full bg-primary"
            style={{ width: `${job.progress.totalItems === 0 ? 0 : (completed / job.progress.totalItems) * 100}%` }}
          />
        </div>
        <p className="text-xs text-muted-foreground">
          {t("backupAssets.recovery.job.progressCounts", {
            succeeded: job.progress.succeededItems,
            skipped: job.progress.skippedItems,
            failed: job.progress.failedItems,
            bytes: formatBytes(job.progress.bytesWritten),
          })}
        </p>
      </div>

      {job.failureCategory !== null ? (
        <InlineAlert tone="warning" live={false} title={t("backupAssets.recovery.job.attention") }>
          {t(`backupAssets.recovery.job.failure.${job.failureCategory}`)}
        </InlineAlert>
      ) : null}

      <section aria-labelledby="recovery-job-items-title" className="space-y-2">
        <div className="flex items-center justify-between gap-2">
          <h4 id="recovery-job-items-title" className="text-xs font-semibold">{t("backupAssets.recovery.job.items")}</h4>
          {itemPage === null ? (
            <Button type="button" size="sm" variant="outline" onClick={() => onLoadItems(1, PAGE_SIZE)}>
              {t("backupAssets.recovery.job.loadItems")}
            </Button>
          ) : null}
        </div>
        {itemPage !== null ? (
          <div data-testid="recovery-item-page" className="space-y-2">
            <ul
              // The bounded scroll viewport must accept keyboard focus so arrow/page scrolling does not require a pointer.
              // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
              tabIndex={0}
              aria-label={t("backupAssets.recovery.job.items")}
              className="max-h-64 divide-y divide-border overflow-y-auto rounded-md border border-border focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
            >
              {itemPage.items.map((item) => (
                <li key={item.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 px-3 py-2 text-xs">
                  <span className="min-w-0">
                    <span className="mr-2 font-mono text-muted-foreground">{item.ordinal + 1}</span>
                    <span className="block">{t(`backupAssets.recovery.job.operation.${item.operation}`)}</span>
                    <span className="block text-[11px] text-muted-foreground">
                      {t(`backupAssets.recovery.job.itemOutcome.${item.outcome}`)}
                      {item.failureCategory ? ` · ${t(`backupAssets.recovery.job.failure.${item.failureCategory}`)}` : ""}
                    </span>
                  </span>
                  <span className="font-mono tabular-nums text-muted-foreground">{formatBytes(item.bytesWritten)}</span>
                </li>
              ))}
            </ul>
            <Pagination
              page={itemPage.page}
              pageSize={itemPage.pageSize}
              total={itemPage.total}
              onPageChange={(page) => onLoadItems(page, itemPage.pageSize)}
            />
          </div>
        ) : null}
      </section>

      {resultSet !== null ? (
        <section aria-labelledby="recovery-results-title" className="space-y-3 rounded-lg border border-border bg-muted/30 p-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h4 id="recovery-results-title" className="text-xs font-semibold">{t("backupAssets.recovery.results.title")}</h4>
              <p className="mt-1 text-[11px] text-muted-foreground">{t("backupAssets.recovery.results.separateLifecycle")}</p>
            </div>
            <Badge
              tone={resultSet.lifecycle === "ready" ? "success" : resultSet.lifecycle === "cleanup_failed" ? "destructive" : "info"}
              data-testid="recovery-result-lifecycle"
            >
              {t(`backupAssets.recovery.results.lifecycle.${resultSet.lifecycle}`)}
            </Badge>
          </div>

          <p data-testid="recovery-result-deadline" className="text-xs text-muted-foreground">
            {t("backupAssets.recovery.results.plaintextDeadline")}{" "}
            <time dateTime={resultSet.plaintextDeadline} className="font-mono tabular-nums">
              {new Date(resultSet.plaintextDeadline).toLocaleString()}
            </time>
          </p>

          {completeReadyResults ? (
            <ReadyRecoveryResults
              key={JSON.stringify([resultSet.id, resultSet.plaintextDeadline])}
              plaintextDeadline={resultSet.plaintextDeadline}
              resultPage={resultPage}
              onLoadResults={onLoadResults}
              onDownloadResult={onDownloadResult}
              onRetainResults={onRetainResults}
            />
          ) : null}

          {(resultSet.lifecycle === "ready" || resultSet.lifecycle === "cleanup_failed") ? (
            <Button type="button" size="sm" variant="destructive" onClick={onCleanupResults}>
              <Trash2 className="size-4" aria-hidden />
              {t("backupAssets.recovery.results.cleanup")}
            </Button>
          ) : null}
        </section>
      ) : null}
    </section>
  );
}

function ReadyRecoveryResults({
  plaintextDeadline,
  resultPage,
  onLoadResults,
  onDownloadResult,
  onRetainResults,
}: Pick<
  RecoveryJobPanelProps,
  "resultPage" | "onLoadResults" | "onDownloadResult" | "onRetainResults"
> & { plaintextDeadline: string }) {
  const { t } = useTranslation();
  const [retainUntil, setRetainUntil] = useState(toLocalDateTimeInput(plaintextDeadline));

  return (
    <>
      {resultPage === null ? (
        <Button type="button" size="sm" variant="outline" onClick={() => onLoadResults(1, PAGE_SIZE)}>
          {t("backupAssets.recovery.results.load")}
        </Button>
      ) : (
        <div data-testid="recovery-result-page" className="space-y-2">
          <ul className="max-h-64 divide-y divide-border overflow-y-auto rounded-md border border-border">
            {resultPage.items.map((item) => (
              <li key={item.id} className="flex items-center gap-3 px-3 py-2 text-xs">
                <ShieldCheck className="size-4 shrink-0 text-success" aria-hidden />
                <span className="min-w-0 flex-1">
                  <span className="block">{t(`backupAssets.recovery.results.kind.${item.kind}`)}</span>
                  <span className="font-mono text-[11px] text-muted-foreground">{formatBytes(item.size)}</span>
                </span>
                <Button type="button" size="sm" variant="ghost" onClick={() => onDownloadResult(item.id)}>
                  <Download className="size-4" aria-hidden />
                  {t("backupAssets.recovery.results.download")}
                </Button>
              </li>
            ))}
          </ul>
          <Pagination
            page={resultPage.page}
            pageSize={resultPage.pageSize}
            total={resultPage.total}
            onPageChange={(page) => onLoadResults(page, resultPage.pageSize)}
          />
        </div>
      )}

      <div className="flex flex-wrap items-end gap-2">
        <label className="grid min-w-0 flex-1 gap-1 text-xs font-medium">
          {t("backupAssets.recovery.results.retainUntil")}
          <input
            type="datetime-local"
            value={retainUntil}
            onChange={(event) => setRetainUntil(event.target.value)}
            className="h-9 rounded-md border border-input bg-background px-2 text-sm"
          />
        </label>
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="self-end"
          disabled={retainUntil === ""}
          onClick={() => onRetainResults(new Date(retainUntil).toISOString())}
        >
          {t("backupAssets.recovery.results.retain")}
        </Button>
      </div>
    </>
  );
}
