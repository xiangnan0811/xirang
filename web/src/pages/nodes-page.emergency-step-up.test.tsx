import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { NodesPage } from "./nodes-page";
import { ApiError, bumpAuthSessionGeneration } from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";

const ONE_SHOT = { persist: false, reuseCached: false };
const PROOF = "fresh-manual-proof";

const {
  toastSuccessMock,
  toastErrorMock,
  authRef,
  getTasksMock,
  grantMock,
  emergencyBackupMock,
  ensureStepUpProofMock,
  clearStepUpProofMock,
} = vi.hoisted(() => ({
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
  authRef: { current: { role: "admin" as const, token: "test-token" } },
  getTasksMock: vi.fn(),
  grantMock: vi.fn(),
  emergencyBackupMock: vi.fn(),
  ensureStepUpProofMock: vi.fn(),
  clearStepUpProofMock: vi.fn(),
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

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function createStepUpRequiredError() {
  return new ApiError(403, "需要二次验证", {
    code: 403,
    message: "需要二次验证",
    data: { error_code: "STEP_UP_REQUIRED", proof_ttl_seconds: 300 },
  });
}

const confirmMock = vi.fn().mockResolvedValue(true);
const searchParamsRef = { current: new URLSearchParams() };
const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const sshKeysRef: { current: Record<string, unknown> } = { current: {} };

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return {
    ...actual,
    useSearchParams: () => [searchParamsRef.current, vi.fn()] as const,
    useNavigate: () => vi.fn(),
  };
});

vi.mock("@/context/shared-context.hooks", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/ssh-keys-context.hooks", () => ({
  useSSHKeysContext: () => sshKeysRef.current,
}));
vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({ confirm: confirmMock, dialog: null }),
}));
vi.mock("@/components/node-editor-dialog", () => ({
  NodeEditorDialog: () => null,
}));
vi.mock("@/components/web-terminal", () => ({
  default: () => null,
}));
vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { success: toastSuccessMock, error: toastErrorMock },
}));
vi.mock("@/lib/api/client", () => ({
  apiClient: {
    runNodeDoctor: vi.fn(),
    trustNodeHostKey: vi.fn(),
    getTasks: getTasksMock,
    requestTaskManualTriggerCredentialGrant: grantMock,
    emergencyBackup: emergencyBackupMock,
  },
}));
vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: authRef.current.token,
    username: authRef.current.role,
    role: authRef.current.role,
    userId: 1,
    isAuthenticated: true,
    login: vi.fn(),
    logout: vi.fn(),
    ensureStepUpProof: ensureStepUpProofMock,
    clearStepUpProof: clearStepUpProofMock,
  }),
}));

function policyTask(id: number) {
  return {
    id,
    policyName: "nightly",
    nodeName: "node-prod-1",
    nodeId: 1,
    status: "pending" as const,
    progress: 0,
    startedAt: "",
    speedMbps: 0,
    enabled: true,
    source: "policy" as const,
    executorType: "rsync" as const,
  };
}

function installPageContext() {
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
      connectionLatencyMs: 12,
    }],
    createNode: vi.fn(),
    updateNode: vi.fn(),
    deleteNode: vi.fn(),
    deleteNodes: vi.fn(),
    testNodeConnection: vi.fn(),
    triggerNodeBackup: vi.fn(),
    refreshNodes: vi.fn().mockResolvedValue(undefined),
    nodesLoading: false,
    nodesError: null,
    nodesLoaded: true,
  };
  sshKeysRef.current = {
    sshKeys: [{ id: "key-1", name: "主机密钥" }],
    refreshSSHKeys: vi.fn().mockResolvedValue(undefined),
    createSSHKey: vi.fn(),
    updateSSHKey: vi.fn(),
    deleteSSHKey: vi.fn(),
  };
}

function renderNodesPage() {
  return render(
    <MemoryRouter>
      <NodesPage />
    </MemoryRouter>,
  );
}

async function clickEmergency(user: ReturnType<typeof userEvent.setup>) {
  const card = screen.getByLabelText("节点卡片 node-prod-1");
  await user.click(within(card).getByRole("button", { name: "紧急备份" }));
}

function expectNoStepUpPromptOrFollowUp() {
  expect(ensureStepUpProofMock).not.toHaveBeenCalled();
  expect(clearStepUpProofMock).not.toHaveBeenCalled();
  expect(toastSuccessMock).not.toHaveBeenCalled();
  expect(toastErrorMock).not.toHaveBeenCalled();
}

