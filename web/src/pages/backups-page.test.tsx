import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { BackupsPage } from "./backups-page";
import type { BackupHealthData, StorageUsageData } from "@/types/domain";

const backupHealth: BackupHealthData = {
  summary: {
    totalNodes: 2,
    neverBackedUp: 0,
    stale48h: 0,
    policiesHealthy: 2,
    policiesDegraded: 0,
    successRate7d: 100,
  },
  staleNodes: [],
  degradedPolicies: [],
  healthTrend: [],
};

const storageUsage: StorageUsageData = {
  mountPoints: [],
  perNode: [],
};

const { getBackupHealthMock, getStorageUsageMock, verifyMountMock } = vi.hoisted(() => ({
  getBackupHealthMock: vi.fn(),
  getStorageUsageMock: vi.fn(),
  verifyMountMock: vi.fn(),
}));

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({
    token: "test-token",
    role: "admin",
  }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getBackupHealth: getBackupHealthMock,
    getStorageUsage: getStorageUsageMock,
    verifyMount: verifyMountMock,
  },
}));

vi.mock("recharts", () => ({
  Area: () => null,
  Bar: () => null,
  BarChart: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  CartesianGrid: () => null,
  ComposedChart: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  ResponsiveContainer: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  Tooltip: () => null,
  XAxis: () => null,
  YAxis: () => null,
}));

describe("BackupsPage", () => {
  it("renders the page action without violating single-child Slot constraints", async () => {
    getBackupHealthMock.mockResolvedValue(backupHealth);
    getStorageUsageMock.mockResolvedValue(storageUsage);

    render(
      <MemoryRouter>
        <BackupsPage />
      </MemoryRouter>
    );

    expect(await screen.findByRole("link", { name: /Configure backup task|配置备份任务/ })).toHaveAttribute("href", "/app/tasks");
    expect(await screen.findByText(/Never backed up|从未备份/)).toBeInTheDocument();
  });
});
