import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "@/lib/api/client";
import { BatchCommandDialog } from "./batch-command-dialog";
import type { NodeRecord } from "@/types/domain";

const { requestBatchCommandCredentialGrantMock, createBatchCommandMock, withStepUpMock, useStepUpActionMock, oneShotStepUpOptions, onOpenChangeMock, onSuccessMock } = vi.hoisted(() => {
  const withStepUpMock = vi.fn((action: (proof?: string) => Promise<unknown>) => action("step-up-marker"));
  const stepUpHookMock = vi.fn((options?: unknown) => {
    stepUpHookMock.lastOptions = options;
    return withStepUpMock;
  }) as ReturnType<typeof vi.fn> & { lastOptions?: unknown };

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
    useStepUpActionMock.lastOptions = undefined;
  });

  it("requests node-scoped grant before creating a batch command without storing command material", async () => {
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

    await waitFor(() => expect(requestBatchCommandCredentialGrantMock).toHaveBeenCalledTimes(1));
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
    );
    expect(requestBatchCommandCredentialGrantMock.mock.invocationCallOrder[0]).toBeLessThan(createBatchCommandMock.mock.invocationCallOrder[0]);
    expect(onOpenChangeMock).toHaveBeenCalledWith(false);
    expect(onSuccessMock).toHaveBeenCalledWith({ batchId: "batch-1", retain: false });

    const browserStorage = JSON.stringify({ ...localStorage, ...sessionStorage });
    expect(browserStorage).not.toContain("df -h");
    expect(browserStorage).not.toContain("step-up-marker");
    expect(browserStorage).not.toContain("active");
  });
});
