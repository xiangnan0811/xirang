import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AuthProvider } from "@/context/auth-context";
import { useAuth } from "@/context/auth-context.hooks";
import { ApiError, rememberAuthIdentity } from "@/lib/api/core";
import { setLanguage } from "@/i18n";
import { runAxe } from "@/test/a11y-helpers";
import { RecoveryCodesManager } from "./recovery-codes-manager";

const { remaining, regenerate } = vi.hoisted(() => ({
  remaining: vi.fn(),
  regenerate: vi.fn(),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    recoveryCodesRemaining: remaining,
    regenerateRecoveryCodes: regenerate,
  },
}));

function apiError(status: number, code: string): ApiError {
  const error = new ApiError(status, "safe", { data: { error_code: code } });
  error.httpStatus = status;
  return error;
}

function browserStorageText(): string {
  const chunks = [window.location.href];
  for (const storage of [window.sessionStorage, window.localStorage]) {
    for (let index = 0; index < storage.length; index += 1) {
      const key = storage.key(index);
      if (!key) continue;
      chunks.push(`${key}=${storage.getItem(key) ?? ""}`);
    }
  }
  return chunks.join("\n");
}

const unknownWarning =
  "结果无法确认，旧码可能已失效；未收到的新码无法重新查看。确认仍持有验证器后，可明确再次生成。";
const rejectedRequestWarning =
  "请求无法接受。旧码是否仍然有效无法确认，没有自动重试。";
const rateLimitedWarning =
  "尝试过于频繁。旧码是否仍然有效无法确认，没有自动重试。";

function defer<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

type DeferredClipboard = {
  writeText: (text: string) => Promise<void>;
  succeed: () => void;
  fail: () => void;
};

function installDeferredClipboard(): DeferredClipboard {
  let resolveWrite: () => void = () => {};
  let rejectWrite: (error: unknown) => void = () => {};
  const writeText = vi.fn(() => new Promise<void>((resolve, reject) => {
    resolveWrite = resolve;
    rejectWrite = reject;
  }));
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText },
  });
  return {
    writeText,
    succeed() {
      resolveWrite();
    },
    fail() {
      rejectWrite(new Error("clipboard denied"));
    },
  };
}

function SessionControls() {
  const auth = useAuth();
  return (
    <main>
      <h1>测试页</h1>
      <h2>账户安全</h2>
      <h3>两步验证</h3>
      <button type="button" onClick={() => auth.login("token-a", "ada", "admin", 1, true)}>登录甲</button>
      <button type="button" onClick={() => auth.login("token-b", "bob", "operator", 2, true)}>登录乙</button>
      <button type="button" onClick={() => auth.login("token-a", "ada", "admin", 1, true)}>返回甲</button>
      <button type="button" onClick={() => auth.login("token-a", "ada", "viewer", 1, true)}>更换角色</button>
      <button type="button" onClick={() => auth.logout()}>退出</button>
      <button type="button" onClick={() => auth.setTotpEnabled(false)}>关闭两步验证</button>
      <RecoveryCodesManager />
    </main>
  );
}

function Harness() {
  return (
    <AuthProvider>
      <SessionControls />
    </AuthProvider>
  );
}

beforeEach(() => {
  remaining.mockReset();
  regenerate.mockReset();
  remaining.mockResolvedValue({ remaining: 8 });
  sessionStorage.clear();
  rememberAuthIdentity(null, null);
});

afterEach(async () => {
  cleanup();
  await setLanguage("zh");
});

async function signIn(user: UserEvent, name = "登录甲") {
  await user.click(screen.getByRole("button", { name }));
  expect(await screen.findByRole("button", { name: "重新生成恢复码" })).toBeInTheDocument();
}

async function submitOtp(user: UserEvent, code: string) {
  await user.click(screen.getByRole("button", { name: "重新生成恢复码" }));
  const input = await screen.findByLabelText("当前验证器验证码");
  await user.clear(input);
  await user.type(input, code);
  await user.click(screen.getByRole("button", { name: "重新生成并使旧码失效" }));
}

it("does not show zero when the count cannot be loaded and can retry", async () => {
  const user = userEvent.setup();
  remaining.mockReset();
  remaining.mockRejectedValueOnce(new Error("SECRET-BACKEND-DETAIL"));
  remaining.mockResolvedValueOnce({ remaining: 3 });
  render(<Harness />);
  await signIn(user);

  expect(await screen.findByRole("alert")).toHaveTextContent("无法读取恢复码数量");
  expect(screen.queryByText("恢复码已用尽")).not.toBeInTheDocument();
  expect(screen.queryByText(/剩余/)).not.toBeInTheDocument();
  expect(screen.queryByText("SECRET-BACKEND-DETAIL")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "重新生成恢复码" })).toBeEnabled();
  expect(await runAxe(document.body)).toHaveNoViolations();

  await user.click(screen.getByRole("button", { name: "重试" }));
  expect(await screen.findByText("剩余 3 个恢复码")).toBeInTheDocument();
  expect(screen.queryByText("SECRET-BACKEND-DETAIL")).not.toBeInTheDocument();
});

