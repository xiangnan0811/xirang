import { describe, test, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import TrendChart from "./trend-chart";

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

const sampleSeries = [
  {
    metric: "cpu_pct",
    unit: "percent",
    points: [
      { t: "2026-04-17T10:00:00Z", avg: 20, max: 30 },
      { t: "2026-04-17T11:00:00Z", avg: 25, max: 35 },
    ],
  },
];

describe("TrendChart", () => {
  test("renders all range buttons and marks active", () => {
    render(<TrendChart series={sampleSeries} range="24h" onRangeChange={() => {}} />);
    expect(screen.getByTestId("range-1h")).toHaveAttribute("data-state", "inactive");
    expect(screen.getByTestId("range-6h")).toHaveAttribute("data-state", "inactive");
    expect(screen.getByTestId("range-24h")).toHaveAttribute("data-state", "active");
    expect(screen.getByTestId("range-7d")).toHaveAttribute("data-state", "inactive");
    expect(screen.getByTestId("range-30d")).toHaveAttribute("data-state", "inactive");
  });

  test("clicking a range calls onRangeChange", () => {
    const spy = vi.fn();
    render(<TrendChart series={sampleSeries} range="24h" onRangeChange={spy} />);
    fireEvent.click(screen.getByTestId("range-7d"));
    expect(spy).toHaveBeenCalledWith("7d");
  });

  test("shows empty state when no series", () => {
    render(<TrendChart series={[]} range="24h" onRangeChange={() => {}} />);
    expect(screen.getByText(/暂无数据/)).toBeInTheDocument();
  });

  test("respects fields filter — no empty state when matching series exists", () => {
    const twoSeries = [
      ...sampleSeries,
      {
        metric: "mem_pct",
        unit: "percent",
        points: [{ t: "2026-04-17T10:00:00Z", avg: 55 }],
      },
    ];
    render(
      <TrendChart series={twoSeries} fields={["cpu_pct"]} range="24h" onRangeChange={() => {}} />
    );
    expect(screen.queryByText(/暂无数据/)).toBeNull();
  });
});
