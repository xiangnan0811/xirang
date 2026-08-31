import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import i18n from "@/i18n";
import { runAxe } from "@/test/a11y-helpers";
import { FormDialog } from "./form-dialog";
import { Input } from "./input";

function renderDialog(onSubmit = vi.fn()) {
  return render(
    <I18nextProvider i18n={i18n}>
      <FormDialog
        open
        onOpenChange={() => {}}
        title="Create silence"
        description="Create a silence window so matching alerts stay quiet."
        saving={false}
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

  it("has no axe violations", async () => {
    renderDialog();
    const results = await runAxe(document.body);
    expect(results).toHaveNoViolations();
  });
});