it("shows an exhausted inventory and plural counts, and refresh rereads", async () => {
  const user = userEvent.setup();
  remaining.mockResolvedValueOnce({ remaining: 0 });
  const zero = render(<Harness />);
  await signIn(user);
  expect(await screen.findByText("恢复码已用尽")).toBeInTheDocument();
  expect(screen.queryByText(/剩余/)).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "重新生成恢复码" })).toBeEnabled();
  zero.unmount();
  sessionStorage.clear();
  rememberAuthIdentity(null, null);

  remaining.mockResolvedValueOnce({ remaining: 1 });
  remaining.mockResolvedValueOnce({ remaining: 2 });
  render(<Harness />);
  await signIn(user);
  expect(await screen.findByText("剩余 1 个恢复码")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "刷新数量" }));
  expect(await screen.findByText("剩余 2 个恢复码")).toBeInTheDocument();
  expect(screen.queryByText("剩余 1 个恢复码")).not.toBeInTheDocument();
  expect(regenerate).not.toHaveBeenCalled();
});

it("uses English plural rules for one and many recovery codes", async () => {
  const user = userEvent.setup();
  await setLanguage("en");
  remaining.mockResolvedValueOnce({ remaining: 1 });
  remaining.mockResolvedValueOnce({ remaining: 2 });
  render(<Harness />);
  await user.click(screen.getByRole("button", { name: "登录甲" }));
  expect(await screen.findByText("1 recovery code remaining")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Refresh count" }));
  expect(await screen.findByText("2 recovery codes remaining")).toBeInTheDocument();
  expect(screen.queryByText("1 recovery code remaining")).not.toBeInTheDocument();
});

it("explains the replacement, restores focus, and cancels without posting", async () => {
  const user = userEvent.setup();
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");

  await user.click(screen.getByRole("button", { name: "重新生成恢复码" }));
  const input = await screen.findByLabelText("当前验证器验证码");
  await waitFor(() => expect(input).toHaveFocus());
  const dialog = screen.getByRole("dialog", { name: "重新生成恢复码" });
  expect(dialog).toHaveAccessibleDescription();
  expect(within(dialog).getByText("新码会使全部旧码失效。", { exact: true })).toBeInTheDocument();
  expect(within(dialog).getByText("新码只在这次操作中显示。请自行保管，关闭后无法再次查看。")).toBeInTheDocument();
  expect(within(dialog).getByText("需要当前验证器中的验证码，恢复码不能代替。")).toBeInTheDocument();
  expect(await runAxe(document.body)).toHaveNoViolations();

  await user.keyboard("{Escape}");
  await waitFor(() => {
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新生成恢复码" })).toHaveFocus();
  });
  expect(regenerate).not.toHaveBeenCalled();

  await user.click(screen.getByRole("button", { name: "重新生成恢复码" }));
  await user.click(await screen.findByRole("button", { name: "取消" }));
  await waitFor(() => {
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新生成恢复码" })).toHaveFocus();
  });
  expect(regenerate).not.toHaveBeenCalled();
});

it("returns an invalid authenticator code to confirmation without another post", async () => {
  const user = userEvent.setup();
  regenerate
    .mockRejectedValueOnce(apiError(403, "RECOVERY_CODE_TOTP_INVALID"))
    .mockResolvedValueOnce({ recoveryCodes: ["retry-one", "retry-two"] });
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "111111");

  expect(await screen.findByRole("alert")).toHaveTextContent("验证码无效");
  expect(screen.getByLabelText("当前验证器验证码")).toHaveValue("");
  expect(screen.getByText("剩余 8 个恢复码")).toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(1);
  await act(async () => {
    await Promise.resolve();
  });
  expect(regenerate).toHaveBeenCalledTimes(1);

  await user.type(screen.getByLabelText("当前验证器验证码"), "222222");
  await user.click(screen.getByRole("button", { name: "重新生成并使旧码失效" }));
  expect(await screen.findByText("retry-one")).toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(2);
  expect(regenerate).toHaveBeenLastCalledWith("token-a", "222222");
});

