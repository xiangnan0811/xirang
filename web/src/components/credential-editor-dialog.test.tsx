import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { CredentialEditorDialog } from "./credential-editor-dialog";
import { mapAppCredential } from "@/lib/api/credentials";

vi.mock("@/context/auth-context.hooks", () => ({ useAuth: () => ({ token: "test-token" }) }));
vi.mock("@/lib/api/credentials", async (original) => ({
  ...await original<typeof import("@/lib/api/credentials")>(),
  createCredentialsApi: () => ({
    listProfiles: async () => [{ id: "db", name: "Database", credentialType: "db", isDocker: false,
      configSchema: [{ key: "password", label: "Test password", type: "password", required: false }] }],
  }),
}));

it("drops unsaved passwords on controlled reopen and loads a different credential's defaults", async () => {
  const user = userEvent.setup();
  const credential = mapAppCredential({ id: 1, type: "db", name: "First", config: {} });
  const props = { onOpenChange: vi.fn(), onSaved: vi.fn(), editingCredential: credential };
  const view = render(<CredentialEditorDialog open {...props} />);
  await user.type(await screen.findByLabelText("Test password"), "UNSAVED-TEST-PASSWORD");
  view.rerender(<CredentialEditorDialog open={false} {...props} />);
  view.rerender(<CredentialEditorDialog open {...props} />);
  expect(await screen.findByLabelText("Test password")).toHaveValue("");
  view.rerender(<CredentialEditorDialog open {...props} editingCredential={mapAppCredential({ id: 2, type: "db", name: "Second" })} />);
  expect(await screen.findByDisplayValue("Second")).toBeInTheDocument();
  expect(await screen.findByLabelText("Test password")).toHaveValue("");
});
