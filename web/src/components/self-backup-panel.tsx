import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { DatabaseBackup, ExternalLink, Loader2, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import { InlineAlert, type InlineAlertTone } from "@/components/ui/inline-alert";
import { BackupTable } from "@/components/self-backup-panel.backup-table";
import { SystemBackupDownloadDialog, type BackupDownloadStage } from "@/components/system-backup-download-dialog";
import { useAuth } from "@/context/auth-context.hooks";
import i18n from "@/i18n";
import { apiClient } from "@/lib/api/client";
import {
  ApiError,
  AuthTransitionRejectedError,
  formatTime,
  getAuthIdentitySnapshot,
  getAuthSessionGeneration,
  isAuthTransitionActive,
  subscribeAuthSessionGeneration,
  subscribeAuthTransition,
} from "@/lib/api/core";
import type { CronBackupStatus, CronBackupStatusCode, CronJobObservation, CronJobStatusCode } from "@/lib/api/cron-backup-status";
import type { BackupEntry, BackupSource } from "@/lib/api/system-api";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import {
  BACKUP_COMPAT_DOWNLOAD_MAX_BYTES,
  BackupDownloadError,
  prepareBackupDownloadSink,
  saveBackupDownload,
  supportsNativeBackupSave,
  type BackupDownloadErrorCode,
  type BackupDownloadResult,
  type BackupDownloadSink,
} from "@/lib/system-backup-download";
import { formatBytes, getErrorMessage } from "@/lib/utils";
import { toast } from "sonner";

const OFFLINE_RECOVERY_HREF = "https://github.com/xiangnan0811/xirang/blob/main/docs/deployment.md#手动备份与恢复";

const SOURCE_HEADING_ID: Record<BackupSource, string> = {
  web: "self-backup-web-heading",
  cron: "self-backup-cron-heading",
};
const DOWNLOAD_REASON_MAX_LENGTH = 240;
const DOWNLOAD_GRANT_TTL_SECONDS = 600;
const PROGRESS_PAINT_INTERVAL_MS = 250;

type BackupListPhase = "loading" | "ready-empty" | "ready-data" | "error" | "unsupported" | "not-configured" | "scan-limit";
type ObserverPhase = "loading" | "ready" | "error";

type DownloadView = {
  entry: BackupEntry;
  stage: BackupDownloadStage;
  received: number;
  total: number;
  result: BackupDownloadResult | null;
  errorMessage: string | null;
  nativeSave: boolean;
};

/** Which await of the download chain failed; it decides how an otherwise generic error is explained. */
type DownloadStep = "target" | "proof" | "grant" | "transfer";

type BackupOperationScope = {
  attempt: number;
  generation: number;
  token: string;
};

type BoundSignal = {
  signal: AbortSignal;
  release: () => void;
  abort: () => void;
};

function useBackupOperation(token: string | null, role: string | null) {
  const mountedRef = useRef(false);
  const attemptRef = useRef(0);
  const controllersRef = useRef(new Set<AbortController>());

  useLayoutEffect(() => {
    mountedRef.current = true;
    const abortAll = () => {
      for (const controller of controllersRef.current) controller.abort();
      controllersRef.current.clear();
    };
    const invalidate = () => {
      attemptRef.current += 1;
      abortAll();
    };
    const unsubscribeGeneration = subscribeAuthSessionGeneration(invalidate);
    const unsubscribeTransition = subscribeAuthTransition(() => {
      if (isAuthTransitionActive()) invalidate();
    });
    return () => {
      unsubscribeGeneration();
      unsubscribeTransition();
      mountedRef.current = false;
      invalidate();
    };
  }, [token, role]);

  const capture = useCallback((): BackupOperationScope | null => {
    if (!mountedRef.current || !token || role !== "admin" || isAuthTransitionActive()) return null;
    const snapshot = getAuthIdentitySnapshot();
    if (snapshot.token !== token || snapshot.role !== "admin") return null;
    return { attempt: attemptRef.current, generation: getAuthSessionGeneration(), token };
  }, [token, role]);

  const disposed = useCallback((scope: BackupOperationScope) => {
    return !mountedRef.current || attemptRef.current !== scope.attempt;
  }, []);

  const current = useCallback((scope: BackupOperationScope) => {
    if (disposed(scope) || isAuthTransitionActive() || getAuthSessionGeneration() !== scope.generation) return false;
    const snapshot = getAuthIdentitySnapshot();
    return snapshot.token === scope.token && snapshot.role === "admin";
  }, [disposed]);

  const bindSignal = useCallback((): BoundSignal => {
    const controller = new AbortController();
    controllersRef.current.add(controller);
    return {
      signal: controller.signal,
      release() {
        controllersRef.current.delete(controller);
      },
      abort() {
        controller.abort();
        controllersRef.current.delete(controller);
      },
    };
  }, []);

  return { capture, current, disposed, bindSignal };
}

function isAbandonedError(error: unknown): boolean {
  return error instanceof AuthTransitionRejectedError || (error instanceof Error && error.name === "AbortError");
}

function isAbandonedRead(error: unknown, signal: AbortSignal): boolean {
  return signal.aborted || isAbandonedError(error);
}

function isSqliteWebUnsupported(error: unknown): boolean {
  return error instanceof ApiError && error.status === 501;
}

/** Fixed `data.error_code` of the unified error envelope; never the human-readable message. */
function envelopeErrorCode(error: ApiError): string | null {
  const detail = error.detail;
  if (!detail || typeof detail !== "object" || !("data" in detail)) return null;
  const data = detail.data;
  if (!data || typeof data !== "object" || !("error_code" in data)) return null;
  return typeof data.error_code === "string" ? data.error_code : null;
}

function listErrorPhase(error: unknown): Exclude<BackupListPhase, "loading" | "ready-empty" | "ready-data"> {
  if (!(error instanceof ApiError)) return "error";
  const code = envelopeErrorCode(error);
  if (code === "BACKUP_SCAN_LIMIT_EXCEEDED") return "scan-limit";
  if (code === "BACKUP_SOURCE_UNSUPPORTED" || error.status === 501) return "unsupported";
  if (code === "BACKUP_SOURCE_NOT_CONFIGURED") return "not-configured";
  return "error";
}

const SINK_ERROR_KEYS: Record<BackupDownloadErrorCode, string> = {
  invalid_entry: "notDownloadable",
  compat_too_large: "compatTooLarge",
  picker_cancelled: "pickerCancelled",
  save_target_failed: "saveTargetFailed",
  target_exists: "targetExists",
  save_target_busy: "saveTargetBusy",
  invalid_response: "invalidResponse",
  selection_changed: "changed",
  transfer_failed: "transferFailed",
  body_length_mismatch: "transferFailed",
  write_failed: "writeFailed",
  commit_failed: "commitFailed",
};

const SERVER_ERROR_KEYS: Record<string, string> = {
  BACKUP_DOWNLOAD_BUSY: "busy",
  BACKUP_SOURCE_NOT_CONFIGURED: "sourceNotConfigured",
  BACKUP_SOURCE_UNSUPPORTED: "sourceUnsupported",
  BACKUP_NOT_FOUND: "notFound",
  BACKUP_CHANGED: "changed",
  BACKUP_NOT_READY: "notReady",
  BACKUP_INTEGRITY_FAILED: "integrityFailed",
  BACKUP_TOO_LARGE: "tooLarge",
  BACKUP_PREPARE_TIMEOUT: "timeout",
  BACKUP_AUTHORIZATION_UNAVAILABLE: "authorizationUnavailable",
  BACKUP_DIRECTORY_UNAVAILABLE: "notReady",
  BACKUP_PRECONDITION_REQUIRED: "requestInvalid",
  BACKUP_REQUEST_INVALID: "requestInvalid",
  BACKUP_RANGE_UNSUPPORTED: "requestInvalid",
  CREDENTIAL_GRANT_REQUIRED: "grantRequired",
  STEP_UP_REQUIRED: "stepUpFailed",
};

const HTTP_ERROR_KEYS: Record<number, string> = {
  404: "notFound",
  412: "changed",
  413: "tooLarge",
  429: "busy",
  504: "timeout",
};

/** Maps a failed step to a fixed localized key under `selfBackup.download.errors`; raw server text is never shown. */
function downloadErrorKey(error: unknown, step: DownloadStep): string {
  if (error instanceof BackupDownloadError) return SINK_ERROR_KEYS[error.code];
  if (step === "proof") {
    // The provider rejects a user-cancelled prompt with this exact local message.
    return error instanceof Error && error.message === i18n.t("stepUp.cancelled") ? "stepUpCancelled" : "stepUpFailed";
  }
  if (!(error instanceof ApiError)) return "generic";
  const httpStatus = error.httpStatus ?? error.status;
  if (httpStatus === 401) return "sessionExpired";
  const code = envelopeErrorCode(error);
  if (code !== null && code in SERVER_ERROR_KEYS) return SERVER_ERROR_KEYS[code];
  if (step === "grant") return httpStatus === 400 ? "reasonRejected" : "grantFailed";
  return HTTP_ERROR_KEYS[httpStatus] ?? "generic";
}

function observerTone(status: CronBackupStatusCode): InlineAlertTone {
  if (status === "fresh") return "success";
  if (status === "not_configured" || status === "no_complete_backup") return "info";
  return "warning";
}

function jobTone(status: CronJobStatusCode): InlineAlertTone {
  if (status === "success") return "success";
  if (status === "not_configured" || status === "not_initialized" || status === "never_run" || status === "running") return "info";
  return "warning";
}

function ObservedInstant({ value }: { value: string }) {
  const { t } = useTranslation();
  return (
    <span className="flex flex-col gap-0.5">
      <span>
        <span className="text-muted-foreground">{t("selfBackup.utcTime")} </span>
        <time dateTime={value} className="font-mono">{value}</time>
      </span>
      <span>
        <span className="text-muted-foreground">{t("selfBackup.localTime")} </span>
        <span>{formatTime(value)}</span>
      </span>
    </span>
  );
}

function FreshnessThreshold({ seconds }: { seconds: number }) {
  const { t } = useTranslation();
  if (seconds > 0 && seconds % 3600 === 0) {
    return <>{t("selfBackup.thresholdHours", { hours: seconds / 3600 })}</>;
  }
  return <>{t("selfBackup.thresholdSeconds", { seconds })}</>;
}

function EvidenceFact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 break-all text-foreground">{children}</dd>
    </div>
  );
}

