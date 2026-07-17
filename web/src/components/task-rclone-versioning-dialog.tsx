import { useEffect, useMemo, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { Cloud, DatabaseZap, KeyRound, Layers3, Loader2, RotateCcw, ShieldCheck } from "lucide-react";

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
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { apiClient } from "@/lib/api/client";
import { getRcloneVersioningErrorCode, type RcloneVersioningErrorCode } from "@/lib/api/tasks-api";
import { formatTime } from "@/lib/date-utils";
import type {
  RcloneBindingSetupResult,
  RcloneEncryptionProfile,
  RclonePublicationReasonCode,
  RclonePublicationState,
  RclonePublicationSummary,
  RcloneVersionedPublicationMode,
  RcloneVersioningMigrationChoice,
  RcloneVersioningPreflightResult,
  TaskRecord,
} from "@/types/domain";

type TaskRcloneVersioningDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  task: TaskRecord | null;
  token: string | null;
  onUpdated: () => void | Promise<void>;
};

type BusyAction = "portable" | "native-setup" | "native" | "preflight" | "activate" | "rollback" | null;
type NoticeCode = RclonePublicationReasonCode | RcloneVersioningErrorCode;
type BootstrapMode = "workload_chain" | "static_sts_bootstrap";

function stateTone(state: RclonePublicationState): "success" | "warning" | "destructive" | "info" | "neutral" {
  switch (state) {
    case "ready":
    case "committed":
      return "success";
    case "preparing":
    case "verifying":
    case "capability_settling":
      return "info";
    case "degraded":
    case "preflight_required":
    case "credential_setup_required":
    case "rollback_prepared":
      return "warning";
    case "at_risk":
    case "failed":
    case "blocked":
      return "destructive";
    default:
      return "neutral";
  }
}

