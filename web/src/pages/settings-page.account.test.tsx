import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { rememberAuthIdentity } from "@/lib/api/core";
import { AccountTab } from "./settings-page.account";

const { setTotpEnabledMock, dialogProps, authHarness, recoveryApi } = vi.hoisted(() => {
  const setTotpEnabledMock = vi.fn();
  return {
    setTotpEnabledMock,
    dialogProps: {
      setup: null as { onSuccess?: () => void; open: boolean } | null,
      disable: null as { onSuccess?: () => void } | null,
    },
    authHarness: {
      token: "token-1" as string | null,
      username: "ada" as string | null,
      role: "admin" as "admin" | "operator" | "viewer" | null,
      totpEnabled: false,
      setTotpEnabled: setTotpEnabledMock,
    },
    recoveryApi: {
      changePassword: vi.fn(),
      recoveryCodesRemaining: vi.fn(),
      regenerateRecoveryCodes: vi.fn(),
    },
  };
});

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authHarness,
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: recoveryApi,
}));

vi.mock("@/components/totp-setup-dialog", () => ({
  TOTPSetupDialog: (props: { onSuccess?: () => void; open: boolean }) => {
    dialogProps.setup = props;
    return null;
  },
}));

vi.mock("@/components/totp-disable-dialog", () => ({
  TOTPDisableDialog: (props: { onSuccess?: () => void }) => {
    dialogProps.disable = props;
    return null;
  },
}));

function PathProbe() {
  const location = useLocation();
  return <div data-testid="path">{`${location.pathname}|${location.search}|${location.hash}`}</div>;
}

beforeEach(() => {
  authHarness.token = "token-1";
  authHarness.username = "ada";
  authHarness.role = "admin";
  authHarness.totpEnabled = false;
  rememberAuthIdentity("token-1", "admin");
  recoveryApi.changePassword.mockReset();
  recoveryApi.recoveryCodesRemaining.mockReset();
  recoveryApi.regenerateRecoveryCodes.mockReset();
  recoveryApi.recoveryCodesRemaining.mockResolvedValue({ remaining: 8 });
});

