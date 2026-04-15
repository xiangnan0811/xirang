import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { SSHKeysPage } from "./ssh-keys-page";

const confirmMock = vi.fn().mockResolvedValue(true);

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const sshKeysRef: { current: Record<string, unknown> } = { current: {} };

function createMemoryStorage() {
  const store = new Map<string, string>();
  return {
    clear: () => store.clear(),
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => Array.from(store.keys())[index] ?? null,
    removeItem: (key: string) => store.delete(key),
    setItem: (key: string, value: string) => store.set(key, value),
    get length() {
      return store.size;
    },
  } satisfies Storage;
}

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return { ...actual };
});

vi.mock("@/context/shared-context", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/ssh-keys-context", () => ({
  useSSHKeysContext: () => sshKeysRef.current,
}));

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

vi.mock("@/components/ssh-key-editor-dialog", () => ({
  SSHKeyEditorDialog: () => null,
}));

vi.mock("@/components/ssh-key-test-connection-dialog", () => ({
  SSHKeyTestConnectionDialog: () => null,
}));

vi.mock("@/components/ssh-key-associated-nodes-sheet", () => ({
  SSHKeyAssociatedNodesSheet: () => null,
}));

vi.mock("@/components/ssh-key-batch-import-dialog", () => ({
  SSHKeyBatchImportDialog: () => null,
}));

vi.mock("@/components/ssh-key-export-dialog", () => ({
  SSHKeyExportDialog: () => null,
}));

vi.mock("@/components/ssh-key-rotation/ssh-key-rotation-wizard", () => ({
  SSHKeyRotationWizard: () => null,
}));

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
}));

vi.mock("@/components/ui/toast", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

const refreshSSHKeysMock = vi.fn().mockResolvedValue(undefined);
const refreshNodesMock = vi.fn().mockResolvedValue(undefined);

function createContext(overrides?: Record<string, unknown>) {
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
    nodes: [
      {
        id: 1,
        name: "node-1",
        host: "10.0.0.1",
        ip: "10.0.0.1",
        port: 22,
        username: "root",
        authType: "key",
        keyId: "key-1",
        tags: [],
        status: "online" as const,
        lastSeenAt: "",
        lastBackupAt: "",
        diskFreePercent: 80,
        diskUsedGb: 100,
        diskTotalGb: 500,
        diskProbeAt: "",
        connectionLatencyMs: 10,
      },
    ],
    refreshNodes: refreshNodesMock,
    createNode: vi.fn(),
    updateNode: vi.fn(),
    deleteNode: vi.fn(),
    deleteNodes: vi.fn(),
    testNodeConnection: vi.fn(),
    triggerNodeBackup: vi.fn(),
  };
  sshKeysRef.current = {
    sshKeys: [
      {
        id: "key-1",
        name: "生产密钥",
        username: "root",
        keyType: "ed25519",
        fingerprint: "SHA256:abc123",
        createdAt: "2026-01-01 00:00:00",
        lastUsedAt: "2026-03-20 10:00:00",
      },
      {
        id: "key-2",
        name: "测试密钥",
        username: "deploy",
        keyType: "rsa",
        fingerprint: "SHA256:def456",
        createdAt: "2026-02-01 00:00:00",
        lastUsedAt: null,
      },
    ],
    createSSHKey: vi.fn().mockResolvedValue(undefined),
    updateSSHKey: vi.fn().mockResolvedValue(undefined),
    deleteSSHKey: vi.fn().mockResolvedValue(true),
    refreshSSHKeys: refreshSSHKeysMock,
    ...(overrides?.sshKeys !== undefined ? { sshKeys: overrides.sshKeys } : {}),
  };
}

describe("SSHKeysPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    confirmMock.mockClear();
    refreshSSHKeysMock.mockClear();
    refreshNodesMock.mockClear();
    createContext();
  });

  it("mount 时同时刷新 SSH Keys 和 Nodes 数据", () => {
    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <SSHKeysPage />
      </MemoryRouter>
    );

    expect(refreshSSHKeysMock).toHaveBeenCalledTimes(1);
    expect(refreshNodesMock).toHaveBeenCalledTimes(1);
  });

  it("渲染密钥列表并正确显示节点使用数", () => {
    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <SSHKeysPage />
      </MemoryRouter>
    );

    // 卡片视图 + 表格视图会各渲染一份，使用 getAllByText
    expect(screen.getAllByText("生产密钥").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("测试密钥").length).toBeGreaterThanOrEqual(1);
  });

  it("无密钥时显示空态", () => {
    createContext({ sshKeys: [] as unknown as Record<string, unknown>[] });

    render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <SSHKeysPage />
      </MemoryRouter>
    );

    // 卡片视图 + 表格视图均渲染空态
    expect(screen.getAllByText("当前还没有 SSH Key").length).toBeGreaterThanOrEqual(1);
  });
});
