import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Navigate, Route, Routes, useLocation, useNavigate } from "react-router-dom";

import { BackupsPage } from "./backups-page";
import { BackupsDataPage } from "./backups-page.data";
import { BackupsOverviewPage } from "./backups-page.overview";
import { BackupsRecoveryPage } from "./backups-page.recovery";
import type { BackupConfidenceData, BackupHealthData, StorageUsageData } from "@/types/domain";
import { recoveryPoint, repository } from "@/features/backup-assets/__tests__/test-utils";
import { BACKUP_ASSETS_PREFERENCES_KEY } from "@/features/backup-assets/backup-assets-preferences";

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

const {
  authRef,
  getBackupConfidenceMock,
  getBackupHealthMock,
  getStorageUsageMock,
  getRecoveryPointMock,
  listBackupAssetsMock,
  listBackupRepositoriesMock,
  listRecoveryPointsMock,
  verifyMountMock,
} = vi.hoisted(() => ({
  authRef: {
    current: {
      token: "test-token" as string | null,
      role: "admin" as "admin" | "operator" | "viewer" | null,
      ensureStepUpProof: vi.fn(),
    },
  },
  getBackupConfidenceMock: vi.fn(),
  getBackupHealthMock: vi.fn(),
  getStorageUsageMock: vi.fn(),
  getRecoveryPointMock: vi.fn(),
  listBackupAssetsMock: vi.fn(),
  listBackupRepositoriesMock: vi.fn(),
  listRecoveryPointsMock: vi.fn(),
  verifyMountMock: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

vi.mock("@/features/backup-assets/export-job-panel", () => ({
  ExportJobPanel: ({
    onRouteChange,
  }: {
    onRouteChange: (exportJobId: string | null, options: { replace: boolean }) => void;
  }) => (
    <div>
      <button type="button" onClick={() => onRouteChange("e".repeat(32), { replace: false })}>
        Synthetic push export
      </button>
      <button type="button" onClick={() => onRouteChange(null, { replace: true })}>
        Synthetic replace export
      </button>
    </div>
  ),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getBackupConfidence: getBackupConfidenceMock,
    getBackupHealth: getBackupHealthMock,
    getStorageUsage: getStorageUsageMock,
    getRecoveryPoint: getRecoveryPointMock,
    listBackupAssets: listBackupAssetsMock,
    listBackupRepositories: listBackupRepositoriesMock,
    listRecoveryPoints: listRecoveryPointsMock,
    verifyMount: verifyMountMock,
  },
}));

vi.mock("recharts", () => ({
  Area: () => null,
  Bar: () => null,
  BarChart: ({ children }: { children: React.ReactNode }) => <svg>{children}</svg>,
  CartesianGrid: () => null,
  ComposedChart: ({ children }: { children: React.ReactNode }) => <svg>{children}</svg>,
  ResponsiveContainer: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  Tooltip: () => null,
  XAxis: () => null,
  YAxis: () => null,
}));

describe("BackupsPage", () => {
  beforeEach(() => {
    vi.unstubAllEnvs();
    window.localStorage.clear();
    authRef.current = { token: "test-token", role: "admin", ensureStepUpProof: vi.fn() };
    getBackupConfidenceMock.mockReset();
    getBackupHealthMock.mockReset();
    getStorageUsageMock.mockReset();
    getRecoveryPointMock.mockReset();
    listBackupAssetsMock.mockReset();
    listBackupRepositoriesMock.mockReset();
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listRecoveryPointsMock.mockReset();
    listRecoveryPointsMock.mockResolvedValue({ items: [], nextCursor: null });
    verifyMountMock.mockReset();
  });

  it("renders the page action without violating single-child Slot constraints", async () => {
    getBackupConfidenceMock.mockResolvedValue(backupConfidence);
    getBackupHealthMock.mockResolvedValue(backupHealth);
    getStorageUsageMock.mockResolvedValue(storageUsage);

    renderBackups("/app/backups/overview");

    expect(await screen.findByRole("link", { name: /Configure backup task|配置备份任务/ })).toHaveAttribute("href", "/app/tasks");
    expect(await screen.findByText(/Backup Confidence|备份可信度/)).toBeInTheDocument();
    expect((await screen.findAllByText(/Insufficient proof|证据不足/)).length).toBeGreaterThan(0);
    expect(await screen.findByText(/Never backed up|从未备份/)).toBeInTheDocument();
  });

  it("demo 模式无 token 时展示 mock 可信路径和故障路径", async () => {
    vi.stubEnv("VITE_ENABLE_DEMO_MODE", "true");
    authRef.current = { token: null, role: null, ensureStepUpProof: vi.fn() };

    renderBackups("/app/backups/overview");

    expect(await screen.findByText("核心 MySQL 增量（演示） · 北京主库-1")).toBeInTheDocument();
    expect(await screen.findByText("消息队列快照（演示故障） · 天津网关-2")).toBeInTheDocument();
    expect(screen.getByText(/演示故障：SSH Key 已过期/)).toBeInTheDocument();
    expect(getBackupConfidenceMock).not.toHaveBeenCalled();
  });

  it("redirects the backups index to overview with replace semantics", async () => {
    getBackupConfidenceMock.mockResolvedValue(backupConfidence);
    getBackupHealthMock.mockResolvedValue(backupHealth);
    getStorageUsageMock.mockResolvedValue(storageUsage);

    renderBackups("/app/backups");

    expect(await screen.findByTestId("backups-location")).toHaveTextContent(
      "/app/backups/overview"
    );
    expect(await screen.findByText(/Backup Confidence|备份可信度/)).toBeInTheDocument();
  });

  it("renders one route tablist with mounted tabpanels and the active data panel", async () => {
    getBackupHealthMock.mockResolvedValue(backupHealth);

    renderBackups("/app/backups/data");

    const tablist = await screen.findByRole("tablist", { name: /Backup views|备份视图/ });
    expect(tablist).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /Overview|概览/ })).toHaveAttribute("aria-selected", "false");
    expect(screen.getByRole("tab", { name: /Data|数据/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: /Recovery|恢复/ })).toHaveAttribute("aria-selected", "false");
    expect(screen.getAllByRole("tabpanel", { hidden: true })).toHaveLength(3);
    expect(screen.getByRole("tabpanel", { name: /Data|数据/ })).not.toHaveAttribute("hidden");
    expect(screen.getByRole("heading", { name: /Backup data|备份数据/ })).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: /Asset results|资产结果/ })).toBeInTheDocument();
  });

  it("passes the authenticated role and token only through the data-page processing boundary", async () => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1440 });
    getBackupHealthMock.mockResolvedValue(backupHealth);
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    const route = `/app/backups/data?repositoryId=${repository.id}`;
    const adminPage = renderBackups(route);

    expect(await screen.findByRole("button", { name: /Processing coverage|处理覆盖/ })).toBeInTheDocument();
    adminPage.unmount();

    authRef.current = {
      token: "operator-token",
      role: "operator",
      ensureStepUpProof: vi.fn(),
    };
    renderBackups(route);
    expect(await screen.findByRole("region", { name: /Asset results|资产结果/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Processing coverage|处理覆盖/ })).not.toBeInTheDocument();
  });

  it("pushes a new export handle, restores it with Back, and replace-dismisses it", async () => {
    getBackupHealthMock.mockResolvedValue(backupHealth);
    getBackupConfidenceMock.mockResolvedValue(backupConfidence);
    getStorageUsageMock.mockResolvedValue(storageUsage);
    const originalJobId = "d".repeat(32);
    const originalRoute = `/app/backups/data?exportJobId=${originalJobId}`;
    const user = userEvent.setup();
    renderBackups(["/app/backups/overview", originalRoute], 1);

    await user.click(await screen.findByRole("button", { name: "Synthetic push export" }));
    await waitFor(() => expect(screen.getByTestId("backups-location")).toHaveTextContent(
      `/app/backups/data?exportJobId=${"e".repeat(32)}`,
    ));

    fireEvent.click(screen.getByTestId("backups-history-back"));
    await waitFor(() => expect(screen.getByTestId("backups-location")).toHaveTextContent(originalRoute));

    await user.click(await screen.findByRole("button", { name: "Synthetic replace export" }));
    await waitFor(() => expect(screen.getByTestId("backups-location")).toHaveTextContent("/app/backups/data"));

    fireEvent.click(screen.getByTestId("backups-history-back"));
    await waitFor(() => expect(screen.getByTestId("backups-location")).toHaveTextContent("/app/backups/overview"));
  });

  it("replace-clears an incompatible export handle on repository change so Back cannot reopen it", async () => {
    getBackupHealthMock.mockResolvedValue(backupHealth);
    getBackupConfidenceMock.mockResolvedValue(backupConfidence);
    getStorageUsageMock.mockResolvedValue(storageUsage);
    const replacementRepository = {
      ...repository,
      id: "e".repeat(32),
      displayName: "Synthetic Replacement Repository",
    };
    listBackupRepositoriesMock.mockResolvedValue({
      items: [
        { status: "available", value: repository },
        { status: "available", value: replacementRepository },
      ],
      nextCursor: null,
    });
    const exportJobId = "d".repeat(32);
    renderBackups([
      "/app/backups/overview",
      `/app/backups/data?repositoryId=${repository.id}&exportJobId=${exportJobId}`,
    ], 1);

    const repositorySelect = await waitFor(() => {
      const element = document.querySelector("#backup-assets-repository");
      expect(element).toBeInstanceOf(HTMLSelectElement);
      return element as HTMLSelectElement;
    });
    fireEvent.change(repositorySelect, { target: { value: replacementRepository.id } });

    await waitFor(() => expect(screen.getByTestId("backups-location")).toHaveTextContent(
      `/app/backups/data?repositoryId=${replacementRepository.id}`,
    ));
    expect(screen.getByTestId("backups-location")).not.toHaveTextContent("exportJobId");

    fireEvent.click(screen.getByTestId("backups-history-back"));
    await waitFor(() => expect(screen.getByTestId("backups-location")).toHaveTextContent("/app/backups/overview"));
  });

  it("hydrates an omitted data layout from the bounded browser preference", async () => {
    getBackupHealthMock.mockResolvedValue(backupHealth);
    window.localStorage.setItem(
      BACKUP_ASSETS_PREFERENCES_KEY,
      JSON.stringify({ version: 1, layout: "grid", contextWidth: 320, inspectorWidth: 480 })
    );

    renderBackups("/app/backups/data");

    await waitFor(() =>
      expect(screen.getByTestId("backups-location")).toHaveTextContent(
        "/app/backups/data?layout=grid"
      )
    );
    expect(screen.getByRole("radio", { name: /Grid|网格/ })).toHaveAttribute(
      "aria-checked",
      "true"
    );
  });

  it("persists a user layout change while preserving unrelated route state", async () => {
    getBackupHealthMock.mockResolvedValue(backupHealth);
    window.localStorage.setItem(
      BACKUP_ASSETS_PREFERENCES_KEY,
      JSON.stringify({ version: 1, layout: "list", contextWidth: 320, inspectorWidth: 480 })
    );
    const user = userEvent.setup();

    renderBackups("/app/backups/data?taskId=7");
    await user.click(await screen.findByRole("radio", { name: /Grid|网格/ }));

    await waitFor(() =>
      expect(screen.getByTestId("backups-location")).toHaveTextContent(
        "/app/backups/data?taskId=7&layout=grid"
      )
    );
    expect(JSON.parse(window.localStorage.getItem(BACKUP_ASSETS_PREFERENCES_KEY) ?? "null")).toEqual({
      version: 1,
      layout: "grid",
      contextWidth: 320,
      inspectorWidth: 480,
    });
  });

  it("safe-resets an unknown data query without retaining it", async () => {
    getBackupHealthMock.mockResolvedValue(backupHealth);

    renderBackups("/app/backups/data?path=%2Fprivate%2Fpayroll.csv");

    expect(await screen.findByTestId("backups-location")).toHaveTextContent("/app/backups/data");
    expect(screen.getByTestId("backups-location")).not.toHaveTextContent("private");
  });

  it("replace-repairs a repository/recovery-point mismatch to canonical repository context", async () => {
    getBackupHealthMock.mockResolvedValue(backupHealth);
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    const mismatchedRecoveryPoint = { ...recoveryPoint, repositoryId: "d".repeat(32) };
    getRecoveryPointMock.mockResolvedValue({ status: "available", value: mismatchedRecoveryPoint });

    renderBackups(
      `/app/backups/data?repositoryId=${repository.id}&recoveryPointId=${recoveryPoint.id}&entryId=${"c".repeat(64)}`
    );

    await waitFor(() =>
      expect(screen.getByTestId("backups-location")).toHaveTextContent(
        new RegExp(`^/app/backups/data\\?repositoryId=${repository.id}$`)
      )
    );
    expect(screen.getByTestId("backups-location")).not.toHaveTextContent("recoveryPointId");
    expect(screen.getByTestId("backups-location")).not.toHaveTextContent("entryId");
    expect(listBackupAssetsMock).not.toHaveBeenCalled();
  });

  it("keeps recovery evidence context separate from future recovery controls", async () => {
    getBackupHealthMock.mockResolvedValue(backupHealth);

    renderBackups(
      `/app/backups/recovery?taskId=7&recoveryPointId=${recoveryPoint.id}&inspectorTab=evidence`
    );

    expect(await screen.findByRole("tabpanel", { name: /Recovery|恢复/ })).toBeInTheDocument();
    const recoveryStatus = screen
      .getByText(/Recovery point evidence|恢复点证据/)
      .closest('[role="status"]');
    expect(recoveryStatus).toHaveTextContent(
      /Exact recovery point selected|已选择精确恢复点/
    );
    expect(recoveryStatus).toHaveTextContent(/Task #7|任务 #7/);
    expect(screen.getByTitle(recoveryPoint.id)).toHaveTextContent(
      `${recoveryPoint.id.slice(0, 8)}…${recoveryPoint.id.slice(-8)}`
    );
    expect(recoveryStatus).not.toHaveTextContent(
      /No recovery point selected|尚未选择恢复点/
    );
    expect(screen.getByRole("link", { name: /Task context|任务上下文/ })).toHaveAttribute(
      "href",
      "/app/tasks"
    );
    expect(screen.queryByRole("button", { name: /Export|导出|Start recovery|开始恢复/ })).not.toBeInTheDocument();
  });
});

function renderBackups(initialEntry: string | string[], initialIndex?: number) {
  const initialEntries = Array.isArray(initialEntry) ? initialEntry : [initialEntry];
  return render(
    <MemoryRouter initialEntries={initialEntries} initialIndex={initialIndex}>
      <Routes>
        <Route path="/app/backups" element={<BackupsPage />}>
          <Route index element={<Navigate to="overview" replace />} />
          <Route path="overview" element={<BackupsOverviewPage />} />
          <Route path="data" element={<BackupsDataPage />} />
          <Route path="recovery" element={<BackupsRecoveryPage />} />
        </Route>
      </Routes>
      <LocationProbe />
    </MemoryRouter>
  );
}

function LocationProbe() {
  const location = useLocation();
  const navigate = useNavigate();
  return (
    <div>
      <output data-testid="backups-location">{location.pathname + location.search}</output>
      <button type="button" data-testid="backups-history-back" onClick={() => navigate(-1)}>Test history back</button>
    </div>
  );
}
