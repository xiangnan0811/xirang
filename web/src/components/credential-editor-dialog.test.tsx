import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { CredentialEditorDialog } from "./credential-editor-dialog";
import { mapAppCredential } from "@/lib/api/credentials";

const { updateMock } = vi.hoisted(() => ({ updateMock: vi.fn() }));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));
vi.mock("@/context/auth-context.hooks", () => ({ useAuth: () => ({ token: "test-token" }) }));
vi.mock("@/lib/api/credentials", async (original) => ({
  ...await original<typeof import("@/lib/api/credentials")>(),
  createCredentialsApi: () => ({
    listProfiles: async () => [{ id: "db", name: "Database", credentialType: "db", isDocker: false,
      configSchema: [{ key: "password", label: "Test password", type: "password", required: false }] }],
    update: (...args: unknown[]) => updateMock(...args),
  }),
}));

it("drops unsaved passwords on controlled reopen and loads a different credential's defaults", async () => {
  const user = userEvent.setup();
  const credential = mapAppCredential({ id: 1, type: "db", name: "First", config: {}, reference_count: 0 });
  expect(credential.referenceCount).toBe(0);
  const props = { onOpenChange: vi.fn(), onSaved: vi.fn(), editingCredential: credential };
  const view = render(<CredentialEditorDialog open {...props} />);
  const password = await screen.findByLabelText("Test password");
  expect(password).toHaveAttribute("aria-describedby", "credential-password-impact-password");
  expect(document.getElementById("credential-password-impact-password")).toHaveTextContent(
    "更改密码会影响引用此凭据的策略，留空将保留原密码。",
  );
  await user.type(password, "UNSAVED-TEST-PASSWORD");
  view.rerender(<CredentialEditorDialog open={false} {...props} />);
  view.rerender(<CredentialEditorDialog open {...props} />);
  expect(await screen.findByLabelText("Test password")).toHaveValue("");
  view.rerender(<CredentialEditorDialog open {...props} editingCredential={mapAppCredential({ id: 2, type: "db", name: "Second" })} />);
  expect(await screen.findByDisplayValue("Second")).toBeInTheDocument();
  expect(await screen.findByLabelText("Test password")).toHaveValue("");
  expect(document.getElementById("credential-password-impact-password")).toHaveTextContent(
    "更改密码会影响引用此凭据的策略，留空将保留原密码。",
  );
});

it("keeps a blank edit password as the existing preserve-password update", async () => {
  const user = userEvent.setup();
  updateMock.mockResolvedValue({ id: 1 });
  const credential = mapAppCredential({ id: 1, type: "db", name: "First", config: {}, reference_count: 0 });
  const onOpenChange = vi.fn();
  render(
    <CredentialEditorDialog
      open
      onOpenChange={onOpenChange}
      onSaved={vi.fn()}
      editingCredential={credential}
    />,
  );
  const password = await screen.findByLabelText("Test password");
  await user.type(password, "temporary");
  await user.clear(password);
  await user.click(screen.getByRole("button", { name: "保存" }));
  expect(updateMock).toHaveBeenCalledWith("test-token", 1, expect.objectContaining({
    name: "First",
    type: "db",
    password: undefined,
  }));
});
