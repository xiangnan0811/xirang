import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { BackupsPage } from "./backups-page";
import type { BackupConfidenceData, BackupHealthData, StorageUsageData } from "@/types/domain";

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

const backupConfidence: BackupConfidenceData = {
  generatedAt: "2026-05-17T00:00:00Z",
  summary: {
    healthy: 0,
    warning: 0,
    atRisk: 0,
    insufficient: 1,
    total: 1,
  },
  items: [
    {
      id: "policy-1",
      scope: "policy",
      policyId: 1,
      policyName: "daily-policy",
      status: "insufficient",
      score: 72,
      reasons: [
        { code: "drill_missing", severity: "warning", message: "缺少恢复演练证据，不能证明备份可恢复" },
      ],
      evidence: [
        { type: "backup", status: "success", message: "最近备份执行状态 success", taskRunId: 10 },
        { type: "drill", status: "missing", message: "没有结构化恢复演练证据" },
      ],
      nextSteps: [
        { code: "run_restore_drill", label: "配置并执行一次恢复演练" },
      ],
      targets: [{ nodeId: 1, nodeName: "node-a" }],
    },
  ],
};

const { getBackupConfidenceMock, getBackupHealthMock, getStorageUsageMock, verifyMountMock } = vi.hoisted(() => ({
  getBackupConfidenceMock: vi.fn(),
  getBackupHealthMock: vi.fn(),
  getStorageUsageMock: vi.fn(),
  verifyMountMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: "test-token",
    role: "admin",
  }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getBackupConfidence: getBackupConfidenceMock,
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
    getBackupConfidenceMock.mockResolvedValue(backupConfidence);
    getBackupHealthMock.mockResolvedValue(backupHealth);
    getStorageUsageMock.mockResolvedValue(storageUsage);

    render(
      <MemoryRouter>
        <BackupsPage />
      </MemoryRouter>
    );

    expect(await screen.findByRole("link", { name: /Configure backup task|配置备份任务/ })).toHaveAttribute("href", "/app/tasks");
    expect(await screen.findByText(/Backup Confidence|备份可信度/)).toBeInTheDocument();
    expect((await screen.findAllByText(/Insufficient proof|证据不足/)).length).toBeGreaterThan(0);
    expect(await screen.findByText(/Never backed up|从未备份/)).toBeInTheDocument();
  });
});
