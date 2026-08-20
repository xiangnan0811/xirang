import "@testing-library/jest-dom/vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { runAxe } from "@/test/a11y-helpers";

import { createInitialBackupAssetsState } from "./backup-assets-state";
import { defaultBackupAssetsRouteState } from "./backup-assets-route-state";
import { BackupAssetsWorkspace } from "./backup-assets-workspace";
import type { BackupAssetsController } from "./use-backup-assets-state";
import { buildAssetRows, recoveryPoint, repository } from "./__tests__/test-utils";

const { coveragePanelRenderMock } = vi.hoisted(() => ({
  coveragePanelRenderMock: vi.fn(),
}));
const { exportPanelRenderMock, exportPanelGate } = vi.hoisted(() => {
  let pending: Promise<void> | null = null;
  let resolvePending: (() => void) | null = null;

  return {
    exportPanelRenderMock: vi.fn(),
    exportPanelGate: {
      arm() {
        pending = new Promise<void>((resolve) => {
          resolvePending = resolve;
        });
      },
      read() {
        return pending;
      },
      release() {
        const resolve = resolvePending;
        pending = null;
        resolvePending = null;
        resolve?.();
      },
      reset() {
        const resolve = resolvePending;
        pending = null;
        resolvePending = null;
        resolve?.();
      },
    },
  };
});
const { archivePanelRenderMock } = vi.hoisted(() => ({
  archivePanelRenderMock: vi.fn(),
}));
const { recoveryWizardRenderMock } = vi.hoisted(() => ({
  recoveryWizardRenderMock: vi.fn(),
}));

vi.mock("./processing-coverage-panel", () => ({
  ProcessingCoveragePanel: (props: unknown) => {
    coveragePanelRenderMock(props);
    return <section data-testid="synthetic-coverage-panel">Synthetic coverage panel</section>;
  },
}));

vi.mock("./export-job-panel", () => ({
  ExportJobPanel: (props: unknown) => {
    const pending = exportPanelGate.read();
    if (pending) throw pending;
    exportPanelRenderMock(props);
    return <section data-testid="synthetic-export-panel">Synthetic export panel</section>;
  },
}));

vi.mock("./backup-asset-processing-panel", () => ({
  BackupAssetProcessingPanel: ({ onBrowseArchive }: { onBrowseArchive?: () => void }) => (
    <button type="button" onClick={onBrowseArchive}>Open archive browser</button>
  ),
}));

vi.mock("./archive-member-panel", () => ({
  ArchiveMemberPanel: (props: { online?: boolean }) => {
    archivePanelRenderMock(props);
    return <section data-testid="synthetic-archive-panel">Synthetic archive panel</section>;
  },
}));

vi.mock("./recovery-plan-wizard", () => ({
  RecoveryPlanWizard: (props: unknown) => {
    recoveryWizardRenderMock(props);
    return <section data-testid="synthetic-recovery-wizard">Synthetic recovery wizard</section>;
  },
}));

function controller(overrides: Partial<BackupAssetsController> = {}): BackupAssetsController {
  const route = { ...defaultBackupAssetsRouteState("data"), repositoryId: repository.id };
  return {
    state: createInitialBackupAssetsState(route),
    repositories: {
      status: "ready",
      items: [{ status: "available", value: repository }],
      nextCursor: null,
    },
    recoveryPoints: { status: "idle", items: [], nextCursor: null },
    selectedRecoveryPoint: null,
    selectedEntry: { status: "idle", value: null },
    evidence: { status: "idle", value: null },
    diff: { status: "idle", value: null },
    content: { status: "idle", value: null },
    overlays: {
      savedSearches: { status: "idle", items: [], nextCursor: null },
      favorites: { status: "idle", items: [], nextCursor: null },
      tags: { status: "idle", items: [], nextCursor: null },
      recent: { status: "idle", items: [], nextCursor: null },
    },
    semanticIssue: null,
    filterIssue: null,
    actions: {
      refreshRepositories: vi.fn(),
      refreshRecoveryPoints: vi.fn(),
      refreshResults: vi.fn(),
      setSearchDraft: vi.fn(),
      executeSearch: vi.fn(),
      toggleSelection: vi.fn(),
      clearSelection: vi.fn(),
      loadMore: vi.fn(),
      loadOverlaySection: vi.fn(),
      toggleFavorite: vi.fn(),
      createSavedSearch: vi.fn(),
      updateSavedSearch: vi.fn(),
      deleteSavedSearch: vi.fn(),
      createTag: vi.fn(),
      updateTag: vi.fn(),
      deleteTag: vi.fn(),
      assignTag: vi.fn(),
      clearRecent: vi.fn(),
      compareRecoveryPoints: vi.fn(),
      loadPreview: vi.fn(),
      renewPreview: vi.fn(),
      prepareDownload: vi.fn(),
      detachContent: vi.fn(),
    },
    ...overrides,
  };
}

function setViewport(width: number) {
  Object.defineProperty(window, "innerWidth", { configurable: true, value: width });
  fireEvent(window, new Event("resize"));
}

