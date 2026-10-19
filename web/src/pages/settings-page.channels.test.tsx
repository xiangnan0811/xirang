import "@testing-library/jest-dom/vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { AuthContext, type AuthContextValue, type AuthRole } from "@/context/auth-context.shared";
import {
  IntegrationsContext,
  type IntegrationsContextValue,
} from "@/context/integrations-context.shared";
import {
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  isAuthTransitionActive,
  rememberAuthIdentity,
} from "@/lib/api/core";
import { toast } from "@/components/ui/toast-sonner";
import type { IntegrationChannel } from "@/types/domain";
import { ChannelsTab } from "./settings-page.channels";

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

// Selectors use the zh copy the test setup installs, via the same i18n keys the components render.
const ADD = i18n.t("notifications.addIntegration");
const EDIT = i18n.t("common.edit");
const CANCEL = i18n.t("common.cancel");
const CONFIRM = i18n.t("common.confirm");
const TEST_SEND = i18n.t("notifications.testSend");
const NAME_FIELD = i18n.t("integration.channelName");
const EMAIL_FIELD = i18n.t("integration.endpointLabels.email");
const SUBMIT_CREATE = i18n.t("integration.submitCreate");
const SUBMIT_EDIT = i18n.t("integration.submitEdit");

const mockChannel: IntegrationChannel = {
  id: "ch-1",
  type: "webhook",
  name: "Alert webhook",
  endpoint: "https://hooks.example.test/alerts",
  hasSecret: false,
  enabled: true,
  failThreshold: 2,
  cooldownMinutes: 5,
  proxyUrl: "",
};

const DELETE = i18n.t("notifications.deleteIntegrationAriaLabel", { name: mockChannel.name });

function createAuthContextValue(token = "test-token", role: AuthRole = "admin"): AuthContextValue {
  return {
    token,
    role,
    username: "admin",
    userId: 1,
    totpEnabled: false,
    isAuthenticated: !!token,
    authTransitioning: false,
    beginTOTPActivation: vi.fn(),
    abortTOTPActivation: vi.fn(),
    completeTOTPActivation: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    setTotpEnabled: vi.fn(),
    ensureStepUpProof: vi.fn().mockResolvedValue("test-proof"),
    clearStepUpProof: vi.fn(),
  };
}

function renderChannelsTab(
  integrationsContextOverrides: Partial<IntegrationsContextValue> = {},
  authValue: AuthContextValue = createAuthContextValue()
) {
  // The provider installs Context and the core snapshot together; owner guards compare both.
  rememberAuthIdentity(authValue.token, authValue.role);

  const integrationsContextValue: IntegrationsContextValue = {
    integrations: [mockChannel],
    refreshIntegrations: vi.fn().mockResolvedValue(undefined),
    addIntegration: vi.fn().mockResolvedValue(undefined),
    removeIntegration: vi.fn().mockResolvedValue(undefined),
    toggleIntegration: vi.fn().mockResolvedValue(undefined),
    updateIntegration: vi.fn().mockResolvedValue(undefined),
    patchIntegration: vi.fn().mockResolvedValue(undefined),
    testIntegration: vi.fn().mockResolvedValue({ ok: true, message: "OK", latencyMs: 42 }),
    ...integrationsContextOverrides,
  };

  const view = render(
    <AuthContext.Provider value={authValue}>
      <IntegrationsContext.Provider value={integrationsContextValue}>
        <I18nextProvider i18n={i18n}>
          <ChannelsTab />
        </I18nextProvider>
      </IntegrationsContext.Provider>
    </AuthContext.Provider>
  );

  return {
    ...view,
    integrationsContextValue,
  };
}

/** Every write the tab can reach through the shared integrations context. */
function writeCalls(value: IntegrationsContextValue) {
  return [
    value.addIntegration,
    value.removeIntegration,
    value.toggleIntegration,
    value.updateIntegration,
    value.patchIntegration,
    value.testIntegration,
  ].reduce((count, fn) => count + vi.mocked(fn).mock.calls.length, 0);
}

async function openCreate(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: ADD }));
  return screen.findByRole("dialog");
}

async function openEdit(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: EDIT }));
  return screen.findByRole("dialog");
}