function JobExecutionRecord({ job }: { job: CronJobObservation }) {
  const { t } = useTranslation();
  const attempt = job.latestAttempt;
  const success = job.lastSuccess;
  return (
    <section data-evidence="job_record" aria-labelledby="cron-job-record-heading" className="min-w-0 space-y-3">
      <h4 id="cron-job-record-heading" className="text-sm font-medium text-foreground">{t("selfBackup.jobTitle")}</h4>
      <p className="text-xs leading-relaxed text-muted-foreground">{t("selfBackup.jobDisclaimer")}</p>
      <InlineAlert tone={jobTone(job.status)} title={t(`selfBackup.jobStatus.${job.status}`)}>
        {t(`selfBackup.jobDetail.${job.status}`)}
      </InlineAlert>
      <dl className="grid grid-cols-1 gap-3 text-xs sm:grid-cols-2">
        <EvidenceFact label={t("selfBackup.jobEvidence")}>{t("selfBackup.evidenceJobRecord")}</EvidenceFact>
        <EvidenceFact label={t("selfBackup.jobCheckedAt")}><ObservedInstant value={job.checkedAt} /></EvidenceFact>
        <EvidenceFact label={t("selfBackup.jobThreshold")}><FreshnessThreshold seconds={job.maxAgeSeconds} /></EvidenceFact>
        {attempt ? <EvidenceFact label={t("selfBackup.jobAttempt")}>{t(`selfBackup.attemptResult.${attempt.result}`)}</EvidenceFact> : null}
        {attempt ? <EvidenceFact label={t("selfBackup.jobStartedAt")}><ObservedInstant value={attempt.startedAt} /></EvidenceFact> : null}
        {attempt?.finishedAt ? <EvidenceFact label={t("selfBackup.jobFinishedAt")}><ObservedInstant value={attempt.finishedAt} /></EvidenceFact> : null}
        {attempt?.detectedAt ? <EvidenceFact label={t("selfBackup.jobDetectedAt")}><ObservedInstant value={attempt.detectedAt} /></EvidenceFact> : null}
        {attempt?.failureCode ? <EvidenceFact label={t("selfBackup.jobFailure")}>{t(`selfBackup.failureCode.${attempt.failureCode}`)}</EvidenceFact> : null}
        {attempt?.artifactName ? <EvidenceFact label={t("selfBackup.jobAttemptArtifact")}><span className="font-mono">{attempt.artifactName}</span></EvidenceFact> : null}
        {success ? <EvidenceFact label={t("selfBackup.jobSuccessArtifact")}><span className="font-mono">{success.artifactName}</span></EvidenceFact> : null}
        {success ? <EvidenceFact label={t("selfBackup.jobSuccessStarted")}><ObservedInstant value={success.startedAt} /></EvidenceFact> : null}
        {success ? <EvidenceFact label={t("selfBackup.jobSuccessFinished")}><ObservedInstant value={success.finishedAt} /></EvidenceFact> : null}
      </dl>
    </section>
  );
}

