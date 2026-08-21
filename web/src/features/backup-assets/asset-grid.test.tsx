import "@testing-library/jest-dom/vitest";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { buildAssetRows } from "./__tests__/test-utils";
import { AssetGrid } from "./asset-grid";

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

describe("AssetGrid", () => {
  it("virtualizes 1000 assets and keeps stable tile dimensions", async () => {
    render(
      <AssetGrid
        rows={buildAssetRows(1000)}
        selectedKeys={new Set()}
        activeKey={null}
        onActiveChange={vi.fn()}
        onSelectionToggle={vi.fn()}
        onOpen={vi.fn()}
      />
    );

    await waitFor(() => expect(screen.getAllByRole("gridcell").length).toBeGreaterThan(0));
    expect(screen.getAllByRole("gridcell").length).toBeLessThan(80);
    expect(screen.getAllByRole("gridcell")[0]).toHaveClass("h-36");
  });

  it("uses the gridcell itself for pointer selection without nesting a checkbox", async () => {
    const user = userEvent.setup();
    const rows = buildAssetRows(2);
    const onSelectionToggle = vi.fn();
    render(
      <AssetGrid
        rows={rows}
        selectedKeys={new Set()}
        activeKey={null}
        onActiveChange={vi.fn()}
        onSelectionToggle={onSelectionToggle}
        onOpen={vi.fn()}
      />
    );

    const cell = (await screen.findAllByRole("gridcell"))[0];
    expect(cell.querySelector('[role="checkbox"]')).toBeNull();
    await user.click(cell);
    expect(onSelectionToggle).toHaveBeenCalledWith(rows[0].ref);
  });

  it("shows a retained version count only on multi-version search tiles", async () => {
    const browse = buildAssetRows(1)[0];
    const single = { ...browse, source: "search" as const, hitFields: ["name" as const], retainedVersionCount: 1 };
    const multi = {
      ...browse,
      ref: { ...browse.ref, entryId: "2".repeat(64) },
      source: "search" as const,
      hitFields: ["name" as const],
      retainedVersionCount: 2,
      asset: { ...browse.asset, name: "multi-version.yaml", ref: { ...browse.ref, entryId: "2".repeat(64) } },
    };
    render(
      <AssetGrid
        rows={[browse, single, multi]}
        selectedKeys={new Set()}
        activeKey={null}
        onActiveChange={vi.fn()}
        onSelectionToggle={vi.fn()}
        onOpen={vi.fn()}
      />
    );

    const cells = await screen.findAllByRole("gridcell");
    expect(cells[0]).not.toHaveTextContent("个保留版本");
    expect(cells[1]).not.toHaveTextContent("个保留版本");
    expect(cells[2]).toHaveTextContent("2 个保留版本");
  });
});