function renderAccount(securityReturnTo?: unknown, includeState = true, totpEnabled = false) {
  dialogProps.setup = null;
  dialogProps.disable = null;
  setTotpEnabledMock.mockClear();
  authHarness.totpEnabled = totpEnabled;
  rememberAuthIdentity(authHarness.token, authHarness.role);
  return render(
    <MemoryRouter initialEntries={[{
      pathname: "/app/settings",
      state: includeState ? { securityReturnTo } : null,
    }]}>
      <PathProbe />
      <Routes>
        <Route path="/app/settings" element={<AccountTab />} />
        <Route path="/app/nodes" element={<div>nodes-page</div>} />
        <Route path="/app/tasks" element={<div>tasks-page</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

const returnButton = () => screen.queryByRole("button", {
  name: /stepUp\.returnToOperation|返回操作|Return to operation/,
});

describe("AccountTab security return", () => {
  it("navigates only to an allowlisted path and does not replay the operation", async () => {
    const user = userEvent.setup();
    renderAccount("/app/nodes?token=secret#hash");

    expect(dialogProps.setup?.onSuccess).toBeUndefined();
    expect(dialogProps.setup?.open).toBe(false);
    dialogProps.disable?.onSuccess?.();
    expect(setTotpEnabledMock).toHaveBeenCalledTimes(1);
    expect(setTotpEnabledMock).toHaveBeenCalledWith(false);

    await user.click(returnButton()!);

    expect(screen.getByTestId("path").textContent).toBe("/app/nodes||");
    expect(screen.getByText("nodes-page")).toBeInTheDocument();
    expect(setTotpEnabledMock).toHaveBeenCalledTimes(1);
    expect(dialogProps.setup?.open).toBe(false);
  });

  it("hides the return button for missing or disallowed targets", () => {
    const { unmount } = renderAccount(undefined, false);
    expect(returnButton()).not.toBeInTheDocument();
    unmount();

    for (const target of ["/app/evil", "/app/settings/account", "/app/nodes/1", 12, null]) {
      const view = renderAccount(target);
      expect(returnButton()).not.toBeInTheDocument();
      view.unmount();
    }
  });
});

describe("AccountTab recovery codes", () => {
  it("loads a personal count only while two-factor authentication is enabled", async () => {
    const disabled = renderAccount();
    expect(screen.queryByRole("button", { name: "重新生成恢复码" })).not.toBeInTheDocument();
    expect(screen.queryByText("恢复码已用尽")).not.toBeInTheDocument();
    expect(recoveryApi.recoveryCodesRemaining).not.toHaveBeenCalled();
    disabled.unmount();

    renderAccount(undefined, true, true);
    expect(await screen.findByText("剩余 8 个恢复码")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "恢复码" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "刷新数量" })).toBeEnabled();
    expect(recoveryApi.recoveryCodesRemaining).toHaveBeenCalledTimes(1);
    expect(recoveryApi.recoveryCodesRemaining).toHaveBeenCalledWith("token-1");
  });

  it("requires an explicit confirmation and does not treat a count failure as zero", async () => {
    const user = userEvent.setup();
    recoveryApi.recoveryCodesRemaining.mockReset();
    recoveryApi.recoveryCodesRemaining.mockRejectedValueOnce(new Error("SECRET-ACCOUNT-COUNT"));
    renderAccount(undefined, true, true);
    expect(await screen.findByRole("alert")).toHaveTextContent("无法读取恢复码数量");
    expect(screen.queryByText("恢复码已用尽")).not.toBeInTheDocument();
    expect(screen.queryByText(/剩余/)).not.toBeInTheDocument();
    expect(screen.queryByText("SECRET-ACCOUNT-COUNT")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新生成恢复码" })).toBeEnabled();

    recoveryApi.recoveryCodesRemaining.mockResolvedValueOnce({ remaining: 8 });
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("剩余 8 个恢复码")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重新生成恢复码" }));
    expect(await screen.findByText("新码会使全部旧码失效。", { exact: true })).toBeInTheDocument();
    expect(screen.getByText("需要当前验证器中的验证码，恢复码不能代替。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(recoveryApi.regenerateRecoveryCodes).not.toHaveBeenCalled();
  });

  it("replaces the visible inventory after regeneration and removes codes when saving is confirmed", async () => {
    const user = userEvent.setup();
    recoveryApi.regenerateRecoveryCodes.mockResolvedValue({ recoveryCodes: ["acct-one", "acct-two"] });
    renderAccount(undefined, true, true);
    expect(await screen.findByText("剩余 8 个恢复码")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重新生成恢复码" }));
    await user.type(await screen.findByLabelText("当前验证器验证码"), " 246810 ");
    await user.click(screen.getByRole("button", { name: "重新生成并使旧码失效" }));

    expect(await screen.findByText("acct-one")).toBeInTheDocument();
    expect(screen.getByText("acct-two")).toBeInTheDocument();
    expect(screen.getByText("剩余 2 个恢复码")).toBeInTheDocument();
    expect(screen.queryByText("剩余 8 个恢复码")).not.toBeInTheDocument();
    expect(recoveryApi.regenerateRecoveryCodes).toHaveBeenCalledTimes(1);
    expect(recoveryApi.regenerateRecoveryCodes).toHaveBeenCalledWith("token-1", "246810");
    const stored = [window.location.href];
    for (const storage of [window.sessionStorage, window.localStorage]) {
      for (let index = 0; index < storage.length; index += 1) {
        const key = storage.key(index);
        if (key) stored.push(`${key}=${storage.getItem(key) ?? ""}`);
      }
    }
    expect(stored.join("\n")).not.toContain("acct-one");
    expect(stored.join("\n")).not.toContain("acct-two");

    await user.click(screen.getByRole("button", { name: "我已保存，关闭" }));
    expect(screen.queryByText("acct-one")).not.toBeInTheDocument();
    expect(screen.queryByText("acct-two")).not.toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByText("剩余 2 个恢复码")).toBeInTheDocument();
  });
});
