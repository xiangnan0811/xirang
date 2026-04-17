import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Stepper } from "../stepper";

const steps = ["Connect", "Configure", "Confirm"];

describe("Stepper", () => {
  it("renders all step labels", () => {
    render(<Stepper steps={steps} current={0} />);
    expect(screen.getByText("Connect")).toBeDefined();
    expect(screen.getByText("Configure")).toBeDefined();
    expect(screen.getByText("Confirm")).toBeDefined();
  });

  it("shows checkmark for completed steps", () => {
    render(<Stepper steps={steps} current={2} />);
    // Steps 0 and 1 are complete, should show ✓
    const checks = screen.getAllByText("✓");
    expect(checks.length).toBe(2);
  });

  it("shows step number for future steps", () => {
    render(<Stepper steps={steps} current={0} />);
    // Steps 1 and 2 are future (numbers 2 and 3)
    expect(screen.getByText("2")).toBeDefined();
    expect(screen.getByText("3")).toBeDefined();
  });
});