it("requires a new confirmation after a conflict and does not replay it", async () => {
  const user = userEvent.setup();
  regenerate.mockRejectedValueOnce(apiError(409, "RECOVERY_CODE_CONFLICT"));
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "333333");

  expect(await screen.findByRole("alert")).toHaveTextContent("请重新确认后再生成");
  expect(screen.getByLabelText("当前验证器验证码")).toHaveValue("");
  expect(screen.getByText("剩余 8 个恢复码")).toBeInTheDocument();
  await act(async () => {
    await Promise.resolve();
  });
  expect(regenerate).toHaveBeenCalledTimes(1);
});

it("sends one regenerate request for two synchronous submits and keeps the dialog open", async () => {
  const user = userEvent.setup();
  const pending = defer<{ recoveryCodes: string[] }>();
  regenerate.mockReturnValue(pending.promise);
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await user.click(screen.getByRole("button", { name: "重新生成恢复码" }));
  const input = await screen.findByLabelText("当前验证器验证码");
  await user.type(input, "123456");
  const form = document.getElementById("recovery-codes-regenerate-form") as HTMLFormElement;

  await act(async () => {
    form.requestSubmit();
    form.requestSubmit();
  });

  expect(regenerate).toHaveBeenCalledTimes(1);
  expect(regenerate).toHaveBeenCalledWith("token-a", "123456");
  expect(await screen.findByRole("status")).toHaveTextContent("正在重新生成");
  expect(screen.getByLabelText("当前验证器验证码")).toBeDisabled();
  await user.keyboard("{Escape}");
  expect(screen.getByRole("dialog")).toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(1);

  await act(async () => {
    pending.resolve({ recoveryCodes: ["flight-one", "flight-two"] });
  });
  expect(await screen.findByText("flight-one")).toBeInTheDocument();
});

it("keeps codes when copying fails and clears them only after the save is confirmed", async () => {
  const user = userEvent.setup();
  const writeText = vi.fn()
    .mockRejectedValueOnce(new Error("clipboard denied"))
    .mockResolvedValueOnce(undefined);
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  regenerate.mockResolvedValue({ recoveryCodes: ["copy-one", "copy-two"] });
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "444444");
  expect(await screen.findByText("copy-one")).toBeInTheDocument();
  expect(screen.getByRole("dialog")).toHaveAccessibleDescription();

  await user.click(screen.getByRole("button", { name: "复制恢复码" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("复制失败");
  expect(screen.getByText("copy-one")).toBeInTheDocument();
  expect(screen.getByText("copy-two")).toBeInTheDocument();
  expect(writeText).toHaveBeenCalledWith("copy-one\ncopy-two");

  await user.keyboard("{Escape}");
  expect(screen.getByText("copy-one")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "复制恢复码" }));
  expect(await screen.findByRole("button", { name: "已复制" })).toBeInTheDocument();
  expect(screen.queryByText("复制失败")).not.toBeInTheDocument();
  expect(screen.getByText("copy-two")).toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "我已保存，关闭" }));
  expect(screen.queryByText("copy-one")).not.toBeInTheDocument();
  expect(screen.queryByText("copy-two")).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(browserStorageText()).not.toContain("copy-one");
  expect(browserStorageText()).not.toContain("copy-two");
});

it("does not replay an unknown result and only regenerates again after an explicit confirmation", async () => {
  const user = userEvent.setup();
  remaining.mockResolvedValueOnce({ remaining: 6 });
  remaining.mockResolvedValueOnce({ remaining: 4 });
  regenerate
    .mockRejectedValueOnce(new TypeError("offline"))
    .mockResolvedValueOnce({ recoveryCodes: ["manual-one"] });
  render(<Harness />);
  await signIn(user);
  expect(await screen.findByText("剩余 6 个恢复码")).toBeInTheDocument();
  await submitOtp(user, "555555");

  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByRole("alert")).toHaveTextContent("结果无法确认，旧码可能已失效");
  expect(screen.getAllByText(unknownWarning)).toHaveLength(1);
  expect(screen.getByText("数量已隐藏", { exact: false })).toBeInTheDocument();
  expect(screen.queryByText("剩余 6 个恢复码")).not.toBeInTheDocument();
  expect(screen.queryByDisplayValue("555555")).not.toBeInTheDocument();
  await act(async () => {
    await Promise.resolve();
  });
  expect(regenerate).toHaveBeenCalledTimes(1);

  await user.click(within(dialog).getByRole("button", { name: "刷新数量" }));
  expect(await screen.findByText("剩余 4 个恢复码")).toBeInTheDocument();
  expect(screen.queryByText("剩余 6 个恢复码")).not.toBeInTheDocument();
  expect(within(screen.getByRole("dialog")).getByRole("alert")).toHaveTextContent("结果无法确认");
  expect(screen.getAllByText(unknownWarning)).toHaveLength(1);
  expect(regenerate).toHaveBeenCalledTimes(1);

  await user.click(screen.getByRole("button", { name: "再次生成" }));
  expect(screen.getByLabelText("当前验证器验证码")).toHaveValue("");
  expect(regenerate).toHaveBeenCalledTimes(1);
  await user.type(screen.getByLabelText("当前验证器验证码"), "666666");
  await user.click(screen.getByRole("button", { name: "重新生成并使旧码失效" }));
  expect(await screen.findByText("manual-one")).toBeInTheDocument();
  expect(screen.getByText("剩余 1 个恢复码")).toBeInTheDocument();
  expect(screen.queryByText("剩余 4 个恢复码")).not.toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(2);
});

