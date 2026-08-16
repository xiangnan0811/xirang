import { useCallback, useEffect, useRef, useState } from "react";

import type { AuthContextValue, AuthRole } from "@/context/auth-context.shared";
import {
  createBackupRecoveryApi,
  generateRecoveryGrantSecret,
  type BackupRecoveryApi,
  type RecoveryConflictPolicy,
  type RecoveryCryptoSource,
  type RecoveryDownloadTicket,
  type RecoveryGrantCategory,
  type RecoveryGrantStatus,
  type RecoveryJob,
  type RecoveryJobItem,
  type RecoveryPage,
  type RecoveryPlan,
  type RecoveryPreflight,
  type RecoveryResultPage,
  type RecoverySecurityFindingCategory,
  type RecoveryTargetMode,
} from "@/lib/api/backup-recovery-api";
import { ApiError } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type { AssetRef } from "@/types/domain";

export type BackupRecoveryPhase =
  | "closed"
  | "target"
  | "creating"
  | "preflighting"
  | "security"
  | "impact"
  | "authorizing_write"
  | "executing"
  | "progress"
  | "delete_authorization"
  | "verification"
  | "result"
  | "unavailable"
  | "error";

export type BackupRecoveryError = "forbidden" | "not_found" | "invalid" | "conflict" | "unavailable";

export interface RecoverySourceContext {
  repositoryId: string;
  catalogGenerationId: string;
}

export interface RecoveryTargetDraft {
  targetMode: RecoveryTargetMode;
  targetNodeId: number;
  targetRootId: string;
  conflictPolicy: RecoveryConflictPolicy;
}

export interface RecoveryPublicGrant {
  id: string;
  category: RecoveryGrantCategory;
  expiresAt: string;
  status: RecoveryGrantStatus;
}

export interface BackupRecoveryState {
  phase: BackupRecoveryPhase;
  selection: AssetRef[];
  source: RecoverySourceContext | null;
  target: RecoveryTargetDraft | null;
  plan: RecoveryPlan | null;
  preflight: RecoveryPreflight | null;
  writeGrant: RecoveryPublicGrant | null;
  job: RecoveryJob | null;
  itemPage: RecoveryPage<RecoveryJobItem> | null;
  resultPage: RecoveryResultPage | null;
  ticket: RecoveryDownloadTicket | null;
  error: BackupRecoveryError | null;
  announcement: string | null;
}

export interface RecoveryRouteHandles {
  planId: string | null;
  jobId: string | null;
}

export interface UseBackupRecoveryOptions {
  token: string | null;
  role: AuthRole | null;
  sessionKey: string | null;
  contextKey?: string;
  planId?: string;
  jobId?: string;
  ensureStepUpProof?: AuthContextValue["ensureStepUpProof"];
  onRouteChange: (handles: RecoveryRouteHandles, options: { replace: boolean }) => void;
  api?: BackupRecoveryApi;
  cryptoSource?: RecoveryCryptoSource;
  newIdempotencyKey?: (endpoint: string) => string;
  pollIntervalMs?: number;
  onDownloadTicket?: (ticket: RecoveryDownloadTicket) => void;
}

interface PendingWriteAuthority {
  planId: string;
  preflightId: string;
  expectedRevision: string;
  reason: string;
  proof: string;
  idempotencyKey: string;
  grantSecret: string;
  grant: RecoveryPublicGrant | null;
}

interface PendingCreatePlan {
  binding: string;
  idempotencyKey: string;
}

interface PendingDeleteAuthority {
  jobId: string;
  planId: string;
  checkpointId: string;
  attemptId: string;
  expectedRevision: string;
  reason: string;
  proof: string;
  idempotencyKey: string;
  grantSecret: string;
}

interface PendingExecute {
  planId: string;
  preflightId: string;
  expectedRevision: string;
  grantId: string;
  proof: string;
  idempotencyKey: string;
}

interface PendingSecurityOverride {
  planId: string;
  preflightId: string;
  expectedRevision: string;
  findingCategory: RecoverySecurityFindingCategory;
  reason: string;
  proof: string;
  idempotencyKey: string;
}

function initialState(): BackupRecoveryState {
  return {
    phase: "closed",
    selection: [],
    source: null,
    target: null,
    plan: null,
    preflight: null,
    writeGrant: null,
    job: null,
    itemPage: null,
    resultPage: null,
    ticket: null,
    error: null,
    announcement: null,
  };
}

function classifyError(error: unknown): BackupRecoveryError {
  if (error instanceof ApiError) {
    if (error.status === 400) return "invalid";
    if (error.status === 401 || error.status === 403) return "forbidden";
    if (error.status === 404) return "not_found";
    if (error.status === 409) return "conflict";
  }
  return "unavailable";
}

function validRef(ref: AssetRef): boolean {
  return /^[0-9a-f]{32}$/.test(ref.recoveryPointId) && /^[0-9a-f]{64}$/.test(ref.entryId);
}

