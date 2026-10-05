import "@testing-library/jest-dom/vitest";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { runAxe } from "@/test/a11y-helpers";
import type { TaskRecord } from "@/types/domain";
import { TaskRsyncVersioningDialog } from "./task-rsync-versioning-dialog";

const { apiClientMock, authRef } = vi.hoisted(() => ({
  authRef: {
    current: {
      token: "token" as string | null,
      role: "admin" as "admin" | "operator" | "viewer" | null,
    },
  },
  apiClientMock: {
    createRsyncVersioningPreflight: vi.fn(),
    activateRsyncVersioning: vi.fn(),
    prepareRsyncVersioningRollback: vi.fn(),
  },
}));

vi.mock("@/lib/api/client", () => ({ apiClient: apiClientMock }));
vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

const task: TaskRecord = {
  id: 7,
  name: "nightly-rsync",
  policyName: "nightly-rsync",
  nodeId: 1,
  nodeName: "backup-node",
  status: "pending",
  progress: 0,
  startedAt: "-",
  speedMbps: 0,
  enabled: true,
  executorType: "rsync",
  rsyncPublication: {
    mode: "legacy_mutable",
    state: "legacy",
    reasonCode: "legacy",
    capabilityRevision: 7,
    taskRevision: "9007199254740993",
    seedFullCopyRequired: false,
  },
};

