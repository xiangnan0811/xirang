import { beforeEach, describe, test, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import DiskForecastCard from "./disk-forecast-card";

const mockData = {
  diskGbTotal: 500,
  diskGbUsedNow: 312.5,
  dailyGrowthGb: 1.8,
  forecast: { daysToFull: 104, dateFull: "2026-07-30", confidence: "medium" as const },
};

const { mockUseDiskForecast } = vi.hoisted(() => ({
  mockUseDiskForecast: vi.fn(),
}));

vi.mock("./use-disk-forecast", () => ({
  useDiskForecast: mockUseDiskForecast,
}));

beforeEach(() => {
  mockUseDiskForecast.mockReset();
  mockUseDiskForecast.mockReturnValue({ data: mockData, isLoading: false, error: null });
});

describe("DiskForecastCard", () => {
  test("renders days_to_full and confidence copy", () => {
    render(<DiskForecastCard nodeId={1} token="test-token" />);
    expect(screen.getByText(/104/)).toBeInTheDocument();
    expect(screen.getByTestId("confidence").textContent).toMatch(/中|medium|Confidence/i);
    expect(mockUseDiskForecast).toHaveBeenCalledWith(1, "test-token");
  });

  test("renders disk usage summary", () => {
    render(<DiskForecastCard nodeId={1} token="test-token" />);
    expect(screen.getByText(/312\.5/)).toBeInTheDocument();
    expect(screen.getByText(/500/)).toBeInTheDocument();
  });

  test("renders daily growth", () => {
    render(<DiskForecastCard nodeId={1} token="test-token" />);
    expect(screen.getByText(/1\.80/)).toBeInTheDocument();
  });

  test("shows loading state when data is null", () => {
    mockUseDiskForecast.mockReturnValueOnce({ data: null, isLoading: true, error: null });
    render(<DiskForecastCard nodeId={1} token="test-token" />);
    expect(screen.getByTestId("disk-forecast-card")).toBeInTheDocument();
  });
});
