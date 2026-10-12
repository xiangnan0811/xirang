import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setLanguage } from "@/i18n";
import { ApiError, beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier, getAuthSessionGeneration, rememberAuthIdentity, releaseAuthTransitionBarrier } from "@/lib/api/core";
import { createBatchApi, type BatchRetryEligibility, type BatchRetryLink, type BatchRetryResult, type BatchStatus } from "@/lib/api/batch-api";
import { BatchRetryContractError } from "@/lib/api/batch-command-response";
import { STEP_UP_ACTIONS } from "@/lib/step-up-storage";
import type { LogEvent } from "@/types/domain";
import { BatchResultDialog } from "./batch-result-dialog";

const COPY_ZH = "复制已加载结果";
const COPY_EN = "Copy loaded results";
const ZH_HINT = "仅复制当前已加载的结果和日志；每个任务最多加载 50 条日志，不代表完整日志。";
const EN_HINT = "Copies only currently loaded results and logs; up to 50 log entries per task, not complete logs.";
const ZH_COPIED = "已复制到剪贴板";
const EN_COPIED = "Copied to clipboard";
const ZH_COPY_FAILED = "复制失败，请手动复制";

type DialogRole = "admin" | "operator" | "viewer" | null;
type ClipboardOutcome = "resolve" | "reject";
type ExpectedLogs =
  | { kind: "unloaded" }
  | { kind: "loading" }
  | { kind: "error" }
  | { kind: "loaded"; messages: string[] };

const { getBatchStatusMock, getTaskLogsMock, deleteBatchMock, retryBatchCommandMock, retryGrantMock, ensureStepUpProofMock, clearStepUpProofMock, onOpenChangeMock, authRef, toastSuccess, toastError } = vi.hoisted(() => ({
  getBatchStatusMock: vi.fn(),
  getTaskLogsMock: vi.fn(),
  deleteBatchMock: vi.fn(),
  retryBatchCommandMock: vi.fn(),
  retryGrantMock: vi.fn(),
  ensureStepUpProofMock: vi.fn(),
  clearStepUpProofMock: vi.fn(),
  onOpenChangeMock: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
  authRef: {
    current: {
      role: "admin" as DialogRole,
      token: "auth-marker" as string,
      totpEnabled: true,
    },
  },
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getBatchStatus: getBatchStatusMock,
    getTaskLogs: getTaskLogsMock,
    deleteBatch: deleteBatchMock,
    retryBatchCommand: retryBatchCommandMock,
    requestBatchCommandRetryCredentialGrant: retryGrantMock,
  },
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: toastSuccess,
    error: toastError,
  },
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: authRef.current.token,
    username: "tester",
    role: authRef.current.role,
    userId: 1,
    isAuthenticated: true,
    login: vi.fn(),
    logout: vi.fn(),
    setTotpEnabled: vi.fn(),
    totpEnabled: authRef.current.totpEnabled,
    ensureStepUpProof: ensureStepUpProofMock,
    clearStepUpProof: clearStepUpProofMock,
  }),
}));

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function batchStatus(tasks: BatchStatus["tasks"], statusCounts: Record<string, number> = {}, total = tasks.length): BatchStatus {
  return {
    batchId: "batch-1",
    total,
    statusCounts,
    tasks,
  };
}

function taskOf(options: {
  id: number;
  nodeId: number;
  nodeName: string;
  status: string;
  dispatchStatus?: "pending" | "dispatching" | "accepted" | "failed";
  lastError?: string;
  name?: string;
}): BatchStatus["tasks"][number] {
  return {
    id: options.id,
    name: options.name ?? `task-${options.id}`,
    status: options.status,
    nodeId: options.nodeId,
    nodeName: options.nodeName,
    dispatchStatus: options.dispatchStatus ?? "accepted",
    ...(options.lastError ? { lastError: options.lastError } : {}),
  };
}

function oneNode(nodeName: string, id = 1): BatchStatus {
  return batchStatus([
    taskOf({ id, nodeId: id, nodeName, status: "success" }),
  ], { success: 1 });
}

function logEvent(id: string, message: string): LogEvent {
  return { id, timestamp: "2026-09-08T00:00:00Z", level: "info", message };
}

function publishAuth(role: DialogRole, token = "auth-marker", totpEnabled = true) {
  authRef.current = { role, token, totpEnabled };
  rememberAuthIdentity(token, role);
}

function resultElement(retain = false, token = "auth-marker", batchId = "batch-1") {
  return (
    <BatchResultDialog
      open
      onOpenChange={onOpenChangeMock}
      batchId={batchId}
      retain={retain}
      token={token}
    />
  );
}

function renderDialog(retain = false, token = "auth-marker", batchId = "batch-1") {
  return render(resultElement(retain, token, batchId));
}

function loadedLogLine(lang: "zh" | "en", count: number) {
  return lang === "zh"
    ? `已加载日志：${count} 条（每任务最多 50 条，非完整日志）`
    : `Loaded log entries: ${count} (up to 50 per task; not complete logs)`;
}

function buildCopyText(
  lang: "zh" | "en",
  batchId: string,
  loaded: number,
  total: number,
  tasks: Array<{
    id: number;
    nodeId: number;
    nodeName: string;
    statusLabel: string;
    lastError?: string;
    logs: ExpectedLogs;
  }>,
) {
  const header = [
    lang === "zh" ? "批量执行结果" : "Batch Execution Results",
    lang === "zh" ? `批次 ID：${batchId}` : `Batch ID: ${batchId}`,
    lang === "zh" ? ZH_HINT : EN_HINT,
    lang === "zh" ? `已加载结果：${loaded} / ${total}` : `Loaded results: ${loaded} / ${total}`,
  ].join("\n");
  const sections = tasks.map((task) => {
    const lines = [
      lang === "zh"
        ? `节点：${JSON.stringify(task.nodeName)}（ID：${task.nodeId}）`
        : `Node: ${JSON.stringify(task.nodeName)} (ID: ${task.nodeId})`,
      lang === "zh" ? `任务 ID：${task.id}` : `Task ID: ${task.id}`,
      lang === "zh" ? `状态：${task.statusLabel}` : `Status: ${task.statusLabel}`,
    ];
    if (task.lastError) lines.push(lang === "zh" ? `错误：${task.lastError}` : `Error: ${task.lastError}`);
    if (task.logs.kind === "unloaded") lines.push(lang === "zh" ? "日志尚未加载" : "Logs not loaded");
    else if (task.logs.kind === "loading") lines.push(lang === "zh" ? "加载日志中..." : "Loading logs...");
    else if (task.logs.kind === "error") lines.push(lang === "zh" ? "日志加载失败" : "Failed to load logs");
    else {
      lines.push(loadedLogLine(lang, task.logs.messages.length));
      lines.push(...task.logs.messages);
    }
    return lines.join("\n");
  });
  return [header, ...sections].join("\n\n");
}

type PendingWrite = {
  text: string;
  resolve: () => void;
  reject: (error: unknown) => void;
};

function installResolvedClipboard() {
  const writeText = vi.fn<(text: string) => Promise<void>>().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  return writeText;
}

function installDeferredClipboard() {
  const writes: PendingWrite[] = [];
  const writeText = vi.fn((text: string) => {
    const deferred = createDeferred<void>();
    writes.push({ text, resolve: deferred.resolve, reject: deferred.reject });
    return deferred.promise;
  });
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  return { writeText, writes };
}

async function settleWrite(write: PendingWrite, outcome: ClipboardOutcome) {
  await act(async () => {
    if (outcome === "resolve") write.resolve();
    else write.reject(new Error("clipboard denied"));
    await Promise.resolve();
  });
}

function expectNoCopyToast() {
  expect(toastSuccess).not.toHaveBeenCalled();
  expect(toastError).not.toHaveBeenCalled();
}

async function enabledCopyButton(name = COPY_ZH) {
  const button = await screen.findByRole("button", { name });
  await waitFor(() => expect(button).toBeEnabled());
  return button;
}

type DeferredClipboard = ReturnType<typeof installDeferredClipboard>;

async function clickDeferredCopy(user: UserEvent) {
  await enabledCopyButton();
  const clipboard = installDeferredClipboard();
  await user.click(screen.getByRole("button", { name: COPY_ZH }));
  expect(clipboard.writeText).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
  expectNoCopyToast();
  return clipboard;
}

async function clickAnotherCopy(user: UserEvent, clipboard: DeferredClipboard) {
  const before = clipboard.writeText.mock.calls.length;
  await enabledCopyButton();
  await user.click(screen.getByRole("button", { name: COPY_ZH }));
  expect(clipboard.writeText).toHaveBeenCalledTimes(before + 1);
  expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
  const write = clipboard.writes[before];
  if (!write) throw new Error("missing clipboard write");
  return write;
}

async function readCopy(
  user: UserEvent,
  writeText: ReturnType<typeof installResolvedClipboard>,
  name = COPY_ZH,
) {
  const before = writeText.mock.calls.length;
  await user.click(screen.getByRole("button", { name }));
  await waitFor(() => expect(writeText).toHaveBeenCalledTimes(before + 1));
  return String(writeText.mock.calls[before]?.[0]);
}

function installBatchPollSpy() {
  const polls = new Map<number, () => void>();
  let nextId = 1;
  const windowHadOwnInterval = window.setInterval !== globalThis.setInterval;
  const previousWindowSetInterval = window.setInterval;
  const previousWindowClearInterval = window.clearInterval;
  const realSetInterval = globalThis.setInterval.bind(globalThis);
  const realClearInterval = globalThis.clearInterval.bind(globalThis);
  const setIntervalSpy = vi.spyOn(globalThis, "setInterval").mockImplementation(((
    handler: TimerHandler,
    timeout?: number,
  ) => {
    if (timeout === 3_000 && typeof handler === "function") {
      const id = nextId;
      nextId += 1;
      polls.set(id, () => { handler(); });
      return id;
    }
    return realSetInterval(handler, timeout);
  }) as typeof globalThis.setInterval);
  const clearIntervalSpy = vi.spyOn(globalThis, "clearInterval").mockImplementation(((id?: number) => {
    if (typeof id === "number" && polls.delete(id)) return;
    realClearInterval(id);
  }) as typeof globalThis.clearInterval);
  if (windowHadOwnInterval) {
    window.setInterval = globalThis.setInterval as typeof window.setInterval;
    window.clearInterval = globalThis.clearInterval as typeof window.clearInterval;
  }
  return {
    polls,
    restore() {
      setIntervalSpy.mockRestore();
      clearIntervalSpy.mockRestore();
      if (windowHadOwnInterval) {
        window.setInterval = previousWindowSetInterval;
        window.clearInterval = previousWindowClearInterval;
      }
    },
  };
}

const SOURCE_BATCH_ID = "A".repeat(26);
const CHILD_BATCH_ID = "B".repeat(26);

