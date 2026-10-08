import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

import { SetupWizard } from "./setup-wizard";

const { authRef, requestMock } = vi.hoisted(() => ({
  authRef: { current: { token: null as string | null } },
  requestMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

vi.mock("@/lib/api/core", () => ({
  request: requestMock,
}));

function renderWizard() {
  return render(
    <MemoryRouter>
      <SetupWizard />
    </MemoryRouter>
  );
}

describe("SetupWizard", () => {
  beforeEach(() => {
    authRef.current = { token: null };
    requestMock.mockReset();
    window.localStorage.removeItem("xirang.setup-wizard");
  });

  it("does not call onboarded write API without a token", async () => {
    const user = userEvent.setup();
    renderWizard();

    await waitFor(() => {
      expect(screen.getByRole("dialog")).toBeInTheDocument();
    });
    await user.click(screen.getByRole("button", { name: "Close" }));

    expect(requestMock).not.toHaveBeenCalled();
  });

  it("marks onboarding only when authenticated", async () => {
    const user = userEvent.setup();
    authRef.current = { token: "jwt-token" };
    requestMock.mockResolvedValue(null);

    renderWizard();

    await waitFor(() => {
      expect(screen.getByRole("dialog")).toBeInTheDocument();
    });
    await user.click(screen.getByRole("button", { name: "Close" }));

    expect(requestMock).toHaveBeenCalledWith("/me/onboarded", {
      method: "POST",
      token: "jwt-token",
    });
  });

  it("shows the security preparation note only inside the existing test-backup step", async () => {
    const user = userEvent.setup();
    renderWizard();
    await waitFor(() => {
      expect(screen.getByRole("dialog")).toBeInTheDocument();
    });
    expect(screen.queryByTestId("setup-wizard-security-note")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /Start Setup|开始配置/ }));
    expect(screen.queryByTestId("setup-wizard-security-note")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Next$|^下一步$/ }));
    await user.click(screen.getByRole("button", { name: /^Next$|^下一步$/ }));
    await user.click(screen.getByRole("button", { name: /^Next$|^下一步$/ }));

    expect(screen.getByTestId("setup-wizard-security-note")).toHaveTextContent(/setupWizard\.securityPreparation|two-factor|两步验证/);
    await user.click(screen.getByRole("button", { name: /^Next$|^下一步$/ }));
    expect(screen.queryByTestId("setup-wizard-security-note")).not.toBeInTheDocument();
  });
});
