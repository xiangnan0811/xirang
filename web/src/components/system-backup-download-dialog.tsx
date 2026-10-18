import type { FormEvent } from "react";
import { Loader2, ShieldAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import { StepUpPrerequisiteNotice } from "@/components/step-up-prerequisite-notice";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Textarea } from "@/components/ui/textarea";
import type { BackupEntry } from "@/lib/api/system-api";
import { backupDownloadFileName, type BackupDownloadResult } from "@/lib/system-backup-download";
import { formatBytes } from "@/lib/utils";

export type BackupDownloadStage =
  | "confirm"
  | "selecting"
  | "verifying"
  | "preparing"
  | "receiving"
  | "committing"
  | "done"
  | "failed";

type ActiveStage = Extract<BackupDownloadStage, "selecting" | "verifying" | "preparing" | "receiving" | "committing">;

export type SystemBackupDownloadDialogProps = {
  /** The dialog is open while an entry is selected. */
  entry: BackupEntry | null;
  stage: BackupDownloadStage;
  received: number;
  total: number;
  result: BackupDownloadResult | null;
  /** Already localized, safe text. Never a raw server message. */
  errorMessage: string | null;
  reason: string;
  reasonError: string | null;
  reasonMaxLength: number;
  totpEnabled: boolean;
  nativeSave: boolean;
  /** The compatibility branch cannot hold this archive; confirming is not offered. */
  compatBlocked: boolean;
  onReasonChange: (value: string) => void;
  /** Called synchronously from the submit gesture so the directory picker keeps user activation. */
  onConfirm: () => void;
  /** Close when idle; abort and close while a download is running. */
  onCancel: () => void;
  onRetry: () => void;
  onCloseAutoFocus: DialogCloseAutoFocus;
};

const REASON_ID = "system-backup-download-reason";
const REASON_HINT_ID = "system-backup-download-reason-hint";
const REASON_ERROR_ID = "system-backup-download-reason-error";

function isActive(stage: BackupDownloadStage): stage is ActiveStage {
  return stage !== "confirm" && stage !== "done" && stage !== "failed";
}

function Fact({ label, children }: { label: string; children: string }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="break-all font-mono text-xs text-foreground">{children}</dd>
    </div>
  );
}