describe("ChannelsTab integration and reopen boundaries", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    if (isAuthTransitionActive()) clearAuthTransitionBarrier();
    bumpAuthSessionGeneration();
    rememberAuthIdentity("test-token", "admin");
  });

  it("submits a valid create once and closes the dialog", async () => {
    const user = userEvent.setup();
    const addIntegration = vi.fn().mockResolvedValue(undefined);
    renderChannelsTab({ addIntegration });

    const dialog = await openCreate(user);
    await user.type(within(dialog).getByLabelText(NAME_FIELD), "New Email Channel");
    await user.type(within(dialog).getByLabelText(EMAIL_FIELD), "oncall@example.com");
    await user.click(within(dialog).getByRole("button", { name: SUBMIT_CREATE }));

    await waitFor(() => expect(addIntegration).toHaveBeenCalledTimes(1));
    expect(addIntegration).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "New Email Channel",
        endpoint: "oncall@example.com",
        type: "email",
      })
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(toast.success).toHaveBeenCalledTimes(1);
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("submits a valid edit once with the edited name and closes the dialog", async () => {
    const user = userEvent.setup();
    const patchIntegration = vi.fn().mockResolvedValue(undefined);
    renderChannelsTab({ patchIntegration });

    const dialog = await openEdit(user);
    const nameInput = within(dialog).getByLabelText(NAME_FIELD);
    await user.clear(nameInput);
    await user.type(nameInput, "Updated webhook");
    await user.click(within(dialog).getByRole("button", { name: SUBMIT_EDIT }));

    await waitFor(() => expect(patchIntegration).toHaveBeenCalledTimes(1));
    expect(patchIntegration).toHaveBeenCalledWith("ch-1", expect.objectContaining({ name: "Updated webhook" }));
    // The endpoint was not touched, so it is not resent.
    expect(patchIntegration.mock.calls[0]?.[1]).not.toHaveProperty("endpoint");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(toast.success).toHaveBeenCalledTimes(1);
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("does zero writes when a filled create draft is cancelled", async () => {
    const user = userEvent.setup();
    const { integrationsContextValue } = renderChannelsTab();

    const dialog = await openCreate(user);
    await user.type(within(dialog).getByLabelText(NAME_FIELD), "Draft only");
    await user.type(within(dialog).getByLabelText(EMAIL_FIELD), "draft@example.com");
    await user.click(within(dialog).getByRole("button", { name: CANCEL }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(writeCalls(integrationsContextValue)).toBe(0);
    expect(toast.success).not.toHaveBeenCalled();

    // Reopening starts from a clean draft rather than the cancelled one.
    const reopened = await openCreate(user);
    expect(within(reopened).getByLabelText(NAME_FIELD)).toHaveValue("");
  });

  it("does zero writes when an edited draft is cancelled", async () => {
    const user = userEvent.setup();
    const { integrationsContextValue } = renderChannelsTab();

    const dialog = await openEdit(user);
    const nameInput = within(dialog).getByLabelText(NAME_FIELD);
    await user.clear(nameInput);
    await user.type(nameInput, "Unsaved rename");
    await user.click(within(dialog).getByRole("button", { name: CANCEL }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(writeCalls(integrationsContextValue)).toBe(0);
    expect(toast.success).not.toHaveBeenCalled();

    const reopened = await openEdit(user);
    expect(within(reopened).getByLabelText(NAME_FIELD)).toHaveValue(mockChannel.name);
  });

  it("does zero writes when deletion is cancelled", async () => {
    const user = userEvent.setup();
    const { integrationsContextValue } = renderChannelsTab();

    await user.click(screen.getByRole("button", { name: DELETE }));
    const confirmDialog = await screen.findByRole("alertdialog");
    await user.click(within(confirmDialog).getByRole("button", { name: CANCEL }));

    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(writeCalls(integrationsContextValue)).toBe(0);
  });

  it("cannot close newly opened create dialog when a previous save promise completes late", async () => {
    const user = userEvent.setup();
    let resolveAdd!: () => void;
    const pendingAdd = new Promise<void>((res) => {
      resolveAdd = res;
    });
    const addIntegration = vi.fn().mockReturnValue(pendingAdd);

    renderChannelsTab({ addIntegration });

    // 1. Open create dialog and trigger save
    const first = await openCreate(user);
    await user.type(within(first).getByLabelText(NAME_FIELD), "First attempt");
    await user.type(within(first).getByLabelText(EMAIL_FIELD), "ops@example.com");
    await user.click(within(first).getByRole("button", { name: SUBMIT_CREATE }));
    await waitFor(() => expect(addIntegration).toHaveBeenCalledTimes(1));

    // 2. User closes dialog while save is in flight
    await user.click(within(first).getByRole("button", { name: CANCEL }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    // 3. User reopens create dialog
    const second = await openCreate(user);
    await user.type(within(second).getByLabelText(NAME_FIELD), "Second draft");

    // 4. Late promise resolves from attempt 1
    await act(async () => {
      resolveAdd();
    });

    // The newly opened create dialog must stay open with its own draft, untoasted by attempt 1.
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(within(screen.getByRole("dialog")).getByLabelText(NAME_FIELD)).toHaveValue("Second draft");
    expect(toast.success).not.toHaveBeenCalled();
    expect(addIntegration).toHaveBeenCalledTimes(1);
  });

  it("cannot close newly opened edit dialog when a previous edit save completes late", async () => {
    const user = userEvent.setup();
    let resolvePatch!: () => void;
    const pendingPatch = new Promise<void>((res) => {
      resolvePatch = res;
    });
    const patchIntegration = vi.fn().mockReturnValue(pendingPatch);

    renderChannelsTab({ patchIntegration });

    // 1. Open edit dialog and trigger save
    const first = await openEdit(user);
    await user.click(within(first).getByRole("button", { name: SUBMIT_EDIT }));
    await waitFor(() => expect(patchIntegration).toHaveBeenCalledTimes(1));

    // 2. User closes edit dialog while patch is in flight
    await user.click(within(first).getByRole("button", { name: CANCEL }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    // 3. User reopens edit dialog for the same channel
    await openEdit(user);

    // 4. Late promise resolves from attempt 1
    await act(async () => {
      resolvePatch();
    });

    // The newly opened edit dialog must remain open
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(toast.success).not.toHaveBeenCalled();
    expect(patchIntegration).toHaveBeenCalledTimes(1);
  });

  it("silently discards feedback and shows no toast when session advances during in-flight toggle", async () => {
    const user = userEvent.setup();
    let resolveToggle!: () => void;
    const pendingToggle = new Promise<void>((res) => {
      resolveToggle = res;
    });
    const toggleIntegration = vi.fn().mockReturnValue(pendingToggle);

    renderChannelsTab({ toggleIntegration });

    await user.click(screen.getByRole("switch"));
    expect(toggleIntegration).toHaveBeenCalledTimes(1);

    // Stale identity occurs
    act(() => {
      bumpAuthSessionGeneration();
    });

    await act(async () => {
      resolveToggle();
    });

    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("silently discards feedback and shows no toast when session advances during in-flight test", async () => {
    const user = userEvent.setup();
    let resolveTest!: (val: { ok: boolean; message: string; latencyMs: number }) => void;
    const pendingTest = new Promise<{ ok: boolean; message: string; latencyMs: number }>((res) => {
      resolveTest = res;
    });
    const testIntegration = vi.fn().mockReturnValue(pendingTest);

    renderChannelsTab({ testIntegration });

    await user.click(screen.getByRole("button", { name: TEST_SEND }));
    expect(testIntegration).toHaveBeenCalledTimes(1);

    // Stale identity occurs
    act(() => {
      bumpAuthSessionGeneration();
    });

    await act(async () => {
      resolveTest({ ok: true, message: "OK", latencyMs: 15 });
    });

    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("silently discards feedback and shows no toast when session advances during in-flight delete", async () => {
    const user = userEvent.setup();
    let resolveRemove!: () => void;
    const pendingRemove = new Promise<void>((res) => {
      resolveRemove = res;
    });
    const removeIntegration = vi.fn().mockReturnValue(pendingRemove);

    renderChannelsTab({ removeIntegration });

    await user.click(screen.getByRole("button", { name: DELETE }));
    const confirmDialog = await screen.findByRole("alertdialog");
    await user.click(within(confirmDialog).getByRole("button", { name: CONFIRM }));
    await waitFor(() => expect(removeIntegration).toHaveBeenCalledTimes(1));

    // Stale identity occurs
    act(() => {
      bumpAuthSessionGeneration();
    });

    await act(async () => {
      resolveRemove();
    });

    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("does not delete when the confirm is accepted after the session advances", async () => {
    const user = userEvent.setup();
    const { integrationsContextValue } = renderChannelsTab();

    await user.click(screen.getByRole("button", { name: DELETE }));
    const confirmDialog = await screen.findByRole("alertdialog");
    act(() => {
      bumpAuthSessionGeneration();
    });
    await user.click(within(confirmDialog).getByRole("button", { name: CONFIRM }));

    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(integrationsContextValue.removeIntegration).not.toHaveBeenCalled();
    expect(toast.success).not.toHaveBeenCalled();
  });
});