function snapshotSelection(refs: readonly AssetRef[]): AssetRef[] {
  if (refs.length === 0 || refs.length > 10_000 || refs.some((ref) => !validRef(ref))) {
    throw new Error("invalid recovery selection");
  }
  const recoveryPointId = refs[0]?.recoveryPointId;
  const keys = new Set<string>();
  return refs.map((ref) => {
    if (ref.recoveryPointId !== recoveryPointId || keys.has(ref.entryId)) {
      throw new Error("invalid recovery selection");
    }
    keys.add(ref.entryId);
    return { recoveryPointId: ref.recoveryPointId, entryId: ref.entryId };
  });
}

function isAmbiguous(error: unknown): boolean {
  if (error instanceof ApiError) return error.status >= 500 && error.status < 600;
  return error instanceof TypeError ||
    (error instanceof Error && error.name === "AbortError");
}

function nextPhaseForJob(job: RecoveryJob): BackupRecoveryPhase {
  if (job.deleteCheckpoint !== null) return "delete_authorization";
  if (job.outcome === "verifying") return "verification";
  if (["succeeded", "degraded", "failed", "needs_attention", "canceled"].includes(job.outcome)) {
    return job.resultSet !== null ? "result" : "verification";
  }
  return "progress";
}

function shouldPoll(job: RecoveryJob): boolean {
  return !["succeeded", "degraded", "failed", "needs_attention", "canceled"].includes(job.outcome);
}

function pageVisible(): boolean {
  return typeof document === "undefined" || document.visibilityState !== "hidden";
}

function samePlanIntent(left: RecoveryPlan, right: RecoveryPlan): boolean {
  return left.id === right.id && left.repositoryId === right.repositoryId &&
    left.recoveryPointId === right.recoveryPointId && left.targetMode === right.targetMode &&
    left.targetNodeId === right.targetNodeId && left.targetRootId === right.targetRootId &&
    left.conflictPolicy === right.conflictPolicy && left.createdAt === right.createdAt;
}

