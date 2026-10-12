import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type MutableRefObject, type ReactNode, type RefObject } from "react";
import { flushSync } from "react-dom";
import { useTranslation } from "react-i18next";
import {
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Circle,
  Copy,
  Loader2,
  XCircle,
} from "lucide-react";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { apiClient } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import { toast } from "@/components/ui/toast-sonner";
import { ApiError, getAuthIdentitySnapshot, getAuthSessionGeneration, isAuthTransitionActive, isStepUpRequiredError } from "@/lib/api/core";
import { useAuth } from "@/context/auth-context.hooks";
import type { AuthRole } from "@/context/auth-context.shared";
import type {
  BatchRetryEligibility,
  BatchRetryLink,
  BatchRetryReason,
  BatchRetryResult,
  BatchRetrySource,
  BatchStatus,
} from "@/lib/api/batch-api";
import { BatchRetryContractError } from "@/lib/api/batch-command-response";
import { useStepUpAction } from "@/hooks/use-step-up-action";
import { sensitiveStepUpBlock } from "@/lib/sensitive-step-up";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type { LogEvent } from "@/types/domain";
import { BatchCommandRetryDialog, type BatchRetryRowSnapshot } from "@/components/batch-command-retry-dialog";

type BatchResultDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  batchId: string | null;
  retain: boolean;
  token: string;
};

type BatchResultLifetime = {
  generation: number;
  sessionGeneration: number;
  token: string;
  role: AuthRole | null;
  open: boolean;
};

type CloseBridge = MutableRefObject<(() => boolean) | null>;

type BatchResultViewProps = BatchResultDialogProps & {
  closeBridgeRef?: CloseBridge;
  returnFocusRef?: RefObject<HTMLElement | null>;
};

type HeldRetry = {
  batchId: string;
  submission: RetrySubmission;
};

type CarrySnapshot = {
  generation: number;
  heldRetry: HeldRetry | null;
  suppressCleanup: boolean;
};

type BatchResultSessionProps = BatchResultViewProps & {
  heldRetry: HeldRetry | null;
  suppressCleanup: boolean;
  carryGeneration: number;
  onCarry: (generation: number, next: { heldRetry: HeldRetry | null; suppressCleanup: boolean }) => void;
};

type RetryPhase = "review" | "submitting" | "unknown" | "created";

type RetrySubmission = {
  fingerprint: string;
  key: string;
  sources: BatchRetrySource[];
  rows: BatchRetryRowSnapshot[];
  phase: RetryPhase;
  postSent: boolean;
  ambiguous: boolean;
};

type RetryMetadata = {
  retain: boolean;
  sources: BatchRetryEligibility[];
  links: BatchRetryLink[];
};

type RetryRecovery =
  | { kind: "idle" }
  | { kind: "reading" }
  | { kind: "read_failed" }
  | { kind: "metadata_missing" }
  | { kind: "no_links" }
  | { kind: "deleted" }
  | { kind: "conflict" }
  | { kind: "same_child"; batchId: string };

type SubmissionOutcome =
  | { kind: "stale" }
  | { kind: "unknown" }
  | { kind: "created"; created: BatchRetryResult };

const BATCH_LOG_LIMIT = 50;
const RETRY_BATCH_ID = /^[A-Z2-7]{26,64}$/;
const RETRY_REASONS = new Set<BatchRetryReason>([
  "eligible",
  "legacy",
  "source_changed",
  "source_missing",
  "not_failed",
  "active",
  "effects_pending",
  "history_unverified",
  "already_retried",
]);

type TaskLogState =
  | { status: "loading" }
  | { status: "error" }
  | { status: "loaded"; logs: LogEvent[] };

const statusIcon: Record<string, ReactNode> = {
  success: <CheckCircle2 className="size-4 shrink-0 text-success" />,
  failed: <XCircle className="size-4 shrink-0 text-destructive" />,
  running: <Loader2 className="size-4 shrink-0 animate-spin text-warning" />,
  pending: <Circle className="size-4 shrink-0 text-muted-foreground" />,
};

const activeBatchTaskStatuses = new Set(["pending", "running", "retrying"]);

const RETRY_REASON_KEY: Record<BatchRetryReason, string> = {
  eligible: "batch.retryFailureExecution",
  legacy: "batch.retryReasonLegacy",
  source_changed: "batch.retryReasonSourceChanged",
  source_missing: "batch.retryReasonSourceMissing",
  not_failed: "batch.retryReasonNotFailed",
  active: "batch.retryReasonActive",
  effects_pending: "batch.retryReasonEffectsPending",
  history_unverified: "batch.retryReasonHistoryUnverified",
  already_retried: "batch.retryReasonAlreadyRetried",
};

function isBatchTaskActive(task: BatchStatus["tasks"][number]) {
  if (task.dispatchStatus === "pending" || task.dispatchStatus === "dispatching") return true;
  return activeBatchTaskStatuses.has(task.status) && !(task.status === "pending" && task.dispatchStatus === "failed");
}

function isSafeId(value: unknown, minimum: number): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= minimum;
}

function isRetryBatchId(value: unknown): value is string {
  return typeof value === "string" && RETRY_BATCH_ID.test(value);
}

function sortSources(sources: readonly BatchRetrySource[]): BatchRetrySource[] {
  return [...sources].sort((left, right) => left.taskId - right.taskId || left.runId - right.runId);
}

function retryFingerprint(sourceBatchId: string, sources: readonly BatchRetrySource[], authGeneration: number): string {
  const pairs = sortSources(sources).map((source) => [source.taskId, source.runId]);
  return JSON.stringify({ sourceBatchId, sources: pairs, authGeneration });
}

function canSendRetry(sourceBatchId: string, sources: readonly BatchRetrySource[], key: string): boolean {
  if (!isRetryBatchId(sourceBatchId)) return false;
  if (key.length === 0 || key.length > 256 || /\s/u.test(key)) return false;
  if (sources.length === 0) return false;
  const seen = new Set<number>();
  for (const source of sources) {
    if (!isSafeId(source.taskId, 1) || !isSafeId(source.runId, 0) || seen.has(source.taskId)) return false;
    seen.add(source.taskId);
  }
  return true;
}

function isRetrySourceShape(source: BatchRetryEligibility, status: BatchStatus): boolean {
  if (!isSafeId(source.taskId, 1) || !isSafeId(source.nodeId, 1) || !isSafeId(source.runId, 0)) return false;
  if (typeof source.eligible !== "boolean" || !RETRY_REASONS.has(source.reason)) return false;
  if (source.failureKind !== null && source.failureKind !== "dispatch" && source.failureKind !== "execution") return false;
  if (source.retryBatchId !== null && (typeof source.retryBatchId !== "string" || source.retryBatchId.trim() === "")) return false;
  const operable = (source.failureKind === "dispatch" && source.runId === 0)
    || (source.failureKind === "execution" && source.runId > 0);
  const task = status.tasks.find((item) => item.id === source.taskId && item.nodeId === source.nodeId);
  if (source.reason === "eligible" || source.eligible) {
    if (source.reason !== "eligible" || source.eligible !== true || source.retryBatchId !== null || !operable || !task) return false;
  }
  if (source.retryBatchId !== null && source.eligible) return false;
  if (source.reason === "already_retried" && (source.eligible || source.retryBatchId === null || !operable)) return false;
  return true;
}

