import { type PropsWithChildren } from "react";
import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthContext, type AuthContextValue } from "@/context/auth-context.shared";
import { apiClient } from "@/lib/api/client";
import { beginAuthTransitionBarrier, clearAuthTransitionBarrier } from "@/lib/api/core";
import { StepUpPausedError } from "@/lib/sensitive-step-up";
import { StepUpPrerequisiteError } from "@/lib/step-up-prerequisite";
import type { AlertRecord, IntegrationChannel } from "@/types/domain";
import { useIntegrationAlertOperations } from "./use-console-integration-alert-operations";

function createAlert(taskId: number | null): AlertRecord {
  return {
    id: "alert-1",
    nodeName: "node-1",
    nodeId: 1,
    taskId,
    policyName: "每日备份",
    severity: "critical",
    status: "open",
    errorCode: "E_CONN",
    message: "connection failed",
    triggeredAt: "2026-03-06 10:00:00",
    retryable: true,
  };
}

function createIntegration(id: string, name: string): IntegrationChannel {
  return {
    id,
    type: "webhook",
    name,
    endpoint: "https://hooks.example.test/xirang",
    hasSecret: false,
    enabled: true,
    failThreshold: 1,
    cooldownMinutes: 5,
    proxyUrl: "",
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function renderOperations(options?: {
  token?: string | null;
  totpEnabled?: boolean;
  authTransitioning?: boolean;
  taskId?: number | null;
  integrations?: IntegrationChannel[];
  isCurrentIntegrationOwner?: () => boolean;
  handleWriteApiError?: (action: string, error: unknown) => void;
}) {
  const token = options && "token" in options ? options.token ?? null : "token-1";
  const totpEnabled = options?.totpEnabled ?? true;
  const authTransitioning = options?.authTransitioning ?? false;
  const retryTask = vi.fn().mockResolvedValue(undefined);
  const setWarning = vi.fn();
  const setIntegrations = vi.fn();
  const handleWriteApiError = vi.fn(options?.handleWriteApiError ?? ((_action: string, error: unknown) => {
    throw error;
  }));
  // 默认 operator 不拥有渠道配置写权限，证明告警操作不依赖该判定。
  const isCurrentIntegrationOwner = options?.isCurrentIntegrationOwner ?? (() => false);
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
    ensureStepUpProof: vi.fn(),
    clearStepUpProof: vi.fn(),
  };
  const hook = renderHook(() => useIntegrationAlertOperations({
    token,
    alerts: [createAlert(options?.taskId === undefined ? 9 : options.taskId)],
    integrations: options?.integrations ?? [],
    setAlerts: vi.fn(),
    setIntegrations,
    setWarning,
    ensureDemoWriteAllowed: vi.fn(),
    handleWriteApiError,
    retryTask,
    isCurrentIntegrationOwner,
  }), {
    wrapper: ({ children }: PropsWithChildren) => (
      <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
    ),
  });
  return { ...hook, retryTask, setWarning, setIntegrations, handleWriteApiError };
}

describe("useIntegrationAlertOperations retryAlert", () => {
  afterEach(() => {
    clearAuthTransitionBarrier();
  });

  it("retries the linked task when two-factor authentication is enabled", async () => {
    const { result, retryTask } = renderOperations();
    await act(async () => {
      await result.current.retryAlert("alert-1");
    });
    expect(retryTask).toHaveBeenCalledTimes(1);
    expect(retryTask).toHaveBeenCalledWith(9);
  });

  it("does not retry a task when two-factor authentication is disabled", async () => {
    const { result, retryTask } = renderOperations({ totpEnabled: false });
    await expect(act(async () => {
      await result.current.retryAlert("alert-1");
    })).rejects.toBeInstanceOf(StepUpPrerequisiteError);
    expect(retryTask).not.toHaveBeenCalled();
  });

  it("does not retry a task while account security is updating", async () => {
    beginAuthTransitionBarrier();
    const { result, retryTask } = renderOperations();
    await expect(act(async () => {
      await result.current.retryAlert("alert-1");
    })).rejects.toBeInstanceOf(StepUpPausedError);
    expect(retryTask).not.toHaveBeenCalled();
  });

  it("keeps the demo retry path when there is no token", async () => {
    const { result, retryTask } = renderOperations({ token: null, totpEnabled: false });
    await act(async () => {
      await result.current.retryAlert("alert-1");
    });
    expect(retryTask).toHaveBeenCalledTimes(1);
    expect(retryTask).toHaveBeenCalledWith(9);
  });
});

describe("useIntegrationAlertOperations integration owner", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("keeps operator alert actions while rejecting channel writes for an inactive owner", async () => {
    const ack = vi.spyOn(apiClient, "ackAlert").mockResolvedValue({ ...createAlert(9), status: "acked" });
    const create = vi.spyOn(apiClient, "createIntegration");
    const test = vi.spyOn(apiClient, "testIntegration");
    const { result, setIntegrations } = renderOperations({ integrations: [createIntegration("int-1", "Ops")] });

    await act(async () => {
      await result.current.acknowledgeAlert("alert-1");
    });
    expect(ack).toHaveBeenCalledTimes(1);

    for (const run of [
      () => result.current.addIntegration({ type: "webhook", name: "x", endpoint: "", failThreshold: 1, cooldownMinutes: 5, enabled: true }),
      () => result.current.removeIntegration("int-1"),
      () => result.current.updateIntegration("int-1", { name: "renamed" }),
      () => result.current.patchIntegration("int-1", { name: "renamed" }),
      () => result.current.toggleIntegration("int-1"),
      () => result.current.testIntegration("int-1"),
    ]) {
      await expect(run()).rejects.toMatchObject({ name: "AbortError", message: "Operation belongs to an inactive session" });
    }
    expect(create).not.toHaveBeenCalled();
    expect(test).not.toHaveBeenCalled();
    expect(setIntegrations).not.toHaveBeenCalled();
  });

  it("drops a late channel failure without reporting it through the shared warning handler", async () => {
    let owned = true;
    const pending = deferred<IntegrationChannel>();
    vi.spyOn(apiClient, "createIntegration").mockReturnValue(pending.promise);
    const { result, handleWriteApiError, setIntegrations } = renderOperations({ isCurrentIntegrationOwner: () => owned });

    const save = result.current.addIntegration({ type: "webhook", name: "x", endpoint: "", failThreshold: 1, cooldownMinutes: 5, enabled: true });
    owned = false;
    pending.reject(new Error("late failure"));

    await expect(save).rejects.toMatchObject({ name: "AbortError" });
    expect(handleWriteApiError).not.toHaveBeenCalled();
    expect(setIntegrations).not.toHaveBeenCalled();
  });

  it("rejects late direct update and patch responses and failures", async () => {
    let owned = true;
    const update = deferred<IntegrationChannel>();
    const patch = deferred<IntegrationChannel>();
    vi.spyOn(apiClient, "updateIntegration").mockReturnValue(update.promise);
    vi.spyOn(apiClient, "patchIntegration").mockReturnValue(patch.promise);
    const { result, setIntegrations } = renderOperations({
      integrations: [createIntegration("int-1", "Ops")],
      isCurrentIntegrationOwner: () => owned,
    });

    const updating = result.current.updateIntegration("int-1", { name: "renamed" });
    const patching = result.current.patchIntegration("int-1", { name: "renamed" });
    owned = false;
    update.resolve(createIntegration("int-1", "renamed"));
    patch.reject(new Error("late failure"));

    await expect(updating).rejects.toMatchObject({ name: "AbortError" });
    await expect(patching).rejects.toMatchObject({ name: "AbortError" });
    expect(setIntegrations).not.toHaveBeenCalled();
  });

  it("keeps a channel when deletion fails and the warning handler does not throw", async () => {
    vi.spyOn(apiClient, "deleteIntegration").mockRejectedValue(new Error("delete failed"));
    const { result, handleWriteApiError, setIntegrations } = renderOperations({
      integrations: [createIntegration("int-1", "Ops")],
      isCurrentIntegrationOwner: () => true,
      handleWriteApiError: () => undefined,
    });

    await expect(result.current.removeIntegration("int-1")).rejects.toBeInstanceOf(Error);
    expect(handleWriteApiError).toHaveBeenCalledTimes(1);
    expect(setIntegrations).not.toHaveBeenCalled();
  });

  it("removes a channel only after the current owner's delete succeeds", async () => {
    vi.spyOn(apiClient, "deleteIntegration").mockResolvedValue(undefined);
    const { result, setIntegrations } = renderOperations({
      integrations: [createIntegration("int-1", "Ops")],
      isCurrentIntegrationOwner: () => true,
    });

    await result.current.removeIntegration("int-1");
    expect(setIntegrations).toHaveBeenCalledTimes(1);
    const updater = setIntegrations.mock.calls[0]![0] as (prev: IntegrationChannel[]) => IntegrationChannel[];
    expect(updater([createIntegration("int-1", "Ops"), createIntegration("int-2", "Other")]).map((item) => item.id)).toEqual(["int-2"]);
  });
});