it("shows a safe hint for a rejected or limited request without replaying it", async () => {
  const user = userEvent.setup();
  regenerate.mockRejectedValueOnce(apiError(400, "REQUEST_INVALID"));
  const view = render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "121212");
  expect(await screen.findByRole("alert")).toHaveTextContent("请求无法接受");
  expect(screen.queryByText("剩余 8 个恢复码")).not.toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(1);
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(screen.getByRole("alert")).toHaveTextContent(rejectedRequestWarning);
  expect(screen.getAllByText(rejectedRequestWarning)).toHaveLength(1);
  expect(screen.queryByText(unknownWarning)).not.toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(1);
  view.unmount();

  regenerate.mockReset();
  regenerate.mockRejectedValueOnce(apiError(429, "RATE_LIMITED"));
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "131313");
  expect(await screen.findByRole("alert")).toHaveTextContent("尝试过于频繁");
  expect(screen.queryByText("剩余 8 个恢复码")).not.toBeInTheDocument();
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(screen.getByRole("alert")).toHaveTextContent(rateLimitedWarning);
  expect(screen.getAllByText(rateLimitedWarning)).toHaveLength(1);
  expect(screen.queryByText(unknownWarning)).not.toBeInTheDocument();
  await act(async () => {
    await Promise.resolve();
  });
  expect(regenerate).toHaveBeenCalledTimes(1);
});

it("closes regeneration when two-factor authentication is disabled or the session is rejected", async () => {
  const user = userEvent.setup();
  regenerate.mockRejectedValueOnce(apiError(409, "RECOVERY_CODE_TOTP_DISABLED"));
  const view = render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "141414");
  expect(await screen.findByRole("alert")).toHaveTextContent("两步验证未启用");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.queryByText("剩余 8 个恢复码")).not.toBeInTheDocument();
  expect(screen.queryByText("恢复码已用尽")).not.toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(1);
  view.unmount();

  regenerate.mockReset();
  regenerate.mockRejectedValueOnce(apiError(401, "UNAUTHENTICATED"));
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "151515");
  expect(await screen.findByRole("alert")).toHaveTextContent("登录已失效");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.queryByText("剩余 8 个恢复码")).not.toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(1);
});

it("does not restore a replaced session or apply its late regeneration", async () => {
  const user = userEvent.setup();
  const pending = defer<{ recoveryCodes: string[] }>();
  regenerate.mockReturnValue(pending.promise);
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  const loginAsB = screen.getByRole("button", { name: "登录乙" });
  await user.click(screen.getByRole("button", { name: "重新生成恢复码" }));
  await user.type(await screen.findByLabelText("当前验证器验证码"), "654321");
  await user.click(screen.getByRole("button", { name: "重新生成并使旧码失效" }));
  expect(await screen.findByRole("status")).toHaveTextContent("正在重新生成");

  fireEvent.click(loginAsB);
  expect(screen.queryByDisplayValue("654321")).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.queryByText("SESSION-A-CODE")).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "返回甲" }));
  expect(screen.queryByDisplayValue("654321")).not.toBeInTheDocument();
  expect(screen.queryByText("SESSION-A-CODE")).not.toBeInTheDocument();
  await act(async () => {
    pending.resolve({ recoveryCodes: ["SESSION-A-CODE"] });
  });
  expect(screen.queryByText("SESSION-A-CODE")).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(1);
  expect(regenerate).toHaveBeenCalledWith("token-a", "654321");
});

it("ignores a regeneration response that arrives after unmount", async () => {
  const user = userEvent.setup();
  const pending = defer<{ recoveryCodes: string[] }>();
  regenerate.mockReturnValue(pending.promise);
  const view = render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "777777");
  expect(regenerate).toHaveBeenCalledTimes(1);
  view.unmount();

  await act(async () => {
    pending.resolve({ recoveryCodes: ["GHOST-CODE"] });
  });
  expect(browserStorageText()).not.toContain("GHOST-CODE");

  render(<Harness />);
  await signIn(user);
  expect(await screen.findByText("剩余 8 个恢复码")).toBeInTheDocument();
  expect(screen.queryByText("GHOST-CODE")).not.toBeInTheDocument();
});