function ArtifactObservation({ status }: { status: CronBackupStatus }) {
  const { t } = useTranslation();
  return (
    <section data-evidence="artifact_pair" aria-labelledby="cron-artifact-evidence-heading" className="min-w-0 space-y-3 border-t border-border/60 pt-4">
      <h4 id="cron-artifact-evidence-heading" className="text-sm font-medium text-foreground">{t("selfBackup.artifactTitle")}</h4>
      <p className="text-xs leading-relaxed text-muted-foreground">{t("selfBackup.cronDisclaimer")}</p>
      <InlineAlert tone={observerTone(status.status)} title={t(`selfBackup.status.${status.status}`)}>
        {t("selfBackup.cronNotEvidence")}
      </InlineAlert>
      <dl className="grid grid-cols-1 gap-3 text-xs sm:grid-cols-2">
        <EvidenceFact label={t("selfBackup.cronEngine")}>
          {status.engine === "" ? t("selfBackup.engine.unknown") : t(`selfBackup.engine.${status.engine}`)}
        </EvidenceFact>
        <EvidenceFact label={t("selfBackup.cronCheckedAt")}><ObservedInstant value={status.checkedAt} /></EvidenceFact>
        <EvidenceFact label={t("selfBackup.cronThreshold")}><FreshnessThreshold seconds={status.maxAgeSeconds} /></EvidenceFact>
        <EvidenceFact label={t("selfBackup.cronEvidence")}>{t("selfBackup.evidenceArtifactPair")}</EvidenceFact>
        <EvidenceFact label={t("selfBackup.cronTimeSource")}>{t("selfBackup.timeSourceMtime")}</EvidenceFact>
        <EvidenceFact label={t("selfBackup.cronContent")}>{t("selfBackup.contentNotVerified")}</EvidenceFact>
        {status.directory ? (
          <EvidenceFact label={t("selfBackup.cronDirectory")}><span className="font-mono">{status.directory}</span></EvidenceFact>
        ) : null}
        {status.artifactName ? (
          <EvidenceFact label={t("selfBackup.cronArtifact")}><span className="font-mono">{status.artifactName}</span></EvidenceFact>
        ) : null}
        {status.latestCompleteAt ? (
          <EvidenceFact label={t("selfBackup.cronLatestComplete")}><ObservedInstant value={status.latestCompleteAt} /></EvidenceFact>
        ) : null}
      </dl>
    </section>
  );
}

