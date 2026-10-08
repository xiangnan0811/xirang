import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { DatabaseBackup, ExternalLink, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { InlineAlert, type InlineAlertTone } from "@/components/ui/inline-alert";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import {
  ApiError,
  AuthTransitionRejectedError,
  formatTime,
  getAuthIdentitySnapshot,
  getAuthSessionGeneration,
  isAuthTransitionActive,
  subscribeAuthTransition,
} from "@/lib/api/core";
import type { BackupEntry, CronBackupStatus, CronBackupStatusCode } from "@/lib/api/system-api";
import { formatBytes, getErrorMessage } from "@/lib/utils";
import { toast } from "sonner";

const OFFLINE_RECOVERY_HREF = "https://github.com/xiangnan0811/xirang/blob/main/docs/deployment.md#手动备份与恢复";

type WebBackupListPhase = "loading" | "ready-empty" | "ready-data" | "error" | "unsupported";
type ObserverPhase = "loading" | "ready" | "error";

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

function useBackupOperation(token: string | null, role: string | null, onTransitionSettled: () => void) {
  const mountedRef = useRef(false);
  const attemptRef = useRef(0);
  const controllersRef = useRef(new Set<AbortController>());

  useLayoutEffect(() => {
    mountedRef.current = true;
    const abortAll = () => {
      for (const controller of controllersRef.current) controller.abort();
      controllersRef.current.clear();
    };
    let restartReads = false;
    const unsubscribe = subscribeAuthTransition(() => {
      if (isAuthTransitionActive()) {
        restartReads = controllersRef.current.size > 0;
        abortAll();
        return;
      }
      if (restartReads && mountedRef.current) {
        restartReads = false;
        onTransitionSettled();
      }
    });
    return () => {
      unsubscribe();
      mountedRef.current = false;
      attemptRef.current += 1;
      abortAll();
    };
  }, [token, role, onTransitionSettled]);

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

function isAbandonedRead(error: unknown, signal: AbortSignal): boolean {
  return signal.aborted || error instanceof AuthTransitionRejectedError || (error instanceof Error && error.name === "AbortError");
}

function isSqliteWebUnsupported(error: unknown): boolean {
  return error instanceof ApiError && error.status === 501;
}

function observerTone(status: CronBackupStatusCode): InlineAlertTone {
  if (status === "fresh") return "success";
  if (status === "not_configured" || status === "no_complete_backup") return "info";
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
      <p className="text-xs leading-relaxed text-muted-foreground">{t("selfBackup.cronDisclaimer")}</p>
      {phase === "loading" ? (
        <p className="mt-3 text-xs text-muted-foreground">{t("selfBackup.cronLoading")}</p>
      ) : phase === "error" || !status ? (
        <InlineAlert className="mt-3" tone="critical" title={t("selfBackup.cronLoadError")}>
          <Button type="button" size="sm" variant="outline" className="mt-2" onClick={onRetry}>
            {t("selfBackup.retryCron")}
          </Button>
        </InlineAlert>
      ) : (
        <>
          <InlineAlert className="mt-3" tone={observerTone(status.status)} title={t(`selfBackup.status.${status.status}`)}>
            {t("selfBackup.cronNotEvidence")}
          </InlineAlert>
          <dl className="mt-3 grid grid-cols-1 gap-3 text-xs sm:grid-cols-2">
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
        </>
      )}
      </CardContent>
    </Card>
  );
}

export function SelfBackupPanel() {
  const { token, role } = useAuth();
  const { i18n } = useTranslation();
  return <SelfBackupPanelContent key={`${token ?? ""}:${role}:${i18n.language}`} />;
}

function SelfBackupPanelContent() {
  const { t } = useTranslation();
  const { token, role } = useAuth();
  const [listReload, setListReload] = useState(0);
  const [observerReload, setObserverReload] = useState(0);
  const [webListPhase, setWebListPhase] = useState<WebBackupListPhase>(Boolean(token) && role === "admin" ? "loading" : "ready-empty");
  const [observerPhase, setObserverPhase] = useState<ObserverPhase>("loading");
  const [observer, setObserver] = useState<CronBackupStatus | null>(null);
  const [backing, setBacking] = useState(false);
  const resumeAfterTransition = useCallback(() => {
    setBacking(false);
    setWebListPhase("loading");
    setObserverPhase("loading");
    setListReload((value) => value + 1);
    setObserverReload((value) => value + 1);
  }, []);
  const { capture, current, bindSignal } = useBackupOperation(token, role, resumeAfterTransition);
  const [backups, setBackups] = useState<BackupEntry[]>([]);
  const listAttemptRef = useRef(0);
  const observerAttemptRef = useRef(0);
  const backupAttemptRef = useRef(0);
  const refreshTicketRef = useRef(0);
  const announcedRefreshRef = useRef(0);

  useEffect(() => {
    const scope = capture();
    if (!scope) return;
    const binding = bindSignal();
    const listAttempt = ++listAttemptRef.current;
    const refreshTicket = refreshTicketRef.current;
    void (async () => {
      let phase: Exclude<WebBackupListPhase, "loading"> | null = null;
      let entries: BackupEntry[] = [];
      try {
        entries = await apiClient.listBackups(scope.token, { signal: binding.signal });
        phase = entries.length > 0 ? "ready-data" : "ready-empty";
      } catch (error) {
        if (!isAbandonedRead(error, binding.signal)) {
          phase = isSqliteWebUnsupported(error) ? "unsupported" : "error";
        }
      } finally {
        binding.release();
      }
      if (phase === null || binding.signal.aborted || listAttempt !== listAttemptRef.current || !current(scope)) return;
      setBackups(phase === "ready-data" ? entries : []);
      setWebListPhase(phase);
      if (refreshTicket !== 0 && refreshTicket === refreshTicketRef.current && announcedRefreshRef.current !== refreshTicket) {
        announcedRefreshRef.current = refreshTicket;
        if (phase === "error" || phase === "unsupported") toast.error(t("selfBackup.refreshFailed"));
      }
    })();
    return () => binding.abort();
  }, [bindSignal, capture, current, listReload, t]);

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

  const retryWebList = () => {
    if (isAuthTransitionActive()) return;
    if (!capture()) return;
    setWebListPhase("loading");
    setListReload((value) => value + 1);
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
      setListReload((value) => value + 1);
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

        <div aria-busy={webListPhase === "loading"}>
          {webListPhase === "loading" ? (
            <p className="text-xs text-muted-foreground">{t("selfBackup.loadingList")}</p>
          ) : webListPhase === "unsupported" ? (
            <InlineAlert tone="warning" title={t("selfBackup.sqliteOnly")}>
              <Button type="button" size="sm" variant="outline" className="mt-2" onClick={retryWebList}>
                {t("selfBackup.retryList")}
              </Button>
            </InlineAlert>
          ) : webListPhase === "error" ? (
            <InlineAlert tone="critical" title={t("selfBackup.listLoadFailed")}>
              <Button type="button" size="sm" variant="outline" className="mt-2" onClick={retryWebList}>
                {t("selfBackup.retryList")}
              </Button>
            </InlineAlert>
          ) : webListPhase === "ready-data" ? (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-xs">
                <thead className="border-b border-border/50 text-muted-foreground">
                  <tr>
                    <th scope="col" className="pb-1.5 pr-4 font-medium">{t("selfBackup.colFilename")}</th>
                    <th scope="col" className="pb-1.5 pr-4 font-medium">{t("selfBackup.colSize")}</th>
                    <th scope="col" className="pb-1.5 pr-4 font-medium">{t("selfBackup.colCreatedAt")}</th>
                    <th scope="col" className="pb-1.5 font-medium">{t("selfBackup.colSha256")}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border/30">
                  {backups.map((entry) => (
                    <tr key={entry.filename} className="text-muted-foreground">
                      <td className="py-1.5 pr-4 font-mono">{entry.filename}</td>
                      <td className="py-1.5 pr-4">{formatBytes(entry.size)}</td>
                      <td className="py-1.5 pr-4">{formatTime(entry.createdAt)}</td>
                      <td className="py-1.5 font-mono" title={entry.sha256}>
                        {entry.sha256 ? entry.sha256.slice(0, 16) + "..." : "-"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">{t("selfBackup.noRecords")}</p>
          )}
        </div>
      </CardContent>
    </Card>
    <CronBackupEvidence phase={observerPhase} status={observer} onRetry={retryObserver} />
    </>
  );
}
