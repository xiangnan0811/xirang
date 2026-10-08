import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode, useEffect, useState } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { AuthProvider } from "@/context/auth-context";
import { useAuth } from "@/context/auth-context.hooks";
import { ApiError, AuthTransitionRejectedError, clearAuthTransitionBarrier, dismissTOTPActivationFailure, isAuthTransitionActive, request } from "@/lib/api/core";
import { TOTPSetupDialog } from "./totp-setup-dialog";

const { setup, verify } = vi.hoisted(() => ({
  setup: vi.fn(),
  verify: vi.fn(),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: { totpSetup: setup, totpVerify: verify },
  ApiError: class extends Error {
    detail?: unknown;
  },
}));

function futureEnrollmentExpiry(): string {
  return new Date(Date.now() + 10 * 60_000).toISOString();
}

const successPayload = {
  token: "replacement-token",
  user: { id: 4, username: "alice", role: "admin" as const, totpEnabled: true as const },
  recoveryCodes: ["code-one", "code-two"],
};

let revivalLogin: ReturnType<typeof useAuth>["login"] | null = null;

beforeEach(() => {
  setup.mockReset();
  verify.mockReset();
  dismissTOTPActivationFailure();
  clearAuthTransitionBarrier();
  sessionStorage.clear();
  localStorage.clear();
  revivalLogin = null;
});

it("discards an already loaded enrollment when the parent closes the dialog", async () => {
  sessionStorage.setItem("xirang-auth-token", "test-token");
  sessionStorage.setItem("xirang-role", "admin");
  setup.mockResolvedValueOnce({ secret: "OLD-LOADED-SECRET", qrUrl: "", enrollmentId: "old", expiresAt: futureEnrollmentExpiry() });
  setup.mockResolvedValueOnce({ secret: "FRESH-LOADED-SECRET", qrUrl: "", enrollmentId: "new", expiresAt: futureEnrollmentExpiry() });
  const props = { onOpenChange: vi.fn(), token: "test-token" };
  const view = render(<AuthProvider><TOTPSetupDialog open {...props} /></AuthProvider>);
  expect(await screen.findByText("OLD-LOADED-SECRET")).toBeInTheDocument();
  view.rerender(<AuthProvider><TOTPSetupDialog open={false} {...props} /></AuthProvider>);
  view.rerender(<AuthProvider><TOTPSetupDialog open {...props} /></AuthProvider>);
  expect(screen.queryByText("OLD-LOADED-SECRET")).not.toBeInTheDocument();
  expect(await screen.findByText("FRESH-LOADED-SECRET")).toBeInTheDocument();
});

it("starts a fresh enrollment on controlled reopen and ignores the old pending secret", async () => {
  sessionStorage.setItem("xirang-auth-token", "test-token");
  sessionStorage.setItem("xirang-role", "admin");
  let finishOld!: (value: object) => void;
  setup.mockReturnValueOnce(new Promise((resolve) => { finishOld = resolve; }));
  setup.mockResolvedValueOnce({ secret: "NEW-TEST-SECRET", qrUrl: "", enrollmentId: "new", expiresAt: futureEnrollmentExpiry() });
  const props = { onOpenChange: vi.fn(), token: "test-token" };
  const view = render(<AuthProvider><TOTPSetupDialog open {...props} /></AuthProvider>);
  view.rerender(<AuthProvider><TOTPSetupDialog open={false} {...props} /></AuthProvider>);
  view.rerender(<AuthProvider><TOTPSetupDialog open {...props} /></AuthProvider>);
  expect(await screen.findByText("NEW-TEST-SECRET")).toBeInTheDocument();
  await act(async () => {
    finishOld({ secret: "OLD-TEST-SECRET", qrUrl: "", enrollmentId: "old", expiresAt: futureEnrollmentExpiry() });
  });
  expect(screen.queryByText("OLD-TEST-SECRET")).not.toBeInTheDocument();
  expect(screen.getByText("NEW-TEST-SECRET")).toBeInTheDocument();
  expect(setup).toHaveBeenCalledTimes(2);
});

function ActivationHarness({
  onOpenChange,
  onSuccess,
}: {
  onOpenChange: (open: boolean) => void;
  onSuccess: () => void;
}) {
  const auth = useAuth();
  const [open, setOpen] = useState(false);
  return (
    <div>
      <button type="button" onClick={() => auth.logout()}>退出</button>
      <button type="button" onClick={() => auth.login("other-token", "bob", "admin", 8, true)}>切换用户</button>
      <button type="button" onClick={() => {
        auth.login("old-token", "alice", "admin", 4, false);
        setOpen(true);
      }}>打开设置</button>
      <span data-testid="session-token">{auth.token ?? "null"}</span>
      <span data-testid="session-totp">{String(auth.totpEnabled)}</span>
      {auth.token ? (
        <TOTPSetupDialog
          open={open}
          onOpenChange={(next) => {
            onOpenChange(next);
            setOpen(next);
          }}
          token={auth.token}
          onSuccess={onSuccess}
        />
      ) : null}
    </div>
  );
}

