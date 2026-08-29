import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { setLanguage } from "@/i18n";
import { runAxe } from "@/test/a11y-helpers";
import type { CatalogStatus, TaskRecord } from "@/types/domain";
import { TaskPreviewConnectDialog } from "./task-preview-connect-dialog";

const { apiClientMock } = vi.hoisted(() => ({
  apiClientMock: {
    connectBackupRepository: vi.fn(),
    getRecoveryPointCatalogStatus: vi.fn(),
  },
}));

vi.mock("@/lib/api/client", () => ({ apiClient: apiClientMock }));

const pointId = "2".repeat(32);
const task: TaskRecord = {
  id: 7,
  name: "nightly-rsync",
  policyName: "nightly-rsync",
  nodeId: 1,
  nodeName: "backup-node",
  status: "success",
  progress: 100,
  startedAt: "-",
  speedMbps: 0,
  enabled: true,
  executorType: "rsync",
  rsyncPublication: {
    mode: "legacy_mutable",
    state: "legacy",
    reasonCode: "legacy",
    capabilityRevision: 1,
    taskRevision: "1",
    seedFullCopyRequired: false,
  },
};

function mutationResult() {
  return {
    status: "available" as const,
    value: {
      repository: {},
      mutablePoint: { id: pointId },
    },
  };
}

function catalogStatus(overrides: Partial<CatalogStatus> = {}) {
  return {
    status: "available" as const,
    value: {
      generation: {
        id: "3".repeat(32),
        sequence: 1,
        state: "complete" as const,
        startedAt: "2026-08-29T00:00:00.000Z",
        finishedAt: "2026-08-29T00:00:01.000Z",
        errorCode: "" as const,
        correlationId: "",
      },
      latestBuild: null,
      coverage: {
        status: "complete" as const,
        indexedEntries: 0,
        expectedEntries: 0,
        manifestDigest: "",
        observedAt: "2026-08-29T00:00:01.000Z",
      },
      staleness: { status: "fresh" as const, observedAt: null, reason: null },
      contentAvailability: { available: true, reason: null },
      permissions: { list: true, preview: false, download: false },
      ...overrides,
    },
  };
}

