import { describe, test, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import MetricsTab from "./metrics-tab";

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
(global as unknown as Record<string, unknown>).ResizeObserver =
  (global as unknown as Record<string, unknown>).ResizeObserver || ResizeObserverStub;

const mockData = {
  granularity: "hourly" as const,
  bucket_seconds: 3600,
  series: [
    {
      metric: "cpu_pct",
      unit: "percent",
      points: [
        { t: "2026-04-17T10:00:00Z", avg: 20, max: 30 },
        { t: "2026-04-17T11:00:00Z", avg: 25, max: 35 },
      ],
    },
  ],
};

vi.mock("./use-node-metrics", () => ({
  useNodeMetrics: () => ({ data: mockData, isLoading: false, error: null }),
}));

beforeEach(() => {
  document.body.innerHTML = "";
});

describe("MetricsTab", () => {
  test("renders range and granularity selectors", () => {
    render(<MetricsTab nodeId={1} />);
    expect(screen.getByTestId("range-select")).toBeInTheDocument();
    expect(screen.getByTestId("granularity-select")).toBeInTheDocument();
  });

  test("renders one section per metric", () => {
    render(<MetricsTab nodeId={1} />);
    expect(screen.getByTestId("metric-section-cpu_pct")).toBeInTheDocument();
    expect(screen.getByTestId("metric-section-mem_pct")).toBeInTheDocument();
    expect(screen.getByTestId("metric-section-load1")).toBeInTheDocument();
  });

  test("changing range updates the select value", () => {
    render(<MetricsTab nodeId={1} />);
    fireEvent.change(screen.getByTestId("range-select"), {
      target: { value: "7d" },
    });
    expect((screen.getByTestId("range-select") as HTMLSelectElement).value).toBe("7d");
  });

  test("export button triggers a download", () => {
    render(<MetricsTab nodeId={1} />);
    const createObjectURL = vi.fn(() => "blob:test");
    const revokeObjectURL = vi.fn();
    URL.createObjectURL = createObjectURL;
    URL.revokeObjectURL = revokeObjectURL;
    const spy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => {});
    fireEvent.click(screen.getByTestId("export-csv"));
    expect(spy).toHaveBeenCalled();
    spy.mockRestore();
  });
});
