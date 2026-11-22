import "@testing-library/jest-dom/vitest";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { runAxe } from "@/test/a11y-helpers";
import i18n from "@/i18n";
import { ApiError } from "@/lib/api/core";
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

  function reasonError(status: number, code: string, params: Record<string, string> = {}) {
    return new ApiError(status, "rsync: ssh backup@10.0.0.5 /private/secret denied", {
      code: status,
      message: "rsync: ssh backup@10.0.0.5 /private/secret denied",
      data: { reason: { code, params } },
    });
  }

  const runPreflightName = i18n.t("rsyncVersioning.runPreflight");
  const activateName = i18n.t("rsyncVersioning.activate");
  const connectPreviewName = i18n.t("rsyncVersioning.connectPreview");
  const useFullCopyName = i18n.t("rsyncVersioning.useFullCopy");

  it.each([
    ["typed SSH source", 501, "local_source_required"],
    ["missing repository link", 404, "repository_connection_required"],
  ])("hands a %s off to file preview without restoring its own opener focus", async (_label, status, code) => {
    const user = userEvent.setup();
    const onConnectPreview = vi.fn().mockReturnValue(true);
    const onCloseAutoFocus = vi.fn();
    apiClientMock.createRsyncVersioningPreflight.mockRejectedValueOnce(reasonError(status, code));
    const dialog = (open: boolean) => (
      <TaskRsyncVersioningDialog
        open={open}
        onOpenChange={vi.fn()}
        task={task}
        token="token"
        onUpdated={vi.fn()}
        onCloseAutoFocus={onCloseAutoFocus}
        onConnectPreview={onConnectPreview}
      />
    );

    const view = render(dialog(true));
    await user.click(screen.getByRole("button", { name: runPreflightName }));

    expect(await screen.findByText(i18n.t(`rsyncVersioning.notice.${code}`))).toBeInTheDocument();
    expect(screen.queryByText(/private\/secret|10\.0\.0\.5/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: useFullCopyName })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: activateName })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: connectPreviewName }));
    expect(onConnectPreview).toHaveBeenCalledTimes(1);
    expect(onConnectPreview).toHaveBeenCalledWith();
    // The parent closes this dialog as preview opens; preview owns focus restoration from here.
    view.rerender(dialog(false));
    await act(() => new Promise((resolve) => setTimeout(resolve, 10)));
    expect(onCloseAutoFocus).not.toHaveBeenCalled();
  });

  it("hands close focus restoration to its opener callback outside a handoff", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    const onCloseAutoFocus = vi.fn();
    const dialog = (open: boolean) => (
      <TaskRsyncVersioningDialog
        open={open}
        onOpenChange={onOpenChange}
        task={task}
        token="token"
        onUpdated={vi.fn()}
        onCloseAutoFocus={onCloseAutoFocus}
      />
    );
    const view = render(dialog(true));

    await user.keyboard("{Escape}");
    expect(onOpenChange).toHaveBeenCalledWith(false);
    view.rerender(dialog(false));
    await waitFor(() => expect(onCloseAutoFocus).toHaveBeenCalledTimes(1));
  });

  it("withholds the preview handoff for remote targets, missing callbacks, and active runs", async () => {
    const user = userEvent.setup();
    const onConnectPreview = vi.fn().mockReturnValue(true);
    apiClientMock.createRsyncVersioningPreflight.mockRejectedValueOnce(reasonError(501, "local_target_required"));
    const view = render(
      <TaskRsyncVersioningDialog open onOpenChange={vi.fn()} task={task} token="token" onUpdated={vi.fn()} onConnectPreview={onConnectPreview} />,
    );
    await user.click(screen.getByRole("button", { name: runPreflightName }));
    expect(await screen.findByText(i18n.t("rsyncVersioning.notice.local_target_required"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: connectPreviewName })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: useFullCopyName })).not.toBeInTheDocument();
    view.unmount();

    apiClientMock.createRsyncVersioningPreflight.mockRejectedValueOnce(reasonError(501, "local_source_required"));
    const withoutHandoff = render(
      <TaskRsyncVersioningDialog open onOpenChange={vi.fn()} task={task} token="token" onUpdated={vi.fn()} />,
    );
    await user.click(screen.getByRole("button", { name: runPreflightName }));
    expect(await screen.findByText(i18n.t("rsyncVersioning.notice.local_source_required"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: connectPreviewName })).not.toBeInTheDocument();
    withoutHandoff.unmount();

    apiClientMock.createRsyncVersioningPreflight.mockRejectedValueOnce(reasonError(404, "repository_connection_required"));
    render(
      <TaskRsyncVersioningDialog
        open
        onOpenChange={vi.fn()}
        task={task}
        token="token"
        onUpdated={vi.fn()}
        onConnectPreview={onConnectPreview}
        connectPreviewDisabled
      />,
    );
    await user.click(screen.getByRole("button", { name: runPreflightName }));
    const blocked = await screen.findByRole("button", { name: connectPreviewName });
    expect(blocked).toHaveAttribute("aria-disabled", "true");
    expect(blocked).toHaveAccessibleDescription(i18n.t("tasks.previewConnectDisabledActive"));
    await user.click(blocked);
    expect(onConnectPreview).not.toHaveBeenCalled();
  });

  it("switches to full copy only by explicit choice and requires a fresh preflight", async () => {
    const user = userEvent.setup();
    apiClientMock.createRsyncVersioningPreflight
      .mockRejectedValueOnce(reasonError(409, "hardlink_unavailable"))
      .mockResolvedValueOnce(readyPreflight);
    render(
      <TaskRsyncVersioningDialog open onOpenChange={vi.fn()} task={task} token="token" onUpdated={vi.fn()} />,
    );
    const modeSelect = screen.getByLabelText(i18n.t("rsyncVersioning.modeLabel"));
    const runPreflight = screen.getByRole("button", { name: runPreflightName });
    const activate = screen.getByRole("button", { name: activateName });
    expect(modeSelect).toHaveValue("versioned_hardlink");

    await user.click(runPreflight);
    expect(await screen.findByText(i18n.t("rsyncVersioning.notice.hardlink_unavailable"))).toBeInTheDocument();
    expect(apiClientMock.createRsyncVersioningPreflight).toHaveBeenCalledTimes(1);
    expect(modeSelect).toHaveValue("versioned_hardlink");
    expect(activate).toBeDisabled();

    await user.click(screen.getByRole("button", { name: useFullCopyName }));
    expect(modeSelect).toHaveValue("versioned_full_copy");
    expect(runPreflight).toHaveFocus();
    expect(apiClientMock.createRsyncVersioningPreflight).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(i18n.t("rsyncVersioning.notice.hardlink_unavailable"))).not.toBeInTheDocument();
    expect(activate).toBeDisabled();

    await user.click(runPreflight);
    await waitFor(() => {
      expect(apiClientMock.createRsyncVersioningPreflight).toHaveBeenLastCalledWith("token", 7, {
        expectedTaskRevision: "9007199254740993",
        requestedMode: "versioned_full_copy",
      });
    });
    expect(activate).toBeDisabled();
    await user.click(await screen.findByRole("radio", { name: "从下一次成功运行开始" }));
    expect(activate).toBeEnabled();
  });

  it("never offers full copy or preview for generic, mismatched, or filesystem failures", async () => {
    const user = userEvent.setup();
    apiClientMock.createRsyncVersioningPreflight
      .mockRejectedValueOnce(new ApiError(409, "raw /private/secret conflict", { code: 409, data: null }))
      .mockRejectedValueOnce(reasonError(404, "hardlink_unavailable"))
      .mockRejectedValueOnce(reasonError(409, "hardlink_unavailable", { path: "/private/secret" }))
      .mockRejectedValueOnce(reasonError(501, "repository_connection_required"))
      .mockRejectedValueOnce(reasonError(409, "filesystem_preflight_failed"))
      .mockResolvedValueOnce({ ...readyPreflight, mode: "versioned_full_copy" as const });
    render(
      <TaskRsyncVersioningDialog
        open
        onOpenChange={vi.fn()}
        task={task}
        token="token"
        onUpdated={vi.fn()}
        onConnectPreview={vi.fn().mockReturnValue(true)}
      />,
    );
    const runPreflight = screen.getByRole("button", { name: runPreflightName });
    const activate = screen.getByRole("button", { name: activateName });
    const expectNoGuidanceActions = () => {
      expect(screen.queryByRole("button", { name: useFullCopyName })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: connectPreviewName })).not.toBeInTheDocument();
      expect(screen.queryByText(/private\/secret|10\.0\.0\.5/)).not.toBeInTheDocument();
      expect(activate).toBeDisabled();
    };

    for (const expected of ["conflict", "not_found", "conflict", "unsupported", "filesystem_preflight_failed"]) {
      await user.click(runPreflight);
      expect(await screen.findByText(i18n.t(`rsyncVersioning.notice.${expected}`))).toBeInTheDocument();
      expectNoGuidanceActions();
    }

    // A ready result for a different mode than requested is not a valid preflight.
    await user.click(runPreflight);
    await waitFor(() => expect(apiClientMock.createRsyncVersioningPreflight).toHaveBeenCalledTimes(6));
    expect(screen.queryByRole("radio", { name: "从下一次成功运行开始" })).not.toBeInTheDocument();
    expect(activate).toBeDisabled();
  });

  it("shows preflight preparation and moves focus to the next step after activation", async () => {
    const user = userEvent.setup();
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
    render(
      <TaskRsyncVersioningDialog open onOpenChange={vi.fn()} task={task} token="token" onUpdated={vi.fn().mockResolvedValue(undefined)} />,
    );
    expect(screen.getByText(i18n.t("rsyncVersioning.preflightPreparation"))).toBeInTheDocument();
    expect(screen.queryByText(i18n.t("rsyncVersioning.readyNextRun"))).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText(i18n.t("rsyncVersioning.modeLabel")), "versioned_full_copy");
    await user.click(screen.getByRole("button", { name: runPreflightName }));
    await user.click(await screen.findByRole("radio", { name: "从下一次成功运行开始" }));
    await user.click(screen.getByRole("button", { name: activateName }));

    const nextStep = await screen.findByText(i18n.t("rsyncVersioning.readyNextRun"));
    await waitFor(() => expect(nextStep.closest("[tabindex='-1']")).toHaveFocus());
    expect(screen.queryByText(i18n.t("rsyncVersioning.baselineNextStep"))).not.toBeInTheDocument();
    expect(screen.queryByText(i18n.t("rsyncVersioning.preflightPreparation"))).not.toBeInTheDocument();
  });

  it("points a committed baseline activation to browsing after indexing and focuses that notice", async () => {
    const user = userEvent.setup();
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    apiClientMock.createRsyncVersioningPreflight.mockResolvedValue(readyPreflight);
    apiClientMock.activateRsyncVersioning.mockResolvedValue({
      migrationChoice: "imported_baseline",
      summary: {
        mode: "versioned_full_copy",
        state: "committed",
        reasonCode: "ready",
        capabilityRevision: 8,
        taskRevision: "9007199254740994",
        seedFullCopyRequired: false,
      },
    });
    render(
      <TaskRsyncVersioningDialog open onOpenChange={vi.fn()} task={task} token="token" onUpdated={onUpdated} />,
    );

    await user.selectOptions(screen.getByLabelText(i18n.t("rsyncVersioning.modeLabel")), "versioned_full_copy");
    await user.click(screen.getByRole("button", { name: runPreflightName }));
    await user.click(await screen.findByRole("radio", { name: i18n.t("rsyncVersioning.choiceImportedBaseline") }));
    await user.click(screen.getByRole("button", { name: activateName }));

    await waitFor(() => {
      expect(apiClientMock.activateRsyncVersioning).toHaveBeenCalledWith("token", 7, {
        expectedTaskRevision: "9007199254740993",
        preflightId: readyPreflight.preflightId,
        migrationChoice: "imported_baseline",
      });
    });
    const nextStep = await screen.findByText(i18n.t("rsyncVersioning.baselineNextStep"));
    await waitFor(() => expect(nextStep.closest("[tabindex='-1']")).toHaveFocus());
    expect(screen.queryByText(i18n.t("rsyncVersioning.readyNextRun"))).not.toBeInTheDocument();
    expect(onUpdated).toHaveBeenCalledTimes(1);
    // Rollback preparation stays available and retires the activation guidance.
    expect(screen.getByRole("button", { name: i18n.t("rsyncVersioning.prepareRollback") })).toBeEnabled();
  });

  it("leaves an existing committed baseline without an activation next step", () => {
    render(
      <TaskRsyncVersioningDialog
        open
        onOpenChange={vi.fn()}
        task={{
          ...task,
          rsyncPublication: { ...task.rsyncPublication!, mode: "versioned_full_copy", state: "committed", reasonCode: "ready" },
        }}
        token="token"
        onUpdated={vi.fn()}
      />,
    );
    expect(screen.getByText(i18n.t("rsyncVersioning.currentState", {
      state: i18n.t("rsyncVersioning.state.committed"),
    }))).toBeInTheDocument();
    expect(screen.queryByText(i18n.t("rsyncVersioning.readyNextRun"))).not.toBeInTheDocument();
    expect(screen.queryByText(i18n.t("rsyncVersioning.baselineNextStep"))).not.toBeInTheDocument();
  });
});
