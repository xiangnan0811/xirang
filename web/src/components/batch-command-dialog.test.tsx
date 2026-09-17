import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "@/lib/api/client";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { BatchCommandDialog } from "./batch-command-dialog";
import type { NodeRecord } from "@/types/domain";

const { requestBatchCommandCredentialGrantMock, createBatchCommandMock, withStepUpMock, useStepUpActionMock, oneShotStepUpOptions, onOpenChangeMock, onSuccessMock } = vi.hoisted(() => {
  const withStepUpMock = vi.fn((action: (proof?: string) => Promise<unknown>) => action("step-up-marker"));
  const stepUpHookMock = vi.fn((stepUpAction?: unknown, options?: unknown) => {
    stepUpHookMock.lastAction = stepUpAction;
    stepUpHookMock.lastOptions = options;
    return withStepUpMock;
  }) as ReturnType<typeof vi.fn> & { lastAction?: unknown; lastOptions?: unknown };

  return {
    requestBatchCommandCredentialGrantMock: vi.fn(),
    createBatchCommandMock: vi.fn(),
    withStepUpMock,
    useStepUpActionMock: stepUpHookMock,
    oneShotStepUpOptions: { persist: false, reuseCached: false },
    onOpenChangeMock: vi.fn(),
    onSuccessMock: vi.fn(),
  };
});

vi.mock("@/hooks/use-step-up-action", () => ({
  useStepUpAction: useStepUpActionMock,
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    requestBatchCommandCredentialGrant: requestBatchCommandCredentialGrantMock,
    createBatchCommand: createBatchCommandMock,
  },
}));

const nodes: NodeRecord[] = [
  {
    id: 1,
    name: "node-a",
    host: "redacted-a",
    address: "redacted-a",
    ip: "redacted-a",
    port: 22,
    username: "root",
    authType: "key",
    basePath: "/",
    tags: [],
    status: "online",
    lastSeenAt: "-",
    lastBackupAt: "-",
    diskFreePercent: 0,
    diskUsedGb: 0,
    diskTotalGb: 0,
    diskProbeAt: "-",
  },
  {
    id: 2,
    name: "node-b",
    host: "redacted-b",
    address: "redacted-b",
    ip: "redacted-b",
    port: 22,
    username: "root",
    authType: "key",
    basePath: "/",
    tags: [],
    status: "online",
    lastSeenAt: "-",
    lastBackupAt: "-",
    diskFreePercent: 0,
    diskUsedGb: 0,
    diskTotalGb: 0,
    diskProbeAt: "-",
  },
];

