import { describe, expect, it, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider, useAuth } from "./auth-context";

function AuthProbe() {
  const { token, username, isAuthenticated, login, logout } = useAuth();

  return (
    <div>
      <span data-testid="token">{token ?? "null"}</span>
      <span data-testid="username">{username ?? "null"}</span>
      <span data-testid="authenticated">{String(isAuthenticated)}</span>
      <button type="button" onClick={() => login("token-123", "alice")}>登录</button>
      <button type="button" onClick={() => logout()}>退出</button>
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
});