export function useBackupRecovery(options: UseBackupRecoveryOptions) {
  const [state, setState] = useState<BackupRecoveryState>(initialState);
  const stateRef = useRef(state);
  const operationRef = useRef(0);
  const abortRef = useRef<AbortController | null>(null);
  const pageOperationRef = useRef(0);
  const pageAbortRef = useRef<AbortController | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingWriteRef = useRef<PendingWriteAuthority | null>(null);
  const pendingDeleteRef = useRef<PendingDeleteAuthority | null>(null);
  const pendingCreateRef = useRef<PendingCreatePlan | null>(null);
  const pendingExecuteRef = useRef<PendingExecute | null>(null);
  const pendingOverrideRef = useRef<PendingSecurityOverride | null>(null);
  const authorityProofRef = useRef(0);
  const mountedRef = useRef(true);

  const update = useCallback((next: BackupRecoveryState | ((current: BackupRecoveryState) => BackupRecoveryState)) => {
    setState((current) => {
      const resolved = typeof next === "function" ? next(current) : next;
      stateRef.current = resolved;
      return resolved;
    });
  }, []);

  const clearWork = useCallback(() => {
    abortRef.current?.abort();
    abortRef.current = null;
    pageAbortRef.current?.abort();
    pageAbortRef.current = null;
    pageOperationRef.current += 1;
    if (timerRef.current !== null) clearTimeout(timerRef.current);
    timerRef.current = null;
  }, []);

  const clearSensitive = useCallback(() => {
    authorityProofRef.current += 1;
    pendingCreateRef.current = null;
    pendingWriteRef.current = null;
    pendingDeleteRef.current = null;
    pendingExecuteRef.current = null;
    pendingOverrideRef.current = null;
  }, []);

  const reset = useCallback(() => {
    operationRef.current += 1;
    clearWork();
    clearSensitive();
    update(initialState());
  }, [clearSensitive, clearWork, update]);

  const begin = useCallback(() => {
    clearWork();
    const controller = new AbortController();
    abortRef.current = controller;
    operationRef.current += 1;
    return { controller, operation: operationRef.current };
  }, [clearWork]);

  const current = useCallback((operation: number) => mountedRef.current && operation === operationRef.current, []);
  const beginPage = useCallback(() => {
    pageAbortRef.current?.abort();
    const controller = new AbortController();
    pageAbortRef.current = controller;
    pageOperationRef.current += 1;
    return { controller, operation: pageOperationRef.current };
  }, []);
  const currentPage = useCallback(
    (operation: number) => mountedRef.current && operation === pageOperationRef.current,
    [],
  );
  const getApi = useCallback(() => options.api ?? createBackupRecoveryApi(), [options.api]);
  const authToken = options.token;
  const authRole = options.role;
  const routePlanId = options.planId;
  const routeChanged = options.onRouteChange;
  const pollIntervalMs = options.pollIntervalMs;
  const ensureStepUpProof = options.ensureStepUpProof;
  const keyFactory = options.newIdempotencyKey;
  const cryptoSource = options.cryptoSource;
  const newKey = useCallback((endpoint: string) => {
    const value = keyFactory?.(endpoint) ?? `${endpoint}-${generateRecoveryGrantSecret(cryptoSource)}`;
    if (value.length < 16 || value.length > 256) throw new Error("invalid recovery idempotency key");
    return value;
  }, [cryptoSource, keyFactory]);

  const reconcilePlan = useCallback(async (planId: string) => {
    if (authToken === null || authRole !== "admin" || !/^[0-9a-f]{32}$/.test(planId) || !pageVisible()) return;
    const { controller, operation } = begin();
    try {
      const product = await getApi().getPlan(authToken, planId, controller.signal);
      if (!current(operation)) return;
      if (product.status !== "available" || product.value.id !== planId) {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      update((value) => ({ ...value, phase: "target", plan: product.value, error: null }));
    } catch (error) {
      if (!current(operation) || (error instanceof Error && error.name === "AbortError")) return;
      const classified = classifyError(error);
      update((value) => ({ ...value, phase: classified === "not_found" ? "unavailable" : "error", error: classified }));
      if (classified === "not_found") routeChanged({ planId: null, jobId: null }, { replace: true });
    }
  }, [authRole, authToken, begin, current, getApi, routeChanged, update]);

  const reconcileJob = useCallback(async (jobId: string) => {
    if (authToken === null || authRole !== "admin" || !/^[0-9a-f]{32}$/.test(jobId) || !pageVisible()) return;
    const { controller, operation } = begin();
    try {
      const product = await getApi().getJob(authToken, jobId, controller.signal);
      if (!current(operation)) return;
      if (product.status !== "available") {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      if (routePlanId !== undefined && product.value.planId !== routePlanId) {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      let recoveredPlan = stateRef.current.plan;
      if (recoveredPlan?.id !== product.value.planId) {
        const planProduct = await getApi().getPlan(authToken, product.value.planId, controller.signal);
        if (!current(operation)) return;
        if (planProduct.status !== "available" || planProduct.value.id !== product.value.planId) {
          update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
          return;
        }
        recoveredPlan = planProduct.value;
      }
      update((value) => ({
        ...value,
        phase: nextPhaseForJob(product.value),
        plan: recoveredPlan,
        job: product.value,
        error: null,
        announcement: value.job?.outcome === product.value.outcome ? value.announcement : `job:${product.value.outcome}`,
      }));
      if (shouldPoll(product.value) && pageVisible()) {
        timerRef.current = setTimeout(() => {
          timerRef.current = null;
          void reconcileJob(product.value.id);
        }, pollIntervalMs ?? 2_000);
      }
    } catch (error) {
      if (!current(operation) || (error instanceof Error && error.name === "AbortError")) return;
      const classified = classifyError(error);
      update((value) => ({ ...value, phase: classified === "not_found" ? "unavailable" : "error", error: classified }));
      if (classified === "not_found") routeChanged({ planId: null, jobId: null }, { replace: true });
    }
  }, [authRole, authToken, begin, current, getApi, pollIntervalMs, routeChanged, routePlanId, update]);

  const open = useCallback((refs: readonly AssetRef[], source: RecoverySourceContext) => {
    const selection = snapshotSelection(refs);
    if (!/^[0-9a-f]{32}$/.test(source.repositoryId) || !/^[0-9a-f]{32}$/.test(source.catalogGenerationId)) {
      throw new Error("invalid recovery source");
    }
    reset();
    update({ ...initialState(), phase: "target", selection, source: { ...source } });
  }, [reset, update]);

  const setTarget = useCallback((target: RecoveryTargetDraft) => {
    if (stateRef.current.phase !== "target" || !Number.isSafeInteger(target.targetNodeId) || target.targetNodeId <= 0 ||
      target.targetRootId.length === 0 || target.targetRootId.length > 32 ||
      (target.conflictPolicy === "exact_mirror" && target.targetMode !== "in_place")) {
      throw new Error("invalid recovery target");
    }
    update((value) => ({ ...value, target: { ...target }, error: null }));
  }, [update]);

  const createPlan = useCallback(async () => {
    const before = stateRef.current;
    if (options.token === null || options.role !== "admin" || before.source === null || before.target === null ||
      before.selection.length === 0) throw new Error("recovery unavailable");
    const { controller, operation } = begin();
    update((value) => ({ ...value, phase: "creating", error: null }));
    const binding = JSON.stringify({
      source: before.source,
      target: before.target,
      selection: before.selection,
    });
    let pending = pendingCreateRef.current;
    if (pending === null || pending.binding !== binding) {
      pending = { binding, idempotencyKey: newKey("create") };
      pendingCreateRef.current = pending;
    }
    try {
      const receipt = await getApi().createPlan(options.token, {
        repositoryId: before.source.repositoryId,
        recoveryPointId: before.selection[0]!.recoveryPointId,
        catalogGenerationId: before.source.catalogGenerationId,
        entryIds: before.selection.map((ref) => ref.entryId),
        ...before.target,
        idempotencyKey: pending.idempotencyKey,
        signal: controller.signal,
      });
      if (!current(operation)) return;
      if (receipt.status !== "available") {
        pendingCreateRef.current = null;
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      pendingCreateRef.current = null;
      const product = await getApi().getPlan(options.token, receipt.value.planId, controller.signal);
      if (!current(operation)) return;
      if (product.status !== "available") {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      update((value) => ({ ...value, phase: "target", plan: product.value, announcement: "plan_created" }));
      options.onRouteChange({ planId: product.value.id, jobId: null }, { replace: false });
    } catch (error) {
      if (!current(operation)) return;
      const ambiguous = isAmbiguous(error);
      if (!ambiguous) pendingCreateRef.current = null;
      update((value) => ({ ...value, phase: ambiguous ? "target" : "error", error: classifyError(error) }));
    }
  }, [begin, current, getApi, newKey, options, update]);

  const runPreflight = useCallback(async () => {
    const before = stateRef.current;
    if (options.token === null || before.plan === null) throw new Error("recovery plan unavailable");
    const { controller, operation } = begin();
    update((value) => ({ ...value, phase: "preflighting", error: null }));
    try {
      const product = await getApi().preflight(options.token, {
        planId: before.plan.id, expectedRevision: before.plan.revision, signal: controller.signal,
      });
      if (!current(operation)) return;
      if (product.status !== "available" || !product.value.persisted || product.value.planRevision === null) {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      update((value) => ({
        ...value,
        phase: product.value.security.decision === "block" ? "security" : "impact",
        plan: value.plan === null ? null : { ...value.plan, revision: product.value.planRevision! },
        preflight: product.value,
        announcement: "preflight_complete",
      }));
    } catch (error) {
      if (!current(operation)) return;
      if (!isAmbiguous(error)) {
        update((value) => ({ ...value, phase: "error", error: classifyError(error) }));
        return;
      }
      try {
        const reconciled = await getApi().getPlan(options.token, before.plan.id, controller.signal);
        if (!current(operation)) return;
        if (reconciled.status !== "available" || !samePlanIntent(before.plan, reconciled.value) ||
          reconciled.value.state !== "draft" || reconciled.value.revision !== before.plan.revision) {
          update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
          return;
        }
        update((value) => ({
          ...value,
          phase: "target",
          plan: reconciled.value,
          error: "unavailable",
          announcement: "preflight_retry_available",
        }));
      } catch {
        if (!current(operation)) return;
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
      }
    }
  }, [begin, current, getApi, options.token, update]);

  const authorizeWrite = useCallback(async (reason: string) => {
    const before = stateRef.current;
    const preflight = before.preflight;
    if (options.token === null || options.ensureStepUpProof === undefined || before.plan === null ||
      preflight?.preflightId === null || preflight?.preflightId === undefined || preflight.planRevision === null) {
      throw new Error("recovery preflight unavailable");
    }
    let pending = pendingWriteRef.current;
    if (pending === null || pending.planId !== before.plan.id || pending.preflightId !== preflight.preflightId ||
      pending.expectedRevision !== before.plan.revision || pending.reason !== reason || pending.grant !== null) {
      const proofOperation = ++authorityProofRef.current;
      const proof = await options.ensureStepUpProof(STEP_UP_ACTIONS.assetRecover, { persist: false, reuseCached: false });
      if (!mountedRef.current || proofOperation !== authorityProofRef.current) return;
      pending = {
        planId: before.plan.id,
        preflightId: preflight.preflightId,
        expectedRevision: before.plan.revision,
        reason,
        proof,
        idempotencyKey: newKey("write"),
        grantSecret: generateRecoveryGrantSecret(options.cryptoSource),
        grant: null,
      };
      pendingWriteRef.current = pending;
    }
    const { controller, operation } = begin();
    update((value) => ({ ...value, phase: "authorizing_write", error: null }));
    try {
      const product = await getApi().authorizeWrite(options.token, { ...pending, signal: controller.signal });
      if (!current(operation)) return;
      if (product.status !== "available" || product.value.grant === null || product.value.operation !== "write_authorize") {
        clearSensitive();
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      pending.grant = { ...product.value.grant };
      pendingWriteRef.current = pending;
      update((value) => ({
        ...value,
        phase: "impact",
        plan: value.plan === null ? null : { ...value.plan, revision: product.value.planRevision },
        writeGrant: { ...product.value.grant! },
        announcement: "write_authorized",
      }));
    } catch (error) {
      if (!current(operation)) return;
      const ambiguous = isAmbiguous(error);
      if (!ambiguous) clearSensitive();
      update((value) => ({ ...value, phase: ambiguous ? "impact" : "error", error: classifyError(error) }));
    }
  }, [begin, clearSensitive, current, getApi, newKey, options, update]);

  const overrideSecurity = useCallback(async (
    findingCategory: RecoverySecurityFindingCategory,
    reason: string,
    confirmed: boolean,
  ) => {
    const before = stateRef.current;
    const preflight = before.preflight;
    if (!confirmed) throw new Error("security override confirmation required");
    if (options.token === null || options.ensureStepUpProof === undefined || before.plan === null ||
      preflight?.persisted !== true || preflight.preflightId === null || preflight.planRevision === null ||
      preflight.security.decision !== "block" ||
      !preflight.security.overridableCategories.includes(findingCategory)) {
      throw new Error("security finding is not overridable");
    }
    let pending = pendingOverrideRef.current;
    if (pending === null || pending.planId !== before.plan.id || pending.preflightId !== preflight.preflightId ||
      pending.expectedRevision !== before.plan.revision || pending.findingCategory !== findingCategory ||
      pending.reason !== reason) {
      const proofOperation = ++authorityProofRef.current;
      const proof = await options.ensureStepUpProof(STEP_UP_ACTIONS.assetRecover, { persist: false, reuseCached: false });
      if (!mountedRef.current || proofOperation !== authorityProofRef.current) return;
      pending = {
        planId: before.plan.id,
        preflightId: preflight.preflightId,
        expectedRevision: before.plan.revision,
        findingCategory,
        reason,
        proof,
        idempotencyKey: newKey("override"),
      };
      pendingOverrideRef.current = pending;
    }
    const { controller, operation } = begin();
    update((value) => ({ ...value, phase: "security", error: null }));
    try {
      const product = await getApi().overrideSecurity(options.token, { ...pending, signal: controller.signal });
      if (!current(operation)) return;
      if (product.status !== "available" || product.value.operation !== "security_override" ||
        product.value.planId !== before.plan.id || product.value.grant !== null) {
        pendingOverrideRef.current = null;
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      pendingOverrideRef.current = null;
      const remainingReasons = preflight.reasons.filter((item) => item !== "security_blocked");
      const eligible = remainingReasons.length === 0;
      update((value) => ({
        ...value,
        phase: eligible ? "impact" : "security",
        plan: value.plan === null ? null : {
          ...value.plan, revision: product.value.planRevision, securityDecision: "admin_override",
        },
        preflight: value.preflight === null ? null : {
          ...value.preflight,
          eligible,
          reasons: remainingReasons,
          security: { ...value.preflight.security, decision: "admin_override" },
        },
        announcement: "security_overridden",
      }));
    } catch (error) {
      if (!current(operation)) return;
      const ambiguous = isAmbiguous(error);
      if (!ambiguous) pendingOverrideRef.current = null;
      update((value) => ({ ...value, phase: ambiguous ? "security" : "error", error: classifyError(error) }));
    }
  }, [begin, current, getApi, newKey, options, update]);

  const execute = useCallback(async () => {
    const before = stateRef.current;
    const pending = pendingWriteRef.current;
    if (options.token === null || options.ensureStepUpProof === undefined || before.plan === null ||
      before.preflight?.preflightId === null || before.preflight?.preflightId === undefined || pending?.grant === null ||
      pending === null) throw new Error("write authority unavailable");
    let executePending = pendingExecuteRef.current;
    if (executePending === null || executePending.planId !== before.plan.id ||
      executePending.preflightId !== before.preflight.preflightId ||
      executePending.expectedRevision !== before.plan.revision || executePending.grantId !== pending.grant.id) {
      const proofOperation = ++authorityProofRef.current;
      const proof = await options.ensureStepUpProof(STEP_UP_ACTIONS.assetRecover, { persist: false, reuseCached: false });
      if (!mountedRef.current || proofOperation !== authorityProofRef.current) return;
      executePending = {
        planId: before.plan.id,
        preflightId: before.preflight.preflightId,
        expectedRevision: before.plan.revision,
        grantId: pending.grant.id,
        proof,
        idempotencyKey: newKey("execute"),
      };
      pendingExecuteRef.current = executePending;
    }
    const { controller, operation } = begin();
    update((value) => ({ ...value, phase: "executing", error: null }));
    let durableJobId: string | null = null;
    try {
      const product = await getApi().execute(options.token, {
        planId: executePending.planId,
        expectedRevision: executePending.expectedRevision,
        preflightId: executePending.preflightId,
        grantId: executePending.grantId,
        grantSecret: pending.grantSecret,
        proof: executePending.proof,
        idempotencyKey: executePending.idempotencyKey,
        signal: controller.signal,
      });
      if (!current(operation)) return;
      if (product.status !== "available" || product.value.operation !== "execute" || product.value.jobId === null) {
        clearSensitive();
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      durableJobId = product.value.jobId;
      options.onRouteChange({ planId: before.plan.id, jobId: durableJobId }, { replace: false });
      clearSensitive();
      update((value) => ({ ...value, phase: "progress", writeGrant: null, announcement: "job_created" }));
      const loaded = await getApi().getJob(options.token, durableJobId, controller.signal);
      if (!current(operation)) return;
      if (loaded.status !== "available") {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      update((value) => ({
        ...value, phase: nextPhaseForJob(loaded.value), job: loaded.value, writeGrant: null,
        announcement: `job:${loaded.value.outcome}`,
      }));
    } catch (error) {
      if (!current(operation)) return;
      if (durableJobId !== null) {
        clearSensitive();
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable", writeGrant: null }));
        return;
      }
      const ambiguous = isAmbiguous(error);
      if (!ambiguous) clearSensitive();
      update((value) => ({ ...value, phase: ambiguous ? "impact" : "error", error: classifyError(error) }));
    }
  }, [begin, clearSensitive, current, getApi, newKey, options, update]);

  const authorizeExactMirrorDelete = useCallback(async (reason: string, confirmed: boolean) => {
    const before = stateRef.current;
    const checkpoint = before.job?.deleteCheckpoint;
    if (!confirmed || options.token === null || options.ensureStepUpProof === undefined || before.job === null ||
      before.plan === null || checkpoint == null || before.job.targetMode !== "in_place") {
      throw new Error("delete confirmation unavailable");
    }
    let pending = pendingDeleteRef.current;
    if (pending === null || pending.jobId !== before.job.id || pending.planId !== before.plan.id ||
      pending.checkpointId !== checkpoint.id || pending.attemptId !== checkpoint.attemptId ||
      pending.expectedRevision !== checkpoint.expectedPlanRevision || pending.reason !== reason) {
      const proofOperation = ++authorityProofRef.current;
      const proof = await options.ensureStepUpProof(STEP_UP_ACTIONS.assetRecover, { persist: false, reuseCached: false });
      if (!mountedRef.current || proofOperation !== authorityProofRef.current) return;
      pending = {
        jobId: before.job.id,
        planId: before.plan.id,
        checkpointId: checkpoint.id,
        attemptId: checkpoint.attemptId,
        expectedRevision: checkpoint.expectedPlanRevision,
        reason,
        proof,
        idempotencyKey: newKey("delete"),
        grantSecret: generateRecoveryGrantSecret(options.cryptoSource),
      };
      pendingDeleteRef.current = pending;
    }
    const { controller, operation } = begin();
    update((value) => ({ ...value, phase: "delete_authorization", error: null }));
    try {
      const product = await getApi().authorizeExactMirrorDelete(options.token, { ...pending, signal: controller.signal });
      if (!current(operation)) return;
      if (product.status !== "available" || product.value.operation !== "exact_mirror_delete_authorize" ||
        product.value.jobId !== before.job.id) {
        pendingDeleteRef.current = null;
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      pendingDeleteRef.current = null;
      update((value) => ({
        ...value,
        phase: "progress",
        job: value.job === null ? null : { ...value.job, deleteCheckpoint: null },
        announcement: "delete_authorized",
      }));
    } catch (error) {
      if (!current(operation)) return;
      const ambiguous = isAmbiguous(error);
      if (!ambiguous) pendingDeleteRef.current = null;
      update((value) => ({
        ...value,
        phase: ambiguous ? "delete_authorization" : "error",
        error: classifyError(error),
      }));
    }
  }, [begin, current, getApi, newKey, options, update]);

  const loadJobItems = useCallback(async (page: number, pageSize = 25) => {
    const job = stateRef.current.job;
    if (options.token === null || job === null) throw new Error("recovery job unavailable");
    const { controller, operation } = beginPage();
    try {
      const product = await getApi().getJobItems(options.token, { jobId: job.id, page, pageSize, signal: controller.signal });
      if (!currentPage(operation)) return;
      if (product.status !== "available" || product.value.jobId !== job.id) {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      update((value) => ({ ...value, itemPage: product.value, error: null }));
    } catch (error) {
      if (!currentPage(operation) || (error instanceof Error && error.name === "AbortError")) return;
      update((value) => ({ ...value, error: classifyError(error) }));
    }
  }, [beginPage, currentPage, getApi, options.token, update]);

  const loadJobResults = useCallback(async (page: number, pageSize = 25) => {
    const job = stateRef.current.job;
    if (options.token === null || job === null || job.targetMode !== "isolated" || job.resultSet?.lifecycle !== "ready") {
      throw new Error("recovery results unavailable");
    }
    const { controller, operation } = beginPage();
    try {
      const product = await getApi().getJobResults(options.token, { jobId: job.id, page, pageSize, signal: controller.signal });
      if (!currentPage(operation)) return;
      if (product.status !== "available" || product.value.jobId !== job.id ||
        product.value.resultSet.id !== job.resultSet.id) {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      update((value) => ({ ...value, resultPage: product.value, error: null }));
    } catch (error) {
      if (!currentPage(operation) || (error instanceof Error && error.name === "AbortError")) return;
      update((value) => ({ ...value, error: classifyError(error) }));
    }
  }, [beginPage, currentPage, getApi, options.token, update]);

  const retainResults = useCallback(async (requestedDeadline: string) => {
    const job = stateRef.current.job;
    if (authToken === null || ensureStepUpProof === undefined || job === null ||
      job.targetMode !== "isolated" || job.resultSet?.lifecycle !== "ready") {
      throw new Error("recovery results unavailable");
    }
    const proofOperation = ++authorityProofRef.current;
    const proof = await ensureStepUpProof(STEP_UP_ACTIONS.recoveryResultRetain, {
      persist: false, reuseCached: false,
    });
    if (!mountedRef.current || proofOperation !== authorityProofRef.current) return;
    const { controller, operation } = begin();
    try {
      const product = await getApi().retainResults(authToken, {
        jobId: job.id, expectedRevision: job.revision, requestedDeadline, proof, signal: controller.signal,
      });
      if (!current(operation)) return;
      if (product.status !== "available" || product.value.jobId !== job.id ||
        product.value.resultSetId !== job.resultSet.id) {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      update((value) => ({
        ...value,
        job: value.job === null || value.job.resultSet === null ? value.job : {
          ...value.job,
          revision: product.value.jobRevision,
          plaintextDeadline: product.value.plaintextDeadline,
          resultSet: {
            ...value.job.resultSet,
            plaintextDeadline: product.value.plaintextDeadline,
            hardDeadline: product.value.hardDeadline,
          },
        },
        error: null,
        announcement: "results_retained",
      }));
    } catch (error) {
      if (!current(operation)) return;
      if (!isAmbiguous(error)) {
        update((value) => ({ ...value, error: classifyError(error) }));
        return;
      }
      try {
        const reconciled = await getApi().getJob(authToken, job.id, controller.signal);
        if (!current(operation)) return;
        if (reconciled.status !== "available" || reconciled.value.id !== job.id ||
          reconciled.value.resultSet?.id !== job.resultSet.id) throw new Error("recovery unavailable");
        update((value) => ({
          ...value,
          job: reconciled.value,
          phase: nextPhaseForJob(reconciled.value),
          error: null,
          announcement: "results_reconciled",
        }));
      } catch {
        if (!current(operation)) return;
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
      }
    }
  }, [authToken, begin, current, ensureStepUpProof, getApi, update]);

  const downloadResult = useCallback(async (resultId: string) => {
    const before = stateRef.current;
    const job = before.job;
    if (options.token === null || options.ensureStepUpProof === undefined || job === null ||
      job.targetMode !== "isolated" || job.resultSet?.lifecycle !== "ready" ||
      before.resultPage?.items.some((item) => item.id === resultId) !== true) {
      throw new Error("recovery result unavailable");
    }
    const proofOperation = ++authorityProofRef.current;
    const proof = await options.ensureStepUpProof(STEP_UP_ACTIONS.recoveryResultDownload, {
      persist: false, reuseCached: false,
    });
    if (!mountedRef.current || proofOperation !== authorityProofRef.current) return;
    const { controller, operation } = begin();
    try {
      const product = await getApi().issueResultDownloadTicket(options.token, {
        jobId: job.id, resultId, proof, signal: controller.signal,
      });
      if (!current(operation)) return;
      if (product.status !== "available") {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      update((value) => ({ ...value, ticket: product.value, error: null }));
      options.onDownloadTicket?.(product.value);
    } catch (error) {
      if (!current(operation)) return;
      update((value) => ({ ...value, error: classifyError(error) }));
    } finally {
      if (current(operation)) update((value) => ({ ...value, ticket: null }));
    }
  }, [begin, current, getApi, options, update]);

  const cleanupResults = useCallback(async () => {
    const job = stateRef.current.job;
    if (options.token === null || job === null || job.targetMode !== "isolated" || job.resultSet === null ||
      (job.resultSet.lifecycle !== "ready" && job.resultSet.lifecycle !== "cleanup_failed")) {
      throw new Error("recovery results unavailable");
    }
    const { controller, operation } = begin();
    try {
      const product = await getApi().cleanupResults(options.token, {
        jobId: job.id, expectedRevision: job.revision, signal: controller.signal,
      });
      if (!current(operation)) return;
      if (product.status !== "available" || product.value.jobId !== job.id ||
        product.value.resultSetId !== job.resultSet.id) {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      update((value) => ({
        ...value,
        job: value.job === null || value.job.resultSet === null ? value.job : {
          ...value.job,
          resultSet: { ...value.job.resultSet, lifecycle: product.value.lifecycle },
        },
        resultPage: null,
        phase: "result",
        error: null,
        announcement: "cleanup_scheduled",
      }));
    } catch (error) {
      if (!current(operation)) return;
      if (!isAmbiguous(error)) {
        update((value) => ({ ...value, error: classifyError(error) }));
        return;
      }
      try {
        const reconciled = await getApi().getJob(options.token, job.id, controller.signal);
        if (!current(operation)) return;
        if (reconciled.status !== "available" || reconciled.value.id !== job.id ||
          reconciled.value.resultSet?.id !== job.resultSet.id) throw new Error("recovery unavailable");
        update((value) => ({
          ...value,
          job: reconciled.value,
          resultPage: reconciled.value.resultSet?.lifecycle === "ready" ? value.resultPage : null,
          phase: nextPhaseForJob(reconciled.value),
          error: null,
          announcement: "cleanup_reconciled",
        }));
      } catch {
        if (!current(operation)) return;
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
      }
    }
  }, [begin, current, getApi, options.token, update]);

  const cancelRecovery = useCallback(async () => {
    const before = stateRef.current;
    if (options.token === null || (before.job === null && before.plan === null)) {
      throw new Error("recovery cancellation unavailable");
    }
    clearSensitive();
    const { controller, operation } = begin();
    try {
      if (before.job !== null) {
        const product = await getApi().cancelJob(options.token, {
          jobId: before.job.id, expectedRevision: before.job.revision, signal: controller.signal,
        });
        if (!current(operation)) return;
        if (product.status !== "available" || product.value.id !== before.job.id) {
          update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
          return;
        }
        update((value) => ({
          ...value,
          phase: nextPhaseForJob(product.value),
          job: product.value,
          error: null,
          announcement: `job:${product.value.outcome}`,
        }));
        return;
      }
      const plan = before.plan!;
      const product = await getApi().cancelPlan(options.token, {
        planId: plan.id, expectedRevision: plan.revision, signal: controller.signal,
      });
      if (!current(operation)) return;
      if (product.status !== "available" || product.value.id !== plan.id) {
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
        return;
      }
      update((value) => ({ ...value, phase: "verification", plan: product.value, error: null, announcement: "plan_canceled" }));
    } catch (error) {
      if (!current(operation)) return;
      if (!isAmbiguous(error)) {
        update((value) => ({ ...value, phase: "error", error: classifyError(error) }));
        return;
      }
      try {
        if (before.job !== null) {
          const reconciled = await getApi().getJob(options.token, before.job.id, controller.signal);
          if (!current(operation)) return;
          if (reconciled.status !== "available") throw new Error("recovery unavailable");
          update((value) => ({
            ...value,
            phase: nextPhaseForJob(reconciled.value),
            job: reconciled.value,
            error: null,
            announcement: `job:${reconciled.value.outcome}`,
          }));
          return;
        }
        const reconciled = await getApi().getPlan(options.token, before.plan!.id, controller.signal);
        if (!current(operation)) return;
        if (reconciled.status !== "available") throw new Error("recovery unavailable");
        update((value) => ({
          ...value, phase: reconciled.value.state === "canceled" ? "verification" : "target",
          plan: reconciled.value, error: null,
        }));
      } catch {
        if (!current(operation)) return;
        update((value) => ({ ...value, phase: "unavailable", error: "unavailable" }));
      }
    }
  }, [begin, clearSensitive, current, getApi, options.token, update]);

  const routeContextBinding = JSON.stringify([
    options.sessionKey ?? "",
    options.contextKey ?? "",
    options.token ?? "",
    options.role ?? "",
    options.planId ?? "",
    options.jobId ?? "",
  ]);
  const priorRouteContextRef = useRef(routeContextBinding);
  useEffect(() => {
    if (priorRouteContextRef.current === routeContextBinding) return;
    priorRouteContextRef.current = routeContextBinding;
    reset();
  }, [reset, routeContextBinding]);

  useEffect(() => {
    const planId = options.planId;
    if (planId === undefined || options.jobId !== undefined) return;
    if (pageVisible()) void reconcilePlan(planId);
    const visibilityChanged = () => {
      if (!pageVisible()) {
        clearWork();
        operationRef.current += 1;
        return;
      }
      void reconcilePlan(planId);
    };
    document.addEventListener("visibilitychange", visibilityChanged);
    return () => document.removeEventListener("visibilitychange", visibilityChanged);
  }, [clearWork, options.jobId, options.planId, reconcilePlan, routeContextBinding]);

  useEffect(() => {
    const jobId = options.jobId;
    if (jobId === undefined) return;
    if (pageVisible()) void reconcileJob(jobId);
    const visibilityChanged = () => {
      if (!pageVisible()) {
        clearWork();
        operationRef.current += 1;
        return;
      }
      void reconcileJob(jobId);
    };
    document.addEventListener("visibilitychange", visibilityChanged);
    return () => document.removeEventListener("visibilitychange", visibilityChanged);
  }, [clearWork, options.jobId, reconcileJob, routeContextBinding]);

  useEffect(() => {
    return () => {
      mountedRef.current = false;
      operationRef.current += 1;
      clearWork();
      clearSensitive();
    };
  }, [clearSensitive, clearWork]);

  return {
    state,
    open,
    setTarget,
    createPlan,
    runPreflight,
    overrideSecurity,
    authorizeWrite,
    execute,
    authorizeExactMirrorDelete,
    loadJobItems,
    loadJobResults,
    retainResults,
    downloadResult,
    cleanupResults,
    cancelRecovery,
    dismiss: reset,
  };
}
