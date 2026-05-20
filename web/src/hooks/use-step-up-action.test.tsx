import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { PropsWithChildren } from "react";
import { AuthContext, type AuthContextValue } from "@/context/auth-context.shared";
import { ApiError } from "@/lib/api/core";
import { useStepUpAction } from "./use-step-up-action";

function createStepUpRequiredError() {
  return new ApiError(403, "需要二次验证", {
    code: 403,
    message: "需要二次验证",
    data: { error_code: "STEP_UP_REQUIRED", proof_ttl_seconds: 300 },
  });
}

describe("useStepUpAction", () => {
  const ensureStepUpProof = vi.fn();
  const clearStepUpProof = vi.fn();

  function wrapper({ children }: PropsWithChildren) {
    const value: AuthContextValue = {
      token: "token-1",
      username: "admin",
      role: "admin",
      userId: 1,
      totpEnabled: true,
      isAuthenticated: true,
      login: vi.fn(),
      logout: vi.fn(),
      setTotpEnabled: vi.fn(),
      ensureStepUpProof,
      clearStepUpProof,
    };
    return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
  }

  beforeEach(() => {
    ensureStepUpProof.mockReset();
    clearStepUpProof.mockReset();
  });

  it("收到 STEP_UP_REQUIRED 后会请求 proof 并重试原动作", async () => {
    ensureStepUpProof.mockResolvedValueOnce("proof-1");
    const action = vi.fn()
      .mockRejectedValueOnce(createStepUpRequiredError())
      .mockResolvedValueOnce("ok");

    const { result } = renderHook(() => useStepUpAction(), { wrapper });
    await expect(result.current(action)).resolves.toBe("ok");

    expect(action).toHaveBeenNthCalledWith(1);
    expect(clearStepUpProof).not.toHaveBeenCalled();
    expect(ensureStepUpProof).toHaveBeenCalledTimes(1);
    expect(action).toHaveBeenNthCalledWith(2, "proof-1");
  });

  it("重试仍要求 step-up 时会再次清理 proof 并抛出错误", async () => {
    ensureStepUpProof.mockResolvedValueOnce("proof-2");
    const action = vi.fn()
      .mockRejectedValueOnce(createStepUpRequiredError())
      .mockRejectedValueOnce(createStepUpRequiredError());

    const { result } = renderHook(() => useStepUpAction(), { wrapper });
    await expect(result.current(action)).rejects.toBeInstanceOf(ApiError);

    expect(clearStepUpProof).toHaveBeenCalledTimes(1);
    expect(action).toHaveBeenNthCalledWith(2, "proof-2");
  });
});