async function openVerifyStep(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: "打开设置" }));
  expect(await screen.findByText("ENROLL-SECRET")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "下一步" }));
  await user.type(screen.getByLabelText("验证码"), "123456");
}

it("blocks close while verifying, installs the replacement session, and keeps codes across that token change", async () => {
  const user = userEvent.setup();
  const onOpenChange = vi.fn();
  const onSuccess = vi.fn();
  let finishVerify!: (value: typeof successPayload) => void;
  setup.mockResolvedValue({
    secret: "ENROLL-SECRET",
    qrUrl: "",
    enrollmentId: "enrollment-1",
    expiresAt: futureEnrollmentExpiry(),
  });
  verify.mockImplementation(async (_token: string, _code: string, _enrollment: string, transitionId: number) => {
    expect(isAuthTransitionActive()).toBe(true);
    expect(transitionId).toEqual(expect.any(Number));
    await expect(request("/nodes", { token: "old-token" })).rejects.toBeInstanceOf(AuthTransitionRejectedError);
    return new Promise<typeof successPayload>((resolve) => {
      finishVerify = resolve;
    });
  });
  render(
    <AuthProvider>
      <ActivationHarness onOpenChange={onOpenChange} onSuccess={onSuccess} />
    </AuthProvider>,
  );
  await openVerifyStep(user);
  await user.click(screen.getByRole("button", { name: "验证并开启" }));
  expect(await screen.findByRole("status")).toHaveTextContent("正在验证，请保持对话框打开，等待结果。");
  expect(screen.getByRole("button", { name: "Close" })).toBeDisabled();
  await user.keyboard("{Escape}");
  expect(onOpenChange).not.toHaveBeenCalled();

  await act(async () => {
    finishVerify(successPayload);
  });
  expect(await screen.findByText("code-one")).toBeInTheDocument();
  expect(screen.getByText("code-two")).toBeInTheDocument();
  expect(screen.getByTestId("session-token")).toHaveTextContent("replacement-token");
  expect(screen.getByTestId("session-totp")).toHaveTextContent("true");
  expect(onSuccess).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "完成" })).toBeDisabled();

  await user.keyboard("{Escape}");
  expect(screen.getByText("code-one")).toBeInTheDocument();
  await user.click(screen.getByRole("checkbox", { name: "我已保存恢复码" }));
  await user.click(screen.getByRole("button", { name: "完成" }));
  expect(onSuccess).toHaveBeenCalledTimes(1);
});

it("keeps the old session for an invalid code and allows one explicit retry", async () => {
  const user = userEvent.setup();
  setup.mockResolvedValue({
    secret: "ENROLL-SECRET",
    qrUrl: "",
    enrollmentId: "enrollment-1",
    expiresAt: futureEnrollmentExpiry(),
  });
  verify
    .mockRejectedValueOnce(new ApiError(400, "bad", { data: { error_code: "TOTP_CODE_INVALID" } }))
    .mockResolvedValueOnce(successPayload);
  render(
    <AuthProvider>
      <ActivationHarness onOpenChange={vi.fn()} onSuccess={vi.fn()} />
    </AuthProvider>,
  );
  await openVerifyStep(user);
  await user.click(screen.getByRole("button", { name: "验证并开启" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("验证码无效，请核对验证器后再试。");
  expect(screen.getByTestId("session-token")).toHaveTextContent("old-token");
  expect(screen.getByTestId("session-totp")).toHaveTextContent("false");

  await user.click(screen.getByRole("button", { name: "验证并开启" }));
  expect(await screen.findByText("code-one")).toBeInTheDocument();
  expect(verify).toHaveBeenCalledTimes(2);
  expect(screen.getByTestId("session-token")).toHaveTextContent("replacement-token");
});

it("does not retry an ambiguous verify, and a late success cannot resurrect a logged-out session", async () => {
  const user = userEvent.setup();
  let finishVerify!: (value: typeof successPayload) => void;
  setup.mockResolvedValue({
    secret: "ENROLL-SECRET",
    qrUrl: "",
    enrollmentId: "enrollment-1",
    expiresAt: futureEnrollmentExpiry(),
  });
  verify
    .mockRejectedValueOnce(new TypeError("Failed to fetch"))
    .mockImplementationOnce(() => new Promise<typeof successPayload>((resolve) => {
      finishVerify = resolve;
    }));
  const view = render(
    <AuthProvider>
      <ActivationHarness onOpenChange={vi.fn()} onSuccess={vi.fn()} />
    </AuthProvider>,
  );
  await openVerifyStep(user);
  await user.click(screen.getByRole("button", { name: "验证并开启" }));
  expect(await screen.findByRole("dialog", { name: "两步验证可能已启用。未收到的恢复码无法从本次响应恢复。请重新登录，不要重复提交本次启用请求。" })).toBeInTheDocument();
  expect(screen.getByTestId("session-token")).toHaveTextContent("null");
  expect(verify).toHaveBeenCalledTimes(1);

  await user.click(screen.getByRole("button", { name: "关闭" }));
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "两步验证可能已启用。未收到的恢复码无法从本次响应恢复。请重新登录，不要重复提交本次启用请求。" })).not.toBeInTheDocument());
  await user.click(screen.getByRole("button", { name: "打开设置" }));
  expect(await screen.findByText("ENROLL-SECRET")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "下一步" }));
  await user.type(screen.getByLabelText("验证码"), "123456");
  await user.click(screen.getByRole("button", { name: "验证并开启" }));
  await act(async () => {
    screen.getByRole("button", { name: "退出", hidden: true }).click();
  });
  await act(async () => {
    finishVerify(successPayload);
  });
  await waitFor(() => expect(screen.getByTestId("session-token")).toHaveTextContent("null"));
  expect(sessionStorage.getItem("xirang-auth-token")).toBeNull();
  expect(screen.queryByText("code-one")).not.toBeInTheDocument();
  expect(verify).toHaveBeenCalledTimes(2);
  view.unmount();
});

