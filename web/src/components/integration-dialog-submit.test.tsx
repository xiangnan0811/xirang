import "@testing-library/jest-dom/vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { describe, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import { EndpointHintWarning } from "@/lib/api/integrations-api";
import type { IntegrationChannel, NewIntegrationInput } from "@/types/domain";
import { IntegrationCreateDialog } from "./integration-create-dialog";
import { IntegrationEditorDialog } from "./integration-editor-dialog";
import type { IntegrationEditorDraft } from "./integration-editor-dialog";

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

function withI18n(ui: React.ReactNode) {
  return render(<I18nextProvider i18n={i18n}>{ui}</I18nextProvider>);
}

async function fillCreateDialog(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText(/通道名称|Channel name/), "Operations email");
  await user.type(screen.getByLabelText(/收件邮箱|Recipient email/), "ops@example.com");
}

async function openCreateHint(
  user: ReturnType<typeof userEvent.setup>,
  onSave: CreateSave,
) {
  withI18n(
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
  withI18n(
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
  it("applies the create sample without submitting the form", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn<CreateSave>().mockResolvedValue(undefined);
    withI18n(
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
});