function CronBackupEvidence({
  phase,
  status,
  onRetry,
}: {
  phase: ObserverPhase;
  status: CronBackupStatus | null;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Card data-panel="cron-backup-status" aria-labelledby="cron-backup-evidence-heading" className="glass-panel border-border/70 relative overflow-hidden">
      <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" aria-hidden="true" />
      <CardHeader className="relative z-10 pb-3">
        <CardTitle id="cron-backup-evidence-heading" className="text-base">{t("selfBackup.cronTitle")}</CardTitle>
      </CardHeader>
      <CardContent className="relative z-10">
      {phase === "loading" ? (
        <p className="text-xs text-muted-foreground">{t("selfBackup.cronLoading")}</p>
      ) : phase === "error" || !status ? (
        <InlineAlert tone="critical" title={t("selfBackup.cronLoadError")}>
          <Button type="button" size="sm" variant="outline" className="mt-2" onClick={onRetry}>
            {t("selfBackup.retryCron")}
          </Button>
        </InlineAlert>
      ) : (
        <div className="space-y-4">
          <JobExecutionRecord job={status.job} />
          <ArtifactObservation status={status} />
        </div>
      )}
      </CardContent>
    </Card>
  );
}

export function SelfBackupPanel() {
  const { token, role } = useAuth();
  const { i18n: translator } = useTranslation();
  const generation = useSyncExternalStore(subscribeAuthSessionGeneration, getAuthSessionGeneration, getAuthSessionGeneration);
  const [barrier, setBarrier] = useState(() => ({ active: isAuthTransitionActive(), revision: 0 }));
  useLayoutEffect(() => {
    const notifyTransition = () => {
      const active = isAuthTransitionActive();
      setBarrier((previous) => ({ active, revision: previous.revision + 1 }));
    };
    const unsubscribe = subscribeAuthTransition(notifyTransition);
    // Close the render-to-subscription gap through the same external-store notification.
    if (isAuthTransitionActive() !== barrier.active) notifyTransition();
    return unsubscribe;
  }, [barrier.active]);
  return barrier.active ? null : (
    <SelfBackupPanelContent key={`${token ?? ""}:${role}:${translator.language}:${generation}:${barrier.revision}`} />
  );
}

