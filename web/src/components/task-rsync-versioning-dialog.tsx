import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { GitBranch, Loader2, RotateCcw, ShieldCheck } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Select } from "@/components/ui/select";
import { apiClient } from "@/lib/api/client";
import { formatTime } from "@/lib/date-utils";
import { getRsyncVersioningErrorCode, type RsyncVersioningErrorCode } from "@/lib/api/tasks-api";
import type {
  RsyncPublicationReasonCode,
  RsyncPublicationMode,
  RsyncPublicationState,
  RsyncPublicationSummary,
  RsyncVersionedPublicationMode,
  RsyncVersioningMigrationChoice,
  RsyncVersioningPreflightResult,
  TaskRecord,
} from "@/types/domain";

type TaskRsyncVersioningDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  task: TaskRecord | null;
  token: string | null;
  onUpdated: () => void | Promise<void>;
};

type SafeNoticeCode = RsyncPublicationReasonCode | RsyncVersioningErrorCode;

function stateTone(state: RsyncPublicationState): "success" | "warning" | "destructive" | "info" | "neutral" {
  switch (state) {
    case "ready":
    case "committed":
      return "success";
    case "preparing":
    case "verifying":
      return "info";
    case "failed":
    case "blocked":
      return "destructive";
    case "preflight_required":
    case "rollback_prepared":
      return "warning";
    default:
      return "neutral";
  }
}

function defaultRequestedMode(mode?: RsyncPublicationMode): RsyncVersionedPublicationMode {
  return mode === "versioned_hardlink" || mode === "versioned_full_copy"
    ? mode
    : "versioned_hardlink";
}