it("clears visible codes when two-factor authentication is turned off and ignores the late response", async () => {
  const user = userEvent.setup();
  const pending = defer<{ recoveryCodes: string[] }>();
  regenerate.mockReturnValue(pending.promise);
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  const disableTotp = screen.getByRole("button", { name: "关闭两步验证" });
  await submitOtp(user, "888888");
  expect(await screen.findByRole("status")).toHaveTextContent("正在重新生成");

  fireEvent.click(disableTotp);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "重新生成恢复码" })).not.toBeInTheDocument();
  expect(screen.queryByText("剩余 8 个恢复码")).not.toBeInTheDocument();

  await act(async () => {
    pending.resolve({ recoveryCodes: ["DISABLED-CODE"] });
  });
  expect(screen.queryByText("DISABLED-CODE")).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(1);
});

it("does not let an older count response overwrite codes that were just generated", async () => {
  const user = userEvent.setup();
  const initial = defer<{ remaining: number }>();
  remaining.mockReset();
  remaining.mockImplementationOnce(() => initial.promise);
  regenerate.mockResolvedValue({ recoveryCodes: ["gen-a", "gen-b"] });
  render(<Harness />);
  await signIn(user);
  await submitOtp(user, "999999");

  expect(await screen.findByText("gen-a")).toBeInTheDocument();
  expect(screen.getByText("剩余 2 个恢复码")).toBeInTheDocument();
  await act(async () => {
    initial.resolve({ remaining: 1 });
  });
  expect(screen.getByText("剩余 2 个恢复码")).toBeInTheDocument();
  expect(screen.queryByText("剩余 1 个恢复码")).not.toBeInTheDocument();
  expect(screen.getByText("gen-b")).toBeInTheDocument();
  expect(remaining).toHaveBeenCalledTimes(1);
  expect(regenerate).toHaveBeenCalledTimes(1);
});

it("keeps the full unconfirmed warning after dismiss and a failed count refresh", async () => {
  const user = userEvent.setup();
  regenerate.mockRejectedValueOnce(new TypeError("offline"));
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "565656");

  expect(await screen.findByRole("alert")).toHaveTextContent(unknownWarning);
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(screen.getByRole("alert")).toHaveTextContent(unknownWarning);
  expect(screen.getAllByText(unknownWarning)).toHaveLength(1);
  expect(screen.getByText(/数量已隐藏/)).toBeInTheDocument();
  expect(screen.queryByText("剩余 8 个恢复码")).not.toBeInTheDocument();
  expect(screen.queryByText("恢复码已用尽")).not.toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(1);

  const failed = defer<{ remaining: number }>();
  remaining.mockImplementationOnce(() => failed.promise);
  await user.click(screen.getByRole("button", { name: "刷新数量" }));
  expect(await screen.findByText("正在读取恢复码数量。")).toBeInTheDocument();
  expect(screen.getByRole("alert")).toHaveTextContent(unknownWarning);
  expect(screen.queryByText("剩余 8 个恢复码")).not.toBeInTheDocument();
  expect(screen.queryByText("恢复码已用尽")).not.toBeInTheDocument();

  await act(async () => {
    failed.reject(new Error("SECRET-REFRESH"));
  });
  expect(await screen.findByText("无法读取恢复码数量。")).toBeInTheDocument();
  expect(screen.getByText(unknownWarning)).toBeInTheDocument();
  expect(screen.queryByText("SECRET-REFRESH")).not.toBeInTheDocument();
  expect(screen.queryByText("剩余 8 个恢复码")).not.toBeInTheDocument();
  expect(screen.queryByText("恢复码已用尽")).not.toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(1);

  remaining.mockResolvedValueOnce({ remaining: 3 });
  await user.click(screen.getByRole("button", { name: "重试" }));
  expect(await screen.findByText("剩余 3 个恢复码")).toBeInTheDocument();
  expect(screen.getByRole("alert")).toHaveTextContent(unknownWarning);
  expect(screen.getByText("重新读取的数量只能说明当前库存，不能证明未显示的新码仍然有效。")).toBeInTheDocument();
  expect(screen.queryByText("剩余 8 个恢复码")).not.toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(1);
});

