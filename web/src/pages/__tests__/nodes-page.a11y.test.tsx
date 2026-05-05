import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { runAxe } from "@/test/a11y-helpers";

// Wave 4 PR-C：nodes 页 a11y smoke 测试。
// PR-D: 改用 runAxe 共享辅助（默认关闭 color-contrast，详见 a11y-helpers.ts）。

const { toastSuccessMock, toastErrorMock } = vi.hoisted(() => ({
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
}));

const searchParamsRef = { current: new URLSearchParams() };
const setSearchParamsMock = vi.fn();
const confirmMock = vi.fn().mockResolvedValue(true);
const navigateMock = vi.fn();

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useSearchParams: () => [searchParamsRef.current, setSearchParamsMock] as const,
    useNavigate: () => navigateMock,
  };
});

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const sshKeysRef: { current: Record<string, unknown> } = { current: {} };

vi.mock("@/context/shared-context", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/ssh-keys-context", () => ({
  useSSHKeysContext: () => sshKeysRef.current,
}));

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
}));

vi.mock("@/components/node-editor-dialog", () => ({
  NodeEditorDialog: () => null,
}));

vi.mock("@/components/ui/toast", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({
    token: "test-token",
    username: "admin",
    role: "admin",
    userId: 1,
    isAuthenticated: true,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}));

function buildContext() {
  const nodes = [
    {
      id: 1,
      name: "node-prod-1",
      host: "node-prod-1.example.com",
      address: "10.0.0.1",
      ip: "10.0.0.1",
      port: 22,
      username: "root",
      authType: "key",
      keyId: "key-1",
      tags: ["prod"],
      status: "online" as const,
      lastSeenAt: "2026-02-24 12:00:00",
      lastBackupAt: "2026-02-24 11:50:00",
      diskFreePercent: 60,
      diskUsedGb: 40,
      diskTotalGb: 100,
      diskProbeAt: "2026-02-24 11:55:00",
      connectionLatencyMs: 12,
    },
  ];

  sharedRef.current = {
    loading: false,
    globalSearch: "",
    setGlobalSearch: vi.fn(),
    warning: null,
    lastSyncedAt: "",
    refreshVersion: 0,
    refresh: vi.fn(),
    overview: {},
    fetchOverviewTraffic: vi.fn(),
  };
  nodesRef.current = {
    nodes,
    createNode: vi.fn().mockResolvedValue(1),
    updateNode: vi.fn().mockResolvedValue(undefined),
    deleteNode: vi.fn().mockResolvedValue(undefined),
    deleteNodes: vi.fn().mockResolvedValue({ deleted: 0, notFoundIds: [] }),
    testNodeConnection: vi.fn().mockResolvedValue({ ok: true, message: "连接成功" }),
    triggerNodeBackup: vi.fn().mockResolvedValue(undefined),
    refreshNodes: vi.fn().mockResolvedValue(undefined),
  };
  sshKeysRef.current = {
    sshKeys: [{ id: "key-1", name: "主机密钥" }],
    refreshSSHKeys: vi.fn().mockResolvedValue(undefined),
    createSSHKey: vi.fn(),
    updateSSHKey: vi.fn(),
    deleteSSHKey: vi.fn(),
  };
}

import { NodesPage } from "../nodes-page";

describe("NodesPage a11y smoke", () => {
  beforeEach(() => {
    confirmMock.mockClear();
    navigateMock.mockReset();
    setSearchParamsMock.mockReset();
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
    searchParamsRef.current = new URLSearchParams();
    buildContext();
  });

  it("初始渲染无 axe violations（关 color-contrast）", async () => {
    const { container } = render(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>
    );

    const results = await runAxe(container);
    expect(results).toHaveNoViolations();
  });
});