function isRetryLinkShape(link: BatchRetryLink): boolean {
  return isSafeId(link.taskId, 1)
    && isSafeId(link.runId, 0)
    && typeof link.batchId === "string"
    && link.batchId.trim() !== ""
    && typeof link.deleted === "boolean";
}

function parseRetryMetadata(status: BatchStatus): RetryMetadata | null {
  if (typeof status.retain !== "boolean" || !Array.isArray(status.retrySources) || !Array.isArray(status.retryLinks)) return null;
  const seen = new Set<number>();
  const sources: BatchRetryEligibility[] = [];
  for (const source of status.retrySources) {
    if (!isRetrySourceShape(source, status) || seen.has(source.taskId)) return null;
    seen.add(source.taskId);
    sources.push(source);
  }
  const links: BatchRetryLink[] = [];
  const linkSeen = new Set<string>();
  for (const link of status.retryLinks) {
    if (!isRetryLinkShape(link)) return null;
    const identity = `${link.taskId}:${link.runId}:${link.batchId}`;
    if (linkSeen.has(identity)) return null;
    linkSeen.add(identity);
    links.push(link);
  }
  return { retain: status.retain, sources, links };
}

function operableSnapshot(status: BatchStatus, metadata: RetryMetadata, source: BatchRetryEligibility): BatchRetryRowSnapshot | null {
  if (source.eligible !== true || source.reason !== "eligible" || source.retryBatchId !== null) return null;
  if (metadata.links.some((link) => link.taskId === source.taskId && link.runId === source.runId)) return null;
  const task = status.tasks.find((item) => item.id === source.taskId && item.nodeId === source.nodeId);
  if (!task) return null;
  if (source.failureKind === "dispatch" && source.runId === 0) {
    return { taskId: source.taskId, runId: 0, nodeId: source.nodeId, nodeName: task.nodeName, failureKind: "dispatch" };
  }
  if (source.failureKind === "execution" && source.runId > 0) {
    return { taskId: source.taskId, runId: source.runId, nodeId: source.nodeId, nodeName: task.nodeName, failureKind: "execution" };
  }
  return null;
}

function reviewStillEligible(status: BatchStatus, rows: readonly BatchRetryRowSnapshot[]): boolean {
  const metadata = parseRetryMetadata(status);
  if (!metadata || rows.length === 0) return false;
  return rows.every((row) => {
    const source = metadata.sources.find((item) => item.taskId === row.taskId);
    const snapshot = source ? operableSnapshot(status, metadata, source) : null;
    return Boolean(snapshot
      && snapshot.runId === row.runId
      && snapshot.nodeId === row.nodeId
      && snapshot.nodeName === row.nodeName
      && snapshot.failureKind === row.failureKind);
  });
}

function classifyRecovery(status: BatchStatus, sources: readonly BatchRetrySource[]): Exclude<RetryRecovery, { kind: "idle" | "reading" }> {
  const metadata = parseRetryMetadata(status);
  if (!metadata) return { kind: "metadata_missing" };
  const groups = sources.map((source) => metadata.links.filter((link) => link.taskId === source.taskId && link.runId === source.runId));
  if (groups.every((group) => group.length === 0)) return { kind: "no_links" };
  if (groups.some((group) => group.length === 0)) return { kind: "conflict" };
  const batchIds = new Set(groups.flat().map((link) => link.batchId));
  if (batchIds.size !== 1) return { kind: "conflict" };
  const deleted = groups.flat().map((link) => link.deleted);
  if (deleted.every(Boolean)) return { kind: "deleted" };
  if (deleted.some(Boolean)) return { kind: "conflict" };
  const batchId = groups[0]?.[0]?.batchId;
  if (!batchId) return { kind: "conflict" };
  return { kind: "same_child", batchId };
}

function usableRetryResult(result: BatchRetryResult, sourceBatchId: string, sources: readonly BatchRetrySource[]): boolean {
  if (!isRetryBatchId(result.batchId) || result.batchId === sourceBatchId) return false;
  if (result.sourceBatchId !== sourceBatchId || typeof result.retain !== "boolean") return false;
  if (!Array.isArray(result.taskIds) || !Array.isArray(result.runIds)) return false;
  if (result.taskIds.length === 0 || result.taskIds.length !== result.runIds.length) return false;
  if (result.taskIds.some((id) => !isSafeId(id, 1)) || result.runIds.some((id) => !isSafeId(id, 0))) return false;
  if (new Set(result.taskIds).size !== result.taskIds.length) return false;
  const sourceIds = new Set(sources.map((source) => source.taskId));
  return result.taskIds.every((id) => !sourceIds.has(id));
}

function retryTransportStatus(error: ApiError): number | undefined {
  const status = error.httpStatus;
  if (typeof status !== "number" || !Number.isInteger(status)) return undefined;
  return status;
}

// Step-up compatibility stays on the status field the hook already reads.
// An unusable HTTP 200 may repeat that code in the envelope; that is not a challenge.
function isExplicitStepUpRejection(error: unknown): boolean {
  if (!isStepUpRequiredError(error)) return false;
  const httpStatus = retryTransportStatus(error);
  return httpStatus === undefined || httpStatus === error.status;
}

function isAmbiguousRetryFailure(error: unknown): boolean {
  if (isExplicitStepUpRejection(error)) return false;
  if (error instanceof BatchRetryContractError) return true;
  if (!(error instanceof ApiError)) return true;
  const httpStatus = retryTransportStatus(error);
  if (httpStatus !== undefined && httpStatus >= 200 && httpStatus < 300) return true;
  if (typeof error.status !== "number") return true;
  const rejectionStatus = httpStatus ?? error.status;
  return rejectionStatus >= 500 || rejectionStatus === 0;
}


function cloneSubmission(submission: RetrySubmission, patch: Partial<RetrySubmission> = {}): RetrySubmission {
  return {
    ...submission,
    ...patch,
    sources: (patch.sources ?? submission.sources).map((source) => ({ ...source })),
    rows: (patch.rows ?? submission.rows).map((row) => ({ ...row })),
  };
}

function sameHeldRetry(left: HeldRetry | null, right: HeldRetry | null): boolean {
  if (left === right) return true;
  if (!left || !right) return false;
  if (left.batchId !== right.batchId) return false;
  const a = left.submission;
  const b = right.submission;
  if (a.key !== b.key || a.phase !== b.phase || a.postSent !== b.postSent || a.ambiguous !== b.ambiguous || a.fingerprint !== b.fingerprint) return false;
  if (a.sources.length !== b.sources.length || a.rows.length !== b.rows.length) return false;
  return a.sources.every((source, index) => source.taskId === b.sources[index]?.taskId && source.runId === b.sources[index]?.runId)
    && a.rows.every((row, index) => row.taskId === b.rows[index]?.taskId && row.runId === b.rows[index]?.runId && row.nodeId === b.rows[index]?.nodeId);
}

