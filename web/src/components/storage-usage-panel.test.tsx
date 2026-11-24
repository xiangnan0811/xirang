import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { setLanguage } from "@/i18n";
import { StorageUsagePanel } from "./storage-usage-panel";
import type { StorageUsageData } from "@/types/domain";

const getStorageUsageMock = vi.fn();
const barChartDataSpy = vi.fn();

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getStorageUsage: (...args: unknown[]) => getStorageUsageMock(...args),
  },
}));

vi.mock("recharts", async () => {
  const actual = await vi.importActual<typeof import("recharts")>("recharts");
  return {
    ...actual,
    ResponsiveContainer: ({ children }: { children: React.ReactNode }) => (
      <div data-testid="responsive-container">{children}</div>
    ),
    BarChart: ({ data, children }: { data: unknown[]; children: React.ReactNode }) => {
      barChartDataSpy(data);
      return <div data-testid="bar-chart">{children}</div>;
    },
  };
});

describe("StorageUsagePanel", () => {
  beforeEach(async () => {
    await setLanguage("zh");
    getStorageUsageMock.mockReset();
    barChartDataSpy.mockReset();
  });

  afterEach(async () => {
    cleanup();
    await setLanguage("zh");
  });

  it("separates incomplete partial and unscanned directories from the complete usage chart and warns on scan timeout", async () => {
    const payload: StorageUsageData = {
      mountPoints: [{ path: "/backup", usedGB: 50, totalGB: 100, pct: 50 }],
      perNode: [
        { nodeId: 1, nodeName: "node-complete", path: "/backup/n1", usedGB: 10 },
        { nodeId: 2, nodeName: "node-partial", path: "/backup/n2", usedGB: 2.09, incomplete: true },
        { nodeId: 3, nodeName: "node-unscanned", path: "/backup/n3", usedGB: 0, incomplete: true },
        { nodeId: 4, nodeName: "node-tiny", path: "/backup/n4", usedGB: 0.29, incomplete: true },
      ],
      incomplete: true,
      mountPointsIncomplete: true,
    };
    getStorageUsageMock.mockResolvedValueOnce(payload);

    render(<StorageUsagePanel />);

    expect(await screen.findByText("存储扫描未完整完成")).toBeInTheDocument();
    expect(screen.getByText(/部分存储卷或目录扫描超时/)).toBeInTheDocument();

    // Only the complete measurement may enter the BarChart dataset.
    expect(barChartDataSpy).toHaveBeenCalledWith([
      { nodeId: 1, nodeName: "node-complete", path: "/backup/n1", usedGB: 10 },
    ]);

    const incompleteList = screen.getByRole("list");
    expect(within(incompleteList).getByText("node-partial")).toBeInTheDocument();
    // 2.09 must never round UP to 2.1 when displayed with a `≥` lower bound.
    expect(within(incompleteList).getByText("≥ 2.09 GB（部分统计）")).toBeInTheDocument();
    expect(within(incompleteList).queryByText(/2\.1/)).not.toBeInTheDocument();
    expect(within(incompleteList).getByText("node-tiny")).toBeInTheDocument();
    expect(within(incompleteList).getByText("≥ 0.29 GB（部分统计）")).toBeInTheDocument();
    expect(within(incompleteList).getByText("node-unscanned")).toBeInTheDocument();
    expect(within(incompleteList).getByText("未完成扫描")).toBeInTheDocument();
  });

  it("warns without showing 'no data' when mount point scanning times out before any volume finishes", async () => {
    const payload: StorageUsageData = {
      mountPoints: [],
      perNode: [],
      incomplete: true,
      mountPointsIncomplete: true,
    };
    getStorageUsageMock.mockResolvedValueOnce(payload);

    render(<StorageUsagePanel />);

    expect(await screen.findByText("存储扫描未完整完成")).toBeInTheDocument();
    expect(screen.getByText(/挂载点扫描超时/)).toBeInTheDocument();
    expect(screen.queryByText("暂无数据")).not.toBeInTheDocument();
  });
});
