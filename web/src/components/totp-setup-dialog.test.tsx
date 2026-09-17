import { act, render, screen } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { TOTPSetupDialog } from "./totp-setup-dialog";

const { setup } = vi.hoisted(() => ({ setup: vi.fn() }));
vi.mock("@/lib/api/client", () => ({
  apiClient: { totpSetup: setup },
  ApiError: class extends Error {},
}));

beforeEach(() => setup.mockReset());

it("discards an already loaded enrollment when the parent closes the dialog", async () => {
  setup.mockResolvedValueOnce({ secret: "OLD-LOADED-SECRET", qrUrl: "", enrollmentId: "old", expiresAt: "2099-01-01" });
  setup.mockResolvedValueOnce({ secret: "FRESH-LOADED-SECRET", qrUrl: "", enrollmentId: "new", expiresAt: "2099-01-01" });
  const props = { onOpenChange: vi.fn(), token: "test-token" };
  const view = render(<TOTPSetupDialog open {...props} />);
  expect(await screen.findByText("OLD-LOADED-SECRET")).toBeInTheDocument();
  view.rerender(<TOTPSetupDialog open={false} {...props} />);
  view.rerender(<TOTPSetupDialog open {...props} />);
  expect(screen.queryByText("OLD-LOADED-SECRET")).not.toBeInTheDocument();
  expect(await screen.findByText("FRESH-LOADED-SECRET")).toBeInTheDocument();
});

it("starts a fresh enrollment on controlled reopen and ignores the old pending secret", async () => {
  let finishOld!: (value: object) => void;
  setup.mockReturnValueOnce(new Promise((resolve) => { finishOld = resolve; }));
  setup.mockResolvedValueOnce({ secret: "NEW-TEST-SECRET", qrUrl: "", enrollmentId: "new", expiresAt: "2099-01-01" });
  const props = { onOpenChange: vi.fn(), token: "test-token" };
  const view = render(<TOTPSetupDialog open {...props} />);
  view.rerender(<TOTPSetupDialog open={false} {...props} />);
  view.rerender(<TOTPSetupDialog open {...props} />);
  expect(await screen.findByText("NEW-TEST-SECRET")).toBeInTheDocument();
  await act(async () => {
    finishOld({ secret: "OLD-TEST-SECRET", qrUrl: "", enrollmentId: "old", expiresAt: "2099-01-01" });
  });
  expect(screen.queryByText("OLD-TEST-SECRET")).not.toBeInTheDocument();
  expect(screen.getByText("NEW-TEST-SECRET")).toBeInTheDocument();
  expect(setup).toHaveBeenCalledTimes(2);
});
