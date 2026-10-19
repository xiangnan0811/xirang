import "@testing-library/jest-dom/vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { AuthContext, type AuthContextValue, type AuthRole } from "@/context/auth-context.shared";
import {
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  isAuthTransitionActive,
  rememberAuthIdentity,
} from "@/lib/api/core";
import { EndpointHintWarning } from "@/lib/api/integrations-api";
import { toast } from "@/components/ui/toast-sonner";
import type { IntegrationChannel, NewIntegrationInput } from "@/types/domain";
import { IntegrationCreateDialog } from "./integration-create-dialog";
import { IntegrationEditorDialog } from "./integration-editor-dialog";
import type { IntegrationEditorDraft } from "./integration-editor-dialog";

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

type CreateSave = (input: NewIntegrationInput) => Promise<void>;
type EditorSave = (draft: IntegrationEditorDraft) => Promise<void>;

const editorIntegration: IntegrationChannel = {
  id: "int-7",
  type: "webhook",
  name: "Operations webhook",
  endpoint: "https://hooks.example.test/xirang",
  hasSecret: false,
  enabled: true,
  failThreshold: 2,
  cooldownMinutes: 5,
  proxyUrl: "",
};

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

function withProviders(
  ui: React.ReactNode,
  authValue: AuthContextValue = createAuthContextValue()
) {
  rememberAuthIdentity(authValue.token, authValue.role);
  return render(
    <AuthContext.Provider value={authValue}>
      <I18nextProvider i18n={i18n}>{ui}</I18nextProvider>
    </AuthContext.Provider>
  );
}

async function fillCreateDialog(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText(/通道名称|Channel name/), "Operations email");
  await user.type(screen.getByLabelText(/收件邮箱|Recipient email/), "ops@example.com");
}

async function openCreateHint(
  user: ReturnType<typeof userEvent.setup>,
  onSave: CreateSave,
) {
  withProviders(
    <IntegrationCreateDialog open onOpenChange={() => {}} onSave={onSave} />,
  );
  await fillCreateDialog(user);
  await user.click(screen.getByRole("button", { name: /保存通道|Save Channel/ }));
  expect(await screen.findByText("endpoint domain hint")).toBeInTheDocument();
}

async function openEditorHint(
  user: ReturnType<typeof userEvent.setup>,
  onSave: EditorSave,
) {
  withProviders(
    <IntegrationEditorDialog
      open
      onOpenChange={() => {}}
      integration={editorIntegration}
      onSave={onSave}
    />,
  );
  await user.click(screen.getByRole("button", { name: /保存修改|Save Changes/ }));
  expect(await screen.findByText("endpoint domain hint")).toBeInTheDocument();
}

