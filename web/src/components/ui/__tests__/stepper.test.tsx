import "@testing-library/jest-dom/vitest";
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Stepper } from "../stepper";

const steps = ["Connect", "Configure", "Confirm"];
const recoverySteps = [
  "Target",
  "Preflight",
  "Security",
  "Impact",
  "Progress",
  "Delete",
  "Result",
];

/** DialogContent: w-full max-w-[calc(100%-2rem)] on a phone-sized viewport. */
const DIALOG_VIEWPORT_INSET_PX = 32;
/** DialogHeader: px-6 */
const DIALOG_HEADER_X_PADDING_PX = 48;
/** Compact indicator: size-6 */
const NARROW_INDICATOR_PX = 24;
/** Compact connector: min-w-1 */
const NARROW_CONNECTOR_PX = 4;

function dialogContentWidth(dialogWidth: number) {
  return dialogWidth - DIALOG_VIEWPORT_INSET_PX - DIALOG_HEADER_X_PADDING_PX;
}

function compactTrackMinWidth(stepCount: number) {
  return stepCount * NARROW_INDICATOR_PX + (stepCount - 1) * NARROW_CONNECTOR_PX;
}

function classTokens(className: string) {
  return className.split(/\s+/).filter(Boolean);
}

describe("Stepper", () => {
  it("renders all step labels including on narrow screens", () => {
    render(<Stepper steps={steps} current={0} />);
    expect(screen.getByText("Connect")).toBeDefined();
    expect(screen.getByText("Configure")).toBeDefined();
    expect(screen.getByText("Confirm")).toBeDefined();
    expect(screen.getByLabelText("Connect")).toHaveAttribute("aria-current", "step");
  });

  it("uses checkmarks for completed steps", () => {
    render(<Stepper steps={steps} current={2} />);
    const nav = screen.getByRole("navigation");
    expect(nav.querySelectorAll("svg").length).toBe(2);
  });

  it("shows step number for future steps", () => {
    render(<Stepper steps={steps} current={0} />);
    expect(screen.getByText("2")).toBeDefined();
    expect(screen.getByText("3")).toBeDefined();
  });

  it("fits seven noninteractive indicators in 320px and 375px dialog geometry", () => {
    expect(compactTrackMinWidth(7)).toBeLessThanOrEqual(dialogContentWidth(320));
    expect(compactTrackMinWidth(7)).toBeLessThanOrEqual(dialogContentWidth(375));

    for (const width of [320, 375] as const) {
      const { unmount } = render(
        <div style={{ width }} className="w-full max-w-[calc(100%-2rem)]">
          <div className="px-6">
            <Stepper steps={recoverySteps} current={2} aria-label="Recovery steps" />
          </div>
        </div>
      );

      const nav = screen.getByRole("navigation", { name: "Recovery steps" });
      const navClasses = classTokens(nav.className);
      expect(navClasses).toContain("w-full");
      expect(navClasses).toContain("min-w-0");
      expect(navClasses).not.toContain("overflow-hidden");

      const indicators = recoverySteps.map((label) => screen.getByLabelText(label));
      expect(indicators).toHaveLength(7);
      for (const indicator of indicators) {
        const classes = classTokens(indicator.className);
        expect(classes).toContain("size-6");
        expect(classes).toContain("sm:size-11");
        expect(classes).toContain("shrink-0");
        expect(classes).not.toContain("size-11");
      }

      expect(screen.getByLabelText("Security")).toHaveAttribute("aria-current", "step");
      expect(indicators.filter((el) => el.getAttribute("aria-current") === "step")).toHaveLength(1);

      for (const label of recoverySteps) {
        expect(classTokens(screen.getByText(label).className)).toContain("max-sm:sr-only");
      }

      const connectors = Array.from(nav.querySelectorAll<HTMLElement>("div[aria-hidden='true']"));
      expect(connectors).toHaveLength(6);
      for (const connector of connectors) {
        const classes = classTokens(connector.className);
        expect(classes).toContain("min-w-1");
        expect(classes).toContain("flex-1");
        expect(classes).toContain("sm:min-w-2");
      }

      unmount();
    }
  });
});