it("shows the prior-attempt warning once when regeneration is explicitly retried", async () => {
  const user = userEvent.setup();
  regenerate
    .mockRejectedValueOnce(new TypeError("offline"))
    .mockResolvedValueOnce({ recoveryCodes: ["again-one"] });
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "575757");

  await user.click(await screen.findByRole("button", { name: "再次生成" }));
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByText(unknownWarning)).toBeInTheDocument();
  expect(screen.getAllByText(unknownWarning)).toHaveLength(1);
  expect(within(dialog).getByText("新码会使全部旧码失效。", { exact: true })).toBeInTheDocument();
  expect(within(dialog).getByText("需要当前验证器中的验证码，恢复码不能代替。")).toBeInTheDocument();
  expect(screen.getByLabelText("当前验证器验证码")).toHaveValue("");
  expect(regenerate).toHaveBeenCalledTimes(1);

  await user.type(screen.getByLabelText("当前验证器验证码"), "585858");
  await user.click(screen.getByRole("button", { name: "重新生成并使旧码失效" }));
  expect(await screen.findByText("again-one")).toBeInTheDocument();
  expect(screen.queryByText(unknownWarning)).not.toBeInTheDocument();
  expect(screen.getByText("剩余 1 个恢复码")).toBeInTheDocument();
  expect(regenerate).toHaveBeenCalledTimes(2);
  expect(regenerate).toHaveBeenLastCalledWith("token-a", "585858");
});

async function copyVisibleCodes(user: UserEvent, clipboard: DeferredClipboard) {
  await user.click(screen.getByRole("button", { name: "复制恢复码" }));
  await waitFor(() => expect(clipboard.writeText).toHaveBeenCalledTimes(1));
}

it("drops deferred clipboard success and failure after the codes dialog closes", async () => {
  const user = userEvent.setup();
  const clipboard = installDeferredClipboard();
  regenerate.mockResolvedValueOnce({ recoveryCodes: ["close-one", "close-two"] });
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "424242");
  expect(await screen.findByText("close-one")).toBeInTheDocument();
  await copyVisibleCodes(user, clipboard);
  await user.click(screen.getByRole("button", { name: "我已保存，关闭" }));
  expect(screen.queryByText("close-one")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "复制恢复码" })).not.toBeInTheDocument();

  regenerate.mockResolvedValueOnce({ recoveryCodes: ["close-next"] });
  await submitOtp(user, "424243");
  expect(await screen.findByText("close-next")).toBeInTheDocument();
  await act(async () => {
    clipboard.succeed();
  });
  expect(screen.getByText("close-next")).toBeInTheDocument();
  expect(screen.queryByText("close-one")).not.toBeInTheDocument();
  expect(screen.queryByText("close-two")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "复制恢复码" })).toBeInTheDocument();
  expect(screen.queryByText("已复制")).not.toBeInTheDocument();
  expect(screen.queryByText(/复制失败/)).not.toBeInTheDocument();
  expect(clipboard.writeText).toHaveBeenCalledTimes(1);
  expect(clipboard.writeText).toHaveBeenCalledWith("close-one\nclose-two");
  expect(browserStorageText()).not.toContain("close-one");
});

it("drops deferred clipboard failure after close and does not restore the secret", async () => {
  const user = userEvent.setup();
  const clipboard = installDeferredClipboard();
  regenerate.mockResolvedValueOnce({ recoveryCodes: ["fail-one", "fail-two"] });
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  await submitOtp(user, "434343");
  expect(await screen.findByText("fail-one")).toBeInTheDocument();
  await copyVisibleCodes(user, clipboard);
  await user.click(screen.getByRole("button", { name: "我已保存，关闭" }));
  expect(screen.queryByText("fail-one")).not.toBeInTheDocument();

  regenerate.mockResolvedValueOnce({ recoveryCodes: ["fail-next"] });
  await submitOtp(user, "434344");
  expect(await screen.findByText("fail-next")).toBeInTheDocument();
  await act(async () => {
    clipboard.fail();
  });
  expect(screen.getByText("fail-next")).toBeInTheDocument();
  expect(screen.queryByText("fail-one")).not.toBeInTheDocument();
  expect(screen.queryByText("fail-two")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "复制恢复码" })).toBeInTheDocument();
  expect(screen.queryByText(/复制失败/)).not.toBeInTheDocument();
  expect(screen.queryByText("已复制")).not.toBeInTheDocument();
  expect(clipboard.writeText).toHaveBeenCalledTimes(1);
  expect(clipboard.writeText).toHaveBeenCalledWith("fail-one\nfail-two");
  expect(browserStorageText()).not.toContain("fail-one");
});