function stepUpRequiredError() {
  return new ApiError(403, "Additional verification required", { data: { error_code: "STEP_UP_REQUIRED" } });
}

function retrySource(overrides: Partial<BatchRetryEligibility> = {}): BatchRetryEligibility {
  return {
    taskId: 7,
    nodeId: 3,
    runId: 9,
    eligible: true,
    reason: "eligible",
    failureKind: "execution",
    retryBatchId: null,
    ...overrides,
  };
}

function retryBatch(options: {
  tasks?: BatchStatus["tasks"];
  sources?: BatchRetryEligibility[];
  links?: BatchRetryLink[];
  retain?: boolean;
} = {}): BatchStatus {
  const tasks = options.tasks ?? [
    taskOf({ id: 7, nodeId: 3, nodeName: "node-a", status: "failed", lastError: "exit 7" }),
  ];
  return {
    ...batchStatus(tasks, { failed: tasks.length }),
    retain: options.retain ?? false,
    sourceBatchId: null,
    retrySources: options.sources ?? [retrySource()],
    retryLinks: options.links ?? [],
  };
}

function retryResult(retain = true): BatchRetryResult {
  return {
    batchId: CHILD_BATCH_ID,
    sourceBatchId: SOURCE_BATCH_ID,
    taskIds: [70],
    runIds: [90],
    retain,
  };
}

function childBatch(): BatchStatus {
  return batchStatus([
    taskOf({ id: 70, nodeId: 3, nodeName: "child-node", status: "success" }),
  ], { success: 1 });
}

function renderSource(retain = false) {
  return render(resultElement(retain, "auth-marker", SOURCE_BATCH_ID));
}

function dialogFor(batchId: string) {
  const match = screen.getAllByRole("dialog", { hidden: true }).find((dialog) => (
    within(dialog).queryByText(batchId, { exact: false })
  ));
  if (!match) throw new Error(`missing dialog ${batchId}`);
  return match;
}

async function chooseRetry(user: UserEvent, name: RegExp) {
  await user.click(await screen.findByRole("checkbox", { name }));
}

function settledBatch() {
  return batchStatus([
    {
      id: 1,
      name: "t1",
      status: "success",
      nodeId: 1,
      nodeName: "node-a",
      dispatchStatus: "accepted",
    },
    {
      id: 2,
      name: "t2",
      status: "pending",
      nodeId: 2,
      nodeName: "node-b",
      dispatchStatus: "failed",
      lastError: "node unreachable",
    },
  ], { success: 1, pending: 1 });
}

