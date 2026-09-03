import "@testing-library/jest-dom/vitest";
import { StrictMode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Navigate, NavLink, Outlet, Route, Routes, createMemoryRouter, RouterProvider, useLocation, useNavigate } from "react-router-dom";
import { canonicalizeBackupLocation } from "@/lib/backup-navigation";

import { BackupsPage } from "./backups-page";
import { BackupsDataPage } from "./backups-page.data";
import { BackupsOverviewPage } from "./backups-page.overview";
import { BackupsRecoveryPage } from "./backups-page.recovery";
import type { BackupConfidenceData, BackupHealthData, StorageUsageData } from "@/types/domain";
import { buildAssetRows, recoveryPoint, repository } from "@/features/backup-assets/__tests__/test-utils";
import { BACKUP_ASSETS_PREFERENCES_KEY } from "@/features/backup-assets/backup-assets-preferences";
import { parseBackupAssetsRoute } from "@/features/backup-assets/backup-assets-route-state";

const filesSavedSearchId = "e".repeat(32);
const filesSearch = `?view=search&savedSearchId=${filesSavedSearchId}`;

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
  getBackupAssetMock,
  issueTicketMock,
  listBackupAssetsMock,
  listBackupRepositoriesMock,
  listRecoveryPointsMock,
  listBackupFileSourceNodesMock,
  listBackupFileSourceSetsMock,
  listBackupFileSourceVersionsMock,
  resolveBackupFileSourceRecoveryPointMock,
  getRecoveryPlanMock,
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
  getBackupAssetMock: vi.fn(),
  issueTicketMock: vi.fn(),
  listBackupAssetsMock: vi.fn(),
  listBackupRepositoriesMock: vi.fn(),
  listRecoveryPointsMock: vi.fn(),
  listBackupFileSourceNodesMock: vi.fn(),
  listBackupFileSourceSetsMock: vi.fn(),
  listBackupFileSourceVersionsMock: vi.fn(),
  resolveBackupFileSourceRecoveryPointMock: vi.fn(),
  getRecoveryPlanMock: vi.fn(),
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

vi.mock("@/features/backup-assets/private-network-content-transport-panel", () => ({
  PrivateNetworkContentTransportPanel: ({ token }: { token: string }) => (
    <section data-testid="private-network-content-transport">{token}</section>
  ),
}));

vi.mock("@/lib/api/backup-ga-api", () => ({
  createBackupGaApi: () => ({
    getReadiness: vi.fn().mockResolvedValue({
      schemaVersion: 1,
      class: "fresh",
      status: "blocked",
      inventoryComplete: false,
      inventoryDigest: "",
      acknowledgedDigest: "",
      exportRootValid: false,
      keyDomainsReady: true,
      workerOptional: true,
      counts: { candidates: 0, conflicts: 0, unsupported: 0, capabilityGaps: 0 },
      conflicts: [],
    }),
    runInventory: vi.fn(),
    acknowledge: vi.fn(),
    enable: vi.fn(),
  }),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getBackupConfidence: getBackupConfidenceMock,
    getBackupHealth: getBackupHealthMock,
    getStorageUsage: getStorageUsageMock,
    getRecoveryPoint: getRecoveryPointMock,
    getBackupAsset: getBackupAssetMock,
    issueTicket: issueTicketMock,
    listBackupAssets: listBackupAssetsMock,
    listBackupRepositories: listBackupRepositoriesMock,
    listRecoveryPoints: listRecoveryPointsMock,
    listBackupFileSourceNodes: listBackupFileSourceNodesMock,
    listBackupFileSourceSets: listBackupFileSourceSetsMock,
    listBackupFileSourceVersions: listBackupFileSourceVersionsMock,
    resolveBackupFileSourceRecoveryPoint: resolveBackupFileSourceRecoveryPointMock,
    verifyMount: verifyMountMock,
  },
}));

