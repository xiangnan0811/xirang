import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type {
  BackupRecoveryApi,
  RecoveryAuthorization,
  RecoveryJob,
  RecoveryPlan,
  RecoveryPreflight,
  RecoveryProduct,
} from "@/lib/api/backup-recovery-api";
import { ApiError } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type { AssetRef } from "@/types/domain";

import { useBackupRecovery } from "./use-backup-recovery";

const planId = "1".repeat(32);
const jobId = "2".repeat(32);
const preflightId = "3".repeat(32);
const grantId = "4".repeat(32);
const checkpointId = "5".repeat(32);
const attemptId = "6".repeat(32);
const ref: AssetRef = { recoveryPointId: "7".repeat(32), entryId: "8".repeat(64) };

function available<T>(value: T): RecoveryProduct<T> {
  return { status: "available", value };
}

function plan(overrides: Partial<RecoveryPlan> = {}): RecoveryPlan {
  return {
    id: planId,
    state: "preflight_ready",
    revision: "8",
    repositoryId: "9".repeat(32),
    recoveryPointId: ref.recoveryPointId,
    targetMode: "in_place",
    targetNodeId: 4,
    targetRootId: "root-1",
    conflictPolicy: "exact_mirror",
    securityDecision: "allow_clean",
    estimatedItems: 2,
    estimatedBytes: 4096,
    createdAt: "2026-08-16T01:00:00.000Z",
    updatedAt: "2026-08-16T01:01:00.000Z",
    ...overrides,
  };
}

function preflight(overrides: Partial<RecoveryPreflight> = {}): RecoveryPreflight {
  return {
    planId,
    persisted: true,
    planRevision: "8",
    eligible: true,
    preferred: false,
    reasons: [],
    preflightId,
    targetMode: "in_place",
    conflictPolicy: "exact_mirror",
    impact: {
      createCount: 1, overwriteCount: 1, skipCount: 0, deleteCount: 2,
      estimatedItems: 4, estimatedBytes: 4096,
    },
    security: { decision: "allow_clean", findingCount: 0, overridableCategories: [] },
    observedAt: "2026-08-16T01:01:00.000Z",
    expiresAt: "2026-08-16T01:11:00.000Z",
    ...overrides,
  };
}

function authorization(overrides: Partial<RecoveryAuthorization> = {}): RecoveryAuthorization {
  return {
    receiptId: "a".repeat(32),
    planId,
    grant: {
      id: grantId,
      category: "write",
      expiresAt: "2026-08-16T01:10:00.000Z",
      status: "issued",
    },
    jobId: null,
    operation: "write_authorize",
    category: "write",
    planRevision: "9",
    replay: false,
    ...overrides,
  };
}

function job(overrides: Partial<RecoveryJob> = {}): RecoveryJob {
  return {
    id: jobId,
    planId,
    outcome: "running",
    revision: "11",
    targetMode: "in_place",
    targetNodeId: 4,
    targetRootId: "root-1",
    estimatedItems: 4,
    estimatedBytes: 4096,
    progress: {
      totalItems: 4, completedItems: 2, succeededItems: 1, skippedItems: 1, failedItems: 0, bytesWritten: 2048,
    },
    failureCategory: null,
    deleteCheckpoint: {
      id: checkpointId,
      attemptId,
      expectedPlanRevision: "10",
      status: "awaiting_authorization",
      expiresAt: "2026-08-16T01:20:00.000Z",
    },
    resultSet: null,
    plaintextDeadline: null,
    createdAt: "2026-08-16T01:02:00.000Z",
    updatedAt: "2026-08-16T01:05:00.000Z",
    ...overrides,
  };
}

function mockApi(overrides: Partial<BackupRecoveryApi> = {}): BackupRecoveryApi {
  return {
    createPlan: vi.fn(),
    getPlan: vi.fn().mockResolvedValue(available(plan())),
    preflight: vi.fn(),
    overrideSecurity: vi.fn(),
    authorizeWrite: vi.fn(),
    execute: vi.fn(),
    getJob: vi.fn(),
    authorizeExactMirrorDelete: vi.fn(),
    getJobItems: vi.fn(),
    getJobResults: vi.fn(),
    cancelPlan: vi.fn(),
    cancelJob: vi.fn(),
    retainResults: vi.fn(),
    issueResultDownloadTicket: vi.fn(),
    cleanupResults: vi.fn(),
    ...overrides,
  };
}