describe("BatchResultDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    publishAuth("admin");
    clearAuthTransitionBarrier();
    getTaskLogsMock.mockResolvedValue([]);
    deleteBatchMock.mockResolvedValue({ deleted: 1 });
    ensureStepUpProofMock.mockResolvedValue("proof-retry");
    retryGrantMock.mockResolvedValue([]);
  });

  afterEach(async () => {
    cleanup();
    vi.useRealTimers();
    clearAuthTransitionBarrier();
    rememberAuthIdentity(null, null);
    await setLanguage("zh");
  });

  it("shows dispatch failure and unconfirmed state without claiming task success", async () => {
    getBatchStatusMock.mockResolvedValue(batchStatus([
      {
        id: 1,
        name: "t1",
        status: "pending",
        nodeId: 1,
        nodeName: "node-a",
        lastError: "node unreachable",
        dispatchStatus: "failed",
      },
      {
        id: 2,
        name: "t2",
        status: "pending",
        nodeId: 2,
        nodeName: "node-b",
        dispatchStatus: "dispatching",
      },
    ], { pending: 2 }));

    const { unmount } = renderDialog();

    const failedRow = await screen.findByRole("button", { name: /node-a/ });
    const pendingRow = screen.getByRole("button", { name: /node-b/ });
    expect(within(failedRow).getByText("派发失败")).toBeInTheDocument();
    expect(within(failedRow).getByText("node unreachable")).toBeInTheDocument();
    expect(within(failedRow).queryByText("成功")).not.toBeInTheDocument();
    expect(within(pendingRow).getByText("派发尚未确认")).toBeInTheDocument();
    expect(pendingRow).toBeDisabled();
    expect(screen.getByText(/运行中/)).toBeInTheDocument();

    unmount();
  });

  it("expands a failed dispatch to show last error without fetching run logs", async () => {
    const user = userEvent.setup();
    getBatchStatusMock.mockResolvedValue(batchStatus([
      {
        id: 1,
        name: "t1",
        status: "pending",
        nodeId: 1,
        nodeName: "node-a",
        lastError: "node unreachable",
        dispatchStatus: "failed",
      },
      {
        id: 2,
        name: "t2",
        status: "success",
        nodeId: 2,
        nodeName: "node-b",
        dispatchStatus: "accepted",
      },
    ], { pending: 1, success: 1 }));
    getTaskLogsMock.mockResolvedValue([
      { id: "1", level: "info", message: "command finished", timestamp: "2026-09-08T00:00:00Z" },
    ]);

    const { unmount } = renderDialog();
    const failedRow = await screen.findByRole("button", { name: /node-a/ });

    await user.click(failedRow);
    expect(getTaskLogsMock).not.toHaveBeenCalled();
    expect(screen.getByText("node unreachable")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /node-b/ }));
    await waitFor(() => expect(getTaskLogsMock).toHaveBeenCalledWith("auth-marker", 2, { limit: 50 }));
    expect(await screen.findByText("command finished")).toBeInTheDocument();

    unmount();
  });

  it("does not auto-delete unfinished or dispatching batches on close", async () => {
    const user = userEvent.setup();
    getBatchStatusMock.mockResolvedValue(batchStatus([
      {
        id: 1,
        name: "t1",
        status: "pending",
        nodeId: 1,
        nodeName: "node-a",
        dispatchStatus: "dispatching",
      },
    ], { pending: 1 }));

    const { unmount } = renderDialog();
    await screen.findByText("派发尚未确认");

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);

    unmount();
  });

  it("deletes a settled unretained batch after every task has a known outcome", async () => {
    const user = userEvent.setup();
    getBatchStatusMock.mockResolvedValue(batchStatus([
      {
        id: 1,
        name: "t1",
        status: "success",
        nodeId: 1,
        nodeName: "node-a",
        dispatchStatus: "accepted",
      },
      {
        id: 2,
        name: "t2",
        status: "pending",
        nodeId: 2,
        nodeName: "node-b",
        dispatchStatus: "failed",
        lastError: "node unreachable",
      },
    ], { success: 1, pending: 1 }));

    const { unmount } = renderDialog();
    await screen.findByText("派发失败");

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).toHaveBeenCalledWith("auth-marker", "batch-1");

    unmount();
  });

  it("lets a viewer read a settled unretained batch and does not delete it on close", async () => {
    const user = userEvent.setup();
    publishAuth("viewer");
    getBatchStatusMock.mockResolvedValue(settledBatch());
    const { unmount } = renderDialog();

    expect(await screen.findByText("派发失败")).toBeInTheDocument();
    expect(getBatchStatusMock).toHaveBeenCalledWith("auth-marker", "batch-1");

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);

    unmount();
  });

  it("deletes a settled unretained batch when the current role is operator", async () => {
    const user = userEvent.setup();
    publishAuth("operator");
    getBatchStatusMock.mockResolvedValue(settledBatch());
    const { unmount } = renderDialog();

    await screen.findByText("派发失败");
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).toHaveBeenCalledWith("auth-marker", "batch-1");
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);

    unmount();
  });

  it("does not delete a settled batch when the role drops to viewer before close", async () => {
    const user = userEvent.setup();
    getBatchStatusMock.mockResolvedValue(settledBatch());
    const view = renderDialog();

    await screen.findByText("派发失败");
    publishAuth("viewer");
    view.rerender(resultElement());
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);

    view.unmount();
  });

  it("does not delete when the token changes before the refreshed status arrives", async () => {
    const user = userEvent.setup();
    const pendingStatus = createDeferred<BatchStatus>();
    getBatchStatusMock.mockResolvedValue(settledBatch());
    const view = renderDialog();

    await screen.findByText("派发失败");
    expect(getBatchStatusMock).toHaveBeenCalledWith("auth-marker", "batch-1");

    getBatchStatusMock.mockReturnValue(pendingStatus.promise);
    view.rerender(resultElement(false, "next-token"));
    expect(screen.queryByText("派发失败")).not.toBeInTheDocument();
    expect(screen.queryByText("node-a")).not.toBeInTheDocument();
    expect(screen.queryByText("node-b")).not.toBeInTheDocument();
    expect(screen.queryByText("node unreachable")).not.toBeInTheDocument();
    expect(screen.getByText("加载中...")).toBeInTheDocument();
    await waitFor(() => expect(getBatchStatusMock).toHaveBeenCalledWith("next-token", "batch-1"));
    expect(screen.queryByText("派发失败")).not.toBeInTheDocument();
    expect(screen.queryByText("node unreachable")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);

    view.unmount();
  });

  it("ignores a status response that arrives after the token changes", async () => {
    const lateStatus = createDeferred<BatchStatus>();
    const freshStatusPromise = createDeferred<BatchStatus>();
    const appliedStatus = batchStatus([
      {
        id: 1,
        name: "t1",
        status: "success",
        nodeId: 1,
        nodeName: "stale-node",
        dispatchStatus: "accepted",
      },
    ], { success: 1 });
    const freshStatus = batchStatus([
      {
        id: 3,
        name: "t3",
        status: "success",
        nodeId: 3,
        nodeName: "fresh-node",
        dispatchStatus: "accepted",
      },
    ], { success: 1 });
    let authMarkerReads = 0;
    getBatchStatusMock.mockImplementation((requestToken: string) => {
      if (requestToken === "next-token") return freshStatusPromise.promise;
      authMarkerReads += 1;
      return authMarkerReads === 1 ? Promise.resolve(appliedStatus) : lateStatus.promise;
    });
    const view = renderDialog();

    expect(await screen.findByText("stale-node")).toBeInTheDocument();
    publishAuth("viewer");
    view.rerender(resultElement());
    await waitFor(() => expect(authMarkerReads).toBe(2));
    expect(screen.getByText("stale-node")).toBeInTheDocument();

    view.rerender(resultElement(false, "next-token"));
    expect(screen.queryByText("stale-node")).not.toBeInTheDocument();
    expect(screen.queryByText("fresh-node")).not.toBeInTheDocument();
    expect(screen.queryByText("late-node")).not.toBeInTheDocument();
    expect(screen.getByText("加载中...")).toBeInTheDocument();

    await act(async () => {
      lateStatus.resolve(batchStatus([
        {
          id: 4,
          name: "t4",
          status: "success",
          nodeId: 4,
          nodeName: "late-node",
          dispatchStatus: "accepted",
        },
      ], { success: 1 }));
    });
    expect(screen.queryByText("stale-node")).not.toBeInTheDocument();
    expect(screen.queryByText("late-node")).not.toBeInTheDocument();
    expect(screen.queryByText("fresh-node")).not.toBeInTheDocument();

    await act(async () => {
      freshStatusPromise.resolve(freshStatus);
    });

    expect(await screen.findByText("fresh-node")).toBeInTheDocument();
    expect(screen.queryByText("stale-node")).not.toBeInTheDocument();
    expect(screen.queryByText("late-node")).not.toBeInTheDocument();

    view.unmount();
  });

  it("does not apply task logs fetched for a previous token", async () => {
    const user = userEvent.setup();
    const lateLogs = createDeferred<Array<{ id: string; level: string; message: string; timestamp: string }>>();
    const nextStatus = createDeferred<BatchStatus>();
    const nextLogs = createDeferred<Array<{ id: string; level: string; message: string; timestamp: string }>>();
    const visibleStatus = batchStatus([
      {
        id: 2,
        name: "t2",
        status: "success",
        nodeId: 2,
        nodeName: "node-b",
        dispatchStatus: "accepted",
      },
      {
        id: 3,
        name: "t3",
        status: "success",
        nodeId: 3,
        nodeName: "node-c",
        dispatchStatus: "accepted",
      },
    ], { success: 2 });
    getBatchStatusMock.mockImplementation((requestToken: string) => {
      if (requestToken === "next-token") return nextStatus.promise;
      return Promise.resolve(visibleStatus);
    });
    getTaskLogsMock.mockImplementation((requestToken: string, taskId: number) => {
      if (requestToken === "next-token") return nextLogs.promise;
      if (taskId === 3) return lateLogs.promise;
      return Promise.resolve([
        { id: "1", level: "info", message: "stale-log", timestamp: "" },
      ]);
    });
    const view = renderDialog();

    await user.click(await screen.findByRole("button", { name: /node-c/ }));
    await waitFor(() => expect(getTaskLogsMock).toHaveBeenCalledWith("auth-marker", 3, { limit: 50 }));
    await user.click(screen.getByRole("button", { name: /node-b/ }));
    expect(await screen.findByText("stale-log")).toBeInTheDocument();

    view.rerender(resultElement(false, "next-token"));
    expect(screen.queryByText("stale-log")).not.toBeInTheDocument();
    expect(screen.queryByText("node-b")).not.toBeInTheDocument();
    expect(screen.queryByText("node-c")).not.toBeInTheDocument();
    expect(getTaskLogsMock).not.toHaveBeenCalledWith("next-token", 2, { limit: 50 });
    expect(getTaskLogsMock).not.toHaveBeenCalledWith("next-token", 3, { limit: 50 });

    await act(async () => {
      lateLogs.resolve([
        { id: "9", level: "info", message: "late-stale-log", timestamp: "" },
      ]);
    });
    expect(screen.queryByText("stale-log")).not.toBeInTheDocument();
    expect(screen.queryByText("late-stale-log")).not.toBeInTheDocument();

    await act(async () => {
      nextStatus.resolve(visibleStatus);
    });
    expect(await screen.findByRole("button", { name: /node-b/ })).toBeInTheDocument();
    expect(screen.queryByText("stale-log")).not.toBeInTheDocument();
    expect(getTaskLogsMock).not.toHaveBeenCalledWith("next-token", 2, { limit: 50 });

    await user.click(screen.getByRole("button", { name: /node-b/ }));
    await waitFor(() => expect(getTaskLogsMock).toHaveBeenCalledWith("next-token", 2, { limit: 50 }));
    expect(screen.queryByText("stale-log")).not.toBeInTheDocument();
    expect(screen.queryByText("fresh-log")).not.toBeInTheDocument();

    await act(async () => {
      nextLogs.resolve([
        { id: "2", level: "info", message: "fresh-log", timestamp: "" },
      ]);
    });
    expect(await screen.findByText("fresh-log")).toBeInTheDocument();
    expect(screen.queryByText("stale-log")).not.toBeInTheDocument();
    expect(screen.queryByText("late-stale-log")).not.toBeInTheDocument();

  });

  it("copies every loaded result with its real status and only logs that were already loaded", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    const mixed = batchStatus([
      taskOf({ id: 11, nodeId: 101, nodeName: "node-ok", status: "success", name: "secret-command" }),
      taskOf({ id: 12, nodeId: 102, nodeName: "node-failed", status: "failed", lastError: "exit 7" }),
      taskOf({
        id: 13,
        nodeId: 103,
        nodeName: "node-dispatch",
        status: "pending",
        dispatchStatus: "failed",
        lastError: "node unreachable",
      }),
      taskOf({ id: 14, nodeId: 104, nodeName: "node-dispatching", status: "pending", dispatchStatus: "dispatching" }),
      taskOf({ id: 15, nodeId: 105, nodeName: "node-running", status: "running" }),
    ], { success: 1, failed: 1, pending: 3 });
    getBatchStatusMock.mockResolvedValue(mixed);
    getTaskLogsMock.mockImplementation((_token: string, taskId: number) => {
      if (taskId === 11) return Promise.resolve([logEvent("1", "R13-copy-proof")]);
      return Promise.reject(new Error(`unexpected logs for ${taskId}`));
    });
    renderDialog();

    const successRow = await screen.findByRole("button", { name: /node-ok/ });
    expect(within(successRow).getByText("成功")).toBeInTheDocument();
    expect(within(screen.getByRole("button", { name: /node-failed/ })).getByText("失败")).toBeInTheDocument();
    expect(within(screen.getByRole("button", { name: /node-dispatch\b/ })).getByText("派发失败")).toBeInTheDocument();
    expect(within(screen.getByRole("button", { name: /node-dispatch\b/ })).queryByText("等待中")).not.toBeInTheDocument();
    const dispatchingRow = screen.getByRole("button", { name: /node-dispatching/ });
    const runningRow = screen.getByRole("button", { name: /node-running/ });
    expect(within(dispatchingRow).getByText("派发尚未确认")).toBeInTheDocument();
    expect(within(runningRow).getByText("执行中")).toBeInTheDocument();
    expect(dispatchingRow).toBeDisabled();
    expect(runningRow).toBeDisabled();
    expect(screen.getByRole("button", { name: COPY_ZH })).toHaveAccessibleDescription(ZH_HINT);

    await user.click(successRow);
    expect(await screen.findByText("R13-copy-proof")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /node-ok/ }));
    expect(screen.queryByText("R13-copy-proof")).not.toBeInTheDocument();
    expect(getTaskLogsMock).toHaveBeenCalledTimes(1);
    expect(getTaskLogsMock).toHaveBeenCalledWith("auth-marker", 11, { limit: 50 });

    const statusCalls = getBatchStatusMock.mock.calls.length;
    const text = await readCopy(user, writeText);
    expect(text).toBe(buildCopyText("zh", "batch-1", 5, 5, [
      { id: 11, nodeId: 101, nodeName: "node-ok", statusLabel: "成功", logs: { kind: "loaded", messages: ["R13-copy-proof"] } },
      { id: 12, nodeId: 102, nodeName: "node-failed", statusLabel: "失败", lastError: "exit 7", logs: { kind: "unloaded" } },
      {
        id: 13,
        nodeId: 103,
        nodeName: "node-dispatch",
        statusLabel: "派发失败",
        lastError: "node unreachable",
        logs: { kind: "unloaded" },
      },
      { id: 14, nodeId: 104, nodeName: "node-dispatching", statusLabel: "派发尚未确认", logs: { kind: "unloaded" } },
      { id: 15, nodeId: 105, nodeName: "node-running", statusLabel: "执行中", logs: { kind: "unloaded" } },
    ]));
    expect(text).not.toContain("secret-command");
    expect(text).not.toContain("auth-marker");
    expect(text).not.toContain("已完成");
    expect(text).not.toContain("状态：等待中");
    expect(text).not.toMatch(/batch\.[A-Za-z]/);
    expect(getTaskLogsMock).toHaveBeenCalledTimes(1);
    expect(getBatchStatusMock).toHaveBeenCalledTimes(statusCalls);
    expect(deleteBatchMock).not.toHaveBeenCalled();
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED));
    expect(toastError).not.toHaveBeenCalled();
  });

  it("distinguishes empty, capped, unloaded, loading, and failed logs in Chinese and English", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    const slow = createDeferred<LogEvent[]>();
    const fifty = Array.from({ length: 50 }, (_, index) => `line-${String(index + 1).padStart(2, "0")}`);
    getBatchStatusMock.mockResolvedValue(batchStatus([
      taskOf({ id: 1, nodeId: 11, nodeName: "node-empty", status: "success" }),
      taskOf({ id: 2, nodeId: 12, nodeName: "node-fifty", status: "success" }),
      taskOf({ id: 3, nodeId: 13, nodeName: "node-idle", status: "success" }),
      taskOf({ id: 4, nodeId: 14, nodeName: "node-slow", status: "success" }),
      taskOf({ id: 5, nodeId: 15, nodeName: "node-broken", status: "failed" }),
    ], { success: 4, failed: 1 }));
    getTaskLogsMock.mockImplementation((_token: string, taskId: number) => {
      if (taskId === 1) return Promise.resolve([]);
      if (taskId === 2) return Promise.resolve(fifty.map((message, index) => logEvent(String(index + 1), message)));
      if (taskId === 4) return slow.promise;
      if (taskId === 5) return Promise.reject(new Error("log boom"));
      return Promise.reject(new Error(`unexpected logs for ${taskId}`));
    });
    renderDialog();

    await user.click(await screen.findByRole("button", { name: /node-empty/ }));
    expect(await screen.findByText("暂无日志输出")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /node-fifty/ }));
    await waitFor(() => {
      const lines = document.querySelector("pre")?.textContent?.split("\n") ?? [];
      expect(lines).toHaveLength(50);
      expect(lines[0]).toBe("line-01");
      expect(lines[49]).toBe("line-50");
    });
    await user.click(screen.getByRole("button", { name: /node-broken/ }));
    expect(await screen.findByText("日志加载失败")).toBeInTheDocument();
    expect(screen.queryByText("log boom")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /node-slow/ }));
    expect(await screen.findByText("加载日志中...")).toBeInTheDocument();
    expect(getTaskLogsMock).toHaveBeenCalledTimes(4);
    expect(getTaskLogsMock).not.toHaveBeenCalledWith("auth-marker", 3, { limit: 50 });

    const loaded = { kind: "loaded" as const, messages: fifty };
    const sections = [
      { id: 1, nodeId: 11, nodeName: "node-empty", statusLabel: "", logs: { kind: "loaded" as const, messages: [] } },
      { id: 2, nodeId: 12, nodeName: "node-fifty", statusLabel: "", logs: loaded },
      { id: 3, nodeId: 13, nodeName: "node-idle", statusLabel: "", logs: { kind: "unloaded" as const } },
      { id: 4, nodeId: 14, nodeName: "node-slow", statusLabel: "", logs: { kind: "loading" as const } },
      { id: 5, nodeId: 15, nodeName: "node-broken", statusLabel: "", logs: { kind: "error" as const } },
    ];
    const chinese = await readCopy(user, writeText);
    expect(chinese).toBe(buildCopyText("zh", "batch-1", 5, 5, sections.map((section, index) => ({
      ...section,
      statusLabel: index === 4 ? "失败" : "成功",
    }))));
    expect(chinese).toContain("已加载日志：0 条（每任务最多 50 条，非完整日志）");
    expect(chinese).toContain("已加载日志：50 条（每任务最多 50 条，非完整日志）");
    expect(chinese).toContain("日志尚未加载");
    expect(chinese).toContain("加载日志中...");
    expect(chinese).toContain("日志加载失败");
    expect(chinese).not.toContain("暂无日志输出");
    expect(chinese).not.toContain("log boom");
    expect(chinese).not.toMatch(/batch\.[A-Za-z]/);
    expect(getTaskLogsMock).toHaveBeenCalledTimes(4);
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED));

    await setLanguage("en");
    await enabledCopyButton(COPY_EN);
    const english = await readCopy(user, writeText, COPY_EN);
    expect(english).toBe(buildCopyText("en", "batch-1", 5, 5, sections.map((section, index) => ({
      ...section,
      statusLabel: index === 4 ? "Failed" : "Success",
    }))));
    expect(english).toContain("Loaded log entries: 0 (up to 50 per task; not complete logs)");
    expect(english).toContain("Loaded log entries: 50 (up to 50 per task; not complete logs)");
    expect(english).toContain("Logs not loaded");
    expect(english).toContain("Loading logs...");
    expect(english).toContain("Failed to load logs");
    expect(english).not.toContain("log boom");
    expect(english).not.toMatch(/batch\.[A-Za-z]/);
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(EN_COPIED));
    expect(getTaskLogsMock).toHaveBeenCalledTimes(4);

    await act(async () => { slow.resolve([]); });
  });

  it("keeps the other task loading when an earlier log request resolves", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    const first = createDeferred<LogEvent[]>();
    const second = createDeferred<LogEvent[]>();
    getBatchStatusMock.mockResolvedValue(batchStatus([
      taskOf({ id: 1, nodeId: 11, nodeName: "node-a", status: "success" }),
      taskOf({ id: 2, nodeId: 22, nodeName: "node-b", status: "success" }),
    ], { success: 2 }));
    getTaskLogsMock.mockImplementation((_token: string, taskId: number) => {
      if (taskId === 1) return first.promise;
      if (taskId === 2) return second.promise;
      return Promise.reject(new Error(`unexpected logs for ${taskId}`));
    });
    renderDialog();

    await user.click(await screen.findByRole("button", { name: /node-a/ }));
    expect(await screen.findByText("加载日志中...")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /node-b/ }));
    expect(screen.getByText("加载日志中...")).toBeInTheDocument();
    expect(getTaskLogsMock).toHaveBeenCalledTimes(2);

    await act(async () => { first.resolve([logEvent("1", "alpha-log")]); });
    expect(screen.getByText("加载日志中...")).toBeInTheDocument();
    expect(screen.queryByText("alpha-log")).not.toBeInTheDocument();
    const whileLoading = await readCopy(user, writeText);
    expect(whileLoading).toBe(buildCopyText("zh", "batch-1", 2, 2, [
      { id: 1, nodeId: 11, nodeName: "node-a", statusLabel: "成功", logs: { kind: "loaded", messages: ["alpha-log"] } },
      { id: 2, nodeId: 22, nodeName: "node-b", statusLabel: "成功", logs: { kind: "loading" } },
    ]));
    expect(getTaskLogsMock).toHaveBeenCalledTimes(2);

    await act(async () => { second.resolve([logEvent("2", "beta-log")]); });
    expect(await screen.findByText("beta-log")).toBeInTheDocument();
    expect(screen.queryByText("加载日志中...")).not.toBeInTheDocument();
    expect(screen.queryByText("alpha-log")).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled());
    const afterBoth = await readCopy(user, writeText);
    expect(afterBoth).toBe(buildCopyText("zh", "batch-1", 2, 2, [
      { id: 1, nodeId: 11, nodeName: "node-a", statusLabel: "成功", logs: { kind: "loaded", messages: ["alpha-log"] } },
      { id: 2, nodeId: 22, nodeName: "node-b", statusLabel: "成功", logs: { kind: "loaded", messages: ["beta-log"] } },
    ]));
    expect(getTaskLogsMock).toHaveBeenCalledTimes(2);
    expect(deleteBatchMock).not.toHaveBeenCalled();
  });

  it("reports clipboard success only after writeText resolves", async () => {
    const user = userEvent.setup();
    getBatchStatusMock.mockResolvedValue(oneNode("node-a"));
    renderDialog();
    const clipboard = await clickDeferredCopy(user);
    expect(clipboard.writes[0]?.text).toContain("已加载结果：1 / 1");
    expect(clipboard.writes[0]?.text).toContain("日志尚未加载");
    expectNoCopyToast();
    await settleWrite(clipboard.writes[0], "resolve");
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledTimes(1));
    expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED);
    expect(toastError).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();
  });

  it("reports clipboard rejection and can copy again", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn<(text: string) => Promise<void>>()
      .mockRejectedValueOnce(new Error("denied"))
      .mockResolvedValueOnce(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    getBatchStatusMock.mockResolvedValue(oneNode("node-a"));
    renderDialog();
    await user.click(await enabledCopyButton());
    await waitFor(() => expect(toastError).toHaveBeenCalledWith(ZH_COPY_FAILED));
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: COPY_ZH }));
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED));
    expect(writeText).toHaveBeenCalledTimes(2);
    expect(toastError).toHaveBeenCalledTimes(1);
  });

  it("reports a missing clipboard API and can copy again", async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
    getBatchStatusMock.mockResolvedValue(oneNode("node-a"));
    renderDialog();
    await user.click(await enabledCopyButton());
    await waitFor(() => expect(toastError).toHaveBeenCalledWith(ZH_COPY_FAILED));
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();

    const writeText = installResolvedClipboard();
    await user.click(screen.getByRole("button", { name: COPY_ZH }));
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED));
    expect(writeText).toHaveBeenCalledTimes(1);
  });

  it("writes once when a second click re-enters before the clipboard settles", async () => {
    const user = userEvent.setup();
    const writes: PendingWrite[] = [];
    const writeText = vi.fn((text: string) => {
      const deferred = createDeferred<void>();
      writes.push({ text, resolve: deferred.resolve, reject: deferred.reject });
      if (writes.length === 1) fireEvent.click(screen.getByRole("button", { name: COPY_ZH }));
      return deferred.promise;
    });
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    getBatchStatusMock.mockResolvedValue(oneNode("node-a"));
    renderDialog();
    await user.click(await enabledCopyButton());
    expect(writeText).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
    await act(async () => { writes[0]?.resolve(); });
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledTimes(1));
    expect(toastError).not.toHaveBeenCalled();
  });

  it.each(["resolve", "reject"] as const)(
    "closes a settled batch while copy is pending and ignores a late clipboard %s",
    async (outcome) => {
      const user = userEvent.setup();
      getBatchStatusMock.mockResolvedValue(oneNode("node-a"));
      renderDialog();
      const clipboard = await clickDeferredCopy(user);
      await user.click(screen.getByRole("button", { name: "Close" }));
      expect(onOpenChangeMock).toHaveBeenCalledWith(false);
      expect(deleteBatchMock).toHaveBeenCalledTimes(1);
      expect(deleteBatchMock).toHaveBeenCalledWith("auth-marker", "batch-1");
      await settleWrite(clipboard.writes[0], outcome);
      expectNoCopyToast();
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
      expect(deleteBatchMock).toHaveBeenCalledTimes(1);
    },
  );

  it.each(["resolve", "reject"] as const)("ignores a clipboard %s after unmount", async (outcome) => {
    const user = userEvent.setup();
    getBatchStatusMock.mockResolvedValue(oneNode("node-a"));
    const view = renderDialog();
    const clipboard = await clickDeferredCopy(user);
    view.unmount();
    await settleWrite(clipboard.writes[0], outcome);
    expectNoCopyToast();
    expect(deleteBatchMock).not.toHaveBeenCalled();
    expect(clipboard.writeText).toHaveBeenCalledTimes(1);
  });

  it.each(["resolve", "reject"] as const)(
    "ignores a clipboard %s after the batch changes and keeps the new copy pending",
    async (outcome) => {
      const user = userEvent.setup();
      const nextStatus = createDeferred<BatchStatus>();
      getBatchStatusMock.mockImplementation((_token: string, batchId: string) => (
        batchId === "batch-2" ? nextStatus.promise : Promise.resolve(oneNode("node-a"))
      ));
      const view = renderDialog();
      const clipboard = await clickDeferredCopy(user);
      expect(clipboard.writes[0]?.text).toContain("批次 ID：batch-1");
      view.rerender(resultElement(false, "auth-marker", "batch-2"));
      expect(screen.queryByRole("button", { name: /node-a/ })).not.toBeInTheDocument();
      expect(screen.getByText("加载中...")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
      await act(async () => { nextStatus.resolve(oneNode("node-b")); });
      const second = await clickAnotherCopy(user, clipboard);
      expect(second.text).toContain("批次 ID：batch-2");
      expect(second.text).toContain(JSON.stringify("node-b"));
      expect(second.text).not.toContain("auth-marker");
      await settleWrite(clipboard.writes[0], outcome);
      expectNoCopyToast();
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
      await settleWrite(second, "resolve");
      await waitFor(() => expect(toastSuccess).toHaveBeenCalledTimes(1));
      expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED);
      expect(toastError).not.toHaveBeenCalled();
      expect(deleteBatchMock).not.toHaveBeenCalled();
    },
  );

  it.each(["resolve", "reject"] as const)(
    "ignores a clipboard %s after the token changes and keeps the new copy pending",
    async (outcome) => {
      const user = userEvent.setup();
      const nextStatus = createDeferred<BatchStatus>();
      getBatchStatusMock.mockImplementation((token: string) => (
        token === "next-token" ? nextStatus.promise : Promise.resolve(oneNode("node-a"))
      ));
      const view = renderDialog();
      const clipboard = await clickDeferredCopy(user);
      expect(clipboard.writes[0]?.text).not.toContain("auth-marker");
      publishAuth("admin", "next-token");
      view.rerender(resultElement(false, "next-token"));
      expect(screen.queryByText("node-a")).not.toBeInTheDocument();
      expect(screen.getByText("加载中...")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
      await act(async () => { nextStatus.resolve(oneNode("node-next")); });
      const second = await clickAnotherCopy(user, clipboard);
      expect(second.text).toContain(JSON.stringify("node-next"));
      expect(second.text).not.toContain("next-token");
      await settleWrite(clipboard.writes[0], outcome);
      expectNoCopyToast();
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
      await settleWrite(second, "resolve");
      await waitFor(() => expect(toastSuccess).toHaveBeenCalledTimes(1));
      expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED);
      expect(toastError).not.toHaveBeenCalled();
      expect(deleteBatchMock).not.toHaveBeenCalled();
    },
  );

  it.each(["resolve", "reject"] as const)(
    "ignores a clipboard %s after a role change and keeps the new copy pending",
    async (outcome) => {
      const user = userEvent.setup();
      getBatchStatusMock.mockResolvedValue(oneNode("node-a"));
      const view = renderDialog();
      const clipboard = await clickDeferredCopy(user);
      publishAuth("operator");
      view.rerender(resultElement());
      expect(screen.getByRole("button", { name: /node-a/ })).toBeInTheDocument();
      const second = await clickAnotherCopy(user, clipboard);
      await settleWrite(clipboard.writes[0], outcome);
      expectNoCopyToast();
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
      await settleWrite(second, "resolve");
      await waitFor(() => expect(toastSuccess).toHaveBeenCalledTimes(1));
      expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED);
      expect(toastError).not.toHaveBeenCalled();
    },
  );

  it.each(["resolve", "reject"] as const)(
    "ignores a clipboard %s when the session generation changes before rerender",
    async (outcome) => {
      const user = userEvent.setup();
      getBatchStatusMock.mockResolvedValue(oneNode("node-a"));
      const view = renderDialog();
      const clipboard = await clickDeferredCopy(user);
      const generation = getAuthSessionGeneration();
      bumpAuthSessionGeneration();
      expect(getAuthSessionGeneration()).toBe(generation + 1);
      await settleWrite(clipboard.writes[0], outcome);
      expectNoCopyToast();
      expect(clipboard.writeText).toHaveBeenCalledTimes(1);
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();

      view.rerender(resultElement());
      const next = installDeferredClipboard();
      await user.click(await enabledCopyButton());
      expect(next.writeText).toHaveBeenCalledTimes(1);
      await act(async () => { next.writes[0]?.resolve(); });
      await waitFor(() => expect(toastSuccess).toHaveBeenCalledTimes(1));
      expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED);
      expect(toastError).not.toHaveBeenCalled();
    },
  );

  it.each(["resolve", "reject"] as const)(
    "keeps a new copy pending when a clipboard %s arrives after the same token gets a new session",
    async (outcome) => {
      const user = userEvent.setup();
      const nextStatus = createDeferred<BatchStatus>();
      let sawBump = false;
      getBatchStatusMock.mockImplementation(() => (
        sawBump ? nextStatus.promise : Promise.resolve(oneNode("node-a"))
      ));
      const view = renderDialog();
      const clipboard = await clickDeferredCopy(user);
      sawBump = true;
      bumpAuthSessionGeneration();
      view.rerender(resultElement());
      expect(screen.queryByText("node-a")).not.toBeInTheDocument();
      expect(screen.getByText("加载中...")).toBeInTheDocument();
      await act(async () => { nextStatus.resolve(oneNode("node-a")); });
      const second = await clickAnotherCopy(user, clipboard);
      await settleWrite(clipboard.writes[0], outcome);
      expectNoCopyToast();
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
      await settleWrite(second, "resolve");
      await waitFor(() => expect(toastSuccess).toHaveBeenCalledTimes(1));
      expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED);
      expect(toastError).not.toHaveBeenCalled();
    },
  );

  it("refuses a new write when identity, session generation, or an auth transition changes before rerender", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    getBatchStatusMock.mockResolvedValue(oneNode("node-a"));

    const snapshotView = renderDialog();
    await enabledCopyButton();
    rememberAuthIdentity("other-token", "admin");
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: COPY_ZH }));
    expect(writeText).not.toHaveBeenCalled();
    expectNoCopyToast();
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();
    snapshotView.unmount();

    publishAuth("admin");
    const roleView = renderDialog();
    await enabledCopyButton();
    rememberAuthIdentity("auth-marker", "viewer");
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: COPY_ZH }));
    expect(writeText).not.toHaveBeenCalled();
    expectNoCopyToast();
    roleView.unmount();

    publishAuth("admin");
    const generationView = renderDialog();
    await enabledCopyButton();
    bumpAuthSessionGeneration();
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: COPY_ZH }));
    expect(writeText).not.toHaveBeenCalled();
    expectNoCopyToast();
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();
    generationView.unmount();

    const transitionView = renderDialog();
    await enabledCopyButton();
    const barrier = beginAuthTransitionBarrier();
    try {
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();
      await user.click(screen.getByRole("button", { name: COPY_ZH }));
      expect(writeText).not.toHaveBeenCalled();
      expectNoCopyToast();
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();
    } finally {
      releaseAuthTransitionBarrier(barrier);
    }
    transitionView.unmount();
  });

  it("keeps result rows on role change, blocks copy until the new status arrives, and copies only reloaded logs", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    const refreshed = createDeferred<BatchStatus>();
    let statusCalls = 0;
    let logCalls = 0;
    const status = oneNode("node-a");
    getBatchStatusMock.mockImplementation(() => {
      statusCalls += 1;
      return statusCalls === 1 ? Promise.resolve(status) : refreshed.promise;
    });
    getTaskLogsMock.mockImplementation(() => {
      logCalls += 1;
      return logCalls === 1
        ? Promise.resolve([logEvent("1", "admin-secret-log")])
        : Promise.resolve([logEvent("2", "viewer-log")]);
    });
    const view = renderDialog();
    await user.click(await screen.findByRole("button", { name: /node-a/ }));
    expect(await screen.findByText("admin-secret-log")).toBeInTheDocument();
    expect(await enabledCopyButton()).toBeEnabled();

    publishAuth("viewer");
    view.rerender(resultElement());
    expect(screen.getByRole("button", { name: /node-a/ })).toBeInTheDocument();
    expect(screen.queryByText("加载中...")).not.toBeInTheDocument();
    expect(screen.queryByText("admin-secret-log")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
    expect(logCalls).toBe(1);
    expect(writeText).not.toHaveBeenCalled();

    await act(async () => { refreshed.resolve(status); });
    await enabledCopyButton();
    const cleared = await readCopy(user, writeText);
    expect(cleared).toBe(buildCopyText("zh", "batch-1", 1, 1, [
      { id: 1, nodeId: 1, nodeName: "node-a", statusLabel: "成功", logs: { kind: "unloaded" } },
    ]));
    expect(cleared).not.toContain("admin-secret-log");
    expect(logCalls).toBe(1);

    await user.click(screen.getByRole("button", { name: /node-a/ }));
    expect(await screen.findByText("viewer-log")).toBeInTheDocument();
    expect(screen.queryByText("admin-secret-log")).not.toBeInTheDocument();
    expect(logCalls).toBe(2);
    await waitFor(() => expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled());
    const reloaded = await readCopy(user, writeText);
    expect(reloaded).toBe(buildCopyText("zh", "batch-1", 1, 1, [
      { id: 1, nodeId: 1, nodeName: "node-a", statusLabel: "成功", logs: { kind: "loaded", messages: ["viewer-log"] } },
    ]));
    expect(reloaded).not.toContain("admin-secret-log");
    expect(logCalls).toBe(2);

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);
  });

  it("lets an operator copy loaded results and still delete a settled unretained batch", async () => {
    const user = userEvent.setup();
    publishAuth("operator");
    const writeText = installResolvedClipboard();
    getBatchStatusMock.mockResolvedValue(oneNode("node-a"));
    renderDialog();
    const text = await readCopy(user, writeText);
    expect(text).toContain("已加载结果：1 / 1");
    expect(text).not.toContain("auth-marker");
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED));
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).toHaveBeenCalledWith("auth-marker", "batch-1");
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);
  });

  it("disables copy for a null role and an unknown role", async () => {
    getBatchStatusMock.mockResolvedValue(oneNode("node-a"));
    publishAuth(null);
    const nullView = renderDialog();
    expect(await screen.findByText("node-a")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
    nullView.unmount();

    authRef.current = { role: "auditor" as "admin", token: "auth-marker", totpEnabled: true };
    rememberAuthIdentity("auth-marker", "auditor" as never);
    const unknownView = renderDialog();
    expect(await screen.findByText("node-a")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
    unknownView.unmount();
  });

  it("disables copy when a later status refresh fails and keeps the loaded rows", async () => {
    const polls = installBatchPollSpy();
    let calls = 0;
    getBatchStatusMock.mockImplementation(() => {
      calls += 1;
      if (calls === 1) {
        return Promise.resolve(batchStatus([
          taskOf({ id: 1, nodeId: 1, nodeName: "node-run", status: "running" }),
        ], { running: 1 }));
      }
      return Promise.reject(new Error("status refresh failed"));
    });
    const view = renderDialog();
    try {
      expect(await screen.findByText("执行中")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();
      expect(polls.polls.size).toBe(1);

      await act(async () => {
        for (const poll of polls.polls.values()) poll();
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(await screen.findByText("status refresh failed")).toBeInTheDocument();
      expect(screen.getByText("node-run")).toBeInTheDocument();
      expect(screen.getByText("执行中")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();
      expect(deleteBatchMock).not.toHaveBeenCalled();
    } finally {
      view.unmount();
      polls.restore();
    }
  });

  it("copies an empty batch as zero of zero without claiming the run is complete", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    getBatchStatusMock.mockResolvedValue(batchStatus([], {}, 0));
    renderDialog();
    await enabledCopyButton();
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeEnabled();
    expect(screen.getByRole("button", { name: COPY_ZH })).toHaveAccessibleDescription(ZH_HINT);
    const chinese = await readCopy(user, writeText);
    expect(chinese).toBe(buildCopyText("zh", "batch-1", 0, 0, []));
    expect(chinese).toContain("已加载结果：0 / 0");
    expect(chinese).not.toContain("已完成");
    expect(chinese).not.toContain("全部");
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED));

    await setLanguage("en");
    await enabledCopyButton(COPY_EN);
    expect(screen.getByRole("button", { name: COPY_EN })).toHaveAccessibleDescription(EN_HINT);
    const english = await readCopy(user, writeText, COPY_EN);
    expect(english).toBe(buildCopyText("en", "batch-1", 0, 0, []));
    expect(english).toContain("Loaded results: 0 / 0");
    expect(english).not.toContain("Done");
    expect(english).not.toMatch(/batch\.[A-Za-z]/);
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(EN_COPIED));
  });

  it("preserves long node names and multiline logs in Chinese and English", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    const nodeName = "机房 A\n\"edge\" node";
    const messages = ["alpha\nbeta", "", "gamma"];
    const lastError = "boom line\nsecond";
    getBatchStatusMock.mockResolvedValue(batchStatus([
      taskOf({ id: 4, nodeId: 9, nodeName, status: "success", lastError }),
    ], { success: 1 }));
    getTaskLogsMock.mockResolvedValue(messages.map((message, index) => logEvent(String(index + 1), message)));
    renderDialog();

    await user.click(await screen.findByRole("button", { name: /机房 A/ }));
    await waitFor(() => {
      expect(document.querySelector("pre")?.textContent).toBe("alpha\nbeta\n\ngamma");
    });
    const task = {
      id: 4,
      nodeId: 9,
      nodeName,
      lastError,
      logs: { kind: "loaded" as const, messages },
    };
    const chinese = await readCopy(user, writeText);
    expect(chinese).toBe(buildCopyText("zh", "batch-1", 1, 1, [{ ...task, statusLabel: "成功" }]));
    expect(chinese).toContain(`节点：${JSON.stringify(nodeName)}（ID：9）`);
    expect(chinese).not.toMatch(/batch\.[A-Za-z]/);
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(ZH_COPIED));

    await setLanguage("en");
    await enabledCopyButton(COPY_EN);
    const english = await readCopy(user, writeText, COPY_EN);
    expect(english).toBe(buildCopyText("en", "batch-1", 1, 1, [{ ...task, statusLabel: "Success" }]));
    expect(english).toContain(`Node: ${JSON.stringify(nodeName)} (ID: 9)`);
    expect(english).toContain("Error: boom line\nsecond");
    expect(english).toContain("alpha\nbeta");
    expect(english).not.toMatch(/batch\.[A-Za-z]/);
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(EN_COPIED));
    expect(getTaskLogsMock).toHaveBeenCalledTimes(1);
    expect(deleteBatchMock).not.toHaveBeenCalled();
  });

  it("keeps the retry checkbox outside the row button and allows one source per node", async () => {
    const user = userEvent.setup();
    publishAuth("operator");
    getBatchStatusMock.mockResolvedValue(retryBatch({
      tasks: [
        taskOf({ id: 7, nodeId: 3, nodeName: "node-a", status: "failed", lastError: "exit 7" }),
        taskOf({ id: 8, nodeId: 3, nodeName: "node-b", status: "failed", lastError: "exit 7" }),
      ],
      sources: [retrySource(), retrySource({ taskId: 8, runId: 11 })],
    }));
    renderSource();

    const checkbox = await screen.findByRole("checkbox", { name: /node-a/ });
    const other = screen.getByRole("checkbox", { name: /node-b/ });
    const rowButton = screen.getByRole("button", { name: /node-a/ });
    expect(rowButton.contains(checkbox)).toBe(false);
    expect(within(rowButton).getByText("失败")).toBeInTheDocument();
    expect(screen.getAllByText("执行失败")).toHaveLength(2);
    await user.click(checkbox);
    expect(checkbox).toBeChecked();
    expect(other).toBeDisabled();
    expect(screen.getByText("同一节点只能选择一个失败来源")).toBeInTheDocument();
    await user.click(other);
    expect(other).not.toBeChecked();
    expect(screen.getByRole("button", { name: "重试选中的失败（1）" })).toBeEnabled();
  });

  it("stops the whole retry when the fresh source review changes", async () => {
    const user = userEvent.setup();
    const tasks = [
      taskOf({ id: 7, nodeId: 3, nodeName: "node-a", status: "failed", lastError: "exit 7" }),
      taskOf({ id: 8, nodeId: 4, nodeName: "node-b", status: "failed", lastError: "exit 8" }),
    ];
    const sources = [retrySource(), retrySource({ taskId: 8, nodeId: 4, runId: 11 })];
    getBatchStatusMock.mockResolvedValue(retryBatch({ tasks, sources }));
    renderSource();
    await chooseRetry(user, /node-a/);
    await chooseRetry(user, /node-b/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（2）" }));
    getBatchStatusMock.mockResolvedValue(retryBatch({
      tasks,
      sources: [sources[0], retrySource({ taskId: 8, nodeId: 4, runId: 11, eligible: false, reason: "source_changed" })],
    }));
    await user.type(screen.getByRole("textbox"), "2");
    fireEvent.submit(screen.getByRole("button", { name: "确认重试" }).closest("form") as HTMLFormElement);

    expect(await screen.findByText("来源资格已变化，本次没有提交。请重新选择并再次确认。")).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: /node-a/ })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: /node-b/ })).toBeChecked();
    expect(screen.getByText("命令来源已变化")).toBeInTheDocument();
    expect(retryGrantMock).not.toHaveBeenCalled();
    expect(retryBatchCommandMock).not.toHaveBeenCalled();
  });

  it("submits one retry when confirmation is activated twice", async () => {
    const user = userEvent.setup();
    const review = createDeferred<BatchStatus>();
    let reads = 0;
    getBatchStatusMock.mockImplementation((_token: string, id: string) => {
      if (id === CHILD_BATCH_ID) return Promise.resolve(childBatch());
      reads += 1;
      return reads === 1 ? Promise.resolve(retryBatch()) : review.promise;
    });
    retryGrantMock.mockImplementation((_token: string, _input: unknown, proof?: string) => (
      proof ? Promise.resolve([]) : Promise.reject(stepUpRequiredError())
    ));
    retryBatchCommandMock.mockResolvedValue(retryResult());
    renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    const form = screen.getByRole("button", { name: "确认重试" }).closest("form");
    if (!form) throw new Error("missing confirm form");
    fireEvent.submit(form);
    fireEvent.submit(form);
    expect(reads).toBe(2);

    await act(async () => { review.resolve(retryBatch()); });
    await waitFor(() => expect(retryBatchCommandMock).toHaveBeenCalledTimes(1));
    expect(retryGrantMock).toHaveBeenCalledTimes(2);
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(
      STEP_UP_ACTIONS.batchCommandRetry,
      { persist: false, reuseCached: false },
    );
    expect(retryBatchCommandMock).toHaveBeenCalledWith(
      "auth-marker",
      SOURCE_BATCH_ID,
      [{ taskId: 7, runId: 9 }],
      "proof-retry",
      expect.any(String),
    );
    expect(await screen.findByRole("button", { name: /child-node/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /node-a/, hidden: true })).toBeInTheDocument();
  });

  it("blocks the result and confirmation dialogs from closing while a retry is submitting", async () => {
    const user = userEvent.setup();
    const post = createDeferred<BatchRetryResult>();
    getBatchStatusMock.mockImplementation((_token: string, id: string) => (
      Promise.resolve(id === CHILD_BATCH_ID ? childBatch() : retryBatch())
    ));
    retryBatchCommandMock.mockReturnValue(post.promise);
    renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    fireEvent.submit(screen.getByRole("button", { name: "确认重试" }).closest("form") as HTMLFormElement);
    await waitFor(() => expect(retryBatchCommandMock).toHaveBeenCalledTimes(1));

    expect(screen.getAllByText("正在提交重试，请保持窗口打开。关闭不会取消已经发出的请求。").length).toBeGreaterThan(0);
    const closeButtons = screen.getAllByRole("button", { name: "Close", hidden: true });
    expect(closeButtons.length).toBeGreaterThan(1);
    for (const button of closeButtons) fireEvent.click(button);
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(onOpenChangeMock).not.toHaveBeenCalled();
    expect(deleteBatchMock).not.toHaveBeenCalled();

    await act(async () => { post.resolve(retryResult(true)); });
    expect(await screen.findByRole("button", { name: /child-node/ })).toBeInTheDocument();
  });

  it("does not replay an unknown retry and continues that submission with the same key", async () => {
    const user = userEvent.setup();
    let posts = 0;
    getBatchStatusMock.mockImplementation((_token: string, id: string) => (
      Promise.resolve(id === CHILD_BATCH_ID ? childBatch() : retryBatch())
    ));
    retryBatchCommandMock.mockImplementation(() => {
      posts += 1;
      if (posts === 1) return Promise.reject(new TypeError("network down"));
      return Promise.resolve(retryResult());
    });
    renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    await user.click(screen.getByRole("button", { name: "确认重试" }));

    expect(await screen.findByRole("button", { name: "继续本次提交" })).toBeEnabled();
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(1);
    expect(screen.getByText("重试结果尚未确认。请读取原批次状态核对，不要认为命令没有执行。")).toBeInTheDocument();
    expect(screen.getByText("原批次没有这些来源的重试记录。只有明确继续才会使用同一次提交。")).toBeInTheDocument();
    const key = retryBatchCommandMock.mock.calls[0]?.[4];
    await user.click(screen.getByRole("button", { name: "继续本次提交" }));
    expect(await screen.findByRole("button", { name: /child-node/ })).toBeInTheDocument();
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(2);
    expect(retryBatchCommandMock.mock.calls[1]?.[4]).toBe(key);
    expect(retryGrantMock).toHaveBeenCalledTimes(2);
    expect(deleteBatchMock).not.toHaveBeenCalled();
  });

  it("stays unknown when retry links are missing and does not offer continuation", async () => {
    const user = userEvent.setup();
    let posted = false;
    getBatchStatusMock.mockImplementation(() => {
      const status = retryBatch();
      if (!posted) return Promise.resolve(status);
      const { retryLinks: _ignored, ...withoutLinks } = status;
      return Promise.resolve(withoutLinks);
    });
    retryBatchCommandMock.mockImplementation(() => {
      posted = true;
      return Promise.reject(new BatchRetryContractError());
    });
    renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    await user.click(screen.getByRole("button", { name: "确认重试" }));

    expect(await screen.findByText("原批次没有可用的重试记录。结果仍未确认，不能继续提交。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "继续本次提交" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /child-node/ })).not.toBeInTheDocument();
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);
  });

  it("shows a created retry link without submitting it again", async () => {
    const user = userEvent.setup();
    let posted = false;
    getBatchStatusMock.mockImplementation((_token: string, id: string) => {
      if (id === CHILD_BATCH_ID) return Promise.resolve(childBatch());
      if (!posted) return Promise.resolve(retryBatch());
      return Promise.resolve(retryBatch({
        links: [{ taskId: 7, runId: 9, batchId: CHILD_BATCH_ID, deleted: false }],
      }));
    });
    retryBatchCommandMock.mockImplementation(() => {
      posted = true;
      return Promise.reject(new TypeError("network down"));
    });
    renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    await user.click(screen.getByRole("button", { name: "确认重试" }));

    await user.click(await screen.findByRole("button", { name: "查看重试批次" }));
    expect(await screen.findByRole("button", { name: /child-node/ })).toBeInTheDocument();
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: "继续本次提交" })).not.toBeInTheDocument();
  });

  it("does not submit a deleted retry link again", async () => {
    const user = userEvent.setup();
    let posted = false;
    getBatchStatusMock.mockImplementation(() => {
      if (!posted) return Promise.resolve(retryBatch());
      return Promise.resolve(retryBatch({
        links: [{ taskId: 7, runId: 9, batchId: CHILD_BATCH_ID, deleted: true }],
      }));
    });
    retryBatchCommandMock.mockImplementation(() => {
      posted = true;
      return Promise.reject(new TypeError("network down"));
    });
    renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    await user.click(screen.getByRole("button", { name: "确认重试" }));

    expect(await screen.findByText("重试批次已创建后清理，不能再次提交。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "继续本次提交" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "查看重试批次" })).not.toBeInTheDocument();
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(1);
  });

  it("does not submit a retry after the auth generation changes during the grant", async () => {
    const user = userEvent.setup();
    const grant = createDeferred<never[]>();
    getBatchStatusMock.mockResolvedValue(retryBatch());
    retryGrantMock.mockReturnValue(grant.promise);
    const view = renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    fireEvent.submit(screen.getByRole("button", { name: "确认重试" }).closest("form") as HTMLFormElement);
    await waitFor(() => expect(retryGrantMock).toHaveBeenCalledTimes(1));

    bumpAuthSessionGeneration();
    view.rerender(resultElement(false, "auth-marker", SOURCE_BATCH_ID));
    await act(async () => { grant.resolve([]); });
    await act(async () => { await Promise.resolve(); });
    expect(retryBatchCommandMock).not.toHaveBeenCalled();
    expect(retryGrantMock).toHaveBeenCalledTimes(1);
  });

  it("does not submit a retry after the role changes during the grant", async () => {
    const user = userEvent.setup();
    const grant = createDeferred<never[]>();
    const refreshed = createDeferred<BatchStatus>();
    let reads = 0;
    getBatchStatusMock.mockImplementation(() => {
      reads += 1;
      return reads <= 2 ? Promise.resolve(retryBatch()) : refreshed.promise;
    });
    retryGrantMock.mockReturnValue(grant.promise);
    const view = renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    fireEvent.submit(screen.getByRole("button", { name: "确认重试" }).closest("form") as HTMLFormElement);
    await waitFor(() => expect(retryGrantMock).toHaveBeenCalledTimes(1));

    publishAuth("viewer");
    view.rerender(resultElement(false, "auth-marker", SOURCE_BATCH_ID));
    expect(screen.getByRole("button", { name: /node-a/ })).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: /node-a/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /重试选中的失败/ })).not.toBeInTheDocument();
    await act(async () => { grant.resolve([]); });
    await act(async () => { refreshed.resolve(retryBatch()); });
    expect(retryBatchCommandMock).not.toHaveBeenCalled();
    expect(clearStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.batchCommandRetry);
  });

  it("does not open a child or delete the source after the retry request is sent and the session changes", async () => {
    const user = userEvent.setup();
    const post = createDeferred<BatchRetryResult>();
    let reads = 0;
    getBatchStatusMock.mockImplementation((_token: string, id: string) => {
      reads += 1;
      return Promise.resolve(id === CHILD_BATCH_ID ? childBatch() : retryBatch());
    });
    retryBatchCommandMock.mockReturnValue(post.promise);
    const view = renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    fireEvent.submit(screen.getByRole("button", { name: "确认重试" }).closest("form") as HTMLFormElement);
    await waitFor(() => expect(retryBatchCommandMock).toHaveBeenCalledTimes(1));

    const readsAtChange = reads;
    bumpAuthSessionGeneration();
    view.rerender(resultElement(false, "auth-marker", SOURCE_BATCH_ID));
    await act(async () => { post.resolve(retryResult()); });
    expect(await screen.findByRole("button", { name: /node-a/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /child-node/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /重试选中的失败/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "继续本次提交" })).not.toBeInTheDocument();
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(1);
    expect(reads).toBeGreaterThan(readsAtChange);
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
  });

  it("hides retry writes from a viewer and clears selection on role change without dropping loaded results", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    const refreshed = createDeferred<BatchStatus>();
    let reads = 0;
    const status = retryBatch();
    getBatchStatusMock.mockImplementation(() => {
      reads += 1;
      return reads === 1 ? Promise.resolve(status) : refreshed.promise;
    });
    getTaskLogsMock.mockResolvedValue([logEvent("1", "parent-log")]);
    const view = renderSource();
    await chooseRetry(user, /node-a/);
    expect(screen.getByRole("checkbox", { name: /node-a/ })).toBeChecked();
    await user.click(screen.getByRole("button", { name: /node-a/ }));
    expect(await screen.findByText("parent-log")).toBeInTheDocument();

    publishAuth("viewer");
    view.rerender(resultElement(false, "auth-marker", SOURCE_BATCH_ID));
    expect(screen.getByRole("button", { name: /node-a/ })).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /重试选中的失败/ })).not.toBeInTheDocument();
    expect(screen.queryByText("parent-log")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: COPY_ZH })).toBeDisabled();

    await act(async () => { refreshed.resolve(status); });
    await enabledCopyButton();
    const text = await readCopy(user, writeText);
    expect(text).not.toContain("parent-log");
    expect(text).toContain("日志尚未加载");
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(deleteBatchMock).not.toHaveBeenCalled();
  });

  it("closes the child through its own retain policy and keeps parent and child copy snapshots apart", async () => {
    const user = userEvent.setup();
    const writeText = installResolvedClipboard();
    getBatchStatusMock.mockImplementation((_token: string, id: string) => (
      Promise.resolve(id === CHILD_BATCH_ID ? childBatch() : retryBatch())
    ));
    getTaskLogsMock.mockImplementation((_token: string, taskId: number) => (
      Promise.resolve([logEvent(String(taskId), taskId === 70 ? "child-log" : "parent-log")])
    ));
    retryBatchCommandMock.mockResolvedValue(retryResult(false));
    renderSource(true);
    await user.click(await screen.findByRole("button", { name: /node-a/ }));
    expect(await screen.findByText("parent-log")).toBeInTheDocument();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    await user.click(screen.getByRole("button", { name: "确认重试" }));
    const childDialog = await waitFor(() => dialogFor(CHILD_BATCH_ID));
    await user.click(await within(childDialog).findByRole("button", { name: /child-node/ }));
    expect(await within(childDialog).findByText("child-log")).toBeInTheDocument();

    const parentDialog = dialogFor(SOURCE_BATCH_ID);
    fireEvent.click(within(parentDialog).getByRole("button", { name: COPY_ZH, hidden: true }));
    await waitFor(() => expect(writeText).toHaveBeenCalled());
    const parentCopy = String(writeText.mock.calls.at(-1)?.[0]);
    expect(parentCopy).toContain("parent-log");
    expect(parentCopy).not.toContain("child-log");
    await user.click(within(childDialog).getByRole("button", { name: COPY_ZH }));
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(2));
    const childCopy = String(writeText.mock.calls.at(-1)?.[0]);
    expect(childCopy).toContain("child-log");
    expect(childCopy).not.toContain("parent-log");

    fireEvent.click(within(parentDialog).getByRole("button", { name: "Close", hidden: true }));
    expect(deleteBatchMock).toHaveBeenCalledTimes(1);
    expect(deleteBatchMock).toHaveBeenCalledWith("auth-marker", CHILD_BATCH_ID);
    expect(deleteBatchMock).not.toHaveBeenCalledWith("auth-marker", SOURCE_BATCH_ID);
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);
  });

  it("stays unknown when the source read fails and continues only the same submission after a later read", async () => {
    const user = userEvent.setup();
    let failRead = false;
    let posts = 0;
    getBatchStatusMock.mockImplementation((_token: string, id: string) => {
      if (id === CHILD_BATCH_ID) return Promise.resolve(childBatch());
      if (failRead) {
        failRead = false;
        return Promise.reject(new Error("status unavailable"));
      }
      return Promise.resolve(retryBatch());
    });
    retryBatchCommandMock.mockImplementation(() => {
      posts += 1;
      if (posts === 1) {
        failRead = true;
        return Promise.reject(new TypeError("network down"));
      }
      return Promise.resolve(retryResult());
    });
    renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    await user.click(screen.getByRole("button", { name: "确认重试" }));

    expect(await screen.findByText("暂时无法读取原批次。结果仍未确认，不能判断为未执行。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "继续本次提交" })).not.toBeInTheDocument();
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(1);
    const key = retryBatchCommandMock.mock.calls[0]?.[4];
    await user.click(screen.getByRole("button", { name: "读取原批次状态" }));
    await user.click(await screen.findByRole("button", { name: "继续本次提交" }));
    expect(await screen.findByRole("button", { name: /child-node/ })).toBeInTheDocument();
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(2);
    expect(retryBatchCommandMock.mock.calls[1]?.[4]).toBe(key);
  });

  it("does not continue when selected pairs point at different retry batches", async () => {
    const user = userEvent.setup();
    const tasks = [
      taskOf({ id: 7, nodeId: 3, nodeName: "node-a", status: "failed", lastError: "exit 7" }),
      taskOf({ id: 8, nodeId: 4, nodeName: "node-b", status: "failed", lastError: "exit 8" }),
    ];
    const sources = [retrySource(), retrySource({ taskId: 8, nodeId: 4, runId: 11 })];
    let posted = false;
    getBatchStatusMock.mockImplementation(() => {
      if (!posted) return Promise.resolve(retryBatch({ tasks, sources }));
      return Promise.resolve(retryBatch({
        tasks,
        sources,
        links: [
          { taskId: 7, runId: 9, batchId: CHILD_BATCH_ID, deleted: false },
          { taskId: 8, runId: 11, batchId: "C".repeat(26), deleted: false },
        ],
      }));
    });
    retryBatchCommandMock.mockImplementation(() => {
      posted = true;
      return Promise.reject(new TypeError("network down"));
    });
    renderSource();
    await chooseRetry(user, /node-a/);
    await chooseRetry(user, /node-b/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（2）" }));
    await user.type(screen.getByRole("textbox"), "2");
    await user.click(screen.getByRole("button", { name: "确认重试" }));

    expect(await screen.findByText("来源对应的重试批次不一致，已停止继续提交。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "继续本次提交" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "查看重试批次" })).not.toBeInTheDocument();
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(1);
  });

  it("releases a new retry after a challenged grant and a final step-up rejection", async () => {
    const user = userEvent.setup();
    const submitting = "正在提交重试，请保持窗口打开。关闭不会取消已经发出的请求。";
    getBatchStatusMock.mockImplementation((_token: string, id: string) => (
      Promise.resolve(id === CHILD_BATCH_ID ? childBatch() : retryBatch())
    ));
    retryGrantMock.mockImplementation((_token: string, _input: unknown, proof?: string) => (
      proof ? Promise.resolve([]) : Promise.reject(stepUpRequiredError())
    ));
    retryBatchCommandMock.mockRejectedValue(stepUpRequiredError());
    renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    fireEvent.submit(screen.getByRole("button", { name: "确认重试" }).closest("form") as HTMLFormElement);

    await waitFor(() => {
      expect(retryBatchCommandMock).toHaveBeenCalledTimes(1);
      expect(screen.getByRole("button", { name: "确认重试" })).toBeEnabled();
    });
    await act(async () => { await Promise.resolve(); });
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(1);
    expect(retryGrantMock).toHaveBeenCalledTimes(2);
    expect(retryGrantMock.mock.calls[0]?.[2]).toBeUndefined();
    expect(retryGrantMock.mock.calls[1]?.[2]).toBe("proof-retry");
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(
      STEP_UP_ACTIONS.batchCommandRetry,
      { persist: false, reuseCached: false },
    );
    expect(retryBatchCommandMock.mock.calls[0]?.[3]).toBe("proof-retry");
    expect(screen.queryByText(submitting)).not.toBeInTheDocument();
    expect(screen.getByText("Additional verification required")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "继续本次提交" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("button", { name: "确认重试" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    fireEvent.submit(screen.getByRole("button", { name: "确认重试" }).closest("form") as HTMLFormElement);

    await waitFor(() => {
      expect(retryBatchCommandMock).toHaveBeenCalledTimes(2);
      expect(screen.getByRole("button", { name: "确认重试" })).toBeEnabled();
    });
    expect(retryBatchCommandMock.mock.calls[1]?.[4]).not.toBe(retryBatchCommandMock.mock.calls[0]?.[4]);
    expect(screen.queryByText(submitting)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /child-node/ })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "取消" }));
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);
    expect(deleteBatchMock).toHaveBeenCalledWith("auth-marker", SOURCE_BATCH_ID);
  });

  it("keeps the same recoverable key when continuing an unknown retry is rejected", async () => {
    const user = userEvent.setup();
    const submitting = "正在提交重试，请保持窗口打开。关闭不会取消已经发出的请求。";
    let posts = 0;
    getBatchStatusMock.mockImplementation((_token: string, id: string) => (
      Promise.resolve(id === CHILD_BATCH_ID ? childBatch() : retryBatch())
    ));
    retryGrantMock.mockImplementation((_token: string, _input: unknown, proof?: string) => (
      proof ? Promise.resolve([]) : Promise.reject(stepUpRequiredError())
    ));
    retryBatchCommandMock.mockImplementation(() => {
      posts += 1;
      if (posts === 1) return Promise.reject(new TypeError("network down"));
      if (posts === 2) return Promise.reject(stepUpRequiredError());
      return Promise.resolve(retryResult());
    });
    renderSource();
    await chooseRetry(user, /node-a/);
    await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
    await user.click(screen.getByRole("button", { name: "确认重试" }));

    const continueRetry = await screen.findByRole("button", { name: "继续本次提交" });
    expect(continueRetry).toBeEnabled();
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(submitting)).not.toBeInTheDocument();
    const key = retryBatchCommandMock.mock.calls[0]?.[4];

    await user.click(continueRetry);
    await waitFor(() => expect(retryBatchCommandMock).toHaveBeenCalledTimes(2));
    const continued = await screen.findByRole("button", { name: "继续本次提交" });
    expect(continued).toBeEnabled();
    await act(async () => { await Promise.resolve(); });
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(2);
    expect(retryBatchCommandMock.mock.calls[1]?.[4]).toBe(key);
    expect(screen.queryByText(submitting)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "重试选中的失败（1）" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /child-node/ })).not.toBeInTheDocument();
    expect(deleteBatchMock).not.toHaveBeenCalled();

    await user.click(continued);
    expect(await screen.findByRole("button", { name: /child-node/ })).toBeInTheDocument();
    expect(retryBatchCommandMock).toHaveBeenCalledTimes(3);
    expect(retryBatchCommandMock.mock.calls[2]?.[4]).toBe(key);
    expect(screen.queryByText(submitting)).not.toBeInTheDocument();
  });

  function transportResponse(status: number, body: unknown): Response {
    return {
      status,
      ok: status >= 200 && status < 300,
      headers: { get: () => null },
      text: async () => JSON.stringify(body),
    } as unknown as Response;
  }

  function postedRetryKey(call: unknown[] | undefined): string {
    const init = call?.[1] as { headers?: Record<string, string>; method?: string; body?: string } | undefined;
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ sources: [{ task_id: 7, run_id: 9 }] });
    const key = init?.headers?.["Idempotency-Key"];
    if (!key) throw new Error("missing idempotency key");
    return key;
  }

  async function renderRealRetryTransport(status: number, body: unknown) {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => transportResponse(status, body));
    vi.stubGlobal("fetch", fetchMock);
    const retry = createBatchApi().retryBatchCommand;
    getBatchStatusMock.mockImplementation((_token: string, id: string) => (
      Promise.resolve(id === CHILD_BATCH_ID ? childBatch() : retryBatch())
    ));
    retryBatchCommandMock.mockImplementation((...args: Parameters<typeof retry>) => retry(...args));
    renderSource();
    return fetchMock;
  }

  it.each([
    { label: "HTTP 200 with envelope code 400", body: { code: 400, message: "bad envelope", data: null } },
    { label: "HTTP 200 with a nonnumeric envelope code", body: { code: "400", message: "bad envelope", data: null } },
    { label: "HTTP 200 with a null envelope code", body: { code: null, message: "bad envelope", data: null } },
  ])("keeps a sent retry unknown for $label, retains the key, and does not clean up", async ({ body }) => {
    const user = userEvent.setup();
    const fetchMock = await renderRealRetryTransport(200, body);
    try {
      await chooseRetry(user, /node-a/);
      await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
      await user.click(screen.getByRole("button", { name: "确认重试" }));

      expect(await screen.findByRole("button", { name: "继续本次提交" })).toBeEnabled();
      expect(screen.getByText("重试结果尚未确认。请读取原批次状态核对，不要认为命令没有执行。")).toBeInTheDocument();
      expect(fetchMock).toHaveBeenCalledTimes(1);
      const key = postedRetryKey(fetchMock.mock.calls[0]);
      expect(String(fetchMock.mock.calls[0]?.[0])).toContain(`/batch-commands/${SOURCE_BATCH_ID}/retry`);

      await user.click(screen.getByRole("button", { name: "继续本次提交" }));
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
      expect(postedRetryKey(fetchMock.mock.calls[1])).toBe(key);
      expect(await screen.findByRole("button", { name: "继续本次提交" })).toBeEnabled();
      expect(screen.queryByText("正在提交重试，请保持窗口打开。关闭不会取消已经发出的请求。")).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /child-node/ })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "重试选中的失败（1）" })).not.toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Close" }));
      expect(deleteBatchMock).not.toHaveBeenCalled();
      expect(onOpenChangeMock).toHaveBeenCalledWith(false);
    } finally {
      vi.unstubAllGlobals();
      retryBatchCommandMock.mockReset();
    }
  });

  it("releases a sent retry when the real HTTP status is a client error", async () => {
    const user = userEvent.setup();
    const fetchMock = await renderRealRetryTransport(400, { code: 400, message: "rejected", data: null });
    try {
      await chooseRetry(user, /node-a/);
      await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
      await user.click(screen.getByRole("button", { name: "确认重试" }));

      await waitFor(() => {
        expect(screen.getByText("rejected")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: "确认重试" })).toBeEnabled();
      });
      expect(screen.queryByRole("button", { name: "继续本次提交" })).not.toBeInTheDocument();
      expect(screen.queryByText("重试结果尚未确认。请读取原批次状态核对，不要认为命令没有执行。")).not.toBeInTheDocument();
      const key = postedRetryKey(fetchMock.mock.calls[0]);

      await user.click(screen.getByRole("button", { name: "取消" }));
      await user.click(screen.getByRole("button", { name: "重试选中的失败（1）" }));
      await user.click(screen.getByRole("button", { name: "确认重试" }));
      await waitFor(() => {
        expect(fetchMock).toHaveBeenCalledTimes(2);
        expect(screen.getByRole("button", { name: "确认重试" })).toBeEnabled();
      });
      expect(postedRetryKey(fetchMock.mock.calls[1])).not.toBe(key);
      expect(screen.queryByRole("button", { name: /child-node/ })).not.toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "取消" }));
      await user.click(screen.getByRole("button", { name: "Close" }));
      expect(onOpenChangeMock).toHaveBeenCalledWith(false);
      expect(deleteBatchMock).toHaveBeenCalledWith("auth-marker", SOURCE_BATCH_ID);
    } finally {
      vi.unstubAllGlobals();
      retryBatchCommandMock.mockReset();
    }
  });

  it("keeps a successful wire row nonselectable while the failed row and retry link stay usable", async () => {
    const user = userEvent.setup();
    const wire = {
      batch_id: SOURCE_BATCH_ID,
      total: 2,
      status_counts: { success: 1, failed: 1 },
      retain: false,
      source_batch_id: null,
      tasks: [
        { id: 11, name: "ok", status: "success", node_id: 3, node: { id: 3, name: "node-ok" } },
        { id: 12, name: "bad", status: "failed", node_id: 4, node: { id: 4, name: "node-fail" }, last_error: "exit 7" },
      ],
      dispatches: [
        { task_id: 11, status: "accepted" },
        { task_id: 12, status: "accepted" },
      ],
      retry_sources: [
        {
          task_id: 11,
          node_id: 3,
          run_id: 41,
          eligible: false,
          reason: "not_failed",
          failure_kind: null,
          retry_batch_id: null,
        },
        {
          task_id: 12,
          node_id: 4,
          run_id: 42,
          eligible: true,
          reason: "eligible",
          failure_kind: "execution",
          retry_batch_id: null,
        },
      ],
      retry_links: [
        { task_id: 11, run_id: 41, batch_id: CHILD_BATCH_ID, deleted: false },
      ],
    };
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => transportResponse(200, { code: 0, message: "ok", data: wire }));
    vi.stubGlobal("fetch", fetchMock);
    const readStatus = createBatchApi().getBatchStatus;
    getBatchStatusMock.mockImplementation((token: string, id: string) => (
      id === CHILD_BATCH_ID ? Promise.resolve(childBatch()) : readStatus(token, id)
    ));
    try {
      renderSource();
      const failed = await screen.findByRole("checkbox", { name: /node-fail/ });
      expect(screen.queryByRole("checkbox", { name: /node-ok/ })).not.toBeInTheDocument();
      expect(screen.getByText("这次运行不是明确失败")).toBeInTheDocument();
      expect(screen.getByText("执行失败")).toBeInTheDocument();
      expect(fetchMock).toHaveBeenCalled();
      expect(String(fetchMock.mock.calls[0]?.[0])).toContain(`/batch-commands/${SOURCE_BATCH_ID}`);

      await user.click(failed);
      expect(failed).toBeChecked();
      expect(screen.getByRole("button", { name: "重试选中的失败（1）" })).toBeEnabled();

      await user.click(screen.getByRole("button", { name: "查看任务 11 的重试批次" }));
      expect(await screen.findByRole("button", { name: /child-node/ })).toBeInTheDocument();
      expect(retryBatchCommandMock).not.toHaveBeenCalled();
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