beforeAll(() => {
  Object.defineProperties(HTMLElement.prototype, {
    getBoundingClientRect: {
      configurable: true,
      value: () => ({
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: 800,
        bottom: 480,
        width: 800,
        height: 480,
        toJSON: () => ({}),
      }) as DOMRect,
    },
    offsetHeight: { configurable: true, get: () => 480 },
    offsetWidth: { configurable: true, get: () => 800 },
    clientHeight: { configurable: true, get: () => 480 },
    clientWidth: { configurable: true, get: () => 800 },
  });
});

afterEach(() => {
  exportPanelGate.reset();
  Object.defineProperty(navigator, "onLine", { configurable: true, value: true });
});

describe("BackupAssetsWorkspace", () => {
  it("freezes explicit recovery refs in memory before opening the lazy wizard", async () => {
    setViewport(1440);
    recoveryWizardRenderMock.mockClear();
    const user = userEvent.setup();
    const rows = buildAssetRows(2);
    const selectedPoint = {
      ...recoveryPoint,
      catalog: recoveryPoint.catalog.status === "available" ? {
        status: "available" as const,
        value: {
          ...recoveryPoint.catalog.value,
          generation: {
            id: "c".repeat(32), sequence: 4, state: "complete" as const,
            startedAt: "2026-08-16T12:00:00Z", finishedAt: "2026-08-16T12:01:00Z",
            errorCode: "" as const, correlationId: "recovery-generation",
          },
        },
      } : recoveryPoint.catalog,
    };
    const state = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: selectedPoint.id,
    });
    state.result = { status: "ready", requestKey: "recovery", generation: 1, rows, nextCursor: null, coverage: "complete", authoritativeEmpty: false };
    state.selection = new Map(rows.map((row) => [`${row.ref.recoveryPointId}:${row.ref.entryId}`, row.ref]));
    render(
      <BackupAssetsWorkspace
        controller={controller({ state, selectedRecoveryPoint: selectedPoint })}
        processingRuntime={{ token: "admin-token", role: "admin", userId: 17, ensureStepUpProof: vi.fn() }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: /Recover selected|恢复所选/ }));
    expect(await screen.findByTestId("synthetic-recovery-wizard")).toBeInTheDocument();
    expect(recoveryWizardRenderMock).toHaveBeenCalledWith(expect.objectContaining({
      open: true,
      recovery: expect.objectContaining({
        state: expect.objectContaining({ selection: rows.map((row) => row.ref) }),
      }),
    }));
    expect(window.location.search).not.toMatch(/entry|selection|secret|proof|reason/i);
  });

  it("recovers one inspected item without requiring it to be bulk-selected", async () => {
    setViewport(1440);
    recoveryWizardRenderMock.mockClear();
    const user = userEvent.setup();
    const row = buildAssetRows(1)[0];
    const selectedPoint = {
      ...recoveryPoint,
      catalog: recoveryPoint.catalog.status === "available" ? {
        status: "available" as const,
        value: {
          ...recoveryPoint.catalog.value,
          generation: {
            id: "c".repeat(32), sequence: 4, state: "complete" as const,
            startedAt: "2026-08-16T12:00:00Z", finishedAt: "2026-08-16T12:01:00Z",
            errorCode: "" as const, correlationId: "recovery-generation",
          },
        },
      } : recoveryPoint.catalog,
    };
    const state = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: selectedPoint.id,
      entryId: row.ref.entryId,
    });
    state.result = { status: "ready", requestKey: "inspected-recovery", generation: 1, rows: [row], nextCursor: null, coverage: "complete", authoritativeEmpty: false };
    render(
      <BackupAssetsWorkspace
        controller={controller({ state, selectedRecoveryPoint: selectedPoint, selectedEntry: { status: "ready", value: row.asset } })}
        processingRuntime={{ token: "admin-token", role: "admin", userId: 17, ensureStepUpProof: vi.fn() }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: /Recover this item|恢复此项/ }));
    expect(await screen.findByTestId("synthetic-recovery-wizard")).toBeInTheDocument();
    expect(recoveryWizardRenderMock).toHaveBeenCalledWith(expect.objectContaining({
      recovery: expect.objectContaining({ state: expect.objectContaining({ selection: [row.ref] }) }),
    }));
  });

  it("opens the lazy export dialog only from an Admin explicit selection", async () => {
    setViewport(1440);
    exportPanelRenderMock.mockClear();
    const user = userEvent.setup();
    const rows = buildAssetRows(1);
    const state = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
    });
    state.result = { status: "ready", requestKey: "test", generation: 1, rows, nextCursor: null, coverage: "complete", authoritativeEmpty: false };
    state.selection = new Map([[`${rows[0].ref.recoveryPointId}:${rows[0].ref.entryId}`, rows[0].ref]]);
    const onRoutePatch = vi.fn();
    render(
      <BackupAssetsWorkspace
        controller={controller({ state, selectedRecoveryPoint: recoveryPoint })}
        processingRuntime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        onRoutePatch={onRoutePatch}
        onReturnOverview={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: /导出所选|Export selected/ }));
    expect(await screen.findByRole("dialog", { name: /导出备份资产|Export backup assets/ })).toBeInTheDocument();
    expect(await screen.findByTestId("synthetic-export-panel")).toBeInTheDocument();
    expect(exportPanelRenderMock).toHaveBeenCalledWith(expect.objectContaining({
      open: true,
      exportJobId: undefined,
      selection: [{ ref: rows[0].ref, logicalBytes: rows[0].asset.size }],
    }));
    expect(onRoutePatch).not.toHaveBeenCalledWith(expect.objectContaining({ selection: expect.anything() }));
  });

  it("freezes explicit export refs before the lazy panel resolves", async () => {
    setViewport(1440);
    exportPanelRenderMock.mockClear();
    exportPanelGate.arm();
    const user = userEvent.setup();
    const rows = buildAssetRows(2);
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
    };
    const initialState = createInitialBackupAssetsState(route);
    initialState.result = {
      status: "ready",
      requestKey: "initial-export-selection",
      generation: 1,
      rows,
      nextCursor: null,
      coverage: "complete",
      authoritativeEmpty: false,
    };
    initialState.selection = new Map([[`${rows[0].ref.recoveryPointId}:${rows[0].ref.entryId}`, rows[0].ref]]);
    const rendered = render(
      <BackupAssetsWorkspace
        controller={controller({ state: initialState, selectedRecoveryPoint: recoveryPoint })}
        processingRuntime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: /导出所选|Export selected/ }));
    expect(await screen.findByRole("dialog", { name: /导出备份资产|Export backup assets/ })).toBeInTheDocument();
    expect(exportPanelRenderMock).not.toHaveBeenCalled();

    const replacementState = createInitialBackupAssetsState({
      ...route,
      parentEntryId: rows[1].ref.entryId,
    });
    replacementState.result = {
      status: "ready",
      requestKey: "replaced-export-selection",
      generation: 2,
      rows: [rows[1]],
      nextCursor: null,
      coverage: "complete",
      authoritativeEmpty: false,
    };
    replacementState.selection = new Map([[`${rows[1].ref.recoveryPointId}:${rows[1].ref.entryId}`, rows[1].ref]]);
    rendered.rerender(
      <BackupAssetsWorkspace
        controller={controller({ state: replacementState, selectedRecoveryPoint: recoveryPoint })}
        processingRuntime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />,
    );

    await act(async () => {
      exportPanelGate.release();
      await Promise.resolve();
    });

    await waitFor(() => expect(exportPanelRenderMock).toHaveBeenCalledWith(expect.objectContaining({
      open: true,
      selection: [{ ref: rows[0].ref, logicalBytes: rows[0].asset.size }],
    })));
  });

  it.each(["operator", "viewer"] as const)("does not expose bulk export to the %s role", (role) => {
    setViewport(1440);
    const rows = buildAssetRows(1);
    const state = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
    });
    state.result = { status: "ready", requestKey: "role-matrix", generation: 1, rows, nextCursor: null, coverage: "complete", authoritativeEmpty: false };
    state.selection = new Map([[`${rows[0].ref.recoveryPointId}:${rows[0].ref.entryId}`, rows[0].ref]]);
    render(
      <BackupAssetsWorkspace
        controller={controller({ state, selectedRecoveryPoint: recoveryPoint })}
        processingRuntime={{ token: `${role}-token`, role, ensureStepUpProof: vi.fn() }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />,
    );

    expect(screen.queryByRole("button", { name: /Export selected|导出所选/ })).not.toBeInTheDocument();
  });

  it("closes a direct-route export on context reset and focuses the results fallback", async () => {
    setViewport(1440);
    const exportJobId = "d".repeat(32);
    const directRoute = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      exportJobId,
    };
    const directState = createInitialBackupAssetsState(directRoute);
    const runtime = { token: "admin-token", role: "admin" as const, ensureStepUpProof: vi.fn() };
    const rendered = render(
      <BackupAssetsWorkspace
        controller={controller({ state: directState })}
        processingRuntime={runtime}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />,
    );

    expect(await screen.findByRole("dialog", { name: /导出备份资产|Export backup assets/ })).toBeInTheDocument();

    rendered.rerender(
      <BackupAssetsWorkspace
        controller={controller({ state: createInitialBackupAssetsState({ ...directRoute, exportJobId: undefined }) })}
        processingRuntime={runtime}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />,
    );

    await waitFor(() => expect(screen.queryByRole("dialog", { name: /导出备份资产|Export backup assets/ })).not.toBeInTheDocument());
    expect(screen.getByRole("region", { name: /Asset results|资产结果/ })).toHaveFocus();
  });

  it("loads the Admin processing surface only after an authorized interaction", async () => {
    setViewport(1440);
    coveragePanelRenderMock.mockClear();
    const user = userEvent.setup();
    const ensureStepUpProof = vi.fn();
    render(
      <BackupAssetsWorkspace
        controller={controller()}
        processingRuntime={{ token: "admin-token", role: "admin", ensureStepUpProof }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />
    );

    const trigger = screen.getByRole("button", { name: /Processing coverage|处理覆盖/ });
    expect(screen.queryByTestId("synthetic-coverage-panel")).not.toBeInTheDocument();
    expect(coveragePanelRenderMock).not.toHaveBeenCalled();
    await user.click(trigger);
    expect(await screen.findByRole("dialog", { name: /Processing coverage|处理覆盖/ })).toBeInTheDocument();
    expect(await screen.findByTestId("synthetic-coverage-panel")).toBeInTheDocument();
    expect(coveragePanelRenderMock).toHaveBeenCalledWith(expect.objectContaining({
      token: "admin-token",
      role: "admin",
      ensureStepUpProof,
    }));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("keeps the lazy Admin processing dialog axe-clean", async () => {
    setViewport(1440);
    const user = userEvent.setup();
    render(
      <BackupAssetsWorkspace
        controller={controller()}
        processingRuntime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />
    );

    await user.click(screen.getByRole("button", { name: /Processing coverage|处理覆盖/ }));
    expect(await screen.findByRole("dialog", { name: /Processing coverage|处理覆盖/ })).toBeInTheDocument();
    expect(await runAxe(document.body)).toHaveNoViolations();
  });

  it("does not expose the processing administration trigger to operators", () => {
    setViewport(1440);
    render(
      <BackupAssetsWorkspace
        controller={controller()}
        processingRuntime={{ token: "operator-token", role: "operator", ensureStepUpProof: vi.fn() }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />
    );

    expect(screen.queryByRole("button", { name: /Processing coverage|处理覆盖/ })).not.toBeInTheDocument();
  });

  it("renders stable unframed three-track desktop regions", () => {
    setViewport(1440);
    const { container } = render(
      <BackupAssetsWorkspace controller={controller()} onRoutePatch={vi.fn()} onReturnOverview={vi.fn()} />
    );

    expect(screen.getByTestId("backup-assets-workspace")).toHaveAttribute("data-viewport", "desktop");
    expect(screen.getByRole("complementary", { name: /Asset context|资产上下文/ })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: /Asset results|资产结果/ })).toBeInTheDocument();
    expect(screen.getByRole("complementary", { name: /Asset inspector|资产检查器/ })).toBeInTheDocument();
    expect(screen.getByTestId("backup-assets-workspace")).toHaveStyle({
      gridTemplateColumns: "minmax(224px, 288px) minmax(420px, 1fr) minmax(300px, 416px)",
    });
    expect(container.querySelectorAll('[data-component="data-surface"], [data-component="card"]')).toHaveLength(0);
  });

  it("applies validated desktop panel-width preferences to stable tracks", () => {
    setViewport(1440);

    render(
      <BackupAssetsWorkspace
        controller={controller()}
        preferences={{ version: 1, layout: "list", contextWidth: 320, inspectorWidth: 480 }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />
    );

    expect(screen.getByTestId("backup-assets-workspace")).toHaveStyle({
      gridTemplateColumns: "minmax(224px, 320px) minmax(420px, 1fr) minmax(300px, 480px)",
    });
  });

  it("uses the sequential layout before the AppShell can contain all desktop track minima", () => {
    setViewport(1200);

    render(
      <BackupAssetsWorkspace controller={controller()} onRoutePatch={vi.fn()} onReturnOverview={vi.fn()} />
    );

    expect(screen.getByTestId("backup-assets-workspace")).toHaveAttribute(
      "data-viewport",
      "intermediate"
    );
    expect(screen.getByRole("button", { name: /Open asset context|打开资产上下文/ })).toBeInTheDocument();
    expect(
      screen.queryByRole("complementary", { name: /Asset inspector|资产检查器/ })
    ).not.toBeInTheDocument();
  });

  it("bounds the workspace to the available viewport above mobile navigation", () => {
    setViewport(1440);
    const desktop = render(
      <BackupAssetsWorkspace controller={controller()} onRoutePatch={vi.fn()} onReturnOverview={vi.fn()} />
    );
    expect(screen.getByTestId("backup-assets-workspace")).toHaveStyle({
      height: "calc(100dvh - 14.25rem)",
    });
    desktop.unmount();

    setViewport(375);
    render(
      <BackupAssetsWorkspace controller={controller()} onRoutePatch={vi.fn()} onReturnOverview={vi.fn()} />
    );
    expect(screen.getByTestId("backup-assets-workspace")).toHaveStyle({
      height: "calc(100dvh - 20.5rem)",
    });
    expect(screen.getByTestId("backup-assets-workspace")).toHaveClass("min-h-[20rem]");
  });

  it("renders the repositories view as read-only server facts with a permission-backed browse command", async () => {
    setViewport(1440);
    const user = userEvent.setup();
    const onRoutePatch = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      view: "repositories" as const,
      repositoryId: repository.id,
    };
    const state = createInitialBackupAssetsState(route);

    render(
      <BackupAssetsWorkspace
        controller={controller({ state })}
        onRoutePatch={onRoutePatch}
        onReturnOverview={vi.fn()}
      />
    );

    const management = screen.getByRole("region", {
      name: /Repository management|仓库管理/,
    });
    expect(management).toHaveTextContent(repository.displayName);
    expect(management).toHaveTextContent(/Restic/);
    expect(management).toHaveTextContent(/Native snapshot|原生快照/);
    expect(management).toHaveTextContent(/Backend versioned|后端版本化/);
    expect(management).toHaveTextContent(/Catalog coverage|目录覆盖/);
    expect(management).toHaveTextContent(/Capabilities|能力/);
    expect(management).toHaveTextContent(/Permissions|权限/);
    expect(within(management).queryByRole("button", { name: /reconnect|重连|purge|清除/i })).not.toBeInTheDocument();

    await user.click(
      within(management).getByRole("button", {
        name: /Browse .*Synthetic Primary Repository|浏览.*Synthetic Primary Repository/,
      })
    );
    expect(onRoutePatch).toHaveBeenCalledWith({
      view: "browse",
      repositoryId: repository.id,
    });
  });

  it("shows Admin lifecycle actions on the repositories view and keeps operators read-only", async () => {
    setViewport(1440);
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      view: "repositories" as const,
      repositoryId: repository.id,
    };
    const state = createInitialBackupAssetsState(route);
    const { rerender } = render(
      <BackupAssetsWorkspace
        controller={controller({
          state,
          recoveryPoints: {
            status: "ready",
            items: [{ status: "available", value: recoveryPoint }],
            nextCursor: null,
          },
        })}
        processingRuntime={{ token: "admin-token", role: "admin", userId: 7, ensureStepUpProof: vi.fn() }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />,
    );

    const management = screen.getByRole("region", { name: /Repository management|仓库管理/ });
    expect(within(management).getByRole("button", { name: /Reconnect|重连/ })).toBeInTheDocument();
    expect(within(management).getByRole("button", { name: /Reconcile|重新探测/ })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: /Retention policies|保留策略/ })).toBeInTheDocument();
    expect(screen.getByLabelText(/Hold recovery point|冻结恢复点/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Create hold|创建冻结/ })).toBeInTheDocument();

    rerender(
      <BackupAssetsWorkspace
        controller={controller({ state })}
        processingRuntime={{ token: "operator-token", role: "operator", ensureStepUpProof: vi.fn() }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />,
    );
    expect(within(screen.getByRole("region", { name: /Repository management|仓库管理/ }))
      .queryByRole("button", { name: /Reconnect|重连/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: /Retention policies|保留策略/ })).not.toBeInTheDocument();
  });

  it("uses a context dialog at intermediate width and returns focus to its trigger", async () => {
    setViewport(768);
    const user = userEvent.setup();
    render(<BackupAssetsWorkspace controller={controller()} onRoutePatch={vi.fn()} onReturnOverview={vi.fn()} />);

    expect(screen.getByTestId("backup-assets-workspace")).toHaveAttribute("data-viewport", "intermediate");
    const trigger = screen.getByRole("button", { name: /Open asset context|打开资产上下文/ });
    await user.click(trigger);
    expect(screen.getByRole("dialog", { name: /Asset context|资产上下文/ })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(trigger).toHaveFocus();
    expect(screen.queryByRole("complementary", { name: /Asset inspector|资产检查器/ })).not.toBeInTheDocument();
  });

  it("renders a closed feature-disabled region without asset probes or fake data", () => {
    setViewport(1440);
    const onReturnOverview = vi.fn();
    render(
      <BackupAssetsWorkspace
        controller={controller({
          repositories: {
            status: "blocked",
            items: [],
            nextCursor: null,
            error: {
              code: "feature_disabled",
              translationKey: "backupAssets.errors.featureDisabled",
              retryable: false,
              action: "return_overview",
            },
          },
        })}
        onRoutePatch={vi.fn()}
        onReturnOverview={onReturnOverview}
      />
    );

    expect(screen.getByRole("alert")).toHaveTextContent(/not enabled|未启用/);
    expect(screen.queryByRole("region", { name: /Asset results|资产结果/ })).not.toBeInTheDocument();
    expect(screen.queryByText(repository.displayName)).not.toBeInTheDocument();
  });

  it("renders a repository load error instead of an empty management panel", () => {
    setViewport(1440);
    render(
      <BackupAssetsWorkspace
        controller={controller({
          repositories: {
            status: "error",
            items: [],
            nextCursor: null,
            error: {
              code: "unknown",
              translationKey: "backupAssets.errors.unknown",
              retryable: true,
              action: "retry",
            },
          },
        })}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(/could not be loaded|无法加载/);
    expect(screen.queryByRole("region", { name: /Repository management|仓库管理/ })).not.toBeInTheDocument();
  });

  it("renders an exact recovery-point blocked state without mounting asset controls", async () => {
    setViewport(1440);
    const user = userEvent.setup();
    const onRoutePatch = vi.fn();
    const expiredRecoveryPoint = { ...recoveryPoint, state: "expired" as const };
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: expiredRecoveryPoint.id,
    };

    render(
      <BackupAssetsWorkspace
        controller={controller({
          state: createInitialBackupAssetsState(route),
          selectedRecoveryPoint: expiredRecoveryPoint,
          semanticIssue: {
            reason: "recovery_point_expired",
            translationKey: "backupAssets.errors.recoveryPointExpired",
            patch: { parentEntryId: undefined, entryId: undefined },
          },
        })}
        onRoutePatch={onRoutePatch}
        onReturnOverview={vi.fn()}
      />
    );

    const results = screen.getByRole("region", { name: /Asset results|资产结果/ });
    expect(within(results).getByRole("alert")).toHaveTextContent(/expired|已过期/i);
    expect(results).toHaveTextContent(expiredRecoveryPoint.producingTaskName);
    expect(screen.queryByRole("searchbox", { name: /Search backup assets|搜索备份资产/ })).not.toBeInTheDocument();

    await user.click(
      within(results).getByRole("button", { name: /Return to repository context|返回仓库上下文/ })
    );
    expect(onRoutePatch).toHaveBeenCalledWith({
      recoveryPointId: undefined,
      parentEntryId: undefined,
      entryId: undefined,
    });
  });

  it("blocks an unavailable route filter without showing unfiltered results", async () => {
    setViewport(1440);
    const user = userEvent.setup();
    const onRoutePatch = vi.fn();
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      view: "search" as const,
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      favoriteOnly: true,
      sort: "relevance" as const,
      direction: "desc" as const,
    };

    render(
      <BackupAssetsWorkspace
        controller={controller({
          state: createInitialBackupAssetsState(route),
          selectedRecoveryPoint: recoveryPoint,
          filterIssue: {
            reason: "favorite_filter_unavailable",
            translationKey: "backupAssets.errors.favoriteFilterUnavailable",
            patch: { favoriteOnly: false },
          },
        })}
        onRoutePatch={onRoutePatch}
        onReturnOverview={vi.fn()}
      />
    );

    const results = screen.getByRole("region", { name: /Asset results|资产结果/ });
    expect(within(results).getByRole("alert")).toHaveTextContent(/favorite filter|收藏筛选/i);
    expect(within(results).queryByRole("searchbox")).not.toBeInTheDocument();
    await user.click(
      within(results).getByRole("button", { name: /Clear unavailable filter|清除不可用筛选/ })
    );
    expect(onRoutePatch).toHaveBeenCalledWith({ favoriteOnly: false });
  });

  it("composes the browser with controller-owned temporary search state", async () => {
    setViewport(1440);
    const user = userEvent.setup();
    const setSearchDraft = vi.fn();
    render(
      <BackupAssetsWorkspace
        controller={controller({
          actions: {
            refreshRepositories: vi.fn(),
            refreshRecoveryPoints: vi.fn(),
            refreshResults: vi.fn(),
            setSearchDraft,
            executeSearch: vi.fn(),
            toggleSelection: vi.fn(),
            clearSelection: vi.fn(),
            loadMore: vi.fn(),
            loadOverlaySection: vi.fn(),
            toggleFavorite: vi.fn(),
            createSavedSearch: vi.fn(),
            updateSavedSearch: vi.fn(),
            deleteSavedSearch: vi.fn(),
            createTag: vi.fn(),
            updateTag: vi.fn(),
            deleteTag: vi.fn(),
            assignTag: vi.fn(),
            clearRecent: vi.fn(),
            compareRecoveryPoints: vi.fn(),
            loadPreview: vi.fn(),
            renewPreview: vi.fn(),
            prepareDownload: vi.fn(),
            detachContent: vi.fn(),
          },
        })}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />
    );

    await user.type(
      screen.getByRole("searchbox", { name: /Search backup assets|搜索备份资产/ }),
      "term"
    );
    expect(setSearchDraft).toHaveBeenCalled();
  });

  it("opens an overlay portal, loads its collection, and restores trigger focus", async () => {
    setViewport(1440);
    const user = userEvent.setup();
    const loadOverlaySection = vi.fn();
    render(
      <BackupAssetsWorkspace
        controller={controller({
          actions: {
            ...controller().actions,
            loadOverlaySection,
          },
        })}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />
    );

    const trigger = screen.getByRole("button", { name: /Favorites.*0|收藏.*0/ });
    await user.click(trigger);
    expect(loadOverlaySection).toHaveBeenCalledWith("favorites");
    expect(screen.getByRole("dialog", { name: /Favorites|收藏/ })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(trigger).toHaveFocus();
  });

  it("opens a composite overlay ref without retaining a conflicting repository context", async () => {
    setViewport(1440);
    const user = userEvent.setup();
    const onRoutePatch = vi.fn();
    const ref = buildAssetRows(1)[0].ref;
    render(
      <BackupAssetsWorkspace
        controller={controller({
          overlays: {
            ...controller().overlays,
            favorites: {
              status: "ready",
              items: [
                {
                  id: "f".repeat(32),
                  ref,
                  label: "synthetic cross-context asset",
                  state: "active",
                  tombstoneReason: null,
                  version: 1,
                  createdAt: "2026-07-19T00:00:00Z",
                  updatedAt: "2026-07-19T00:00:00Z",
                },
              ],
              nextCursor: null,
            },
          },
        })}
        onRoutePatch={onRoutePatch}
        onReturnOverview={vi.fn()}
      />
    );

    await user.click(screen.getByRole("button", { name: /Favorites.*1|收藏.*1/ }));
    await user.click(screen.getByRole("button", { name: /Open asset|打开资产/ }));

    expect(onRoutePatch).toHaveBeenCalledWith({
      view: "browse",
      repositoryId: undefined,
      taskId: undefined,
      recoveryPointId: ref.recoveryPointId,
      parentEntryId: undefined,
      entryId: ref.entryId,
      scope: "current",
    });
  });

  it("renders the exact selected entry in the desktop inspector and emits reversible route patches", async () => {
    setViewport(1440);
    const user = userEvent.setup();
    const rows = buildAssetRows(2);
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      entryId: rows[0].ref.entryId,
    };
    const state = createInitialBackupAssetsState(route);
    state.result = {
      status: "ready",
      requestKey: "synthetic-results",
      generation: 1,
      rows,
      nextCursor: null,
      coverage: "complete",
      authoritativeEmpty: false,
    };
    const onRoutePatch = vi.fn();

    render(
      <BackupAssetsWorkspace
        controller={controller({
          state,
          recoveryPoints: {
            status: "ready",
            items: [{ status: "available", value: recoveryPoint }],
            nextCursor: null,
          },
          selectedRecoveryPoint: recoveryPoint,
          selectedEntry: { status: "ready", value: rows[0].asset },
        })}
        onRoutePatch={onRoutePatch}
        onReturnOverview={vi.fn()}
      />
    );

    expect(screen.getByRole("complementary", { name: /Asset inspector|资产检查器/ })).toHaveTextContent(
      rows[0].asset.name
    );
    await user.click(screen.getByRole("tab", { name: /Metadata|元数据/ }));
    expect(onRoutePatch).toHaveBeenCalledWith({ inspectorTab: "metadata" });
    await user.click(screen.getByRole("button", { name: /Next asset|下一个资产/ }));
    expect(onRoutePatch).toHaveBeenCalledWith({
      recoveryPointId: rows[1].ref.recoveryPointId,
      entryId: rows[1].ref.entryId,
    });
    await user.click(screen.getByRole("button", { name: /Close asset inspector|关闭资产检查器/ }));
    expect(onRoutePatch).toHaveBeenCalledWith({ entryId: undefined });
  });

  it("loads favorite membership for a manageable exact asset and waits for a complete list", async () => {
    setViewport(1440);
    const row = buildAssetRows(1)[0];
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      entryId: row.ref.entryId,
    };
    const state = createInitialBackupAssetsState(route);
    const loadOverlaySection = vi.fn();
    const actions = { ...controller().actions, loadOverlaySection };
    const buildController = (
      favorites: BackupAssetsController["overlays"]["favorites"]
    ): BackupAssetsController =>
      controller({
        state,
        selectedRecoveryPoint: recoveryPoint,
        selectedEntry: { status: "ready", value: row.asset },
        overlays: {
          ...controller().overlays,
          favorites,
        },
        actions,
      });
    const { rerender } = render(
      <BackupAssetsWorkspace
        controller={buildController({ status: "idle", items: [], nextCursor: null })}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />
    );

    await waitFor(() => expect(loadOverlaySection).toHaveBeenCalledWith("favorites"));
    expect(screen.queryByRole("button", { name: /Add favorite|添加收藏/ })).not.toBeInTheDocument();

    rerender(
      <BackupAssetsWorkspace
        controller={buildController({ status: "ready", items: [], nextCursor: "more-favorites" })}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />
    );
    expect(screen.queryByRole("button", { name: /Add favorite|添加收藏/ })).not.toBeInTheDocument();

    rerender(
      <BackupAssetsWorkspace
        controller={buildController({ status: "ready", items: [], nextCursor: null })}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />
    );
    expect(screen.getByRole("button", { name: /Add favorite|添加收藏/ })).toBeInTheDocument();
  });

  it("switches mobile results to a full-height inspector without compressing columns", () => {
    setViewport(390);
    const row = buildAssetRows(1)[0];
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      entryId: row.ref.entryId,
    };
    render(
      <BackupAssetsWorkspace
        controller={controller({
          state: createInitialBackupAssetsState(route),
          selectedRecoveryPoint: recoveryPoint,
          selectedEntry: { status: "ready", value: row.asset },
        })}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />
    );

    expect(screen.getByTestId("backup-assets-mobile-inspector")).toHaveTextContent(row.asset.name);
    expect(screen.queryByRole("region", { name: /Asset results|资产结果/ })).not.toBeInTheDocument();
  });

  it.each([
    { layout: "list" as const, containerRole: "listbox", itemRole: "option" },
    { layout: "grid" as const, containerRole: "grid", itemRole: "gridcell" },
  ])("restores the exact $layout result focus and virtual scroll after closing the mobile inspector", async ({
    layout,
    containerRole,
    itemRole,
  }) => {
    setViewport(390);
    const user = userEvent.setup();
    const rows = buildAssetRows(24);
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      layout,
    };
    const resultState = createInitialBackupAssetsState(route);
    resultState.result = {
      status: "ready",
      requestKey: "synthetic-mobile-results",
      generation: 1,
      rows,
      nextCursor: null,
      coverage: "complete",
      authoritativeEmpty: false,
    };
    const onRoutePatch = vi.fn();
    const renderWorkspace = (selected: boolean) => {
      const state = {
        ...resultState,
        route: selected ? { ...route, entryId: rows[2].ref.entryId } : route,
      };
      return (
        <BackupAssetsWorkspace
          controller={controller({
            state,
            selectedRecoveryPoint: recoveryPoint,
            selectedEntry: selected ? { status: "ready", value: rows[2].asset } : { status: "idle", value: null },
          })}
          onRoutePatch={onRoutePatch}
          onReturnOverview={vi.fn()}
        />
      );
    };
    const { rerender } = render(renderWorkspace(false));
    const resultContainer = await screen.findByRole(containerRole);
    resultContainer.scrollTop = 88;
    const target = screen.getByTitle(rows[2].asset.name).closest(`[role="${itemRole}"]`);
    expect(target).not.toBeNull();
    if (!(target instanceof HTMLElement)) return;

    await user.dblClick(target);
    expect(onRoutePatch).toHaveBeenCalledWith({ entryId: rows[2].ref.entryId });

    rerender(renderWorkspace(true));
    await waitFor(() => expect(screen.getByRole("heading", { name: rows[2].asset.name })).toHaveFocus());
    await user.click(screen.getByRole("button", { name: /Close asset inspector|关闭资产检查器/ }));
    rerender(renderWorkspace(false));

    const restoredResultContainer = await screen.findByRole(containerRole);
    await waitFor(() => expect(restoredResultContainer.scrollTop).toBe(88));
    const restoredTarget = screen.getByTitle(rows[2].asset.name).closest(`[role="${itemRole}"]`);
    await waitFor(() => expect(restoredTarget).toHaveFocus());
  });

  it("binds preview and download commands to exact server permissions and detaches on unmount", async () => {
    setViewport(1440);
    const user = userEvent.setup();
    const row = buildAssetRows(1)[0];
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      entryId: row.ref.entryId,
    };
    const loadPreview = vi.fn();
    const prepareDownload = vi.fn();
    const detachContent = vi.fn();
    const { unmount } = render(
      <BackupAssetsWorkspace
        controller={controller({
          state: createInitialBackupAssetsState(route),
          selectedRecoveryPoint: recoveryPoint,
          selectedEntry: { status: "ready", value: row.asset },
          actions: {
            ...controller().actions,
            loadPreview,
            prepareDownload,
            detachContent,
          },
        })}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />
    );

    await user.click(screen.getByRole("button", { name: /Load preview|加载预览/ }));
    await user.click(screen.getByRole("button", { name: /Prepare download|准备下载/ }));
    expect(loadPreview).toHaveBeenCalledWith(row.asset);
    expect(prepareDownload).toHaveBeenCalledWith(row.asset);
    unmount();
    expect(detachContent).toHaveBeenCalledTimes(1);
  });

  it("passes live browser connectivity to the archive fallback panel", async () => {
    setViewport(1440);
    archivePanelRenderMock.mockClear();
    Object.defineProperty(navigator, "onLine", { configurable: true, value: false });
    const user = userEvent.setup();
    const row = buildAssetRows(1)[0];
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: repository.id,
      recoveryPointId: recoveryPoint.id,
      entryId: row.ref.entryId,
    };
    render(
      <BackupAssetsWorkspace
        controller={controller({
          state: createInitialBackupAssetsState(route),
          selectedRecoveryPoint: recoveryPoint,
          selectedEntry: { status: "ready", value: row.asset },
        })}
        processingRuntime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        onRoutePatch={vi.fn()}
        onReturnOverview={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: /处理状态|Processing status/ }));
    await user.click(await screen.findByRole("button", { name: "Open archive browser" }));
    await screen.findByTestId("synthetic-archive-panel");
    expect(archivePanelRenderMock.mock.calls.at(-1)?.[0]).toMatchObject({ online: false });

    Object.defineProperty(navigator, "onLine", { configurable: true, value: true });
    await act(async () => {
      window.dispatchEvent(new Event("online"));
      await Promise.resolve();
    });
    await waitFor(() => expect(archivePanelRenderMock.mock.calls.at(-1)?.[0]).toMatchObject({ online: true }));
  });
});
