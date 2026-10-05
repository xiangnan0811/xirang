import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { StrictMode } from "react";
import { ApiError } from "@/lib/api/core";
import { apiClient } from "@/lib/api/client";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { BatchCommandDialog } from "./batch-command-dialog";
import type { NodeRecord } from "@/types/domain";

const {
  requestBatchCommandCredentialGrantMock,
  createBatchCommandMock,
  withStepUpMock,
  useStepUpActionMock,
  oneShotStepUpOptions,
  onOpenChangeMock,
  onSuccessMock,
  ensureStepUpProofMock,
  clearStepUpProofMock,
  useRealStepUpRef,
  authRef,
} = vi.hoisted(() => {
  const withStepUpMock = vi.fn((action: (proof?: string) => Promise<unknown>) => action("step-up-marker"));
  const stepUpHookMock = vi.fn((stepUpAction?: unknown, options?: unknown) => {
    stepUpHookMock.lastAction = stepUpAction;
    stepUpHookMock.lastOptions = options;
    return withStepUpMock;
  }) as ReturnType<typeof vi.fn<(stepUpAction?: unknown, options?: unknown) => typeof withStepUpMock>> & { lastAction?: unknown; lastOptions?: unknown };

  return {
    requestBatchCommandCredentialGrantMock: vi.fn(),
    createBatchCommandMock: vi.fn(),
    withStepUpMock,
    useStepUpActionMock: stepUpHookMock,
    oneShotStepUpOptions: { persist: false, reuseCached: false },
    onOpenChangeMock: vi.fn(),
    onSuccessMock: vi.fn(),
    ensureStepUpProofMock: vi.fn(),
    clearStepUpProofMock: vi.fn(),
    useRealStepUpRef: { current: false },
    authRef: {
      current: {
        role: "admin" as "admin" | "operator" | "viewer" | null,
        token: "auth-marker",
      },
    },
  };
});

vi.mock("@/hooks/use-step-up-action", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/use-step-up-action")>("@/hooks/use-step-up-action");
  return {
    useStepUpAction: (stepUpAction: unknown, options?: unknown) => {
      useStepUpActionMock(stepUpAction, options);
      if (useRealStepUpRef.current) {
        return actual.useStepUpAction(stepUpAction as never, options as never);
      }
      return withStepUpMock;
    },
  };
});


vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: authRef.current.token,
    username: "tester",
    role: authRef.current.role,
    userId: 1,
    isAuthenticated: Boolean(authRef.current.token),
    login: vi.fn(),
    logout: vi.fn(),
    setTotpEnabled: vi.fn(),
    totpEnabled: false,
    ensureStepUpProof: ensureStepUpProofMock,
    clearStepUpProof: clearStepUpProofMock,
  }),
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
  },
];

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

function commandElement(token = "auth-marker") {
  return (
    <BatchCommandDialog
      open
      onOpenChange={onOpenChangeMock}
      nodes={nodes}
      token={token}
      defaultNodeIds={[1]}
      onSuccess={onSuccessMock}
    />
  );
}

async function submitReviewedCommand(user: ReturnType<typeof userEvent.setup>, command = "uptime") {
  await user.type(screen.getByLabelText("命令"), command);
  await user.click(screen.getByRole("button", { name: "执行" }));
  await user.click(screen.getByRole("button", { name: "确认并验证" }));
}

function expectNoBatchFollowUp() {
  expect(createBatchCommandMock).not.toHaveBeenCalled();
  expect(ensureStepUpProofMock).not.toHaveBeenCalled();
  expect(clearStepUpProofMock).not.toHaveBeenCalled();
  expect(onOpenChangeMock).not.toHaveBeenCalled();
  expect(onSuccessMock).not.toHaveBeenCalled();
  expect(screen.queryByText("需要二次验证")).not.toBeInTheDocument();
}