export function TaskRsyncVersioningDialog({
  open,
  onOpenChange,
  task,
  token,
  onUpdated,
}: TaskRsyncVersioningDialogProps) {
  const { t } = useTranslation();
  const [requestedMode, setRequestedMode] = useState<RsyncVersionedPublicationMode>("versioned_hardlink");
  const [preflight, setPreflight] = useState<RsyncVersioningPreflightResult | null>(null);
  const [migrationChoice, setMigrationChoice] = useState<RsyncVersioningMigrationChoice | null>(null);
  const [summaryOverride, setSummaryOverride] = useState<RsyncPublicationSummary | null>(null);
  const [notice, setNotice] = useState<SafeNoticeCode | null>(null);
  const [preflighting, setPreflighting] = useState(false);
  const [activating, setActivating] = useState(false);
  const [preparingRollback, setPreparingRollback] = useState(false);

  const initialTaskRevision = task?.rsyncPublication?.taskRevision ?? "";
  const summary = summaryOverride ?? task?.rsyncPublication;
  const taskRevision = summary?.taskRevision ?? "";

  useEffect(() => {
    if (!open) {
      return;
    }
    setRequestedMode(defaultRequestedMode(task?.rsyncPublication?.mode));
    setPreflight(null);
    setMigrationChoice(null);
    setSummaryOverride(null);
    setNotice(null);
    setPreflighting(false);
    setActivating(false);
    setPreparingRollback(false);
  }, [open, task?.id, task?.rsyncPublication?.mode, initialTaskRevision]);

  const canStartMigration = Boolean(
    task &&
      token &&
      taskRevision &&
      summary?.mode === "legacy_mutable" &&
      summary.state !== "blocked" &&
      summary.state !== "rollback_prepared",
  );
  const canActivate = Boolean(
    canStartMigration &&
      preflight?.state === "ready" &&
      preflight.preflightId &&
      preflight.mode === requestedMode &&
      migrationChoice &&
      !activating,
  );
  const canPrepareRollback = Boolean(
    task &&
      token &&
      taskRevision &&
      summary &&
      summary.mode !== "legacy_mutable" &&
      summary.state !== "rollback_prepared" &&
      !preparingRollback,
  );
  const busy = preflighting || activating || preparingRollback;
  const safeNotice = useMemo(() => {
    if (!notice) {
      return null;
    }
    return t("rsyncVersioning.notice." + notice, {
      defaultValue: t("rsyncVersioning.notice.request_failed"),
    });
  }, [notice, t]);

  if (!task || !summary) {
    return null;
  }

  const modeSelectID = "rsync-versioning-mode-select-" + task.id;
  const choiceLabelID = "rsync-versioning-choice-" + task.id;

  const runPreflight = async () => {
    if (!canStartMigration || !token) {
      setNotice("unsupported");
      return;
    }
    setPreflighting(true);
    setNotice(null);
    setPreflight(null);
    setMigrationChoice(null);
    try {
      const result = await apiClient.createRsyncVersioningPreflight(token, task.id, {
        expectedTaskRevision: taskRevision,
        requestedMode,
      });
      if (result.state !== "ready" || !result.preflightId || result.mode !== requestedMode) {
        setNotice(result.reasonCode);
        return;
      }
      setPreflight(result);
    } catch (error) {
      setNotice(getRsyncVersioningErrorCode(error));
    } finally {
      setPreflighting(false);
    }
  };

  const activate = async () => {
    if (!canActivate || !token || !preflight || !migrationChoice) {
      return;
    }
    setActivating(true);
    setNotice(null);
    try {
      const result = await apiClient.activateRsyncVersioning(token, task.id, {
        expectedTaskRevision: taskRevision,
        preflightId: preflight.preflightId,
        migrationChoice,
      });
      setSummaryOverride(result.summary);
      setPreflight(null);
      setMigrationChoice(null);
      await onUpdated();
    } catch (error) {
      setNotice(getRsyncVersioningErrorCode(error));
    } finally {
      setActivating(false);
    }
  };

  const prepareRollback = async () => {
    if (!canPrepareRollback || !token) {
      return;
    }
    setPreparingRollback(true);
    setNotice(null);
    try {
      const result = await apiClient.prepareRsyncVersioningRollback(token, task.id, {
        expectedTaskRevision: taskRevision,
      });
      setSummaryOverride(result.summary);
      await onUpdated();
    } catch (error) {
      setNotice(getRsyncVersioningErrorCode(error));
    } finally {
      setPreparingRollback(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!busy) onOpenChange(nextOpen); }}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <GitBranch className="size-5 text-primary" aria-hidden />
            {t("rsyncVersioning.dialogTitle", { name: task.name ?? task.policyName })}
          </DialogTitle>
          <DialogDescription>{t("rsyncVersioning.dialogDescription")}</DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border/70 bg-muted/30 px-3 py-2.5">
            <div>
              <p className="text-xs text-muted-foreground">{t("rsyncVersioning.currentMode")}</p>
              <p className="text-sm font-medium">{t("rsyncVersioning.mode." + summary.mode)}</p>
            </div>
            <Badge tone={stateTone(summary.state)}>
              {t("rsyncVersioning.currentState", { state: t("rsyncVersioning.state." + summary.state) })}
            </Badge>
          </div>

          {summary.seedFullCopyRequired ? (
            <InlineAlert tone="warning">{t("rsyncVersioning.seedFullCopyRequired")}</InlineAlert>
          ) : null}

          {summary.state === "blocked" ? (
            <InlineAlert tone="critical">{t("rsyncVersioning.reason." + summary.reasonCode)}</InlineAlert>
          ) : null}

          {safeNotice ? <InlineAlert tone="critical">{safeNotice}</InlineAlert> : null}

          {canStartMigration ? (
            <section className="space-y-3">
              <div>
                <label htmlFor={modeSelectID} className="mb-1 block text-sm font-medium">
                  {t("rsyncVersioning.modeLabel")}
                </label>
                <Select
                  id={modeSelectID}
                  containerClassName="w-full"
                  value={requestedMode}
                  disabled={busy}
                  onChange={(event) => {
                    setRequestedMode(event.target.value as RsyncVersionedPublicationMode);
                    setPreflight(null);
                    setMigrationChoice(null);
                    setNotice(null);
                  }}
                >
                  <option value="versioned_hardlink">{t("rsyncVersioning.mode.versioned_hardlink")}</option>
                  <option value="versioned_full_copy">{t("rsyncVersioning.mode.versioned_full_copy")}</option>
                </Select>
                <p className="mt-1 text-xs text-muted-foreground">{t("rsyncVersioning.modeHint")}</p>
              </div>

              <Button type="button" variant="outline" onClick={() => void runPreflight()} disabled={busy}>
                {preflighting ? <Loader2 className="mr-1.5 size-4 animate-spin" aria-hidden /> : <ShieldCheck className="mr-1.5 size-4" aria-hidden />}
                {preflighting ? t("rsyncVersioning.preflighting") : t("rsyncVersioning.runPreflight")}
              </Button>
            </section>
          ) : null}

          {preflight ? (
            <section className="space-y-3 rounded-lg border border-success/30 bg-success/5 p-3" aria-labelledby={choiceLabelID}>
              <div className="space-y-1">
                <p className="font-medium text-success">{t("rsyncVersioning.preflightReady")}</p>
                <p className="text-xs text-muted-foreground">
                  {t("rsyncVersioning.preflightExpires", { time: formatTime(preflight.expiresAt) })}
                </p>
                <div className="flex flex-wrap gap-2 pt-1">
                  <Badge tone={preflight.capacityEstimate === "available" ? "success" : preflight.capacityEstimate === "constrained" ? "warning" : "neutral"}>
                    {t("rsyncVersioning.capacityEstimate", { value: t("rsyncVersioning.estimate." + preflight.capacityEstimate) })}
                  </Badge>
                  <Badge tone={preflight.inodeEstimate === "available" ? "success" : preflight.inodeEstimate === "constrained" ? "warning" : "neutral"}>
                    {t("rsyncVersioning.inodeEstimate", { value: t("rsyncVersioning.estimate." + preflight.inodeEstimate) })}
                  </Badge>
                </div>
              </div>

              <div className="space-y-2" role="radiogroup" aria-labelledby={choiceLabelID}>
                <p id={choiceLabelID} className="text-sm font-medium">
                  {t("rsyncVersioning.choiceLabel")}
                </p>
                <label className="flex cursor-pointer items-start gap-2.5 rounded-md border border-border/70 p-3 transition-colors has-[:checked]:border-primary/50 has-[:checked]:bg-primary/5">
                  <input
                    type="radio"
                    name={"rsync-versioning-choice-" + task.id}
                    aria-label={t("rsyncVersioning.choiceFirstNewPoint")}
                    checked={migrationChoice === "first_new_point"}
                    disabled={busy}
                    onChange={() => setMigrationChoice("first_new_point")}
                  />
                  <span>
                    <span className="block text-sm font-medium">{t("rsyncVersioning.choiceFirstNewPoint")}</span>
                    <span className="mt-0.5 block text-xs text-muted-foreground">{t("rsyncVersioning.choiceFirstNewPointDescription")}</span>
                  </span>
                </label>
                <label className="flex cursor-pointer items-start gap-2.5 rounded-md border border-border/70 p-3 transition-colors has-[:checked]:border-primary/50 has-[:checked]:bg-primary/5">
                  <input
                    type="radio"
                    name={"rsync-versioning-choice-" + task.id}
                    aria-label={t("rsyncVersioning.choiceImportedBaseline")}
                    checked={migrationChoice === "imported_baseline"}
                    disabled={busy}
                    onChange={() => setMigrationChoice("imported_baseline")}
                  />
                  <span>
                    <span className="block text-sm font-medium">{t("rsyncVersioning.choiceImportedBaseline")}</span>
                    <span className="mt-0.5 block text-xs text-muted-foreground">{t("rsyncVersioning.choiceImportedBaselineDescription")}</span>
                  </span>
                </label>
              </div>
            </section>
          ) : null}

          {summary.mode !== "legacy_mutable" ? (
            <InlineAlert tone="info">{t("rsyncVersioning.managedTreeNotice")}</InlineAlert>
          ) : null}
        </DialogBody>

        <DialogFooter className="justify-between">
          <div>
            {canPrepareRollback ? (
              <Button type="button" variant="outline" onClick={() => void prepareRollback()} disabled={busy}>
                {preparingRollback ? <Loader2 className="mr-1.5 size-4 animate-spin" aria-hidden /> : <RotateCcw className="mr-1.5 size-4" aria-hidden />}
                {preparingRollback ? t("rsyncVersioning.preparingRollback") : t("rsyncVersioning.prepareRollback")}
              </Button>
            ) : null}
          </div>
          <Button type="button" onClick={() => void activate()} disabled={!canActivate}>
            {activating ? <Loader2 className="mr-1.5 size-4 animate-spin" aria-hidden /> : <ShieldCheck className="mr-1.5 size-4" aria-hidden />}
            {activating ? t("rsyncVersioning.activating") : t("rsyncVersioning.activate")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
