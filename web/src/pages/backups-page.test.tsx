import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
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

const { authRef, getBackupConfidenceMock, getBackupHealthMock, getStorageUsageMock, verifyMountMock } = vi.hoisted(() => ({
  authRef: { current: { token: "test-token" as string | null, role: "admin" as "admin" | null } },
  getBackupConfidenceMock: vi.fn(),
  getBackupHealthMock: vi.fn(),
  getStorageUsageMock: vi.fn(),
  verifyMountMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
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
  beforeEach(() => {
    vi.unstubAllEnvs();
    authRef.current = { token: "test-token", role: "admin" };
    getBackupConfidenceMock.mockReset();
    getBackupHealthMock.mockReset();
    getStorageUsageMock.mockReset();
    verifyMountMock.mockReset();
  });

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

  it("demo 模式无 token 时展示 mock 可信路径和故障路径", async () => {
    vi.stubEnv("VITE_ENABLE_DEMO_MODE", "true");
    authRef.current = { token: null, role: null };

    render(
      <MemoryRouter>
        <BackupsPage />
      </MemoryRouter>
    );

    expect(await screen.findByText("核心 MySQL 增量（演示） · 北京主库-1")).toBeInTheDocument();
    expect(await screen.findByText("消息队列快照（演示故障） · 天津网关-2")).toBeInTheDocument();
    expect(screen.getByText(/演示故障：SSH Key 已过期/)).toBeInTheDocument();
    expect(getBackupConfidenceMock).not.toHaveBeenCalled();
  });
});
