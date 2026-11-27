import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import i18n from "@/i18n";
import { runAxe } from "@/test/a11y-helpers";
import { FormDialog } from "./form-dialog";
import { Input } from "./input";

function renderDialog(
  onSubmit = vi.fn(),
  overrides: { saving?: boolean; submitDisabled?: boolean; lockWhileSaving?: boolean; onOpenChange?: (open: boolean) => void } = {},
) {
  return render(
    <I18nextProvider i18n={i18n}>
      <FormDialog
        open
        onOpenChange={overrides.onOpenChange ?? (() => {})}
        lockWhileSaving={overrides.lockWhileSaving}
        title="Create silence"
        description="Create a silence window so matching alerts stay quiet."
        saving={overrides.saving ?? false}
        submitDisabled={overrides.submitDisabled}
        onSubmit={onSubmit}
        submitLabel="Create"
      >
        <label htmlFor="dialog-name">Name</label>
        <Input id="dialog-name" name="name" />
      </FormDialog>
    </I18nextProvider>,
  );
}

describe("FormDialog", () => {
  it("requires an accessible description and uses native submit button types", () => {
    renderDialog();

    expect(screen.getByRole("dialog")).toHaveAccessibleDescription(
      "Create a silence window so matching alerts stay quiet.",
    );
    expect(screen.getByRole("button", { name: "Create" })).toHaveAttribute("type", "submit");
    expect(screen.getByRole("button", { name: /Cancel|取消/ })).toHaveAttribute("type", "button");
  });

  it("bounds the pinned header so the scrollable body and footer stay reachable", () => {
    renderDialog();

    // 内容区保留整体滚动作为极矮视口的兜底，正常情况下只有正文滚动。
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveClass("flex", "flex-col", "overflow-y-auto");
    expect(dialog).not.toHaveClass("overflow-hidden");

    const title = screen.getByText("Create silence");
    expect(title).toHaveClass("[overflow-wrap:anywhere]");
    expect(screen.getByText("Create a silence window so matching alerts stay quiet.")).toHaveClass("[overflow-wrap:anywhere]");
    const header = title.parentElement;
    expect(header).toHaveClass("shrink-0", "pr-10", "max-h-[30dvh]", "overflow-y-auto");

    const submitButton = screen.getByRole("button", { name: "Create" });
    const form = submitButton.closest("form");
    expect(form).toHaveClass("flex", "min-h-0", "flex-1", "flex-col");

    const body = screen.getByLabelText("Name").parentElement;
    expect(body).toHaveClass("min-h-24", "flex-1", "overflow-y-auto", "thin-scrollbar");

    const footer = submitButton.parentElement;
    expect(footer).toHaveClass("shrink-0", "flex-wrap");
  });

  it("blocks every dismissal path while a locked save is in flight", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    renderDialog(vi.fn(), { saving: true, lockWhileSaving: true, onOpenChange });

    expect(screen.getByRole("button", { name: /Cancel|取消/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Close" })).toBeDisabled();
    await user.keyboard("{Escape}");
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toContainElement(document.activeElement as HTMLElement);
  });

  it("stays dismissible while saving unless the consumer opts into the lock", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    renderDialog(vi.fn(), { saving: true, onOpenChange });

    expect(screen.getByRole("button", { name: /Cancel|取消/ })).toBeEnabled();
    await user.keyboard("{Escape}");
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("leaves validation to the dialog submit handler", () => {
    renderDialog();

    expect(screen.getByRole("button", { name: "Create" }).closest("form")).toHaveAttribute(
      "novalidate",
    );
  });

  it("submits when Enter is pressed in a field", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    renderDialog(onSubmit);

    await user.type(screen.getByLabelText("Name"), "maintenance{Enter}");

    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("blocks submission when submitDisabled is true while preserving submit label", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    renderDialog(onSubmit, { submitDisabled: true });

    const submitButton = screen.getByRole("button", { name: "Create" });
    expect(submitButton).toBeDisabled();

    await user.type(screen.getByLabelText("Name"), "maintenance{Enter}");
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("has no axe violations", async () => {
    renderDialog();
    const results = await runAxe(document.body);
    expect(results).toHaveNoViolations();
  });
});