describe("TaskPreviewConnectDialog", () => {
  beforeEach(() => {
    apiClientMock.connectBackupRepository.mockReset();
    apiClientMock.getRecoveryPointCatalogStatus.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("explains the read-only boundary, connects task-only, and opens the exact empty ready point", async () => {
    apiClientMock.connectBackupRepository.mockResolvedValue(mutationResult());
    apiClientMock.getRecoveryPointCatalogStatus.mockResolvedValue(catalogStatus());
    const user = userEvent.setup();

    render(
      <TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token" />,
    );

    expect(screen.getByText(/有界只读探测/)).toBeInTheDocument();
    expect(screen.getByText(/不会启用多版本，也不会修改或删除备份文件/)).toBeInTheDocument();
    expect(screen.getByText("确认后将开始只读探测。").closest("[role=status]"))
      .toHaveAttribute("aria-live", "polite");
    await user.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));

    await waitFor(() => {
      expect(apiClientMock.connectBackupRepository).toHaveBeenCalledWith(
        "token",
        { taskId: 7 },
        expect.any(AbortSignal),
      );
      expect(apiClientMock.getRecoveryPointCatalogStatus).toHaveBeenCalledWith(
        "token",
        pointId,
        expect.any(AbortSignal),
      );
    });
    expect(await screen.findByText("文件预览已就绪")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "打开文件预览" })).toHaveAttribute(
      "href",
      `/app/backups/data?taskId=7&recoveryPointId=${pointId}`,
    );
    await expect(runAxe(document.body)).resolves.toHaveNoViolations();
  });

  it.each([
    ["zh", "关闭"],
    ["en", "Close"],
  ] as const)("localizes both dialog close controls in %s", async (language, accessibleName) => {
    await setLanguage(language);
    render(<TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token" />);

    expect(screen.getAllByRole("button", { name: accessibleName })).toHaveLength(2);
    await setLanguage("zh");
  });

  it("fails closed for a blocked Connect result or a missing mutable point", async () => {
    const user = userEvent.setup();
    const { rerender } = render(
      <TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token" />,
    );
    apiClientMock.connectBackupRepository.mockResolvedValueOnce({
      status: "blocked",
      reason: { code: "unknown_internal_state", params: {} },
    });
    await user.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));
    expect(await screen.findByText("无法安全接入文件预览，请稍后重试或检查任务配置。")).toBeInTheDocument();
    expect(apiClientMock.getRecoveryPointCatalogStatus).not.toHaveBeenCalled();

    rerender(<TaskPreviewConnectDialog open={false} onOpenChange={vi.fn()} task={task} token="token" />);
    rerender(<TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token" />);
    apiClientMock.connectBackupRepository.mockResolvedValueOnce({
      ...mutationResult(),
      value: { ...mutationResult().value, mutablePoint: null },
    });
    await user.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));
    expect(await screen.findByText("接入结果未返回可验证的恢复点，请刷新任务后重试。")).toBeInTheDocument();
    expect(apiClientMock.getRecoveryPointCatalogStatus).not.toHaveBeenCalled();
  });

  it.each([
    ["failed", "目录索引失败，请稍后重试。"],
    ["partial", "目录索引不完整，请稍后重试。"],
    ["unavailable", "目录索引当前不可用，请稍后重试。"],
  ] as const)("uses a stable message for %s catalog coverage", async (coverage, message) => {
    apiClientMock.connectBackupRepository.mockResolvedValue(mutationResult());
    apiClientMock.getRecoveryPointCatalogStatus.mockResolvedValue(catalogStatus({
      generation: coverage === "failed" ? {
        ...catalogStatus().value.generation!,
        state: "failed",
        errorCode: "catalog_build_failed",
      } : null,
      coverage: { ...catalogStatus().value.coverage, status: coverage },
      contentAvailability: { available: false, reason: null },
    }));

    render(<TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token" />);
    await userEvent.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));

    expect(await screen.findByText(message)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/catalog_build_failed|provider|database/i);
  });

  it("uses a stable request failure notice without exposing backend details", async () => {
    apiClientMock.connectBackupRepository.mockRejectedValue(new Error("PRIVATE database connection failed"));

    render(<TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token" />);
    await userEvent.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));

    expect(await screen.findByText("接入文件预览失败，请稍后重试。")).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/PRIVATE|database connection failed/i);
  });

  it("times out in the foreground without disconnecting or claiming indexing failed", async () => {
    vi.useFakeTimers();
    apiClientMock.connectBackupRepository.mockResolvedValue(mutationResult());
    apiClientMock.getRecoveryPointCatalogStatus.mockResolvedValue(catalogStatus({
      generation: null,
      coverage: { ...catalogStatus().value.coverage, status: "building" },
      contentAvailability: { available: false, reason: null },
    }));

    render(<TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token" />);
    fireEvent.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));
    await vi.advanceTimersByTimeAsync(0);
    expect(apiClientMock.getRecoveryPointCatalogStatus).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(118_000);

    expect(screen.getByText("目录仍在后台构建，可稍后从任务再次打开。")).toBeInTheDocument();
    expect(screen.queryByText("目录索引失败，请稍后重试。")).not.toBeInTheDocument();
    expect(apiClientMock.getRecoveryPointCatalogStatus).toHaveBeenCalledTimes(60);
    expect(apiClientMock).not.toHaveProperty("disconnectBackupRepository");
  });

  it("uses a wall-clock deadline when a catalog request never settles", async () => {
    vi.useFakeTimers();
    let catalogSignal: AbortSignal | undefined;
    apiClientMock.connectBackupRepository.mockResolvedValue(mutationResult());
    apiClientMock.getRecoveryPointCatalogStatus.mockImplementation((...args: unknown[]) => {
      catalogSignal = args[2] as AbortSignal;
      return new Promise(() => undefined);
    });

    render(<TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token" />);
    fireEvent.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));
    await vi.advanceTimersByTimeAsync(0);
    expect(apiClientMock.getRecoveryPointCatalogStatus).toHaveBeenCalledTimes(1);
    expect(screen.getByText("接入成功，正在构建文件目录…")).toBeInTheDocument();

    await vi.advanceTimersByTimeAsync(119_999);
    expect(screen.getByText("接入成功，正在构建文件目录…")).toBeInTheDocument();
    await vi.advanceTimersByTimeAsync(1);

    expect(catalogSignal?.aborted).toBe(true);
    expect(screen.getByText("目录仍在后台构建，可稍后从任务再次打开。")).toBeInTheDocument();
    expect(screen.queryByText("接入文件预览失败，请稍后重试。")).not.toBeInTheDocument();
  });

  it.each(["task", "token"] as const)(
    "aborts an in-flight catalog request, clears its deadline, and ignores a late result when %s changes",
    async (changedValue) => {
      vi.useFakeTimers();
      let catalogSignal: AbortSignal | undefined;
      let resolveCatalog!: (value: ReturnType<typeof catalogStatus>) => void;
      apiClientMock.connectBackupRepository.mockResolvedValue(mutationResult());
      apiClientMock.getRecoveryPointCatalogStatus.mockImplementation((...args: unknown[]) => {
        catalogSignal = args[2] as AbortSignal;
        return new Promise((resolve) => {
          resolveCatalog = resolve;
        });
      });
      const view = render(
        <TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token-a" />,
      );
      fireEvent.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));
      await vi.advanceTimersByTimeAsync(0);
      expect(catalogSignal?.aborted).toBe(false);
      expect(vi.getTimerCount()).toBe(1);

      const nextTask = changedValue === "task"
        ? { ...task, id: 8, name: "next-rsync", policyName: "next-rsync" }
        : task;
      const nextToken = changedValue === "token" ? "token-b" : "token-a";
      view.rerender(
        <TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={nextTask} token={nextToken} />,
      );
      await vi.advanceTimersByTimeAsync(0);
      expect(catalogSignal?.aborted).toBe(true);
      expect(vi.getTimerCount()).toBe(0);

      resolveCatalog(catalogStatus());
      await vi.advanceTimersByTimeAsync(0);
      expect(screen.queryByText("文件预览已就绪")).not.toBeInTheDocument();
      expect(screen.getByText("确认后将开始只读探测。")).toBeInTheDocument();
    },
  );

  it("aborts the in-flight request when unmounted", async () => {
    let signal: AbortSignal | undefined;
    apiClientMock.connectBackupRepository.mockImplementation((...args: unknown[]) => {
      signal = args[2] as AbortSignal;
      return new Promise(() => undefined);
    });
    const user = userEvent.setup();
    const view = render(<TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token" />);
    await user.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));
    expect(signal?.aborted).toBe(false);

    view.unmount();

    expect(signal?.aborted).toBe(true);
  });

  it("aborts stale work when the task or token changes or the controlled dialog closes", async () => {
    const signals: AbortSignal[] = [];
    apiClientMock.connectBackupRepository.mockImplementation((...args: unknown[]) => {
      signals.push(args[2] as AbortSignal);
      return new Promise(() => undefined);
    });
    const user = userEvent.setup();
    const view = render(<TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token-a" />);
    await user.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));

    view.rerender(<TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={task} token="token-b" />);
    expect(signals[0].aborted).toBe(true);
    await user.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));

    const nextTask = { ...task, id: 8, name: "next-rsync", policyName: "next-rsync" };
    view.rerender(<TaskPreviewConnectDialog open onOpenChange={vi.fn()} task={nextTask} token="token-b" />);
    expect(signals[1].aborted).toBe(true);
    await user.click(screen.getByRole("button", { name: "接入或刷新文件预览" }));

    view.rerender(<TaskPreviewConnectDialog open={false} onOpenChange={vi.fn()} task={nextTask} token="token-b" />);
    expect(signals[2].aborted).toBe(true);
  });
});