export function TaskRcloneVersioningDialog({
  open,
  onOpenChange,
  task,
  token,
  onUpdated,
}: TaskRcloneVersioningDialogProps) {
  const { t } = useTranslation();
  const [selectedMode, setSelectedMode] = useState<RcloneVersionedPublicationMode>("versioned_prefix");
  const [summaryOverride, setSummaryOverride] = useState<RclonePublicationSummary | null>(null);
  const [preflight, setPreflight] = useState<RcloneVersioningPreflightResult | null>(null);
  const [migrationChoice, setMigrationChoice] = useState<RcloneVersioningMigrationChoice>("first_new_point");
  const [confirmImportedBaseline, setConfirmImportedBaseline] = useState(false);
  const [notice, setNotice] = useState<NoticeCode | null>(null);
  const [busy, setBusy] = useState<BusyAction>(null);

  const [targetRemote, setTargetRemote] = useState("");
  const [managedRootLocator, setManagedRootLocator] = useState("");
  const [boundConfig, setBoundConfig] = useState("");

  const [nativeSetup, setNativeSetup] = useState<RcloneBindingSetupResult | null>(null);
  const [region, setRegion] = useState("");
  const [bucket, setBucket] = useState("");
  const [managedPrefix, setManagedPrefix] = useState("");
  const [roleArn, setRoleArn] = useState("");
  const [bootstrapMode, setBootstrapMode] = useState<BootstrapMode>("workload_chain");
  const [accessKeyId, setAccessKeyId] = useState("");
  const [secretAccessKey, setSecretAccessKey] = useState("");
  const [encryptionProfile, setEncryptionProfile] = useState<Exclude<RcloneEncryptionProfile, "none">>("sse_s3");
  const [kmsKeyArn, setKmsKeyArn] = useState("");

  const initialSummary = task?.rclonePublication;
  const initialTaskRevision = initialSummary?.taskRevision ?? "";
  const initialBindingRevision = initialSummary?.bindingRevision ?? "";
  const initialMode = initialSummary?.mode;
  const summary = summaryOverride ?? initialSummary;
  const taskRevision = summary?.taskRevision ?? "";
  const bindingRevision = summary?.bindingRevision ?? "0";

  useEffect(() => {
    if (!open) return;
    setSelectedMode(initialMode === "native_object_versions" ? "native_object_versions" : "versioned_prefix");
    setSummaryOverride(null);
    setPreflight(null);
    setMigrationChoice("first_new_point");
    setConfirmImportedBaseline(false);
    setNotice(null);
    setBusy(null);
    setTargetRemote("");
    setManagedRootLocator("");
    setBoundConfig("");
    setNativeSetup(null);
    setRegion("");
    setBucket("");
    setManagedPrefix("");
    setRoleArn("");
    setBootstrapMode("workload_chain");
    setAccessKeyId("");
    setSecretAccessKey("");
    setEncryptionProfile("sse_s3");
    setKmsKeyArn("");
  }, [open, task?.id, initialMode, initialTaskRevision, initialBindingRevision]);

  const noticeText = useMemo(() => {
    if (!notice) return null;
    return t(`rcloneVersioning.reason.${notice}`, {
      defaultValue: t("rcloneVersioning.reason.request_failed"),
    });
  }, [notice, t]);

  if (!task || !summary) return null;

  const isBusy = busy !== null;
  const modeLocked = summary.mode !== "legacy_mutable" && summary.mode !== selectedMode;
  const preflightExpired = preflight?.expiresAt ? Date.parse(preflight.expiresAt) <= Date.now() : true;
  const canRunPreflight = Boolean(
    token && taskRevision && summary.mode === selectedMode && bindingRevision !== "0" && !isBusy,
  );
  const canActivate = Boolean(
    token && preflight && !preflightExpired && preflight.summary.state === "ready" &&
      preflight.summary.mode === selectedMode &&
      (migrationChoice === "first_new_point" || confirmImportedBaseline) && !isBusy,
  );

  const changeMode = (mode: RcloneVersionedPublicationMode) => {
    if (isBusy || (summary.mode !== "legacy_mutable" && summary.mode !== mode)) return;
    setSelectedMode(mode);
    setPreflight(null);
    setMigrationChoice("first_new_point");
    setConfirmImportedBaseline(false);
    setNotice(null);
  };

  const handleModeKeys = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight" && event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
    event.preventDefault();
    const nextMode = selectedMode === "versioned_prefix" ? "native_object_versions" : "versioned_prefix";
    const nextButton = event.currentTarget.querySelector<HTMLButtonElement>(`[data-rclone-mode="${nextMode}"]`);
    if (nextButton?.disabled) return;
    changeMode(nextMode);
    nextButton?.focus();
  };

  const savePortableBinding = async () => {
    if (!token || !taskRevision || !targetRemote.trim() || !managedRootLocator.trim() || !boundConfig) {
      setNotice("admission_blocked");
      return;
    }
    setBusy("portable");
    setNotice(null);
    try {
      const setup = await apiClient.createRclonePortableBindingSetup(token, task.id, {
        expectedTaskRevision: taskRevision,
      });
      const next = await apiClient.setRclonePortableBinding(token, task.id, {
        expectedTaskRevision: taskRevision,
        expectedBindingRevision: bindingRevision,
        setupId: setup.setupId,
        targetRemote: targetRemote.trim(),
        managedRootLocator: managedRootLocator.trim(),
        boundConfig,
      });
      setSummaryOverride(next);
      setPreflight(null);
      await onUpdated();
    } catch (error) {
      setNotice(getRcloneVersioningErrorCode(error));
    } finally {
      setBoundConfig("");
      setBusy(null);
    }
  };

  const createNativeSetup = async () => {
    if (!token || !taskRevision) return;
    setBusy("native-setup");
    setNotice(null);
    try {
      const setup = await apiClient.createRcloneNativeBindingSetup(token, task.id, {
        expectedTaskRevision: taskRevision,
      });
      setNativeSetup(setup);
    } catch (error) {
      setNotice(getRcloneVersioningErrorCode(error));
    } finally {
      setBusy(null);
    }
  };

  const saveNativeBinding = async () => {
    if (!token || !taskRevision || !nativeSetup?.setupId || !region.trim() || !bucket.trim() ||
        !managedPrefix.trim() || !roleArn.trim() || (bootstrapMode === "static_sts_bootstrap" && (!accessKeyId || !secretAccessKey)) ||
        (encryptionProfile === "sse_kms_cmk" && !kmsKeyArn.trim())) {
      setNotice("admission_blocked");
      return;
    }
    setBusy("native");
    setNotice(null);
    try {
      const next = await apiClient.setRcloneNativeBinding(token, task.id, {
        expectedTaskRevision: taskRevision,
        expectedBindingRevision: bindingRevision,
        setupId: nativeSetup.setupId,
        region: region.trim(),
        bucket: bucket.trim(),
        managedPrefix: managedPrefix.trim(),
        roleArn: roleArn.trim(),
        bootstrap: bootstrapMode === "workload_chain"
          ? { mode: "workload_chain" }
          : { mode: "static_sts_bootstrap", accessKeyId, secretAccessKey },
        encryptionProfile,
        kmsKeyArn: encryptionProfile === "sse_kms_cmk" ? kmsKeyArn.trim() : undefined,
      });
      setSummaryOverride(next);
      setNativeSetup(null);
      setPreflight(null);
      await onUpdated();
    } catch (error) {
      setNotice(getRcloneVersioningErrorCode(error));
    } finally {
      setAccessKeyId("");
      setSecretAccessKey("");
      setKmsKeyArn("");
      setBusy(null);
    }
  };

  const runPreflight = async () => {
    if (!canRunPreflight || !token) return;
    setBusy("preflight");
    setNotice(null);
    setPreflight(null);
    try {
      const result = await apiClient.createRcloneVersioningPreflight(token, task.id, {
        expectedTaskRevision: taskRevision,
        requestedMode: selectedMode,
      });
      setSummaryOverride(result.summary);
      setPreflight(result);
      if (result.summary.state !== "ready") setNotice(result.summary.reasonCode);
    } catch (error) {
      setNotice(getRcloneVersioningErrorCode(error));
    } finally {
      setBusy(null);
    }
  };

  const activate = async () => {
    if (!canActivate || !token || !preflight) return;
    setBusy("activate");
    setNotice(null);
    try {
      const result = await apiClient.activateRcloneVersioning(token, task.id, {
        expectedTaskRevision: taskRevision,
        preflightId: preflight.preflightId,
        migrationChoice,
      });
      setSummaryOverride(result.summary);
      setPreflight(null);
      setConfirmImportedBaseline(false);
      await onUpdated();
    } catch (error) {
      setNotice(getRcloneVersioningErrorCode(error));
    } finally {
      setBusy(null);
    }
  };

  const rollback = async (clean: boolean) => {
    if (!token || !taskRevision || !bindingRevision || isBusy) return;
    setBusy("rollback");
    setNotice(null);
    try {
      const result = clean
        ? await apiClient.cleanRollbackRcloneVersioning(token, task.id, {
            expectedTaskRevision: taskRevision,
            expectedBindingRevision: bindingRevision,
          })
        : await apiClient.prepareRcloneVersioningRollback(token, task.id, {
            expectedTaskRevision: taskRevision,
            expectedBindingRevision: bindingRevision,
          });
      setSummaryOverride(result.summary);
      await onUpdated();
    } catch (error) {
      setNotice(getRcloneVersioningErrorCode(error));
    } finally {
      setBusy(null);
    }
  };

  const modeOptions: Array<{ value: RcloneVersionedPublicationMode; icon: typeof Layers3 }> = [
    { value: "versioned_prefix", icon: Layers3 },
    { value: "native_object_versions", icon: DatabaseZap },
  ];

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!isBusy) onOpenChange(next); }}>
      <DialogContent size="lg">
        <DialogHeader className="border-b border-border/60 pb-4">
          <DialogTitle className="flex items-center gap-2">
            <Cloud className="size-5 text-primary" aria-hidden />
            {t("rcloneVersioning.dialogTitle", { name: task.name ?? task.policyName })}
          </DialogTitle>
          <DialogDescription>{t("rcloneVersioning.dialogDescription")}</DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-5">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border/60 pb-4">
            <div>
              <p className="text-xs uppercase tracking-wide text-muted-foreground">{t("rcloneVersioning.currentMode")}</p>
              <p className="mt-1 text-sm font-medium">{t(`rcloneVersioning.mode.${summary.mode}`)}</p>
            </div>
            <Badge tone={stateTone(summary.state)}>{t(`rcloneVersioning.state.${summary.state}`)}</Badge>
          </div>

          {summary.state === "blocked" ? (
            <InlineAlert tone="critical">{t("rcloneVersioning.blockedMessage")}</InlineAlert>
          ) : null}
          {noticeText ? <InlineAlert tone="critical">{noticeText}</InlineAlert> : null}

          <section className="space-y-3" aria-labelledby={`rclone-mode-${task.id}`}>
            <div>
              <h3 id={`rclone-mode-${task.id}`} className="text-sm font-semibold">{t("rcloneVersioning.modeLabel")}</h3>
              <p className="mt-0.5 text-xs text-muted-foreground">{t("rcloneVersioning.modeHint")}</p>
            </div>
            <div
              role="radiogroup"
              tabIndex={-1}
              aria-labelledby={`rclone-mode-${task.id}`}
              className="grid grid-cols-2 gap-2"
              onKeyDown={handleModeKeys}
            >
              {modeOptions.map(({ value, icon: Icon }) => {
                const checked = selectedMode === value;
                const disabled = isBusy || (summary.mode !== "legacy_mutable" && summary.mode !== value);
                return (
                  <button
                    key={value}
                    data-rclone-mode={value}
                    type="button"
                    role="radio"
                    aria-checked={checked}
                    tabIndex={checked ? 0 : -1}
                    disabled={disabled}
                    onClick={() => changeMode(value)}
                    className={`flex min-h-14 items-center gap-2 border px-3 text-left transition-colors focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/35 ${checked ? "border-primary bg-primary/5 text-foreground" : "border-border/70 text-muted-foreground hover:bg-muted/50"}`}
                  >
                    <Icon className="size-4 shrink-0" aria-hidden />
                    <span className="text-sm font-medium">{t(`rcloneVersioning.mode.${value}`)}</span>
                  </button>
                );
              })}
            </div>
            {modeLocked ? <InlineAlert tone="warning">{t("rcloneVersioning.modeLocked")}</InlineAlert> : null}
          </section>

          {selectedMode === "versioned_prefix" ? (
            <section className="space-y-3 border-t border-border/60 pt-4" aria-labelledby={`portable-binding-${task.id}`}>
              <h3 id={`portable-binding-${task.id}`} className="flex items-center gap-2 text-sm font-semibold">
                <Layers3 className="size-4 text-primary" aria-hidden />
                {t("rcloneVersioning.portableBinding")}
              </h3>
              <div className="grid gap-3 sm:grid-cols-2">
                <div>
                  <label htmlFor={`rclone-remote-${task.id}`} className="mb-1 block text-xs font-medium">{t("rcloneVersioning.targetRemote")}</label>
                  <Input id={`rclone-remote-${task.id}`} value={targetRemote} disabled={isBusy} autoComplete="off" onChange={(event) => setTargetRemote(event.target.value)} />
                </div>
                <div>
                  <label htmlFor={`rclone-root-${task.id}`} className="mb-1 block text-xs font-medium">{t("rcloneVersioning.managedRoot")}</label>
                  <Input id={`rclone-root-${task.id}`} value={managedRootLocator} disabled={isBusy} autoComplete="off" onChange={(event) => setManagedRootLocator(event.target.value)} />
                </div>
              </div>
              <div>
                <label htmlFor={`rclone-config-${task.id}`} className="mb-1 block text-xs font-medium">{t("rcloneVersioning.boundConfig")}</label>
                <Textarea id={`rclone-config-${task.id}`} rows={5} value={boundConfig} disabled={isBusy} autoComplete="off" spellCheck={false} className="font-mono text-xs" onChange={(event) => setBoundConfig(event.target.value)} />
              </div>
              <Button type="button" variant="outline" disabled={isBusy} onClick={() => void savePortableBinding()}>
                {busy === "portable" ? <Loader2 className="mr-1.5 size-4 animate-spin" aria-hidden /> : <ShieldCheck className="mr-1.5 size-4" aria-hidden />}
                {t("rcloneVersioning.savePortable")}
              </Button>
            </section>
          ) : (
            <section className="space-y-3 border-t border-border/60 pt-4" aria-labelledby={`native-binding-${task.id}`}>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <h3 id={`native-binding-${task.id}`} className="flex items-center gap-2 text-sm font-semibold">
                  <DatabaseZap className="size-4 text-primary" aria-hidden />
                  {t("rcloneVersioning.nativeBinding")}
                </h3>
                <Button type="button" variant="outline" size="sm" disabled={isBusy} onClick={() => void createNativeSetup()}>
                  {busy === "native-setup" ? <Loader2 className="mr-1.5 size-4 animate-spin" aria-hidden /> : <KeyRound className="mr-1.5 size-4" aria-hidden />}
                  {t("rcloneVersioning.generateExternalId")}
                </Button>
              </div>
              {nativeSetup?.externalId ? (
                <div>
                  <label htmlFor={`rclone-external-id-${task.id}`} className="mb-1 block text-xs font-medium">External ID</label>
                  <Input id={`rclone-external-id-${task.id}`} readOnly value={nativeSetup.externalId} className="font-mono text-xs" />
                  <p className="mt-1 text-xs text-muted-foreground">{t("rcloneVersioning.setupExpires", { time: formatTime(nativeSetup.expiresAt) })}</p>
                </div>
              ) : null}
              <div className="grid gap-3 sm:grid-cols-2">
                <Field id={`rclone-region-${task.id}`} label={t("rcloneVersioning.region")} value={region} disabled={isBusy} onChange={setRegion} />
                <Field id={`rclone-bucket-${task.id}`} label={t("rcloneVersioning.bucket")} value={bucket} disabled={isBusy} onChange={setBucket} />
                <Field id={`rclone-prefix-${task.id}`} label={t("rcloneVersioning.managedPrefix")} value={managedPrefix} disabled={isBusy} onChange={setManagedPrefix} />
                <Field id={`rclone-role-${task.id}`} label={t("rcloneVersioning.roleArn")} value={roleArn} disabled={isBusy} onChange={setRoleArn} />
                <div>
                  <label htmlFor={`rclone-bootstrap-${task.id}`} className="mb-1 block text-xs font-medium">{t("rcloneVersioning.bootstrapMode")}</label>
                  <Select id={`rclone-bootstrap-${task.id}`} value={bootstrapMode} disabled={isBusy} onChange={(event) => setBootstrapMode(event.target.value as BootstrapMode)}>
                    <option value="workload_chain">{t("rcloneVersioning.bootstrap.workload_chain")}</option>
                    <option value="static_sts_bootstrap">{t("rcloneVersioning.bootstrap.static_sts_bootstrap")}</option>
                  </Select>
                </div>
                <div>
                  <label htmlFor={`rclone-encryption-${task.id}`} className="mb-1 block text-xs font-medium">{t("rcloneVersioning.encryptionProfile")}</label>
                  <Select id={`rclone-encryption-${task.id}`} value={encryptionProfile} disabled={isBusy} onChange={(event) => setEncryptionProfile(event.target.value as Exclude<RcloneEncryptionProfile, "none">)}>
                    <option value="sse_s3">SSE-S3</option>
                    <option value="sse_kms_cmk">SSE-KMS CMK</option>
                  </Select>
                </div>
              </div>
              {bootstrapMode === "static_sts_bootstrap" ? (
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field id={`rclone-access-key-${task.id}`} label={t("rcloneVersioning.accessKeyId")} value={accessKeyId} disabled={isBusy} onChange={setAccessKeyId} secret />
                  <Field id={`rclone-secret-key-${task.id}`} label={t("rcloneVersioning.secretAccessKey")} value={secretAccessKey} disabled={isBusy} onChange={setSecretAccessKey} secret />
                </div>
              ) : null}
              {encryptionProfile === "sse_kms_cmk" ? (
                <Field id={`rclone-kms-key-${task.id}`} label={t("rcloneVersioning.kmsKeyArn")} value={kmsKeyArn} disabled={isBusy} onChange={setKmsKeyArn} secret />
              ) : null}
              <Button type="button" variant="outline" disabled={isBusy || !nativeSetup?.setupId} onClick={() => void saveNativeBinding()}>
                {busy === "native" ? <Loader2 className="mr-1.5 size-4 animate-spin" aria-hidden /> : <ShieldCheck className="mr-1.5 size-4" aria-hidden />}
                {t("rcloneVersioning.saveNative")}
              </Button>
            </section>
          )}

          <section className="space-y-3 border-t border-border/60 pt-4" aria-labelledby={`rclone-proof-${task.id}`}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <h3 id={`rclone-proof-${task.id}`} className="text-sm font-semibold">{t("rcloneVersioning.proofTitle")}</h3>
                <p className="mt-0.5 text-xs text-muted-foreground">{t("rcloneVersioning.proofHint")}</p>
              </div>
              <Button type="button" variant="outline" disabled={!canRunPreflight} onClick={() => void runPreflight()}>
                {busy === "preflight" ? <Loader2 className="mr-1.5 size-4 animate-spin" aria-hidden /> : <ShieldCheck className="mr-1.5 size-4" aria-hidden />}
                {t("rcloneVersioning.runPreflight")}
              </Button>
            </div>
            <SummaryFacts summary={summary} />
            {preflight ? (
              <p className="text-xs text-muted-foreground">{t("rcloneVersioning.preflightExpires", { time: formatTime(preflight.expiresAt) })}</p>
            ) : null}
          </section>

          {preflight?.summary.state === "ready" ? (
            <section className="space-y-3 border-t border-border/60 pt-4" aria-labelledby={`rclone-migration-${task.id}`}>
              <h3 id={`rclone-migration-${task.id}`} className="text-sm font-semibold">{t("rcloneVersioning.migrationChoice")}</h3>
              <div className="grid gap-2 sm:grid-cols-2" role="radiogroup" aria-labelledby={`rclone-migration-${task.id}`}>
                <ChoiceRadio taskId={task.id} value="first_new_point" current={migrationChoice} disabled={isBusy} label={t("rcloneVersioning.firstNewPoint")} onChange={setMigrationChoice} />
                <ChoiceRadio taskId={task.id} value="imported_baseline" current={migrationChoice} disabled={isBusy} label={t("rcloneVersioning.importedBaseline")} onChange={setMigrationChoice} />
              </div>
              {migrationChoice === "imported_baseline" ? (
                <label className="flex items-start gap-2 border-l-2 border-warning pl-3 text-xs text-muted-foreground">
                  <input type="checkbox" checked={confirmImportedBaseline} disabled={isBusy} onChange={(event) => setConfirmImportedBaseline(event.target.checked)} />
                  <span>{t("rcloneVersioning.importedBaselineConfirm")}</span>
                </label>
              ) : null}
            </section>
          ) : null}
        </DialogBody>

        <DialogFooter className="justify-between">
          <div>
            {summary.mode !== "legacy_mutable" && summary.rollbackCapability === "clean_available" ? (
              <Button type="button" variant="outline" disabled={isBusy} onClick={() => void rollback(true)}>
                {busy === "rollback" ? <Loader2 className="mr-1.5 size-4 animate-spin" aria-hidden /> : <RotateCcw className="mr-1.5 size-4" aria-hidden />}
                {t("rcloneVersioning.cleanRollback")}
              </Button>
            ) : summary.mode !== "legacy_mutable" && summary.rollbackCapability === "preparation_only" ? (
              <Button type="button" variant="outline" disabled={isBusy} onClick={() => void rollback(false)}>
                {busy === "rollback" ? <Loader2 className="mr-1.5 size-4 animate-spin" aria-hidden /> : <RotateCcw className="mr-1.5 size-4" aria-hidden />}
                {t("rcloneVersioning.prepareRollback")}
              </Button>
            ) : null}
          </div>
          <Button type="button" disabled={!canActivate} onClick={() => void activate()}>
            {busy === "activate" ? <Loader2 className="mr-1.5 size-4 animate-spin" aria-hidden /> : <ShieldCheck className="mr-1.5 size-4" aria-hidden />}
            {t("rcloneVersioning.activate")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

type FieldProps = {
  id: string;
  label: string;
  value: string;
  disabled: boolean;
  onChange: (value: string) => void;
  secret?: boolean;
};

function Field({ id, label, value, disabled, onChange, secret = false }: FieldProps) {
  return (
    <div>
      <label htmlFor={id} className="mb-1 block text-xs font-medium">{label}</label>
      <Input
        id={id}
        type={secret ? "password" : "text"}
        value={value}
        disabled={disabled}
        autoComplete="off"
        onChange={(event) => onChange(event.target.value)}
      />
    </div>
  );
}

function ChoiceRadio({
  taskId,
  value,
  current,
  disabled,
  label,
  onChange,
}: {
  taskId: number;
  value: RcloneVersioningMigrationChoice;
  current: RcloneVersioningMigrationChoice;
  disabled: boolean;
  label: string;
  onChange: (value: RcloneVersioningMigrationChoice) => void;
}) {
  return (
    <label className="flex cursor-pointer items-center gap-2 border border-border/70 px-3 py-2.5 text-sm has-[:checked]:border-primary has-[:checked]:bg-primary/5">
      <input
        type="radio"
        name={`rclone-migration-${taskId}`}
        value={value}
        checked={current === value}
        disabled={disabled}
        onChange={() => onChange(value)}
      />
      <span>{label}</span>
    </label>
  );
}

function SummaryFacts({ summary }: { summary: RclonePublicationSummary }) {
  const { t } = useTranslation();
  const facts = [
    [t("rcloneVersioning.reasonLabel"), t(`rcloneVersioning.reason.${summary.reasonCode}`)],
    [t("rcloneVersioning.consistency"), t(`rcloneVersioning.consistencyValue.${summary.consistencyClass}`)],
    [t("rcloneVersioning.fidelity"), t(`rcloneVersioning.fidelityValue.${summary.hashFidelity}`)],
    [t("rcloneVersioning.readBytes"), `${summary.estimatedReadBytes} B`],
    [t("rcloneVersioning.apiCost"), t(`rcloneVersioning.cost.${summary.apiCostClass}`)],
    [t("rcloneVersioning.storageCost"), t(`rcloneVersioning.cost.${summary.storageCostClass}`)],
    [t("rcloneVersioning.egressCost"), t(`rcloneVersioning.cost.${summary.egressCostClass}`)],
    [t("rcloneVersioning.encryptionLabel"), t(`rcloneVersioning.encryption.${summary.encryptionProfile}`)],
    [t("rcloneVersioning.credentialExpiry"), summary.credentialExpiresAt ? formatTime(summary.credentialExpiresAt) : t("rcloneVersioning.notApplicable")],
    [t("rcloneVersioning.kmsStatusLabel"), t(`rcloneVersioning.kmsStatus.${summary.kmsKeyStatus}`)],
    [t("rcloneVersioning.kmsReadKeys"), t("rcloneVersioning.kmsReadKeyCount", { count: summary.kmsReadKeyCount })],
    [t("rcloneVersioning.rollbackLocator"), t(summary.rollbackLocatorPresent ? "rcloneVersioning.present" : "rcloneVersioning.absent")],
    [t("rcloneVersioning.rollback"), t(`rcloneVersioning.rollbackCapability.${summary.rollbackCapability}`)],
  ];
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-2 border-y border-border/50 py-3 sm:grid-cols-4">
      {facts.map(([label, value]) => (
        <div key={label} className="min-w-0">
          <dt className="text-[11px] uppercase tracking-wide text-muted-foreground">{label}</dt>
          <dd className="mt-0.5 truncate text-xs font-medium" title={value}>{value}</dd>
        </div>
      ))}
    </dl>
  );
}
