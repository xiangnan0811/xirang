import "@testing-library/jest-dom/vitest";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { buildAssetRows } from "./__tests__/test-utils";
import { AssetGrid } from "./asset-grid";
import { assetRefKey } from "./backup-assets-state";
import { runAxe } from "@/test/a11y-helpers";

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

  it("keeps selection and activation as sibling controls with pointer, Enter, and Space parity", async () => {
    const user = userEvent.setup();
    const rows = buildAssetRows(2);
    const onSelectionToggle = vi.fn();
    const onOpen = vi.fn();
    render(
      <AssetGrid
        rows={rows}
        selectedKeys={new Set()}
        activeKey={null}
        onActiveChange={vi.fn()}
        onSelectionToggle={onSelectionToggle}
        onOpen={onOpen}
      />
    );

    const cell = (await screen.findAllByRole("gridcell"))[1];
    const checkbox = screen.getAllByRole("checkbox")[1];
    const activation = screen.getByRole("button", { name: new RegExp(rows[1].asset.name) });
    expect(activation).not.toContainElement(checkbox);
    expect(cell).toContainElement(checkbox);
    expect(cell).toContainElement(activation);

    await user.click(activation);
    expect(onOpen).toHaveBeenCalledOnce();
    expect(onSelectionToggle).not.toHaveBeenCalled();

    activation.focus();
    await user.keyboard("{Enter}");
    await user.keyboard(" ");
    expect(onOpen).toHaveBeenCalledTimes(3);

    checkbox.focus();
    await user.keyboard(" ");
    expect(onSelectionToggle).toHaveBeenCalledOnce();
    expect(onSelectionToggle).toHaveBeenCalledWith(rows[1].ref);
    expect(onOpen).toHaveBeenCalledTimes(3);
  });

  it("keeps the spatial grid axe-clean with native selection and current activation state", async () => {
    const rows = buildAssetRows(2);
    const { container } = render(
      <AssetGrid
        rows={rows}
        selectedKeys={new Set([assetRefKey(rows[1].ref)])}
        activeKey={assetRefKey(rows[0].ref)}
        onActiveChange={vi.fn()}
        onSelectionToggle={vi.fn()}
        onOpen={vi.fn()}
      />,
    );

    const current = await screen.findByRole("button", { name: new RegExp(rows[0].asset.name) });
    const selected = screen.getByRole("checkbox", { name: new RegExp(rows[1].asset.name) });
    expect(current).toHaveAttribute("aria-current", "true");
    expect(selected).toBeChecked();
    expect(await runAxe(container)).toHaveNoViolations();
  });

  it("shows a retained version count only on multi-version search tiles", async () => {
    const [browse, singleBase] = buildAssetRows(2);
    const single = { ...singleBase, source: "search" as const, hitFields: ["name" as const], retainedVersionCount: 1 };
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
