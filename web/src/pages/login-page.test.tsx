import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { LoginPage } from "./login-page";

const { navigateMock, loginMock, apiLoginMock, apiTotpLoginMock, ApiErrorClass } = vi.hoisted(() => {
  class ApiError extends Error {
    status: number;
    detail?: unknown;
    constructor(status: number, message: string, detail?: unknown) {
      super(message);
      this.name = "ApiError";
      this.status = status;
      this.detail = detail;
    }
  }
  return {
    navigateMock: vi.fn(),
    loginMock: vi.fn(),
    apiLoginMock: vi.fn(),
    apiTotpLoginMock: vi.fn(),
    ApiErrorClass: ApiError,
  };
});

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useNavigate: () => navigateMock,
  };
});

vi.mock("@/lib/api/client", () => ({
  ApiError: ApiErrorClass,
  apiClient: {
    login: apiLoginMock,
    totpLogin: apiTotpLoginMock,
  },
}));

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({
    isAuthenticated: false,
    login: loginMock,
  }),
}));

function renderLoginPage() {
  return renderLoginPageWithEntries(["/login"]);
}

function renderLoginPageWithEntries(initialEntries: string[]) {
  return render(
    <MemoryRouter
      initialEntries={initialEntries}
      future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
    >
      <LoginPage />
    </MemoryRouter>
  );
}

