import { useEffect, useRef, useState } from "react";
import { ShieldAlert, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/confirm-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Stepper } from "@/components/ui/stepper";
import type {
  RecoveryConflictPolicy,
  RecoverySecurityFindingCategory,
  RecoveryTargetMode,
} from "@/lib/api/backup-recovery-api";
import type { AuthRole } from "@/context/auth-context.shared";

import { RecoveryImpactPanel } from "./recovery-impact-panel";
import { RecoveryJobPanel } from "./recovery-job-panel";
import type { useBackupRecovery } from "./use-backup-recovery";
import { ContentTransportGuidance } from "./content-transport-guidance";

type BackupRecoveryController = ReturnType<typeof useBackupRecovery>;

export interface RecoveryPlanWizardProps {
  open: boolean;
  recovery: BackupRecoveryController;
  authRole?: AuthRole | null;
  onOpenChange: (open: boolean) => void;
}

const PHASE_STEP: Record<string, number> = {
  closed: 0,
  target: 0,
  creating: 0,
  preflighting: 1,
  security: 2,
  impact: 3,
  authorizing_write: 3,
  executing: 4,
  progress: 4,
  delete_authorization: 5,
  verification: 6,
  result: 6,
  unavailable: 6,
  error: 6,
};

function announcement(value: string | null, t: (key: string) => string): string {
  if (value === null) return "";
  if (value.startsWith("job:")) return t(`backupAssets.recovery.job.outcome.${value.slice(4)}`);
  return t(`backupAssets.recovery.announcement.${value}`);
}