function SourceListBody({
  source,
  phase,
  entries,
  downloadDisabled,
  onDownload,
  onRetry,
}: {
  source: BackupSource;
  phase: BackupListPhase;
  entries: BackupEntry[];
  downloadDisabled: boolean;
  onDownload: (entry: BackupEntry) => void;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  const web = source === "web";
  const retry = (
    <Button type="button" size="sm" variant="outline" className="mt-2" onClick={onRetry}>
      {t(web ? "selfBackup.retryList" : "selfBackup.retryCronFiles")}
    </Button>
  );
  switch (phase) {
    case "loading":
      return <p className="text-xs text-muted-foreground">{t(web ? "selfBackup.loadingList" : "selfBackup.cronFilesLoading")}</p>;
    case "unsupported":
      return web ? (
        <InlineAlert tone="warning" title={t("selfBackup.sqliteOnly")}>{retry}</InlineAlert>
      ) : (
        <InlineAlert tone="info">{t("selfBackup.cronFilesUnsupported")}</InlineAlert>
      );
    case "not-configured":
      return <InlineAlert tone="info">{t("selfBackup.cronFilesNotConfigured")}</InlineAlert>;
    case "scan-limit":
      return <InlineAlert tone="warning" title={t("selfBackup.listScanLimit")}>{retry}</InlineAlert>;
    case "error":
      return <InlineAlert tone="critical" title={t(web ? "selfBackup.listLoadFailed" : "selfBackup.cronFilesLoadFailed")}>{retry}</InlineAlert>;
    case "ready-data":
      return <BackupTable entries={entries} downloadDisabled={downloadDisabled} onDownload={onDownload} />;
    case "ready-empty":
      return <p className="text-xs text-muted-foreground">{t(web ? "selfBackup.noRecords" : "selfBackup.cronFilesEmpty")}</p>;
  }
}

function RefreshListButton({ label, disabled, onClick }: { label: string; disabled: boolean; onClick: () => void }) {
  const { t } = useTranslation();
  return (
    <Button type="button" size="sm" variant="ghost" className="h-7 px-2 text-xs" aria-label={label} title={label} disabled={disabled} onClick={onClick}>
      <RefreshCw className="mr-1 size-3.5" aria-hidden="true" />
      {t("common.refresh")}
    </Button>
  );
}

function SelfBackupPanelContent() {
  const { t } = useTranslation();
  const { token, role, totpEnabled, ensureStepUpProof } = useAuth();
  const adminSession = Boolean(token) && role === "admin";
  const [webReload, setWebReload] = useState(0);
  const [cronReload, setCronReload] = useState(0);
  const [observerReload, setObserverReload] = useState(0);
  const [webListPhase, setWebListPhase] = useState<BackupListPhase>(adminSession ? "loading" : "ready-empty");
  const [cronListPhase, setCronListPhase] = useState<BackupListPhase>(adminSession ? "loading" : "ready-empty");
  const [observerPhase, setObserverPhase] = useState<ObserverPhase>("loading");
  const [observer, setObserver] = useState<CronBackupStatus | null>(null);
  const [backing, setBacking] = useState(false);
  const [download, setDownload] = useState<DownloadView | null>(null);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | null>(null);
  const { capture, current, disposed, bindSignal } = useBackupOperation(token, role);
  const [backups, setBackups] = useState<BackupEntry[]>([]);
  const [cronBackups, setCronBackups] = useState<BackupEntry[]>([]);
  const listAttemptRef = useRef(0);
  const cronListAttemptRef = useRef(0);
  const observerAttemptRef = useRef(0);
  const backupAttemptRef = useRef(0);
  const refreshTicketRef = useRef(0);
  const announcedRefreshRef = useRef(0);
  const downloadAttemptRef = useRef(0);
  const downloadBindingRef = useRef<BoundSignal | null>(null);
  const focusReturnRef = useRef<Pick<BackupEntry, "source" | "filename"> | null>(null);

  useEffect(() => {
    const scope = capture();
    if (!scope) return;
    const binding = bindSignal();
    const listAttempt = ++listAttemptRef.current;
    const refreshTicket = refreshTicketRef.current;
    void (async () => {
      let phase: Exclude<BackupListPhase, "loading"> | null = null;
      let entries: BackupEntry[] = [];
      try {
        entries = await apiClient.listBackups(scope.token, { signal: binding.signal });
        phase = entries.length > 0 ? "ready-data" : "ready-empty";
      } catch (error) {
        if (!isAbandonedRead(error, binding.signal)) {
          phase = listErrorPhase(error);
        }
      } finally {
        binding.release();
      }
      if (phase === null || binding.signal.aborted || listAttempt !== listAttemptRef.current || !current(scope)) return;
      setBackups(phase === "ready-data" ? entries : []);
      setWebListPhase(phase);
      if (refreshTicket !== 0 && refreshTicket === refreshTicketRef.current && announcedRefreshRef.current !== refreshTicket) {
        announcedRefreshRef.current = refreshTicket;
        if (phase !== "ready-data" && phase !== "ready-empty") toast.error(t("selfBackup.refreshFailed"));
      }
    })();
    return () => binding.abort();
  }, [bindSignal, capture, current, webReload, t]);

  useEffect(() => {
    const scope = capture();
    if (!scope) return;
    const binding = bindSignal();
    const listAttempt = ++cronListAttemptRef.current;
    void (async () => {
      let phase: Exclude<BackupListPhase, "loading"> | null = null;
      let entries: BackupEntry[] = [];
      try {
        entries = await apiClient.listBackups(scope.token, { signal: binding.signal, source: "cron" });
        phase = entries.length > 0 ? "ready-data" : "ready-empty";
      } catch (error) {
        if (!isAbandonedRead(error, binding.signal)) {
          phase = listErrorPhase(error);
        }
      } finally {
        binding.release();
      }
      if (phase === null || binding.signal.aborted || listAttempt !== cronListAttemptRef.current || !current(scope)) return;
      setCronBackups(phase === "ready-data" ? entries : []);
      setCronListPhase(phase);
    })();
    return () => binding.abort();
  }, [bindSignal, capture, current, cronReload]);

  useEffect(() => {
    const scope = capture();
    if (!scope) return;
    const binding = bindSignal();
    const observerAttempt = ++observerAttemptRef.current;
    void (async () => {
      let next: CronBackupStatus | null = null;
      let failed = false;
      let abandoned = false;
      try {
        next = await apiClient.getCronBackupStatus(scope.token, { signal: binding.signal });
      } catch (error) {
        if (isAbandonedRead(error, binding.signal)) abandoned = true;
        else failed = true;
      } finally {
        binding.release();
      }
      if (abandoned || binding.signal.aborted || observerAttempt !== observerAttemptRef.current || !current(scope)) return;
      if (failed || !next) {
        setObserver(null);
        setObserverPhase("error");
        return;
      }
      setObserver(next);
      setObserverPhase("ready");
    })();
    return () => binding.abort();
  }, [bindSignal, capture, current, observerReload, t]);

  const reloadList = (source: BackupSource) => {
    if (isAuthTransitionActive()) return;
    if (!capture()) return;
    if (source === "web") {
      setWebListPhase("loading");
      setWebReload((value) => value + 1);
      return;
    }
    setCronListPhase("loading");
    setCronReload((value) => value + 1);
  };

  const retryObserver = () => {
    if (isAuthTransitionActive()) return;
    if (!capture()) return;
    setObserverPhase("loading");
    setObserverReload((value) => value + 1);
  };

  if (!token || role !== "admin") return null;

  const handleBackup = async () => {
    if (isAuthTransitionActive()) return;
    const scope = capture();
    if (!scope) return;
    const binding = bindSignal();
    const backupAttempt = ++backupAttemptRef.current;
    setBacking(true);
    try {
      const result = await apiClient.backupDB(scope.token, { signal: binding.signal });
      if (!current(scope) || binding.signal.aborted) return;
      toast.success(t("selfBackup.backupSuccess", { filename: result.filename, size: formatBytes(result.size) }));
      if (!current(scope) || binding.signal.aborted) return;
      refreshTicketRef.current += 1;
      setWebListPhase("loading");
      setWebReload((value) => value + 1);
    } catch (error) {
      if (!current(scope) || binding.signal.aborted || isAbandonedRead(error, binding.signal)) return;
      if (isSqliteWebUnsupported(error)) {
        setBackups([]);
        setWebListPhase("unsupported");
        return;
      }
      toast.error(getErrorMessage(error, t("selfBackup.backupFailed")));
    } finally {
      binding.release();
      if (backupAttempt === backupAttemptRef.current && current(scope)) setBacking(false);
    }
  };

  const openDownload = (entry: BackupEntry) => {
    if (download !== null || downloadBindingRef.current !== null || !entry.downloadable || !capture()) return;
    focusReturnRef.current = { source: entry.source, filename: entry.filename };
    setReason("");
    setReasonError(null);
    setDownload({
      entry,
      stage: "confirm",
      received: 0,
      total: entry.packagedSize,
      result: null,
      errorMessage: null,
      nativeSave: supportsNativeBackupSave(),
    });
  };

  // Invalidates the running attempt: its signal aborts the proof prompt, grant, request and sink, and
  // every late result is dropped by the attempt check. Nothing is retried automatically.
  const closeDownload = () => {
    downloadAttemptRef.current += 1;
    downloadBindingRef.current?.abort();
    downloadBindingRef.current = null;
    setDownload(null);
    setReasonError(null);
  };

  const retryDownload = () => {
    if (download?.stage !== "failed" || downloadBindingRef.current !== null) return;
    setDownload({ ...download, stage: "confirm", received: 0, result: null, errorMessage: null, nativeSave: supportsNativeBackupSave() });
  };

  // Runs synchronously up to prepareBackupDownloadSink so the directory picker retains user activation.
  const confirmDownload = () => {
    const view = download;
    if (view?.stage !== "confirm" || downloadBindingRef.current !== null || totpEnabled !== true) return;
    const entry = view.entry;
    if (!view.nativeSave && entry.packagedSize > BACKUP_COMPAT_DOWNLOAD_MAX_BYTES) return;
    const trimmedReason = reason.trim();
    if (!trimmedReason) {
      setReasonError(t("selfBackup.download.reasonRequired"));
      return;
    }
    if (Array.from(trimmedReason).length > DOWNLOAD_REASON_MAX_LENGTH) {
      setReasonError(t("selfBackup.download.reasonTooLong", { max: DOWNLOAD_REASON_MAX_LENGTH }));
      return;
    }
    if (isAuthTransitionActive()) return;
    const scope = capture();
    if (!scope) return;
    setReasonError(null);
    const binding = bindSignal();
    const attempt = ++downloadAttemptRef.current;
    downloadBindingRef.current = binding;
    const isCurrent = () => !binding.signal.aborted && attempt === downloadAttemptRef.current && current(scope);
    const sinkPromise = prepareBackupDownloadSink(entry, isCurrent);
    const update = (patch: Partial<DownloadView>) => {
      if (!isCurrent()) return;
      setDownload((previous) => (previous?.entry === entry ? { ...previous, ...patch } : previous));
    };
    const staleError = () => new DOMException("Backup download superseded", "AbortError");
    update({ stage: view.nativeSave ? "selecting" : "verifying", received: 0, total: entry.packagedSize });

    void (async () => {
      let step: DownloadStep = "target";
      let sink: BackupDownloadSink | null = null;
      const abortSink = () => { void sink?.abort(); };
      try {
        sink = await sinkPromise;
        if (!isCurrent()) throw staleError();
        // Own the sink throughout proof, grant, fetch and streaming, not just while reading bytes.
        binding.signal.addEventListener("abort", abortSink, { once: true });
        step = "proof";
        update({ stage: "verifying" });
        const proof = await ensureStepUpProof(STEP_UP_ACTIONS.systemBackupDownload, {
          persist: false,
          reuseCached: false,
          signal: binding.signal,
        });
        if (!isCurrent()) throw staleError();
        step = "grant";
        update({ stage: "preparing" });
        await apiClient.requestSystemBackupDownloadCredentialGrant(
          scope.token,
          { reason: trimmedReason, requestedTtlSeconds: DOWNLOAD_GRANT_TTL_SECONDS },
          proof,
          { signal: binding.signal, isCurrent },
        );
        if (!isCurrent()) throw staleError();
        step = "transfer";
        const response = await apiClient.downloadBackup(scope.token, entry, proof, { signal: binding.signal, isCurrent });
        if (!isCurrent()) {
          void response.body?.cancel().catch(() => undefined);
          throw staleError();
        }
        update({ stage: "receiving" });
        let lastPaint = 0;
        const result = await saveBackupDownload(response, sink, {
          signal: binding.signal,
          isCurrent,
          onProgress: (received, total) => {
            // Receiving all bytes still requires EOF validation before the irreversible close.
            if (received === total) {
              update({ stage: "committing", received, total });
              return;
            }
            const now = Date.now();
            if (now - lastPaint < PROGRESS_PAINT_INTERVAL_MS) return;
            lastPaint = now;
            update({ received, total });
          },
        });
        update({ stage: "done", result, received: sink.expectedSize, total: sink.expectedSize });
      } catch (error) {
        await sink?.abort().catch(() => undefined);
        // A user cancel already closed the dialog; an unmounted panel has nothing to show.
        if (attempt !== downloadAttemptRef.current || disposed(scope)) return;
        if (!isCurrent() || isAbandonedError(error)) {
          // Identity or auth transition ended this attempt: never surface its outcome.
          setDownload(null);
          return;
        }
        const message = t(`selfBackup.download.errors.${downloadErrorKey(error, step)}`);
        setDownload((previous) => (previous?.entry === entry ? { ...previous, stage: "failed", errorMessage: message } : previous));
      } finally {
        await sink?.abort().catch(() => undefined);
        binding.signal.removeEventListener("abort", abortSink);
        binding.release();
        if (downloadBindingRef.current === binding) downloadBindingRef.current = null;
      }
    })();
  };

  const restoreDownloadFocus: DialogCloseAutoFocus = (event) => {
    const origin = focusReturnRef.current;
    if (!origin) return;
    event.preventDefault();
    const key = `${origin.source}:${origin.filename}`;
    const button = Array.from(document.querySelectorAll<HTMLElement>("[data-backup-download-key]"))
      .find((element) => element.dataset.backupDownloadKey === key);
    button?.focus();
    if (!button || document.activeElement !== button) {
      document.getElementById(SOURCE_HEADING_ID[origin.source])?.focus();
    }
  };

  const downloadOpen = download !== null;

  return (
    <>
    <Card className="glass-panel border-border/70 relative overflow-hidden group">
      <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" />
      <CardHeader className="pb-3 z-10 relative">
        <CardTitle className="text-base">{t("selfBackup.title")}</CardTitle>
      </CardHeader>
      <CardContent className="relative z-10 space-y-4">
        <div className="space-y-2 text-xs leading-relaxed text-muted-foreground">
          <p>{t("selfBackup.desc")}</p>
          <p>{t("selfBackup.snapshotCaveat")}</p>
          <p>{t("selfBackup.retentionNote")}</p>
          <p>
            <a
              href={OFFLINE_RECOVERY_HREF}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-primary underline underline-offset-2 hover:text-primary/80"
            >
              {t("selfBackup.offlineRecovery")}
              <ExternalLink className="size-3" aria-hidden="true" />
            </a>
          </p>
        </div>
        <Button type="button" size="sm" variant="outline" onClick={handleBackup} disabled={backing || webListPhase === "unsupported"}>
          {backing ? <Loader2 className="mr-1 size-3.5 animate-spin" aria-hidden="true" /> : <DatabaseBackup className="mr-1 size-3.5" aria-hidden="true" />}
          {t("selfBackup.backupNow")}
        </Button>

        <section
          data-backup-source="web"
          aria-labelledby={SOURCE_HEADING_ID.web}
          aria-busy={webListPhase === "loading"}
          className="space-y-2 border-t border-border/50 pt-3"
        >
          <div className="flex items-center justify-between gap-2">
            <h4 id={SOURCE_HEADING_ID.web} tabIndex={-1} className="rounded-sm text-sm font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring">
              {t("selfBackup.webListTitle")}
            </h4>
            <RefreshListButton label={t("selfBackup.refreshWeb")} disabled={webListPhase === "loading"} onClick={() => reloadList("web")} />
          </div>
          <SourceListBody
            source="web"
            phase={webListPhase}
            entries={backups}
            downloadDisabled={downloadOpen}
            onDownload={openDownload}
            onRetry={() => reloadList("web")}
          />
        </section>
      </CardContent>
    </Card>
    <CronBackupEvidence phase={observerPhase} status={observer} onRetry={retryObserver} />
    <section
      data-panel="cron-backup-files"
      data-backup-source="cron"
      aria-labelledby={SOURCE_HEADING_ID.cron}
      aria-busy={cronListPhase === "loading"}
      className="glass-panel relative overflow-hidden rounded-lg border border-border/70 bg-card text-card-foreground shadow-sm dark:shadow-none"
    >
      <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" aria-hidden="true" />
      <CardHeader className="relative z-10 flex-row items-center justify-between gap-2 pb-3">
        <CardTitle id={SOURCE_HEADING_ID.cron} tabIndex={-1} className="rounded-sm text-base outline-none focus-visible:ring-2 focus-visible:ring-ring">
          {t("selfBackup.cronFilesTitle")}
        </CardTitle>
        <RefreshListButton label={t("selfBackup.refreshCron")} disabled={cronListPhase === "loading"} onClick={() => reloadList("cron")} />
      </CardHeader>
      <CardContent className="relative z-10 space-y-3">
        <p className="text-xs leading-relaxed text-muted-foreground">{t("selfBackup.cronFilesDesc")}</p>
        <SourceListBody
          source="cron"
          phase={cronListPhase}
          entries={cronBackups}
          downloadDisabled={downloadOpen}
          onDownload={openDownload}
          onRetry={() => reloadList("cron")}
        />
      </CardContent>
    </section>
    <SystemBackupDownloadDialog
      entry={download?.entry ?? null}
      stage={download?.stage ?? "confirm"}
      received={download?.received ?? 0}
      total={download?.total ?? 0}
      result={download?.result ?? null}
      errorMessage={download?.errorMessage ?? null}
      reason={reason}
      reasonError={reasonError}
      reasonMaxLength={DOWNLOAD_REASON_MAX_LENGTH}
      totpEnabled={totpEnabled === true}
      nativeSave={download?.nativeSave ?? false}
      compatBlocked={download !== null && !download.nativeSave && download.entry.packagedSize > BACKUP_COMPAT_DOWNLOAD_MAX_BYTES}
      onReasonChange={(value) => {
        setReason(value);
        if (reasonError) setReasonError(null);
      }}
      onConfirm={confirmDownload}
      onCancel={closeDownload}
      onRetry={retryDownload}
      onCloseAutoFocus={restoreDownloadFocus}
    />
    </>
  );
}