describe("LoginPage", () => {
  beforeEach(() => {
    navigateMock.mockReset();
    loginMock.mockReset();
    apiLoginMock.mockReset();
    apiTotpLoginMock.mockReset();
  });

  it("成功登录后调用 login 并跳转到 /app/overview", async () => {
    const user = userEvent.setup();
    apiLoginMock.mockResolvedValue({
      token: "jwt-token",
      user: { id: 1, username: "admin", role: "admin" },
    });

    renderLoginPage();

    await user.type(screen.getByLabelText("用户名"), "admin");
    await user.type(screen.getByLabelText("密码"), "secret");
    await user.click(screen.getByRole("button", { name: "登录控制台" }));

    await waitFor(() => {
      expect(loginMock).toHaveBeenCalledWith("jwt-token", "admin", "admin", 1, false);
    });
    expect(navigateMock).toHaveBeenCalledWith("/app/overview", { replace: true });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("密码错误时显示错误提示且不跳转", async () => {
    const user = userEvent.setup();
    apiLoginMock.mockRejectedValue(
      new ApiErrorClass(401, "用户名或密码错误。", { error: "用户名或密码错误。" })
    );

    renderLoginPage();

    await user.type(screen.getByLabelText("用户名"), "admin");
    await user.type(screen.getByLabelText("密码"), "wrong");
    await user.click(screen.getByRole("button", { name: "登录控制台" }));

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeInTheDocument();
    });
    expect(screen.getByRole("alert")).toHaveTextContent("用户名或密码错误。");
    expect(navigateMock).not.toHaveBeenCalled();
    expect(loginMock).not.toHaveBeenCalled();
  });

  it("响应包含 requires_2fa 时显示两步验证步骤", async () => {
    const user = userEvent.setup();
    apiLoginMock.mockResolvedValue({
      requires_2fa: true,
      login_token: "temp-login-token",
    });

    renderLoginPage();

    await user.type(screen.getByLabelText("用户名"), "admin");
    await user.type(screen.getByLabelText("密码"), "secret");
    await user.click(screen.getByRole("button", { name: "登录控制台" }));

    await waitFor(() => {
      expect(screen.getByText("两步验证")).toBeInTheDocument();
    });
    expect(screen.getByLabelText("验证码")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "验证" })).toBeInTheDocument();
    expect(screen.queryByLabelText("用户名")).not.toBeInTheDocument();
  });

  it("两步验证成功后调用 login 并跳转", async () => {
    const user = userEvent.setup();
    apiLoginMock.mockResolvedValue({
      requires_2fa: true,
      login_token: "temp-login-token",
    });
    apiTotpLoginMock.mockResolvedValue({
      token: "jwt-token-2fa",
      user: { id: 2, username: "admin", role: "admin" },
    });

    renderLoginPage();

    await user.type(screen.getByLabelText("用户名"), "admin");
    await user.type(screen.getByLabelText("密码"), "secret");
    await user.click(screen.getByRole("button", { name: "登录控制台" }));

    await waitFor(() => {
      expect(screen.getByLabelText("验证码")).toBeInTheDocument();
    });

    await user.type(screen.getByLabelText("验证码"), "123456");
    await user.click(screen.getByRole("button", { name: "验证" }));

    await waitFor(() => {
      expect(loginMock).toHaveBeenCalledWith("jwt-token-2fa", "admin", "admin", 2, undefined);
    });
    expect(navigateMock).toHaveBeenCalledWith("/app/overview", { replace: true });
  });

  it("成功登录后保留 redirect 查询参数中的完整返回路径", async () => {
    const user = userEvent.setup();
    apiLoginMock.mockResolvedValue({
      token: "jwt-token",
      user: { id: 9, username: "admin", role: "admin" },
    });

    renderLoginPageWithEntries([
      "/login?redirect=%2Fapp%2Flogs%3Ftask%3D7%26level%3Derror%23tail",
    ]);

    await user.type(screen.getByLabelText("用户名"), "admin");
    await user.type(screen.getByLabelText("密码"), "secret");
    await user.click(screen.getByRole("button", { name: "登录控制台" }));

    await waitFor(() => {
      expect(loginMock).toHaveBeenCalledWith("jwt-token", "admin", "admin", 9, false);
    });
    expect(navigateMock).toHaveBeenCalledWith("/app/logs?task=7&level=error#tail", { replace: true });
  });

  it("忽略站外 redirect 参数，回退到站内默认页", async () => {
    const user = userEvent.setup();
    apiLoginMock.mockResolvedValue({
      token: "jwt-token",
      user: { id: 10, username: "admin", role: "admin" },
    });

    renderLoginPageWithEntries(["/login?redirect=https%3A%2F%2Fevil.example%2Fphish"]);

    await user.type(screen.getByLabelText("用户名"), "admin");
    await user.type(screen.getByLabelText("密码"), "secret");
    await user.click(screen.getByRole("button", { name: "登录控制台" }));

    await waitFor(() => {
      expect(loginMock).toHaveBeenCalledWith("jwt-token", "admin", "admin", 10, false);
    });
    expect(navigateMock).toHaveBeenCalledWith("/app/overview", { replace: true });
  });

  it("忽略带反斜杠的畸形 redirect 参数，回退到站内默认页", async () => {
    const user = userEvent.setup();
    apiLoginMock.mockResolvedValue({
      token: "jwt-token",
      user: { id: 11, username: "admin", role: "admin" },
    });

    renderLoginPageWithEntries(["/login?redirect=%2F%5Cevil.example%2Fphish"]);

    await user.type(screen.getByLabelText("用户名"), "admin");
    await user.type(screen.getByLabelText("密码"), "secret");
    await user.click(screen.getByRole("button", { name: "登录控制台" }));

    await waitFor(() => {
      expect(loginMock).toHaveBeenCalledWith("jwt-token", "admin", "admin", 11, false);
    });
    expect(navigateMock).toHaveBeenCalledWith("/app/overview", { replace: true });
  });

  it("账号被锁定（403）时显示无权访问错误", async () => {
    const user = userEvent.setup();
    apiLoginMock.mockRejectedValue(new ApiErrorClass(403, "当前账号无权访问该系统。"));

    renderLoginPage();

    await user.type(screen.getByLabelText("用户名"), "locked");
    await user.type(screen.getByLabelText("密码"), "any");
    await user.click(screen.getByRole("button", { name: "登录控制台" }));

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeInTheDocument();
    });
    expect(screen.getByRole("alert")).toHaveTextContent("当前账号无权访问该系统。");
    expect(loginMock).not.toHaveBeenCalled();
  });

  it("登录频率被锁定（423）时显示后端返回的锁定提示", async () => {
    const user = userEvent.setup();
    apiLoginMock.mockRejectedValue(
      new ApiErrorClass(423, "登录失败次数过多，请稍后再试", {
        error: "登录失败次数过多，请稍后再试",
      })
    );

    renderLoginPage();

    await user.type(screen.getByLabelText("用户名"), "locked");
    await user.type(screen.getByLabelText("密码"), "secret");
    await user.click(screen.getByRole("button", { name: "登录控制台" }));

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeInTheDocument();
    });
    expect(screen.getByRole("alert")).toHaveTextContent("登录失败次数过多，请稍后再试");
    expect(loginMock).not.toHaveBeenCalled();
  });
});
