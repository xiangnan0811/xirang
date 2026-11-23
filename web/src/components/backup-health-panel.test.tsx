import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { setLanguage } from "@/i18n";
import { BackupHealthPanel } from "./backup-health-panel";

const getBackupHealthMock = vi.fn();

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getBackupHealth: (...args: unknown[]) => getBackupHealthMock(...args),
  },
}));

describe("BackupHealthPanel", () => {
  beforeEach(async () => {
    await setLanguage("zh");
    getBackupHealthMock.mockReset();
  });

  afterEach(async () => {
    cleanup();
    await setLanguage("zh");
  });

  it("renders honest unknown state when successRate7d is null instead of 100%", async () => {
    getBackupHealthMock.mockResolvedValue({
      generatedAt: "2026-05-17T01:00:00Z",
      staleNodes: [],
      degradedPolicies: [],
      healthTrend: [{ date: "2026-05-17", total: 0, success: 0, rate: 0 }],
      summary: {
        totalNodes: 2,
        neverBackedUp: 0,
        stale48h: 0,
        policiesHealthy: 1,
        policiesDegraded: 0,
        successRate7d: null,
      },
    });

    render(<BackupHealthPanel />);

    expect(await screen.findByText("7日成功率")).toBeInTheDocument();
    const rateCard = screen.getByText("7日成功率").closest("[data-tone]");
    expect(rateCard).toHaveTextContent("—");
    expect(rateCard).toHaveTextContent("未知（近 7 天暂无执行记录）");
    expect(rateCard).not.toHaveTextContent("100");
    expect(rateCard).not.toHaveAttribute("data-tone", "success");

    await act(async () => setLanguage("en"));
    await screen.findByText("7-day success rate");
    expect(screen.getByText("Unknown (no runs in 7d)")).toBeInTheDocument();
  });

  it("renders rounded percentage and tone when successRate7d has samples", async () => {
    getBackupHealthMock.mockResolvedValue({
      generatedAt: "2026-05-17T01:00:00Z",
      staleNodes: [],
      degradedPolicies: [],
      healthTrend: [{ date: "2026-05-17", total: 20, success: 18, rate: 90 }],
      summary: {
        totalNodes: 2,
        neverBackedUp: 0,
        stale48h: 0,
        policiesHealthy: 1,
        policiesDegraded: 0,
        successRate7d: 90,
      },
    });

    render(<BackupHealthPanel />);

    expect(await screen.findByText("7日成功率")).toBeInTheDocument();
    const rateCard = screen.getByText("7日成功率").closest("[data-tone]");
    expect(rateCard).toHaveTextContent("90%");
    expect(rateCard).toHaveAttribute("data-tone", "warning");
  });
});