it("drops deferred clipboard success and failure after logout", async () => {
  const user = userEvent.setup();
  const success = installDeferredClipboard();
  regenerate.mockResolvedValueOnce({ recoveryCodes: ["bye-one", "bye-two"] });
  const view = render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  const logoutSuccess = screen.getByRole("button", { name: "退出" });
  await submitOtp(user, "444445");
  expect(await screen.findByText("bye-one")).toBeInTheDocument();
  await copyVisibleCodes(user, success);
  fireEvent.click(logoutSuccess);
  expect(screen.queryByText("bye-one")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "复制恢复码" })).not.toBeInTheDocument();

  await signIn(user);
  regenerate.mockResolvedValueOnce({ recoveryCodes: ["bye-next"] });
  await submitOtp(user, "444447");
  expect(await screen.findByText("bye-next")).toBeInTheDocument();
  await act(async () => {
    success.succeed();
  });
  expect(screen.getByText("bye-next")).toBeInTheDocument();
  expect(screen.queryByText("bye-one")).not.toBeInTheDocument();
  expect(screen.queryByText("bye-two")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "复制恢复码" })).toBeInTheDocument();
  expect(screen.queryByText("已复制")).not.toBeInTheDocument();
  expect(screen.queryByText(/复制失败/)).not.toBeInTheDocument();
  expect(success.writeText).toHaveBeenCalledTimes(1);
  expect(success.writeText).toHaveBeenCalledWith("bye-one\nbye-two");
  expect(browserStorageText()).not.toContain("bye-one");
  view.unmount();

  sessionStorage.clear();
  rememberAuthIdentity(null, null);
  const failure = installDeferredClipboard();
  regenerate.mockResolvedValueOnce({ recoveryCodes: ["bye-fail", "bye-fail-two"] });
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  const logoutFailure = screen.getByRole("button", { name: "退出" });
  await submitOtp(user, "444446");
  expect(await screen.findByText("bye-fail")).toBeInTheDocument();
  await copyVisibleCodes(user, failure);
  fireEvent.click(logoutFailure);
  expect(screen.queryByText("bye-fail")).not.toBeInTheDocument();
  await signIn(user);
  regenerate.mockResolvedValueOnce({ recoveryCodes: ["bye-fail-next"] });
  await submitOtp(user, "444448");
  expect(await screen.findByText("bye-fail-next")).toBeInTheDocument();
  await act(async () => {
    failure.fail();
  });
  expect(screen.getByText("bye-fail-next")).toBeInTheDocument();
  expect(screen.queryByText("bye-fail")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "复制恢复码" })).toBeInTheDocument();
  expect(screen.queryByText(/复制失败/)).not.toBeInTheDocument();
  expect(screen.queryByText("已复制")).not.toBeInTheDocument();
  expect(failure.writeText).toHaveBeenCalledTimes(1);
  expect(failure.writeText).toHaveBeenCalledWith("bye-fail\nbye-fail-two");
  expect(browserStorageText()).not.toContain("bye-fail");
});

it("drops deferred clipboard success and failure after an identity change", async () => {
  const user = userEvent.setup();
  const success = installDeferredClipboard();
  regenerate.mockResolvedValueOnce({ recoveryCodes: ["swap-one", "swap-two"] });
  const view = render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  const changeIdentitySuccess = screen.getByRole("button", { name: "登录乙" });
  const restoreIdentitySuccess = screen.getByRole("button", { name: "返回甲" });
  await submitOtp(user, "454545");
  expect(await screen.findByText("swap-one")).toBeInTheDocument();
  await copyVisibleCodes(user, success);
  fireEvent.click(changeIdentitySuccess);
  expect(screen.queryByText("swap-one")).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "复制恢复码" })).not.toBeInTheDocument();

  regenerate.mockResolvedValueOnce({ recoveryCodes: ["swap-next"] });
  await submitOtp(user, "454547");
  expect(await screen.findByText("swap-next")).toBeInTheDocument();
  await act(async () => {
    success.succeed();
  });
  expect(screen.getByText("swap-next")).toBeInTheDocument();
  expect(screen.queryByText("swap-one")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "复制恢复码" })).toBeInTheDocument();
  expect(screen.queryByText("已复制")).not.toBeInTheDocument();
  expect(success.writeText).toHaveBeenCalledTimes(1);
  expect(success.writeText).toHaveBeenCalledWith("swap-one\nswap-two");

  fireEvent.click(restoreIdentitySuccess);
  expect(screen.queryByText("swap-one")).not.toBeInTheDocument();
  expect(screen.queryByText("swap-next")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "复制恢复码" })).not.toBeInTheDocument();
  expect(screen.queryByText("已复制")).not.toBeInTheDocument();
  expect(success.writeText).toHaveBeenCalledTimes(1);
  expect(browserStorageText()).not.toContain("swap-one");
  view.unmount();

  sessionStorage.clear();
  rememberAuthIdentity(null, null);
  const failure = installDeferredClipboard();
  regenerate.mockResolvedValueOnce({ recoveryCodes: ["swap-fail", "swap-fail-two"] });
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  const changeIdentityFailure = screen.getByRole("button", { name: "登录乙" });
  const restoreIdentityFailure = screen.getByRole("button", { name: "返回甲" });
  await submitOtp(user, "454546");
  expect(await screen.findByText("swap-fail")).toBeInTheDocument();
  await copyVisibleCodes(user, failure);
  fireEvent.click(changeIdentityFailure);
  expect(screen.queryByText("swap-fail")).not.toBeInTheDocument();
  regenerate.mockResolvedValueOnce({ recoveryCodes: ["swap-fail-next"] });
  await submitOtp(user, "454548");
  expect(await screen.findByText("swap-fail-next")).toBeInTheDocument();
  await act(async () => {
    failure.fail();
  });
  expect(screen.getByText("swap-fail-next")).toBeInTheDocument();
  expect(screen.queryByText("swap-fail")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "复制恢复码" })).toBeInTheDocument();
  expect(screen.queryByText(/复制失败/)).not.toBeInTheDocument();
  expect(screen.queryByText("已复制")).not.toBeInTheDocument();
  expect(failure.writeText).toHaveBeenCalledTimes(1);
  expect(failure.writeText).toHaveBeenCalledWith("swap-fail\nswap-fail-two");

  fireEvent.click(restoreIdentityFailure);
  expect(screen.queryByText("swap-fail")).not.toBeInTheDocument();
  expect(screen.queryByText("swap-fail-next")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "复制恢复码" })).not.toBeInTheDocument();
  expect(failure.writeText).toHaveBeenCalledTimes(1);
  expect(browserStorageText()).not.toContain("swap-fail");
});

