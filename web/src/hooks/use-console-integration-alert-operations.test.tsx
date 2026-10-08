import { type PropsWithChildren } from "react";
import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthContext, type AuthContextValue } from "@/context/auth-context.shared";
import { beginAuthTransitionBarrier, clearAuthTransitionBarrier } from "@/lib/api/core";
import { StepUpPausedError } from "@/lib/sensitive-step-up";
import { StepUpPrerequisiteError } from "@/lib/step-up-prerequisite";
import type { AlertRecord } from "@/types/domain";
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

function renderOperations(options?: {
  token?: string | null;
  totpEnabled?: boolean;
  authTransitioning?: boolean;
  taskId?: number | null;
}) {
  const token = options && "token" in options ? options.token ?? null : "token-1";
  const totpEnabled = options?.totpEnabled ?? true;
  const authTransitioning = options?.authTransitioning ?? false;
  const retryTask = vi.fn().mockResolvedValue(undefined);
  const setWarning = vi.fn();
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
    integrations: [],
    setAlerts: vi.fn(),
    setIntegrations: vi.fn(),
    setWarning,
    ensureDemoWriteAllowed: vi.fn(),
    handleWriteApiError: vi.fn(),
    retryTask,
  }), {
    wrapper: ({ children }: PropsWithChildren) => (
      <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
    ),
  });
  return { ...hook, retryTask, setWarning };
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
