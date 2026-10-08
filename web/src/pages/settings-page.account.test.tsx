import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { AccountTab } from "./settings-page.account";

const { setTotpEnabledMock, dialogProps } = vi.hoisted(() => ({
  setTotpEnabledMock: vi.fn(),
  dialogProps: {
    setup: null as { onSuccess?: () => void; open: boolean } | null,
    disable: null as { onSuccess?: () => void } | null,
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: "token-1",
    username: "ada",
    role: "admin",
    totpEnabled: false,
    setTotpEnabled: setTotpEnabledMock,
  }),
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

function renderAccount(securityReturnTo?: unknown, includeState = true) {
  dialogProps.setup = null;
  dialogProps.disable = null;
  setTotpEnabledMock.mockClear();
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