vi.mock("@/lib/api/backup-recovery-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/backup-recovery-api")>();
  return {
    ...actual,
    createBackupRecoveryApi: () => ({
      createPlan: vi.fn(),
      getPlan: getRecoveryPlanMock,
      preflight: vi.fn(),
      overrideSecurity: vi.fn(),
      authorizeWrite: vi.fn(),
      execute: vi.fn(),
      getJob: vi.fn(),
      authorizeExactMirrorDelete: vi.fn(),
      getJobItems: vi.fn(),
      getJobResults: vi.fn(),
      cancelPlan: vi.fn(),
      cancelJob: vi.fn(),
      retainResults: vi.fn(),
      issueResultDownloadTicket: vi.fn(),
      cleanupResults: vi.fn(),
    }),
  };
});

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
    getBackupAssetMock.mockReset();
    issueTicketMock.mockReset();
    listBackupAssetsMock.mockReset();
    listBackupRepositoriesMock.mockReset();
    listBackupRepositoriesMock.mockResolvedValue({ items: [], nextCursor: null });
    listRecoveryPointsMock.mockReset();
    listRecoveryPointsMock.mockResolvedValue({ items: [], nextCursor: null });
    listBackupFileSourceNodesMock.mockReset();
    listBackupFileSourceNodesMock.mockResolvedValue({ status: "available", value: { items: [], nextCursor: null } });
    listBackupFileSourceSetsMock.mockReset();
    listBackupFileSourceSetsMock.mockResolvedValue({ status: "available", value: { items: [], nextCursor: null } });
    listBackupFileSourceVersionsMock.mockReset();
    listBackupFileSourceVersionsMock.mockResolvedValue({ status: "available", value: { items: [], nextCursor: null } });
    resolveBackupFileSourceRecoveryPointMock.mockReset();
    resolveBackupFileSourceRecoveryPointMock.mockResolvedValue({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
    getRecoveryPlanMock.mockReset();
    verifyMountMock.mockReset();
  });

  it("renders the page action without violating single-child Slot constraints", async () => {
    getBackupConfidenceMock.mockResolvedValue(backupConfidence);
    getBackupHealthMock.mockResolvedValue(backupHealth);
    getStorageUsageMock.mockResolvedValue(storageUsage);

    renderBackups("/app/backups/overview");
    expect(await screen.findByRole("link", { name: /Configure backup task|配置备份任务/ })).toHaveAttribute("href", "/app/tasks");
    expect(await screen.findByText(/Backup Confidence|备份可信度/)).toBeInTheDocument();
    await waitFor(() => expect(document.getElementById("backup-assets-ga")).toBeInTheDocument());
    expect((await screen.findAllByText(/Insufficient proof|证据不足/)).length).toBeGreaterThan(0);
    expect(await screen.findByText(/Never backed up|从未备份/)).toBeInTheDocument();
  });

  it("mounts the private-network content control only for an authenticated Admin", async () => {
    getBackupConfidenceMock.mockResolvedValue(backupConfidence);
    getBackupHealthMock.mockResolvedValue(backupHealth);
    getStorageUsageMock.mockResolvedValue(storageUsage);

    const admin = renderBackups("/app/backups/overview");
    expect(await screen.findByTestId("private-network-content-transport")).toHaveTextContent("test-token");
    admin.unmount();

    for (const role of ["operator", "viewer"] as const) {
      authRef.current = { token: "test-token", role, ensureStepUpProof: vi.fn() };
      const page = renderBackups("/app/backups/overview");
      await screen.findByText(/Backup Confidence|备份可信度/);
      expect(screen.queryByTestId("private-network-content-transport")).not.toBeInTheDocument();
      page.unmount();
    }
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

  it("redirects the backups index to Files with replace semantics", async () => {
    getBackupConfidenceMock.mockResolvedValue(backupConfidence);
    getBackupHealthMock.mockResolvedValue(backupHealth);
    getStorageUsageMock.mockResolvedValue(storageUsage);

    renderBackups("/app/backups");

    expect(await screen.findByTestId("backups-location")).toHaveTextContent(
      "/app/backups/data"
    );
    expect(await screen.findByRole("heading", { name: /Files|文件/ })).toBeInTheDocument();
  });

  it("renders one route tablist with mounted tabpanels and the active data panel", async () => {
    getBackupHealthMock.mockResolvedValue(backupHealth);

    renderBackups("/app/backups/data");

    const tablist = await screen.findByRole("tablist", { name: /Backup views|备份视图/ });
    expect(tablist).toBeInTheDocument();
    expect(tablist).toHaveClass("min-h-11");
    expect(within(tablist).getAllByRole("tab").map((tab) => tab.textContent)).toEqual(["文件", "概览", "恢复"]);
    expect(within(tablist).getAllByRole("tab").every((tab) => tab.className.includes("min-h-11"))).toBe(true);
    expect(screen.getByRole("tab", { name: /Overview|概览/ })).toHaveAttribute("aria-selected", "false");
    expect(screen.getByRole("tab", { name: /Files|文件/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: /Recovery|恢复/ })).toHaveAttribute("aria-selected", "false");
    expect(screen.getAllByRole("tabpanel", { hidden: true })).toHaveLength(3);
    expect(screen.getByRole("tabpanel", { name: /Files|文件/ })).not.toHaveAttribute("hidden");
    expect(screen.getByRole("heading", { name: /Files|文件/ })).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: /File results|文件结果/ })).toBeInTheDocument();
  });

  it("does not request backup health on Files or Recovery", async () => {
    const user = userEvent.setup();
    renderBackups("/app/backups/data");

    expect(await screen.findByRole("tab", { name: /Files|文件/ })).toHaveAttribute("aria-selected", "true");
    expect(getBackupHealthMock).not.toHaveBeenCalled();

    await user.click(screen.getByRole("tab", { name: /Recovery|恢复/ }));
    expect(await screen.findByRole("tab", { name: /Recovery|恢复/ })).toHaveAttribute("aria-selected", "true");
    expect(getBackupHealthMock).not.toHaveBeenCalled();
  });

  it("keeps a blocked file-source projection out of the searchable workspace", async () => {
    getBackupHealthMock.mockResolvedValue(backupHealth);
    listBackupFileSourceNodesMock.mockRejectedValue(new Error("synthetic source unavailable"));

    renderBackups("/app/backups/data");

    expect(await screen.findByText(/The file source is currently unavailable|文件来源当前不可用/)).toBeInTheDocument();
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: /File results|文件结果/ })).not.toBeInTheDocument();
  });

  it("issues one automatic safe preview when a generic-MIME file is activated", async () => {
    Object.defineProperty(HTMLElement.prototype, "getBoundingClientRect", {
      configurable: true,
      value: () => ({
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: 1024,
        bottom: 640,
        width: 1024,
        height: 640,
        toJSON: () => ({}),
      }) as DOMRect,
    });
    Object.defineProperties(HTMLElement.prototype, {
      offsetHeight: { configurable: true, get: () => 640 },
      offsetWidth: { configurable: true, get: () => 1024 },
      clientHeight: { configurable: true, get: () => 640 },
      clientWidth: { configurable: true, get: () => 1024 },
    });
    getBackupHealthMock.mockResolvedValue(backupHealth);
    const backupSetId = "c".repeat(32);
    const row = buildAssetRows(2)[1];
    const asset = { ...row.asset, mimeType: "application/octet-stream" };
    const genericRow = { ...row, asset };
    listBackupRepositoriesMock.mockResolvedValue({
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    });
    listRecoveryPointsMock.mockResolvedValue({
      items: [{ status: "available", value: recoveryPoint }],
      nextCursor: null,
    });
    getRecoveryPointMock.mockResolvedValue({ status: "available", value: recoveryPoint });
    listBackupAssetsMock.mockResolvedValue({
      items: [{ status: "available", value: asset }],
      nextCursor: null,
    });
    getBackupAssetMock.mockResolvedValue({ status: "available", value: asset });
    listBackupFileSourceNodesMock.mockResolvedValue({
      status: "available",
      value: {
        items: [{ nodeId: 3, displayName: "节点", backupSetCount: 1, retainedVersionCount: 1, latestRetainedAt: null, catalogCoverage: "complete", browseState: "browsable", unavailableReason: null }],
        nextCursor: null,
      },
    });
    listBackupFileSourceSetsMock.mockResolvedValue({
      status: "available",
      value: {
        items: [{ backupSetId, nodeId: 3, displayLabel: "每日", lineageKind: "task", versionCount: 1, latestRetainedAt: null, catalogCoverage: "complete", browseState: "browsable", unavailableReason: null }],
        nextCursor: null,
      },
    });
    listBackupFileSourceVersionsMock.mockResolvedValue({
      status: "available",
      value: {
        items: [{
          recoveryPointId: recoveryPoint.id,
          repositoryId: repository.id,
          producingTaskId: 7,
          capturedAt: recoveryPoint.capturedAt,
          committedAt: recoveryPoint.committedAt,
          createdAt: recoveryPoint.createdAt,
          lifecycleState: "committed",
          catalogCoverage: "complete",
          browseState: "browsable",
          unavailableReason: null,
          contentAvailability: { available: true, reason: null },
          entryCount: 1,
          logicalBytes: asset.size,
          permissions: { list: true, preview: false, download: false },
        }],
        nextCursor: null,
      },
    });
    issueTicketMock.mockResolvedValue({
      status: "available",
      value: {
        schemaVersion: 1,
        action: "preview",
        assetRef: asset.ref,
        renderer: "plain_text",
        profile: "text_v2",
        contentUrl: "/api/v1/backup-assets/content/synthetic",
        method: "GET",
        contentType: "text/plain; charset=utf-8",
        disposition: "inline",
        range: { supported: false, maxBytes: 262_144 },
        classification: "ordinary",
        expiresAt: "2026-08-27T12:00:00.000Z",
        truncated: false,
      },
    });

    const route = `/app/backups/data?nodeId=3&backupSetId=${backupSetId}&repositoryId=${repository.id}&taskId=7&recoveryPointId=${recoveryPoint.id}`;
    const user = userEvent.setup();
    renderBackups(route, undefined, { strict: true });

    await waitFor(() => expect(listBackupAssetsMock).toHaveBeenCalled());
    await user.click(await screen.findByRole("button", { name: new RegExp(`(?:Open file or directory|打开文件或目录) ${genericRow.asset.name}`) }));

    await waitFor(() => expect(issueTicketMock).toHaveBeenCalledTimes(1));
    expect(issueTicketMock).toHaveBeenCalledWith(
      "test-token",
      genericRow.ref,
      expect.objectContaining({
        schemaVersion: 1,
        action: "preview",
        previewIntent: "safePreviewV1",
        signal: expect.any(AbortSignal),
      }),
    );
    expect(issueTicketMock.mock.calls[0]?.[2]).not.toHaveProperty("renderer");
    expect(issueTicketMock.mock.calls[0]?.[2]).not.toHaveProperty("profile");
    expect(screen.queryByRole("button", { name: /Load preview|加载预览/ })).not.toBeInTheDocument();
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
    expect(await screen.findByRole("region", { name: /File results|文件结果/ })).toBeInTheDocument();
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

  it("replace-clears an incompatible export handle on primary source change so Back cannot reopen it", async () => {
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
    listBackupFileSourceNodesMock.mockResolvedValue({
      status: "available",
      value: {
        items: [
          { nodeId: 7, displayName: "节点 A", backupSetCount: 1, retainedVersionCount: 1, latestRetainedAt: null, catalogCoverage: "complete", browseState: "browsable", unavailableReason: null },
          { nodeId: 8, displayName: "节点 B", backupSetCount: 1, retainedVersionCount: 1, latestRetainedAt: null, catalogCoverage: "complete", browseState: "browsable", unavailableReason: null },
        ],
        nextCursor: null,
      },
    });
    const exportJobId = "d".repeat(32);
    renderBackups([
      "/app/backups/overview",
      `/app/backups/data?repositoryId=${repository.id}&exportJobId=${exportJobId}`,
    ], 1);

    const nodeSelect = await waitFor(() => {
      const element = document.querySelector('select[aria-label="节点"]');
      expect(element).toBeInstanceOf(HTMLSelectElement);
      return element as HTMLSelectElement;
    });
    fireEvent.change(nodeSelect, { target: { value: "8" } });

    await waitFor(() => expect(screen.getByTestId("backups-location")).toHaveTextContent(
      "/app/backups/data?nodeId=8",
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
    resolveBackupFileSourceRecoveryPointMock.mockResolvedValue({
      status: "available",
      value: {
        nodeId: 7,
        backupSetId: "a".repeat(32),
        recoveryPointId: recoveryPoint.id,
        repositoryId: mismatchedRecoveryPoint.repositoryId,
        producingTaskId: 9,
        browseState: "browsable",
        unavailableReason: null,
      },
    });

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
    expect(resolveBackupFileSourceRecoveryPointMock).toHaveBeenCalledTimes(1);
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
      "/app/backups/data?taskId=7",
    );
    expect(screen.queryByRole("button", { name: /Export|导出|Start recovery|开始恢复/ })).not.toBeInTheDocument();
  });

  it("hydrates the recovery wizard from opaque route handles without putting authority material in the route", async () => {
    const planId = "1".repeat(32);
    getBackupHealthMock.mockResolvedValue(backupHealth);
    getRecoveryPlanMock.mockResolvedValue({
      status: "available",
      value: {
        id: planId,
        state: "preflight_ready",
        revision: "8",
        repositoryId: "2".repeat(32),
        recoveryPointId: recoveryPoint.id,
        targetMode: "isolated",
        targetNodeId: 4,
        targetRootId: "recovery-root",
        conflictPolicy: "fail_on_conflict",
        securityDecision: "allow_clean",
        estimatedItems: 2,
        estimatedBytes: 4096,
        createdAt: "2026-08-16T01:00:00.000Z",
        updatedAt: "2026-08-16T01:01:00.000Z",
      },
    });

    renderBackups(`/app/backups/recovery?recoveryPointId=${recoveryPoint.id}&planId=${planId}`);

    expect(await screen.findByRole("heading", { name: /Run recovery preflight|运行恢复预检/ })).toBeInTheDocument();
    expect(getRecoveryPlanMock).toHaveBeenCalledWith("test-token", planId, expect.any(AbortSignal));
    expect(screen.getByTestId("backups-location")).toHaveTextContent(`planId=${planId}`);
    expect(screen.getByTestId("backups-location")).not.toHaveTextContent(/proof|reason|grant|secret|ticket/i);
  });

  it("fails closed with an explicit unavailable state when a recovery route lacks admin authority", async () => {
    const planId = "1".repeat(32);
    getBackupHealthMock.mockResolvedValue(backupHealth);
    authRef.current = { token: "test-token", role: "viewer", ensureStepUpProof: vi.fn() };

    renderBackups(`/app/backups/recovery?recoveryPointId=${recoveryPoint.id}&planId=${planId}`);

    expect(await screen.findByText(/Recovery unavailable|恢复不可用/)).toBeInTheDocument();
    expect(getRecoveryPlanMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /Create recovery plan|创建恢复计划|Start recovery|开始恢复/ })).not.toBeInTheDocument();
  });

  it("hides task context when recovery has no producing task", async () => {
    renderBackups("/app/backups/recovery");

    expect(await screen.findByRole("heading", { name: /Recovery evidence|恢复证据/ })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Task context|任务上下文/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Export|导出|Start recovery|开始恢复/ })).not.toBeInTheDocument();
  });

  it.each([
    ["Files", "/app/backups/data", <BackupsDataPage />],
    ["Recovery", "/app/backups/recovery", <BackupsRecoveryPage />],
  ])("does not let an exiting %s page overwrite sibling navigation", async (_, initialEntry, page) => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={[initialEntry]}>
        <NavLink to="/app/tasks">Tasks</NavLink>
        {page}
        <LocationProbe />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole("link", { name: "Tasks" }));

    await waitFor(() => {
      expect(screen.getByTestId("backups-location")).toHaveTextContent("/app/tasks");
    });
  });

  it("reaches Tasks in one click from the backup index", async () => {
    const user = userEvent.setup();
    const router = createMemoryRouter(
      [
        {
          path: "/",
          HydrateFallback: () => null,
          element: (
            <>
              <nav>
                <NavLink to="/app/tasks">Tasks</NavLink>
              </nav>
              <Outlet />
              <LocationProbe />
            </>
          ),
          children: [
            {
              path: "app/backups",
              loader: canonicalizeBackupLocation,
              HydrateFallback: () => null,
              element: <BackupsPage />,
              children: [
                { path: "data", element: <div>files-panel</div> },
                { path: "overview", element: <div>overview-panel</div> },
                { path: "recovery", element: <div>recovery-panel</div> },
              ],
            },
            { path: "app/tasks", element: <div data-testid="tasks-page">Tasks ready</div> },
          ],
        },
      ],
      { initialEntries: ["/app/backups"] },
    );

    render(<RouterProvider router={router} />);

    expect(await screen.findByRole("tab", { name: /Files|文件/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("files-panel")).toBeInTheDocument();
    expect(screen.queryByText("overview-panel")).not.toBeInTheDocument();
    expect(screen.getByTestId("backups-location")).toHaveTextContent("/app/backups/data");

    await user.click(screen.getByRole("link", { name: "Tasks" }));
    expect(await screen.findByTestId("tasks-page")).toBeInTheDocument();
    expect(screen.getByTestId("backups-location")).toHaveTextContent("/app/tasks");
  });

  it("preserves exact Files search when clicking the active Files tab", async () => {
    const user = userEvent.setup();
    expect(parseBackupAssetsRoute("/app/backups/data", filesSearch)).toMatchObject({
      status: "valid",
      state: { page: "data", view: "search", savedSearchId: filesSavedSearchId },
    });
    renderBackupTabs(`/app/backups/data${filesSearch}`);

    const files = screen.getByRole("tab", { name: /Files|文件/ });
    expect(files).toHaveAttribute("href", `/app/backups/data${filesSearch}`);
    await user.click(files);
    expect(screen.getByTestId("backups-location").textContent).toBe(`/app/backups/data${filesSearch}`);
  });

  it("restores Files search after a mounted click round-trip through a sibling tab", async () => {
    const user = userEvent.setup();
    renderBackupTabs(`/app/backups/data${filesSearch}`);

    await user.click(screen.getByRole("tab", { name: /Overview|概览/ }));
    expect(screen.getByTestId("backups-location").textContent).toBe("/app/backups/overview");
    expect(screen.getByRole("tab", { name: /Files|文件/ })).toHaveAttribute(
      "href",
      `/app/backups/data${filesSearch}`,
    );
    expect(screen.getByRole("tab", { name: /Recovery|恢复/ })).toHaveAttribute("href", "/app/backups/recovery");

    await user.click(screen.getByRole("tab", { name: /Files|文件/ }));
    expect(screen.getByTestId("backups-location").textContent).toBe(`/app/backups/data${filesSearch}`);
  });

  it("restores Files search after a mounted keyboard round-trip through a sibling tab", () => {
    renderBackupTabs(`/app/backups/data${filesSearch}`);

    fireEvent.keyDown(screen.getByRole("tab", { name: /Files|文件/ }), { key: "ArrowRight" });
    expect(screen.getByTestId("backups-location").textContent).toBe("/app/backups/overview");

    fireEvent.keyDown(screen.getByRole("tab", { name: /Overview|概览/ }), { key: "ArrowLeft" });
    expect(screen.getByTestId("backups-location").textContent).toBe(`/app/backups/data${filesSearch}`);
  });

  it("restores changed Files query after programmatic Recovery entry", async () => {
    const user = userEvent.setup();
    const changedRepositoryId = "c".repeat(32);
    const changedRecoveryPointId = "d".repeat(32);
    const changedParentEntryId = "f".repeat(64);
    const changedFilesHref =
      `/app/backups/data?repositoryId=${changedRepositoryId}` +
      `&recoveryPointId=${changedRecoveryPointId}` +
      `&parentEntryId=${changedParentEntryId}`;
    const changedFilesSearch = changedFilesHref.slice("/app/backups/data".length);
    const planId = "1".repeat(32);
    const jobId = "2".repeat(32);
    const recoveryHref =
      `/app/backups/recovery?recoveryPointId=${changedRecoveryPointId}&planId=${planId}`;
    const recoveryPatchHref = `${recoveryHref}&jobId=${jobId}`;
    expect(parseBackupAssetsRoute("/app/backups/data", filesSearch)).toMatchObject({
      status: "valid",
      state: { page: "data", view: "search", savedSearchId: filesSavedSearchId },
    });
    expect(parseBackupAssetsRoute("/app/backups/data", changedFilesSearch)).toMatchObject({
      status: "valid",
      state: {
        page: "data",
        repositoryId: changedRepositoryId,
        recoveryPointId: changedRecoveryPointId,
        parentEntryId: changedParentEntryId,
      },
    });

    render(
      <MemoryRouter initialEntries={[`/app/backups/data${filesSearch}`]}>
        <Routes>
          <Route path="/app/backups" element={<BackupsPage />}>
            <Route
              path="data"
              element={
                <div>
                  <RoutePatchButton to={changedFilesHref}>Change files query</RoutePatchButton>
                  <RoutePatchButton to={recoveryHref}>Enter recovery</RoutePatchButton>
                </div>
              }
            />
            <Route path="overview" element={<div>overview-panel</div>} />
            <Route
              path="recovery"
              element={<RoutePatchButton to={recoveryPatchHref}>Patch recovery</RoutePatchButton>}
            />
          </Route>
        </Routes>
        <LocationProbe />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole("button", { name: "Change files query" }));
    expect(screen.getByTestId("backups-location").textContent).toBe(changedFilesHref);

    await user.click(screen.getByRole("button", { name: "Enter recovery" }));
    expect(screen.getByTestId("backups-location").textContent).toBe(recoveryHref);
    expect(screen.getByRole("tab", { name: /Files|文件/ })).toHaveAttribute("href", changedFilesHref);

    await user.click(screen.getByRole("button", { name: "Patch recovery" }));
    expect(screen.getByTestId("backups-location").textContent).toBe(recoveryPatchHref);
    expect(screen.getByRole("tab", { name: /Files|文件/ })).toHaveAttribute("href", changedFilesHref);

    await user.click(screen.getByRole("tab", { name: /Files|文件/ }));
    expect(screen.getByTestId("backups-location").textContent).toBe(changedFilesHref);
  });

  it("does not leak Overview or Recovery query params into Files", async () => {
    const user = userEvent.setup();
    const recovery = renderBackupTabs("/app/backups/recovery?planId=abc&recoveryPointId=def");
    expect(screen.getByRole("tab", { name: /Files|文件/ })).toHaveAttribute("href", "/app/backups/data");
    expect(screen.getByRole("tab", { name: /Overview|概览/ })).toHaveAttribute("href", "/app/backups/overview");
    await user.click(screen.getByRole("tab", { name: /Files|文件/ }));
    expect(screen.getByTestId("backups-location").textContent).toBe("/app/backups/data");
    recovery.unmount();

    renderBackupTabs("/app/backups/overview?foo=1");
    expect(screen.getByRole("tab", { name: /Files|文件/ })).toHaveAttribute("href", "/app/backups/data");
    await user.click(screen.getByRole("tab", { name: /Files|文件/ }));
    expect(screen.getByTestId("backups-location").textContent).toBe("/app/backups/data");
  });
});

