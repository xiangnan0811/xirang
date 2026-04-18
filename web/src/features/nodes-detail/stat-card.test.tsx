import { describe, test, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import StatCard from "./stat-card";

// Recharts uses ResizeObserver in jsdom which doesn't exist by default.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (!global.ResizeObserver) {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (global as any).ResizeObserver = ResizeObserverStub;
}

describe("StatCard", () => {
  test("renders label, formatted value, and unit", () => {
    render(<StatCard label="CPU" value={85.4} unit="%" />);
    expect(screen.getByText("CPU")).toBeInTheDocument();
    expect(screen.getByText("85.4")).toBeInTheDocument();
    expect(screen.getByText("%")).toBeInTheDocument();
  });

  test("applies warn variant when value >= warnAt", () => {
    render(<StatCard label="DISK" value={92} warnAt={80} />);
    expect(screen.getByTestId("stat-card")).toHaveAttribute("data-variant", "warn");
  });

  test("stays default when value < warnAt", () => {
    render(<StatCard label="MEM" value={60} warnAt={80} />);
    expect(screen.getByTestId("stat-card")).toHaveAttribute("data-variant", "default");
  });

  test("renders em-dash for non-finite value", () => {
    render(<StatCard label="LOAD" value={NaN} />);
    expect(screen.getByText("—")).toBeInTheDocument();
  });
});