it("logs out when the replacement session cannot be stored", async () => {
  const user = userEvent.setup();
  const original = Storage.prototype.setItem;
  const spy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(function setItem(this: Storage, key: string, value: string) {
    if (value === "replacement-token") throw new Error("quota");
    return original.call(this, key, value);
  });
  try {
    setup.mockResolvedValue({
      secret: "ENROLL-SECRET",
      qrUrl: "",
      enrollmentId: "enrollment-1",
      expiresAt: futureEnrollmentExpiry(),
    });
    verify.mockResolvedValue(successPayload);
    render(
      <AuthProvider>
        <ActivationHarness onOpenChange={vi.fn()} onSuccess={vi.fn()} />
      </AuthProvider>,
    );
    await openVerifyStep(user);
    await user.click(screen.getByRole("button", { name: "验证并开启" }));
    await waitFor(() => expect(screen.getByTestId("session-token")).toHaveTextContent("null"));
    expect(screen.queryByText("code-one")).not.toBeInTheDocument();
    expect(verify).toHaveBeenCalledTimes(1);
  } finally {
    spy.mockRestore();
  }
});

it("keeps recovery codes on a clipboard failure and clears them for a different user", async () => {
  const user = userEvent.setup();
  const writeText = vi.fn().mockRejectedValue(new Error("denied"));
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  setup.mockResolvedValue({
    secret: "ENROLL-SECRET",
    qrUrl: "",
    enrollmentId: "enrollment-1",
    expiresAt: futureEnrollmentExpiry(),
  });
  verify.mockResolvedValue(successPayload);
  render(
    <AuthProvider>
      <ActivationHarness onOpenChange={vi.fn()} onSuccess={vi.fn()} />
    </AuthProvider>,
  );
  await openVerifyStep(user);
  await user.click(screen.getByRole("button", { name: "验证并开启" }));
  expect(await screen.findByText("code-one")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "复制恢复码" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("复制失败。恢复码仍保留在当前页面，请保存后再关闭。");
  expect(screen.getByText("code-one")).toBeInTheDocument();
  expect(screen.getByText("code-two")).toBeInTheDocument();

  await act(async () => {
    screen.getByRole("button", { name: "切换用户", hidden: true }).click();
  });
  await waitFor(() => expect(screen.queryByText("code-one")).not.toBeInTheDocument());
  expect(screen.queryByText("code-two")).not.toBeInTheDocument();
});

function RevivalHarness() {
  const auth = useAuth();
  const login = auth.login;
  useEffect(() => {
    revivalLogin = login;
  }, [login]);
  const [open, setOpen] = useState(false);
  return (
    <div>
      <button type="button" onClick={() => auth.login("token-a", "alice", "admin", 4, false)}>登录甲</button>
      <button type="button" onClick={() => setOpen(true)}>打开甲</button>
      <span data-testid="session-token">{auth.token ?? "null"}</span>
      {auth.token ? (
        <TOTPSetupDialog open={open} onOpenChange={setOpen} token={auth.token} />
      ) : null}
    </div>
  );
}

function switchRevivalUser(...args: Parameters<NonNullable<typeof revivalLogin>>) {
  if (!revivalLogin) {
    throw new Error("revival login was not captured");
  }
  revivalLogin(...args);
}

it("does not revive an in-flight enrollment after the same user returns", async () => {
  const user = userEvent.setup();
  let resolveFirst!: (value: object) => void;
  setup.mockImplementationOnce(() => new Promise((resolve) => {
    resolveFirst = resolve;
  }));
  setup.mockResolvedValue({
    secret: "FRESH-SECRET",
    qrUrl: "",
    enrollmentId: "fresh",
    expiresAt: futureEnrollmentExpiry(),
  });
  render(
    <AuthProvider>
      <RevivalHarness />
    </AuthProvider>,
  );
  await user.click(screen.getByRole("button", { name: "登录甲" }));
  await user.click(screen.getByRole("button", { name: "打开甲" }));
  await waitFor(() => expect(setup).toHaveBeenCalledTimes(1));
  await act(async () => {
    switchRevivalUser("token-b", "bob", "operator", 8, false);
  });
  await act(async () => {
    switchRevivalUser("token-a", "alice", "admin", 4, false);
  });
  expect(await screen.findByText("FRESH-SECRET")).toBeInTheDocument();
  await act(async () => {
    resolveFirst({ secret: "STALE-SECRET", qrUrl: "", enrollmentId: "stale", expiresAt: futureEnrollmentExpiry() });
  });
  expect(screen.queryByText("STALE-SECRET")).not.toBeInTheDocument();
  expect(screen.getByText("FRESH-SECRET")).toBeInTheDocument();
});

it("does not revive recovery codes or a late clipboard result after the same user returns", async () => {
  const user = userEvent.setup();
  let resolveCopy!: () => void;
  const writeText = vi.fn(() => new Promise<void>((resolve) => {
    resolveCopy = resolve;
  }));
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  setup.mockResolvedValue({
    secret: "ENROLL-SECRET",
    qrUrl: "",
    enrollmentId: "enrollment-1",
    expiresAt: futureEnrollmentExpiry(),
  });
  verify.mockResolvedValue(successPayload);
  render(
    <AuthProvider>
      <RevivalHarness />
    </AuthProvider>,
  );
  await user.click(screen.getByRole("button", { name: "登录甲" }));
  await user.click(screen.getByRole("button", { name: "打开甲" }));
  expect(await screen.findByText("ENROLL-SECRET")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "下一步" }));
  await user.type(screen.getByLabelText("验证码"), "123456");
  await user.click(screen.getByRole("button", { name: "验证并开启" }));
  expect(await screen.findByText("code-one")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "复制恢复码" }));
  expect(writeText).toHaveBeenCalledTimes(1);

  await act(async () => {
    switchRevivalUser("token-b", "bob", "operator", 8, false);
  });
  await waitFor(() => expect(screen.queryByText("code-one")).not.toBeInTheDocument());
  await act(async () => {
    switchRevivalUser("token-a", "alice", "admin", 4, false);
  });
  await act(async () => {
    resolveCopy();
  });
  expect(screen.queryByText("code-one")).not.toBeInTheDocument();
  expect(screen.queryByText("code-two")).not.toBeInTheDocument();
  expect(screen.queryByText("已复制")).not.toBeInTheDocument();
  expect(screen.queryByText("复制失败。恢复码仍保留在当前页面，请保存后再关闭。")).not.toBeInTheDocument();
});