it("drops deferred clipboard success and failure after a role or generation change", async () => {
  const user = userEvent.setup();
  const success = installDeferredClipboard();
  regenerate.mockResolvedValueOnce({ recoveryCodes: ["gen-one", "gen-two"] });
  const view = render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  const changeGeneration = screen.getByRole("button", { name: "返回甲" });
  await submitOtp(user, "464646");
  expect(await screen.findByText("gen-one")).toBeInTheDocument();
  await copyVisibleCodes(user, success);
  fireEvent.click(changeGeneration);
  expect(screen.queryByText("gen-one")).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "复制恢复码" })).not.toBeInTheDocument();

  regenerate.mockResolvedValueOnce({ recoveryCodes: ["gen-next"] });
  await submitOtp(user, "464648");
  expect(await screen.findByText("gen-next")).toBeInTheDocument();
  await act(async () => {
    success.succeed();
  });
  expect(screen.getByText("gen-next")).toBeInTheDocument();
  expect(screen.queryByText("gen-one")).not.toBeInTheDocument();
  expect(screen.queryByText("gen-two")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "复制恢复码" })).toBeInTheDocument();
  expect(screen.queryByText("已复制")).not.toBeInTheDocument();
  expect(success.writeText).toHaveBeenCalledTimes(1);
  expect(success.writeText).toHaveBeenCalledWith("gen-one\ngen-two");
  expect(browserStorageText()).not.toContain("gen-one");
  view.unmount();

  sessionStorage.clear();
  rememberAuthIdentity(null, null);
  const failure = installDeferredClipboard();
  regenerate.mockResolvedValueOnce({ recoveryCodes: ["role-one", "role-two"] });
  render(<Harness />);
  await signIn(user);
  await screen.findByText("剩余 8 个恢复码");
  const changeRole = screen.getByRole("button", { name: "更换角色" });
  const restoreRole = screen.getByRole("button", { name: "返回甲" });
  await submitOtp(user, "464647");
  expect(await screen.findByText("role-one")).toBeInTheDocument();
  await copyVisibleCodes(user, failure);
  fireEvent.click(changeRole);
  expect(screen.queryByText("role-one")).not.toBeInTheDocument();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "复制恢复码" })).not.toBeInTheDocument();

  regenerate.mockResolvedValueOnce({ recoveryCodes: ["role-next"] });
  await submitOtp(user, "464649");
  expect(await screen.findByText("role-next")).toBeInTheDocument();
  await act(async () => {
    failure.fail();
  });
  expect(screen.getByText("role-next")).toBeInTheDocument();
  expect(screen.queryByText("role-one")).not.toBeInTheDocument();
  expect(screen.queryByText("role-two")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "复制恢复码" })).toBeInTheDocument();
  expect(screen.queryByText(/复制失败/)).not.toBeInTheDocument();
  expect(screen.queryByText("已复制")).not.toBeInTheDocument();
  expect(failure.writeText).toHaveBeenCalledTimes(1);
  expect(failure.writeText).toHaveBeenCalledWith("role-one\nrole-two");

  fireEvent.click(restoreRole);
  expect(screen.queryByText("role-one")).not.toBeInTheDocument();
  expect(screen.queryByText("role-next")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "复制恢复码" })).not.toBeInTheDocument();
  expect(failure.writeText).toHaveBeenCalledTimes(1);
  expect(browserStorageText()).not.toContain("role-one");
});