export function SystemBackupDownloadDialog({
  entry,
  stage,
  received,
  total,
  result,
  errorMessage,
  reason,
  reasonError,
  reasonMaxLength,
  totpEnabled,
  nativeSave,
  compatBlocked,
  onReasonChange,
  onConfirm,
  onCancel,
  onRetry,
  onCloseAutoFocus,
}: SystemBackupDownloadDialogProps) {
  const { t } = useTranslation();
  const active = isActive(stage);
  const percent = total > 0 ? Math.min(100, Math.floor((received * 100) / total)) : 0;
  const showProgress = stage === "receiving" || stage === "committing";

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (stage !== "confirm") return;
    onConfirm();
  };

  const statusText = active
    ? t(`selfBackup.download.stage.${stage}`)
    : stage === "done"
      ? t(result === "saved" ? "selfBackup.download.doneNative" : "selfBackup.download.doneCompat")
      : "";

  return (
    <Dialog open={entry !== null} onOpenChange={(open) => { if (!open) onCancel(); }}>
      <DialogContent
        size="md"
        data-dialog="system-backup-download"
        onCloseAutoFocus={onCloseAutoFocus}
        onPointerDownOutside={(event) => { if (active) event.preventDefault(); }}
        onInteractOutside={(event) => { if (active) event.preventDefault(); }}
      >
        {entry ? (
          <form onSubmit={handleSubmit} noValidate>
            <DialogHeader>
              <DialogTitle>{t("selfBackup.download.title")}</DialogTitle>
              <DialogDescription>{t("selfBackup.download.description")}</DialogDescription>
            </DialogHeader>
            <DialogBody className="space-y-4">
              <dl className="grid grid-cols-2 gap-x-4 gap-y-2 rounded-md border border-border/60 bg-muted/30 p-3 sm:grid-cols-[auto_auto_1fr]">
                <Fact label={t("selfBackup.download.factSource")}>{t(`selfBackup.download.sourceName.${entry.source}`)}</Fact>
                <Fact label={t("selfBackup.download.factEngine")}>{t(`selfBackup.engine.${entry.engine}`)}</Fact>
                <div className="col-span-2 sm:col-span-1">
                  <Fact label={t("selfBackup.download.factFile")}>{backupDownloadFileName(entry)}</Fact>
                </div>
                <Fact label={t("selfBackup.download.factSize")}>{formatBytes(entry.size)}</Fact>
                <Fact label={t("selfBackup.download.factPackaged")}>{formatBytes(entry.packagedSize)}</Fact>
              </dl>

              <div className="flex gap-2.5 rounded-md border border-warning/40 bg-warning/10 p-3 text-xs leading-relaxed text-foreground/90">
                <ShieldAlert className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />
                <div className="space-y-1">
                  <p className="font-medium">{t("selfBackup.download.sensitive")}</p>
                  <p>{t("selfBackup.download.keysSeparate")}</p>
                  <p className="text-muted-foreground">{t("selfBackup.download.notRecovery")}</p>
                </div>
              </div>

              {stage === "confirm" ? (
                <>
                  {!totpEnabled ? <StepUpPrerequisiteNotice /> : null}
                  {compatBlocked ? (
                    <InlineAlert tone="warning">{t("selfBackup.download.compatTooLarge")}</InlineAlert>
                  ) : (
                    <p className="text-xs text-muted-foreground">
                      {t(nativeSave ? "selfBackup.download.saveNative" : "selfBackup.download.saveCompat")}
                    </p>
                  )}
                  <div className="space-y-1.5">
                    <label className="text-sm font-medium" htmlFor={REASON_ID}>
                      {t("selfBackup.download.reasonLabel")}
                    </label>
                    <Textarea
                      id={REASON_ID}
                      value={reason}
                      onChange={(event) => onReasonChange(event.target.value)}
                      maxLength={reasonMaxLength}
                      rows={2}
                      placeholder={t("selfBackup.download.reasonPlaceholder")}
                      disabled={!totpEnabled || compatBlocked}
                      aria-describedby={reasonError ? `${REASON_HINT_ID} ${REASON_ERROR_ID}` : REASON_HINT_ID}
                      aria-invalid={reasonError ? true : undefined}
                    />
                    <p id={REASON_HINT_ID} className="text-xs text-muted-foreground">
                      {t("selfBackup.download.reasonHint", { max: reasonMaxLength })}
                    </p>
                    {reasonError ? (
                      <p id={REASON_ERROR_ID} role="alert" className="text-xs text-destructive">{reasonError}</p>
                    ) : null}
                  </div>
                </>
              ) : null}

              <div role="status" aria-live="polite" className={statusText ? "space-y-2" : "sr-only"}>
                {active ? (
                  <p className="flex items-center gap-2 text-sm">
                    <Loader2 className="size-4 animate-spin text-primary" aria-hidden="true" />
                    {statusText}
                  </p>
                ) : stage === "done" ? (
                  <InlineAlert tone="success" live={false}>
                    <p>{statusText}</p>
                    <p className="mt-1 text-xs">{t("selfBackup.download.doneNote")}</p>
                  </InlineAlert>
                ) : null}
              </div>

              {showProgress ? (
                <div className="space-y-1.5">
                  <div
                    role="progressbar"
                    aria-label={t("selfBackup.download.progressLabel")}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={percent}
                    aria-valuetext={t("selfBackup.download.progressValue", {
                      received: formatBytes(received),
                      total: formatBytes(total),
                      percent,
                    })}
                    className="h-2 overflow-hidden rounded-full bg-muted"
                  >
                    <div className="h-full rounded-full bg-primary transition-[width] duration-200 ease-out" style={{ width: `${percent}%` }} />
                  </div>
                  <p className="text-xs tabular-nums text-muted-foreground" aria-hidden="true">
                    {t("selfBackup.download.progressValue", {
                      received: formatBytes(received),
                      total: formatBytes(total),
                      percent,
                    })}
                  </p>
                </div>
              ) : null}

              {stage === "failed" && errorMessage ? (
                <InlineAlert tone="critical" title={t("selfBackup.download.failedTitle")}>
                  {errorMessage}
                </InlineAlert>
              ) : null}
            </DialogBody>
            {/* Every footer button has its own key. Without keys React reuses the same <button> across stages, so
                "Try again" (type=button) can become the confirm submit while its click is still being dispatched; the
                browser then runs the submit default action and starts a new proof without a fresh confirm. */}
            <DialogFooter>
              {stage === "confirm" ? (
                <>
                  <Button key="confirm-cancel" type="button" variant="outline" onClick={onCancel}>
                    {t("selfBackup.download.cancel")}
                  </Button>
                  <Button key="confirm-submit" type="submit" disabled={!totpEnabled || compatBlocked}>
                    {t("selfBackup.download.confirm")}
                  </Button>
                </>
              ) : active ? (
                <Button key="active-cancel" type="button" variant="outline" onClick={onCancel}>
                  {t("selfBackup.download.cancelActive")}
                </Button>
              ) : (
                <>
                  <Button key="settled-close" type="button" variant="outline" onClick={onCancel}>
                    {t("selfBackup.download.close")}
                  </Button>
                  {stage === "failed" ? (
                    <Button key="failed-retry" type="button" onClick={onRetry}>
                      {t("selfBackup.download.retry")}
                    </Button>
                  ) : null}
                </>
              )}
            </DialogFooter>
          </form>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