describe("integration dialog submit boundaries", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    if (isAuthTransitionActive()) clearAuthTransitionBarrier();
    bumpAuthSessionGeneration();
    rememberAuthIdentity("test-token", "admin");
  });

  it("applies the create sample without submitting the form", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn<CreateSave>().mockResolvedValue(undefined);
    withProviders(
      <IntegrationCreateDialog open onOpenChange={() => {}} onSave={onSave} />,
    );
    await fillCreateDialog(user);

    await user.click(screen.getByRole("button", { name: /套用示例|Apply Sample/ }));

    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByLabelText(/收件邮箱|Recipient email/)).toHaveValue("ops@example.com");
  });

  it("rechecks a create hint without submitting the form", async () => {
    const user = userEvent.setup();
    const onSave = vi
      .fn<CreateSave>()
      .mockRejectedValueOnce(new EndpointHintWarning("endpoint domain hint"))
      .mockResolvedValue(undefined);
    await openCreateHint(user, onSave);

    await user.click(screen.getByRole("button", { name: /重新检查|Re-check/ }));

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
  });

  it("confirms a create hint with exactly one save request", async () => {
    const user = userEvent.setup();
    const onSave = vi
      .fn<CreateSave>()
      .mockRejectedValueOnce(new EndpointHintWarning("endpoint domain hint"))
      .mockResolvedValue(undefined);
    await openCreateHint(user, onSave);

    await user.click(screen.getByRole("button", { name: /确认保存|Confirm Save/ }));

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2));
    expect(onSave.mock.calls[1]?.[0]).toEqual(
      expect.objectContaining({ skipEndpointHint: true }),
    );
  });

  it("rechecks an edit hint without submitting the form", async () => {
    const user = userEvent.setup();
    const onSave = vi
      .fn<EditorSave>()
      .mockRejectedValueOnce(new EndpointHintWarning("endpoint domain hint"))
      .mockResolvedValue(undefined);
    await openEditorHint(user, onSave);

    await user.click(screen.getByRole("button", { name: /重新检查|Re-check/ }));

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
  });

  it("confirms an edit hint with exactly one save request", async () => {
    const user = userEvent.setup();
    const onSave = vi
      .fn<EditorSave>()
      .mockRejectedValueOnce(new EndpointHintWarning("endpoint domain hint"))
      .mockResolvedValue(undefined);
    await openEditorHint(user, onSave);

    await user.click(screen.getByRole("button", { name: /确认保存|Confirm Save/ }));

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2));
    expect(onSave.mock.calls[1]?.[0]).toEqual(
      expect.objectContaining({ skipEndpointHint: true }),
    );
  });

  it("submits valid create form once when inputs are valid", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn<CreateSave>().mockResolvedValue(undefined);
    withProviders(
      <IntegrationCreateDialog open onOpenChange={() => {}} onSave={onSave} />,
    );
    await fillCreateDialog(user);

    await user.click(screen.getByRole("button", { name: /保存通道|Save Channel/ }));

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "Operations email",
        endpoint: "ops@example.com",
        type: "email",
      }),
    );
  });

  it("discards late create save feedback when dialog is closed during in-flight save", async () => {
    const user = userEvent.setup();
    let resolveSave!: () => void;
    const savePromise = new Promise<void>((res) => {
      resolveSave = res;
    });
    const onSave = vi.fn<CreateSave>().mockReturnValue(savePromise);

    const { rerender } = withProviders(
      <IntegrationCreateDialog open onOpenChange={() => {}} onSave={onSave} />,
    );
    await fillCreateDialog(user);

    await user.click(screen.getByRole("button", { name: /保存通道|Save Channel/ }));
    expect(onSave).toHaveBeenCalledTimes(1);

    // Dialog is closed while save is pending
    rerender(
      <AuthContext.Provider value={createAuthContextValue()}>
        <I18nextProvider i18n={i18n}>
          <IntegrationCreateDialog open={false} onOpenChange={() => {}} onSave={onSave} />
        </I18nextProvider>
      </AuthContext.Provider>,
    );

    await act(async () => {
      resolveSave();
    });

    expect(toast.error).not.toHaveBeenCalled();
  });

  it("discards late create save feedback when auth generation advances during in-flight save", async () => {
    const user = userEvent.setup();
    let rejectSave!: (err: unknown) => void;
    const savePromise = new Promise<void>((_, rej) => {
      rejectSave = rej;
    });
    const onSave = vi.fn<CreateSave>().mockReturnValue(savePromise);

    withProviders(
      <IntegrationCreateDialog open onOpenChange={() => {}} onSave={onSave} />,
    );
    await fillCreateDialog(user);

    await user.click(screen.getByRole("button", { name: /保存通道|Save Channel/ }));
    expect(onSave).toHaveBeenCalledTimes(1);

    // Stale identity occurs during save
    act(() => {
      bumpAuthSessionGeneration();
    });

    await act(async () => {
      rejectSave(new Error("Network failed"));
    });

    expect(toast.error).not.toHaveBeenCalled();
  });

  it("invalidates stale edit attempt when editing object changes for the same ID", async () => {
    const user = userEvent.setup();
    let resolveSave!: () => void;
    const savePromise = new Promise<void>((res) => {
      resolveSave = res;
    });
    const onSave = vi.fn<EditorSave>().mockReturnValue(savePromise);

    const { rerender } = withProviders(
      <IntegrationEditorDialog
        open
        onOpenChange={() => {}}
        integration={editorIntegration}
        onSave={onSave}
      />,
    );

    await user.click(screen.getByRole("button", { name: /保存修改|Save Changes/ }));
    expect(onSave).toHaveBeenCalledTimes(1);

    // Same-ID editing object changes (e.g., refreshed from server with a new name)
    const updatedIntegration: IntegrationChannel = {
      ...editorIntegration,
      name: "Operations webhook updated",
    };
    rerender(
      <AuthContext.Provider value={createAuthContextValue()}>
        <I18nextProvider i18n={i18n}>
          <IntegrationEditorDialog
            open
            onOpenChange={() => {}}
            integration={updatedIntegration}
            onSave={onSave}
          />
        </I18nextProvider>
      </AuthContext.Provider>,
    );

    await act(async () => {
      resolveSave();
    });

    // Old attempt was invalidated; no toast displayed
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("does not submit create dialog when user is not active admin", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn<CreateSave>().mockResolvedValue(undefined);
    withProviders(
      <IntegrationCreateDialog open onOpenChange={() => {}} onSave={onSave} />,
      createAuthContextValue("test-token", "viewer"),
    );
    await fillCreateDialog(user);

    await user.click(screen.getByRole("button", { name: /保存通道|Save Channel/ }));

    expect(onSave).not.toHaveBeenCalled();
  });
});
