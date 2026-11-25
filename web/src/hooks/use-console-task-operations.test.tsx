import { StrictMode, type PropsWithChildren } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { AuthContext, type AuthContextValue } from "@/context/auth-context.shared";
import { ApiError, beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier } from "@/lib/api/core";
import { StepUpPausedError } from "@/lib/sensitive-step-up";
import { StepUpPrerequisiteError } from "@/lib/step-up-prerequisite";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type { TaskRecord } from "@/types/domain";
import { useTaskOperations } from "./use-console-task-operations";

const { apiClientMock } = vi.hoisted(() => ({
  apiClientMock: {
    requestTaskManualTriggerCredentialGrant: vi.fn(),
    triggerTask: vi.fn(),
    getTask: vi.fn(),
    getAlerts: vi.fn(),
    getTaskLogs: vi.fn(),
    updateTask: vi.fn(),
  },
}));

vi.mock("@/lib/api/client", () => ({ apiClient: apiClientMock }));

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function stepUpRequired() {
  return new ApiError(403, "需要二次验证", {
    code: 403,
    message: "需要二次验证",
    data: { error_code: "STEP_UP_REQUIRED", proof_ttl_seconds: 300 },
  });
}

function createTask(id: number): TaskRecord {
  return {
    id,
    name: `task-${id}`,
    policyName: "每日备份",
    policyId: 1,
    nodeName: "node-1",
    nodeId: 1,
    status: "failed",
    progress: 0,
    startedAt: "2026-03-06 10:00:00",
    speedMbps: 0,
    enabled: true,
    executorType: "rsync",
  };
}

function renderOperations(options?: { token?: string | null; demo?: boolean; strict?: boolean; totpEnabled?: boolean; authTransitioning?: boolean; tasks?: TaskRecord[] }) {
  const token = options && "token" in options ? options.token ?? null : "token-1";
  const demo = options?.demo ?? false;
  const totpEnabled = options?.totpEnabled ?? true;
  const authTransitioning = options?.authTransitioning ?? false;
  const tasks = options?.tasks ?? [createTask(7)];
  const setTasks = vi.fn();
  const setAlerts = vi.fn();
  const markTasksMutated = vi.fn();
  const ensureDemoWriteAllowed = vi.fn();
  const handleWriteApiError = vi.fn((_action: string, error: unknown) => {
    throw error instanceof Error ? error : new Error("write failed");
  });
  const ensureStepUpProof = vi.fn();
  const clearStepUpProof = vi.fn();
  const value: AuthContextValue = {
    token,
    username: "operator",
    role: "operator",
    userId: 2,
    totpEnabled,
    isAuthenticated: Boolean(token),
    authTransitioning,
    beginTOTPActivation: vi.fn(() => 1),
    abortTOTPActivation: vi.fn(),
    completeTOTPActivation: vi.fn(() => false),
    login: vi.fn(),
    logout: vi.fn(),
    setTotpEnabled: vi.fn(),
    ensureStepUpProof,
    clearStepUpProof,
  };
  const hook = renderHook(({ currentTasks }: { currentTasks: TaskRecord[] }) => useTaskOperations({
    token,
    demoModeEnabled: demo,
    nodes: [],
    policies: [],
    tasks: currentTasks,
    setTasks,
    setAlerts,
    markTasksMutated,
    ensureDemoWriteAllowed,
    handleWriteApiError,
  }), {
    initialProps: { currentTasks: tasks },
    wrapper: ({ children }: PropsWithChildren) => {
      const provider = <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
      return options?.strict ? <StrictMode>{provider}</StrictMode> : provider;
    },
  });
  return {
    ...hook,
    tasks,
    setTasks,
    setAlerts,
    markTasksMutated,
    ensureDemoWriteAllowed,
    ensureStepUpProof,
    clearStepUpProof,
    handleWriteApiError,
  };
}