it("reuses one setup across StrictMode replay and verifies that enrollment", async () => {
  sessionStorage.setItem("xirang-auth-token", "test-token");
  sessionStorage.setItem("xirang-role", "admin");
  let resolveSetup!: (value: { secret: string; qrUrl: string; enrollmentId: string; expiresAt: string }) => void;
  setup.mockImplementation(() => new Promise((resolve) => {
    resolveSetup = resolve;
  }));
  verify.mockResolvedValue(successPayload);
  const user = userEvent.setup();
  render(
    <AuthProvider>
      <StrictMode>
        <TOTPSetupDialog open onOpenChange={vi.fn()} token="test-token" />
      </StrictMode>
    </AuthProvider>,
  );
  await act(async () => {
    await Promise.resolve();
  });
  expect(setup).toHaveBeenCalledTimes(1);

  await act(async () => {
    resolveSetup({
      secret: "CURRENT-SECRET",
      qrUrl: "",
      enrollmentId: "current-enrollment",
      expiresAt: futureEnrollmentExpiry(),
    });
  });
  expect(setup).toHaveBeenCalledTimes(1);
  expect(await screen.findByText("CURRENT-SECRET")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "下一步" }));
  await user.type(screen.getByLabelText("验证码"), "123456");
  await user.click(screen.getByRole("button", { name: "验证并开启" }));
  expect(await screen.findByText("code-one")).toBeInTheDocument();
  expect(verify).toHaveBeenCalledTimes(1);
  expect(verify).toHaveBeenCalledWith("test-token", "123456", "current-enrollment", expect.any(Number));
});