function options(api: BackupRecoveryApi) {
  return {
    token: "token",
    role: "admin" as const,
    sessionKey: "session-1",
    api,
    ensureStepUpProof: vi.fn().mockResolvedValueOnce("write-proof").mockResolvedValueOnce("execute-proof"),
    onRouteChange: vi.fn(),
    newIdempotencyKey: (endpoint: string) => `${endpoint}-stable-replay-key`,
    cryptoSource: {
      getRandomValues(bytes: Uint8Array) {
        bytes.fill(7);
        return bytes;
      },
    },
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

describe("useBackupRecovery", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
  });

  it("hands explicit selection through create, preflight, write authority and execute without serializing secrets", async () => {
    const executeReceipt = authorization({
      grant: { id: grantId, category: "write", expiresAt: "2026-08-16T01:10:00.000Z", status: "consumed" },
      jobId,
      operation: "execute",
      category: "execute",
      planRevision: "10",
    });
    const api = mockApi({
      createPlan: vi.fn().mockResolvedValue(available({ planId, state: "draft", replay: false })),
      getPlan: vi.fn().mockResolvedValue(available(plan({ state: "draft", revision: "1" }))),
      preflight: vi.fn().mockResolvedValue(available(preflight())),
      authorizeWrite: vi.fn().mockResolvedValue(available(authorization())),
      execute: vi.fn().mockResolvedValue(available(executeReceipt)),
      getJob: vi.fn().mockResolvedValue(available(job())),
    });
    const hookOptions = options(api);
    const { result } = renderHook(() => useBackupRecovery(hookOptions));

    act(() => result.current.open([ref], {
      repositoryId: plan().repositoryId,
      catalogGenerationId: "b".repeat(32),
    }));
    act(() => result.current.setTarget({
      targetMode: "in_place", targetNodeId: 4, targetRootId: "root-1", conflictPolicy: "exact_mirror",
    }));
    await act(async () => result.current.createPlan());
    await act(async () => result.current.runPreflight());
    await act(async () => result.current.authorizeWrite("approved write"));
    await act(async () => result.current.execute());

    expect(api.createPlan).toHaveBeenCalledWith("token", expect.objectContaining({
      recoveryPointId: ref.recoveryPointId,
      entryIds: [ref.entryId],
    }));
    expect(hookOptions.ensureStepUpProof).toHaveBeenNthCalledWith(1, STEP_UP_ACTIONS.assetRecover, {
      persist: false, reuseCached: false,
    });
    const writeInput = vi.mocked(api.authorizeWrite).mock.calls[0]?.[1];
    const executeInput = vi.mocked(api.execute).mock.calls[0]?.[1];
    expect(writeInput?.grantSecret).toHaveLength(43);
    expect(executeInput?.grantSecret).toBe(writeInput?.grantSecret);
    expect(hookOptions.onRouteChange).toHaveBeenNthCalledWith(1, { planId, jobId: null }, { replace: false });
    expect(hookOptions.onRouteChange).toHaveBeenNthCalledWith(2, { planId, jobId }, { replace: false });
    expect(result.current.state.job?.id).toBe(jobId);
    const serialized = JSON.stringify(result.current.state);
    for (const forbidden of [writeInput?.grantSecret ?? "", "write-proof", "execute-proof", "approved write"]) {
      expect(serialized).not.toContain(forbidden);
    }
  });

  it("reuses the exact write key, proof and secret after network ambiguity and clears them on session replacement", async () => {
    const authorizeWrite = vi.fn()
      .mockRejectedValueOnce(new TypeError("ambiguous network failure"))
      .mockResolvedValueOnce(available(authorization({ replay: true })));
    const api = mockApi({
      createPlan: vi.fn().mockResolvedValue(available({ planId, state: "draft", replay: false })),
      getPlan: vi.fn().mockResolvedValue(available(plan({ state: "draft", revision: "1" }))),
      preflight: vi.fn().mockResolvedValue(available(preflight())),
      authorizeWrite,
    });
    const cryptoSource = { getRandomValues: vi.fn((bytes: Uint8Array) => { bytes.fill(12); return bytes; }) };
    const hookOptions = { ...options(api), cryptoSource };
    const { result, rerender } = renderHook(
      ({ sessionKey }) => useBackupRecovery({ ...hookOptions, sessionKey }),
      { initialProps: { sessionKey: "session-1" } },
    );
    act(() => result.current.open([ref], {
      repositoryId: plan().repositoryId,
      catalogGenerationId: "b".repeat(32),
    }));
    act(() => result.current.setTarget({
      targetMode: "in_place", targetNodeId: 4, targetRootId: "root-1", conflictPolicy: "exact_mirror",
    }));
    await act(async () => result.current.createPlan());
    await act(async () => result.current.runPreflight());

    await act(async () => result.current.authorizeWrite("same reason"));
    expect(result.current.state).toMatchObject({ phase: "impact", error: "unavailable" });
    await act(async () => result.current.authorizeWrite("same reason"));

    const first = authorizeWrite.mock.calls[0]?.[1];
    const replay = authorizeWrite.mock.calls[1]?.[1];
    expect(replay).toMatchObject({
      idempotencyKey: first?.idempotencyKey,
      grantSecret: first?.grantSecret,
      proof: first?.proof,
      reason: "same reason",
    });
    expect(cryptoSource.getRandomValues).toHaveBeenCalledTimes(1);
    expect(hookOptions.ensureStepUpProof).toHaveBeenCalledTimes(1);

    rerender({ sessionKey: "session-2" });
    expect(result.current.state).toMatchObject({ phase: "closed", writeGrant: null, plan: null, job: null });
    await expect(result.current.execute()).rejects.toThrow("write authority unavailable");
    expect(api.execute).not.toHaveBeenCalled();
  });

  it("fences an unresolved authority proof when its owning session is replaced", async () => {
    const proof = deferred<string>();
    const api = mockApi({
      createPlan: vi.fn().mockResolvedValue(available({ planId, state: "draft", replay: false })),
      getPlan: vi.fn().mockResolvedValue(available(plan({ state: "draft", revision: "1" }))),
      preflight: vi.fn().mockResolvedValue(available(preflight())),
      authorizeWrite: vi.fn(),
    });
    const hookOptions = { ...options(api), ensureStepUpProof: vi.fn(() => proof.promise) };
    const { result, rerender } = renderHook(
      ({ sessionKey }) => useBackupRecovery({ ...hookOptions, sessionKey }),
      { initialProps: { sessionKey: "session-1" } },
    );
    act(() => result.current.open([ref], {
      repositoryId: plan().repositoryId,
      catalogGenerationId: "b".repeat(32),
    }));
    act(() => result.current.setTarget({
      targetMode: "in_place", targetNodeId: 4, targetRootId: "root-1", conflictPolicy: "exact_mirror",
    }));
    await act(async () => result.current.createPlan());
    await act(async () => result.current.runPreflight());

    let authorization!: Promise<void>;
    act(() => { authorization = result.current.authorizeWrite("session-owned reason"); });
    expect(hookOptions.ensureStepUpProof).toHaveBeenCalledTimes(1);
    rerender({ sessionKey: "session-2" });
    await act(async () => {
      proof.resolve("stale-proof");
      await authorization;
    });

    expect(api.authorizeWrite).not.toHaveBeenCalled();
    expect(result.current.state).toMatchObject({ phase: "closed", plan: null, writeGrant: null });
    expect(JSON.stringify(result.current.state)).not.toContain("session-owned reason");
    expect(JSON.stringify(result.current.state)).not.toContain("stale-proof");
  });

  it("uses an independent confirmation, reason, proof, key and one-shot secret at the exact delete checkpoint", async () => {
    const api = mockApi({
      createPlan: vi.fn().mockResolvedValue(available({ planId, state: "draft", replay: false })),
      getPlan: vi.fn().mockResolvedValue(available(plan({ state: "draft", revision: "1" }))),
      preflight: vi.fn().mockResolvedValue(available(preflight())),
      authorizeWrite: vi.fn().mockResolvedValue(available(authorization())),
      execute: vi.fn().mockResolvedValue(available(authorization({
        grant: { id: grantId, category: "write", expiresAt: "2026-08-16T01:10:00.000Z", status: "consumed" },
        jobId, operation: "execute", category: "execute", planRevision: "10",
      }))),
      getJob: vi.fn().mockResolvedValue(available(job())),
      authorizeExactMirrorDelete: vi.fn()
        .mockRejectedValueOnce(new ApiError(503, "lost delete response"))
        .mockResolvedValueOnce(available(authorization({
          grant: {
            id: "c".repeat(32), category: "exact_mirror_delete",
            expiresAt: "2026-08-16T01:19:00.000Z", status: "issued",
          },
          jobId,
          operation: "exact_mirror_delete_authorize",
          category: "exact_mirror_delete",
          planRevision: "10",
          replay: true,
        }))),
    });
    const hookOptions = {
      ...options(api),
      ensureStepUpProof: vi.fn().mockResolvedValue("fresh-purpose-proof"),
    };
    const { result } = renderHook(() => useBackupRecovery(hookOptions));
    act(() => result.current.open([ref], {
      repositoryId: plan().repositoryId,
      catalogGenerationId: "b".repeat(32),
    }));
    act(() => result.current.setTarget({
      targetMode: "in_place", targetNodeId: 4, targetRootId: "root-1", conflictPolicy: "exact_mirror",
    }));
    await act(async () => result.current.createPlan());
    await act(async () => result.current.runPreflight());
    await act(async () => result.current.authorizeWrite("write reason"));
    await act(async () => result.current.execute());
    await act(async () => result.current.authorizeExactMirrorDelete("delete reason", true));
    expect(result.current.state).toMatchObject({ phase: "delete_authorization", error: "unavailable" });
    await act(async () => result.current.authorizeExactMirrorDelete("delete reason", true));

    expect(api.authorizeExactMirrorDelete).toHaveBeenLastCalledWith("token", expect.objectContaining({
      jobId,
      planId,
      checkpointId,
      attemptId,
      expectedRevision: "10",
      reason: "delete reason",
      proof: "fresh-purpose-proof",
      idempotencyKey: "delete-stable-replay-key",
      grantSecret: expect.stringMatching(/^[A-Za-z0-9_-]{43}$/),
    }));
    const firstDelete = vi.mocked(api.authorizeExactMirrorDelete).mock.calls[0]?.[1];
    const replayDelete = vi.mocked(api.authorizeExactMirrorDelete).mock.calls[1]?.[1];
    expect(replayDelete).toMatchObject({
      idempotencyKey: firstDelete?.idempotencyKey,
      proof: firstDelete?.proof,
      grantSecret: firstDelete?.grantSecret,
    });
    expect(result.current.state.phase).toBe("progress");
    expect(JSON.stringify(result.current.state)).not.toContain("delete reason");
    expect(JSON.stringify(result.current.state)).not.toContain("fresh-purpose-proof");
  });

  it("reconciles an opaque reload handle, pauses polling while hidden and resumes on visibility", async () => {
    vi.useFakeTimers();
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    const getJob = vi.fn().mockResolvedValue(available(job({ deleteCheckpoint: null })));
    const api = mockApi({ getJob });
    const hookOptions = { ...options(api), jobId, planId, pollIntervalMs: 1_000 };
    const { result } = renderHook(() => useBackupRecovery(hookOptions));

    await act(async () => { await Promise.resolve(); });
    expect(getJob).not.toHaveBeenCalled();

    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(getJob).toHaveBeenCalledTimes(1);
    expect(result.current.state.job?.id).toBe(jobId);

    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(getJob).toHaveBeenCalledTimes(1);
  });

  it("keeps live job reconciliation scheduled while loading a bounded item page", async () => {
    vi.useFakeTimers();
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    const getJob = vi.fn().mockResolvedValue(available(job({ deleteCheckpoint: null })));
    const getJobItems = vi.fn().mockResolvedValue(available({
      jobId, page: 1, pageSize: 25, total: 1,
      items: [{
        id: "d".repeat(32), ordinal: 0, operation: "create" as const, outcome: "pending" as const,
        estimatedBytes: 10, bytesWritten: 0, verifiedSize: 0, failureCategory: null,
        createdAt: "2026-08-16T01:02:00.000Z", updatedAt: "2026-08-16T01:02:00.000Z",
      }],
    }));
    const api = mockApi({ getJob, getJobItems });
    const hookOptions = { ...options(api), planId, jobId, pollIntervalMs: 1_000 };
    const { result } = renderHook(() => useBackupRecovery(hookOptions));

    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(getJob).toHaveBeenCalledTimes(1);
    await act(async () => result.current.loadJobItems(1));
    act(() => vi.advanceTimersByTime(1_000));
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });

    expect(getJobItems).toHaveBeenCalledTimes(1);
    expect(getJob).toHaveBeenCalledTimes(2);
    expect(result.current.state.itemPage?.items).toHaveLength(1);
  });

  it("hydrates an exact durable plan handle on recovery-page reload", async () => {
    const api = mockApi({ getPlan: vi.fn().mockResolvedValue(available(plan())) });
    const hookOptions = { ...options(api), planId, jobId: undefined };
    const { result } = renderHook(() => useBackupRecovery(hookOptions));

    await waitFor(() => expect(result.current.state.plan?.id).toBe(planId));
    expect(result.current.state.phase).toBe("target");
    expect(api.getPlan).toHaveBeenCalledWith("token", planId, expect.any(AbortSignal));
  });

  it("resets before hydrating a replacement recovery-page plan handle", async () => {
    const replacementPlanId = "a".repeat(32);
    const api = mockApi({
      getPlan: vi.fn().mockImplementation((_token, requestedPlanId) => Promise.resolve(available(
        plan({ id: requestedPlanId }),
      ))),
    });
    let routePlanId = planId;
    const hookOptions = options(api);
    const { result, rerender } = renderHook(() => useBackupRecovery({
      ...hookOptions,
      planId: routePlanId,
      jobId: undefined,
    }));

    await waitFor(() => expect(result.current.state.plan?.id).toBe(planId));

    routePlanId = replacementPlanId;
    rerender();

    await waitFor(() => expect(result.current.state.plan?.id).toBe(replacementPlanId));
    expect(api.getPlan).toHaveBeenLastCalledWith("token", replacementPlanId, expect.any(AbortSignal));
  });

  it("replaces bounded server pages and clears the download ticket after one-shot handoff", async () => {
    const resultSet = {
      id: "c".repeat(32),
      lifecycle: "ready" as const,
      plaintextDeadline: "2026-08-17T01:00:00.000Z",
      hardDeadline: "2026-08-18T01:00:00.000Z",
      createdAt: "2026-08-16T01:00:00.000Z",
      updatedAt: "2026-08-16T01:10:00.000Z",
    };
    const terminalJob = job({
      outcome: "succeeded",
      targetMode: "isolated",
      deleteCheckpoint: null,
      resultSet,
      plaintextDeadline: resultSet.plaintextDeadline,
      progress: {
        totalItems: 4, completedItems: 4, succeededItems: 3, skippedItems: 1, failedItems: 0, bytesWritten: 4096,
      },
    });
    const itemPage = {
      jobId, page: 2, pageSize: 25, total: 26,
      items: [{
        id: "d".repeat(32), ordinal: 25, operation: "skip" as const, outcome: "skipped" as const,
        estimatedBytes: 0, bytesWritten: 0, verifiedSize: 0, failureCategory: null,
        createdAt: "2026-08-16T01:00:00.000Z", updatedAt: "2026-08-16T01:01:00.000Z",
      }],
    };
    const resultPage = {
      jobId,
      resultSet,
      page: 1,
      pageSize: 25,
      total: 1,
      items: [{
        id: "e".repeat(32), kind: "regular_file" as const, size: 4096,
        modifiedAt: null, createdAt: "2026-08-16T01:10:00.000Z",
      }],
    };
    const ticket = {
      contentUrl: `/api/v1/asset-content/${"f".repeat(32)}`,
      contentType: "application/octet-stream",
      contentLength: 4096,
      etag: '"safe"',
      lastModified: null,
      range: "single" as const,
      classification: "non_secret" as const,
      expiresAt: "2026-08-16T01:20:00.000Z",
      idleExpiresAt: "2026-08-16T01:15:00.000Z",
    };
    const api = mockApi({
      getJob: vi.fn().mockResolvedValue(available(terminalJob)),
      getJobItems: vi.fn().mockResolvedValue(available(itemPage)),
      getJobResults: vi.fn().mockResolvedValue(available(resultPage)),
      retainResults: vi.fn().mockResolvedValue(available({
        resultSetId: resultSet.id, jobId, jobRevision: "12",
        plaintextDeadline: "2026-08-17T12:00:00.000Z", hardDeadline: resultSet.hardDeadline,
      })),
      issueResultDownloadTicket: vi.fn().mockResolvedValue(available(ticket)),
      cleanupResults: vi.fn().mockResolvedValue(available({
        jobId, resultSetId: resultSet.id, lifecycle: "revoking", scheduledAt: "2026-08-16T01:12:00.000Z",
      })),
    });
    const hookOptions = {
      ...options(api), planId, jobId,
      ensureStepUpProof: vi.fn().mockResolvedValueOnce("retain-proof").mockResolvedValueOnce("download-proof"),
      onDownloadTicket: vi.fn(),
    };
    const { result } = renderHook(() => useBackupRecovery(hookOptions));
    await waitFor(() => expect(result.current.state.job?.id).toBe(jobId));

    await act(async () => result.current.loadJobItems(2));
    await act(async () => result.current.loadJobResults(1));
    await act(async () => result.current.retainResults("2026-08-17T12:00:00Z"));
    await act(async () => result.current.downloadResult(resultPage.items[0]!.id));
    expect(result.current.state.resultPage?.items).toHaveLength(1);
    await act(async () => result.current.cleanupResults());

    expect(api.getJobItems).toHaveBeenCalledWith("token", expect.objectContaining({ jobId, page: 2, pageSize: 25 }));
    expect(api.getJobResults).toHaveBeenCalledWith("token", expect.objectContaining({ jobId, page: 1, pageSize: 25 }));
    expect(api.retainResults).toHaveBeenCalledWith("token", expect.objectContaining({
      proof: "retain-proof", expectedRevision: "11",
    }));
    expect(api.issueResultDownloadTicket).toHaveBeenCalledWith("token", expect.objectContaining({
      resultId: resultPage.items[0]!.id, proof: "download-proof",
    }));
    expect(hookOptions.onDownloadTicket).toHaveBeenCalledWith(ticket);
    expect(result.current.state.itemPage?.items).toHaveLength(1);
    expect(result.current.state.resultPage).toBeNull();
    expect(result.current.state.job).toMatchObject({
      outcome: "succeeded",
      revision: "12",
      resultSet: { lifecycle: "revoking" },
    });
    expect(result.current.state.ticket).toBeNull();
  });

  it("replays ambiguous create with the same endpoint key instead of creating a second intent", async () => {
    const createPlan = vi.fn()
      .mockRejectedValueOnce(new TypeError("lost create response"))
      .mockResolvedValueOnce(available({ planId, state: "draft", replay: true }));
    const api = mockApi({
      createPlan,
      getPlan: vi.fn().mockResolvedValue(available(plan({ state: "draft", revision: "1" }))),
    });
    let keySequence = 0;
    const hookOptions = {
      ...options(api),
      newIdempotencyKey: vi.fn((endpoint: string) => `${endpoint}-${++keySequence}-stable-replay-key`),
    };
    const { result } = renderHook(() => useBackupRecovery(hookOptions));
    act(() => result.current.open([ref], {
      repositoryId: plan().repositoryId,
      catalogGenerationId: "b".repeat(32),
    }));
    act(() => result.current.setTarget({
      targetMode: "in_place", targetNodeId: 4, targetRootId: "root-1", conflictPolicy: "exact_mirror",
    }));

    await act(async () => result.current.createPlan());
    expect(result.current.state).toMatchObject({ phase: "target", error: "unavailable" });
    await act(async () => result.current.createPlan());

    expect(createPlan.mock.calls[1]?.[1].idempotencyKey).toBe(createPlan.mock.calls[0]?.[1].idempotencyKey);
    expect(hookOptions.newIdempotencyKey).toHaveBeenCalledTimes(1);
    expect(result.current.state.plan?.id).toBe(planId);
  });

  it("replays ambiguous execute with the exact key, proof and secret and routes the durable job before reconciliation", async () => {
    const execute = vi.fn()
      .mockRejectedValueOnce(new ApiError(503, "ambiguous execute"))
      .mockResolvedValueOnce(available(authorization({
        grant: { id: grantId, category: "write", expiresAt: "2026-08-16T01:10:00.000Z", status: "consumed" },
        jobId,
        operation: "execute",
        category: "execute",
        planRevision: "10",
        replay: true,
      })));
    const api = mockApi({
      createPlan: vi.fn().mockResolvedValue(available({ planId, state: "draft", replay: false })),
      getPlan: vi.fn().mockResolvedValue(available(plan({ state: "draft", revision: "1" }))),
      preflight: vi.fn().mockResolvedValue(available(preflight())),
      authorizeWrite: vi.fn().mockResolvedValue(available(authorization())),
      execute,
      getJob: vi.fn().mockRejectedValue(new ApiError(503, "job read unavailable")),
    });
    let keySequence = 0;
    const hookOptions = {
      ...options(api),
      ensureStepUpProof: vi.fn()
        .mockResolvedValueOnce("write-proof")
        .mockResolvedValueOnce("execute-proof")
        .mockResolvedValueOnce("must-not-be-used"),
      newIdempotencyKey: vi.fn((endpoint: string) => `${endpoint}-${++keySequence}-stable-replay-key`),
    };
    const { result } = renderHook(() => useBackupRecovery(hookOptions));
    act(() => result.current.open([ref], {
      repositoryId: plan().repositoryId,
      catalogGenerationId: "b".repeat(32),
    }));
    act(() => result.current.setTarget({
      targetMode: "in_place", targetNodeId: 4, targetRootId: "root-1", conflictPolicy: "exact_mirror",
    }));
    await act(async () => result.current.createPlan());
    await act(async () => result.current.runPreflight());
    await act(async () => result.current.authorizeWrite("write reason"));

    await act(async () => result.current.execute());
    expect(result.current.state).toMatchObject({ phase: "impact", error: "unavailable" });
    await act(async () => result.current.execute());

    const first = execute.mock.calls[0]?.[1];
    const replay = execute.mock.calls[1]?.[1];
    expect(replay).toMatchObject({
      idempotencyKey: first?.idempotencyKey,
      proof: first?.proof,
      grantSecret: first?.grantSecret,
    });
    expect(hookOptions.ensureStepUpProof).toHaveBeenCalledTimes(2);
    expect(hookOptions.onRouteChange).toHaveBeenLastCalledWith({ planId, jobId }, { replace: false });
    expect(result.current.state.phase).toBe("unavailable");
  });

  it("allows only an explicitly confirmed overridable security finding and keeps its authority separate", async () => {
    const blocked = preflight({
      eligible: false,
      reasons: ["security_blocked"],
      security: { decision: "block", findingCount: 1, overridableCategories: ["suspicious"] },
    });
    const api = mockApi({
      createPlan: vi.fn().mockResolvedValue(available({ planId, state: "draft", replay: false })),
      getPlan: vi.fn().mockResolvedValue(available(plan({ state: "draft", revision: "1", securityDecision: "block" }))),
      preflight: vi.fn().mockResolvedValue(available(blocked)),
      overrideSecurity: vi.fn()
        .mockRejectedValueOnce(new ApiError(503, "lost override response"))
        .mockResolvedValueOnce(available(authorization({
          grant: null,
          operation: "security_override",
          category: "security_override",
          planRevision: "9",
          replay: true,
        }))),
    });
    const hookOptions = {
      ...options(api),
      ensureStepUpProof: vi.fn().mockResolvedValue("override-proof"),
    };
    const { result } = renderHook(() => useBackupRecovery(hookOptions));
    act(() => result.current.open([ref], {
      repositoryId: plan().repositoryId,
      catalogGenerationId: "b".repeat(32),
    }));
    act(() => result.current.setTarget({
      targetMode: "in_place", targetNodeId: 4, targetRootId: "root-1", conflictPolicy: "exact_mirror",
    }));
    await act(async () => result.current.createPlan());
    await act(async () => result.current.runPreflight());

    await expect(result.current.overrideSecurity("suspicious", "override reason", false)).rejects.toThrow(
      "security override confirmation required",
    );
    expect(api.overrideSecurity).not.toHaveBeenCalled();
    await act(async () => result.current.overrideSecurity("suspicious", "override reason", true));
    expect(result.current.state).toMatchObject({ phase: "security", error: "unavailable" });
    await act(async () => result.current.overrideSecurity("suspicious", "override reason", true));

    expect(api.overrideSecurity).toHaveBeenCalledWith("token", expect.objectContaining({
      findingCategory: "suspicious",
      reason: "override reason",
      proof: "override-proof",
      idempotencyKey: "override-stable-replay-key",
    }));
    const firstOverride = vi.mocked(api.overrideSecurity).mock.calls[0]?.[1];
    const replayOverride = vi.mocked(api.overrideSecurity).mock.calls[1]?.[1];
    expect(replayOverride).toMatchObject({
      idempotencyKey: firstOverride?.idempotencyKey,
      proof: firstOverride?.proof,
    });
    expect(hookOptions.ensureStepUpProof).toHaveBeenCalledTimes(1);
    expect(result.current.state).toMatchObject({
      phase: "impact",
      plan: { revision: "9", securityDecision: "admin_override" },
      preflight: { eligible: true, reasons: [], security: { decision: "admin_override" } },
    });
    expect(JSON.stringify(result.current.state)).not.toContain("override reason");
    expect(JSON.stringify(result.current.state)).not.toContain("override-proof");
  });

  it("aborts stale route reads and clears all context-owned state when the recovery context changes", async () => {
    const firstJobId = "d".repeat(32);
    const secondJobId = "e".repeat(32);
    const first = deferred<RecoveryProduct<RecoveryJob>>();
    const second = deferred<RecoveryProduct<RecoveryJob>>();
    const signals: AbortSignal[] = [];
    const getJob = vi.fn((_: string, requestedJobId: string, signal?: AbortSignal) => {
      if (signal) signals.push(signal);
      return requestedJobId === firstJobId ? first.promise : second.promise;
    });
    const api = mockApi({ getJob });
    const hookOptions = options(api);
    const { result, rerender } = renderHook(
      ({ routeJobId, contextKey }) => useBackupRecovery({
        ...hookOptions,
        planId,
        jobId: routeJobId,
        contextKey,
      }),
      { initialProps: { routeJobId: firstJobId as string | undefined, contextKey: "source-context-1" } },
    );
    await waitFor(() => expect(getJob).toHaveBeenCalledTimes(1));

    rerender({ routeJobId: secondJobId, contextKey: "source-context-1" });
    await waitFor(() => expect(getJob).toHaveBeenCalledTimes(2));
    expect(signals[0]?.aborted).toBe(true);
    await act(async () => {
      second.resolve(available(job({ id: secondJobId, deleteCheckpoint: null })));
      await Promise.resolve();
    });
    expect(result.current.state.job?.id).toBe(secondJobId);
    await act(async () => {
      first.resolve(available(job({ id: firstJobId, deleteCheckpoint: null })));
      await Promise.resolve();
    });
    expect(result.current.state.job?.id).toBe(secondJobId);

    rerender({ routeJobId: undefined, contextKey: "source-context-2" });
    expect(result.current.state).toMatchObject({
      phase: "closed", selection: [], plan: null, job: null, itemPage: null, resultPage: null, ticket: null,
    });
  });

  it("reconciles an ambiguous cancel by reading the exact handle without duplicating the mutation", async () => {
    const getJob = vi.fn()
      .mockResolvedValueOnce(available(job({ deleteCheckpoint: null })))
      .mockResolvedValueOnce(available(job({
        outcome: "canceled",
        deleteCheckpoint: null,
        progress: {
          totalItems: 4, completedItems: 2, succeededItems: 1, skippedItems: 1, failedItems: 0, bytesWritten: 2048,
        },
      })));
    const cancelJob = vi.fn().mockRejectedValueOnce(new TypeError("lost cancel response"));
    const api = mockApi({ getJob, cancelJob });
    const hookOptions = { ...options(api), planId, jobId };
    const { result } = renderHook(() => useBackupRecovery(hookOptions));
    await waitFor(() => expect(result.current.state.job?.id).toBe(jobId));

    await act(async () => result.current.cancelRecovery());

    expect(cancelJob).toHaveBeenCalledTimes(1);
    expect(cancelJob).toHaveBeenCalledWith("token", expect.objectContaining({ jobId, expectedRevision: "11" }));
    expect(getJob).toHaveBeenCalledTimes(2);
    expect(result.current.state).toMatchObject({ phase: "verification", job: { outcome: "canceled" } });
  });

  it("reconciles ambiguous retain and cleanup receipts from the exact job without duplicating either mutation", async () => {
    const readySet = {
      id: "c".repeat(32), lifecycle: "ready" as const,
      plaintextDeadline: "2026-08-17T01:00:00.000Z", hardDeadline: "2026-08-18T01:00:00.000Z",
      createdAt: "2026-08-16T01:00:00.000Z", updatedAt: "2026-08-16T01:10:00.000Z",
    };
    const initialJob = job({
      outcome: "succeeded", targetMode: "isolated", deleteCheckpoint: null, resultSet: readySet,
      plaintextDeadline: readySet.plaintextDeadline,
    });
    const retainedJob = job({
      ...initialJob,
      revision: "12",
      plaintextDeadline: "2026-08-17T12:00:00.000Z",
      resultSet: { ...readySet, plaintextDeadline: "2026-08-17T12:00:00.000Z" },
    });
    const revokingJob = job({
      ...retainedJob,
      revision: "13",
      resultSet: { ...retainedJob.resultSet!, lifecycle: "revoking" },
    });
    const getJob = vi.fn()
      .mockResolvedValueOnce(available(initialJob))
      .mockResolvedValueOnce(available(retainedJob))
      .mockResolvedValueOnce(available(revokingJob));
    const retainResults = vi.fn().mockRejectedValueOnce(new ApiError(503, "lost retain response"));
    const cleanupResults = vi.fn().mockRejectedValueOnce(new TypeError("lost cleanup response"));
    const api = mockApi({ getJob, retainResults, cleanupResults });
    const hookOptions = {
      ...options(api), planId, jobId,
      ensureStepUpProof: vi.fn().mockResolvedValue("retain-proof"),
    };
    const { result } = renderHook(() => useBackupRecovery(hookOptions));
    await waitFor(() => expect(result.current.state.job?.id).toBe(jobId));

    await act(async () => result.current.retainResults("2026-08-17T12:00:00Z"));
    expect(retainResults).toHaveBeenCalledTimes(1);
    expect(result.current.state.job).toMatchObject({ revision: "12", resultSet: { lifecycle: "ready" } });

    await act(async () => result.current.cleanupResults());
    expect(cleanupResults).toHaveBeenCalledTimes(1);
    expect(getJob).toHaveBeenCalledTimes(3);
    expect(result.current.state.job).toMatchObject({ revision: "13", resultSet: { lifecycle: "revoking" } });
    expect(result.current.state.resultPage).toBeNull();
  });

  it("reconciles an ambiguous preflight against the exact plan before allowing an explicit same-revision retry", async () => {
    const draft = plan({ state: "draft", revision: "1" });
    const getPlan = vi.fn().mockResolvedValueOnce(available(draft)).mockResolvedValueOnce(available(draft));
    const runPreflight = vi.fn()
      .mockRejectedValueOnce(new ApiError(503, "lost preflight response"))
      .mockResolvedValueOnce(available(preflight({ planRevision: "2" })));
    const api = mockApi({
      createPlan: vi.fn().mockResolvedValue(available({ planId, state: "draft", replay: false })),
      getPlan,
      preflight: runPreflight,
    });
    const hookOptions = options(api);
    const { result } = renderHook(() => useBackupRecovery(hookOptions));
    act(() => result.current.open([ref], {
      repositoryId: plan().repositoryId,
      catalogGenerationId: "b".repeat(32),
    }));
    act(() => result.current.setTarget({
      targetMode: "in_place", targetNodeId: 4, targetRootId: "root-1", conflictPolicy: "exact_mirror",
    }));
    await act(async () => result.current.createPlan());

    await act(async () => result.current.runPreflight());
    expect(runPreflight).toHaveBeenCalledTimes(1);
    expect(getPlan).toHaveBeenCalledTimes(2);
    expect(result.current.state).toMatchObject({ phase: "target", plan: { state: "draft", revision: "1" } });

    await act(async () => result.current.runPreflight());
    expect(runPreflight).toHaveBeenCalledTimes(2);
    expect(runPreflight.mock.calls[1]?.[1]).toMatchObject({ planId, expectedRevision: "1" });
    expect(result.current.state.phase).toBe("impact");
  });

  it("aborts the active read and removes its poll timer on unmount", async () => {
    vi.useFakeTimers();
    const signals: AbortSignal[] = [];
    const getJob = vi.fn((_: string, __: string, signal?: AbortSignal) => {
      if (signal) signals.push(signal);
      return Promise.resolve(available(job({ deleteCheckpoint: null })));
    });
    const api = mockApi({ getJob });
    const hookOptions = { ...options(api), planId, jobId, pollIntervalMs: 1_000 };
    const { unmount } = renderHook(() => useBackupRecovery(hookOptions));
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(getJob).toHaveBeenCalledTimes(1);

    unmount();
    expect(signals[0]?.aborted).toBe(true);
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(getJob).toHaveBeenCalledTimes(1);
  });
});