describe("useTaskOperations trigger lifetime", () => {
  beforeEach(() => {
    clearAuthTransitionBarrier();
    apiClientMock.requestTaskManualTriggerCredentialGrant.mockReset();
    apiClientMock.triggerTask.mockReset();
    apiClientMock.getTask.mockReset();
    apiClientMock.getAlerts.mockReset();
    apiClientMock.getTaskLogs.mockReset();
    apiClientMock.getTaskLogs.mockResolvedValue([]);
    apiClientMock.requestTaskManualTriggerCredentialGrant.mockResolvedValue({ id: 1, status: "active" });
    apiClientMock.triggerTask.mockResolvedValue(undefined);
    apiClientMock.getTask.mockResolvedValue({ ...createTask(7), status: "running", progress: 40 });
    apiClientMock.getAlerts.mockResolvedValue([]);
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
  });

  it("triggers and reads back while the captured predicate stays current", async () => {
    const { result, setTasks, tasks, markTasksMutated, ensureStepUpProof } = renderOperations();
    await act(async () => {
      await result.current.triggerTask(7, () => true);
    });

    expect(apiClientMock.requestTaskManualTriggerCredentialGrant.mock.invocationCallOrder[0])
      .toBeLessThan(apiClientMock.triggerTask.mock.invocationCallOrder[0]);
    expect(apiClientMock.triggerTask.mock.invocationCallOrder[0])
      .toBeLessThan(apiClientMock.getTask.mock.invocationCallOrder[0]);
    expect(apiClientMock.triggerTask).toHaveBeenCalledWith("token-1", 7, undefined);
    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(markTasksMutated).toHaveBeenCalledTimes(1);
    const updated = setTasks.mock.calls[0]?.[0](tasks);
    expect(updated[0]).toMatchObject({ id: 7, status: "running", progress: 40 });
  });

  it("retries step-up for an operator when the grant is still current", async () => {
    const { result, ensureStepUpProof, setTasks } = renderOperations({ strict: true });
    ensureStepUpProof.mockResolvedValue("proof-1");
    apiClientMock.requestTaskManualTriggerCredentialGrant
      .mockRejectedValueOnce(stepUpRequired())
      .mockResolvedValueOnce({ id: 1, status: "active" });

    await act(async () => {
      await result.current.triggerTask(7, () => true);
    });

    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProof).toHaveBeenCalledWith(
      STEP_UP_ACTIONS.taskManualTrigger,
      { persist: false, reuseCached: false },
    );
    expect(apiClientMock.requestTaskManualTriggerCredentialGrant).toHaveBeenNthCalledWith(2, "token-1", {
      taskId: 7,
      reason: "手动触发任务 #7",
      requestedTtlSeconds: 600,
    }, "proof-1");
    expect(apiClientMock.triggerTask).toHaveBeenCalledTimes(1);
    expect(apiClientMock.triggerTask).toHaveBeenCalledWith("token-1", 7, "proof-1");
    expect(setTasks).toHaveBeenCalledTimes(1);
  });

  it("does not enter step-up when the predicate is already false", async () => {
    const { result, ensureStepUpProof, setTasks, markTasksMutated } = renderOperations();
    await act(async () => {
      await result.current.triggerTask(7, () => false);
    });
    expect(apiClientMock.requestTaskManualTriggerCredentialGrant).not.toHaveBeenCalled();
    expect(apiClientMock.triggerTask).not.toHaveBeenCalled();
    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(setTasks).not.toHaveBeenCalled();
    expect(markTasksMutated).not.toHaveBeenCalled();
  });

  it("stops after the grant and before trigger when the attempt goes stale", async () => {
    let current = true;
    apiClientMock.requestTaskManualTriggerCredentialGrant.mockImplementation(async () => {
      current = false;
      return { id: 1, status: "active" };
    });
    const { result, setTasks, ensureStepUpProof } = renderOperations();
    await act(async () => {
      await result.current.triggerTask(7, () => current);
    });
    expect(apiClientMock.triggerTask).not.toHaveBeenCalled();
    expect(apiClientMock.getTask).not.toHaveBeenCalled();
    expect(setTasks).not.toHaveBeenCalled();
    expect(ensureStepUpProof).not.toHaveBeenCalled();
  });

  it("swallows a stale step-up rejection inside the grant callback", async () => {
    const grant = createDeferred<unknown>();
    apiClientMock.requestTaskManualTriggerCredentialGrant.mockReturnValueOnce(grant.promise);
    let current = true;
    const { result, ensureStepUpProof, setTasks, clearStepUpProof } = renderOperations();
    let pending!: Promise<void>;
    act(() => {
      pending = result.current.triggerTask(7, () => current);
    });
    await waitFor(() => expect(apiClientMock.requestTaskManualTriggerCredentialGrant).toHaveBeenCalledTimes(1));
    current = false;
    await act(async () => {
      grant.reject(stepUpRequired());
      await pending;
    });
    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(clearStepUpProof).not.toHaveBeenCalled();
    expect(apiClientMock.triggerTask).not.toHaveBeenCalled();
    expect(apiClientMock.getTask).not.toHaveBeenCalled();
    expect(setTasks).not.toHaveBeenCalled();
  });

  it("skips readback and local state after the trigger when the attempt goes stale", async () => {
    let current = true;
    apiClientMock.triggerTask.mockImplementation(async () => {
      current = false;
    });
    const { result, setTasks, markTasksMutated } = renderOperations();
    await act(async () => {
      await result.current.triggerTask(7, () => current);
    });
    expect(apiClientMock.triggerTask).toHaveBeenCalledTimes(1);
    expect(apiClientMock.getTask).not.toHaveBeenCalled();
    expect(setTasks).not.toHaveBeenCalled();
    expect(markTasksMutated).not.toHaveBeenCalled();
  });

  it("does not apply readback state when the attempt goes stale during getTask", async () => {
    let current = true;
    apiClientMock.getTask.mockImplementation(async () => {
      current = false;
      return { ...createTask(7), status: "running", progress: 40 };
    });
    const { result, setTasks } = renderOperations();
    await act(async () => {
      await result.current.triggerTask(7, () => current);
    });
    expect(apiClientMock.getTask).toHaveBeenCalledTimes(1);
    expect(setTasks).not.toHaveBeenCalled();
  });

  it("keeps the demo trigger path when no predicate is supplied and skips it when stale", async () => {
    const currentDemo = renderOperations({ token: null, demo: true });
    await act(async () => {
      await currentDemo.result.current.triggerTask(7);
    });
    expect(apiClientMock.requestTaskManualTriggerCredentialGrant).not.toHaveBeenCalled();
    expect(currentDemo.ensureDemoWriteAllowed).toHaveBeenCalled();
    expect(currentDemo.markTasksMutated).toHaveBeenCalledTimes(1);
    expect(currentDemo.setTasks).toHaveBeenCalledTimes(1);

    const staleDemo = renderOperations({ token: null, demo: true });
    await act(async () => {
      await staleDemo.result.current.triggerTask(7, () => false);
    });
    expect(staleDemo.ensureDemoWriteAllowed).not.toHaveBeenCalled();
    expect(staleDemo.setTasks).not.toHaveBeenCalled();
  });

  it("refreshes alerts on retry only while the predicate stays current", async () => {
    const current = renderOperations();
    apiClientMock.getAlerts.mockResolvedValue([{ id: "alert-1" }]);
    await act(async () => {
      await current.result.current.retryTask(7, () => true);
    });
    expect(apiClientMock.getAlerts).toHaveBeenCalledWith("token-1");
    expect(current.setAlerts).toHaveBeenCalledWith([{ id: "alert-1" }]);

    let fresh = true;
    apiClientMock.triggerTask.mockImplementation(async () => {
      fresh = false;
    });
    const stale = renderOperations();
    await act(async () => {
      await stale.result.current.retryTask(7, () => fresh);
    });
    expect(apiClientMock.getAlerts).toHaveBeenCalledTimes(1);
    expect(stale.setAlerts).not.toHaveBeenCalled();
  });

  it("does not store alerts fetched after retry becomes stale", async () => {
    let current = true;
    apiClientMock.getAlerts.mockImplementation(async () => {
      current = false;
      return [{ id: "alert-2" }];
    });
    const { result, setAlerts } = renderOperations();
    await act(async () => {
      await result.current.retryTask(7, () => current);
    });
    expect(apiClientMock.getAlerts).toHaveBeenCalledTimes(1);
    expect(setAlerts).not.toHaveBeenCalled();
  });

  it("propagates a current non-step-up trigger failure", async () => {
    apiClientMock.requestTaskManualTriggerCredentialGrant.mockRejectedValue(new Error("grant down"));
    const { result, setTasks, ensureStepUpProof } = renderOperations();
    await expect(act(async () => {
      await result.current.triggerTask(7, () => true);
    })).rejects.toThrow("grant down");
    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(apiClientMock.triggerTask).not.toHaveBeenCalled();
    expect(setTasks).not.toHaveBeenCalled();
  });

  it("does not grant or trigger when two-factor authentication is disabled", async () => {
    const { result } = renderOperations({ totpEnabled: false });
    await expect(result.current.triggerTask(7, () => true)).rejects.toBeInstanceOf(StepUpPrerequisiteError);
    expect(apiClientMock.requestTaskManualTriggerCredentialGrant).not.toHaveBeenCalled();
    expect(apiClientMock.triggerTask).not.toHaveBeenCalled();
  });

  it("does not grant while authentication is transitioning", async () => {
    beginAuthTransitionBarrier();
    const { result } = renderOperations();
    await expect(result.current.triggerTask(7, () => true)).rejects.toBeInstanceOf(StepUpPausedError);
    expect(apiClientMock.requestTaskManualTriggerCredentialGrant).not.toHaveBeenCalled();
    expect(apiClientMock.triggerTask).not.toHaveBeenCalled();
  });

  it("returns api log failures instead of an empty success", async () => {
    const forbidden = new ApiError(403, "denied");
    forbidden.httpStatus = 403;
    apiClientMock.getTaskLogs.mockRejectedValueOnce(forbidden);
    const denied = renderOperations();
    await expect(denied.result.current.fetchTaskLogs(7)).rejects.toBe(forbidden);

    const network = new Error("network down");
    apiClientMock.getTaskLogs.mockRejectedValueOnce(network);
    const failed = renderOperations();
    await expect(failed.result.current.fetchTaskLogs(7)).rejects.toBe(network);
  });

  it("returns an empty log list when the api succeeds with no rows", async () => {
    apiClientMock.getTaskLogs.mockResolvedValueOnce([]);
    const { result } = renderOperations();
    await expect(result.current.fetchTaskLogs(7)).resolves.toEqual([]);
  });

  it("keeps the no-token log path from calling the api", async () => {
    const { result } = renderOperations({ token: null });
    await expect(result.current.fetchTaskLogs(7)).resolves.toEqual([]);
    expect(apiClientMock.getTaskLogs).not.toHaveBeenCalled();
  });
});

