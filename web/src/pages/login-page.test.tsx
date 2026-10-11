import "@testing-library/jest-dom/vitest";
import { StrictMode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { LoginPage } from "./login-page";

const { navigateMock, loginMock, apiLoginMock, apiTotpLoginMock, getCaptchaMock, ApiErrorClass } = vi.hoisted(() => {
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
    getCaptchaMock: vi.fn(),
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
    getCaptcha: getCaptchaMock,
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
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
    <MemoryRouter initialEntries={initialEntries}>
      <LoginPage />
    </MemoryRouter>
  );
}

function renderLoginPageStrict() {
  return render(
    <StrictMode>
      <MemoryRouter initialEntries={["/login"]}>
        <LoginPage />
      </MemoryRouter>
    </StrictMode>,
  );
}

async function reachAuthenticatorStep(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText("用户名"), "admin");
  await user.type(screen.getByLabelText("密码"), "secret");
  await user.click(screen.getByRole("button", { name: "登录控制台" }));
  await waitFor(() => {
    expect(screen.getByLabelText("验证码")).toBeInTheDocument();
  });
}

function verificationForm() {
  const form = screen.getByRole("button", { name: "验证" }).closest("form");
  if (!(form instanceof HTMLFormElement)) {
    throw new Error("missing verification form");
  }
  return form;
}