describe("BatchCommandDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    requestBatchCommandCredentialGrantMock.mockResolvedValue([{ id: 11, status: "active" }]);
    createBatchCommandMock.mockResolvedValue({ batchId: "batch-1", retain: false });
    withStepUpMock.mockImplementation((action: (proof?: string) => Promise<unknown>) => action("step-up-marker"));
    useStepUpActionMock.mockClear();
    useStepUpActionMock.lastAction = undefined;
    useStepUpActionMock.lastOptions = undefined;
  });

  it("requires impact review acknowledgement before requesting grant and creating a multi-node command", async () => {
    const user = userEvent.setup();
    render(
      <BatchCommandDialog
        open
        onOpenChange={onOpenChangeMock}
        nodes={nodes}
        token="auth-marker"
        defaultNodeIds={[1, 2]}
        onSuccess={onSuccessMock}
      />,
    );

    await user.type(screen.getByLabelText("命令"), "df -h");
    await user.click(screen.getByRole("button", { name: "执行" }));

    expect(screen.getByText("执行前复核影响")).toBeInTheDocument();
    expect(screen.getByText("node-a")).toBeInTheDocument();
    expect(screen.getByText("node-b")).toBeInTheDocument();
    expect(requestBatchCommandCredentialGrantMock).not.toHaveBeenCalled();
    expect(createBatchCommandMock).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "确认并验证" }));
    expect(screen.getByText("请输入 2 以确认选中节点数。")).toBeInTheDocument();
    expect(requestBatchCommandCredentialGrantMock).not.toHaveBeenCalled();
    expect(createBatchCommandMock).not.toHaveBeenCalled();

    await user.type(screen.getByLabelText("输入 2 以确认选中节点数"), "2");
    await user.click(screen.getByRole("button", { name: "确认并验证" }));

    await waitFor(() => expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledTimes(1));
    expect(useStepUpActionMock.lastAction).toBe(STEP_UP_ACTIONS.batchCommandCreate);
    expect(useStepUpActionMock.lastOptions).toEqual(oneShotStepUpOptions);
    expect(withStepUpMock).toHaveBeenCalledWith(expect.any(Function));
    expect(apiClient.requestBatchCommandCredentialGrant).toHaveBeenCalledWith("auth-marker", {
      nodeIds: [1, 2],
      reason: "批量操作 2 个节点",
      requestedTtlSeconds: 600,
    }, "step-up-marker");
    expect(apiClient.createBatchCommand).toHaveBeenCalledWith(
      "auth-marker",
      [1, 2],
      "df -h",
      undefined,
      false,
      "step-up-marker",
      expect.stringMatching(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i),
    );
    expect(requestBatchCommandCredentialGrantMock.mock.invocationCallOrder[0]).toBeLessThan(createBatchCommandMock.mock.invocationCallOrder[0]);
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);
    expect(onSuccessMock).toHaveBeenCalledWith({ batchId: "batch-1", retain: false });

    const browserStorage = JSON.stringify({ ...localStorage, ...sessionStorage });
    expect(browserStorage).not.toContain("df -h");
    expect(browserStorage).not.toContain("step-up-marker");
    expect(browserStorage).not.toContain("active");
  });

  it("shows generic dangerous-command warnings without blocking confirmed execution", async () => {
    const user = userEvent.setup();
    render(
      <BatchCommandDialog
        open
        onOpenChange={onOpenChangeMock}
        nodes={nodes}
        token="auth-marker"
        defaultNodeIds={[1]}
        onSuccess={onSuccessMock}
      />,
    );

    await user.type(screen.getByLabelText("命令"), "rm -rf /tmp/cache");
    await user.click(screen.getByRole("button", { name: "执行" }));

    expect(screen.getByText("检测到的风险信号")).toBeInTheDocument();
    expect(screen.getByText("递归或强制删除文件")).toBeInTheDocument();
    expect(screen.queryByLabelText(/确认选中节点数/)).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "确认并验证" }));

    await waitFor(() => expect(createBatchCommandMock).toHaveBeenCalledTimes(1));
    expect(apiClient.createBatchCommand).toHaveBeenCalledWith(
      "auth-marker",
      [1],
      "rm -rf /tmp/cache",
      undefined,
      false,
      "step-up-marker",
      expect.stringMatching(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i),
    );
  });

  it("reuses the same idempotency key through a failed create retry and step-up", async () => {
    const user = userEvent.setup();
    withStepUpMock.mockImplementation(async (action: (proof?: string) => Promise<unknown>) => {
      try {
        return await action();
      } catch {
        return await action("step-up-marker");
      }
    });
    createBatchCommandMock
      .mockRejectedValueOnce(new Error("创建结果未确认"))
      .mockResolvedValueOnce({ batchId: "batch-1", retain: false });

    render(
      <BatchCommandDialog
        open
        onOpenChange={onOpenChangeMock}
        nodes={nodes}
        token="auth-marker"
        defaultNodeIds={[1]}
        onSuccess={onSuccessMock}
      />,
    );

    await user.type(screen.getByLabelText("命令"), "uptime");
    await user.click(screen.getByRole("button", { name: "执行" }));
    await user.click(screen.getByRole("button", { name: "确认并验证" }));

    await waitFor(() => expect(createBatchCommandMock).toHaveBeenCalledTimes(2));
    const firstKey = createBatchCommandMock.mock.calls[0]?.[6];
    const replayKey = createBatchCommandMock.mock.calls[1]?.[6];
    expect(firstKey).toEqual(expect.stringMatching(/^[0-9a-f-]{36}$/i));
    expect(replayKey).toBe(firstKey);
    expect(createBatchCommandMock.mock.calls[0]?.[5]).toBeUndefined();
    expect(createBatchCommandMock.mock.calls[1]?.[5]).toBe("step-up-marker");
    expect(onSuccessMock).toHaveBeenCalledWith({ batchId: "batch-1", retain: false });
  });

  it("preserves the key for a lost-response retry and rotates it after the command changes", async () => {
    const user = userEvent.setup();
    createBatchCommandMock
      .mockRejectedValueOnce(new Error("创建结果未确认"))
      .mockRejectedValueOnce(new Error("创建结果未确认"))
      .mockResolvedValueOnce({ batchId: "batch-2", retain: false });

    render(
      <BatchCommandDialog
        open
        onOpenChange={onOpenChangeMock}
        nodes={nodes}
        token="auth-marker"
        defaultNodeIds={[1]}
        onSuccess={onSuccessMock}
      />,
    );

    await user.type(screen.getByLabelText("命令"), "uptime");
    await user.click(screen.getByRole("button", { name: "执行" }));
    await user.click(screen.getByRole("button", { name: "确认并验证" }));

    await waitFor(() => expect(screen.getByText("创建结果未确认")).toBeInTheDocument());
    expect(screen.getByText("创建结果未确认").textContent).not.toMatch(/password|secret|token/i);

    await user.click(screen.getByRole("button", { name: "确认并验证" }));
    await waitFor(() => expect(createBatchCommandMock).toHaveBeenCalledTimes(2));
    expect(createBatchCommandMock.mock.calls[1]?.[6]).toBe(createBatchCommandMock.mock.calls[0]?.[6]);

    await user.click(screen.getByRole("button", { name: "返回编辑" }));
    await user.clear(screen.getByLabelText("命令"));
    await user.type(screen.getByLabelText("命令"), "df -h");
    await user.click(screen.getByRole("button", { name: "执行" }));
    await user.click(screen.getByRole("button", { name: "确认并验证" }));

    await waitFor(() => expect(createBatchCommandMock).toHaveBeenCalledTimes(3));
    expect(createBatchCommandMock.mock.calls[2]?.[2]).toBe("df -h");
    expect(createBatchCommandMock.mock.calls[2]?.[6]).not.toBe(createBatchCommandMock.mock.calls[0]?.[6]);
    expect(onSuccessMock).toHaveBeenCalledWith({ batchId: "batch-2", retain: false });
  });
});