describe("useTaskOperations update completion continuity", () => {
  const expectedRevision = "1772791200000000000";
  const loaded: TaskRecord = {
    ...createTask(7),
    lastRunAt: "2026-03-06 10:00:00",
    lastFinishedAt: "2026-03-06 10:05:00",
    lastDurationMs: 300000,
    lastActivityMs: 3000,
  };

  function applyUpdate(setTasks: Mock, previous: TaskRecord): TaskRecord {
    expect(setTasks).toHaveBeenCalledTimes(1);
    const next = setTasks.mock.calls[0]?.[0]([previous, createTask(8)]) as TaskRecord[];
    expect(next[1]).toEqual(createTask(8));
    return next[0];
  }

  beforeEach(() => {
    clearAuthTransitionBarrier();
    apiClientMock.updateTask.mockReset();
    apiClientMock.getTask.mockReset();
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
  });

  it.each([
    { returnedActivity: undefined, expectedActivity: 3000 },
    { returnedActivity: 1000, expectedActivity: 3000 },
    { returnedActivity: 5000, expectedActivity: 5000 },
  ])("keeps the loaded completion pair when a same-node save omits it (activity $returnedActivity)", async ({ returnedActivity, expectedActivity }) => {
    apiClientMock.updateTask.mockResolvedValue({
      ...createTask(7),
      name: "renamed",
      lastActivityMs: returnedActivity,
    });
    const { result, setTasks, markTasksMutated } = renderOperations({ tasks: [loaded] });
    await act(async () => {
      await result.current.updateTask(7, { name: "renamed", expectedRevision });
    });

    expect(apiClientMock.updateTask).toHaveBeenCalledTimes(1);
    expect(apiClientMock.getTask).not.toHaveBeenCalled();
    expect(markTasksMutated).toHaveBeenCalledTimes(1);
    expect(applyUpdate(setTasks, loaded)).toMatchObject({
      name: "renamed",
      nodeId: 1,
      lastFinishedAt: "2026-03-06 10:05:00",
      lastDurationMs: 300000,
      lastActivityMs: expectedActivity,
    });
  });

  it("takes a returned same-node completion pair whole, including a missing duration", async () => {
    apiClientMock.updateTask.mockResolvedValue({
      ...createTask(7),
      lastFinishedAt: "2026-03-06 11:00:00",
      lastDurationMs: undefined,
      lastActivityMs: 7000,
    });
    const { result, setTasks } = renderOperations({ tasks: [loaded] });
    await act(async () => {
      await result.current.updateTask(7, { name: "renamed", expectedRevision });
    });

    const updated = applyUpdate(setTasks, loaded);
    expect(updated.lastFinishedAt).toBe("2026-03-06 11:00:00");
    expect(updated.lastDurationMs).toBeUndefined();
    expect(updated.lastActivityMs).toBe(7000);
  });

  it("clears the old node's completion, then hydrates the destination's from the task read", async () => {
    const putRow: TaskRecord = { ...createTask(7), nodeId: 2, nodeName: "node-2", lastActivityMs: 1000 };
    const read = createDeferred<TaskRecord>();
    apiClientMock.updateTask.mockResolvedValue(putRow);
    apiClientMock.getTask.mockReturnValue(read.promise);
    const { result, setTasks, markTasksMutated } = renderOperations();
    await act(async () => {
      await result.current.updateTask(7, { nodeId: 2, expectedRevision });
    });

    expect(apiClientMock.getTask).toHaveBeenCalledWith("token-1", 7);
    const cleared = applyUpdate(setTasks, loaded);
    expect(cleared).toBe(putRow);
    expect(cleared.lastFinishedAt).toBeUndefined();
    expect(cleared.lastDurationMs).toBeUndefined();

    await act(async () => {
      read.resolve({
        ...putRow,
        lastFinishedAt: "2026-03-05 08:00:00",
        lastDurationMs: 60000,
        lastActivityMs: 2000,
      });
      await read.promise;
    });
    await waitFor(() => expect(setTasks).toHaveBeenCalledTimes(2));
    expect(markTasksMutated).toHaveBeenCalledTimes(1);
    const hydrate = setTasks.mock.calls[1]?.[0] as (prev: TaskRecord[]) => TaskRecord[];
    expect(hydrate([putRow, createTask(8)])).toEqual([
      { ...putRow, lastFinishedAt: "2026-03-05 08:00:00", lastDurationMs: 60000, lastActivityMs: 2000 },
      createTask(8),
    ]);
    const superseded = { ...putRow, name: "edited again" };
    expect(hydrate([superseded])[0]).toBe(superseded);
  });

  it("hydrates the newest row when a same-node save follows a move before its read lands", async () => {
    const moved: TaskRecord = { ...createTask(7), nodeId: 2, nodeName: "node-2" };
    const renamed: TaskRecord = { ...moved, name: "renamed" };
    const firstRead = createDeferred<TaskRecord>();
    const secondRead = createDeferred<TaskRecord>();
    apiClientMock.updateTask.mockResolvedValueOnce(moved).mockResolvedValueOnce(renamed);
    apiClientMock.getTask.mockReturnValueOnce(firstRead.promise).mockReturnValueOnce(secondRead.promise);
    const { result, rerender, setTasks } = renderOperations({ tasks: [loaded] });
    const applyCall = (index: number, prev: TaskRecord[]) =>
      setTasks.mock.calls[index]?.[0](prev) as TaskRecord[];

    await act(async () => {
      await result.current.updateTask(7, { nodeId: 2, expectedRevision });
    });
    const afterMove = applyCall(0, [loaded]);
    expect(afterMove[0]).toBe(moved);
    rerender({ currentTasks: afterMove });

    await act(async () => {
      await result.current.updateTask(7, { name: "renamed", expectedRevision });
    });
    expect(apiClientMock.getTask).toHaveBeenCalledTimes(2);
    const afterRename = applyCall(1, afterMove);
    expect(afterRename[0]).toBe(renamed);

    await act(async () => {
      secondRead.resolve({ ...renamed, lastFinishedAt: "2026-03-05 08:00:00", lastDurationMs: 60000, lastActivityMs: 2000 });
      await secondRead.promise;
    });
    await waitFor(() => expect(setTasks).toHaveBeenCalledTimes(3));
    const hydrated = applyCall(2, afterRename);
    expect(hydrated[0]).toEqual({ ...renamed, lastFinishedAt: "2026-03-05 08:00:00", lastDurationMs: 60000, lastActivityMs: 2000 });

    await act(async () => {
      firstRead.resolve({ ...moved, lastFinishedAt: "2026-03-01 08:00:00", lastDurationMs: 1000, lastActivityMs: 1000 });
      await firstRead.promise;
    });
    await waitFor(() => expect(setTasks).toHaveBeenCalledTimes(4));
    expect(applyCall(3, hydrated)[0]).toBe(hydrated[0]);
    expect(applyCall(3, afterRename)[0]).toBe(renamed);
  });

  it("keeps the cleared row when the follow-up read fails or the session moves on", async () => {
    const putRow: TaskRecord = { ...createTask(7), nodeId: 2, nodeName: "node-2" };
    apiClientMock.updateTask.mockResolvedValue(putRow);

    apiClientMock.getTask.mockRejectedValueOnce(new Error("read down"));
    const failed = renderOperations();
    await act(async () => {
      await failed.result.current.updateTask(7, { nodeId: 2, expectedRevision });
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(failed.setTasks).toHaveBeenCalledTimes(1);

    const read = createDeferred<TaskRecord>();
    apiClientMock.getTask.mockReturnValueOnce(read.promise);
    const stale = renderOperations();
    await act(async () => {
      await stale.result.current.updateTask(7, { nodeId: 2, expectedRevision });
    });
    beginAuthTransitionBarrier();
    await act(async () => {
      read.resolve({ ...putRow, lastFinishedAt: "2026-03-05 08:00:00", lastDurationMs: 60000 });
      await read.promise;
    });
    expect(stale.setTasks).toHaveBeenCalledTimes(1);
  });

  it("uses a completion pair returned with a node change without reading again", async () => {
    apiClientMock.updateTask.mockResolvedValue({
      ...createTask(7),
      nodeId: 2,
      nodeName: "node-2",
      lastFinishedAt: "2026-03-05 08:00:00",
      lastDurationMs: undefined,
      lastActivityMs: 2000,
    });
    const { result, setTasks } = renderOperations();
    await act(async () => {
      await result.current.updateTask(7, { nodeId: 2, expectedRevision });
    });
    expect(apiClientMock.getTask).not.toHaveBeenCalled();
    const updated = applyUpdate(setTasks, loaded);
    expect(updated).toMatchObject({ nodeId: 2, lastFinishedAt: "2026-03-05 08:00:00", lastActivityMs: 2000 });
    expect(updated.lastDurationMs).toBeUndefined();
  });

  it("leaves the row untouched when the save fails", async () => {
    apiClientMock.updateTask.mockRejectedValue(new Error("save down"));
    const { result, setTasks, markTasksMutated } = renderOperations();
    await expect(act(async () => {
      await result.current.updateTask(7, { name: "renamed", expectedRevision });
    })).rejects.toThrow("save down");
    expect(setTasks).not.toHaveBeenCalled();
    expect(markTasksMutated).not.toHaveBeenCalled();
  });

  it("clears demo completion only when the node changes", async () => {
    const sameNode = renderOperations({ token: null, demo: true });
    await act(async () => {
      await sameNode.result.current.updateTask(7, { name: "renamed", nodeId: 1, expectedRevision });
    });
    expect(apiClientMock.updateTask).not.toHaveBeenCalled();
    expect(applyUpdate(sameNode.setTasks, loaded)).toMatchObject({
      name: "renamed",
      lastFinishedAt: "2026-03-06 10:05:00",
      lastDurationMs: 300000,
    });

    const moved = renderOperations({ token: null, demo: true });
    await act(async () => {
      await moved.result.current.updateTask(7, { nodeId: 2, expectedRevision });
    });
    const cleared = applyUpdate(moved.setTasks, loaded);
    expect(cleared.nodeId).toBe(2);
    expect(cleared.lastFinishedAt).toBeUndefined();
    expect(cleared.lastDurationMs).toBeUndefined();
  });

  it.each([
    { label: "session generation bump", retire: () => { bumpAuthSessionGeneration(); } },
    { label: "auth transition", retire: () => { beginAuthTransitionBarrier(); } },
  ])("drops a save that lands after its session retired ($label)", async ({ retire }) => {
    const saved = createDeferred<TaskRecord>();
    apiClientMock.updateTask.mockReturnValueOnce(saved.promise);
    const succeeded = renderOperations({ tasks: [loaded] });
    const pending = succeeded.result.current.updateTask(7, { nodeId: 2, expectedRevision });
    retire();
    saved.resolve({ ...createTask(7), nodeId: 2, nodeName: "node-2" });
    await act(async () => {
      await pending;
    });
    expect(succeeded.setTasks).not.toHaveBeenCalled();
    expect(succeeded.markTasksMutated).not.toHaveBeenCalled();
    expect(apiClientMock.getTask).not.toHaveBeenCalled();
    clearAuthTransitionBarrier();

    const failing = createDeferred<TaskRecord>();
    apiClientMock.updateTask.mockReturnValueOnce(failing.promise);
    const failed = renderOperations({ tasks: [loaded] });
    const rejected = failed.result.current.updateTask(7, { name: "renamed", expectedRevision });
    retire();
    const error = new Error("save down");
    const settled = expect(rejected).rejects.toBe(error);
    failing.reject(error);
    await act(async () => {
      await settled;
    });
    expect(failed.handleWriteApiError).not.toHaveBeenCalled();
    expect(failed.setTasks).not.toHaveBeenCalled();
    expect(failed.markTasksMutated).not.toHaveBeenCalled();
  });

  it("keeps a newer loaded row when an older save lands late", async () => {
    const saved = createDeferred<TaskRecord>();
    apiClientMock.updateTask.mockReturnValueOnce(saved.promise);
    const original: TaskRecord = { ...loaded, revision: "1772791200000000000" };
    const { result, rerender, setTasks, markTasksMutated } = renderOperations({ tasks: [original] });
    const pending = result.current.updateTask(7, { name: "renamed", expectedRevision: "1772791200000000000" });

    // A list read lands first with a later edit: moved, then finished on the new node.
    // The revisions differ only past Number precision.
    const newer: TaskRecord = {
      ...original,
      revision: "1772791200000000002",
      name: "edited elsewhere",
      nodeId: 3,
      nodeName: "node-3",
      lastFinishedAt: "2026-03-07 09:00:00",
      lastDurationMs: 1000,
      lastActivityMs: 9000,
    };
    rerender({ currentTasks: [newer] });
    saved.resolve({ ...createTask(7), name: "renamed", revision: "1772791200000000001" });
    await act(async () => {
      await pending;
    });

    expect(markTasksMutated).not.toHaveBeenCalled();
    expect(apiClientMock.getTask).not.toHaveBeenCalled();
    expect(applyUpdate(setTasks, newer)).toBe(newer);
  });

  it("accepts a successful compare-and-set after the server clock moves backward", async () => {
    const original: TaskRecord = { ...loaded, revision: "1772791200000000001" };
    const saved: TaskRecord = { ...createTask(7), nodeId: 2, revision: "1772791200000000000" };
    apiClientMock.updateTask.mockResolvedValueOnce(saved);
    apiClientMock.getTask.mockResolvedValueOnce(saved);
    const { result, setTasks, markTasksMutated } = renderOperations({ tasks: [original] });

    await act(async () => {
      await result.current.updateTask(7, { nodeId: 2, expectedRevision: "1772791200000000001" });
    });

    expect(applyUpdate(setTasks, original)).toBe(saved);
    expect(markTasksMutated).toHaveBeenCalledTimes(1);
    expect(apiClientMock.getTask).toHaveBeenCalledWith("token-1", 7);
  });

  it("applies a later save over an older list read that landed while it was in flight", async () => {
    const saved = createDeferred<TaskRecord>();
    apiClientMock.updateTask.mockReturnValueOnce(saved.promise);
    const original: TaskRecord = { ...createTask(7), revision: "9999999999999999998" };
    const { result, rerender, setTasks, markTasksMutated } = renderOperations({ tasks: [original] });
    const pending = result.current.updateTask(7, { name: "renamed", expectedRevision: "9999999999999999998" });

    // A new row object from a read older than the save, now carrying a same-node
    // completion. Revisions differ in length, so string order would invert them.
    const refreshed: TaskRecord = {
      ...original,
      revision: "9999999999999999999",
      lastFinishedAt: "2026-03-06 10:05:00",
      lastDurationMs: 300000,
      lastActivityMs: 4000,
    };
    rerender({ currentTasks: [refreshed] });
    const putRow: TaskRecord = { ...createTask(7), name: "renamed", revision: "10000000000000000000" };
    saved.resolve(putRow);
    await act(async () => {
      await pending;
    });

    expect(markTasksMutated).toHaveBeenCalledTimes(1);
    // The rendered row already holds this node's completion, so no read starts.
    expect(apiClientMock.getTask).not.toHaveBeenCalled();
    expect(applyUpdate(setTasks, refreshed)).toEqual({
      ...putRow,
      lastFinishedAt: "2026-03-06 10:05:00",
      lastDurationMs: 300000,
      lastActivityMs: 4000,
    });
  });
});