describe("emergency backup stale step-up", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    confirmMock.mockReset();
    confirmMock.mockResolvedValue(true);
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
    getTasksMock.mockReset();
    grantMock.mockReset();
    emergencyBackupMock.mockReset();
    ensureStepUpProofMock.mockReset();
    clearStepUpProofMock.mockReset();
    ensureStepUpProofMock.mockResolvedValue(PROOF);
    authRef.current = { role: "admin", token: "test-token" };
    searchParamsRef.current = new URLSearchParams();
    installPageContext();
  });

  it("卸载后迟到的授权 STEP_UP_REQUIRED 不再打开全局验证", async () => {
    const user = userEvent.setup();
    const pendingGrant = createDeferred<unknown>();
    getTasksMock.mockResolvedValue([policyTask(7)]);
    grantMock.mockReturnValueOnce(pendingGrant.promise);
    emergencyBackupMock.mockResolvedValue({ triggered: 1, taskIds: [7], errors: [] });

    const view = renderNodesPage();
    await clickEmergency(user);
    await waitFor(() => {
      expect(grantMock).toHaveBeenCalledTimes(1);
    });
    expect(grantMock).toHaveBeenCalledWith("test-token", {
      taskId: 7,
      reason: "手动触发任务 #7",
      requestedTtlSeconds: 600,
    }, undefined);

    view.unmount();
    await act(async () => {
      pendingGrant.reject(createStepUpRequiredError());
    });

    expectNoStepUpPromptOrFollowUp();
    expect(grantMock).toHaveBeenCalledTimes(1);
    expect(emergencyBackupMock).not.toHaveBeenCalled();
  });

  it("会话切换后迟到的授权 STEP_UP_REQUIRED 不再打开全局验证", async () => {
    const user = userEvent.setup();
    const pendingGrant = createDeferred<unknown>();
    getTasksMock.mockResolvedValue([policyTask(7)]);
    grantMock.mockReturnValueOnce(pendingGrant.promise);
    emergencyBackupMock.mockResolvedValue({ triggered: 1, taskIds: [7], errors: [] });

    renderNodesPage();
    await clickEmergency(user);
    await waitFor(() => {
      expect(grantMock).toHaveBeenCalledTimes(1);
    });

    bumpAuthSessionGeneration();
    await act(async () => {
      pendingGrant.reject(createStepUpRequiredError());
    });

    expectNoStepUpPromptOrFollowUp();
    expect(grantMock).toHaveBeenCalledTimes(1);
    expect(emergencyBackupMock).not.toHaveBeenCalled();
  });

  it("卸载后空任务紧急备份的 STEP_UP_REQUIRED 不再打开全局验证", async () => {
    const user = userEvent.setup();
    const pendingBackup = createDeferred<{ triggered: number; taskIds: number[]; errors: string[] }>();
    getTasksMock.mockResolvedValue([]);
    emergencyBackupMock.mockReturnValueOnce(pendingBackup.promise);

    const view = renderNodesPage();
    await clickEmergency(user);
    await waitFor(() => {
      expect(emergencyBackupMock).toHaveBeenCalledTimes(1);
    });
    expect(emergencyBackupMock).toHaveBeenCalledWith("test-token", 1, undefined);
    expect(grantMock).not.toHaveBeenCalled();

    view.unmount();
    await act(async () => {
      pendingBackup.reject(createStepUpRequiredError());
    });

    expectNoStepUpPromptOrFollowUp();
    expect(grantMock).not.toHaveBeenCalled();
    expect(emergencyBackupMock).toHaveBeenCalledTimes(1);
  });

  it("操作仍有效时授权 STEP_UP_REQUIRED 只挑战一次并带 proof 重试", async () => {
    const user = userEvent.setup();
    const pendingGrant = createDeferred<unknown>();
    getTasksMock.mockResolvedValue([policyTask(7)]);
    grantMock
      .mockReturnValueOnce(pendingGrant.promise)
      .mockResolvedValueOnce({ id: 1, status: "active" });
    emergencyBackupMock.mockResolvedValue({ triggered: 1, taskIds: [7], errors: [] });

    renderNodesPage();
    await clickEmergency(user);
    await waitFor(() => {
      expect(grantMock).toHaveBeenCalledTimes(1);
    });

    await act(async () => {
      pendingGrant.reject(createStepUpRequiredError());
    });

    await waitFor(() => {
      expect(emergencyBackupMock).toHaveBeenCalledTimes(1);
    });
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.taskManualTrigger, ONE_SHOT);
    expect(clearStepUpProofMock).not.toHaveBeenCalled();
    expect(grantMock).toHaveBeenCalledTimes(2);
    expect(grantMock).toHaveBeenNthCalledWith(2, "test-token", {
      taskId: 7,
      reason: "手动触发任务 #7",
      requestedTtlSeconds: 600,
    }, PROOF);
    expect(emergencyBackupMock).toHaveBeenCalledWith("test-token", 1, PROOF);
    expect(toastSuccessMock).toHaveBeenCalledWith("紧急备份已触发：1 个任务已启动。");
    expect(toastErrorMock).not.toHaveBeenCalled();
  });
});