function heldUnknownSubmission(held: HeldRetry | null, batchId: string | null): RetrySubmission | null {
  if (!batchId || !held || held.batchId !== batchId) return null;
  if (!held.submission.postSent || held.submission.phase === "created") return null;
  return cloneSubmission(held.submission, { phase: "unknown", ambiguous: true });
}

function isWriter(role: AuthRole | null): boolean {
  return role === "admin" || role === "operator";
}

export function BatchResultDialog(props: BatchResultDialogProps) {
  return <BatchResultDialogView {...props} />;
}

function BatchResultDialogView(props: BatchResultViewProps) {
  useAuth();
  const [boundary, setBoundary] = useState({
    batchId: props.batchId,
    open: props.open,
    generation: 0,
  });
  const [carry, setCarry] = useState<CarrySnapshot>({
    generation: 0,
    heldRetry: null,
    suppressCleanup: false,
  });
  const resetCarry = boundary.batchId !== props.batchId || (props.open && !boundary.open);
  const nextGeneration = resetCarry ? boundary.generation + 1 : boundary.generation;
  const visibleCarry: CarrySnapshot = resetCarry
    ? { generation: nextGeneration, heldRetry: null, suppressCleanup: false }
    : carry;
  if (resetCarry || boundary.open !== props.open || boundary.batchId !== props.batchId) {
    setBoundary({ batchId: props.batchId, open: props.open, generation: nextGeneration });
    if (resetCarry) setCarry(visibleCarry);
  }
  const onCarry = useCallback((generation: number, next: { heldRetry: HeldRetry | null; suppressCleanup: boolean }) => {
    setCarry((current) => {
      if (current.generation !== generation) return current;
      if (current.suppressCleanup === next.suppressCleanup && sameHeldRetry(current.heldRetry, next.heldRetry)) return current;
      return { generation, heldRetry: next.heldRetry, suppressCleanup: next.suppressCleanup };
    });
  }, []);

  return (
    <BatchResultSession
      key={JSON.stringify([props.open, props.batchId, props.token, getAuthSessionGeneration()])}
      {...props}
      heldRetry={visibleCarry.heldRetry}
      suppressCleanup={visibleCarry.suppressCleanup}
      carryGeneration={visibleCarry.generation}
      onCarry={onCarry}
    />
  );
}

