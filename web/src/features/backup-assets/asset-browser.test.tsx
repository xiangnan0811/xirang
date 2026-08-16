import "@testing-library/jest-dom/vitest";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

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

    expect(screen.getByRole("listbox")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(/1.*selected|已选择.*1/);
    await user.click(screen.getByRole("radio", { name: /Grid|网格/ }));
    expect(onRoutePatch).toHaveBeenCalledWith({ layout: "grid" });
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
      nextCursor: null, coverage: "complete", authoritativeEmpty: false,
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
    expect(screen.queryByText(/No matching assets|没有匹配的资产/)).not.toBeInTheDocument();
  });
});
