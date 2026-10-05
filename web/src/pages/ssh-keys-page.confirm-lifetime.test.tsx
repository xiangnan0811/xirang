import { StrictMode } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { SSHKeysPage } from "./ssh-keys-page";

const authRef: {
  current: {
    token: string | null;
    role: "admin" | "operator" | "viewer" | null;
    username: string | null;
    userId: number | null;
    totpEnabled: boolean;
    isAuthenticated: boolean;
    login: () => void;
    logout: () => void;
    setTotpEnabled: (enabled: boolean) => void;
    ensureStepUpProof: (action: string) => Promise<string>;
    clearStepUpProof: () => void;
  };
} = {
  current: {
    token: "test-token",
    role: "admin",
    username: "admin",
    userId: 1,
    totpEnabled: false,
    isAuthenticated: true,
    login: vi.fn(),
    logout: vi.fn(),
    setTotpEnabled: vi.fn(),
    ensureStepUpProof: vi.fn(async () => "proof"),
    clearStepUpProof: vi.fn(),
  },
};

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
  },
}));

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const sshKeysRef: { current: Record<string, unknown> } = { current: {} };
const deleteSSHKey = vi.fn().mockResolvedValue(true);

vi.mock("@/context/shared-context.hooks", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/ssh-keys-context.hooks", () => ({
  useSSHKeysContext: () => sshKeysRef.current,
}));

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

function createContext() {
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
        connectionLatencyMs: 10,
      },
    ],
    refreshNodes: vi.fn().mockResolvedValue(undefined),
    nodesLoading: false,
    nodesError: null,
    nodesLoaded: true,
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
        publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG... root@prod",
        fingerprint: "SHA256:abc123",
        createdAt: "2026-01-01 00:00:00",
        lastUsedAt: "2026-03-20 10:00:00",
      },
    ],
    createSSHKey: vi.fn().mockResolvedValue(undefined),
    updateSSHKey: vi.fn().mockResolvedValue(undefined),
    deleteSSHKey,
    refreshSSHKeys: vi.fn().mockResolvedValue(undefined),
  };
}

function renderPage() {
  return (
    <StrictMode>
      <MemoryRouter>
        <SSHKeysPage />
      </MemoryRouter>
    </StrictMode>
  );
}

async function openDeleteConfirm(user: UserEvent) {
  await user.click(screen.getAllByRole("button", { name: "操作" })[0]);
  await user.click(screen.getByRole("menuitem", { name: "删除" }));
  const alert = await screen.findByRole("alertdialog");
  expect(alert).toHaveTextContent("确认删除");
  expect(alert).toHaveTextContent("生产密钥");
  return alert;
}

describe("SSHKeysPage real confirm lifetime", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
    authRef.current = {
      ...authRef.current,
      token: "test-token",
      role: "admin",
    };
    deleteSSHKey.mockClear();
    createContext();
  });

  it("删除确认在角色降级后再恢复时消失且不会复活", async () => {
    const user = userEvent.setup();
    const view = render(renderPage());
    await openDeleteConfirm(user);

    authRef.current = { ...authRef.current, role: "viewer" };
    view.rerender(renderPage());
    await waitFor(() => {
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    });

    authRef.current = { ...authRef.current, role: "admin" };
    view.rerender(renderPage());
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(deleteSSHKey).not.toHaveBeenCalled();

    const alert = await openDeleteConfirm(user);
    await user.click(within(alert).getByRole("button", { name: "确认" }));
    await waitFor(() => expect(deleteSSHKey).toHaveBeenCalledTimes(1));
    expect(deleteSSHKey).toHaveBeenCalledWith("key-1");
  });

  it("删除确认在令牌变化后消失，恢复原令牌也不会重新打开", async () => {
    const user = userEvent.setup();
    const view = render(renderPage());
    await openDeleteConfirm(user);

    authRef.current = { ...authRef.current, token: "token-b" };
    view.rerender(renderPage());
    await waitFor(() => {
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    });

    authRef.current = { ...authRef.current, token: "test-token" };
    view.rerender(renderPage());
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(deleteSSHKey).not.toHaveBeenCalled();
  });
});