function SecurityOverrideForm({
  categories,
  onOverride,
}: {
  categories: RecoverySecurityFindingCategory[];
  onOverride: (category: RecoverySecurityFindingCategory, reason: string, confirmed: boolean) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [category, setCategory] = useState<RecoverySecurityFindingCategory>(categories[0]!);
  const [reason, setReason] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const submit = () => {
    const submittedReason = reason.trim();
    void onOverride(category, submittedReason, true);
  };

  return (
    <div className="space-y-3 rounded-lg border border-destructive/30 bg-destructive/5 p-3">
      <label className="grid gap-1 text-xs font-medium">
        {t("backupAssets.recovery.security.category")}
        <select
          value={category}
          onChange={(event) => setCategory(event.target.value as RecoverySecurityFindingCategory)}
          className="h-9 rounded-md border border-input bg-background px-2 text-sm"
        >
          {categories.map((value) => (
            <option key={value} value={value}>{t(`backupAssets.recovery.security.finding.${value}`)}</option>
          ))}
        </select>
      </label>
      <label className="grid gap-1 text-xs font-medium">
        {t("backupAssets.recovery.security.reason")}
        <textarea
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          className="min-h-24 rounded-md border border-input bg-background px-2 py-2 text-sm"
        />
      </label>
      <label className="flex items-start gap-2 text-xs">
        <Checkbox checked={confirmed} onCheckedChange={(value) => setConfirmed(value === true)} />
        <span>{t("backupAssets.recovery.security.confirm")}</span>
      </label>
      <Button
        type="button"
        variant="destructive"
        disabled={!confirmed || reason.trim() === ""}
        onClick={submit}
      >
        {t("backupAssets.recovery.security.override")}
      </Button>
    </div>
  );
}

function WriteAuthorizationControl({
  authorizing,
  authorized,
  onAuthorize,
  onExecute,
}: {
  authorizing: boolean;
  authorized: boolean;
  onAuthorize: (reason: string) => Promise<void>;
  onExecute: () => Promise<void>;
}) {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");

  if (authorized) {
    return (
      <InlineAlert tone="success" live={false} title={t("backupAssets.recovery.write.authorized")}>
        <Button type="button" size="sm" onClick={() => void onExecute()}>{t("backupAssets.recovery.write.execute")}</Button>
      </InlineAlert>
    );
  }

  const submit = () => {
    const submittedReason = reason.trim();
    void onAuthorize(submittedReason);
  };
  return (
    <div className="space-y-2">
      <label className="grid gap-1 text-xs font-medium">
        {t("backupAssets.recovery.write.reason")}
        <textarea
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          className="min-h-24 rounded-md border border-input bg-background px-2 py-2 text-sm"
        />
      </label>
      <Button type="button" loading={authorizing} disabled={reason.trim() === ""} onClick={submit}>
        {t("backupAssets.recovery.write.authorize")}
      </Button>
    </div>
  );
}

function DeleteAuthorizationForm({
  onAuthorize,
}: {
  onAuthorize: (reason: string, confirmed: boolean) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const submit = () => {
    const submittedReason = reason.trim();
    void onAuthorize(submittedReason, true);
  };

  return (
    <>
      <label className="grid gap-1 text-xs font-medium">
        {t("backupAssets.recovery.delete.reason")}
        <textarea
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          className="min-h-24 rounded-md border border-input bg-background px-2 py-2 text-sm"
        />
      </label>
      <label className="flex items-start gap-2 text-xs">
        <Checkbox checked={confirmed} onCheckedChange={(value) => setConfirmed(value === true)} />
        <span>{t("backupAssets.recovery.delete.confirm")}</span>
      </label>
      <Button type="button" variant="destructive" disabled={!confirmed || reason.trim() === ""} onClick={submit}>
        {t("backupAssets.recovery.delete.authorize")}
      </Button>
    </>
  );
}

export function RecoveryPlanWizard({ open, recovery, authRole = null, onOpenChange }: RecoveryPlanWizardProps) {
  const { t } = useTranslation();
  const { state } = recovery;
  const phaseHeadingRef = useRef<HTMLHeadingElement>(null);
  const errorFocusRef = useRef<HTMLDivElement>(null);
  const [targetMode, setTargetMode] = useState<RecoveryTargetMode>("isolated");
  const [targetNodeId, setTargetNodeId] = useState("");
  const [targetRootId, setTargetRootId] = useState("");
  const [conflictPolicy, setConflictPolicy] = useState<RecoveryConflictPolicy>("fail_on_conflict");
  const [inPlaceConfirmOpen, setInPlaceConfirmOpen] = useState(false);

  const showError = state.phase === "unavailable" || state.error !== null;

  useEffect(() => {
    if (!open) return;
    if (showError) {
      errorFocusRef.current?.focus();
      return;
    }
    phaseHeadingRef.current?.focus();
  }, [open, showError, state.phase]);

  const changeMode = (mode: RecoveryTargetMode) => {
    setTargetMode(mode);
    if (mode === "isolated" && conflictPolicy === "exact_mirror") setConflictPolicy("fail_on_conflict");
  };

  const submitTarget = async () => {
    recovery.setTarget({
      targetMode,
      targetNodeId: Number(targetNodeId),
      targetRootId: targetRootId.trim(),
      conflictPolicy,
    });
    await recovery.createPlan();
  };

  const requestCreate = () => {
    if (targetMode === "in_place") {
      setInPlaceConfirmOpen(true);
      return;
    }
    void submitTarget();
  };

  const close = () => {
    recovery.dismiss();
    onOpenChange(false);
  };

  const steps = [
    t("backupAssets.recovery.steps.target"),
    t("backupAssets.recovery.steps.preflight"),
    t("backupAssets.recovery.steps.security"),
    t("backupAssets.recovery.steps.impact"),
    t("backupAssets.recovery.steps.progress"),
    t("backupAssets.recovery.steps.delete"),
    t("backupAssets.recovery.steps.result"),
  ];

  return (
    <>
    <Dialog open={open} onOpenChange={(next) => (next ? onOpenChange(true) : close())}>
      <DialogContent
        size="lg"
        className="flex flex-col overflow-hidden p-0"
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          if (showError) {
            errorFocusRef.current?.focus();
            return;
          }
          phaseHeadingRef.current?.focus();
        }}
      >
        <DialogHeader className="border-b border-border pr-10">
          <DialogTitle>{t("backupAssets.recovery.title")}</DialogTitle>
          <DialogDescription>{t("backupAssets.recovery.description", { count: state.selection.length })}</DialogDescription>
          <Button type="button" variant="ghost" size="icon" className="absolute right-3 top-3" aria-label={t("backupAssets.recovery.close")} onClick={close}>
            <X className="size-4" aria-hidden />
          </Button>
          <Stepper
            className="mt-3 overflow-hidden"
            aria-label={t("backupAssets.recovery.steps.label")}
            steps={steps}
            current={PHASE_STEP[state.phase] ?? 0}
          />
        </DialogHeader>

        <DialogBody className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden px-4">
          {(state.phase === "target" || state.phase === "creating") ? (
            state.plan === null ? (
              <section className="space-y-4" aria-labelledby="recovery-target-title">
                <h2 ref={phaseHeadingRef} tabIndex={-1} id="recovery-target-title" className="text-sm font-semibold outline-none">
                  {t("backupAssets.recovery.target.title")}
                </h2>
                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="grid gap-1 text-xs font-medium">
                    {t("backupAssets.recovery.target.mode")}
                    <select
                      value={targetMode}
                      onChange={(event) => changeMode(event.target.value === "in_place" ? "in_place" : "isolated")}
                      disabled={state.phase === "creating"}
                      className="h-9 rounded-md border border-input bg-background px-2 text-sm"
                    >
                      <option value="isolated">{t("backupAssets.recovery.target.isolated")}</option>
                      <option value="in_place">{t("backupAssets.recovery.target.inPlace")}</option>
                    </select>
                  </label>
                  <label className="grid gap-1 text-xs font-medium">
                    {t("backupAssets.recovery.target.node")}
                    <input
                      type="number"
                      min={1}
                      value={targetNodeId}
                      onChange={(event) => setTargetNodeId(event.target.value)}
                      disabled={state.phase === "creating"}
                      className="h-9 rounded-md border border-input bg-background px-2 text-sm"
                    />
                  </label>
                  <label className="grid gap-1 text-xs font-medium">
                    {t("backupAssets.recovery.target.root")}
                    <input
                      value={targetRootId}
                      maxLength={32}
                      onChange={(event) => setTargetRootId(event.target.value)}
                      disabled={state.phase === "creating"}
                      className="h-9 rounded-md border border-input bg-background px-2 text-sm"
                    />
                  </label>
                  <label className="grid gap-1 text-xs font-medium">
                    {t("backupAssets.recovery.target.conflict")}
                    <select
                      value={conflictPolicy}
                      onChange={(event) => setConflictPolicy(event.target.value as RecoveryConflictPolicy)}
                      disabled={state.phase === "creating"}
                      className="h-9 rounded-md border border-input bg-background px-2 text-sm"
                    >
                      <option value="fail_on_conflict">{t("backupAssets.recovery.target.fail")}</option>
                      <option value="skip_existing">{t("backupAssets.recovery.target.skip")}</option>
                      <option value="overwrite_selected">{t("backupAssets.recovery.target.overwrite")}</option>
                      {targetMode === "in_place" ? <option value="exact_mirror">{t("backupAssets.recovery.target.exactMirror")}</option> : null}
                    </select>
                  </label>
                </div>
                <Button
                  type="button"
                  loading={state.phase === "creating"}
                  disabled={targetNodeId === "" || targetRootId.trim() === ""}
                  onClick={requestCreate}
                >
                  {t("backupAssets.recovery.target.create")}
                </Button>
              </section>
            ) : (
              <section className="space-y-3" aria-labelledby="recovery-preflight-title">
                <h2 ref={phaseHeadingRef} tabIndex={-1} id="recovery-preflight-title" className="text-sm font-semibold outline-none">
                  {t("backupAssets.recovery.preflight.title")}
                </h2>
                <p className="text-xs text-muted-foreground">{t("backupAssets.recovery.preflight.description")}</p>
                <Button type="button" onClick={() => void recovery.runPreflight()}>{t("backupAssets.recovery.preflight.run")}</Button>
              </section>
            )
          ) : null}

          {state.phase === "preflighting" ? (
            <section aria-labelledby="recovery-preflighting-title" className="space-y-2">
              <h2 ref={phaseHeadingRef} tabIndex={-1} id="recovery-preflighting-title" className="text-sm font-semibold outline-none">
                {t("backupAssets.recovery.preflight.running")}
              </h2>
              <p className="text-xs text-muted-foreground">{t("backupAssets.recovery.preflight.wait")}</p>
            </section>
          ) : null}

          {state.phase === "security" && state.preflight !== null ? (
            <section aria-labelledby="recovery-security-title" className="space-y-4">
              <h2 ref={phaseHeadingRef} tabIndex={-1} id="recovery-security-title" className="text-sm font-semibold outline-none">
                {t("backupAssets.recovery.security.title")}
              </h2>
              {state.preflight.security.overridableCategories.length === 0 ? (
                <InlineAlert tone="critical" live={false} icon={<ShieldAlert className="size-4" aria-hidden />}>
                  {t("backupAssets.recovery.security.notOverridable")}
                </InlineAlert>
              ) : (
                <SecurityOverrideForm
                  key={state.preflight.preflightId}
                  categories={state.preflight.security.overridableCategories}
                  onOverride={recovery.overrideSecurity}
                />
              )}
            </section>
          ) : null}

          {(state.phase === "impact" || state.phase === "authorizing_write") && state.preflight !== null ? (
            <section className="space-y-4" aria-labelledby="recovery-authority-title">
              <h2 ref={phaseHeadingRef} tabIndex={-1} id="recovery-authority-title" className="sr-only outline-none">
                {t("backupAssets.recovery.write.title")}
              </h2>
              <RecoveryImpactPanel preflight={state.preflight} />
              <WriteAuthorizationControl
                key={JSON.stringify([state.plan?.id ?? "", state.preflight.preflightId, state.writeGrant?.id ?? ""])}
                authorizing={state.phase === "authorizing_write"}
                authorized={state.writeGrant !== null}
                onAuthorize={recovery.authorizeWrite}
                onExecute={recovery.execute}
              />
            </section>
          ) : null}

          {state.phase === "delete_authorization" && state.job?.deleteCheckpoint !== null && state.job?.deleteCheckpoint !== undefined ? (
            <section aria-labelledby="recovery-delete-title" className="space-y-4">
              <h2 ref={phaseHeadingRef} tabIndex={-1} id="recovery-delete-title" className="text-sm font-semibold outline-none">
                {t("backupAssets.recovery.delete.title")}
              </h2>
              <InlineAlert tone="critical" live={false}>{t("backupAssets.recovery.delete.warning")}</InlineAlert>
              <DeleteAuthorizationForm
                key={state.job.deleteCheckpoint.id}
                onAuthorize={recovery.authorizeExactMirrorDelete}
              />
            </section>
          ) : null}

          {(["executing", "progress", "verification", "result"] as const).includes(state.phase as never) && state.job !== null ? (
            <section aria-labelledby="recovery-job-phase-title">
              <h2 ref={phaseHeadingRef} tabIndex={-1} id="recovery-job-phase-title" className="sr-only outline-none">
                {t(`backupAssets.recovery.phase.${state.phase}`)}
              </h2>
              <RecoveryJobPanel
                job={state.job}
                itemPage={state.itemPage}
                resultPage={state.resultPage}
                onLoadItems={(page, pageSize) => void recovery.loadJobItems(page, pageSize)}
                onLoadResults={(page, pageSize) => void recovery.loadJobResults(page, pageSize)}
                onDownloadResult={(resultId) => void recovery.downloadResult(resultId)}
                onRetainResults={(deadline) => void recovery.retainResults(deadline)}
                onCleanupResults={() => void recovery.cleanupResults()}
              />
            </section>
          ) : null}

          {state.phase === "unavailable" ? (
            <div ref={errorFocusRef} tabIndex={-1} data-testid="recovery-error" className="outline-none">
              <InlineAlert tone="critical" title={t("backupAssets.recovery.unavailable.title")}>
                {t("backupAssets.recovery.unavailable.body")}
              </InlineAlert>
            </div>
          ) : null}
          {state.error !== null && state.phase !== "unavailable" ? (
            <div ref={errorFocusRef} tabIndex={-1} data-testid="recovery-error" className="outline-none">
              <InlineAlert tone="critical">
                {t(state.error === "secure_transport_required"
                  ? "backupAssets.errors.secureTransportRequired"
                  : `backupAssets.recovery.error.${state.error}`)}
                {state.error === "secure_transport_required" ? <ContentTransportGuidance authRole={authRole} /> : null}
              </InlineAlert>
            </div>
          ) : null}

          <p id="recovery-announcement" data-testid="recovery-announcement" aria-live="polite" aria-atomic="true" className="sr-only">
            {announcement(state.announcement, t)}
          </p>
        </DialogBody>

        <DialogFooter className="flex-wrap justify-between px-4">
          <Button type="button" size="sm" variant="ghost" onClick={close}>{t("backupAssets.recovery.close")}</Button>
          {(state.plan !== null || state.job !== null) && !["result", "unavailable"].includes(state.phase) ? (
            <Button type="button" size="sm" variant="outline" onClick={() => void recovery.cancelRecovery()}>
              {t("backupAssets.recovery.cancel")}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
    <AlertDialog open={inPlaceConfirmOpen} onOpenChange={setInPlaceConfirmOpen}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("backupAssets.recovery.target.inPlaceConfirmTitle")}</AlertDialogTitle>
          <AlertDialogDescription>{t("backupAssets.recovery.target.inPlaceConfirmDescription")}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => {
              setInPlaceConfirmOpen(false);
              void submitTarget();
            }}
          >
            {t("backupAssets.recovery.target.inPlaceConfirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
    </>
  );
}