describe("TaskRsyncVersioningDialog", () => {
  beforeEach(() => {
    authRef.current = { token: "token", role: "admin" };
    apiClientMock.createRsyncVersioningPreflight.mockReset();
    apiClientMock.activateRsyncVersioning.mockReset();
    apiClientMock.prepareRsyncVersioningRollback.mockReset();
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("requires a matching preflight and an explicit migration choice before activation", async () => {
    const user = userEvent.setup();
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    apiClientMock.createRsyncVersioningPreflight.mockResolvedValue({
      preflightId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      mode: "versioned_full_copy",
      state: "ready",
      reasonCode: "ready",
      capabilityRevision: 8,
      expiresAt: "2026-07-15T10:00:00Z",
      capacityEstimate: "available",
      inodeEstimate: "constrained",
    });
    apiClientMock.activateRsyncVersioning.mockResolvedValue({
      migrationChoice: "first_new_point",
      summary: {
        mode: "versioned_full_copy",
        state: "ready",
        reasonCode: "ready",
        capabilityRevision: 8,
        taskRevision: "9007199254740994",
        seedFullCopyRequired: false,
      },
    });

    render(
      <TaskRsyncVersioningDialog
        open
        onOpenChange={vi.fn()}
        task={task}
        token="token"
        onUpdated={onUpdated}
      />,
    );

    const activate = screen.getByRole("button", { name: "启用版本化" });
    expect(activate).toBeDisabled();

    await user.selectOptions(screen.getByLabelText("恢复点模式"), "versioned_full_copy");
    await user.click(screen.getByRole("button", { name: "运行预检" }));

    await waitFor(() => {
      expect(apiClientMock.createRsyncVersioningPreflight).toHaveBeenCalledWith("token", 7, {
        expectedTaskRevision: "9007199254740993",
        requestedMode: "versioned_full_copy",
      });
    });
    expect(activate).toBeDisabled();

    await user.click(screen.getByRole("radio", { name: "从下一次成功运行开始" }));
    expect(activate).toBeEnabled();
    await user.click(activate);

    await waitFor(() => {
      expect(apiClientMock.activateRsyncVersioning).toHaveBeenCalledWith("token", 7, {
        expectedTaskRevision: "9007199254740993",
        preflightId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        migrationChoice: "first_new_point",
      });
    });
    expect(onUpdated).toHaveBeenCalledTimes(1);
  });

  it("uses the activation response revision when preparing rollback", async () => {
    const user = userEvent.setup();
    apiClientMock.createRsyncVersioningPreflight.mockResolvedValue({
      preflightId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      mode: "versioned_full_copy",
      state: "ready",
      reasonCode: "ready",
      capabilityRevision: 8,
      expiresAt: "2026-07-15T10:00:00Z",
      capacityEstimate: "available",
      inodeEstimate: "available",
    });
    apiClientMock.activateRsyncVersioning.mockResolvedValue({
      migrationChoice: "first_new_point",
      summary: {
        mode: "versioned_full_copy",
        state: "ready",
        reasonCode: "ready",
        capabilityRevision: 8,
        taskRevision: "9007199254740994",
        seedFullCopyRequired: false,
      },
    });
    apiClientMock.prepareRsyncVersioningRollback.mockResolvedValue({
      summary: {
        mode: "versioned_full_copy",
        state: "rollback_prepared",
        reasonCode: "rollback_prepared",
        capabilityRevision: 8,
        taskRevision: "9007199254740995",
        seedFullCopyRequired: false,
      },
    });

    render(
      <TaskRsyncVersioningDialog
        open
        onOpenChange={vi.fn()}
        task={task}
        token="token"
        onUpdated={vi.fn().mockResolvedValue(undefined)}
      />,
    );

    await user.selectOptions(screen.getByLabelText("恢复点模式"), "versioned_full_copy");
    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await user.click(await screen.findByRole("radio", { name: "从下一次成功运行开始" }));
    await user.click(screen.getByRole("button", { name: "启用版本化" }));

    await user.click(await screen.findByRole("button", { name: "准备回退" }));

    await waitFor(() => {
      expect(apiClientMock.prepareRsyncVersioningRollback).toHaveBeenCalledWith("token", 7, {
        expectedTaskRevision: "9007199254740994",
      });
    });
  });

  it("keeps the dialog accessible while versioning is blocked", async () => {
    render(
      <TaskRsyncVersioningDialog
        open
        onOpenChange={vi.fn()}
        task={{
          ...task,
          rsyncPublication: {
            ...task.rsyncPublication!,
            state: "blocked",
            reasonCode: "unsupported",
          },
        }}
        token="token"
        onUpdated={vi.fn()}
      />,
    );

    expect(screen.getByText("当前状态受阻")).toBeInTheDocument();
    await expect(runAxe(document.body)).resolves.toHaveNoViolations();
  });

  function createDeferred<T>() {
    let resolve!: (value: T) => void;
    let reject!: (error: unknown) => void;
    const promise = new Promise<T>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    return { promise, resolve, reject };
  }

  function dialogElement(open: boolean, token: string, onUpdated: () => void) {
    return (
      <TaskRsyncVersioningDialog open={open} onOpenChange={vi.fn()} task={task} token={token} onUpdated={onUpdated} />
    );
  }

  const readyPreflight = {
    preflightId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    mode: "versioned_full_copy" as const,
    state: "ready" as const,
    reasonCode: "ready" as const,
    capabilityRevision: 8,
    expiresAt: "2026-07-15T10:00:00Z",
    capacityEstimate: "available" as const,
    inodeEstimate: "available" as const,
  };

  it("keeps activation working after StrictMode replay", async () => {
    const user = userEvent.setup();
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    apiClientMock.createRsyncVersioningPreflight.mockResolvedValue(readyPreflight);
    apiClientMock.activateRsyncVersioning.mockResolvedValue({
      migrationChoice: "first_new_point",
      summary: {
        mode: "versioned_full_copy",
        state: "ready",
        reasonCode: "ready",
        capabilityRevision: 8,
        taskRevision: "9007199254740994",
        seedFullCopyRequired: false,
      },
    });
    render(<StrictMode>{dialogElement(true, "token", onUpdated)}</StrictMode>);
    await user.selectOptions(screen.getByLabelText("恢复点模式"), "versioned_full_copy");
    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await user.click(await screen.findByRole("radio", { name: "从下一次成功运行开始" }));
    await user.click(screen.getByRole("button", { name: "启用版本化" }));
    await waitFor(() => expect(apiClientMock.activateRsyncVersioning).toHaveBeenCalledTimes(1));
    expect(onUpdated).toHaveBeenCalledTimes(1);
  });

  it("suppresses late preflight and activation updates after auth ABA, close, and unmount", async () => {
    const user = userEvent.setup();
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    const preflight = createDeferred<typeof readyPreflight>();
    apiClientMock.createRsyncVersioningPreflight.mockReturnValueOnce(preflight.promise);
    const view = render(<StrictMode>{dialogElement(true, "token", onUpdated)}</StrictMode>);
    await user.selectOptions(screen.getByLabelText("恢复点模式"), "versioned_full_copy");
    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => expect(apiClientMock.createRsyncVersioningPreflight).toHaveBeenCalledTimes(1));

    authRef.current = { token: "token-b", role: "operator" };
    view.rerender(<StrictMode>{dialogElement(true, "token-b", onUpdated)}</StrictMode>);
    authRef.current = { token: "token", role: "admin" };
    view.rerender(<StrictMode>{dialogElement(true, "token", onUpdated)}</StrictMode>);
    await act(async () => {
      preflight.resolve(readyPreflight);
    });
    expect(screen.queryByText("预检已就绪")).not.toBeInTheDocument();
    expect(screen.queryByText("版本化操作未完成，请刷新后重试。")).not.toBeInTheDocument();

    apiClientMock.createRsyncVersioningPreflight.mockResolvedValue(readyPreflight);
    await user.selectOptions(screen.getByLabelText("恢复点模式"), "versioned_full_copy");
    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await user.click(await screen.findByRole("radio", { name: "从下一次成功运行开始" }));
    const activation = createDeferred<unknown>();
    apiClientMock.activateRsyncVersioning.mockReturnValueOnce(activation.promise);
    await user.click(screen.getByRole("button", { name: "启用版本化" }));
    await waitFor(() => expect(apiClientMock.activateRsyncVersioning).toHaveBeenCalledTimes(1));
    view.rerender(<StrictMode>{dialogElement(false, "token", onUpdated)}</StrictMode>);
    view.rerender(<StrictMode>{dialogElement(true, "token", onUpdated)}</StrictMode>);
    await act(async () => {
      activation.resolve({
        migrationChoice: "first_new_point",
        summary: {
          mode: "versioned_full_copy",
          state: "ready",
          reasonCode: "ready",
          capabilityRevision: 8,
          taskRevision: "9007199254740994",
          seedFullCopyRequired: false,
        },
      });
    });
    expect(onUpdated).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "准备回退" })).not.toBeInTheDocument();
    expect(screen.getByText("传统可变目标")).toBeInTheDocument();

    const rejected = createDeferred<typeof readyPreflight>();
    const preflightCalls = apiClientMock.createRsyncVersioningPreflight.mock.calls.length;
    apiClientMock.createRsyncVersioningPreflight.mockReturnValueOnce(rejected.promise);
    await user.selectOptions(screen.getByLabelText("恢复点模式"), "versioned_full_copy");
    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => expect(apiClientMock.createRsyncVersioningPreflight).toHaveBeenCalledTimes(preflightCalls + 1));
    view.unmount();
    await act(async () => {
      rejected.reject(new Error("unmounted"));
    });
    expect(onUpdated).not.toHaveBeenCalled();
  });
});