describe("LoginPage", () => {
  it("demo 模式提供 mock-only 入口且不执行登录请求", async () => {
    vi.stubEnv("VITE_ENABLE_DEMO_MODE", "true");
    const user = userEvent.setup();

    renderLoginPage();

    expect(screen.getByText("演示模式仅使用 mock 数据")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "进入演示控制台" }));

    expect(navigateMock).toHaveBeenCalledWith("/app/overview", { replace: true });
    expect(apiLoginMock).not.toHaveBeenCalled();
  });

  it("renders exactly one level-1 heading", () => {
    renderLoginPage();

    const h1s = screen.getAllByRole("heading", { level: 1 });
    expect(h1s).toHaveLength(1);
    expect(h1s[0]).toHaveTextContent("息壤集中备份管理平台");
  });

  beforeEach(() => {
    vi.unstubAllEnvs();
    navigateMock.mockReset();
    loginMock.mockReset();
    apiLoginMock.mockReset();
    apiTotpLoginMock.mockReset();
    getCaptchaMock.mockReset();
    // Default: captcha channels off — no UI fields.
    getCaptchaMock.mockResolvedValue({
      enabled: false,
      secondRequired: false,
    });
  });

  it("enabled=false 时不渲染主验证码输入框", async () => {
    renderLoginPage();
    await waitFor(() => {
      expect(getCaptchaMock).toHaveBeenCalled();
    });
    expect(screen.queryByLabelText("验证码")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("二次验证码")).not.toBeInTheDocument();
  });

  it("仅二次验证码开启时只渲染二次输入框，登录 payload 只带 secondCaptcha", async () => {
    getCaptchaMock.mockResolvedValue({
      enabled: false,
      secondRequired: true,
      secondId: "sec-1",
      secondQuestion: "9 + 1 = ?",
    });
    apiLoginMock.mockResolvedValue({
      token: "jwt-token",
      user: { id: 1, username: "admin", role: "admin" },
    });
    const user = userEvent.setup();
    renderLoginPage();
    await waitFor(() => {
      expect(screen.getByLabelText("二次验证码")).toBeInTheDocument();
    });
    expect(screen.getByText("9 + 1 = ?")).toBeInTheDocument();
    expect(screen.queryByLabelText("验证码")).not.toBeInTheDocument();

    await user.type(screen.getByLabelText("用户名"), "admin");
    await user.type(screen.getByLabelText("密码"), "secret");
    await user.type(screen.getByLabelText("二次验证码"), "10");
    await user.click(screen.getByRole("button", { name: "登录控制台" }));

    await waitFor(() => {
      expect(apiLoginMock).toHaveBeenCalledWith("admin", "secret", {
        captchaId: undefined,
        captchaAnswer: undefined,
        secondCaptchaId: "sec-1",
        secondCaptchaAnswer: "10",
      });
    });
  });

  it("主验证码开启时渲染主输入框，登录时带上 captcha 字段", async () => {
    getCaptchaMock.mockResolvedValue({
      enabled: true,
      id: "cap-1",
      question: "2 + 3 = ?",
      secondRequired: false,
    });
    apiLoginMock.mockResolvedValue({
      token: "jwt-token",
      user: { id: 1, username: "admin", role: "admin" },
    });
    const user = userEvent.setup();
    renderLoginPage();
    await waitFor(() => {
      expect(screen.getByLabelText("验证码")).toBeInTheDocument();
    });
    expect(screen.getByText("2 + 3 = ?")).toBeInTheDocument();

    await user.type(screen.getByLabelText("用户名"), "admin");
    await user.type(screen.getByLabelText("密码"), "secret");
    await user.type(screen.getByLabelText("验证码"), "5");
    await user.click(screen.getByRole("button", { name: "登录控制台" }));

    await waitFor(() => {
      expect(apiLoginMock).toHaveBeenCalledWith("admin", "secret", {
        captchaId: "cap-1",
        captchaAnswer: "5",
        secondCaptchaId: undefined,
        secondCaptchaAnswer: undefined,
      });
    });
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
      requires2FA: true,
      loginToken: "temp-login-token",
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
      requires2FA: true,
      loginToken: "temp-login-token",
    });
    apiTotpLoginMock.mockResolvedValue({
      token: "jwt-token-2fa",
      user: { id: 2, username: "admin", role: "admin", totpEnabled: true },
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
      expect(loginMock).toHaveBeenCalledWith("jwt-token-2fa", "admin", "admin", 2, true);
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

  it("切换验证模式时清空已输入秘密和错误并聚焦同一输入框", async () => {
    apiLoginMock.mockResolvedValue({
      requires2FA: true,
      loginToken: "temp-login-token",
    });
    apiTotpLoginMock.mockRejectedValue(
      new ApiErrorClass(401, "验证码错误", { error: "验证码错误" }),
    );
    const user = userEvent.setup();
    renderLoginPage();
    await reachAuthenticatorStep(user);

    const totpInput = screen.getByLabelText("验证码");
    expect(totpInput).toHaveAttribute("type", "text");
    expect(totpInput).toHaveAttribute("inputmode", "numeric");
    expect(totpInput).toHaveAttribute("autocomplete", "one-time-code");
    expect(totpInput).not.toHaveAttribute("maxlength");
    expect(totpInput).not.toHaveAttribute("pattern");
    expect(totpInput).toHaveAttribute("aria-describedby", "login-verification-help");
    expect(document.getElementById("login-verification-help")).toHaveTextContent(
      "请输入验证器 App 中的验证码。",
    );
    expect(screen.getAllByText("请输入验证器 App 中的验证码。")).toHaveLength(1);

    await user.type(totpInput, "123456");
    await user.click(screen.getByRole("button", { name: "验证" }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent("返回登录重新开始");
    });

    await user.click(screen.getByRole("button", { name: "使用恢复码" }));

    const recoveryInput = screen.getByLabelText("恢复码");
    expect(recoveryInput).toBe(totpInput);
    expect(recoveryInput).toHaveValue("");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(recoveryInput).toHaveAttribute("aria-invalid", "false");
    expect(recoveryInput).toHaveAttribute("inputmode", "text");
    expect(recoveryInput).toHaveAttribute("autocomplete", "off");
    expect(recoveryInput).toHaveAttribute("autocapitalize", "none");
    expect(recoveryInput).toHaveAttribute("spellcheck", "false");
    expect(recoveryInput).not.toHaveAttribute("maxlength");
    expect(document.getElementById("login-verification-help")).toHaveTextContent(
      "恢复码为 8 位字母数字，每个只能使用一次，大小写均可。",
    );
    expect(screen.getAllByText("恢复码为 8 位字母数字，每个只能使用一次，大小写均可。")).toHaveLength(1);
    expect(screen.queryByText("请输入验证器 App 中的验证码。")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(recoveryInput).toHaveFocus();
    });

    await user.type(recoveryInput, "AbCd2345");
    await user.click(screen.getByRole("button", { name: "使用验证器验证码" }));
    const totpAgain = screen.getByLabelText("验证码");
    expect(totpAgain).toBe(recoveryInput);
    expect(totpAgain).toHaveValue("");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(totpAgain).toHaveFocus();
    });
  });

  it("粘贴含首尾空白的恢复码时保留空白和大小写并不截断", async () => {
    apiLoginMock.mockResolvedValue({
      requires2FA: true,
      loginToken: "temp-login-token",
    });
    apiTotpLoginMock.mockResolvedValue({
      token: "jwt-recovery",
      user: { id: 3, username: "admin", role: "admin", totpEnabled: true },
    });
    const user = userEvent.setup();
    renderLoginPage();
    await reachAuthenticatorStep(user);
    await user.click(screen.getByRole("button", { name: "使用恢复码" }));

    const recoveryInput = screen.getByLabelText("恢复码");
    await user.click(recoveryInput);
    // 单行输入会去掉换行；空格、制表符和大小写必须原样留下。
    await user.paste(" \tAbCd2345 \r\n");

    expect(recoveryInput).toHaveValue(" \tAbCd2345 ");
    expect(recoveryInput).not.toHaveAttribute("maxlength");
    await user.click(screen.getByRole("button", { name: "验证" }));

    await waitFor(() => {
      expect(apiTotpLoginMock).toHaveBeenCalledWith("temp-login-token", " \tAbCd2345 ");
    });
    expect(loginMock).toHaveBeenCalledWith("jwt-recovery", "admin", "admin", 3, true);
  });

  it("返回登录后新的待验证步骤不恢复旧模式、输入和错误", async () => {
    apiLoginMock
      .mockResolvedValueOnce({ requires2FA: true, loginToken: "pending-1" })
      .mockResolvedValueOnce({ requires2FA: true, loginToken: "pending-2" });
    apiTotpLoginMock
      .mockRejectedValueOnce(new ApiErrorClass(401, "登录步骤已过期", { error: "登录步骤已过期" }))
      .mockResolvedValueOnce({
        token: "jwt-after-back",
        user: { id: 4, username: "admin", role: "admin", totpEnabled: true },
      });
    const user = userEvent.setup();
    renderLoginPage();
    await waitFor(() => {
      expect(getCaptchaMock).toHaveBeenCalledTimes(1);
    });
    await reachAuthenticatorStep(user);

    await user.click(screen.getByRole("button", { name: "使用恢复码" }));
    await user.type(screen.getByLabelText("恢复码"), "AbCd2345");
    await user.click(screen.getByRole("button", { name: "验证" }));
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent("返回登录重新开始");
    });
    expect(screen.queryByText("登录步骤已过期")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "返回登录" }));

    expect(screen.getByLabelText("用户名")).toHaveValue("admin");
    expect(screen.getByLabelText("密码")).toHaveValue("secret");
    expect(screen.queryByLabelText("恢复码")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(getCaptchaMock).toHaveBeenCalledTimes(2);
    });

    await user.click(screen.getByRole("button", { name: "登录控制台" }));
    await waitFor(() => {
      expect(screen.getByLabelText("验证码")).toHaveValue("");
    });
    expect(screen.getByRole("button", { name: "使用恢复码" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "使用验证器验证码" })).not.toBeInTheDocument();
    expect(document.getElementById("login-verification-help")).toHaveTextContent(
      "请输入验证器 App 中的验证码。",
    );
    expect(screen.queryByText("恢复码为 8 位字母数字，每个只能使用一次，大小写均可。")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    await user.type(screen.getByLabelText("验证码"), "654321");
    await user.click(screen.getByRole("button", { name: "验证" }));
    await waitFor(() => {
      expect(loginMock).toHaveBeenCalledWith("jwt-after-back", "admin", "admin", 4, true);
    });
    expect(apiTotpLoginMock).toHaveBeenNthCalledWith(1, "pending-1", "AbCd2345");
    expect(apiTotpLoginMock).toHaveBeenNthCalledWith(2, "pending-2", "654321");
  });

  it("验证请求未返回时不能切换模式、返回或重复提交", async () => {
    let resolveTotp: (value: {
      token: string;
      user: { id: number; username: string; role: string; totpEnabled: boolean };
    }) => void = () => {};
    apiLoginMock.mockResolvedValue({
      requires2FA: true,
      loginToken: "temp-login-token",
    });
    apiTotpLoginMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveTotp = resolve;
        }),
    );
    const user = userEvent.setup();
    renderLoginPage();
    await reachAuthenticatorStep(user);
    await user.type(screen.getByLabelText("验证码"), "123456");

    const form = verificationForm();
    fireEvent.submit(form);
    fireEvent.submit(form);

    expect(apiTotpLoginMock).toHaveBeenCalledTimes(1);
    expect(apiTotpLoginMock).toHaveBeenCalledWith("temp-login-token", "123456");
    expect(screen.getByLabelText("验证码")).toBeDisabled();
    expect(screen.getByLabelText("验证码")).toHaveValue("123456");
    expect(screen.getByRole("button", { name: "使用恢复码" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "返回登录" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "验证" })).toBeDisabled();
    expect(screen.queryByLabelText("用户名")).not.toBeInTheDocument();

    fireEvent.submit(form);
    expect(apiTotpLoginMock).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolveTotp({
        token: "jwt-token-2fa",
        user: { id: 2, username: "admin", role: "admin", totpEnabled: true },
      });
    });
    await waitFor(() => {
      expect(loginMock).toHaveBeenCalledTimes(1);
    });
    expect(loginMock).toHaveBeenCalledWith("jwt-token-2fa", "admin", "admin", 2, true);
    expect(navigateMock).toHaveBeenCalledWith("/app/overview", { replace: true });
    expect(screen.getByLabelText("验证码")).toHaveValue("");
  });

  it("页面卸载后迟到的验证成功不安装身份", async () => {
    let resolveTotp: (value: {
      token: string;
      user: { id: number; username: string; role: string; totpEnabled: boolean };
    }) => void = () => {};
    apiLoginMock.mockResolvedValue({
      requires2FA: true,
      loginToken: "temp-login-token",
    });
    apiTotpLoginMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveTotp = resolve;
        }),
    );
    const user = userEvent.setup();
    const { unmount } = renderLoginPageStrict();
    await reachAuthenticatorStep(user);
    await user.type(screen.getByLabelText("验证码"), "123456");
    fireEvent.submit(verificationForm());
    await waitFor(() => {
      expect(apiTotpLoginMock).toHaveBeenCalledTimes(1);
    });

    unmount();
    await act(async () => {
      resolveTotp({
        token: "jwt-late",
        user: { id: 2, username: "admin", role: "admin", totpEnabled: true },
      });
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(loginMock).not.toHaveBeenCalled();
    expect(navigateMock).not.toHaveBeenCalled();
  });

  it("验证返回 401 时显示重新开始提示且不自动重试", async () => {
    apiLoginMock.mockResolvedValue({
      requires2FA: true,
      loginToken: "temp-login-token",
    });
    apiTotpLoginMock.mockRejectedValue(
      new ApiErrorClass(401, "登录步骤已过期", { error: "登录步骤已过期" }),
    );
    const user = userEvent.setup();
    renderLoginPage();
    await reachAuthenticatorStep(user);
    await user.type(screen.getByLabelText("验证码"), "123456");
    await user.click(screen.getByRole("button", { name: "验证" }));

    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(
        "验证码不正确，或本次登录步骤已失效。请检查后重试，或返回登录重新开始。",
      );
    });
    expect(screen.queryByText("登录步骤已过期")).not.toBeInTheDocument();
    expect(screen.getByLabelText("验证码")).toHaveValue("123456");
    expect(screen.getByLabelText("验证码")).toBeEnabled();
    expect(screen.getByLabelText("验证码")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("验证码")).toHaveAttribute("aria-errormessage", "login-form-error");
    expect(screen.getByRole("button", { name: "验证" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "返回登录" })).toBeEnabled();
    expect(screen.queryByLabelText("用户名")).not.toBeInTheDocument();
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(apiTotpLoginMock).toHaveBeenCalledTimes(1);
  });

  it("StrictMode 重新挂载后两步验证成功仍会安装身份", async () => {
    apiLoginMock.mockResolvedValue({
      requires2FA: true,
      loginToken: "temp-login-token",
    });
    apiTotpLoginMock.mockResolvedValue({
      token: "jwt-token-2fa",
      user: { id: 2, username: "admin", role: "admin", totpEnabled: true },
    });
    const user = userEvent.setup();
    renderLoginPageStrict();
    await reachAuthenticatorStep(user);
    await user.type(screen.getByLabelText("验证码"), "123456");
    await user.click(screen.getByRole("button", { name: "验证" }));

    await waitFor(() => {
      expect(loginMock).toHaveBeenCalledWith("jwt-token-2fa", "admin", "admin", 2, true);
    });
    expect(navigateMock).toHaveBeenCalledWith("/app/overview", { replace: true });
    expect(screen.getByLabelText("验证码")).toHaveValue("");
  });
});
