import "@testing-library/jest-dom/vitest";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { runAxe } from "@/test/a11y-helpers";
import { buildAssetRows } from "./__tests__/test-utils";
import { AssetBrowser } from "./asset-browser";
import { assetRefKey, createInitialBackupAssetsState } from "./backup-assets-state";
import { defaultBackupAssetsRouteState } from "./backup-assets-route-state";

beforeAll(() => {
  Object.defineProperties(HTMLElement.prototype, {
    getBoundingClientRect: {
      configurable: true,
      value: () => ({
        x: 0, y: 0, top: 0, left: 0, right: 800, bottom: 480,
        width: 800, height: 480, toJSON: () => ({}),
      } as DOMRect),
    },
    offsetHeight: { configurable: true, get: () => 480 },
    offsetWidth: { configurable: true, get: () => 800 },
    clientHeight: { configurable: true, get: () => 480 },
    clientWidth: { configurable: true, get: () => 800 },
  });
});

describe("AssetBrowser", () => {
  it("renders a navigable directory breadcrumb without exposing a raw path", async () => {
    const rows = buildAssetRows(1);
    const directoryId = "c".repeat(64);
    rows[0].asset.breadcrumb = [{ name: "ROW_ONLY", ref: { recoveryPointId: rows[0].ref.recoveryPointId, entryId: "d".repeat(64) } }];
    const state = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"), recoveryPointId: rows[0].ref.recoveryPointId, parentEntryId: directoryId,
    });
    state.result = {
      status: "ready", requestKey: "breadcrumb", generation: 1, rows, nextCursor: null,
      coverage: "complete", authoritativeEmpty: false,
      directory: {
        current: { name: "配置", ref: { recoveryPointId: rows[0].ref.recoveryPointId, entryId: directoryId } },
        parent: null,
        breadcrumb: [{ name: "配置", ref: { recoveryPointId: rows[0].ref.recoveryPointId, entryId: directoryId } }],
      },
    };
    const onRoutePatch = vi.fn();
    const { container } = render(<AssetBrowser state={state} onRoutePatch={onRoutePatch} onSearch={vi.fn()} onSearchDraftChange={vi.fn()}
      onToggleSelection={vi.fn()} onClearSelection={vi.fn()} onOpen={vi.fn()} onLoadMore={vi.fn()} />);
    const breadcrumb = screen.getByRole("navigation", { name: /Directory breadcrumb|目录位置/ });
    expect(breadcrumb).toHaveTextContent("配置");
    expect(breadcrumb).not.toHaveTextContent("/");
    expect(breadcrumb).not.toHaveTextContent("ROW_ONLY");
    await userEvent.click(screen.getByRole("button", { name: /Up one directory|返回上级目录/ }));
    expect(onRoutePatch).toHaveBeenLastCalledWith({ parentEntryId: undefined, entryId: undefined });
    await userEvent.click(screen.getByRole("button", { name: /Root|根目录/ }));
    expect(onRoutePatch).toHaveBeenLastCalledWith({ parentEntryId: undefined, entryId: undefined });
    expect(screen.getByRole("button", { name: "配置" })).toHaveAttribute("aria-current", "page");
    expect(await runAxe(container)).toHaveNoViolations();
  });

  it("keeps Up and opaque ancestor navigation available in an empty directory", async () => {
    const recoveryPointId = "b".repeat(32);
    const ancestorId = "c".repeat(64);
    const currentId = "d".repeat(64);
    const state = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"), recoveryPointId, parentEntryId: currentId,
    });
    state.result = {
      status: "ready", requestKey: "empty-directory", generation: 1, rows: [], nextCursor: null,
      coverage: "complete", authoritativeEmpty: true,
      directory: {
        current: { name: "empty", ref: { recoveryPointId, entryId: currentId } },
        parent: { recoveryPointId, entryId: ancestorId },
        breadcrumb: [
          { name: "ancestor", ref: { recoveryPointId, entryId: ancestorId } },
          { name: "empty", ref: { recoveryPointId, entryId: currentId } },
        ],
      },
    };
    const onRoutePatch = vi.fn();
    render(<AssetBrowser state={state} onRoutePatch={onRoutePatch} onSearch={vi.fn()} onSearchDraftChange={vi.fn()}
      onToggleSelection={vi.fn()} onClearSelection={vi.fn()} onOpen={vi.fn()} onLoadMore={vi.fn()} />);

    await userEvent.click(screen.getByRole("button", { name: /Up one directory|返回上级目录/ }));
    expect(onRoutePatch).toHaveBeenLastCalledWith({ parentEntryId: ancestorId, entryId: undefined });
    await userEvent.click(screen.getByRole("button", { name: "ancestor" }));
    expect(onRoutePatch).toHaveBeenLastCalledWith({ parentEntryId: ancestorId, entryId: undefined });
  });

  it("does not navigate or detach state when the current breadcrumb is activated", async () => {
    const recoveryPointId = "b".repeat(32);
    const currentId = "c".repeat(64);
    const state = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"), recoveryPointId, parentEntryId: currentId,
    });
    state.result = {
      ...state.result, status: "ready", requestKey: "current-directory", generation: 1,
      coverage: "complete", authoritativeEmpty: true,
      directory: {
        current: { name: "current", ref: { recoveryPointId, entryId: currentId } },
        parent: null,
        breadcrumb: [{ name: "current", ref: { recoveryPointId, entryId: currentId } }],
      },
    };
    const onNavigateDirectory = vi.fn();
    render(
      <AssetBrowser
        state={state}
        onRoutePatch={vi.fn()}
        onNavigateDirectory={onNavigateDirectory}
        onSearch={vi.fn()}
        onSearchDraftChange={vi.fn()}
        onToggleSelection={vi.fn()}
        onClearSelection={vi.fn()}
        onOpen={vi.fn()}
        onLoadMore={vi.fn()}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "current" }));

    expect(onNavigateDirectory).not.toHaveBeenCalled();
  });

  it("disables Up at root and restores focus to the root crumb after navigating there", async () => {
    const recoveryPointId = "b".repeat(32);
    const currentId = "c".repeat(64);
    const nested = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"), recoveryPointId, parentEntryId: currentId,
    });
    nested.result = {
      ...nested.result, status: "ready", requestKey: "nested", generation: 1,
      coverage: "complete", authoritativeEmpty: true,
      directory: {
        current: { name: "nested", ref: { recoveryPointId, entryId: currentId } },
        parent: null,
        breadcrumb: [{ name: "nested", ref: { recoveryPointId, entryId: currentId } }],
      },
    };
    const props = { onRoutePatch: vi.fn(), onSearch: vi.fn(), onSearchDraftChange: vi.fn(), onToggleSelection: vi.fn(),
      onClearSelection: vi.fn(), onOpen: vi.fn(), onLoadMore: vi.fn() };
    const rendered = render(<AssetBrowser state={nested} {...props} />);
    await userEvent.click(screen.getByRole("button", { name: /Up one directory|返回上级目录/ }));

    const root = createInitialBackupAssetsState({ ...defaultBackupAssetsRouteState("data"), recoveryPointId });
    root.result = {
      ...root.result, status: "ready", requestKey: "root", generation: 2,
      coverage: "complete", authoritativeEmpty: true,
      directory: { current: null, parent: null, breadcrumb: [] },
    };
    rendered.rerender(<AssetBrowser state={root} {...props} />);

    expect(screen.getByRole("button", { name: /Up one directory|返回上级目录/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: /Root directory|根目录/ })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("button", { name: /Root directory|根目录/ })).toHaveFocus();
  });

  it("reserves a full toolbar row for search before secondary controls", () => {
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: "a".repeat(32),
      recoveryPointId: "b".repeat(32),
    };
    const state = createInitialBackupAssetsState(route);
    render(
      <AssetBrowser
        state={state}
        onRoutePatch={vi.fn()}
        onSearch={vi.fn()}
        onSearchDraftChange={vi.fn()}
        onToggleSelection={vi.fn()}
        onClearSelection={vi.fn()}
        onOpen={vi.fn()}
        onLoadMore={vi.fn()}
      />
    );

    const search = screen.getByRole("search");
    expect(search.parentElement).toHaveClass("grid");
    expect(search).toHaveClass("col-span-full");
    expect(screen.getByRole("searchbox").parentElement).toHaveClass("min-w-0");
    expect(screen.getByRole("combobox", { name: /Search scope|搜索范围/ }).parentElement).toHaveClass("w-32");
  });

  it("keeps every primary browser control at least 44px tall on touch layouts", async () => {
    const rows = buildAssetRows(1);
    const state = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: "a".repeat(32),
      recoveryPointId: "b".repeat(32),
    });
    state.result = {
      status: "ready",
      requestKey: "touch-targets",
      generation: 1,
      rows,
      nextCursor: "next-page",
      coverage: "complete",
      authoritativeEmpty: false,
      directory: null,
    };
    render(
      <AssetBrowser
        state={state}
        onRoutePatch={vi.fn()}
        onSearch={vi.fn()}
        onSearchDraftChange={vi.fn()}
        onToggleSelection={vi.fn()}
        onClearSelection={vi.fn()}
        onOpen={vi.fn()}
        onLoadMore={vi.fn()}
      />,
    );

    const touchTargets = [
      screen.getByRole("button", { name: /Up one directory|返回上级目录/ }),
      screen.getByRole("button", { name: /^(Root directory|根目录)$/ }),
      screen.getByRole("searchbox"),
      screen.getByRole("combobox", { name: /Search scope|搜索范围/ }),
      screen.getByRole("button", { name: /^(Search|搜索)$/ }),
      screen.getByRole("combobox", { name: /Sort|排序/ }),
      ...screen.getAllByRole("radio"),
      screen.getByRole("button", { name: /Load more|加载更多/ }),
    ];
    for (const target of touchTargets) expect(target).toHaveClass("min-h-11", "touch-target");
    expect(screen.getByRole("navigation", { name: /Directory breadcrumb|目录位置/ })).toHaveClass("lg:h-10");
    expect(screen.getByRole("radiogroup", { name: /Layout|布局/ })).toHaveClass("p-0");
    expect(screen.getByRole("radiogroup", { name: /Layout|布局/ })).not.toHaveClass("lg:p-1");
  });

  it("shares selection and active state while switching list and grid", async () => {
    const user = userEvent.setup();
    const rows = buildAssetRows(12);
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: "a".repeat(32),
      recoveryPointId: "b".repeat(32),
    };
    const state = createInitialBackupAssetsState(route);
    state.result = {
      status: "ready",
      requestKey: "synthetic",
      generation: 1,
      rows,
      nextCursor: null,
      coverage: "complete",
      authoritativeEmpty: false,
      directory: null,
    };
    state.selection = new Map([[assetRefKey(rows[0].ref), rows[0].ref]]);
    const onRoutePatch = vi.fn();
    render(
      <AssetBrowser
        state={state}
        onRoutePatch={onRoutePatch}
        onSearch={vi.fn()}
        onSearchDraftChange={vi.fn()}
        onToggleSelection={vi.fn()}
        onClearSelection={vi.fn()}
        onOpen={vi.fn()}
        onLoadMore={vi.fn()}
      />
    );

    expect(screen.getByRole("list", { name: /File list|文件列表/ })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(/1.*selected|已选择.*1/);
    await user.click(screen.getByRole("radio", { name: /Grid|网格/ }));
    expect(onRoutePatch).toHaveBeenCalledWith({ layout: "grid" });
  });

  it("moves roving focus without claiming a current file before preview activation", async () => {
    const user = userEvent.setup();
    const rows = buildAssetRows(3);
    const state = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: "a".repeat(32),
      recoveryPointId: "b".repeat(32),
    });
    state.result = {
      status: "ready",
      requestKey: "focus-without-preview",
      generation: 1,
      rows,
      nextCursor: null,
      coverage: "complete",
      authoritativeEmpty: false,
      directory: null,
    };
    const onOpen = vi.fn();
    render(
      <AssetBrowser
        state={state}
        onRoutePatch={vi.fn()}
        onSearch={vi.fn()}
        onSearchDraftChange={vi.fn()}
        onToggleSelection={vi.fn()}
        onClearSelection={vi.fn()}
        onOpen={onOpen}
        onLoadMore={vi.fn()}
      />,
    );

    const first = await screen.findByRole("button", { name: new RegExp(rows[0].asset.name) });
    const second = screen.getByRole("button", { name: new RegExp(rows[1].asset.name) });
    first.focus();
    await user.keyboard("{ArrowDown}");

    expect(second).toHaveFocus();
    expect(first).not.toHaveAttribute("aria-current");
    expect(second).not.toHaveAttribute("aria-current");
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("delegates the bulk export command only through the explicit authorization prop", async () => {
    const user = userEvent.setup();
    const rows = buildAssetRows(1);
    const state = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: "a".repeat(32),
      recoveryPointId: "b".repeat(32),
    });
    state.result = {
      status: "ready",
      requestKey: "explicit-export",
      generation: 1,
      rows,
      nextCursor: null,
      coverage: "complete",
      authoritativeEmpty: false,
      directory: null,
    };
    state.selection = new Map([[assetRefKey(rows[0].ref), rows[0].ref]]);
    const onExport = vi.fn();
    render(
      <AssetBrowser
        state={state}
        canExport
        onExport={onExport}
        onRoutePatch={vi.fn()}
        onSearch={vi.fn()}
        onSearchDraftChange={vi.fn()}
        onToggleSelection={vi.fn()}
        onClearSelection={vi.fn()}
        onOpen={vi.fn()}
        onLoadMore={vi.fn()}
      />
    );

    await user.click(screen.getByRole("button", { name: /Export selected|导出所选/ }));

    expect(onExport).toHaveBeenCalledOnce();
  });

  it("keeps recovery stateless and delegates the exact selected refs", async () => {
    const user = userEvent.setup();
    const rows = buildAssetRows(2);
    const state = createInitialBackupAssetsState({
      ...defaultBackupAssetsRouteState("data"),
      repositoryId: "a".repeat(32),
      recoveryPointId: "b".repeat(32),
    });
    state.result = {
      status: "ready", requestKey: "explicit-recovery", generation: 1, rows,
      nextCursor: null, coverage: "complete", authoritativeEmpty: false, directory: null,
    };
    state.selection = new Map(rows.map((row) => [assetRefKey(row.ref), row.ref]));
    const onRecover = vi.fn();
    render(
      <AssetBrowser
        state={state}
        canRecover
        onRecover={onRecover}
        onRoutePatch={vi.fn()}
        onSearch={vi.fn()}
        onSearchDraftChange={vi.fn()}
        onToggleSelection={vi.fn()}
        onClearSelection={vi.fn()}
        onOpen={vi.fn()}
        onLoadMore={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: /Recover selected|恢复所选/ }));
    expect(onRecover).toHaveBeenCalledOnce();
  });

  it("does not present partial zero results as an authoritative empty state", () => {
    const route = {
      ...defaultBackupAssetsRouteState("data"),
      view: "search" as const,
      sort: "relevance" as const,
      direction: "desc" as const,
    };
    const state = createInitialBackupAssetsState(route);
    state.result = {
      status: "ready",
      requestKey: "partial",
      generation: 1,
      rows: [],
      nextCursor: null,
      coverage: "partial",
      authoritativeEmpty: false,
      directory: null,
    };
    render(
      <AssetBrowser
        state={state}
        onRoutePatch={vi.fn()}
        onSearch={vi.fn()}
        onSearchDraftChange={vi.fn()}
        onToggleSelection={vi.fn()}
        onClearSelection={vi.fn()}
        onOpen={vi.fn()}
        onLoadMore={vi.fn()}
      />
    );

    expect(screen.getByRole("status")).toHaveTextContent(/partial|部分|不完整/i);
    expect(screen.queryByText(/No matching files|没有匹配的文件/)).not.toBeInTheDocument();
  });

  it("announces browser loading and failure as scoped live states", () => {
    const state = createInitialBackupAssetsState(defaultBackupAssetsRouteState("data"));
    state.result = {
      ...state.result,
      status: "loading",
      requestKey: "live-state",
    };
    const props = {
      onRoutePatch: vi.fn(),
      onSearch: vi.fn(),
      onSearchDraftChange: vi.fn(),
      onToggleSelection: vi.fn(),
      onClearSelection: vi.fn(),
      onOpen: vi.fn(),
      onLoadMore: vi.fn(),
    };
    const rendered = render(<AssetBrowser state={state} {...props} />);

    expect(screen.getByRole("status")).toHaveAttribute("aria-live", "polite");
    expect(screen.getByRole("status")).toHaveAttribute("aria-busy", "true");

    state.result = { ...state.result, status: "failed" };
    rendered.rerender(<AssetBrowser state={state} {...props} />);
    expect(screen.getByRole("alert")).toBeInTheDocument();
  });
});