function BatchResultSession({
  open,
  onOpenChange,
  batchId,
  retain,
  token,
  heldRetry,
  suppressCleanup,
  carryGeneration,
  onCarry,
  closeBridgeRef,
  returnFocusRef,
}: BatchResultSessionProps) {
  const copyHintId = useId();
  const retryDomId = useId();
  const { t } = useTranslation();
  const { role, clearStepUpProof, totpEnabled } = useAuth();
  const clearStepUpProofRef = useRef(clearStepUpProof);
  const lifetimeRef = useRef<BatchResultLifetime>({
    generation: 0,
    sessionGeneration: getAuthSessionGeneration(),
    token,
    role,
    open,
  });
  const statusTokenRef = useRef<string | null>(null);
  const statusGenerationRef = useRef<number | null>(null);
  const [status, setStatus] = useState<BatchStatus | null>(null);
  const [statusRole, setStatusRole] = useState<AuthRole | null>(null);
  const [sessionGeneration] = useState(getAuthSessionGeneration);
  const [error, setError] = useState("");
  const [expandedTaskId, setExpandedTaskId] = useState<number | null>(null);
  const [taskLogStates, setTaskLogStates] = useState<Record<number, TaskLogState>>({});
  const taskLogStatesRef = useRef<Record<number, TaskLogState>>({});
  const updateTaskLogStates = useCallback((states: Record<number, TaskLogState>) => {
    taskLogStatesRef.current = states;
    setTaskLogStates(states);
  }, []);
  const [copyPending, setCopyPending] = useState(false);
  const copyPendingRef = useRef(false);
  const [selectedRows, setSelectedRows] = useState<BatchRetryRowSnapshot[]>([]);
  const [reviewRows, setReviewRows] = useState<BatchRetryRowSnapshot[]>([]);
  const [reviewRetain, setReviewRetain] = useState(retain);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [confirmError, setConfirmError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const restoredSubmission = heldUnknownSubmission(heldRetry, batchId);
  const [submission, setSubmissionState] = useState<RetrySubmission | null>(restoredSubmission);
  const submissionRef = useRef<RetrySubmission | null>(restoredSubmission);
  const [retryPhase, setRetryPhase] = useState<RetryPhase>(restoredSubmission ? "unknown" : "review");
  const [frozenRows, setFrozenRows] = useState<BatchRetryRowSnapshot[]>(restoredSubmission?.rows ?? []);
  const [recovery, setRecovery] = useState<RetryRecovery>({ kind: "idle" });
  const [retryNotice, setRetryNotice] = useState("");
  const [child, setChild] = useState<{ batchId: string; retain: boolean } | null>(null);
  const heldRetryRef = useRef(heldRetry);
  const suppressCleanupRef = useRef(suppressCleanup);
  const confirmLockRef = useRef(false);
  const recoveryGenerationRef = useRef(0);
  const childCloseRef = useRef<(() => boolean) | null>(null);
  const retryButtonRef = useRef<HTMLButtonElement>(null);
  const focusReturnRef = useRef<HTMLElement | null>(null);
  const mountedRef = useRef(true);
  const withStepUp = useStepUpAction(STEP_UP_ACTIONS.batchCommandRetry, { persist: false, reuseCached: false });
  const [appliedRole, setAppliedRole] = useState(role);
  if (appliedRole !== role) {
    setAppliedRole(role);
    setStatusRole(null);
    setExpandedTaskId(null);
    setCopyPending(false);
    setSelectedRows([]);
    setConfirmOpen(false);
    setConfirmError(null);
    setReviewRows([]);
    setRetryNotice("");
    if (submission?.postSent && submission.phase !== "created") {
      setSubmissionState(cloneSubmission(submission, { phase: "unknown", ambiguous: true }));
      setRetryPhase("unknown");
    } else if (!submission || submission.phase !== "created") {
      setSubmissionState(null);
      setRetryPhase("review");
      setFrozenRows([]);
      setRecovery({ kind: "idle" });
    }
  }
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  useLayoutEffect(() => {
    clearStepUpProofRef.current = clearStepUpProof;
  });
  useLayoutEffect(() => {
    submissionRef.current = submission;
  }, [submission]);
  useLayoutEffect(() => {
    heldRetryRef.current = heldRetry;
    suppressCleanupRef.current = suppressCleanup;
  }, [heldRetry, suppressCleanup]);
  useLayoutEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  useLayoutEffect(() => {
    if (lifetimeRef.current.role !== role) {
      confirmLockRef.current = false;
      updateTaskLogStates({});
      statusGenerationRef.current = null;
      copyPendingRef.current = false;
      clearStepUpProofRef.current(STEP_UP_ACTIONS.batchCommandRetry);
    }
    lifetimeRef.current = {
      generation: lifetimeRef.current.generation + 1,
      sessionGeneration: getAuthSessionGeneration(),
      token,
      role,
      open,
    };
    return () => {
      lifetimeRef.current = {
        ...lifetimeRef.current,
        generation: lifetimeRef.current.generation + 1,
      };
    };
  }, [open, role, token, updateTaskLogStates]);

  const stopPolling = useCallback(() => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
  }, []);

  const fetchStatus = useCallback(() => {
    if (!batchId) return;
    const generation = lifetimeRef.current.generation;
    const requestSessionGeneration = lifetimeRef.current.sessionGeneration;
    const requestToken = lifetimeRef.current.token;
    if (!requestToken || requestSessionGeneration !== getAuthSessionGeneration()) return;
    return apiClient.getBatchStatus(requestToken, batchId).then((result) => {
      if (lifetimeRef.current.generation !== generation || getAuthSessionGeneration() !== requestSessionGeneration) return;
      statusGenerationRef.current = generation;
      statusTokenRef.current = requestToken;
      setStatus(result);
      setStatusRole(lifetimeRef.current.role);
      setError("");

      const hasActive = result.tasks.some(isBatchTaskActive);
      if (!hasActive) {
        stopPolling();
      }
    }).catch((err: unknown) => {
      if (lifetimeRef.current.generation !== generation || getAuthSessionGeneration() !== requestSessionGeneration) return;
      setError(err instanceof Error ? err.message : t("batch.fetchStatusFailed"));
    });
  }, [batchId, stopPolling, t]);

  useEffect(() => {
    if (!open || !batchId || !token) {
      stopPolling();
      return;
    }

    void fetchStatus();
    intervalRef.current = setInterval(() => {
      void fetchStatus();
    }, 3_000);

    return stopPolling;
  }, [open, batchId, token, role, fetchStatus, stopPolling]);

  const loadTaskLogs = useCallback(
    async (taskId: number) => {
      if (taskLogStatesRef.current[taskId]) return;
      const generation = lifetimeRef.current.generation;
      const requestSessionGeneration = lifetimeRef.current.sessionGeneration;
      const requestToken = lifetimeRef.current.token;
      if (!requestToken || requestSessionGeneration !== getAuthSessionGeneration()) return;
      updateTaskLogStates({ ...taskLogStatesRef.current, [taskId]: { status: "loading" } });
      try {
        const logs = await apiClient.getTaskLogs(requestToken, taskId, { limit: BATCH_LOG_LIMIT });
        if (lifetimeRef.current.generation !== generation || getAuthSessionGeneration() !== requestSessionGeneration) return;
        updateTaskLogStates({ ...taskLogStatesRef.current, [taskId]: { status: "loaded", logs } });
      } catch {
        if (lifetimeRef.current.generation !== generation || getAuthSessionGeneration() !== requestSessionGeneration) return;
        updateTaskLogStates({ ...taskLogStatesRef.current, [taskId]: { status: "error" } });
      }
    },
    [updateTaskLogStates]
  );

  const handleToggleExpand = useCallback(
    (task: BatchStatus["tasks"][number]) => {
      if (expandedTaskId === task.id) {
        setExpandedTaskId(null);
        return;
      }
      setExpandedTaskId(task.id);
      if (
        task.dispatchStatus !== "pending"
        && task.dispatchStatus !== "dispatching"
        && task.dispatchStatus !== "failed"
        && !activeBatchTaskStatuses.has(task.status)
      ) {
        void loadTaskLogs(task.id);
      }
    },
    [expandedTaskId, loadTaskLogs]
  );

  function sessionMatches(captured: BatchResultLifetime, write: boolean): boolean {
    const current = lifetimeRef.current;
    const identity = getAuthIdentitySnapshot();
    const roleAllowed = write ? isWriter(captured.role) : captured.role === "admin" || captured.role === "operator" || captured.role === "viewer";
    return Boolean(current.open
      && roleAllowed
      && captured.generation === current.generation
      && captured.sessionGeneration === getAuthSessionGeneration()
      && captured.token === current.token
      && captured.role === current.role
      && captured.token.length > 0
      && !isAuthTransitionActive()
      && identity.token === captured.token
      && identity.role === captured.role);
  }

  function commitSubmission(next: RetrySubmission | null) {
    submissionRef.current = next;
    setSubmissionState(next);
  }

  function rememberSent(next: RetrySubmission) {
    if (!batchId || !next.postSent || next.phase === "created") return;
    const held = { batchId, submission: cloneSubmission(next) };
    heldRetryRef.current = held;
    suppressCleanupRef.current = true;
    onCarry(carryGeneration, { heldRetry: held, suppressCleanup: true });
  }

  function publishUnknown(current: RetrySubmission) {
    const next = cloneSubmission(current, { phase: "unknown", ambiguous: true, postSent: true });
    commitSubmission(next);
    rememberSent(next);
    setRetryPhase("unknown");
    setConfirmOpen(false);
    setSaving(false);
  }

  function releaseSubmission() {
    commitSubmission(null);
    if (heldRetryRef.current?.batchId === batchId) {
      heldRetryRef.current = null;
      onCarry(carryGeneration, { heldRetry: null, suppressCleanup: false });
    }
    suppressCleanupRef.current = false;
    setRetryPhase("review");
    setFrozenRows([]);
  }

  function clearSubmission() {
    const current = submissionRef.current;
    if (current?.postSent && current.ambiguous) {
      publishUnknown(current);
      return;
    }
    releaseSubmission();
  }

  function markSubmitting(next: RetrySubmission) {
    commitSubmission(next);
    flushSync(() => {
      setRetryPhase("submitting");
      setSaving(true);
      setConfirmError(null);
    });
  }

  async function recover(captured: BatchResultLifetime, sources: readonly BatchRetrySource[]) {
    if (!batchId || sources.length === 0) return;
    if (captured.generation !== lifetimeRef.current.generation || getAuthSessionGeneration() !== captured.sessionGeneration) return;
    const generation = recoveryGenerationRef.current + 1;
    recoveryGenerationRef.current = generation;
    setRecovery({ kind: "reading" });
    try {
      const next = await apiClient.getBatchStatus(captured.token, batchId);
      if (generation !== recoveryGenerationRef.current) return;
      if (captured.generation !== lifetimeRef.current.generation || getAuthSessionGeneration() !== captured.sessionGeneration) return;
      setRecovery(classifyRecovery(next, sources));
    } catch {
      if (generation !== recoveryGenerationRef.current) return;
      if (captured.generation !== lifetimeRef.current.generation || getAuthSessionGeneration() !== captured.sessionGeneration) return;
      setRecovery({ kind: "read_failed" });
    }
  }

  function enterUnknown(captured: BatchResultLifetime, sources: readonly BatchRetrySource[]) {
    if (!mountedRef.current || captured.generation !== lifetimeRef.current.generation) return;
    if (getAuthSessionGeneration() !== captured.sessionGeneration || !lifetimeRef.current.open) return;
    if (submissionRef.current?.postSent) {
      publishUnknown(submissionRef.current);
    } else {
      if (submissionRef.current) commitSubmission(cloneSubmission(submissionRef.current, { phase: "unknown" }));
      setRetryPhase("unknown");
      setConfirmOpen(false);
      setSaving(false);
    }
    if (!sessionMatches(captured, false)) return;
    void recover(captured, sources);
  }

  function releaseStale(captured: BatchResultLifetime, mode: "new" | "continue", pendingKey: string, pendingSources: readonly BatchRetrySource[]): boolean {
    if (sessionMatches(captured, true)) return false;
    if (!mountedRef.current || captured.generation !== lifetimeRef.current.generation) return true;
    if (submissionRef.current?.postSent) {
      enterUnknown(captured, pendingSources);
      return true;
    }
    if (mode === "continue" && submissionRef.current?.key === pendingKey) {
      commitSubmission(cloneSubmission(submissionRef.current, { phase: "unknown", ambiguous: true }));
      setRetryPhase("unknown");
      return true;
    }
    if (submissionRef.current?.phase === "submitting") clearSubmission();
    return true;
  }

  function showChild(childBatchId: string, childRetain: boolean, focus: HTMLElement | null) {
    if (child && child.batchId !== childBatchId && childCloseRef.current && !childCloseRef.current()) return;
    focusReturnRef.current = focus;
    setChild({ batchId: childBatchId, retain: childRetain });
  }

  async function openLinkedChild(childBatchId: string, focus: HTMLElement | null) {
    const captured = { ...lifetimeRef.current };
    if (!sessionMatches(captured, false)) return;
    try {
      const next = await apiClient.getBatchStatus(captured.token, childBatchId);
      if (!sessionMatches(captured, false)) return;
      showChild(childBatchId, typeof next.retain === "boolean" ? next.retain : true, focus);
    } catch (err: unknown) {
      if (!sessionMatches(captured, false)) return;
      setRetryNotice(err instanceof Error ? err.message : t("batch.fetchStatusFailed"));
    }
  }

  async function runSubmission(mode: "new" | "continue") {
    if (confirmLockRef.current || !batchId) return;
    confirmLockRef.current = true;
    const captured = { ...lifetimeRef.current };
    let rows: BatchRetryRowSnapshot[] = [];
    let sources: BatchRetrySource[] = [];
    let key = "";
    try {
      if (!sessionMatches(captured, true)) return;
      const block = sensitiveStepUpBlock({ token: captured.token, totpEnabled });
      if (block !== "ready") {
        setConfirmError(t(block === "paused" ? "stepUp.authTransitioning" : "stepUp.totpRequired"));
        return;
      }

      if (mode === "continue") {
        const existing = submissionRef.current;
        if (!existing || existing.phase !== "unknown" || existing.postSent !== true) return;
        const fingerprint = retryFingerprint(batchId, existing.sources, getAuthSessionGeneration());
        if (existing.fingerprint !== fingerprint) return;
        rows = existing.rows;
        sources = existing.sources;
        key = existing.key;
        if (!canSendRetry(batchId, sources, key)) {
          setConfirmError(t("common.operationFailed"));
          return;
        }
        markSubmitting({ ...existing, phase: "submitting" });
        let probed: Exclude<RetryRecovery, { kind: "idle" | "reading" }>;
        try {
          const next = await apiClient.getBatchStatus(captured.token, batchId);
          if (releaseStale(captured, "continue", key, sources)) return;
          probed = classifyRecovery(next, sources);
        } catch {
          if (releaseStale(captured, "continue", key, sources)) return;
          if (submissionRef.current?.key === key) {
            commitSubmission(cloneSubmission(submissionRef.current, { phase: "unknown", ambiguous: true }));
          }
          setRetryPhase("unknown");
          setRecovery({ kind: "read_failed" });
          return;
        }
        setRecovery(probed);
        if (probed.kind !== "no_links") {
          if (submissionRef.current?.key === key) {
            commitSubmission(cloneSubmission(submissionRef.current, { phase: "unknown", ambiguous: true }));
          }
          setRetryPhase("unknown");
          return;
        }
      } else {
        if (submissionRef.current?.phase === "unknown" || submissionRef.current?.phase === "submitting") return;
        if (heldRetryRef.current?.batchId === batchId && heldRetryRef.current.submission.postSent && heldRetryRef.current.submission.phase !== "created") return;
        rows = [...reviewRows].sort((left, right) => left.taskId - right.taskId || left.runId - right.runId);
        if (rows.length === 0) return;
        sources = sortSources(rows.map((row) => ({ taskId: row.taskId, runId: row.runId })));
        key = crypto.randomUUID();
        const fingerprint = retryFingerprint(batchId, sources, captured.sessionGeneration);
        if (!canSendRetry(batchId, sources, key)) {
          setConfirmError(t("common.operationFailed"));
          return;
        }
        markSubmitting({ fingerprint, key, sources, rows, phase: "submitting", postSent: false, ambiguous: false });
        setFrozenRows(rows);
        let fresh: BatchStatus;
        try {
          fresh = await apiClient.getBatchStatus(captured.token, batchId);
        } catch (err: unknown) {
          if (releaseStale(captured, "new", key, sources)) return;
          clearSubmission();
          setConfirmError(err instanceof Error ? err.message : t("batch.fetchStatusFailed"));
          return;
        }
        if (releaseStale(captured, "new", key, sources)) return;
        if (submissionRef.current?.key !== key) {
          const current = submissionRef.current;
          if (!current) setRetryPhase("review");
          else if (current.phase !== "submitting") setRetryPhase(current.phase);
          return;
        }
        statusGenerationRef.current = captured.generation;
        statusTokenRef.current = captured.token;
        setStatus(fresh);
        setStatusRole(captured.role);
        setError("");
        if (!reviewStillEligible(fresh, rows)) {
          clearSubmission();
          setConfirmOpen(false);
          setConfirmError(null);
          setReviewRows([]);
          setRetryNotice(t("batch.retryReviewChanged"));
          return;
        }
      }

      const outcome = await withStepUp(async (proof): Promise<SubmissionOutcome> => {
        if (!sessionMatches(captured, true) || submissionRef.current?.key !== key) return { kind: "stale" };
        try {
          const nodeIds = [...new Set(rows.map((row) => row.nodeId))].sort((left, right) => left - right);
          await apiClient.requestBatchCommandRetryCredentialGrant(captured.token, {
            nodeIds,
            reason: t("batch.retryGrantReason", { count: nodeIds.length }),
            requestedTtlSeconds: 600,
          }, proof);
          if (!sessionMatches(captured, true) || submissionRef.current?.key !== key) return { kind: "stale" };
          if (submissionRef.current) {
            const sent = cloneSubmission(submissionRef.current, { postSent: true });
            commitSubmission(sent);
            rememberSent(sent);
          }
          suppressCleanupRef.current = true;
          const created = await apiClient.retryBatchCommand(captured.token, batchId, sources, proof, key);
          if (!sessionMatches(captured, true)) return { kind: "stale" };
          if (!usableRetryResult(created, batchId, sources)) return { kind: "unknown" };
          return { kind: "created", created };
        } catch (err: unknown) {
          if (isExplicitStepUpRejection(err)) throw err;
          if (submissionRef.current?.postSent && isAmbiguousRetryFailure(err)) return { kind: "unknown" };
          throw err;
        }
      });

      if (releaseStale(captured, mode, key, sources)) return;
      if (!outcome || outcome.kind === "stale") {
        if (submissionRef.current?.postSent) enterUnknown(captured, sources);
        else if (mode === "continue" && submissionRef.current?.key === key) {
          commitSubmission(cloneSubmission(submissionRef.current, { phase: "unknown", ambiguous: true }));
          setRetryPhase("unknown");
        } else clearSubmission();
        return;
      }
      if (outcome.kind === "unknown") {
        enterUnknown(captured, sources);
        return;
      }
      if (submissionRef.current) commitSubmission(cloneSubmission(submissionRef.current, { phase: "created" }));
      if (heldRetryRef.current?.batchId === batchId) {
        heldRetryRef.current = null;
        onCarry(carryGeneration, { heldRetry: null, suppressCleanup: false });
      }
      suppressCleanupRef.current = false;
      setRetryPhase("created");
      setConfirmOpen(false);
      setConfirmError(null);
      setRecovery({ kind: "idle" });
      showChild(outcome.created.batchId, outcome.created.retain, retryButtonRef.current);
      void fetchStatus();
    } catch (err: unknown) {
      if (releaseStale(captured, mode, key, sources)) return;
      const failed = submissionRef.current;
      if (failed?.postSent && isAmbiguousRetryFailure(err)) {
        enterUnknown(captured, sources);
        return;
      }
      if (failed?.postSent && (failed.ambiguous || (mode === "continue" && failed.key === key))) {
        publishUnknown(failed);
        setConfirmError(err instanceof Error ? err.message : t("common.operationFailed"));
        return;
      }
      clearSubmission();
      setConfirmError(err instanceof Error ? err.message : t("common.operationFailed"));
    } finally {
      confirmLockRef.current = false;
      if (mountedRef.current) setSaving(false);
    }
  }

  const handleClose = useCallback((nextOpen: boolean): boolean => {
    if (!nextOpen && submissionRef.current?.phase === "submitting") return false;
    if (!nextOpen && childCloseRef.current && !childCloseRef.current()) return false;
    try {
      if (!nextOpen) {
        const current = lifetimeRef.current;
        const protect = suppressCleanupRef.current || submissionRef.current?.phase === "unknown";
        const settled = status?.tasks.every((task) => !isBatchTaskActive(task)) ?? false;
        if (
          batchId
          && !retain
          && settled
          && !protect
          && current.token.length > 0
          && statusTokenRef.current === current.token
          && isWriter(current.role)
        ) {
          void apiClient.deleteBatch(current.token, batchId).catch(() => {});
        }
      }
    } finally {
      if (!nextOpen) {
        lifetimeRef.current = { ...lifetimeRef.current, open: false, generation: lifetimeRef.current.generation + 1 };
      }
      onOpenChange(nextOpen);
    }
    return true;
  }, [batchId, onOpenChange, retain, status]);

  useLayoutEffect(() => {
    if (!closeBridgeRef) return;
    closeBridgeRef.current = () => handleClose(false);
    return () => {
      closeBridgeRef.current = null;
    };
  }, [closeBridgeRef, handleClose]);

  function ownsCopyLifetime(captured: BatchResultLifetime): boolean {
    const current = lifetimeRef.current;
    const identity = getAuthIdentitySnapshot();
    return current.open
      && captured.generation === current.generation
      && captured.sessionGeneration === getAuthSessionGeneration()
      && !isAuthTransitionActive()
      && identity.token === captured.token
      && identity.role === captured.role;
  }

  const identity = getAuthIdentitySnapshot();
  const copyAvailable = Boolean(open && batchId && token && status && !error
    && statusRole === role
    && sessionGeneration === getAuthSessionGeneration()
    && !isAuthTransitionActive()
    && identity.token === token && identity.role === role
    && (role === "admin" || role === "operator" || role === "viewer"));

  function canCopyResults(): boolean {
    const current = lifetimeRef.current;
    return Boolean(copyAvailable
      && statusGenerationRef.current === current.generation
      && current.role === role && current.token === token
      && (role === "admin" || role === "operator" || role === "viewer")
      && ownsCopyLifetime(current));
  }

  const copyHint = t("batch.copyLoadedHint", { limit: BATCH_LOG_LIMIT });

  async function handleCopyResults(): Promise<void> {
    if (copyPendingRef.current || !canCopyResults() || !status) return;
    const captured = { ...lifetimeRef.current };
    copyPendingRef.current = true;
    setCopyPending(true);
    try {
      const sections = [
        [
          t("batch.resultTitle"),
          t("batch.batchId", { id: batchId }),
          copyHint,
          t("batch.loadedResults", { loaded: status.tasks.length, total: status.total }),
        ].join("\n"),
        ...status.tasks.map((task) => {
          const taskStatus = task.dispatchStatus === "failed"
            ? t("batch.dispatchFailed")
            : task.dispatchStatus === "pending" || task.dispatchStatus === "dispatching"
              ? t("batch.dispatchPending")
              : t(`status.batch.${task.status}`, task.status);
          const lines = [
            t("batch.copyNode", { name: JSON.stringify(task.nodeName), id: task.nodeId }),
            t("batch.copyTask", { id: task.id }),
            t("batch.copyStatus", { status: taskStatus }),
          ];
          if (task.lastError) lines.push(t("batch.copyError", { error: task.lastError }));
          const logs = taskLogStates[task.id];
          if (!logs) lines.push(t("batch.logsNotLoaded"));
          else if (logs.status === "loading") lines.push(t("batch.loadingLogs"));
          else if (logs.status === "error") lines.push(t("batch.logLoadFailed"));
          else {
            lines.push(t("batch.loadedLogCount", { count: logs.logs.length, limit: BATCH_LOG_LIMIT }));
            lines.push(...logs.logs.map((log) => log.message));
          }
          return lines.join("\n");
        }),
      ];
      await navigator.clipboard.writeText(sections.join("\n\n"));
      if (ownsCopyLifetime(captured)) toast.success(t("common.copiedToClipboard"));
    } catch {
      if (ownsCopyLifetime(captured)) toast.error(t("common.copyFailed"));
    } finally {
      if (ownsCopyLifetime(captured)) {
        copyPendingRef.current = false;
        setCopyPending(false);
      }
    }
  }

  const metadata = status ? parseRetryMetadata(status) : null;
  const writer = isWriter(role);
  const retryVerified = Boolean(writer && copyAvailable && metadata);
  const selectionFrozen = !retryVerified || retryPhase === "submitting" || retryPhase === "unknown" || confirmOpen;
  const canReadRole = role === "admin" || role === "operator" || role === "viewer";
  const sameKeyCurrent = Boolean(batchId && submission?.postSent
    && submission.fingerprint === retryFingerprint(batchId, submission.sources, getAuthSessionGeneration()));

  function openConfirm() {
    if (!retryVerified || selectedRows.length === 0 || !status || typeof status.retain !== "boolean") return;
    if (submissionRef.current?.phase === "unknown" || submissionRef.current?.phase === "submitting") return;
    setReviewRows([...selectedRows].sort((left, right) => left.taskId - right.taskId || left.runId - right.runId));
    setReviewRetain(status.retain);
    setConfirmError(null);
    setConfirmOpen(true);
  }

  function toggleRow(snapshot: BatchRetryRowSnapshot) {
    if (selectionFrozen) return;
    setSelectedRows((current) => {
      if (current.some((row) => row.taskId === snapshot.taskId)) {
        return current.filter((row) => row.taskId !== snapshot.taskId);
      }
      if (current.some((row) => row.nodeId === snapshot.nodeId)) return current;
      return [...current, snapshot];
    });
  }

  const allDone = status
    ? status.tasks.every((task) => !isBatchTaskActive(task))
    : false;
  const successCount = status?.statusCounts["success"] ?? 0;
  const failedCount = status?.tasks.filter((task) => task.status === "failed" || task.dispatchStatus === "failed").length ?? 0;

  return (
    <>
      <Dialog open={open} onOpenChange={handleClose}>
        <DialogContent
          onEscapeKeyDown={(event) => {
            if (submissionRef.current?.phase === "submitting") event.preventDefault();
          }}
          onPointerDownOutside={(event) => {
            if (submissionRef.current?.phase === "submitting") event.preventDefault();
          }}
          onInteractOutside={(event) => {
            if (submissionRef.current?.phase === "submitting") event.preventDefault();
          }}
          onCloseAutoFocus={(event) => {
            const target = returnFocusRef?.current;
            if (!target?.isConnected) return;
            event.preventDefault();
            target.focus();
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("batch.resultTitle")}</DialogTitle>
            <DialogDescription>
              {t("batch.batchId", { id: batchId })}
              {status && (
                <span className="ml-2">
                  — {allDone ? t("batch.done") : t("batch.running")}
                  {status.total > 0 && (
                    <span className="ml-1 text-xs">
                      ({successCount} {t("batch.successCount")}
                      {failedCount > 0 && (
                        <span className="text-destructive">，{failedCount} {t("batch.failedCount")}</span>
                      )}
                      ，{t("batch.total", { count: status.total })})
                    </span>
                  )}
                </span>
              )}
              {!retain && retryPhase !== "unknown" && retryPhase !== "submitting" && (
                <span className="ml-2 text-xs text-muted-foreground">
                  · {t("batch.autoCleanHint")}
                </span>
              )}
            </DialogDescription>
            <DialogCloseButton />
          </DialogHeader>
          <DialogBody>
            <div className="min-w-0 space-y-2">
              <Button
                type="button"
                variant="outline"
                className="h-auto max-w-full whitespace-normal text-left"
                aria-describedby={copyHintId}
                disabled={copyPending || !copyAvailable}
                onClick={() => void handleCopyResults()}
              >
                <Copy className="size-4 shrink-0" aria-hidden />
                {t("batch.copyLoadedResults")}
              </Button>
              <p id={copyHintId} className="text-xs text-muted-foreground break-words">{copyHint}</p>
            </div>
            {retryPhase === "submitting" ? (
              <InlineAlert tone="warning">{t("batch.retrySubmitting")}</InlineAlert>
            ) : null}
            {retryPhase === "unknown" ? (
              <div className="space-y-2">
                <InlineAlert tone="warning">{t("batch.retryUnknown")}</InlineAlert>
                <p className="text-xs text-muted-foreground break-words">{t("batch.retryUnknownClose")}</p>
                {frozenRows.length > 0 ? (
                  <ul className="space-y-1 text-xs break-words">
                    {frozenRows.map((row) => (
                      <li key={row.taskId}>
                        {t("batch.retrySnapshot", {
                          name: row.nodeName,
                          nodeId: row.nodeId,
                          taskId: row.taskId,
                          runId: row.runId,
                          failure: t(row.failureKind === "dispatch" ? "batch.retryFailureDispatch" : "batch.retryFailureExecution"),
                        })}
                      </li>
                    ))}
                  </ul>
                ) : null}
                {recovery.kind === "reading" ? <p className="text-sm text-muted-foreground">{t("batch.retryReading")}</p> : null}
                {recovery.kind === "read_failed" ? <p role="alert" className="text-sm break-words">{t("batch.retryReadFailed")}</p> : null}
                {recovery.kind === "metadata_missing" ? <p role="alert" className="text-sm break-words">{t("batch.retryMetadataMissing")}</p> : null}
                {recovery.kind === "no_links" && sameKeyCurrent ? <p className="text-sm break-words">{t("batch.retryNoLinks")}</p> : null}
                {recovery.kind === "deleted" ? <p className="text-sm break-words">{t("batch.retryCreatedThenCleaned")}</p> : null}
                {recovery.kind === "conflict" ? <p role="alert" className="text-sm break-words">{t("batch.retryConflict")}</p> : null}
                {canReadRole && (recovery.kind === "idle" || recovery.kind === "reading" || recovery.kind === "read_failed" || recovery.kind === "metadata_missing") ? (
                  <Button
                    type="button"
                    variant="outline"
                    className="h-auto whitespace-normal"
                    disabled={recovery.kind === "reading"}
                    onClick={() => {
                      const submission = submissionRef.current;
                      if (!submission) return;
                      void recover({ ...lifetimeRef.current }, submission.sources);
                    }}
                  >
                    {t("batch.retryReadSource")}
                  </Button>
                ) : null}
                {writer && sameKeyCurrent && recovery.kind === "no_links" ? (
                  <Button type="button" className="h-auto whitespace-normal" onClick={() => void runSubmission("continue")}>
                    {t("batch.retryContinue")}
                  </Button>
                ) : null}
                {canReadRole && recovery.kind === "same_child" ? (
                  <Button
                    type="button"
                    variant="outline"
                    className="h-auto whitespace-normal"
                    onClick={(event) => void openLinkedChild(recovery.batchId, event.currentTarget)}
                  >
                    {t("batch.retryViewLinked")}
                  </Button>
                ) : null}
              </div>
            ) : null}
            {writer && metadata && retryPhase !== "unknown" ? (
              <Button
                ref={retryButtonRef}
                type="button"
                className="h-auto max-w-full whitespace-normal"
                disabled={!retryVerified || selectedRows.length === 0 || retryPhase === "submitting"}
                onClick={openConfirm}
              >
                {t("batch.retrySelected", { count: selectedRows.length })}
              </Button>
            ) : null}
            {retryNotice ? (
              <p role="alert" className="text-sm text-destructive break-words">{retryNotice}</p>
            ) : null}
            {error && (
              <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {error}
              </div>
            )}

            {!status && !error && (
              <div className="flex items-center justify-center py-8">
                <Loader2 className="size-5 animate-spin text-muted-foreground" />
                <span className="ml-2 text-sm text-muted-foreground">{t("common.loading")}</span>
              </div>
            )}

            {status && (
              <div className="max-h-96 space-y-1.5 overflow-y-auto">
                {status.tasks.map((task) => {
                  const isExpanded = expandedTaskId === task.id;
                  const isDone = !isBatchTaskActive(task);
                  const dispatchFailed = task.dispatchStatus === "failed";
                  const dispatchUnconfirmed = task.dispatchStatus === "pending" || task.dispatchStatus === "dispatching";
                  const logState = taskLogStates[task.id];
                  const source = metadata?.sources.find((item) => item.taskId === task.id) ?? null;
                  const snapshot = source && metadata ? operableSnapshot(status, metadata, source) : null;
                  const selected = selectedRows.find((row) => row.taskId === task.id) ?? null;
                  const sameNodeBlocked = Boolean(snapshot && !selected && selectedRows.some((row) => row.nodeId === snapshot.nodeId));
                  const showCheckbox = writer && Boolean(snapshot || selected);
                  const reasonId = `${retryDomId}-reason-${task.id}`;
                  const links = metadata && source
                    ? metadata.links.filter((link) => link.taskId === source.taskId && link.runId === source.runId)
                    : [];
                  const linkIds = new Set(links.map((link) => link.batchId));
                  const linkDeleted = links.length > 0 && links.every((link) => link.deleted);
                  const linkConflict = linkIds.size > 1 || (links.some((link) => link.deleted) && !linkDeleted);
                  const childBatchId = linkConflict ? null : (links[0]?.batchId ?? (linkDeleted ? null : source?.retryBatchId ?? null));
                  const checkboxSnapshot = snapshot ?? selected;

                  return (
                    <div key={task.id} className="rounded-md border border-border">
                      <div className="flex items-start gap-2 pr-2">
                        {showCheckbox && checkboxSnapshot ? (
                          <div className="pl-3 pt-2.5">
                            <input
                              type="checkbox"
                              className="size-4 rounded"
                              checked={Boolean(selected)}
                              disabled={selectionFrozen || sameNodeBlocked}
                              aria-label={t("batch.retrySelect", { name: task.nodeName, id: task.nodeId })}
                              aria-describedby={sameNodeBlocked ? reasonId : undefined}
                              onChange={() => {
                                if (snapshot) toggleRow(snapshot);
                                else if (selected) toggleRow(selected);
                              }}
                            />
                          </div>
                        ) : null}
                        <button
                          type="button"
                          className="flex min-w-0 flex-1 items-center gap-3 px-3 py-2 text-left hover:bg-muted/50 transition-colors"
                          onClick={() => handleToggleExpand(task)}
                          disabled={!isDone}
                        >
                          {statusIcon[dispatchFailed ? "failed" : dispatchUnconfirmed ? "pending" : task.status] ?? (
                            <Circle className="size-4 shrink-0 text-muted-foreground" />
                          )}
                          <div className="flex-1 min-w-0">
                            <div className="flex items-center gap-2">
                              <span className="text-sm font-medium truncate">
                                {task.nodeName}
                              </span>
                              <span className="shrink-0 text-xs text-muted-foreground">
                                {dispatchFailed ? t("batch.dispatchFailed") : dispatchUnconfirmed ? t("batch.dispatchPending") : t(`status.batch.${task.status}`, task.status)}
                              </span>
                            </div>
                            {task.lastError && !isExpanded && (
                              <p
                                className="mt-0.5 text-xs text-destructive truncate"
                                title={task.lastError}
                              >
                                {task.lastError}
                              </p>
                            )}
                          </div>
                          {isDone && (
                            <span className="shrink-0 text-muted-foreground">
                              {isExpanded ? (
                                <ChevronDown className="size-4" />
                              ) : (
                                <ChevronRight className="size-4" />
                              )}
                            </span>
                          )}
                        </button>
                      </div>
                      {source ? (
                        <div className="space-y-1 border-t border-border px-3 py-2 text-xs text-muted-foreground">
                          {source.failureKind === "dispatch" || source.failureKind === "execution" ? (
                            <p>{t(source.failureKind === "dispatch" ? "batch.retryFailureDispatch" : "batch.retryFailureExecution")}</p>
                          ) : null}
                          {source.reason !== "eligible" ? <p>{t(RETRY_REASON_KEY[source.reason])}</p> : null}
                          {sameNodeBlocked ? <p id={reasonId}>{t("batch.retrySameNode")}</p> : null}
                          {linkConflict ? <p>{t("batch.retryConflict")}</p> : null}
                          {linkDeleted ? <p>{t("batch.retryCreatedThenCleaned")}</p> : null}
                          {canReadRole && childBatchId && !linkDeleted && !linkConflict ? (
                            <Button
                              type="button"
                              variant="outline"
                              size="sm"
                              className="h-auto whitespace-normal text-left"
                              onClick={(event) => void openLinkedChild(childBatchId, event.currentTarget)}
                            >
                              {t("batch.retryViewChild", { taskId: task.id })}
                            </Button>
                          ) : null}
                        </div>
                      ) : null}

                      {isExpanded && isDone && (
                        <div className="space-y-2 border-t border-border bg-muted/30 px-3 py-2">
                          {task.lastError && (
                            <p className="text-xs text-destructive whitespace-pre-wrap break-all">
                              {task.lastError}
                            </p>
                          )}
                          {logState?.status === "loading" && (
                            <div className="flex items-center gap-2 py-2 text-xs text-muted-foreground">
                              <Loader2 className="size-3 animate-spin" />
                              {t("batch.loadingLogs")}
                            </div>
                          )}
                          {logState?.status === "error" && (
                            <p className="text-xs text-destructive">{t("batch.logLoadFailed")}</p>
                          )}
                          {logState?.status === "loaded" && logState.logs.length === 0 && (
                            <p className="text-xs text-muted-foreground">{t("batch.noLogOutput")}</p>
                          )}
                          {logState?.status === "loaded" && logState.logs.length > 0 && (
                            <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all font-mono text-xs leading-relaxed">
                              {logState.logs.map((log) => log.message).join("\n")}
                            </pre>
                          )}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            )}
          </DialogBody>
        </DialogContent>
      </Dialog>
      {batchId ? (
        <BatchCommandRetryDialog
          open={confirmOpen}
          onOpenChange={setConfirmOpen}
          rows={reviewRows}
          sourceBatchId={batchId}
          retain={reviewRetain}
          saving={saving || retryPhase === "submitting"}
          error={confirmError}
          onConfirm={() => void runSubmission("new")}
        />
      ) : null}
      {child ? (
        <BatchResultDialogView
          open
          onOpenChange={(next) => {
            if (!next) {
              const target = focusReturnRef.current;
              setChild(null);
              if (target?.isConnected) target.focus();
            }
          }}
          batchId={child.batchId}
          retain={child.retain}
          token={token}
          closeBridgeRef={childCloseRef}
          returnFocusRef={focusReturnRef}
        />
      ) : null}
    </>
  );
}