describe("BatchCommandDialog", () => {
  it("starts a fresh command and selection when reopened with new defaults", async () => {
    const user = userEvent.setup();
    const props = { nodes, token: "test-token", onOpenChange: vi.fn() };
    const view = render(<BatchCommandDialog open defaultNodeIds={[1]} {...props} />);
    await user.type(screen.getByLabelText("命令"), "echo test-session");
    view.rerender(<BatchCommandDialog open={false} defaultNodeIds={[1]} {...props} />);
    view.rerender(<BatchCommandDialog open defaultNodeIds={[2]} {...props} />);
    expect(screen.getByLabelText("命令")).toHaveValue("");
    const selected = screen.getAllByRole("checkbox").filter((element) => (element as HTMLInputElement).checked);
    expect(selected).toHaveLength(1);
    expect(selected[0].closest("label")).toHaveTextContent("node-b");
  });

  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    useRealStepUpRef.current = false;
    authRef.current = { role: "admin", token: "auth-marker" };
    requestBatchCommandCredentialGrantMock.mockReset();
    requestBatchCommandCredentialGrantMock.mockResolvedValue([{ id: 11, status: "active" }]);
    createBatchCommandMock.mockReset();
    createBatchCommandMock.mockResolvedValue({ batchId: "batch-1", retain: false });
    ensureStepUpProofMock.mockReset();
    ensureStepUpProofMock.mockResolvedValue("step-up-marker");
    clearStepUpProofMock.mockReset();
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

  it("does not create or open step-up when a pending grant rejects after unmount", async () => {
    useRealStepUpRef.current = true;
    const user = userEvent.setup();
    const pendingGrant = createDeferred<unknown>();
    requestBatchCommandCredentialGrantMock.mockReturnValueOnce(pendingGrant.promise);
    const view = render(commandElement());

    await submitReviewedCommand(user);
    await waitFor(() => expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledTimes(1));
    expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledWith("auth-marker", {
      nodeIds: [1],
      reason: "批量操作 1 个节点",
      requestedTtlSeconds: 600,
    }, undefined);

    view.unmount();
    const openChangesAfterUnmount = onOpenChangeMock.mock.calls.length;
    await act(async () => {
      pendingGrant.reject(createStepUpRequiredError());
    });

    expect(createBatchCommandMock).not.toHaveBeenCalled();
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(clearStepUpProofMock).not.toHaveBeenCalled();
    expect(onSuccessMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledTimes(openChangesAfterUnmount);
  });

  it("does not create after a pending grant resolves once the role is viewer", async () => {
    useRealStepUpRef.current = true;
    const user = userEvent.setup();
    const pendingGrant = createDeferred<unknown>();
    requestBatchCommandCredentialGrantMock.mockReturnValueOnce(pendingGrant.promise);
    const view = render(commandElement());

    await submitReviewedCommand(user);
    await waitFor(() => expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledTimes(1));

    authRef.current.role = "viewer";
    view.rerender(commandElement());
    await act(async () => {
      pendingGrant.resolve([{ id: 11, status: "active" }]);
    });

    expectNoBatchFollowUp();

    await user.click(screen.getByRole("button", { name: "确认并验证" }));
    expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledTimes(1);
    expect(createBatchCommandMock).not.toHaveBeenCalled();
    expect(onSuccessMock).not.toHaveBeenCalled();
  });

  it("does not continue a pending grant after the role leaves and returns to admin", async () => {
    useRealStepUpRef.current = true;
    const user = userEvent.setup();
    const pendingGrant = createDeferred<unknown>();
    requestBatchCommandCredentialGrantMock.mockReturnValueOnce(pendingGrant.promise);
    const view = render(commandElement());

    await submitReviewedCommand(user);
    await waitFor(() => expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledTimes(1));

    authRef.current.role = "operator";
    view.rerender(commandElement());
    authRef.current.role = "admin";
    view.rerender(commandElement());
    await act(async () => {
      pendingGrant.reject(createStepUpRequiredError());
    });

    expectNoBatchFollowUp();

    await user.click(screen.getByRole("button", { name: "确认并验证" }));
    await waitFor(() => expect(createBatchCommandMock).toHaveBeenCalledTimes(1));
    expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledTimes(2);
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(onSuccessMock).toHaveBeenCalledWith({ batchId: "batch-1", retain: false });
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);
  });

  it("does not create when the token changes while the grant is pending", async () => {
    useRealStepUpRef.current = true;
    const user = userEvent.setup();
    const pendingGrant = createDeferred<unknown>();
    const abandonedKey = "00000000-0000-4000-8000-000000000001";
    const nextKey = "00000000-0000-4000-8000-000000000002";
    const randomUUID = vi.spyOn(crypto, "randomUUID")
      .mockReturnValueOnce(abandonedKey)
      .mockReturnValueOnce(nextKey);
    requestBatchCommandCredentialGrantMock.mockReturnValueOnce(pendingGrant.promise);
    const view = render(commandElement());

    const fillDraft = async () => {
      await user.type(screen.getByLabelText("任务名称（可选）"), "draft-name");
      await user.click(screen.getByRole("checkbox", { name: /node-b/ }));
      await user.click(screen.getByRole("checkbox", { name: /保留任务记录/ }));
      await user.type(screen.getByLabelText("命令"), "uptime");
      await user.click(screen.getByRole("button", { name: "执行" }));
      await user.type(screen.getByLabelText("输入 2 以确认选中节点数"), "2");
      await user.click(screen.getByRole("button", { name: "确认并验证" }));
    };

    try {
      await fillDraft();
      await waitFor(() => expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledTimes(1));
      expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledWith("auth-marker", {
        nodeIds: [1, 2],
        reason: "批量操作 2 个节点",
        requestedTtlSeconds: 600,
      }, undefined);
      expect(randomUUID).toHaveBeenCalledTimes(1);

      view.rerender(commandElement("next-token"));
      expect(screen.getByLabelText("命令")).toHaveValue("");
      expect(screen.getByLabelText("任务名称（可选）")).toHaveValue("");
      expect(screen.getByRole("checkbox", { name: /保留任务记录/ })).not.toBeChecked();
      const checked = screen.getAllByRole("checkbox").filter((element) => (element as HTMLInputElement).checked);
      expect(checked).toHaveLength(1);
      expect(checked[0]?.closest("label")).toHaveTextContent("node-a");
      expect(screen.queryByDisplayValue("uptime")).not.toBeInTheDocument();
      expect(screen.queryByDisplayValue("draft-name")).not.toBeInTheDocument();
      expect(screen.queryByText("执行前复核影响")).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "确认并验证" })).not.toBeInTheDocument();
      expect(screen.queryByLabelText("输入 2 以确认选中节点数")).not.toBeInTheDocument();
      expect(randomUUID).toHaveBeenCalledTimes(1);

      await act(async () => {
        pendingGrant.reject(createStepUpRequiredError());
      });

      expectNoBatchFollowUp();
      expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledTimes(1);
      expect(createBatchCommandMock.mock.calls.map((call) => call[6])).not.toContain(abandonedKey);

      await fillDraft();
      await waitFor(() => expect(createBatchCommandMock).toHaveBeenCalledTimes(1));
      expect(randomUUID).toHaveBeenCalledTimes(2);
      expect(createBatchCommandMock).toHaveBeenCalledWith(
        "next-token",
        [1, 2],
        "uptime",
        "draft-name",
        true,
        undefined,
        nextKey,
      );
      expect(nextKey).not.toBe(abandonedKey);
      expect(requestBatchCommandCredentialGrantMock).toHaveBeenLastCalledWith("next-token", {
        nodeIds: [1, 2],
        reason: "批量操作 2 个节点",
        requestedTtlSeconds: 600,
      }, undefined);
    } finally {
      randomUUID.mockRestore();
    }
  });

  it("does not publish callbacks when create resolves after a role downgrade", async () => {
    useRealStepUpRef.current = true;
    const user = userEvent.setup();
    const pendingCreate = createDeferred<{ batchId: string; retain: boolean }>();
    createBatchCommandMock.mockReturnValueOnce(pendingCreate.promise);
    const view = render(commandElement());

    await submitReviewedCommand(user);
    await waitFor(() => expect(createBatchCommandMock).toHaveBeenCalledTimes(1));

    authRef.current.role = "viewer";
    view.rerender(commandElement());
    await act(async () => {
      pendingCreate.resolve({ batchId: "batch-1", retain: false });
    });

    expect(onOpenChangeMock).not.toHaveBeenCalled();
    expect(onSuccessMock).not.toHaveBeenCalled();
    expect(ensureStepUpProofMock).not.toHaveBeenCalled();
    expect(document.getElementById("batch-command-error")).not.toBeInTheDocument();
  });

  it("lets the current operator complete grant step-up and create the batch", async () => {
    useRealStepUpRef.current = true;
    authRef.current = { role: "operator", token: "auth-marker" };
    const user = userEvent.setup();
    requestBatchCommandCredentialGrantMock
      .mockRejectedValueOnce(createStepUpRequiredError())
      .mockResolvedValueOnce([{ id: 11, status: "active" }]);
    render(commandElement());

    await submitReviewedCommand(user);

    await waitFor(() => expect(createBatchCommandMock).toHaveBeenCalledTimes(1));
    expect(ensureStepUpProofMock).toHaveBeenCalledTimes(1);
    expect(ensureStepUpProofMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.batchCommandCreate, oneShotStepUpOptions);
    expect(requestBatchCommandCredentialGrantMock).toHaveBeenNthCalledWith(1, "auth-marker", {
      nodeIds: [1],
      reason: "批量操作 1 个节点",
      requestedTtlSeconds: 600,
    }, undefined);
    expect(requestBatchCommandCredentialGrantMock).toHaveBeenNthCalledWith(2, "auth-marker", {
      nodeIds: [1],
      reason: "批量操作 1 个节点",
      requestedTtlSeconds: 600,
    }, "step-up-marker");
    expect(requestBatchCommandCredentialGrantMock.mock.invocationCallOrder[0])
      .toBeLessThan(ensureStepUpProofMock.mock.invocationCallOrder[0]);
    expect(ensureStepUpProofMock.mock.invocationCallOrder[0])
      .toBeLessThan(requestBatchCommandCredentialGrantMock.mock.invocationCallOrder[1]);
    expect(requestBatchCommandCredentialGrantMock.mock.invocationCallOrder[1])
      .toBeLessThan(createBatchCommandMock.mock.invocationCallOrder[0]);
    expect(createBatchCommandMock).toHaveBeenCalledWith(
      "auth-marker",
      [1],
      "uptime",
      undefined,
      false,
      "step-up-marker",
      expect.stringMatching(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i),
    );
    expect(clearStepUpProofMock).not.toHaveBeenCalled();
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);
    expect(onSuccessMock).toHaveBeenCalledWith({ batchId: "batch-1", retain: false });
  });

  it("drops a pending grant after a StrictMode token change before step-up", async () => {
    useRealStepUpRef.current = true;
    const user = userEvent.setup();
    const pendingGrant = createDeferred<unknown>();
    requestBatchCommandCredentialGrantMock.mockReturnValueOnce(pendingGrant.promise);
    const view = render(<StrictMode>{commandElement()}</StrictMode>);

    await submitReviewedCommand(user);
    await waitFor(() => expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledTimes(1));

    view.rerender(<StrictMode>{commandElement("next-token")}</StrictMode>);
    expect(screen.getByLabelText("命令")).toHaveValue("");
    expect(screen.queryByDisplayValue("uptime")).not.toBeInTheDocument();
    expect(screen.queryByText("执行前复核影响")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "确认并验证" })).not.toBeInTheDocument();
    await act(async () => {
      pendingGrant.reject(createStepUpRequiredError());
    });

    expectNoBatchFollowUp();
    expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledTimes(1);
  });
});
