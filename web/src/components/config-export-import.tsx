import { type ChangeEvent, type FormEvent, useCallback, useLayoutEffect, useRef, useState } from "react";
import { Download, Upload, Loader2, ShieldAlert } from "lucide-react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { InlineAlert, type InlineAlertTone } from "@/components/ui/inline-alert";
import { Textarea } from "@/components/ui/textarea";
import { StepUpPrerequisiteNotice } from "@/components/step-up-prerequisite-notice";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import {
  ApiError,
  AuthTransitionRejectedError,
  getAuthIdentitySnapshot,
  getAuthSessionGeneration,
  isAuthTransitionActive,
  subscribeAuthTransition,
} from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { assertSensitiveStepUpReady, sensitiveStepUpBlock, StepUpPausedError } from "@/lib/sensitive-step-up";
import { useConfirm } from "@/hooks/use-confirm";
import { cn, getErrorMessage } from "@/lib/utils";
import type {
  ConfigImportEntity,
  ConfigImportResult,
  ConfigImportWarning,
  ConfigImportWarningCode,
} from "@/types/domain";
import { toast } from "sonner";

const CONFIG_GRANT_MAX_REASON_LENGTH = 240;
const CONFIG_GRANT_TTL_SECONDS = 600;
const MAX_VISIBLE_WARNINGS = 100;

const IMPORT_ENTITIES: Record<ConfigImportEntity, true> = {
  nodes: true,
  ssh_keys: true,
  policies: true,
  tasks: true,
  system_settings: true,
};
const IMPORT_WARNING_CODES: Record<ConfigImportWarningCode, true> = {
  invalid_input: true,
  invalid_scope: true,
  unresolved_node_scope: true,
  invalid_private_key: true,
  missing_private_key: true,
  missing_password: true,
  missing_inline_private_key: true,
  unresolved_ssh_key: true,
  duplicate_name: true,
  invalid_reference: true,
  reference_conflict: true,
};
const SCOPE_WARNING_CODES: Record<string, true> = {
  invalid_scope: true,
  unresolved_node_scope: true,
};
const DISABLED_KEY_WARNING_CODES: Record<string, true> = {
  missing_private_key: true,
};
const NODE_WARNING_CODES: Record<string, true> = {
  missing_password: true,
  missing_inline_private_key: true,
  unresolved_ssh_key: true,
};
const NAME_REFERENCE_CODES: Record<string, true> = {
  invalid_reference: true,
  reference_conflict: true,
};
const IMPORT_NAME_WARNING_CODES: Record<string, true> = {
  duplicate_name: true,
  invalid_reference: true,
  reference_conflict: true,
};

type ConfigGrantMode = "import" | "sensitive-export";

type AdminOperationScope = {
  attempt: number;
  generation: number;
  token: string;
};

type BoundSignal = {
  signal: AbortSignal;
  release: () => void;
  abort: () => void;
};

type DisplayImportResult = {
  nodes: number;
  sshKeys: number;
  policies: number;
  tasks: number;
  systemSettings: number;
  imported: number;
  skipped: number;
  created: number;
  updated: number;
  rejected: number;
  disabledImported: number;
  warnings: ConfigImportWarning[];
  warningsTruncated: number;
};

type ImportPresentation =
  | { status: "success" | "warnings" | "rejected"; result: DisplayImportResult }
  | { status: "failure" }
  | { status: "unknown" };

class StaleOperationError extends Error {
  readonly code = "STALE_OPERATION" as const;

  constructor() {
    super("stale config operation");
    this.name = "StaleOperationError";
  }
}

