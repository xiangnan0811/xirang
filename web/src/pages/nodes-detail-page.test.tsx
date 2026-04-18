import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { NodesDetailPage } from "./nodes-detail-page";

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

vi.mock("@/features/nodes-detail/use-node-status", () => ({
  useNodeStatus: () => ({
    data: {
      online: true,
      probed_at: null,
      current: {},
      trend_1h: {},
      trend_24h: {},
      open_alerts: 0,
      running_tasks: 0,
    },
    isLoading: false,
    error: null,
    refetch: vi.fn(),
  }),
}));

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/app/nodes/:id" element={<NodesDetailPage />} />
      </Routes>
    </MemoryRouter>
  );
}

describe("NodesDetailPage", () => {
  it("overview tab is active by default", () => {
    renderAt("/app/nodes/42");
    const overviewTab = screen.getByRole("tab", { name: /概览/ });
    expect(overviewTab).toHaveAttribute("aria-selected", "true");
  });

  it("?tab=metrics activates the metrics tab", () => {
    renderAt("/app/nodes/42?tab=metrics");
    const metricsTab = screen.getByRole("tab", { name: /指标/ });
    expect(metricsTab).toHaveAttribute("aria-selected", "true");
  });

  it("clicking a tab updates aria-selected", () => {
    renderAt("/app/nodes/42");
    fireEvent.click(screen.getByRole("tab", { name: /告警/ }));
    expect(screen.getByRole("tab", { name: /告警/ })).toHaveAttribute("aria-selected", "true");
  });
});