function renderBackups(
  initialEntry: string | string[],
  initialIndex?: number,
  options?: { strict?: boolean },
) {
  const initialEntries = Array.isArray(initialEntry) ? initialEntry : [initialEntry];
  const tree = (
    <MemoryRouter initialEntries={initialEntries} initialIndex={initialIndex}>
      <Routes>
        <Route path="/app/backups" element={<BackupsPage />}>
          <Route index element={<Navigate to="data" replace />} />
          <Route path="overview" element={<BackupsOverviewPage />} />
          <Route path="data" element={<BackupsDataPage />} />
          <Route path="recovery" element={<BackupsRecoveryPage />} />
        </Route>
      </Routes>
      <LocationProbe />
    </MemoryRouter>
  );
  return render(options?.strict ? <StrictMode>{tree}</StrictMode> : tree);
}

function renderBackupTabs(initialEntry: string) {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/app/backups" element={<BackupsPage />}>
          <Route path="data" element={<div>files-panel</div>} />
          <Route path="overview" element={<div>overview-panel</div>} />
          <Route path="recovery" element={<div>recovery-panel</div>} />
        </Route>
      </Routes>
      <LocationProbe />
    </MemoryRouter>,
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

function RoutePatchButton({ to, children }: { to: string; children: string }) {
  const navigate = useNavigate();
  return (
    <button type="button" onClick={() => navigate(to)}>
      {children}
    </button>
  );
}
