import { StrictMode, type PropsWithChildren } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AuthContext, type AuthContextValue } from "@/context/auth-context.shared";
import { ApiError } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type { TaskRecord } from "@/types/domain";
import { useTaskOperations } from "./use-console-task-operations";

const { apiClientMock } = vi.hoisted(() => ({
  apiClientMock: {
    requestTaskManualTriggerCredentialGrant: vi.fn(),
    triggerTask: vi.fn(),
    getTask: vi.fn(),
    getAlerts: vi.fn(),
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

function renderOperations(options?: { token?: string | null; demo?: boolean; strict?: boolean }) {
  const token = options && "token" in options ? options.token ?? null : "token-1";
  const demo = options?.demo ?? false;
  const tasks = [createTask(7)];
  const setTasks = vi.fn();
  const setAlerts = vi.fn();
  const setWarning = vi.fn();
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
    totpEnabled: true,
    isAuthenticated: Boolean(token),
    login: vi.fn(),
    logout: vi.fn(),
    setTotpEnabled: vi.fn(),
    ensureStepUpProof,
    clearStepUpProof,
  };
  const hook = renderHook(() => useTaskOperations({
    token,
    demoModeEnabled: demo,
    nodes: [],
    policies: [],
    tasks,
    setTasks,
    setAlerts,
    setWarning,
    markTasksMutated,
    ensureDemoWriteAllowed,
    handleWriteApiError,
  }), {
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
    apiClientMock.requestTaskManualTriggerCredentialGrant.mockReset();
    apiClientMock.triggerTask.mockReset();
    apiClientMock.getTask.mockReset();
    apiClientMock.getAlerts.mockReset();
    apiClientMock.requestTaskManualTriggerCredentialGrant.mockResolvedValue({ id: 1, status: "active" });
    apiClientMock.triggerTask.mockResolvedValue(undefined);
    apiClientMock.getTask.mockResolvedValue({ ...createTask(7), status: "running", progress: 40 });
    apiClientMock.getAlerts.mockResolvedValue([]);
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
});
