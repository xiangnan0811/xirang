import { describe, expect, it, beforeEach } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { AuthProvider } from "./auth-context";
import { useAuth } from "./auth-context.hooks";
import { saveStepUpProof } from "@/lib/step-up-storage";

function AuthProbe() {
  const { token, username, isAuthenticated, login, logout, setTotpEnabled, ensureStepUpProof } = useAuth();
  const [stepUpProof, setStepUpProof] = useState("pending");

  return (
    <div>
      <span data-testid="token">{token ?? "null"}</span>
      <span data-testid="username">{username ?? "null"}</span>
      <span data-testid="authenticated">{String(isAuthenticated)}</span>
      <span data-testid="step-up-proof">{stepUpProof}</span>
      <button type="button" onClick={() => login("token-123", "alice", "admin", 1, true)}>登录</button>
      <button type="button" onClick={() => logout()}>退出</button>
      <button type="button" onClick={() => setTotpEnabled(false)}>关闭两步验证</button>
      <button type="button" onClick={() => void ensureStepUpProof().then(setStepUpProof)}>请求二次验证</button>
    </div>
  );
}

describe("AuthProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
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

    expect(screen.getByTestId("token").textContent).toBe("token-123");
    expect(screen.getByTestId("username").textContent).toBe("alice");
    expect(screen.getByTestId("authenticated").textContent).toBe("true");
    expect(sessionStorage.getItem("xirang-auth-token")).toBe("token-123");
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
    saveStepUpProof("proof-before-login", Date.now() + 60_000);

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    await user.click(screen.getByRole("button", { name: "登录" }));
    expect(sessionStorage.getItem("xirang-step-up-proof")).toBeNull();

    saveStepUpProof("proof-before-disable", Date.now() + 60_000);
    await user.click(screen.getByRole("button", { name: "关闭两步验证" }));
    expect(sessionStorage.getItem("xirang-step-up-proof")).toBeNull();

    saveStepUpProof("proof-before-logout", Date.now() + 60_000);
    await user.click(screen.getByRole("button", { name: "退出" }));
    expect(sessionStorage.getItem("xirang-step-up-proof")).toBeNull();
  });

  it("ensureStepUpProof 会复用未过期 proof", async () => {
    saveStepUpProof("cached-proof", Date.now() + 60_000);

    render(
      <AuthProvider>
        <AuthProbe />
      </AuthProvider>
    );

    await act(async () => {
      screen.getByRole("button", { name: "请求二次验证" }).click();
    });

    expect(screen.getByTestId("step-up-proof").textContent).toBe("cached-proof");
    expect(sessionStorage.getItem("xirang-step-up-proof")).toBe("cached-proof");
    expect(screen.queryByText("需要二次验证")).toBeNull();
  });
});
