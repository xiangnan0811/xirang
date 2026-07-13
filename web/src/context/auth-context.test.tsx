import { describe, expect, it, beforeEach, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { AuthProvider } from "./auth-context";
import { useAuth } from "./auth-context.hooks";
import { saveStepUpProof } from "@/lib/step-up-storage";
import { apiClient } from "@/lib/api/client";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    requestStepUpProof: vi.fn(),
  },
}));

const requestStepUpProofMock = vi.mocked(apiClient.requestStepUpProof);

function AuthProbe() {
  const { token, username, isAuthenticated, login, logout, setTotpEnabled, ensureStepUpProof } = useAuth();
  const [stepUpProof, setStepUpProof] = useState("pending");

  return (
    <div>
      <span data-testid="token">{token ?? "null"}</span>
      <span data-testid="username">{username ?? "null"}</span>
      <span data-testid="authenticated">{String(isAuthenticated)}</span>
      <span data-testid="step-up-proof">{stepUpProof}</span>
      <button type="button" onClick={() => login("auth-marker", "alice", "admin", 1, true)}>登录</button>
      <button type="button" onClick={() => logout()}>退出</button>
      <button type="button" onClick={() => setTotpEnabled(false)}>关闭两步验证</button>
      <button type="button" onClick={() => void ensureStepUpProof(STEP_UP_ACTIONS.taskManualTrigger).then(setStepUpProof)}>请求二次验证</button>
      <button type="button" onClick={() => void ensureStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, { persist: false, reuseCached: false }).then(setStepUpProof)}>请求一次性二次验证</button>
    </div>
  );
}

describe("AuthProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    requestStepUpProofMock.mockReset();
  });

  it("初始化时可迁移旧 localStorage 到 sessionStorage", () => {
    localStorage.setItem("xirang-auth-token", "persisted-token");
    localStorage.setItem("xirang-username", "persisted-user");

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    expect(screen.getByTestId("token").textContent).toBe("persisted-token");
    expect(screen.getByTestId("username").textContent).toBe("persisted-user");
    expect(screen.getByTestId("authenticated").textContent).toBe("true");
    expect(sessionStorage.getItem("xirang-auth-token")).toBe("persisted-token");
    expect(sessionStorage.getItem("xirang-username")).toBe("persisted-user");
    expect(localStorage.getItem("xirang-auth-token")).toBeNull();
    expect(localStorage.getItem("xirang-username")).toBeNull();
  });

  it("登录与退出会同步更新鉴权状态和 sessionStorage", async () => {
    const user = userEvent.setup();

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    expect(screen.getByTestId("authenticated").textContent).toBe("false");

    await user.click(screen.getByRole("button", { name: "登录" }));

    expect(screen.getByTestId("token").textContent).toBe("auth-marker");
    expect(screen.getByTestId("username").textContent).toBe("alice");
    expect(screen.getByTestId("authenticated").textContent).toBe("true");
    expect(sessionStorage.getItem("xirang-auth-token")).toBe("auth-marker");
    expect(sessionStorage.getItem("xirang-username")).toBe("alice");
    expect(localStorage.getItem("xirang-auth-token")).toBeNull();
    expect(localStorage.getItem("xirang-username")).toBeNull();

    await user.click(screen.getByRole("button", { name: "退出" }));

    expect(screen.getByTestId("token").textContent).toBe("null");
    expect(screen.getByTestId("username").textContent).toBe("null");
    expect(screen.getByTestId("authenticated").textContent).toBe("false");
    expect(sessionStorage.getItem("xirang-auth-token")).toBeNull();
    expect(sessionStorage.getItem("xirang-username")).toBeNull();
    expect(localStorage.getItem("xirang-auth-token")).toBeNull();
    expect(localStorage.getItem("xirang-username")).toBeNull();
  });

  it("登录、退出和关闭两步验证会清理 session 级 step-up proof", async () => {
    const user = userEvent.setup();
    saveStepUpProof(STEP_UP_ACTIONS.terminalOpen, "proof-before-login-marker", Date.now() + 60_000);

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    await user.click(screen.getByRole("button", { name: "登录" }));
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();

    saveStepUpProof(STEP_UP_ACTIONS.configExport, "proof-before-disable-marker", Date.now() + 60_000);
    await user.click(screen.getByRole("button", { name: "关闭两步验证" }));
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();

    saveStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, "proof-before-logout-marker", Date.now() + 60_000);
    await user.click(screen.getByRole("button", { name: "退出" }));
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });

  it("ensureStepUpProof 会复用未过期 proof", async () => {
    saveStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, "cached-step-up-marker", Date.now() + 60_000);

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    await act(async () => {
      screen.getByRole("button", { name: "请求二次验证" }).click();
    });

    expect(screen.getByTestId("step-up-proof").textContent).toBe("cached-step-up-marker");
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toContain("cached-step-up-marker");
    expect(screen.queryByText("需要二次验证")).toBeNull();
  });

  it("可请求一次性 step-up proof 而不复用且清除 sessionStorage proof", async () => {
    const user = userEvent.setup();
    saveStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, "cached-step-up-marker", Date.now() + 60_000);
    requestStepUpProofMock.mockResolvedValueOnce({
      proof: "one-time-step-up-marker",
      expires_at: new Date(Date.now() + 60_000).toISOString(),
      proof_ttl_seconds: 60,
    });

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    await user.click(screen.getByRole("button", { name: "登录" }));
    saveStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, "cached-step-up-marker", Date.now() + 60_000);
    await user.click(screen.getByRole("button", { name: "请求一次性二次验证" }));
    expect(await screen.findByRole("dialog", { name: "需要二次验证" })).toBeInTheDocument();
    await user.type(screen.getByLabelText("验证器验证码"), "123456");
    await user.click(screen.getByRole("button", { name: "验证" }));

    await waitFor(() => expect(screen.getByTestId("step-up-proof").textContent).toBe("one-time-step-up-marker"));
    expect(requestStepUpProofMock).toHaveBeenCalledWith("auth-marker", "123456", STEP_UP_ACTIONS.taskManualTrigger);
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });
});
