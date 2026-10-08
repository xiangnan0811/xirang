import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PropsWithChildren } from "react";
import { AuthContext, type AuthContextValue } from "@/context/auth-context.shared";
import {
  ApiError,
  AuthTransitionRejectedError,
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
} from "@/lib/api/core";
import { StepUpPausedError } from "@/lib/sensitive-step-up";
import { StepUpPrerequisiteError } from "@/lib/step-up-prerequisite";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { useStepUpAction } from "./use-step-up-action";

function createStepUpRequiredError() {
  return new ApiError(403, "需要二次验证", {
    code: 403,
    message: "需要二次验证",
    data: { error_code: "STEP_UP_REQUIRED", proof_ttl_seconds: 300 },
  });
}

const session = {
  totpEnabled: true,
  authTransitioning: false,
};

describe("useStepUpAction", () => {
  const ensureStepUpProof = vi.fn();
  const clearStepUpProof = vi.fn();

  function wrapper({ children }: PropsWithChildren) {
    const value: AuthContextValue = {
      token: "token-1",
      username: "admin",
      role: "admin",
      userId: 1,
      totpEnabled: session.totpEnabled,
      isAuthenticated: true,
      authTransitioning: session.authTransitioning,
      beginTOTPActivation: vi.fn(() => 1),
      abortTOTPActivation: vi.fn(),
      completeTOTPActivation: vi.fn(() => false),
      login: vi.fn(),
      logout: vi.fn(),
      setTotpEnabled: vi.fn(),
      ensureStepUpProof,
      clearStepUpProof,
    };
    return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
  }

  beforeEach(() => {
    session.totpEnabled = true;
    session.authTransitioning = false;
    ensureStepUpProof.mockReset();
    clearStepUpProof.mockReset();
    clearAuthTransitionBarrier();
  });

  afterEach(() => {
    clearAuthTransitionBarrier();
  });

  it("收到 STEP_UP_REQUIRED 后会请求 proof 并重试原动作", async () => {
    ensureStepUpProof.mockResolvedValueOnce("proof-1");
    const action = vi.fn()
      .mockRejectedValueOnce(createStepUpRequiredError())
      .mockResolvedValueOnce("ok");

    const { result } = renderHook(() => useStepUpAction(STEP_UP_ACTIONS.taskManualTrigger), { wrapper });
    await expect(result.current(action)).resolves.toBe("ok");

    expect(action).toHaveBeenNthCalledWith(1);
    expect(clearStepUpProof).not.toHaveBeenCalled();
    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.taskManualTrigger);
    expect(action).toHaveBeenNthCalledWith(2, "proof-1");
  });

  it("重试仍要求 step-up 时会再次清理 proof 并抛出错误", async () => {
    ensureStepUpProof.mockResolvedValueOnce("proof-2");
    const action = vi.fn()
      .mockRejectedValueOnce(createStepUpRequiredError())
      .mockRejectedValueOnce(createStepUpRequiredError());

    const { result } = renderHook(() => useStepUpAction(STEP_UP_ACTIONS.taskManualTrigger), { wrapper });
    await expect(result.current(action)).rejects.toBeInstanceOf(ApiError);

    expect(clearStepUpProof).toHaveBeenCalledTimes(1);
    expect(clearStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.taskManualTrigger);
    expect(action).toHaveBeenNthCalledWith(2, "proof-2");
  });

  it("支持为一次性操作请求非持久化 proof", async () => {
    ensureStepUpProof.mockResolvedValueOnce("proof-one-shot");
    const action = vi.fn()
      .mockRejectedValueOnce(createStepUpRequiredError())
      .mockResolvedValueOnce("ok");

    const { result } = renderHook(() => useStepUpAction(STEP_UP_ACTIONS.batchCommandCreate, { persist: false, reuseCached: false }), { wrapper });
    await expect(result.current(action)).resolves.toBe("ok");

    expect(ensureStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.batchCommandCreate, { persist: false, reuseCached: false });
    expect(action).toHaveBeenNthCalledWith(2, "proof-one-shot");
  });

  it("does not call the action when two-factor authentication is disabled", async () => {
    session.totpEnabled = false;
    const action = vi.fn();
    const { result } = renderHook(() => useStepUpAction(STEP_UP_ACTIONS.taskManualTrigger), { wrapper });

    await expect(result.current(action)).rejects.toBeInstanceOf(StepUpPrerequisiteError);
    expect(action).not.toHaveBeenCalled();
    expect(ensureStepUpProof).not.toHaveBeenCalled();
  });

  it("does not call or replay the action while the auth barrier is held", async () => {
    beginAuthTransitionBarrier();
    const action = vi.fn();
    const { result } = renderHook(() => useStepUpAction(STEP_UP_ACTIONS.taskManualTrigger), { wrapper });

    await expect(result.current(action)).rejects.toBeInstanceOf(AuthTransitionRejectedError);
    expect(action).not.toHaveBeenCalled();
    expect(ensureStepUpProof).not.toHaveBeenCalled();
  });

  it("ignores a stale authTransitioning flag when the barrier is clear", async () => {
    session.authTransitioning = true;
    const action = vi.fn().mockResolvedValue("ok");
    const { result } = renderHook(() => useStepUpAction(STEP_UP_ACTIONS.taskManualTrigger), { wrapper });

    await expect(result.current(action)).resolves.toBe("ok");
    expect(ensureStepUpProof).not.toHaveBeenCalled();
  });

  it("pauses a non-cached worker without reading proof while the barrier is held", async () => {
    beginAuthTransitionBarrier();
    const action = vi.fn();
    const { result } = renderHook(
      () => useStepUpAction(STEP_UP_ACTIONS.batchCommandCreate, { persist: false, reuseCached: false }),
      { wrapper },
    );

    await expect(result.current(action)).rejects.toBeInstanceOf(StepUpPausedError);
    expect(action).not.toHaveBeenCalled();
    expect(ensureStepUpProof).not.toHaveBeenCalled();
  });

  it("throws before reading a cached proof when the barrier starts after the first attempt", async () => {
    const action = vi.fn().mockImplementation(async () => {
      beginAuthTransitionBarrier();
      throw createStepUpRequiredError();
    });
    const { result } = renderHook(() => useStepUpAction(STEP_UP_ACTIONS.taskManualTrigger), { wrapper });

    await expect(result.current(action)).rejects.toBeInstanceOf(AuthTransitionRejectedError);
    expect(action).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProof).not.toHaveBeenCalled();
  });

  it("does not return a result after the auth generation changes", async () => {
    const action = vi.fn().mockImplementation(async () => {
      bumpAuthSessionGeneration();
      return "stale";
    });
    const { result } = renderHook(() => useStepUpAction(STEP_UP_ACTIONS.taskManualTrigger), { wrapper });

    await expect(result.current(action)).rejects.toBeInstanceOf(StepUpPausedError);
    expect(ensureStepUpProof).not.toHaveBeenCalled();
  });

  it("does not open a challenge when the auth generation changes before proof", async () => {
    const action = vi.fn().mockImplementation(async () => {
      bumpAuthSessionGeneration();
      throw createStepUpRequiredError();
    });
    const { result } = renderHook(() => useStepUpAction(STEP_UP_ACTIONS.taskManualTrigger), { wrapper });

    await expect(result.current(action)).rejects.toBeInstanceOf(StepUpPausedError);
    expect(action).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(clearStepUpProof).not.toHaveBeenCalled();
  });

  it("does not retry the action when the auth generation changes during proof", async () => {
    ensureStepUpProof.mockImplementation(async () => {
      bumpAuthSessionGeneration();
      return "proof-stale";
    });
    const action = vi.fn().mockRejectedValueOnce(createStepUpRequiredError());
    const { result } = renderHook(() => useStepUpAction(STEP_UP_ACTIONS.taskManualTrigger), { wrapper });

    await expect(result.current(action)).rejects.toBeInstanceOf(StepUpPausedError);
    expect(action).toHaveBeenCalledTimes(1);
    expect(clearStepUpProof).not.toHaveBeenCalled();
  });
});
