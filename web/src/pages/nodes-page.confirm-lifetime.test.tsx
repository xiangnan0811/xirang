import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { NodesPage } from "./nodes-page";

const { authRef } = vi.hoisted(() => ({
  authRef: {
    current: {
      role: "admin" as "admin" | "operator" | "viewer",
      token: "test-token",
    },
  },
}));

const nodesRef: { current: Record<string, unknown> } = { current: {} };
const sshKeysRef: { current: Record<string, unknown> } = { current: {} };
const sharedRef: { current: Record<string, unknown> } = { current: {} };

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: authRef.current.token,
    username: "admin",
    role: authRef.current.role,
    userId: 1,
    isAuthenticated: true,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}));

vi.mock("@/context/shared-context.hooks", () => ({
  useSharedContext: () => sharedRef.current,
}));

vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => nodesRef.current,
}));

vi.mock("@/context/ssh-keys-context.hooks", () => ({
  useSSHKeysContext: () => sshKeysRef.current,
}));

vi.mock("@/components/node-editor-dialog", () => ({
  NodeEditorDialog: () => null,
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
  },
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    runNodeDoctor: vi.fn(),
    trustNodeHostKey: vi.fn(),
    getTasks: vi.fn(),
    getBatchStatus: vi.fn(),
    requestTaskManualTriggerCredentialGrant: vi.fn(),
    emergencyBackup: vi.fn(),
  },
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

function renderNodesPage() {
  return render(
    <MemoryRouter>
      <NodesPage />
    </MemoryRouter>,
  );
}

describe("NodesPage confirm lifetime", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    authRef.current = { role: "admin", token: "test-token" };
    sharedRef.current = {
      globalSearch: "",
      setGlobalSearch: vi.fn(),
    };
    nodesRef.current = {
      nodes: [{
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
        status: "online",
        lastSeenAt: "2026-02-24 12:00:00",
        lastBackupAt: "2026-02-24 11:50:00",
      }],
      nodesLoading: false,
      nodesError: null,
      nodesLoaded: true,
      createNode: vi.fn(),
      updateNode: vi.fn(),
      deleteNode: vi.fn().mockResolvedValue(undefined),
      deleteNodes: vi.fn(),
      testNodeConnection: vi.fn(),
      triggerNodeBackup: vi.fn(),
      refreshNodes: vi.fn().mockResolvedValue(undefined),
    };
    sshKeysRef.current = {
      sshKeys: [],
      refreshSSHKeys: vi.fn().mockResolvedValue(undefined),
    };
  });

  async function openDeleteConfirm() {
    const user = userEvent.setup();
    const view = renderNodesPage();
    const card = screen.getByLabelText("节点卡片 node-prod-1");
    await user.click(within(card).getByRole("button", { name: /删除节点 node-prod-1/ }));
    expect(await screen.findByRole("alertdialog", { name: "确认操作" })).toBeInTheDocument();
    expect(screen.getByText("确认删除节点 node-prod-1 吗？此操作会移除关联任务记录。")).toBeInTheDocument();
    return view;
  }

  it("角色往返后真实删除确认消失且不再删除", async () => {
    const view = await openDeleteConfirm();
    const deleteNode = nodesRef.current.deleteNode as ReturnType<typeof vi.fn>;

    authRef.current = { role: "viewer", token: "test-token" };
    view.rerender(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>,
    );
    await waitFor(() => {
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    });
    expect(deleteNode).not.toHaveBeenCalled();

    authRef.current = { role: "admin", token: "test-token" };
    view.rerender(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>,
    );
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(deleteNode).not.toHaveBeenCalled();
  });

  it("更换 token 后真实删除确认消失且不再删除", async () => {
    const view = await openDeleteConfirm();
    const deleteNode = nodesRef.current.deleteNode as ReturnType<typeof vi.fn>;

    authRef.current = { role: "admin", token: "next-token" };
    view.rerender(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>,
    );
    await waitFor(() => {
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    });
    expect(deleteNode).not.toHaveBeenCalled();

    authRef.current = { role: "admin", token: "test-token" };
    view.rerender(
      <MemoryRouter>
        <NodesPage />
      </MemoryRouter>,
    );
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(deleteNode).not.toHaveBeenCalled();
  });
});