function useAdminOperation(token: string | null, role: string | null) {
  const mountedRef = useRef(false);
  const attemptRef = useRef(0);
  const controllersRef = useRef(new Set<AbortController>());

  useLayoutEffect(() => {
    mountedRef.current = true;
    const abortAll = () => {
      for (const controller of controllersRef.current) controller.abort();
      controllersRef.current.clear();
    };
    const unsubscribe = subscribeAuthTransition(() => {
      if (!isAuthTransitionActive()) return;
      abortAll();
    });
    return () => {
      unsubscribe();
      mountedRef.current = false;
      attemptRef.current += 1;
      abortAll();
    };
  }, [token, role]);

  const capture = useCallback((): AdminOperationScope | null => {
    if (!mountedRef.current || !token || role !== "admin" || isAuthTransitionActive()) return null;
    const snapshot = getAuthIdentitySnapshot();
    if (snapshot.token !== token || snapshot.role !== "admin") return null;
    return { attempt: attemptRef.current, generation: getAuthSessionGeneration(), token };
  }, [token, role]);

  const disposed = useCallback((scope: AdminOperationScope) => {
    return !mountedRef.current || attemptRef.current !== scope.attempt;
  }, []);

  const current = useCallback((scope: AdminOperationScope) => {
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

function countOrZero(value: unknown): number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 && value <= Number.MAX_SAFE_INTEGER ? value : 0;
}

function isImportEntity(value: string): value is ConfigImportEntity {
  return Object.hasOwn(IMPORT_ENTITIES, value);
}

function isImportWarningCode(value: string): value is ConfigImportWarningCode {
  return Object.hasOwn(IMPORT_WARNING_CODES, value);
}

function visibleWarningName(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  if (!trimmed || trimmed.length > 120) return undefined;
  for (let index = 0; index < trimmed.length; index += 1) {
    const code = trimmed.charCodeAt(index);
    if (code < 32 || code === 127) return undefined;
  }
  return trimmed;
}

function visibleWarnings(value: unknown): ConfigImportWarning[] {
  if (!Array.isArray(value)) return [];
  const warnings: ConfigImportWarning[] = [];
  for (const entry of value) {
    if (entry === null || typeof entry !== "object" || Array.isArray(entry)) continue;
    if (!("entity" in entry) || !("code" in entry) || !("index" in entry)) continue;
    const entity = entry.entity;
    const code = entry.code;
    const index = entry.index;
    if (typeof entity !== "string" || !isImportEntity(entity)) continue;
    if (typeof code !== "string" || !isImportWarningCode(code)) continue;
    if (typeof index !== "number" || !Number.isInteger(index) || index < 0) continue;
    const warning: ConfigImportWarning = { entity, index, code };
    if ("name" in entry) {
      const name = visibleWarningName(entry.name);
      if (name) warning.name = name;
    }
    warnings.push(warning);
    if (warnings.length === MAX_VISIBLE_WARNINGS) break;
  }
  return warnings;
}

function hasWarningCode(warnings: ConfigImportWarning[], codes: Record<string, true>): boolean {
  return warnings.some((warning) => codes[warning.code] === true);
}

function hasEntityWarning(
  warnings: ConfigImportWarning[],
  codes: Record<string, true>,
  entity: ConfigImportEntity,
): boolean {
  return warnings.some((warning) => warning.entity === entity && codes[warning.code] === true);
}

function remediationWarnings(warnings: ConfigImportWarning[]): ConfigImportWarning[] {
  return warnings.filter((warning) => {
    const isCompanion = (warning.entity === "nodes" && warning.code === "unresolved_ssh_key")
      || (warning.entity === "ssh_keys" && warning.code === "unresolved_node_scope");
    return !isCompanion || !warnings.some((reference) =>
      reference.entity === warning.entity
      && reference.index === warning.index
      && NAME_REFERENCE_CODES[reference.code] === true,
    );
  });
}

function warningsUseNameMappingRoutesOnly(warnings: ConfigImportWarning[]): boolean {
  return warnings.length > 0 && warnings.every((warning) => IMPORT_NAME_WARNING_CODES[warning.code] === true);
}

function presentImportResult(result: Partial<ConfigImportResult> | null | undefined): ImportPresentation {
  const warnings = visibleWarnings(result?.warnings);
  const display: DisplayImportResult = {
    nodes: countOrZero(result?.nodes),
    sshKeys: countOrZero(result?.sshKeys),
    policies: countOrZero(result?.policies),
    tasks: countOrZero(result?.tasks),
    systemSettings: countOrZero(result?.systemSettings),
    imported: countOrZero(result?.imported),
    skipped: countOrZero(result?.skipped),
    created: countOrZero(result?.created),
    updated: countOrZero(result?.updated),
    rejected: countOrZero(result?.rejected),
    disabledImported: countOrZero(result?.disabledImported),
    warnings,
    warningsTruncated: countOrZero(result?.warningsTruncated),
  };
  if (display.rejected > 0) return { status: "rejected", result: display };
  if (display.warnings.length > 0 || display.warningsTruncated > 0 || display.disabledImported > 0) {
    return { status: "warnings", result: display };
  }
  return { status: "success", result: display };
}

function isDefiniteImportFailure(error: unknown): boolean {
  return error instanceof ApiError
    && error.status >= 400
    && error.status < 500
    && error.status !== 408
    && error.status !== 429;
}

function isPausedStepUp(error: unknown): boolean {
  return error instanceof StepUpPausedError || error instanceof AuthTransitionRejectedError || error instanceof StaleOperationError;
}

function CountStat({ label, value, emphasis }: { label: string; value: number; emphasis: "critical" | "warning" | "quiet" }) {
  return (
    <p className={cn(
      "rounded-md border px-2 py-1.5",
      emphasis === "critical" && "border-destructive/40 bg-destructive/10",
      emphasis === "warning" && "border-warning/40 bg-warning/10",
      emphasis === "quiet" && "border-border/70",
    )}>
      <span className="block text-[11px] text-muted-foreground">{label}</span>
      <span className={cn(
        "tabular-nums",
        emphasis === "quiet" ? "text-sm font-medium text-foreground" : "text-lg font-semibold",
        emphasis === "critical" && "text-destructive",
        emphasis === "warning" && "text-warning",
      )}>{value}</span>
    </p>
  );
}

function ReviewLinks({ keys = true, nodes = true }: { keys?: boolean; nodes?: boolean }) {
  const { t } = useTranslation();
  if (!keys && !nodes) return null;
  const className = "text-xs font-medium text-primary underline underline-offset-4";
  return (
    <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1">
      {keys ? <Link className={className} to="/app/ssh-keys">{t("configExport.remediation.openKeys")}</Link> : null}
      {nodes ? <Link className={className} to="/app/nodes">{t("configExport.remediation.openNodes")}</Link> : null}
    </div>
  );
}

function ConfigImportOutcomePanel({ presentation }: { presentation: ImportPresentation }) {
  const { t } = useTranslation();
  if (presentation.status === "failure" || presentation.status === "unknown") {
    const unknown = presentation.status === "unknown";
    return (
      <InlineAlert
        className="mt-3"
        tone={unknown ? "warning" : "critical"}
        title={t(unknown ? "configExport.result.unknownTitle" : "configExport.result.failureTitle")}
      >
        <p>{t(unknown ? "configExport.result.unknownBody" : "configExport.result.failureBody")}</p>
        {unknown ? <ReviewLinks /> : null}
      </InlineAlert>
    );
  }

  const { result } = presentation;
  const routes = remediationWarnings(result.warnings);
  const nameMappingOnly = warningsUseNameMappingRoutesOnly(routes) && result.warningsTruncated === 0;
  const showSshNameReference = hasEntityWarning(routes, NAME_REFERENCE_CODES, "ssh_keys");
  const showDisabledKeySteps = (result.disabledImported > 0 && !(nameMappingOnly && showSshNameReference))
    || hasWarningCode(routes, DISABLED_KEY_WARNING_CODES);
  const showScopeSteps = hasWarningCode(routes, SCOPE_WARNING_CODES);
  const showNodePath = hasWarningCode(routes, NODE_WARNING_CODES);
  const showNodeNameReference = hasEntityWarning(routes, NAME_REFERENCE_CODES, "nodes");
  const showFileCorrection = hasWarningCode(routes, { duplicate_name: true });
  const showKeyPath = showDisabledKeySteps || showScopeSteps || (result.rejected > 0 && !nameMappingOnly);
  const showReviewScope = showKeyPath || showSshNameReference;
  const showEnableManually = showDisabledKeySteps || showSshNameReference;
  const showRebind = showNodePath || showNodeNameReference;
  const showVerify = showKeyPath || showNodePath;
  const showLegacyLinks = showKeyPath || showNodePath;
  const showOpenKeys = showLegacyLinks || showSshNameReference;
  const showOpenNodes = showLegacyLinks || showNodeNameReference;
  const showNamedRoutes = showReviewScope || showEnableManually || showRebind || showVerify || showOpenKeys || showOpenNodes;
  const tone: InlineAlertTone = presentation.status === "success"
    ? "success"
    : presentation.status === "rejected" && result.imported === 0
      ? "critical"
      : "warning";
  const title = presentation.status === "success"
    ? "configExport.result.successTitle"
    : presentation.status === "rejected"
      ? "configExport.result.rejectedTitle"
      : "configExport.result.warningsTitle";

  return (
    <InlineAlert className="mt-3" tone={tone} title={t(title)}>
      <div className="mt-1 grid grid-cols-2 gap-2 sm:grid-cols-3">
        <CountStat label={t("configExport.result.rejected")} value={result.rejected} emphasis={result.rejected > 0 ? "critical" : "quiet"} />
        <CountStat label={t("configExport.result.disabledImported")} value={result.disabledImported} emphasis={result.disabledImported > 0 ? "warning" : "quiet"} />
        <CountStat label={t("configExport.result.created")} value={result.created} emphasis="quiet" />
        <CountStat label={t("configExport.result.updated")} value={result.updated} emphasis="quiet" />
        <CountStat label={t("configExport.result.imported")} value={result.imported} emphasis="quiet" />
        <CountStat label={t("configExport.result.skipped")} value={result.skipped} emphasis="quiet" />
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        {t("configExport.result.nodes")} {result.nodes}
        {" · "}
        {t("configExport.result.sshKeys")} {result.sshKeys}
        {" · "}
        {t("configExport.result.policies")} {result.policies}
        {" · "}
        {t("configExport.result.tasks")} {result.tasks}
        {" · "}
        {t("configExport.result.systemSettings")} {result.systemSettings}
      </p>
      {result.warnings.length > 0 ? (
        <ul className="mt-2 space-y-1 text-xs" aria-label={t("configExport.result.warningList")}>
          {result.warnings.map((warning, position) => (
            <li key={`${warning.entity}:${warning.index}:${warning.code}:${position}`}>
              {t(`configExport.entity.${warning.entity}`)}
              {" "}
              {t("configExport.warning.index", { index: warning.index })}
              {warning.name ? ` (${warning.name})` : ""}
              {". "}
              {t(`configExport.warning.${warning.code}`)}
            </li>
          ))}
        </ul>
      ) : null}
      {result.warningsTruncated > 0 ? (
        <p className="mt-1 text-xs">{t("configExport.result.warningsTruncated", { count: result.warningsTruncated })}</p>
      ) : null}
      {showFileCorrection ? (
        <p className="mt-2 text-xs font-medium text-foreground">{t("configExport.remediation.correctFile")}</p>
      ) : null}
      {showNamedRoutes ? (
        <>
          <p className="mt-2 text-xs font-medium text-foreground">{t("configExport.remediation.title")}</p>
          <ol className="mt-1 list-decimal space-y-1 pl-4 text-xs text-foreground">
            {showDisabledKeySteps ? <li>{t("configExport.remediation.editKey")}</li> : null}
            {showReviewScope ? <li>{t("configExport.remediation.reviewScope")}</li> : null}
            {showEnableManually ? <li>{t("configExport.remediation.enableManually")}</li> : null}
            {showRebind ? <li>{t("configExport.remediation.rebindNode")}</li> : null}
            {showVerify ? <li>{t("configExport.remediation.verifyNodes")}</li> : null}
          </ol>
          <ReviewLinks keys={showOpenKeys} nodes={showOpenNodes} />
        </>
      ) : null}
    </InlineAlert>
  );
}

export function ConfigExportImport() {
  const { token, role } = useAuth();
  if (!token || role !== "admin") return null;
  return <ConfigExportImportSession key={`${token}:${role}`} />;
}

function ConfigExportImportSession() {
  const { t } = useTranslation();
  const { token, role, totpEnabled, ensureStepUpProof } = useAuth();
  const { capture, current, disposed, bindSignal } = useAdminOperation(token, role);
  const { confirm, dialog } = useConfirm();
  const [exporting, setExporting] = useState(false);
  const [importing, setImporting] = useState(false);
  const [sensitiveExporting, setSensitiveExporting] = useState(false);
  const [grantDialogOpen, setGrantDialogOpen] = useState(false);
  const [grantMode, setGrantMode] = useState<ConfigGrantMode>("import");
  const [grantReason, setGrantReason] = useState("");
  const [grantError, setGrantError] = useState<string | null>(null);
  const [importPresentation, setImportPresentation] = useState<ImportPresentation | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const downloadConfigPayload = (data: unknown, suffix: string, scope: AdminOperationScope): boolean => {
    if (!current(scope)) return false;
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
    if (!current(scope)) return false;
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `xirang-${suffix}-${new Date().toISOString().slice(0, 10)}.json`;
    if (!current(scope)) {
      URL.revokeObjectURL(url);
      return false;
    }
    try {
      anchor.click();
    } finally {
      URL.revokeObjectURL(url);
    }
    return current(scope);
  };

  const protectedConfigBlocked = () => sensitiveStepUpBlock({
    token,
    totpEnabled,
  }) !== "ready";

  const handleExport = async () => {
    if (isAuthTransitionActive()) {
      toast.error(t("stepUp.authTransitioning"));
      return;
    }
    const scope = capture();
    if (!scope) return;
    const binding = bindSignal();
    setExporting(true);
    try {
      const data = await apiClient.exportConfig(scope.token, false, undefined, { signal: binding.signal });
      if (!current(scope) || binding.signal.aborted) return;
      const downloaded = downloadConfigPayload(data, "config", scope);
      if (!downloaded || !current(scope)) return;
      toast.success(t("configExport.exportSuccess"));
    } catch (error) {
      if (disposed(scope) || !current(scope) || binding.signal.aborted || isPausedStepUp(error)) return;
      toast.error(getErrorMessage(error, t("configExport.exportFailed")));
    } finally {
      binding.release();
      if (!disposed(scope)) setExporting(false);
    }
  };

  const resetGrantPromptState = () => {
    setGrantReason("");
    setGrantError(null);
    setGrantDialogOpen(false);
    if (fileInputRef.current) fileInputRef.current.value = "";
  };

  const handleImportClick = () => {
    if (protectedConfigBlocked()) return;
    fileInputRef.current?.click();
  };

  const handleSensitiveExportClick = () => {
    if (protectedConfigBlocked()) return;
    setGrantMode("sensitive-export");
    setGrantReason("");
    setGrantError(null);
    setGrantDialogOpen(true);
  };

  const handleFileChange = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;
    if (protectedConfigBlocked()) {
      if (fileInputRef.current) fileInputRef.current.value = "";
      return;
    }
    const scope = capture();
    if (!scope) {
      if (fileInputRef.current) fileInputRef.current.value = "";
      return;
    }
    const ok = await confirm({
      title: t("configExport.importConfirm"),
      description: t("common.irreversible"),
    });
    if (!ok || !current(scope)) {
      if (!disposed(scope)) resetGrantPromptState();
      return;
    }
    setImportPresentation(null);
    setGrantMode("import");
    setGrantReason("");
    setGrantError(null);
    setGrantDialogOpen(true);
  };

  const grantSubmitting = importing || sensitiveExporting;

  const handleGrantDialogChange = (open: boolean) => {
    if (grantSubmitting) return;
    if (!open) {
      resetGrantPromptState();
      return;
    }
    setGrantDialogOpen(true);
  };

  const selectedImportFile = (): File => {
    const file = fileInputRef.current?.files?.[0];
    if (!file) throw new Error(t("configExport.importPayloadMissing"));
    return file;
  };

  const readImportFile = async (file: File, scope: AdminOperationScope): Promise<Record<string, unknown>> => {
    const text = await file.text();
    if (!current(scope)) throw new StaleOperationError();
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      throw new Error(t("configExport.invalidImportFile"));
    }
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      throw new Error(t("configExport.invalidImportFile"));
    }
    return parsed as Record<string, unknown>;
  };

  const publishUncertainImport = (scope: AdminOperationScope) => {
    if (disposed(scope)) return;
    setImportPresentation({ status: "unknown" });
    toast.warning(t("configExport.result.unknownTitle"));
    resetGrantPromptState();
  };

  const handleImportGrantSubmit = async (reason: string) => {
    if (protectedConfigBlocked()) return;
    if (isAuthTransitionActive()) {
      setGrantError(t("stepUp.authTransitioning"));
      return;
    }
    const scope = capture();
    if (!scope) return;
    const binding = bindSignal();
    let importDispatched = false;
    setImporting(true);
    setGrantError(null);
    try {
      const file = selectedImportFile();
      if (!current(scope)) return;
      await readImportFile(file, scope);
      if (!current(scope)) return;
      assertSensitiveStepUpReady({ token: scope.token, totpEnabled });
      if (!current(scope)) return;
      const proof = await ensureStepUpProof(STEP_UP_ACTIONS.configImport, { persist: false, reuseCached: false });
      if (!current(scope)) return;
      await apiClient.requestConfigImportCredentialGrant(scope.token, {
        reason,
        requestedTtlSeconds: CONFIG_GRANT_TTL_SECONDS,
      }, proof);
      if (!current(scope)) return;
      const pendingImport = await readImportFile(file, scope);
      if (!current(scope)) return;
      importDispatched = true;
      const result = await apiClient.importConfig(scope.token, pendingImport, "skip", proof, { signal: binding.signal });
      if (disposed(scope)) return;
      if (!current(scope) || binding.signal.aborted) {
        publishUncertainImport(scope);
        return;
      }
      const presentation = presentImportResult(result);
      setImportPresentation(presentation);
      if (presentation.status === "success") {
        toast.success(t("configExport.importSuccess", { imported: presentation.result.imported, skipped: presentation.result.skipped }));
      } else {
        toast.warning(t(presentation.status === "rejected" ? "configExport.result.rejectedTitle" : "configExport.result.warningsTitle"));
      }
      resetGrantPromptState();
    } catch (error) {
      if (disposed(scope)) return;
      if (!importDispatched) {
        if (!current(scope) || isPausedStepUp(error)) return;
        setGrantError(getErrorMessage(error, t("configExport.importFailed")));
        return;
      }
      if (isDefiniteImportFailure(error) && current(scope) && !binding.signal.aborted) {
        setImportPresentation({ status: "failure" });
        toast.error(t("configExport.result.failureTitle"));
        resetGrantPromptState();
        return;
      }
      publishUncertainImport(scope);
    } finally {
      binding.release();
      if (!disposed(scope)) setImporting(false);
    }
  };

  const handleSensitiveExportGrantSubmit = async (reason: string) => {
    if (protectedConfigBlocked()) return;
    if (isAuthTransitionActive()) {
      setGrantError(t("stepUp.authTransitioning"));
      return;
    }
    const scope = capture();
    if (!scope) return;
    const binding = bindSignal();
    setSensitiveExporting(true);
    setGrantError(null);
    try {
      assertSensitiveStepUpReady({ token: scope.token, totpEnabled });
      if (!current(scope)) return;
      const proof = await ensureStepUpProof(STEP_UP_ACTIONS.configExport, { persist: false, reuseCached: false });
      if (!current(scope)) return;
      await apiClient.requestConfigExportCredentialGrant(scope.token, {
        reason,
        requestedTtlSeconds: CONFIG_GRANT_TTL_SECONDS,
      }, proof);
      if (!current(scope)) return;
      const data = await apiClient.exportConfig(scope.token, true, proof, { signal: binding.signal });
      if (!current(scope) || binding.signal.aborted) return;
      const downloaded = downloadConfigPayload(data, "config-with-sensitive", scope);
      if (!downloaded || !current(scope)) return;
      toast.success(t("configExport.sensitiveExportSuccess"));
      resetGrantPromptState();
    } catch (error) {
      if (disposed(scope) || !current(scope) || binding.signal.aborted || isPausedStepUp(error)) return;
      setGrantError(getErrorMessage(error, t("configExport.sensitiveExportFailed")));
    } finally {
      binding.release();
      if (!disposed(scope)) setSensitiveExporting(false);
    }
  };

  const handleGrantSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const reason = grantReason.trim();
    if (!reason) {
      setGrantError(t("configExport.grantReasonRequired"));
      return;
    }
    if (Array.from(reason).length > CONFIG_GRANT_MAX_REASON_LENGTH) {
      setGrantError(t("configExport.grantReasonTooLong", { max: CONFIG_GRANT_MAX_REASON_LENGTH }));
      return;
    }
    if (grantMode === "sensitive-export") {
      await handleSensitiveExportGrantSubmit(reason);
      return;
    }
    await handleImportGrantSubmit(reason);
  };


  return (
    <>
      <Card className="glass-panel border-border/70 relative overflow-hidden group">
        <div className="absolute top-0 left-0 w-1 h-full bg-primary/50" aria-hidden />
        <CardHeader className="pb-3 z-10 relative">
          <CardTitle className="text-base">{t("configExport.title")}</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-xs text-muted-foreground mb-3">
            {t("configExport.desc")}
          </p>
          <p className="text-xs text-muted-foreground mb-3">{t("configExport.importSkipPolicy")}</p>
          <p className="text-xs text-muted-foreground mb-3">{t("configExport.importOverwriteLimit")}</p>
          {totpEnabled === false ? <StepUpPrerequisiteNotice className="mb-3" /> : null}
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" onClick={handleExport} disabled={exporting}>
              {exporting ? <Loader2 className="mr-1 size-3.5 animate-spin" aria-hidden /> : <Download className="mr-1 size-3.5" aria-hidden />}
              {t("configExport.exportConfig")}
            </Button>
            <Button size="sm" variant="outline" onClick={handleSensitiveExportClick} disabled={sensitiveExporting}>
              {sensitiveExporting ? <Loader2 className="mr-1 size-3.5 animate-spin" aria-hidden /> : <ShieldAlert className="mr-1 size-3.5" aria-hidden />}
              {t("configExport.exportSensitiveConfig")}
            </Button>
            <Button size="sm" variant="outline" onClick={handleImportClick} disabled={grantSubmitting}>
              {importing ? <Loader2 className="mr-1 size-3.5 animate-spin" aria-hidden /> : <Upload className="mr-1 size-3.5" aria-hidden />}
              {t("configExport.importConfig")}
            </Button>
            <input ref={fileInputRef} type="file" accept=".json" className="hidden" aria-label={t("configExport.importConfig")} onChange={handleFileChange} />
          </div>
          {importPresentation ? <ConfigImportOutcomePanel presentation={importPresentation} /> : null}
        </CardContent>
      </Card>

      <Dialog open={grantDialogOpen} onOpenChange={handleGrantDialogChange}>
        <DialogContent size="sm">
          <form onSubmit={handleGrantSubmit}>
            <DialogHeader>
              <DialogTitle>{t(grantMode === "sensitive-export" ? "configExport.sensitiveGrantTitle" : "configExport.grantTitle")}</DialogTitle>
              <DialogDescription>{t(grantMode === "sensitive-export" ? "configExport.sensitiveGrantDescription" : "configExport.grantDescription")}</DialogDescription>
            </DialogHeader>
            <DialogBody className="space-y-3">
              <div className="space-y-1.5">
                <label className="text-sm font-medium" htmlFor="config-import-grant-reason">
                  {t("configExport.grantReasonLabel")}
                </label>
                <Textarea
                  id="config-import-grant-reason"
                  value={grantReason}
                  onChange={(event) => setGrantReason(event.target.value)}
                  maxLength={CONFIG_GRANT_MAX_REASON_LENGTH}
                  placeholder={t("configExport.grantReasonPlaceholder")}
                  disabled={grantSubmitting}
                  aria-describedby="config-import-grant-reason-hint"
                  aria-invalid={grantError ? true : undefined}
                />
                <p id="config-import-grant-reason-hint" className="text-xs text-muted-foreground">
                  {t("configExport.grantReasonHint", { max: CONFIG_GRANT_MAX_REASON_LENGTH })}
                </p>
              </div>
              {grantError ? (
                <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive" role="alert">
                  {grantError}
                </p>
              ) : null}
            </DialogBody>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={resetGrantPromptState} disabled={grantSubmitting}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" loading={grantSubmitting}>
                {t(grantMode === "sensitive-export" ? "configExport.sensitiveGrantSubmit" : "configExport.grantSubmit")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      {dialog}
    </>
  );
}
